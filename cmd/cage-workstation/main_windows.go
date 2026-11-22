// CageWorkstation opens a private Paseo desktop profile connected to the existing Cage guest.
package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/host"
	"golang.org/x/sys/windows"
)

//go:embed paseo.json
var initialConfig []byte

const listen = "127.0.0.1:6798"

func main() {
	if err := run(os.Args[1:]); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		if len(os.Args) == 1 && os.Getenv("CAGE_WORKSTATION_CLI_HOST") == "" {
			title, _ := windows.UTF16PtrFromString("Cage 工作机")
			body, _ := windows.UTF16PtrFromString(err.Error())
			windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(title)), 0x10)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	app := filepath.Dir(self)
	client := filepath.Join(app, "bin", "cc-remote.exe")
	if target := os.Getenv("CAGE_WORKSTATION_CLI_HOST"); target != "" {
		cmd := exec.Command(client, append([]string{"--host", target, "--cli"}, args...)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		return cmd.Run()
	}
	if len(args) == 0 {
		manager := filepath.Join(app, "CageLAN.exe")
		if _, err := os.Stat(manager); err == nil {
			cmd := exec.Command(manager, "client")
			if err := cmd.Start(); err != nil {
				return err
			}
			return cmd.Process.Release()
		}
	}
	paired := len(args) == 1 && args[0] == "paired"
	diagnose := len(args) == 1 && args[0] == "doctor"
	if len(args) != 0 && !paired && !diagnose && !(len(args) == 1 && args[0] == "local") {
		return errors.New("用法：CageWorkstation.exe [local | doctor]")
	}
	root := filepath.Dir(app)
	profile := filepath.Join(root, "Data", "Profile")
	instance := cage.InstanceName(profile, cage.EnvironmentDir(profile))
	sshConfig := filepath.Join(profile, "state", "lima", instance, "ssh.config")
	ssh := filepath.Join(root, "EXE", "resources", "runtime", "openssh", "ssh.exe")
	gui := filepath.Join(app, "Paseo", "Paseo.exe")
	if paired {
		gui = filepath.Join(app, "PaseoLAN", "Paseo.exe")
		home, endpoint := os.Getenv("PASEO_HOME"), os.Getenv("PASEO_LISTEN")
		if home == "" || endpoint == "" || os.Getenv("CAGE_WORKSTATION_SSH_CONFIG") == "" {
			return errors.New("请从局域网服务器列表打开已配对的工作台")
		}
		if err := host.EnsurePrivateDir(home); err != nil {
			return err
		}
		if err := seedConfig(home, endpoint, client); err != nil {
			return err
		}
		cmd := exec.Command(gui)
		cmd.Dir = app
		cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(client)+string(os.PathListSeparator)+os.Getenv("PATH"))
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	for _, path := range []string{sshConfig, ssh, client, gui} {
		fi, err := os.Stat(path)
		if err != nil || !fi.Mode().IsRegular() {
			return fmt.Errorf("找不到组件或现有客机的连接配置：%s。请检查安装目录，并先从 Cage 启动原环境。", path)
		}
	}
	data := filepath.Join(app, "Data")
	lanDir := os.Getenv("CAGE_LAN_CLIENT_DIR")
	if lanDir == "" {
		lanDir = filepath.Join(data, "LAN")
	}
	if enhanced := filepath.Join(app, "PaseoLAN", "Paseo.exe"); !diagnose {
		if _, err := os.Stat(enhanced); err == nil {
			gui = enhanced
			panel := exec.Command(filepath.Join(app, "CageLAN.exe"), "client", "--headless")
			panel.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			panel.Env = append(os.Environ(), "CAGE_LAN_CLIENT_DIR="+lanDir)
			if err := panel.Start(); err != nil {
				return err
			}
			_ = panel.Process.Release()
		}
	}
	home := filepath.Join(data, "Paseo")
	if err := host.EnsurePrivateDir(data); err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return err
	}
	if err := seedConfig(home, listen, client); err != nil {
		return err
	}
	environ := append(os.Environ(),
		"CAGE_LAN_CLIENT_DIR="+lanDir,
		"PATH="+filepath.Dir(client)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CC_REMOTE_HOST=lima-"+instance,
		"CAGE_WORKSTATION_SSH="+ssh,
		"CAGE_WORKSTATION_SSH_CONFIG="+sshConfig,
		"CLAUDE_CONFIG_DIR="+filepath.Join(data, "Sessions"),
		"PASEO_HOME="+home,
		"PASEO_ELECTRON_USER_DATA_DIR="+filepath.Join(data, "Desktop"),
		"PASEO_LISTEN="+listen,
		"PASEO_RELAY_ENABLED=false",
	)
	if diagnose {
		cmd := exec.Command(client, "doctor")
		cmd.Env = environ
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		return cmd.Run()
	}
	cmd := exec.Command(gui)
	cmd.Env, cmd.Dir = environ, app
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// Publish a complete initial config once; subsequent launches retain the user's UI settings.
func seedConfig(home, endpoint, adapter string) error {
	path := filepath.Join(home, "config.json")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	var cfg struct {
		Version int            `json:"version"`
		Daemon  map[string]any `json:"daemon"`
		Agents  struct {
			Providers map[string]map[string]any `json:"providers"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(initialConfig, &cfg); err != nil {
		return err
	}
	cfg.Daemon["listen"] = endpoint
	cfg.Agents.Providers["cage-claude"]["command"] = []string{adapter}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(home, ".workstation-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(data)
	if err = errors.Join(writeErr, tmp.Close()); err != nil {
		return err
	}
	src, err := windows.UTF16PtrFromString(tmp.Name())
	if err != nil {
		return err
	}
	dst, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err = windows.MoveFile(src, dst); errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_FILE_EXISTS) {
		return nil
	}
	return err
}
