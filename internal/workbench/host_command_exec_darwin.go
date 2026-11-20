package workbench

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const (
	hostCommandPlatform = "macos"
	hostCommandOS       = "macOS"
	hostCommandShell    = "zsh"
	hostCommandMarker   = "cage-host-commands-v1-macos\n"
)

func runHostCommand(parent context.Context, req hostCommandRequest) hostCommandResult {
	result := hostCommandResult{ID: req.ID, ExitCode: -1}
	ctx, cancel := context.WithTimeout(parent, time.Duration(req.Timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/zsh", "-f", "-s")
	cmd.Dir = req.Cwd
	cmd.Stdin = strings.NewReader(req.Command)
	var stdout, stderr hostCommandOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 2 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}

	err := cmd.Run()
	result.Stdout = strings.ToValidUTF8(stdout.data.String(), "\uFFFD")
	result.Stderr = strings.ToValidUTF8(stderr.data.String(), "\uFFFD")
	result.Truncated = stdout.truncated || stderr.truncated
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	var exited *exec.ExitError
	switch {
	case ctx.Err() != nil:
		result.Error = ctx.Err().Error()
	case errors.Is(err, exec.ErrWaitDelay):
		result.Truncated = true
	case err != nil && !errors.As(err, &exited):
		result.Error = err.Error()
	}
	return result
}
