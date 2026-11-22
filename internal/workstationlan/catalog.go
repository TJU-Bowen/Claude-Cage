package workstationlan

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sylvinus/agent-vm/internal/host"
	"golang.org/x/crypto/ssh"
)

const catalogChannel = "cage-session-catalog-v1"

var uuidPattern = regexp.MustCompile("^[0-9a-fA-F]{8}-([0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$")

// Legacy guests used the hostname as a workspace namespace. Match a shared message UUID as well.
type Receipt struct {
	ID       string `json:"id"`
	Cwd      string `json:"cwd"`
	Title    string `json:"title"`
	Archived bool   `json:"archived"`
	Proof    string `json:"proof,omitempty"`
	Deleted  bool   `json:"deleted,omitempty"`
}
type guestSessionQuery struct {
	Action   string    `json:"action"`
	Device   string    `json:"device"`
	Name     string    `json:"name"`
	Receipts []Receipt `json:"receipts"`
	IDs      []string  `json:"ids,omitempty"`
}

//go:embed guest_sessions.py
var guestInventory string

func (g LimaGuest) SessionFiles(ctx context.Context, q guestSessionQuery) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	alias, config := g.config()
	script := "python3 -c '" + strings.ReplaceAll(guestInventory, "'", "'\\''") + "'"
	cmd := exec.CommandContext(ctx, g.SSH, "-F", config, "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-T", alias, script)
	hideChild(cmd)
	data, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}
	cmd.Stdin = bytes.NewReader(data)
	output, err := cmd.Output()
	if err != nil {
		return nil, errors.New("无法读取客机会话，请确认 Cage 虚拟机正在运行")
	}
	var reply struct {
		Error string `json:"error"`
	}
	if err = json.Unmarshal(output, &reply); err != nil {
		return nil, errors.New("客机会话响应无效")
	}
	if reply.Error != "" {
		return nil, errors.New(reply.Error)
	}
	return json.RawMessage(output), nil
}
func (s *Server) receiptPath(device string) string {
	return filepath.Join(filepath.Dir(s.store), "sessions", device+".json")
}
func readReceipts(path string) (map[string]Receipt, error) {
	entries := map[string]Receipt{}
	err := readJSON(path, &entries)
	if errors.Is(err, os.ErrNotExist) {
		return entries, nil
	}
	if entries == nil && err == nil {
		return nil, errors.New("会话索引无效")
	}
	return entries, err
}
func (s *Server) mergeReceipts(device string, update []Receipt) error {
	s.admin.Lock()
	defer s.admin.Unlock()
	s.mu.Lock()
	trusted := s.trusted(device)
	s.mu.Unlock()
	if !trusted || len(update) > 96 {
		return errors.New("会话目录更新无效")
	}
	path := s.receiptPath(device)
	entries, err := readReceipts(path)
	if err != nil {
		return err
	}
	changed := false
	for _, row := range update {
		if !uuidPattern.MatchString(row.ID) || (row.Proof != "" && !uuidPattern.MatchString(row.Proof)) ||
			len(row.Cwd) > 4096 || len(row.Title) > 1024 || strings.ContainsRune(row.Cwd, 0) {
			return errors.New("会话记录无效")
		}
		old := entries[row.ID]
		if row.Deleted {
			return errors.New("工作机不能删除服务端会话索引")
		}
		if old.Deleted {
			continue
		}
		if row.Proof == "" {
			row.Proof = old.Proof
		}
		if old != row {
			entries[row.ID] = row
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err = host.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	return writeJSON(path, entries)
}
func (s *Server) receiveCatalog(device string, ch ssh.NewChannel) {
	if len(ch.ExtraData()) != 0 {
		_ = ch.Reject(ssh.Prohibited, "无效请求")
		return
	}
	stream, req, err := ch.Accept()
	if err != nil {
		return
	}
	defer stream.Close()
	go ssh.DiscardRequests(req)
	var update []Receipt
	if err = json.NewDecoder(io.LimitReader(stream, 600<<10)).Decode(&update); err == nil {
		err = s.mergeReceipts(device, update)
	}
	_ = json.NewEncoder(stream).Encode(map[string]bool{"ok": err == nil})
}
func (s *Server) ManageSessions(ctx context.Context, device, op string, ids []string) (json.RawMessage, error) {
	s.mu.Lock()
	d, ok := s.paired[device]
	s.mu.Unlock()
	if !ok {
		return nil, errors.New("工作机未配对")
	}
	if op == "delete" {
		if len(ids) == 0 || len(ids) > 200 {
			return nil, errors.New("每次请选择 1 到 200 个会话")
		}
		for _, id := range ids {
			if !uuidPattern.MatchString(id) {
				return nil, errors.New("会话标识无效")
			}
		}
	} else if op != "list" {
		return nil, errors.New("会话操作无效")
	}
	s.admin.Lock()
	entries, err := readReceipts(s.receiptPath(device))
	s.admin.Unlock()
	if err != nil {
		return nil, err
	}
	guest, ok := s.guest.(interface {
		SessionFiles(context.Context, guestSessionQuery) (json.RawMessage, error)
	})
	if !ok {
		return nil, errors.New("客机不支持会话管理")
	}
	rows := make([]Receipt, 0, len(entries))
	for _, r := range entries {
		if !r.Deleted {
			rows = append(rows, r)
		}
	}
	reply, err := guest.SessionFiles(ctx, guestSessionQuery{op, device, d.Name, rows, ids})
	if err != nil || op != "delete" {
		return reply, err
	}
	var outcome struct {
		Results []struct {
			ID    string
			Error string
		}
	}
	if err = json.Unmarshal(reply, &outcome); err != nil {
		return nil, err
	}
	s.admin.Lock()
	defer s.admin.Unlock()
	path := s.receiptPath(device)
	entries, err = readReceipts(path)
	if err != nil {
		return nil, err
	}
	for _, item := range outcome.Results {
		if item.Error == "" {
			entries[item.ID] = Receipt{ID: item.ID, Deleted: true}
		}
	}
	if err = host.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err = writeJSON(path, entries); err != nil {
		return nil, err
	}
	return reply, nil
}

type localRecord struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	Title       string `json:"title"`
	Cwd         string `json:"cwd"`
	ArchivedAt  string `json:"archivedAt"`
	Persistence struct {
		SessionID string `json:"sessionId"`
	} `json:"persistence"`
}

func childDirs(root string) ([]string, error) {
	items, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, it := range items {
		if it.IsDir() && it.Type()&os.ModeSymlink == 0 {
			dirs = append(dirs, filepath.Join(root, it.Name()))
		}
	}
	return dirs, nil
}
func localRecords(profile string) ([]localRecord, error) {
	registry := filepath.Join(profile, "Paseo", "agents")
	dirs, err := childDirs(registry)
	if err != nil {
		return nil, err
	}
	var rows []localRecord
	for _, dir := range append(dirs, registry) {
		items, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			id, ok := strings.CutSuffix(item.Name(), ".json")
			if !ok || !uuidPattern.MatchString(id) || !item.Type().IsRegular() {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, item.Name()))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			var row localRecord
			if err = json.Unmarshal(b, &row); err != nil {
				return nil, err
			}
			if row.ID == id {
				rows = append(rows, row)
			}
		}
	}
	return rows, nil
}
func messageProof(file string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	in := bufio.NewReader(f)
	for {
		line, err := in.ReadBytes('\n')
		var msg struct{ Type, UUID string }
		if json.Unmarshal(line, &msg) == nil && (msg.Type == "user" || msg.Type == "assistant") && uuidPattern.MatchString(msg.UUID) {
			return msg.UUID
		}
		if err != nil {
			return ""
		}
	}
}
func catalogReceipt(row localRecord, dirs []string) Receipt {
	title := []rune(row.Title)
	if len(title) > 250 {
		title = title[:250]
	}
	r := Receipt{ID: row.Persistence.SessionID, Cwd: row.Cwd, Title: string(title), Archived: row.ArchivedAt != ""}
	for _, dir := range dirs {
		if r.Proof = messageProof(filepath.Join(dir, r.ID+".jsonl")); r.Proof != "" {
			break
		}
	}
	return r
}
func (c *Client) sendReceipts(ctx context.Context, saved SavedServer, rows []Receipt) error {
	if len(rows) == 0 {
		return nil
	}
	conn, err := dialServer(ctx, saved.Peer, c.signer, "workstation")
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	ch, requests, err := conn.OpenChannel(catalogChannel, nil)
	if err != nil {
		return err
	}
	defer ch.Close()
	go ssh.DiscardRequests(requests)
	if err = json.NewEncoder(ch).Encode(rows); err != nil {
		return err
	}
	if err = ch.CloseWrite(); err != nil {
		return err
	}
	var ack struct {
		OK bool `json:"ok"`
	}
	if err = json.NewDecoder(io.LimitReader(ch, 128)).Decode(&ack); err != nil {
		return err
	}
	if !ack.OK {
		return errors.New("服务端未保存会话索引")
	}
	return nil
}

// An offline purge leaves a metadata-only receipt until the paired server acknowledges it.
func (c *Client) syncProfile(ctx context.Context, saved SavedServer, previous string) (string, error) {
	profile := c.Profile(saved.ID)
	rows, err := localRecords(profile)
	if err != nil {
		return "", err
	}
	dirs, err := childDirs(filepath.Join(profile, "Sessions", "projects"))
	if err != nil {
		return "", err
	}
	outbox := filepath.Join(profile, "receipt-outbox")
	files, err := os.ReadDir(outbox)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	byID := map[string]Receipt{}
	for _, f := range files {
		if !uuidPattern.MatchString(strings.TrimSuffix(f.Name(), ".json")) || !f.Type().IsRegular() {
			continue
		}
		var item Receipt
		if err := readJSON(filepath.Join(outbox, f.Name()), &item); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		byID[item.ID] = item
	}
	for _, row := range rows {
		if row.Provider == "cage-claude" && uuidPattern.MatchString(row.Persistence.SessionID) {
			item := catalogReceipt(row, dirs)
			if old, ok := byID[item.ID]; ok && item.Proof == "" {
				item.Proof = old.Proof
			}
			byID[item.ID] = item
		}
	}
	items := make([]Receipt, 0, len(byID))
	for _, v := range byID {
		items = append(items, v)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	wire, _ := json.Marshal(items)
	digest := fmt.Sprintf("%x", sha256.Sum256(wire))
	if digest != previous || len(files) > 0 {
		for start := 0; start < len(items); start += 96 {
			if err = c.sendReceipts(ctx, saved, items[start:min(start+96, len(items))]); err != nil {
				return "", err
			}
		}
		for _, f := range files {
			if f.Type().IsRegular() && uuidPattern.MatchString(strings.TrimSuffix(f.Name(), ".json")) {
				_ = os.Remove(filepath.Join(outbox, f.Name()))
			}
		}
	}
	return digest, nil
}
func (c *Client) SyncCatalogs(ctx context.Context) {
	last := map[string]string{}
	refresh := time.Now()
	for {
		c.mu.Lock()
		saved := make([]SavedServer, 0, len(c.saved.Servers))
		for _, s := range c.saved.Servers {
			saved = append(saved, s)
		}
		c.mu.Unlock()
		if time.Since(refresh) > 5*time.Minute {
			clear(last)
			refresh = time.Now()
		}
		for _, s := range saved {
			timed, cancel := context.WithTimeout(ctx, 12*time.Second)
			if digest, err := c.syncProfile(timed, s, last[s.ID]); err == nil {
				last[s.ID] = digest
			}
			cancel()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(12 * time.Second):
		}
	}
}
