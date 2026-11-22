package workstationlan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/crypto/ssh"
)

type testGuest struct {
	key         ssh.PublicKey
	mu          sync.Mutex
	enrolled    map[string]bool
	connections chan struct{}
}

func (g *testGuest) ChangeDevice(ctx context.Context, k ssh.PublicKey, add bool) (GuestInfo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if add {
		g.enrolled[idFor(k)] = true
	} else {
		delete(g.enrolled, idFor(k))
	}
	return GuestInfo{User: "tester", Public: wireKey(g.key)}, nil
}

func (g *testGuest) Dial(ctx context.Context) (net.Conn, error) {
	ready, echo := net.Pipe()
	go func() {
		defer echo.Close()
		var buf [256]byte
		for {
			n, e := echo.Read(buf[:])
			if n > 0 {
				if _, err := echo.Write(buf[:n]); err != nil {
					return
				}
			}
			if e != nil {
				return
			}
		}
	}()
	select {
	case g.connections <- struct{}{}:
	default:
	}
	return ready, nil
}

func TestDeviceLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	serverDir, clientDir := t.TempDir(), t.TempDir()
	guestKey, e := initIdentity(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	guest := &testGuest{key: guestKey.PublicKey(), enrolled: map[string]bool{}, connections: make(chan struct{}, 2)}
	s, e := NewServer(serverDir, "test server", guest)
	if e != nil {
		t.Fatal(e)
	}
	c, e := NewClient(clientDir, "CageLAN", "test client")
	if e != nil {
		t.Fatal(e)
	}
	address := "127.0.0.2"
	if runtime.GOOS == "darwin" {
		// Darwin only configures 127.0.0.1 by default.
		address = "127.0.0.1"
	}
	listener, e := net.Listen("tcp4", net.JoinHostPort(address, fmt.Sprint(ServicePort)))
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	go s.Serve(ctx, listener, net.IP.IsLoopback)
	peer := s.Self
	peer.Address = listener.Addr().String()
	c.seen[peer.ID] = peer
	if untrusted, e := dialServer(ctx, peer, c.signer, "workstation"); e == nil {
		untrusted.Close()
		t.Fatal("unapproved client connected")
	}
	a, e := c.Request(ctx, peer.ID)
	if e != nil || a.State != "pending" {
		t.Fatalf("request %v %v", a.State, e)
	}
	req, _, _ := s.Lists()
	if len(req) != 1 || req[0].Code != a.Code || req[0].Name != c.Name {
		t.Fatal("mismatched description or comparison number")
	}
	if e = s.Decide(ctx, req[0].ID, "other ticket", "approve"); e == nil {
		t.Fatal("stale approval accepted")
	}
	if e = s.Decide(ctx, req[0].ID, a.Ticket, "deny"); e != nil {
		t.Fatal(e)
	}
	rows, e := c.Rows(ctx)
	if e != nil || len(rows) != 1 || rows[0].State != "denied" {
		t.Fatalf("denial not delivered: %v", e)
	}
	a, e = c.Request(ctx, peer.ID)
	if e != nil || a.State != "pending" {
		t.Fatal("could not request again", e)
	}
	if e = s.Decide(ctx, req[0].ID, a.Ticket, "approve"); e != nil {
		t.Fatal(e)
	}
	rows, e = c.Rows(ctx)
	if e != nil || len(rows) != 1 || rows[0].State != "approved" {
		t.Fatal("approval was not remembered", e)
	}
	if e = c.Select(peer.ID); e != nil {
		t.Fatal(e)
	}
	fresh, e := NewClient(clientDir, "Moved CageLAN", "test client")
	if e != nil {
		t.Fatal(e)
	}
	if fresh.Last() != peer.ID {
		t.Fatal("lost selected server on restart")
	}
	rows, e = fresh.Rows(ctx)
	if e != nil || len(rows) != 1 || rows[0].State != "approved" {
		t.Fatalf("saved server could not reconnect: %v %v", rows, e)
	}
	again, e := NewServer(serverDir, "test server", guest)
	if e != nil {
		t.Fatal(e)
	}
	if again.Self.ID != s.Self.ID || !again.trusted(req[0].ID) {
		t.Fatal("lost server identity or trust on restart")
	}
	saved, ok := fresh.Saved(peer.ID)
	if !ok || saved.DesktopPort == 0 {
		t.Fatal("missing persistent workbench profile")
	}
	conf, e := os.ReadFile(filepath.Join(fresh.Profile(peer.ID), "ssh.config"))
	if e != nil || !strings.Contains(string(conf), "StrictHostKeyChecking yes") ||
		!strings.Contains(string(conf), shellPath(fresh.Helper)) {
		t.Fatal("guest pin missing", e)
	}
	sc, e := dialServer(ctx, peer, fresh.signer, "workstation")
	if e != nil {
		t.Fatal(e)
	}
	defer sc.Close()
	ch, rs, e := sc.OpenChannel(tunnelChannel, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer ch.Close()
	go ssh.DiscardRequests(rs)
	if _, e = ch.Write([]byte("pair")); e != nil {
		t.Fatal(e)
	}
	out := make([]byte, 4)
	if _, e = io.ReadFull(ch, out); e != nil || string(out) != "pair" {
		t.Fatal("tunnel echo failed", e)
	}
	if e = s.Decide(ctx, req[0].ID, "", "revoke"); e != nil {
		t.Fatal(e)
	}
	if _, e = ch.Read(out); e == nil {
		t.Fatal("revoked active connection survived")
	}
	if untrusted, e := dialServer(ctx, peer, fresh.signer, "workstation"); e == nil {
		untrusted.Close()
		t.Fatal("revoked client reconnected")
	}
	guest.mu.Lock()
	still := len(guest.enrolled)
	guest.mu.Unlock()
	if still != 0 {
		t.Fatal("guest enrollment persisted after revocation")
	}
}

func TestPanelLocalApproval(t *testing.T) {
	k, e := initIdentity(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	g := &testGuest{key: k.PublicKey(), enrolled: map[string]bool{}}
	s, e := NewServer(t.TempDir(), "host", g)
	if e != nil {
		t.Fatal(e)
	}
	r := s.response(Peer{ID: idFor(k.PublicKey()), Public: wireKey(k.PublicKey()), Address: "127.0.0.1"}, query{Action: "request", Name: "workstation"})
	ctrl := Control{Address: "127.0.0.1:33244", Token: randomID(32)}
	panel := Panel{Host: s}
	body, _ := json.Marshal(action{ID: idFor(k.PublicKey()), Ticket: r.Ticket})
	for _, test := range []struct {
		host, origin, token string
		want                int
	}{
		{ctrl.Address, "http://" + ctrl.Address, "", 403},
		{"another.local", "http://" + ctrl.Address, ctrl.Token, 403},
		{ctrl.Address, "http://another.local", ctrl.Token, 403},
		{ctrl.Address, "http://" + ctrl.Address, ctrl.Token, 200},
	} {
		req := httptest.NewRequest(http.MethodPost, "http://"+test.host+"/api/approve", bytes.NewReader(body))
		req.Header.Set("Origin", test.origin)
		req.Header.Set("Authorization", "Bearer "+test.token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		panel.Handler(ctrl).ServeHTTP(w, req)
		if w.Code != test.want {
			t.Fatalf("unexpected management response %d, want %d", w.Code, test.want)
		}
	}
}

func TestDiscoveryIdentityAndExpiry(t *testing.T) {
	k, e := initIdentity(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	s, e := NewServer(t.TempDir(), "host", &testGuest{key: k.PublicKey()})
	if e != nil {
		t.Fatal(e)
	}
	var q dns.Msg
	q.SetQuestion(serviceName, dns.TypePTR)
	ip := net.IPv4(192, 168, 71, 20)
	response := announce(link{ip: ip}, s.Self, ServicePort, &q)
	peer, ok := advertised(response, ip)
	if !ok || peer.ID != s.Self.ID || peer.Address != fmt.Sprintf("192.168.71.20:%d", ServicePort) {
		t.Fatal("discovery lost server identity")
	}
	p := Peer{ID: idFor(k.PublicKey()), Public: wireKey(k.PublicKey()), Address: "127.0.0.1"}
	a := s.response(p, query{Action: "request", Name: "client"})
	r := s.pending[p.ID]
	r.Expires = time.Now().Add(-time.Second)
	s.pending[p.ID] = r
	if e = s.Decide(context.Background(), p.ID, a.Ticket, "approve"); e == nil {
		t.Fatal("expired ticket accepted")
	}
	requests, _, _ := s.Lists()
	if len(requests) != 0 {
		t.Fatal("expired requests listed")
	}
}
