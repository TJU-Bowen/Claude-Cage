package workstationlan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sylvinus/agent-vm/internal/host"
	"golang.org/x/crypto/ssh"
)

type Row struct {
	Peer
	Saved bool   `json:"saved"`
	State string `json:"state"`
	Code  string `json:"code,omitempty"`
}
type Client struct {
	Dir, Helper, Name string
	signer            ssh.Signer
	mu                sync.Mutex
	purgeMu           sync.Mutex
	saved             ClientRecord
	seen              map[string]Peer
	waits             map[string]answer
}

func NewClient(dir, helper, name string) (*Client, error) {
	signer, e := initIdentity(dir)
	if e != nil {
		return nil, e
	}
	c := &Client{Dir: dir, Helper: helper, Name: cleanName(name), signer: signer, seen: map[string]Peer{}, waits: map[string]answer{}, saved: ClientRecord{Servers: map[string]SavedServer{}}}
	if e = readJSON(filepath.Join(dir, "servers.json"), &c.saved); e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if c.saved.Servers == nil {
		return nil, errors.New("服务器记录损坏")
	}
	return c, nil
}
func (c *Client) Last() string     { c.mu.Lock(); defer c.mu.Unlock(); return c.saved.Last }
func (c *Client) DeviceID() string { return idFor(c.signer.PublicKey()) }
func (c *Client) Saved(id string) (SavedServer, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.saved.Servers[id]
	return p, ok
}
func dialServer(ctx context.Context, p Peer, signer ssh.Signer, user string) (*ssh.Client, error) {
	key, e := decodeKey(p.Public)
	if e != nil || idFor(key) != p.ID || !addressOK(p.Address) {
		return nil, errors.New("服务器身份或局域网地址无效")
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, e := d.DialContext(ctx, "tcp4", p.Address)
	if e != nil {
		return nil, errors.New("无法连接局域网服务器")
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	cfg := &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(key)}
	sc, ch, r, e := ssh.NewClientConn(conn, p.Address, cfg)
	if e != nil {
		conn.Close()
		return nil, errors.New("服务器身份验证或设备授权失败")
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(sc, ch, r), nil
}
func (c *Client) query(ctx context.Context, p Peer, q query) (answer, error) {
	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	sc, e := dialServer(ctx, p, c.signer, "pair")
	if e != nil {
		return answer{}, e
	}
	defer sc.Close()
	stop := context.AfterFunc(ctx, func() { sc.Close() })
	defer stop()
	data, _ := json.Marshal(q)
	ch, requests, e := sc.OpenChannel(pairChannel, data)
	if e != nil {
		return answer{}, e
	}
	defer ch.Close()
	go ssh.DiscardRequests(requests)
	var a answer
	if e = json.NewDecoder(io.LimitReader(ch, 4096)).Decode(&a); e != nil {
		return answer{}, e
	}
	if a.State == "pending" && (a.Ticket == "" || a.Code != codeFor(p.ID, idFor(c.signer.PublicKey()), a.Ticket)) {
		return answer{}, errors.New("配对编号校验失败")
	}
	return a, nil
}
func (c *Client) Scan(ctx context.Context) ([]Row, error) {
	found, e := discover(ctx)
	c.mu.Lock()
	seen := make(map[string]Peer, len(found))
	for _, p := range found {
		seen[p.ID] = p
	}
	for id, p := range c.seen {
		if _, waiting := c.waits[id]; waiting {
			if _, ok := seen[id]; !ok {
				seen[id] = p
			}
		}
	}
	c.seen = seen
	c.mu.Unlock()
	rows, err := c.Rows(ctx)
	if len(rows) > 0 {
		return rows, err
	}
	return rows, e
}
func (c *Client) Rows(ctx context.Context) ([]Row, error) {
	c.mu.Lock()
	peers := map[string]Peer{}
	known := map[string]bool{}
	waits := map[string]answer{}
	for id, s := range c.saved.Servers {
		peers[id] = s.Peer
		known[id] = true
	}
	for id, p := range c.seen {
		peers[id] = p
	}
	for id, w := range c.waits {
		waits[id] = w
	}
	c.mu.Unlock()
	rows := []Row{}
	var m sync.Mutex
	var wg sync.WaitGroup
	for id, p := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := Row{Peer: p, State: "unpaired", Saved: known[id]}
			if w, ok := waits[id]; ok || known[id] {
				a, e := c.query(ctx, p, query{Action: "status", Ticket: w.Ticket})
				if e != nil {
					r.State = "offline"
				} else {
					r.State = a.State
					r.Code = a.Code
					if a.State == "approved" {
						if e = c.remember(p, a.Guest); e != nil {
							r.State = "identity_changed"
						}
					}
					if a.State == "denied" || a.State == "unpaired" {
						c.mu.Lock()
						if c.waits[id].Ticket == w.Ticket {
							delete(c.waits, id)
						}
						c.mu.Unlock()
					}
				}
			}
			m.Lock()
			rows = append(rows, r)
			m.Unlock()
		}()
	}
	wg.Wait()
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}
func (c *Client) Request(ctx context.Context, id string) (answer, error) {
	c.mu.Lock()
	p, ok := c.seen[id]
	if !ok {
		p = c.saved.Servers[id].Peer
	}
	c.mu.Unlock()
	if p.ID != id || id == "" {
		return answer{}, errors.New("请先搜索并选择服务器")
	}
	a, e := c.query(ctx, p, query{Action: "request", Name: c.Name})
	if e != nil {
		return a, e
	}
	if a.State == "pending" {
		c.mu.Lock()
		c.waits[id] = a
		c.mu.Unlock()
	}
	if a.State == "approved" {
		e = c.remember(p, a.Guest)
	}
	return a, e
}
func (c *Client) Profile(id string) string { return filepath.Join(c.Dir, "hosts", id) }
func unusedPort() (int, error) {
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return 0, e
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
func sshPath(s string) string {
	return "\"" + strings.NewReplacer("\\", "/", "\"", "\\\"", "%", "%%").Replace(s) + "\""
}
func shellPath(s string) string {
	if runtime.GOOS == "windows" {
		return sshPath(s)
	}
	return "'" + strings.NewReplacer("'", "'\\''", "%", "%%").Replace(s) + "'"
}
func (c *Client) remember(p Peer, guest GuestInfo) error {
	g, e := decodeKey(guest.Public)
	if e != nil || !accountName.MatchString(guest.User) {
		return errors.New("服务器返回的客机身份无效")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	old, ok := c.saved.Servers[p.ID]
	if ok && old.Guest != guest {
		return errors.New("客机身份已变化，请先忘记旧连接再配对")
	}
	dir := c.Profile(p.ID)
	alias := "cage-" + p.ID
	known := filepath.Join(dir, "known_hosts")
	sshConfig := filepath.Join(dir, "ssh.config")
	conf := fmt.Sprintf("Host cage-lan\n  HostName cage-guest\n  User %s\n  HostKeyAlias %s\n  HostKeyAlgorithms ssh-ed25519\n  StrictHostKeyChecking yes\n  UpdateHostKeys no\n  IdentitiesOnly yes\n  IdentityFile %s\n  UserKnownHostsFile %s\n  ProxyCommand %s proxy %s\n", guest.User, alias, sshPath(filepath.Join(c.Dir, "identity")), sshPath(known), shellPath(c.Helper), p.ID)
	if ok && old.Peer == p {
		// The app can be moved or rebuilt without changing the paired identity.
		if data, err := os.ReadFile(sshConfig); err == nil && string(data) == conf {
			delete(c.waits, p.ID)
			return nil
		}
	}
	if e = host.EnsurePrivateDir(dir); e != nil {
		return e
	}
	port := old.DesktopPort
	if port == 0 {
		port, e = unusedPort()
		if e != nil {
			return e
		}
	}
	if e = writeFile(known, append([]byte(alias+" "), ssh.MarshalAuthorizedKey(g)...)); e != nil {
		return e
	}
	if e = writeFile(sshConfig, []byte(conf)); e != nil {
		return e
	}
	c.saved.Servers[p.ID] = SavedServer{Peer: p, Guest: guest, DesktopPort: port}
	if e = writeJSON(filepath.Join(c.Dir, "servers.json"), c.saved); e != nil {
		if ok {
			c.saved.Servers[p.ID] = old
		} else {
			delete(c.saved.Servers, p.ID)
		}
		return e
	}
	delete(c.waits, p.ID)
	return nil
}
func (c *Client) Select(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.saved.Servers[id]; !ok {
		return errors.New("设备尚未配对")
	}
	old := c.saved.Last
	c.saved.Last = id
	if e := writeJSON(filepath.Join(c.Dir, "servers.json"), c.saved); e != nil {
		c.saved.Last = old
		return e
	}
	return nil
}
func (c *Client) Forget(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	old, ok := c.saved.Servers[id]
	if !ok {
		return nil
	}
	last := c.saved.Last
	delete(c.saved.Servers, id)
	delete(c.waits, id)
	if last == id {
		c.saved.Last = ""
	}
	if e := writeJSON(filepath.Join(c.Dir, "servers.json"), c.saved); e != nil {
		c.saved.Servers[id] = old
		c.saved.Last = last
		return e
	}
	return nil
}
func (c *Client) Tunnel(ctx context.Context, id string, in io.Reader, out io.Writer) error {
	saved, ok := c.Saved(id)
	if !ok {
		return errors.New("服务器尚未配对")
	}
	sc, e := dialServer(ctx, saved.Peer, c.signer, "workstation")
	if e != nil {
		found, _ := discover(ctx)
		for _, p := range found {
			if p.ID == id && p.Address != saved.Address {
				sc, e = dialServer(ctx, p, c.signer, "workstation")
				break
			}
		}
	}
	if e != nil {
		return e
	}
	defer sc.Close()
	ch, r, e := sc.OpenChannel(tunnelChannel, nil)
	if e != nil {
		return fmt.Errorf("无法连接 Cage 客机，请检查服务端授权和环境状态")
	}
	defer ch.Close()
	go ssh.DiscardRequests(r)
	stop := context.AfterFunc(ctx, func() { sc.Close() })
	defer stop()
	go func() { io.Copy(ch, in); ch.CloseWrite() }()
	_, e = io.Copy(out, ch)
	return e
}
