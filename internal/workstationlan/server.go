package workstationlan

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const pairChannel = "cage-pair-v1"
const tunnelChannel = "cage-stream-v1"

type Request struct {
	Peer
	Ticket  string    `json:"ticket"`
	Code    string    `json:"code"`
	State   string    `json:"state"`
	Expires time.Time `json:"expires"`
}
type query struct{ Action, Name, Ticket string }
type answer struct {
	State  string    `json:"state"`
	Ticket string    `json:"ticket,omitempty"`
	Code   string    `json:"code,omitempty"`
	Guest  GuestInfo `json:"guest,omitempty"`
}
type trust struct {
	Device
	Guest GuestInfo `json:"guest"`
}

type Server struct {
	Self     Peer
	signer   ssh.Signer
	guest    Guest
	store    string
	admin    sync.Mutex
	mu       sync.Mutex
	paired   map[string]trust
	pending  map[string]Request
	sessions map[string]map[net.Conn]bool
	slots    chan struct{}
	Network  map[string]string
}

func NewServer(dir, name string, guest Guest) (*Server, error) {
	key, e := initIdentity(dir)
	if e != nil {
		return nil, e
	}
	s := &Server{Self: Peer{ID: idFor(key.PublicKey()), Name: cleanName(name), Public: wireKey(key.PublicKey())}, signer: key, guest: guest, store: filepath.Join(dir, "devices.json"), paired: map[string]trust{}, pending: map[string]Request{}, sessions: map[string]map[net.Conn]bool{}, slots: make(chan struct{}, 64), Network: map[string]string{}}
	if e = readJSON(s.store, &s.paired); e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if s.paired == nil {
		return nil, errors.New("设备记录损坏")
	}
	return s, nil
}
func codeFor(server, client, ticket string) string {
	hash := sha256.Sum256([]byte("Cage LAN pairing v1\n" + server + "\n" + client + "\n" + ticket))
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(hash[:4])%1000000)
}
func (s *Server) cleanup() {
	now := time.Now()
	for k, r := range s.pending {
		if !now.Before(r.Expires) {
			delete(s.pending, k)
		}
	}
}
func (s *Server) trusted(id string) bool { r, ok := s.paired[id]; return ok && !r.Disabled }
func (s *Server) response(p Peer, q query) answer {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	if s.trusted(p.ID) {
		return answer{State: "approved", Guest: s.paired[p.ID].Guest}
	}
	r, ok := s.pending[p.ID]
	if q.Action == "status" {
		if !ok || q.Ticket != r.Ticket {
			return answer{State: "unpaired"}
		}
	} else if q.Action == "request" {
		if !ok || (r.State == "denied" && q.Ticket != r.Ticket) {
			count := 0
			for _, v := range s.pending {
				if v.Address == p.Address {
					count++
				}
			}
			if len(s.pending) >= 64 || count >= 6 {
				return answer{State: "busy"}
			}
			p.Name = cleanName(q.Name)
			ticket := randomID(20)
			r = Request{Peer: p, Ticket: ticket, Code: codeFor(s.Self.ID, p.ID, ticket), State: "pending", Expires: time.Now().Add(4 * time.Minute)}
			s.pending[p.ID] = r
		}
	} else {
		return answer{State: "invalid"}
	}
	return answer{State: r.State, Ticket: r.Ticket, Code: r.Code}
}
func (s *Server) Lists() ([]Request, []Device, map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	req := []Request{}
	dev := []Device{}
	network := map[string]string{}
	for _, r := range s.pending {
		if r.State == "pending" {
			req = append(req, r)
		}
	}
	for _, d := range s.paired {
		dev = append(dev, d.Device)
	}
	for k, v := range s.Network {
		network[k] = v
	}
	sort.Slice(req, func(i, j int) bool { return req[i].Expires.Before(req[j].Expires) })
	sort.Slice(dev, func(i, j int) bool { return dev[i].Name < dev[j].Name })
	return req, dev, network
}

// Local administrator actions are serialized. Persist a revocation before closing existing transports.
func (s *Server) Decide(ctx context.Context, id, ticket, action string) error {
	s.admin.Lock()
	defer s.admin.Unlock()
	if action == "revoke" {
		return s.revoke(ctx, id)
	}
	s.mu.Lock()
	s.cleanup()
	r, ok := s.pending[id]
	if !ok || r.State != "pending" || r.Ticket != ticket {
		s.mu.Unlock()
		return errors.New("申请已更新，请刷新")
	}
	if action == "deny" {
		r.State = "denied"
		s.pending[id] = r
		s.mu.Unlock()
		return nil
	}
	if action != "approve" {
		s.mu.Unlock()
		return errors.New("无法识别该操作")
	}
	s.mu.Unlock()
	k, e := decodeKey(r.Public)
	if e != nil {
		return e
	}
	guest, e := s.guest.ChangeDevice(ctx, k, true)
	if e != nil {
		return e
	}
	s.mu.Lock()
	previous, was := s.paired[id]
	s.paired[id] = trust{Device: Device{Peer: r.Peer}, Guest: guest}
	if e = writeJSON(s.store, s.paired); e != nil {
		if was {
			s.paired[id] = previous
		} else {
			delete(s.paired, id)
		}
	} else {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if e != nil {
		_, _ = s.guest.ChangeDevice(context.Background(), k, false)
	}
	return e
}
func (s *Server) revoke(ctx context.Context, id string) error {
	s.mu.Lock()
	d, ok := s.paired[id]
	if !ok {
		s.mu.Unlock()
		return errors.New("设备已移除")
	}
	old := d
	d.Disabled = true
	s.paired[id] = d
	if e := writeJSON(s.store, s.paired); e != nil {
		s.paired[id] = old
		s.mu.Unlock()
		return e
	}
	for c := range s.sessions[id] {
		c.Close()
	}
	delete(s.pending, id)
	s.mu.Unlock()
	k, e := decodeKey(d.Public)
	if e == nil {
		_, e = s.guest.ChangeDevice(ctx, k, false)
	}
	if e != nil {
		return errors.New("接入已撤销。客机就绪后可重试清理此设备")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.paired, id)
	if e = writeJSON(s.store, s.paired); e != nil {
		s.paired[id] = d
	}
	return e
}
func (s *Server) Serve(ctx context.Context, listener net.Listener, allowed func(net.IP) bool) {
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	defer listener.Close()
	for {
		c, e := listener.Accept()
		if e != nil {
			return
		}
		ip := c.RemoteAddr().(*net.TCPAddr).IP
		if !allowed(ip) {
			c.Close()
			continue
		}
		select {
		case s.slots <- struct{}{}:
			go func() { defer func() { <-s.slots }(); s.connection(ctx, c, ip.String()) }()
		default:
			c.Close()
		}
	}
}
func (s *Server) connection(ctx context.Context, raw net.Conn, address string) {
	defer raw.Close()
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	cfg := &ssh.ServerConfig{MaxAuthTries: 2, PublicKeyCallback: func(m ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if k.Type() != ssh.KeyAlgoED25519 {
			return nil, errors.New("设备身份无效")
		}
		id := idFor(k)
		s.mu.Lock()
		ok := s.trusted(id)
		s.mu.Unlock()
		if m.User() != "pair" && !(m.User() == "workstation" && ok) {
			return nil, errors.New("尚未批准连接")
		}
		return &ssh.Permissions{Extensions: map[string]string{"device": id, "public": wireKey(k)}}, nil
	}}
	cfg.AddHostKey(s.signer)
	_ = raw.SetDeadline(time.Now().Add(8 * time.Second))
	c, channels, requests, e := ssh.NewServerConn(raw, cfg)
	if e != nil {
		return
	}
	defer c.Close()
	_ = raw.SetDeadline(time.Now().Add(12 * time.Second))
	go ssh.DiscardRequests(requests)
	id := c.Permissions.Extensions["device"]
	ch, ok := <-channels
	if !ok {
		return
	}
	if c.User() == "pair" && ch.ChannelType() == pairChannel && len(ch.ExtraData()) <= 1024 {
		var q query
		if json.Unmarshal(ch.ExtraData(), &q) != nil {
			return
		}
		stream, r, e := ch.Accept()
		if e != nil {
			return
		}
		go ssh.DiscardRequests(r)
		reply := s.response(Peer{ID: id, Public: c.Permissions.Extensions["public"], Address: address}, q)
		_ = json.NewEncoder(stream).Encode(reply)
		stream.Close()
		return
	}
	if c.User() == "workstation" && ch.ChannelType() == catalogChannel {
		s.receiveCatalog(id, ch)
		return
	}
	if c.User() != "workstation" || ch.ChannelType() != tunnelChannel || len(ch.ExtraData()) != 0 {
		_ = ch.Reject(ssh.Prohibited, "无此服务")
		return
	}
	s.mu.Lock()
	if !s.trusted(id) {
		s.mu.Unlock()
		_ = ch.Reject(ssh.Prohibited, "设备已撤销")
		return
	}
	if s.sessions[id] == nil {
		s.sessions[id] = map[net.Conn]bool{}
	}
	s.sessions[id][raw] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessions[id], raw)
		if len(s.sessions[id]) == 0 {
			delete(s.sessions, id)
		}
		s.mu.Unlock()
	}()
	target, e := s.guest.Dial(ctx)
	if e != nil {
		_ = ch.Reject(ssh.ConnectionFailed, "Cage 客机暂时不可用")
		return
	}
	defer target.Close()
	stream, r, e := ch.Accept()
	if e != nil {
		return
	}
	defer stream.Close()
	go ssh.DiscardRequests(r)
	_ = raw.SetDeadline(time.Time{})
	done := make(chan struct{})
	go func() {
		io.Copy(target, stream)
		if cw, ok := target.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		close(done)
	}()
	io.Copy(stream, target)
	stream.CloseWrite()
	stream.Close()
	target.Close()
	<-done
}

func (s *Server) RunLAN(ctx context.Context) {
	type binding struct{ stop context.CancelFunc }
	current := map[string]binding{}
	defer func() {
		for _, b := range current {
			b.stop()
		}
	}()
	syncNet := func() {
		live := map[string]bool{}
		ls := links()
		ls = append(ls, link{ip: net.IPv4(127, 0, 0, 1), subnet: &net.IPNet{IP: net.IPv4(127, 0, 0, 0), Mask: net.CIDRMask(8, 32)}})
		notes := map[string]string{}
		for _, l := range ls {
			addr := l.ip.String()
			live[addr] = true
			if _, ok := current[addr]; ok {
				notes[addr] = "online"
				continue
			}
			socket, e := net.Listen("tcp4", net.JoinHostPort(addr, fmt.Sprint(ServicePort)))
			if e != nil {
				notes[addr] = "监听失败"
				continue
			}
			life, cancel := context.WithCancel(ctx)
			if !l.ip.IsLoopback() {
				_, e = responder(life, l, s.Self, ServicePort)
				if e != nil {
					socket.Close()
					cancel()
					notes[addr] = "局域网发现不可用"
					continue
				}
			}
			current[addr] = binding{cancel}
			notes[addr] = "online"
			go s.Serve(life, socket, l.subnet.Contains)
		}
		for addr, b := range current {
			if !live[addr] {
				b.stop()
				delete(current, addr)
			}
		}
		s.mu.Lock()
		s.Network = notes
		s.mu.Unlock()
	}
	syncNet()
	ticker := time.NewTicker(7 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			syncNet()
		}
	}
}
