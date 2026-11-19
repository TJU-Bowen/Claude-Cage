package workbench

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/host"
)

// Opt-in: uses the caller's running local Cage VM and a temporary guest HOME.
// Never registers the fixture in the real Claude account or installed profile.
func TestHostCommandRealGuest(t *testing.T) {
	cfg, target := os.Getenv("CAGE_TEST_HOST_SSH_CONFIG"), os.Getenv("CAGE_TEST_HOST_SSH_TARGET")
	if cfg == "" || target == "" {
		t.Skip("set CAGE_TEST_HOST_SSH_CONFIG and CAGE_TEST_HOST_SSH_TARGET for live guest test")
	}
	exe := os.Getenv("CAGE_TEST_HOST_SSH")
	if exe == "" {
		exe = sshExecutable()
	}
	ssh := func(ctx context.Context, script string) *exec.Cmd {
		return exec.CommandContext(ctx, exe, "-F", cfg, "-o", "BatchMode=yes", "-o", "ClearAllForwardings=yes", "-T", target, "--", script)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	b, err := ssh(ctx, "mktemp -d /tmp/cage-host-test.XXXXXXXXXX").Output()
	guestHome := strings.TrimSpace(string(b))
	if err != nil || !regexp.MustCompile(`^/tmp/cage-host-test\.[a-zA-Z0-9]+$`).MatchString(guestHome) {
		t.Fatalf("could not prepare private guest HOME: %v", err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 8*time.Second)
		defer stop()
		if err := ssh(cleanup, "rm -rf -- "+guestHome).Run(); err != nil {
			t.Errorf("clean guest fixture: %v", err)
		}
	}()
	guest := func(ctx context.Context, script string) *exec.Cmd {
		return ssh(ctx, "export HOME="+guestHome+"; "+script)
	}
	s := &Server{profile: t.TempDir(), ready: true, account: &localAccount{}}
	s.account.setKey(make([]byte, 32))
	defer s.account.lock()
	if err := host.EnsurePrivateDir(s.profile); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(s.profile, "host-commands-v1.enabled")
	if err := os.WriteFile(marker, []byte(hostCommandMarker), 0600); err != nil {
		t.Fatal(err)
	}
	if err := host.ProtectFile(marker); err != nil {
		t.Fatal(err)
	}
	bridgeCtx, stopBridge := context.WithCancel(ctx)
	done := make(chan struct{})
	ready := make(chan struct{}, 1)
	var bridgeErr error
	go func() {
		defer close(done)
		bridgeErr = s.runGuestHostCommandBridge(bridgeCtx, ready, guest)
	}()
	defer func() {
		stopBridge()
		<-done
	}()
	select {
	case <-ready:
	case <-done:
		t.Fatalf("start live bridge: %v", bridgeErr)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cwd := t.TempDir()
	call := func(command string) string {
		t.Helper()
		mcp := guest(ctx, `exec python3 -I "$HOME/.local/lib/cage-host-command/runner.py" mcp`)
		var input bytes.Buffer
		enc := json.NewEncoder(&input)
		enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26"}})
		enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
		enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
		enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "run_host_command", "arguments": map[string]any{"cwd": cwd, "command": command}}})
		mcp.Stdin = &input
		response, err := mcp.Output()
		if err != nil || !bytes.Contains(response, []byte("run_host_command")) {
			t.Fatalf("MCP handshake/tool discovery failed: %v", err)
		}
		sc := bufio.NewScanner(bytes.NewReader(response))
		for sc.Scan() {
			var frame struct {
				ID     int `json:"id"`
				Result struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			if json.Unmarshal(sc.Bytes(), &frame) == nil && frame.ID == 3 && len(frame.Result.Content) == 1 {
				return frame.Result.Content[0].Text
			}
		}
		t.Fatal("missing tools/call response")
		return ""
	}
	text := call("whoami.exe\nWrite-Output (Get-Location).Path\nWrite-Output '桥接测试'\n[Console]::Error.WriteLine('separate stderr')\nexit 7")
	var result hostCommandResult
	if err := json.Unmarshal([]byte(text), &result); err != nil || result.Error != "" || result.ExitCode != 7 ||
		!strings.Contains(result.Stdout, cwd) || !strings.Contains(result.Stdout, "桥接测试") || !strings.Contains(result.Stderr, "separate stderr") {
		t.Fatalf("MCP command result: %s", text)
	}
	t.Logf("Windows tools/call confirmed cwd, separate UTF-8 output, exit_code=7, whoami=%q", strings.Split(strings.TrimSpace(result.Stdout), "\n")[0])
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	sideEffect := filepath.Join(cwd, "revoked-command-ran.txt")
	res := call("Set-Content -LiteralPath './revoked-command-ran.txt' -Value 'unexpected'")
	if _, err := os.Stat(sideEffect); !os.IsNotExist(err) || !strings.Contains(res, "error") {
		t.Fatalf("revoked command wasn't rejected: %s", res)
	}
}
