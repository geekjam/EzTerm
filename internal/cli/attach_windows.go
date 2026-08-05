//go:build windows

package cli

import (
	"time"
)

// watchResize invokes fn whenever the terminal window size changes and
// returns a stop function. Windows consoles do not deliver SIGWINCH, so the
// window size is polled periodically and fn fires only on an actual change.
func watchResize(fn func()) func() {
	done := make(chan struct{})
	rows, cols, _ := terminalSize()
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				r, c, err := terminalSize()
				if err != nil {
					continue
				}
				if r != rows || c != cols {
					rows, cols = r, c
					fn()
				}
			}
		}
	}()
	return func() { close(done) }
}
