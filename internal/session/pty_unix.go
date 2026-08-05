//go:build !windows

package session

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// unixPTY adapts creack/pty to the ptySession interface.
type unixPTY struct {
	ptmx *os.File
	cmd  *exec.Cmd
}

func startPTY(command string, args []string, rows, cols int, env []string) (ptySession, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = env
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, fmt.Errorf("start PTY for %q: %w", command, err)
	}
	return &unixPTY{ptmx: ptmx, cmd: cmd}, nil
}

func (u *unixPTY) Read(p []byte) (int, error)  { return u.ptmx.Read(p) }
func (u *unixPTY) Write(p []byte) (int, error) { return u.ptmx.Write(p) }

func (u *unixPTY) Close() error { return u.ptmx.Close() }

func (u *unixPTY) Resize(rows, cols int) error {
	return pty.Setsize(u.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

func (u *unixPTY) PID() int { return u.cmd.Process.Pid }

func (u *unixPTY) Wait() (int, error) {
	err := u.cmd.Wait()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return -1, err
}

func (u *unixPTY) Kill() error {
	if u.cmd.Process == nil {
		return nil
	}
	return u.cmd.Process.Kill()
}

func (u *unixPTY) Signal(sig syscall.Signal) error {
	if u.cmd.Process == nil {
		return nil
	}
	return u.cmd.Process.Signal(sig)
}
