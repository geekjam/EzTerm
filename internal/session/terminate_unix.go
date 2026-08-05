//go:build !windows

package session

import (
	"syscall"
	"time"
)

// gracefulKill sends SIGTERM, waits up to grace, then forces SIGKILL.
func (l *localProc) gracefulKill(grace time.Duration) {
	if l.ptmx != nil {
		_ = l.ptmx.Signal(syscall.SIGTERM)
	} else if l.cmd != nil && l.cmd.Process != nil {
		_ = l.cmd.Process.Signal(syscall.SIGTERM)
	}
	l.waitExit(grace)
	select {
	case <-l.done:
		return
	default:
		l.forceKill()
	}
}
