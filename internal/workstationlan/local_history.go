package workstationlan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sylvinus/agent-vm/internal/host"
)

type localHistoryItem struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Pending bool   `json:"pending,omitempty"`
}
type localHistoryPage struct {
	ServerID string             `json:"serverId"`
	Archived []localHistoryItem `json:"archived"`
}

func (c *Client) historyProfile(id string) (string, error) {
	if id == "local" {
		return filepath.Dir(c.Dir), nil
	}
	if _, ok := c.Saved(id); !ok {
		return "", errors.New("找不到工作台使用的服务器配置")
	}
	return c.Profile(id), nil
}
func (c *Client) History(id string) (localHistoryPage, error) {
	profile, err := c.historyProfile(id)
	if err != nil {
		return localHistoryPage{}, err
	}
	b, err := os.ReadFile(filepath.Join(profile, "Paseo", "server-id"))
	if err != nil {
		return localHistoryPage{}, errors.New("工作台会话服务尚未就绪")
	}
	page := localHistoryPage{ServerID: strings.TrimSpace(string(b)), Archived: []localHistoryItem{}}
	records, err := localRecords(profile)
	if err != nil {
		return page, err
	}
	seen := map[string]bool{}
	for _, r := range records {
		if r.ArchivedAt != "" && r.Provider == "cage-claude" && !seen[r.ID] {
			seen[r.ID] = true
			page.Archived = append(page.Archived, localHistoryItem{r.ID, r.Title, false})
		}
	}
	pending := filepath.Join(profile, "session-purges")
	files, err := os.ReadDir(pending)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return page, err
	}
	for _, f := range files {
		id := strings.TrimSuffix(f.Name(), ".json")
		if !uuidPattern.MatchString(id) || !f.Type().IsRegular() || seen[id] {
			continue
		}
		var row localRecord
		if err = readJSON(filepath.Join(pending, f.Name()), &row); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return page, err
		}
		page.Archived = append(page.Archived, localHistoryItem{id, row.Title, true})
	}
	return page, nil
}

// Use Paseo's own deletion fence, persistence flush and timeline removal, then verify its disk record.
func deletePaseoRecord(ctx context.Context, helper, profile, agentID string) error {
	resources := filepath.Join(filepath.Dir(helper), "PaseoLAN", "resources")
	app := filepath.Join(filepath.Dir(resources), "Paseo.exe")
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, app, "--disable-warning=DEP0040",
		filepath.Join(resources, "app.asar.unpacked", "dist", "daemon", "node-entrypoint-runner.js"), "node-script",
		filepath.Join(resources, "app.asar", "node_modules", "@getpaseo", "cli", "dist", "index.js"),
		"agent", "delete", agentID, "--json")
	hideChild(cmd)
	cmd.Env = append(os.Environ(), "ELECTRON_RUN_AS_NODE=1", "PASEO_NODE_ENV=production", "PASEO_HOME="+filepath.Join(profile, "Paseo"))
	output, err := cmd.Output()
	var response struct {
		DeletedCount int      `json:"deletedCount"`
		AgentIDs     []string `json:"agentIds"`
	}
	if err != nil || json.Unmarshal(output, &response) != nil || response.DeletedCount != 1 || len(response.AgentIDs) != 1 || response.AgentIDs[0] != agentID {
		return errors.New("Paseo 未确认删除，请确认工作台在线并重试")
	}
	records, err := localRecords(profile)
	if err != nil {
		return err
	}
	for _, r := range records {
		if r.ID == agentID {
			return errors.New("会话索引仍在磁盘上，请重试")
		}
	}
	return nil
}

// Paths under this root are generated from actual project directories and an exact UUID.
// os.Root confines all path resolution, including reparse points encountered during deletion.
func purgeNative(profile, nativeID string) (int64, error) {
	if nativeID == "" {
		return 0, nil
	}
	if !uuidPattern.MatchString(nativeID) {
		return 0, errors.New("Claude 会话标识无效")
	}
	base := filepath.Join(profile, "Sessions")
	abs, err := filepath.Abs(base)
	if err != nil {
		return 0, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !strings.EqualFold(resolved, abs) {
		return 0, errors.New("本机会话目录包含链接")
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	var total int64
	var targets []string
	parents := []string{"projects", "file-history", "session-env", "tasks"}
	for _, parent := range parents {
		info, err := root.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return total, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return total, fmt.Errorf("会话目录无效：%s", parent)
		}
		if parent != "projects" {
			targets = append(targets, filepath.Join(parent, nativeID))
			continue
		}
		projects, err := fs.ReadDir(root.FS(), "projects")
		if err != nil {
			return total, err
		}
		for _, p := range projects {
			if p.IsDir() && p.Type()&os.ModeSymlink == 0 {
				targets = append(targets, filepath.Join(parent, p.Name(), nativeID), filepath.Join(parent, p.Name(), nativeID+".jsonl"))
			}
		}
	}
	for _, target := range targets {
		info, err := root.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return total, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return total, errors.New("目标会话包含链接")
		}
		size := int64(0)
		err = fs.WalkDir(root.FS(), filepath.ToSlash(target), func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type().IsRegular() {
				i, e := d.Info()
				if e != nil {
					return e
				}
				size += i.Size()
			}
			return nil
		})
		if err != nil {
			return total, err
		}
		if err = root.RemoveAll(target); err != nil {
			return total, err
		}
		total += size
	}
	return total, nil
}
func (c *Client) DeleteLocal(ctx context.Context, device, serverID, id string) (map[string]any, error) {
	c.purgeMu.Lock()
	defer c.purgeMu.Unlock()
	if !uuidPattern.MatchString(id) {
		return nil, errors.New("会话标识无效")
	}
	profile, err := c.historyProfile(device)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(profile, "Paseo", "server-id"))
	if err != nil || strings.TrimSpace(string(b)) != serverID {
		return nil, errors.New("此会话属于另一个工作台")
	}
	records, err := localRecords(profile)
	if err != nil {
		return nil, err
	}
	var row localRecord
	present := false
	for _, r := range records {
		if r.ID == id {
			row = r
			present = true
			break
		}
	}
	intentDir := filepath.Join(profile, "session-purges")
	intent := filepath.Join(intentDir, id+".json")
	if present {
		if row.Provider != "cage-claude" || row.ArchivedAt == "" {
			return nil, errors.New("请先归档这条 Claude 会话")
		}
		if row.Persistence.SessionID != "" && !uuidPattern.MatchString(row.Persistence.SessionID) {
			return nil, errors.New("无法核对原始会话标识")
		}
		if device != "local" && row.Persistence.SessionID != "" {
			dirs, e := childDirs(filepath.Join(profile, "Sessions", "projects"))
			if e != nil {
				return nil, e
			}
			receipt := catalogReceipt(row, dirs)
			outbox := filepath.Join(profile, "receipt-outbox")
			if err = host.EnsurePrivateDir(outbox); err != nil {
				return nil, err
			}
			if err = writeJSON(filepath.Join(outbox, receipt.ID+".json"), receipt); err != nil {
				return nil, err
			}
		}
		if err = host.EnsurePrivateDir(intentDir); err != nil {
			return nil, err
		}
		if err = writeJSON(intent, row); err != nil {
			return nil, err
		}
		if err = deletePaseoRecord(ctx, c.Helper, profile, id); err != nil {
			return nil, err
		}
	} else if err = readJSON(intent, &row); err != nil || row.ID != id {
		return nil, errors.New("找不到已归档的会话")
	}
	// Two Paseo tabs can refer to the same Claude session. Its mirror belongs to the last reference.
	records, err = localRecords(profile)
	if err != nil {
		return nil, err
	}
	shared := false
	for _, r := range records {
		if row.Persistence.SessionID != "" && r.Persistence.SessionID == row.Persistence.SessionID {
			shared = true
			break
		}
	}
	var reclaimed int64
	if !shared {
		reclaimed, err = purgeNative(profile, row.Persistence.SessionID)
		if err != nil {
			return nil, err
		}
	}
	if err = os.Remove(intent); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return map[string]any{"id": id, "bytes": reclaimed, "shared": shared}, nil
}
