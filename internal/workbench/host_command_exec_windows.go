package workbench

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sylvinus/agent-vm/internal/host"
	"golang.org/x/sys/windows"
)

const (
	hostCommandPlatform = "windows"
	hostCommandOS       = "Windows"
	hostCommandShell    = "PowerShell"
	hostCommandMarker   = "cage-host-commands-v1\n"
)

// The command is transmitted via stdin rather than a quoted argument, so
// multi-line, non-ASCII and long programs are passed without reinterpretation.
// The worker waits for stdin before loading profiles or running user code.
const hostPowerShellWrapper = `
$source = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))
$cwd = [Environment]::CurrentDirectory
foreach ($profilePath in @($PROFILE.AllUsersAllHosts,$PROFILE.AllUsersCurrentHost,$PROFILE.CurrentUserAllHosts,$PROFILE.CurrentUserCurrentHost)) {
    if (Test-Path -LiteralPath $profilePath -PathType Leaf) { . $profilePath }
}
Set-Location -LiteralPath $cwd -ErrorAction Stop
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = [Console]::OutputEncoding
$global:LASTEXITCODE = 0
$captureStatus = 'if (!$?) { if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }; exit 1 }; exit 0'
try {
    . ([ScriptBlock]::Create($source + [Environment]::NewLine + [Environment]::NewLine + $captureStatus))
} catch {
    [Console]::Error.WriteLine($_.ToString())
    exit 1
}
exit 0
`

func runHostCommand(parent context.Context, req hostCommandRequest) hostCommandResult {
	r := hostCommandResult{ID: req.ID, ExitCode: -1}
	ctx, cancel := context.WithTimeout(parent, time.Duration(req.Timeout)*time.Second)
	defer cancel()
	shell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.CommandContext(ctx, shell, "-NoLogo", "-NonInteractive", "-NoProfile", "-Command", hostPowerShellWrapper)
	cmd.Dir = req.Cwd
	// Leave Env nil: inherit the Cage user's actual Windows environment.
	host.HideCommandWindow(cmd)
	var stdout, stderr hostCommandOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 2 * time.Second
	in, err := cmd.StdinPipe()
	if err != nil {
		r.Error = err.Error()
		return r
	}
	defer in.Close()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	defer windows.CloseHandle(job)
	// This job groups Windows child processes for cancellation only. No
	// restrictions on resources, filesystem, privileges or networking.
	cmd.Cancel = func() error {
		_ = windows.TerminateJobObject(job, 1)
		// May be called before assignment; kill the stdin-gated worker too.
		return cmd.Process.Kill()
	}
	if err := cmd.Start(); err != nil {
		r.Error = err.Error()
		return r
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
		windows.CloseHandle(process)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = in.Close()
		_ = cmd.Wait()
		r.Error = "cannot manage the Windows process: " + err.Error()
		return r
	}
	_, inputErr := io.WriteString(in, base64.StdEncoding.EncodeToString([]byte(req.Command)))
	_ = in.Close()
	err = cmd.Wait()
	r.Stdout = strings.ToValidUTF8(stdout.data.String(), "\uFFFD")
	r.Stderr = strings.ToValidUTF8(stderr.data.String(), "\uFFFD")
	r.Truncated = stdout.truncated || stderr.truncated
	if cmd.ProcessState != nil {
		r.ExitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case ctx.Err() != nil:
		r.Error = ctx.Err().Error()
	case inputErr != nil:
		r.Error = inputErr.Error()
	case errors.Is(err, exec.ErrWaitDelay):
		r.Truncated = true
	case err != nil:
		var exited *exec.ExitError
		if !errors.As(err, &exited) {
			r.Error = err.Error()
		}
	}
	return r
}
