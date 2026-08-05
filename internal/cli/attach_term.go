package cli

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/term"
)

// terminalState captures the original terminal configuration so it can be
// restored when an attach session ends. Restore is idempotent so it is safe
// both from a deferred call and from explicit error/detach paths.
type terminalState struct {
	mu       sync.Mutex
	fd       int
	state    *term.State
	restored bool
}

// makeRaw switches stdin into raw mode and captures the original state. It
// fails when stdin is not a terminal (for example piped input), because raw
// attach requires an interactive terminal.
func makeRaw() (*terminalState, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, fmt.Errorf("stdin is not a terminal; attach requires an interactive terminal")
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, fmt.Errorf("enter raw mode: %w", err)
	}
	return &terminalState{fd: fd, state: state}, nil
}

// Restore puts the terminal back into its original mode. Safe to call more
// than once; subsequent calls are no-ops.
func (t *terminalState) Restore() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.restored {
		return nil
	}
	t.restored = true
	if err := term.Restore(t.fd, t.state); err != nil {
		return fmt.Errorf("restore terminal: %w", err)
	}
	return nil
}

// terminalSize returns the current terminal dimensions of stdout.
func terminalSize() (rows, cols int, err error) {
	cols, rows, err = term.GetSize(int(os.Stdout.Fd()))
	return rows, cols, err
}
