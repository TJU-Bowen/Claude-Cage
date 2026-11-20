package workbench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/host"
)

func TestHostCommandMacOSBridge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "/usr/bin/python3", "--version").Run(); err != nil {
		t.Skip("this bridge fixture requires the local system Python")
	}
	// A short canonical HOME keeps the adapter's UNIX socket within Darwin's limit.
	guestHome, err := os.MkdirTemp("/private/tmp", "cage-host-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(guestHome) })
	guest := func(ctx context.Context, script string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "/bin/bash", "--noprofile", "--norc", "-c", script)
		cmd.Env = append(os.Environ(), "HOME="+guestHome)
		return cmd
	}

	s := &Server{profile: t.TempDir(), ready: true, account: &localAccount{}}
	s.account.setKey(make([]byte, 32))
	defer s.account.lock()
	if _, ok := s.hostCommandAccess(); ok {
		t.Fatal("host commands enabled without consent")
	}
	if err := host.EnsurePrivateDir(s.profile); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(s.profile, "host-commands-v1.enabled")
	if err := os.WriteFile(marker, []byte(hostCommandMarker), 0600); err != nil {
		t.Fatal(err)
	}

	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	bridgeCtx, stop := context.WithCancel(ctx)
	go func() {
		done <- s.runGuestHostCommandBridge(bridgeCtx, ready, guest)
		close(done)
	}()
	defer func() {
		stop()
		<-done
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("start bridge: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	call := func(command string) hostCommandResult {
		t.Helper()
		mcp := guest(ctx, "exec /usr/bin/python3 -I \"$HOME/.local/lib/cage-host-command/runner.py\" mcp macos")
		request := map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{
				"name": "run_host_command", "arguments": map[string]any{"command": command, "cwd": cwd},
			},
		}
		var input bytes.Buffer
		if err := json.NewEncoder(&input).Encode(request); err != nil {
			t.Fatal(err)
		}
		mcp.Stdin = &input
		output, err := mcp.Output()
		if err != nil {
			t.Fatal(err)
		}
		var reply struct {
			Result struct {
				Content []struct{ Text string }
			}
		}
		var result hostCommandResult
		if err := json.Unmarshal(output, &reply); err != nil || len(reply.Result.Content) != 1 ||
			json.Unmarshal([]byte(reply.Result.Content[0].Text), &result) != nil {
			t.Fatalf("invalid MCP result: %s", output)
		}
		return result
	}
	result := call("pwd\nprintf '桥接测试\\n'\nprintf 'separate stderr\\n' >&2\nexit 7")
	if result.Error != "" || result.ExitCode != 7 || result.Stdout != cwd+"\n桥接测试\n" ||
		result.Stderr != "separate stderr\n" || result.Truncated {
		t.Fatalf("unexpected command result: %+v", result)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	result = call("touch should-not-run")
	if _, err := os.Stat(filepath.Join(cwd, "should-not-run")); !errors.Is(err, os.ErrNotExist) || result.Error == "" {
		t.Fatalf("command after revocation: result=%+v stat=%v", result, err)
	}
}

func TestHostCommandMacOSTimeout(t *testing.T) {
	start := time.Now()
	result := runHostCommand(context.Background(), hostCommandRequest{
		Cwd: t.TempDir(), Command: "echo child-started\n/bin/sleep 30 &\nwait", Timeout: 1,
	})
	if result.Error != context.DeadlineExceeded.Error() || !strings.Contains(result.Stdout, "child-started") {
		t.Fatalf("unexpected timeout result: %+v", result)
	}
	if elapsed := time.Since(start); elapsed >= 3*time.Second {
		t.Fatalf("child held its output pipe after timeout: %v", elapsed)
	}
}
