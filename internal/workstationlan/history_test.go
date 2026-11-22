package workstationlan

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPurgeNativeConfinesSessionFiles(t *testing.T) {
	profile, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := "437deca1-f79b-440b-9579-14fa4ea26bc4"
	other := "38251167-ad83-48b6-b778-3243cd178e98"
	base := filepath.Join(profile, "Sessions")
	fixture := func(rel string) string {
		path := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("12345"), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	deleted := []string{fixture(filepath.Join("projects", "P", target+".jsonl")), fixture(filepath.Join("projects", "P", target, "subagents", "a.jsonl")), fixture(filepath.Join("file-history", target, "old"))}
	kept := fixture(filepath.Join("projects", "P", other+".jsonl"))
	if _, err := purgeNative(profile, ".."); err == nil {
		t.Fatal("invalid session id accepted")
	}
	n, err := purgeNative(profile, target)
	if err != nil || n != 15 {
		t.Fatalf("purge: %v, %d", err, n)
	}
	for _, p := range deleted {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("remaining %s", p)
		}
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatal(err)
	}
	if n, err := purgeNative(profile, target); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}

func TestArchivedHistoryScopedToCurrentDaemon(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "LAN")
	c, err := NewClient(dir, "helper", "work")
	if err != nil {
		t.Fatal(err)
	}
	sid := "44d6f82e-ae62-4f44-9b3b-759dbb288927"
	agentDir := filepath.Join(base, "Paseo", "agents", "project")
	if err = os.MkdirAll(agentDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(base, "Paseo", "server-id"), []byte("srv_test"), 0600); err != nil {
		t.Fatal(err)
	}
	record := localRecord{ID: sid, Provider: "cage-claude", Title: "Fixture"}
	put := func() {
		b, _ := json.Marshal(record)
		if err := os.WriteFile(filepath.Join(agentDir, sid+".json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	put()
	if _, err = c.DeleteLocal(context.Background(), "local", "wrong", sid); err == nil {
		t.Fatal("wrong daemon accepted")
	}
	if _, err = c.DeleteLocal(context.Background(), "local", "srv_test", sid); err == nil {
		t.Fatal("active record accepted")
	}
	record.ArchivedAt = "2026-11-22T00:00:00Z"
	put()
	page, err := c.History("local")
	if err != nil || len(page.Archived) != 1 || page.Archived[0].ID != sid {
		t.Fatal(page, err)
	}
	if _, err = c.History("../other"); err == nil {
		t.Fatal("unpaired profile accepted")
	}
}
