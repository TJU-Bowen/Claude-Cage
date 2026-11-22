package workstationlan

import (
	"os/exec"
	"syscall"
)

func hideChild(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
