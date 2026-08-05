//go:build windows

package cli

import (
	"os/exec"
	"syscall"
)

// detachProcess hides the daemon console window on Windows.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
