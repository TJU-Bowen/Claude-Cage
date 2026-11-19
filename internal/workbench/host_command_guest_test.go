//go:build linux

package workbench

import (
	"os/exec"
	"testing"
)

func TestHostCommandGuestProtocol(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	cmd := exec.Command(python, "-I", "host_command_guest_test.py")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("host command guest protocol: %v\n%s", err, output)
	}
}
