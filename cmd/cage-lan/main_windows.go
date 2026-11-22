// CageLAN adds device discovery and approval to the installed Cage and workstation applications.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/sylvinus/agent-vm/internal/workstationlan"
	"golang.org/x/sys/windows"
)

func shellOpen(verb, file, args string, show int32) error {
	op, err := windows.UTF16PtrFromString(verb)
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	params, err := windows.UTF16PtrFromString(args)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, op, target, params, nil, show)
}

func start(path string, args []string, env []string) error {
	cmd := exec.Command(path, args...)
	cmd.Env, cmd.Dir = env, filepath.Dir(path)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func running(path string) (bool, error) {
	snap, e := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if e != nil {
		return false, e
	}
	defer windows.CloseHandle(snap)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for e = windows.Process32First(snap, &entry); e == nil; e = windows.Process32Next(snap, &entry) {
		if !strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), filepath.Base(path)) {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
		if err != nil {
			continue
		}
		buf := make([]uint16, 32768)
		size := uint32(len(buf))
		err = windows.QueryFullProcessImageName(h, 0, &buf[0], &size)
		windows.CloseHandle(h)
		if err == nil && strings.EqualFold(windows.UTF16ToString(buf[:size]), path) {
			return true, nil
		}
	}
	if errors.Is(e, windows.ERROR_NO_MORE_FILES) {
		return false, nil
	}
	return false, e
}

func watchCage(ctx context.Context, cancel context.CancelFunc, path string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if alive, e := running(path); e == nil && !alive {
				cancel()
				return
			}
		}
	}
}

func clientDir(app string) string {
	if d := os.Getenv("CAGE_LAN_CLIENT_DIR"); d != "" {
		return d
	}
	return filepath.Join(app, "Data", "LAN")
}

func workstationLaunch(app string, c *workstationlan.Client, ssh string) func(context.Context, *workstationlan.SavedServer) error {
	return func(ctx context.Context, s *workstationlan.SavedServer) error {
		launcher := filepath.Join(app, "CageWorkstation.exe")
		if s == nil {
			return start(launcher, []string{"local"}, os.Environ())
		}
		gui := filepath.Join(app, "PaseoLAN", "Paseo.exe")
		if _, e := os.Stat(gui); e != nil {
			return fmt.Errorf("找不到工作台组件 %s", gui)
		}
		if _, e := os.Stat(ssh); e != nil {
			systemSSH := filepath.Join(os.Getenv("SystemRoot"), "System32", "OpenSSH", "ssh.exe")
			if _, e = os.Stat(systemSSH); e != nil {
				return errors.New("工作机缺少 OpenSSH 客户端，请安装 Windows OpenSSH Client")
			}
			ssh = systemSSH
		}
		dir := c.Profile(s.ID)
		env := append(os.Environ(),
			"CAGE_LAN_CLIENT_DIR="+c.Dir,
			"CAGE_WORKSTATION_DEVICE_ID="+c.DeviceID(),
			"CC_REMOTE_HOST=cage-lan",
			"CAGE_WORKSTATION_SSH="+ssh,
			"CAGE_WORKSTATION_SSH_CONFIG="+filepath.Join(dir, "ssh.config"),
			"CLAUDE_CONFIG_DIR="+filepath.Join(dir, "Sessions"),
			"PASEO_HOME="+filepath.Join(dir, "Paseo"),
			"PASEO_ELECTRON_USER_DATA_DIR="+filepath.Join(dir, "Desktop"),
			"PASEO_LISTEN=127.0.0.1:"+strconv.Itoa(s.DesktopPort),
			"PASEO_RELAY_ENABLED=false",
		)
		return start(launcher, []string{"paired"}, env)
	}
}

// Fixed, program-specific inbound rules. The elevated helper runs only from the server panel button.
func firewall(app string, install bool) error {
	ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	script := `$ErrorActionPreference = 'Stop'
$program = $env:CAGE_LAN_FIREWALL_APP
foreach ($spec in @(@('ClaudeCage-LAN-SSH','TCP',17942),@('ClaudeCage-LAN-Discovery','UDP',5353))) {
    $rule = Get-NetFirewallRule -Name $spec[0] -ErrorAction SilentlyContinue
    if ($rule -and $rule.Enabled -eq 'True' -and $rule.Direction -eq 'Inbound' -and $rule.Action -eq 'Allow' -and ($rule | Get-NetFirewallApplicationFilter).Program -eq $program) {
        continue
    }
    if ($env:CAGE_LAN_FIREWALL_INSTALL -ne 'true') { exit 1 }
    if ($rule) { Remove-NetFirewallRule -Name $spec[0] }
    New-NetFirewallRule -Name $spec[0] -DisplayName $spec[0] -Enabled True -Direction Inbound -Action Allow -Program $program -Protocol $spec[1] -LocalPort $spec[2] -RemoteAddress LocalSubnet -Profile Any | Out-Null
}`
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Env = append(os.Environ(), "CAGE_LAN_FIREWALL_APP="+app, "CAGE_LAN_FIREWALL_INSTALL="+strconv.FormatBool(install))
	return cmd.Run()
}

func run(args []string) error {
	self, e := os.Executable()
	if e != nil {
		return e
	}
	app := filepath.Dir(self)
	root := filepath.Dir(app)
	name, e := os.Hostname()
	if e != nil {
		return e
	}
	mode := "client"
	if len(args) > 0 {
		mode = args[0]
	}
	if mode == "proxy" {
		if len(args) != 2 {
			return errors.New("缺少服务器标识")
		}
		cl, e := workstationlan.NewClient(clientDir(app), self, name)
		if e != nil {
			return e
		}
		return cl.Tunnel(context.Background(), args[1], os.Stdin, os.Stdout)
	}
	if mode == "allow-firewall" && len(args) == 1 {
		return firewall(self, true)
	}
	headless := len(args) == 2 && args[1] == "--headless"
	if (mode != "host" && mode != "client" && mode != "manage") || len(args) > 1 && !headless {
		return errors.New("用法：CageLAN.exe host | client | manage")
	}
	dir := clientDir(app)
	config := os.Getenv("CLAUDE_CAGE_CONFIG")
	if config == "" {
		config = filepath.Join(root, "Data", "Profile", "config.json")
	}
	cage := filepath.Join(root, "EXE", "Cage.exe")
	if mode == "host" {
		dir = filepath.Join(filepath.Dir(filepath.Dir(config)), "LAN-Server")
	}
	l, ctrl, e := workstationlan.BindPanel(dir)
	if e != nil {
		return e
	}
	show := func() error {
		if headless {
			return nil
		}
		if mode == "host" {
			return start(cage, []string{"--workstation-connections"}, append(os.Environ(), "CLAUDE_CAGE_CONFIG="+config))
		}
		startMode := "manage"
		if mode == "client" {
			startMode = "auto"
		}
		return start(filepath.Join(app, "PaseoLAN", "Paseo.exe"), nil, append(os.Environ(), "CAGE_LAN_SELECTOR="+startMode, "CAGE_LAN_CLIENT_DIR="+dir))
	}
	if l == nil {
		return show()
	}
	defer l.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	panel := workstationlan.Panel{}
	ssh := filepath.Join(root, "EXE", "resources", "runtime", "openssh", "ssh.exe")
	if mode == "host" {
		alive, e := running(cage)
		if e != nil {
			return e
		}
		if !alive && headless {
			if e = start(cage, nil, append(os.Environ(), "CLAUDE_CAGE_CONFIG="+config)); e != nil {
				return e
			}
		}
		service, e := workstationlan.NewServer(dir, name, workstationlan.LimaGuest{Profile: filepath.Dir(config), SSH: ssh})
		if e != nil {
			return e
		}
		panel.Host = service
		panel.Firewall = func() error { return shellOpen("runas", self, "allow-firewall", windows.SW_HIDE) }
		go watchCage(ctx, cancel, cage)
		go service.RunLAN(ctx)
	} else {
		cl, e := workstationlan.NewClient(dir, self, name)
		if e != nil {
			return e
		}
		panel.Client = cl
		go cl.SyncCatalogs(ctx)
		panel.Launch = workstationLaunch(app, cl, ssh)
		_, e = os.Stat(filepath.Join(root, "Data", "Profile", "config.json"))
		panel.HasLocal = e == nil
	}
	// Native clients wait for the local service to finish publishing its control record.
	if e = show(); e != nil && !headless {
		return e
	}
	return workstationlan.RunPanel(ctx, dir, l, ctrl, panel)
}

func main() {
	if e := run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, e)
		if len(os.Args) < 2 || (os.Args[1] != "proxy" && (len(os.Args) < 3 || os.Args[2] != "--headless")) {
			title, _ := windows.UTF16PtrFromString("Claude Cage 工作机连接")
			msg, _ := windows.UTF16PtrFromString(e.Error())
			windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(msg)), uintptr(unsafe.Pointer(title)), 0x10)
		}
		os.Exit(1)
	}
}
