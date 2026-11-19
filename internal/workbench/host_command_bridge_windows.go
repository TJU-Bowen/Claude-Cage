package workbench

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sylvinus/agent-vm/internal/containerenv"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/host"
)

//go:embed host_command_guest.py
var hostCommandGuest []byte

const hostCommandMarker = "cage-host-commands-v1\n"

func (s *Server) startHostCommandMonitor(ctx context.Context) {
	if s.profile == "" || s.clientOnly || s.desktopEntry || s.windowsGuest != nil || s.macosGuest != nil || containerenv.Configured(s.profile) {
		return
	}
	s.monitors.Add(1)
	go func() {
		defer s.monitors.Done()
		s.monitorHostCommands(ctx)
	}()
}

// This private, non-shared marker can be written by the native host UI.
// No workbench, phone or guest API provides an opt-in operation.
func (s *Server) hostCommandsEnabled() bool {
	path := filepath.Join(s.profile, "host-commands-v1.enabled")
	if err := host.CheckPrivateFile(path); err != nil {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(len(hostCommandMarker))+1))
	return err == nil && string(b) == hostCommandMarker
}

func (s *Server) hostCommandAccess() (context.Context, bool) {
	s.mu.Lock()
	ready := s.ready && !s.closing
	s.mu.Unlock()
	if !ready || (s.remote != nil && s.remote.isRemote()) || !s.hostCommandsEnabled() {
		return nil, false
	}
	return s.account.access()
}

func (s *Server) monitorHostCommands(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	path := filepath.Join(s.profile, "host-commands-v1.status.json")
	lastState := ""
	status := func(state string) {
		if state != lastState {
			if writePrivate(path, map[string]any{"pid": os.Getpid(), "state": state}) == nil {
				lastState = state
			}
		}
	}
	var cancel context.CancelFunc
	var finished chan error
	var started chan struct{}
	var retryAfter time.Time
	defer func() {
		if cancel != nil {
			cancel()
			<-finished
		}
		_ = os.Remove(path)
	}()
	for {
		enabled := s.hostCommandsEnabled()
		epoch, allowed := s.hostCommandAccess()
		if !allowed {
			if cancel != nil {
				cancel()
			}
			retryAfter = time.Time{}
			if enabled {
				status("waiting")
			} else {
				status("disabled")
			}
		} else if finished == nil && !time.Now().Before(retryAfter) {
			var session context.Context
			session, cancel = context.WithCancel(ctx)
			done := make(chan error, 1)
			ready := make(chan struct{}, 1)
			finished, started = done, ready
			stopOnLock := context.AfterFunc(epoch, cancel)
			go func() {
				err := s.runHostCommandBridge(session, ready)
				stopOnLock()
				done <- err
			}()
			status("waiting")
		}
		select {
		case <-ctx.Done():
			return
		case <-started:
			started = nil
			if _, ok := s.hostCommandAccess(); ok {
				status("connected")
			}
		case err := <-finished:
			cancel()
			cancel, finished, started = nil, nil, nil
			if err != nil && !errors.Is(err, context.Canceled) {
				if _, allowed := s.hostCommandAccess(); allowed {
					retryAfter = time.Now().Add(5 * time.Second)
					status("retrying")
					// No commands, output or guest-controlled diagnostics in host logs.
					_ = diagnostics.New(s.profile).Record("warn", "host-commands", "Guest bridge unavailable; retrying")
				}
			}
		case <-ticker.C:
		}
	}
}

const guestCommandDir = "${HOME}/.local/lib/cage-host-command"

func (s *Server) runHostCommandBridge(ctx context.Context, ready chan<- struct{}) error {
	return s.runGuestHostCommandBridge(ctx, ready, func(ctx context.Context, script string) *exec.Cmd {
		return s.sshCommand(ctx, "-T", s.sshTarget(), "--", script)
	})
}

func (s *Server) runGuestHostCommandBridge(ctx context.Context, ready chan<- struct{}, guest func(context.Context, string) *exec.Cmd) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Use only this workload's managed Linux SSH config, never a client
	// connection, remote target, local sshd, or network port for shell access.
	installCtx, installCancel := context.WithTimeout(ctx, 30*time.Second)
	install := guest(installCtx,
		`umask 077; dir="`+guestCommandDir+`"; mkdir -p -- "$dir" && tmp=$(mktemp "$dir/.runner.XXXXXX") && trap 'rm -f -- "$tmp"' EXIT && cat > "$tmp" && mv -f -- "$tmp" "$dir/runner.py" && python3 "$dir/runner.py" setup`)
	install.Stdin = bytes.NewReader(hostCommandGuest)
	err := install.Run()
	installCancel()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("install guest MCP helper: %w", err)
	}
	cmd := guest(ctx, `exec python3 "`+guestCommandDir+`/runner.py" bridge`)
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer out.Close()
	if err := cmd.Start(); err != nil {
		return err
	}
	defer cmd.Wait()
	var workers sync.WaitGroup
	defer func() {
		cancel()
		in.Close()
		workers.Wait()
	}()
	scan := bufio.NewScanner(out)
	scan.Buffer(make([]byte, 4096), 128<<10)
	handshakeTimer := time.AfterFunc(20*time.Second, cancel)
	ok := scan.Scan()
	handshakeTimer.Stop()
	var hello struct {
		Ready int `json:"ready"`
	}
	if !ok || json.Unmarshal(scan.Bytes(), &hello) != nil || hello.Ready != 1 {
		return errors.New("bridge handshake failed")
	}
	ready <- struct{}{}
	var writeMu sync.Mutex
	respond := func(r hostCommandResult) {
		writeMu.Lock()
		defer writeMu.Unlock()
		if ctx.Err() == nil && json.NewEncoder(in).Encode(r) != nil {
			cancel()
		}
	}
	slots := make(chan struct{}, 8)
	for scan.Scan() {
		var req hostCommandRequest
		if json.Unmarshal(scan.Bytes(), &req) != nil || req.ID == "" || len(req.ID) > 128 {
			return errors.New("invalid bridge request")
		}
		if _, ok := s.hostCommandAccess(); !ok || ctx.Err() != nil {
			return context.Canceled
		}
		if err := req.validate(); err != nil {
			respond(hostCommandResult{ID: req.ID, ExitCode: -1, Error: err.Error()})
			continue
		}
		select {
		case slots <- struct{}{}:
			workers.Add(1)
			go func(req hostCommandRequest) {
				defer workers.Done()
				defer func() { <-slots }()
				respond(runWindowsHostCommand(ctx, req))
			}(req)
		default:
			respond(hostCommandResult{ID: req.ID, ExitCode: -1, Error: "too many active host commands"})
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("guest bridge disconnected")
}

type hostCommandRequest struct {
	ID      string `json:"id"`
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
	Timeout int    `json:"timeout_seconds,omitempty"`
}

func (r *hostCommandRequest) validate() error {
	if strings.TrimSpace(r.Command) == "" || len(r.Command) > 64<<10 || strings.ContainsRune(r.Command, 0) {
		return errors.New("a PowerShell command up to 64 KiB is required")
	}
	if !filepath.IsAbs(r.Cwd) || strings.ContainsRune(r.Cwd, 0) {
		return errors.New("an absolute Windows working directory is required")
	}
	if r.Timeout == 0 {
		r.Timeout = 300
	}
	if r.Timeout < 1 || r.Timeout > 3600 {
		return errors.New("timeout_seconds must be between 1 and 3600")
	}
	return nil
}

type hostCommandResult struct {
	ID        string `json:"id"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error,omitempty"`
}
