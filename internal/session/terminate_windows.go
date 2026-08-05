//go:build windows

package session

import "time"

// gracefulKill closes the ConPTY master (or stdin in pipe mode), waits up to
// grace, then force-terminates the process.
func (l *localProc) gracefulKill(grace time.Duration) {
	if l.ptmx != nil {
		_ = l.ptmx.Close() // closing the ConPTY master terminates the attached process tree
	} else if l.stdin != nil {
		_ = l.stdin.Close() // EOF on stdin hints line-oriented console programs to exit
	}
	l.waitExit(grace)
	select {
	case <-l.done:
		return
	default:
		l.forceKill()
	}
}
