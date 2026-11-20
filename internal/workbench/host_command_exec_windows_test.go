package workbench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/host"
)

func TestHostCommandWindowsWorkingDirectoryAndResult(t *testing.T) {
	dir := t.TempDir()
	res := runHostCommand(context.Background(), hostCommandRequest{
		ID: "sample", Cwd: dir, Timeout: 20,
		Command: "Write-Output (Get-Location).Path\nWrite-Output '中文'\n[Console]::Error.WriteLine('error-stream')\nexit 23",
	})
	if res.Error != "" || res.ExitCode != 23 || !strings.Contains(res.Stdout, dir) ||
		!strings.Contains(res.Stdout, "中文") || !strings.Contains(res.Stderr, "error-stream") || res.Truncated {
		t.Fatalf("result = %+v", res)
	}
}

func TestHostCommandWindowsNativeExitAndTimeout(t *testing.T) {
	dir := t.TempDir()
	res := runHostCommand(context.Background(), hostCommandRequest{Cwd: dir, Timeout: 20, Command: "cmd /c exit 17"})
	if res.ExitCode != 17 || res.Error != "" {
		t.Fatalf("native exit = %+v", res)
	}
	res = runHostCommand(context.Background(), hostCommandRequest{Cwd: dir, Timeout: 20, Command: "Write-Error 'final error'"})
	if res.ExitCode != 1 || !strings.Contains(res.Stderr, "final error") {
		t.Fatalf("PowerShell error = %+v", res)
	}
	res = runHostCommand(context.Background(), hostCommandRequest{Cwd: dir, Timeout: 1, Command: "Start-Sleep -Seconds 30"})
	if res.Error != context.DeadlineExceeded.Error() {
		t.Fatalf("timeout = %+v", res)
	}
}

func TestHostCommandOptInIsPrivateAndExact(t *testing.T) {
	s := &Server{profile: t.TempDir()}
	if err := host.EnsurePrivateDir(s.profile); err != nil {
		t.Fatal(err)
	}
	if s.hostCommandsEnabled() {
		t.Fatal("new profile is enabled")
	}
	p := filepath.Join(s.profile, "host-commands-v1.enabled")
	for _, content := range []string{"true", hostCommandMarker, hostCommandMarker + "extra"} {
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := host.ProtectFile(p); err != nil {
			t.Fatal(err)
		}
		if s.hostCommandsEnabled() != (content == hostCommandMarker) {
			t.Fatalf("unexpected permission for marker %q", content)
		}
	}
}
