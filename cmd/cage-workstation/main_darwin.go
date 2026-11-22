// CageWorkstation opens the LAN server list and launches a private Paseo profile for a paired guest.
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
	"syscall"

	"github.com/sylvinus/agent-vm/internal/host"
	"github.com/sylvinus/agent-vm/internal/workstationlan"
	"golang.org/x/sys/unix"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if len(os.Args) == 1 || (len(os.Args) == 2 && os.Args[1] == "panel") {
			_ = exec.Command("/usr/bin/osascript", "-e", "on run argv", "-e",
				"display alert \"Cage 工作台\" message (item 1 of argv) as critical", "-e", "end run", "--", err.Error()).Run()
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		cmd := exec.Command(self, "panel")
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}

	dir := os.Getenv("CAGE_LAN_CLIENT_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(home, "Library", "Application Support", "Claude Cage Workstation", "LAN")
	}
	name, err := os.Hostname()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch {
	case len(args) == 1 && args[0] == "panel":
		return servePanel(ctx, self, dir, name)
	case len(args) == 2 && args[0] == "proxy":
		client, err := workstationlan.NewClient(dir, self, name)
		if err != nil {
			return err
		}
		return client.Tunnel(ctx, args[1], os.Stdin, os.Stdout)
	default:
		return errors.New("用法：CageWorkstation [panel | proxy <server-id>]")
	}
}

func servePanel(ctx context.Context, self, dir, name string) error {
	paseo, _ := workstationlan.PaseoPaths(self)
	adapter := filepath.Join(filepath.Dir(filepath.Dir(self)), "Helpers", "cc-remote")
	for _, component := range []string{paseo, adapter} {
		if err := unix.Access(component, unix.X_OK); err != nil {
			return fmt.Errorf("无法运行工作台组件 %s: %w", component, err)
		}
	}
	env := []string{
		"CAGE_LAN_CLIENT_DIR=" + dir,
		"PASEO_RELAY_ENABLED=false",
		"PATH=" + filepath.Dir(adapter) + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
	show := func() error {
		return startPaseo(paseo, append(env[:len(env):len(env)], "CAGE_LAN_SELECTOR=auto"))
	}
	listener, control, err := workstationlan.BindPanel(dir)
	if err != nil {
		return err
	}
	if listener == nil {
		return show()
	}
	defer listener.Close()
	client, err := workstationlan.NewClient(dir, self, name)
	if err != nil {
		return err
	}
	go client.SyncCatalogs(ctx)
	panel := workstationlan.Panel{
		Client: client,
		Launch: func(_ context.Context, server *workstationlan.SavedServer) error {
			profile := client.Profile(server.ID)
			home := filepath.Join(profile, "Paseo")
			endpoint := "127.0.0.1:" + strconv.Itoa(server.DesktopPort)
			if err := host.EnsurePrivateDir(home); err != nil {
				return err
			}
			if err := seedConfig(home, endpoint, adapter); err != nil {
				return err
			}
			return startPaseo(paseo, append(env[:len(env):len(env)],
				"CAGE_LAN_SELECTOR=",
				"CAGE_WORKSTATION_DEVICE_ID="+client.DeviceID(),
				"CC_REMOTE_HOST=cage-lan",
				"CAGE_WORKSTATION_SSH=/usr/bin/ssh",
				"CAGE_WORKSTATION_SSH_CONFIG="+filepath.Join(profile, "ssh.config"),
				"CLAUDE_CONFIG_DIR="+filepath.Join(profile, "Sessions"),
				"PASEO_HOME="+home,
				"PASEO_ELECTRON_USER_DATA_DIR="+filepath.Join(profile, "Desktop"),
				"PASEO_LISTEN="+endpoint,
			))
		},
	}
	return workstationlan.RunPanel(ctx, dir, listener, control, panel, show)
}

func startPaseo(binary string, env []string) error {
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func publishConfig(source, path string) error {
	if err := os.Link(source, path); !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}
