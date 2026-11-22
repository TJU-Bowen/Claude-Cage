//go:build !windows

package workstationlan

import "os/exec"

func hideChild(c *exec.Cmd) {}
