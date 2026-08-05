//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// detachProcess puts the daemon in its own process group on Unix.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
