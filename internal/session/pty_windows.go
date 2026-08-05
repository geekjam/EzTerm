//go:build windows

package session

import (
	"errors"
	"fmt"
	"syscall"

	"github.com/charmbracelet/x/conpty"
	"golang.org/x/sys/windows"
)

// winPTY adapts charmbracelet/x/conpty to the ptySession interface.
type winPTY struct {
	c      *conpty.ConPty
	handle windows.Handle
	pid    int
	wait   chan struct{}
	code   int
}

func startPTY(command string, args []string, rows, cols int, env []string) (ptySession, error) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	c, err := conpty.New(cols, rows, 0)
	if err != nil {
		return nil, fmt.Errorf("create ConPTY: %w", err)
	}
	argv := append([]string{command}, args...)
	pid, handle, err := c.Spawn(command, argv, &syscall.ProcAttr{Env: env})
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("spawn ConPTY process for %q: %w", command, err)
	}
	w := &winPTY{
		c:      c,
		handle: windows.Handle(handle),
		pid:    pid,
		wait:   make(chan struct{}),
	}
	go func() {
		_, _ = windows.WaitForSingleObject(w.handle, windows.INFINITE)
		var code uint32
		_ = windows.GetExitCodeProcess(w.handle, &code)
		w.code = int(code)
		_ = windows.CloseHandle(w.handle)
		close(w.wait)
	}()
	return w, nil
}

func (w *winPTY) Read(p []byte) (int, error)  { return w.c.Read(p) }
func (w *winPTY) Write(p []byte) (int, error) { return w.c.Write(p) }
func (w *winPTY) Close() error                { return w.c.Close() }

func (w *winPTY) Resize(rows, cols int) error {
	return w.c.Resize(cols, rows)
}

func (w *winPTY) PID() int { return w.pid }

func (w *winPTY) Wait() (int, error) {
	<-w.wait
	return w.code, nil
}

func (w *winPTY) Kill() error {
	return windows.TerminateProcess(w.handle, 1)
}

// Signal is unsupported on Windows; ConPTY processes are stopped by closing
// the pseudo-console or via Kill.
func (w *winPTY) Signal(syscall.Signal) error {
	return errors.New("signals unsupported on Windows ConPTY")
}
