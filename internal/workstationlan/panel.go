package workstationlan

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/sylvinus/agent-vm/internal/host"
)

type Panel struct {
	Host     *Server
	Client   *Client
	HasLocal bool
	Launch   func(context.Context, *SavedServer) error
	Firewall func() error
}
type Control struct {
	Address string `json:"address"`
	ID      string `json:"id"`
	Token   string `json:"token"`
}

func BindPanel(dir string) (net.Listener, Control, error) {
	if e := host.EnsurePrivateDir(dir); e != nil {
		return nil, Control{}, e
	}
	abs, e := filepath.Abs(dir)
	if e != nil {
		return nil, Control{}, e
	}
	h := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(abs))))
	addr := fmt.Sprintf("127.0.0.1:%d", 22000+int(binary.BigEndian.Uint16(h[:2]))%25000)
	l, e := net.Listen("tcp4", addr)
	if e == nil {
		return l, Control{Address: addr, ID: randomID(20), Token: randomID(32)}, nil
	}
	var old Control
	if readJSON(filepath.Join(dir, "control.json"), &old) != nil || old.Address != addr {
		return nil, Control{}, fmt.Errorf("工作台本机端口已被使用")
	}
	httpc := http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := httpc.Get("http://" + addr + "/instance")
	if e != nil {
		return nil, Control{}, e
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(io.LimitReader(resp.Body, 100))
	if e != nil || resp.StatusCode != 200 || string(data) != old.ID {
		return nil, Control{}, errors.New("本机管理端口的身份不匹配")
	}
	return nil, old, nil
}
func RunPanel(ctx context.Context, dir string, l net.Listener, ctrl Control, app Panel) error {
	if e := writeJSON(filepath.Join(dir, "control.json"), ctrl); e != nil {
		l.Close()
		return e
	}
	serv := &http.Server{Handler: app.Handler(ctrl), ReadHeaderTimeout: 4 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 70 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	stop := context.AfterFunc(ctx, func() { serv.Close() })
	defer stop()
	e := serv.Serve(l)
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}

type action struct {
	ID       string   `json:"id"`
	Ticket   string   `json:"ticket"`
	IDs      []string `json:"ids,omitempty"`
	ServerID string   `json:"serverId,omitempty"`
}

func (a Panel) Handler(ctrl Control) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'")
		if r.Host != ctrl.Address {
			http.Error(w, "无效来源", 403)
			return
		}
		p := r.URL.Path
		if r.Method == "GET" {
			if p == "/instance" {
				io.WriteString(w, ctrl.ID)
				return
			}
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+ctrl.Token)) != 1 {
			http.Error(w, "请从 Cage 应用打开连接管理", 403)
			return
		}
		var input action
		if r.Method == "POST" {
			if r.Header.Get("Origin") != "http://"+ctrl.Address || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				http.Error(w, "无效操作来源", 403)
				return
			}
			if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); e != nil {
				http.Error(w, "无效操作", 400)
				return
			}
		}
		var result any = map[string]bool{"ok": true}
		var e error
		switch {
		case p == "/api/info" && r.Method == "GET":
			role := "client"
			name := ""
			last := ""
			network := map[string]string{}
			if a.Host != nil {
				role = "host"
				name = a.Host.Self.Name
				_, _, network = a.Host.Lists()
			} else {
				name = a.Client.Name
				last = a.Client.Last()
			}
			result = map[string]any{"role": role, "name": name, "last": last, "local": a.HasLocal, "network": network}
		case a.Host != nil && p == "/api/devices" && r.Method == "GET":
			req, dev, network := a.Host.Lists()
			result = map[string]any{"requests": req, "devices": dev, "network": network}
		case a.Host != nil && p == "/api/device-sessions" && r.Method == "POST":
			result, e = a.Host.ManageSessions(r.Context(), input.ID, "list", nil)
		case a.Host != nil && p == "/api/delete-device-sessions" && r.Method == "POST":
			result, e = a.Host.ManageSessions(r.Context(), input.ID, "delete", input.IDs)
		case a.Client != nil && p == "/api/local-history" && r.Method == "POST":
			result, e = a.Client.History(input.ID)
		case a.Client != nil && p == "/api/delete-local-session" && r.Method == "POST":
			if len(input.IDs) != 1 {
				e = errors.New("请选择一个已归档的会话")
			} else {
				result, e = a.Client.DeleteLocal(r.Context(), input.ID, input.ServerID, input.IDs[0])
			}
		case a.Host != nil && r.Method == "POST" && (p == "/api/approve" || p == "/api/deny" || p == "/api/revoke"):
			e = a.Host.Decide(r.Context(), input.ID, input.Ticket, strings.TrimPrefix(p, "/api/"))
		case a.Host != nil && r.Method == "POST" && p == "/api/firewall" && a.Firewall != nil:
			e = a.Firewall()
		case a.Client != nil && p == "/api/scan" && r.Method == "POST":
			result, e = a.Client.Scan(r.Context())
		case a.Client != nil && p == "/api/servers" && r.Method == "GET":
			result, e = a.Client.Rows(r.Context())
		case a.Client != nil && p == "/api/request" && r.Method == "POST":
			result, e = a.Client.Request(r.Context(), input.ID)
		case a.Client != nil && p == "/api/forget" && r.Method == "POST":
			e = a.Client.Forget(input.ID)
		case a.Client != nil && p == "/api/open" && r.Method == "POST":
			if input.ID == "local" && a.HasLocal {
				e = a.Launch(r.Context(), nil)
			} else if saved, ok := a.Client.Saved(input.ID); ok {
				var reply answer
				reply, e = a.Client.query(r.Context(), saved.Peer, query{Action: "status"})
				if e == nil && (reply.State != "approved" || reply.Guest != saved.Guest) {
					e = errors.New("服务端授权已失效或客机身份已变化")
				}
				if e == nil {
					e = a.Launch(r.Context(), &saved)
				}
				if e == nil {
					e = a.Client.Select(input.ID)
				}
			} else {
				e = errors.New("请先申请连接")
			}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if e != nil {
			w.WriteHeader(400)
			result = map[string]string{"error": e.Error()}
		}
		_ = json.NewEncoder(w).Encode(result)
	})
}
