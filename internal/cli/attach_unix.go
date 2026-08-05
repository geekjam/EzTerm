//go:build !windows

package cli

import (
	"os"
	"os/signal"
	"syscall"
)

// watchResize invokes fn whenever the terminal window size changes and
// returns a stop function. Unix terminals deliver SIGWINCH on size changes;
// the first resize on attach is performed by the caller, so the watcher only
// reacts to later changes.
func watchResize(fn func()) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				fn()
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
