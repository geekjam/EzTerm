package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"

	"ezterm/internal/api"
)

// ptySession abstracts the platform PTY master used in pty mode.
type ptySession interface {
	io.ReadWriteCloser
	Resize(rows, cols int) error
	PID() int
	// Wait blocks until the attached process exits and returns its exit code.
	Wait() (int, error)
	// Kill force-terminates the attached process.
	Kill() error
	// Signal sends a signal to the attached process (Unix only).
	Signal(sig syscall.Signal) error
}

// localProc runs a local command either through a PTY (pty mode) or through
// plain stdin/stdout/stderr pipes (pipe mode).
type localProc struct {
	cmd      *exec.Cmd // pipe mode, or Unix PTY mode
	ptmx     ptySession
	stdin    io.WriteCloser
	copyWG   sync.WaitGroup
	done     chan struct{}
	exitCode int
	pid      int
}

// newLocalProc builds and starts a local process writing merged output to out.
func newLocalProc(command string, args []string, mode api.Mode, rows, cols int, env []string, out io.Writer) (proc, error) {
	if command == "" {
		command, args = defaultShell()
	}
	if env == nil {
		env = os.Environ()
	}
	l := &localProc{done: make(chan struct{})}

	if mode == api.ModePTY {
		ps, err := startPTY(command, args, rows, cols, env)
		if err != nil {
			return nil, err
		}
		l.ptmx = ps
		l.pid = ps.PID()
		l.copyWG.Add(1)
		go func() {
			defer l.copyWG.Done()
			_, _ = io.Copy(out, ps)
			_ = ps.Close() // idempotent; unblocks reads on platforms that need it
		}()
	} else {
		cmd := exec.Command(command, args...)
		cmd.Env = env
		stdin, pipeErr := cmd.StdinPipe()
		if pipeErr != nil {
			return nil, fmt.Errorf("create stdin pipe: %w", pipeErr)
		}
		stdout, pipeErr := cmd.StdoutPipe()
		if pipeErr != nil {
			_ = stdin.Close()
			return nil, fmt.Errorf("create stdout pipe: %w", pipeErr)
		}
		stderr, pipeErr := cmd.StderrPipe()
		if pipeErr != nil {
			_ = stdin.Close()
			_ = stdout.Close()
			return nil, fmt.Errorf("create stderr pipe: %w", pipeErr)
		}
		if startErr := cmd.Start(); startErr != nil {
			return nil, fmt.Errorf("start %q: %w", command, startErr)
		}
		l.cmd = cmd
		l.stdin = stdin
		l.pid = cmd.Process.Pid
		l.copyWG.Add(2)
		go func() {
			defer l.copyWG.Done()
			_, _ = io.Copy(out, stdout)
		}()
		go func() {
			defer l.copyWG.Done()
			_, _ = io.Copy(out, stderr)
		}()
	}

	go func() {
		var code int
		if l.ptmx != nil {
			code, _ = l.ptmx.Wait()
		} else {
			waitErr := l.cmd.Wait()
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				code = exitErr.ExitCode()
			}
		}
		l.exitCode = code
		// Signal completion only after every output copy has drained, so the
		// caller never misses trailing output.
		if l.ptmx != nil {
			// Closing the PTY unblocks reads on platforms where the master stays
			// open after the child exits (e.g. Windows ConPTY).
			copyDone := make(chan struct{})
			go func() {
				l.copyWG.Wait()
				close(copyDone)
			}()
			select {
			case <-copyDone:
			case <-time.After(200 * time.Millisecond):
				_ = l.ptmx.Close()
			}
		}
		l.copyWG.Wait()
		close(l.done)
	}()
	slog.Debug("local process started", "command", command, "pid", l.pid, "mode", mode)
	return l, nil
}

func defaultShell() (string, []string) {
	if runtime.GOOS == "windows" {
		if comspec := os.Getenv("COMSPEC"); comspec != "" {
			return comspec, nil
		}
		return "cmd.exe", nil
	}
	if home := os.Getenv("SHELL"); home != "" {
		return home, nil
	}
	return "/bin/sh", nil
}

func (l *localProc) Stdin() io.Writer {
	if l.ptmx != nil {
		return l.ptmx
	}
	return l.stdin
}

func (l *localProc) Done() <-chan struct{} {
	return l.done
}

func (l *localProc) ExitCode() int {
	return l.exitCode
}

// Terminate stops the process with a graceful phase (if !force) followed by a
// forced kill. It is safe to call multiple times; the first call wins.
func (l *localProc) Terminate(grace time.Duration, force bool) {
	select {
	case <-l.done:
		return
	default:
	}
	if l.ptmx == nil && (l.cmd == nil || l.cmd.Process == nil) {
		return
	}
	if force {
		l.forceKill()
		return
	}
	l.gracefulKill(grace)
}

// forceKill terminates the process without a grace period.
func (l *localProc) forceKill() {
	if l.ptmx != nil {
		_ = l.ptmx.Kill()
	} else if l.cmd != nil && l.cmd.Process != nil {
		_ = l.cmd.Process.Kill()
	}
	l.waitExit(10 * time.Second)
}

// gracefulKill is the platform-specific graceful shutdown path, implemented
// in terminate_unix.go and terminate_windows.go.

func (l *localProc) waitExit(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case <-l.done:
	case <-ctx.Done():
	}
}

func (l *localProc) Close() error {
	l.Terminate(0, true)
	return nil
}

func (l *localProc) Resize(rows, cols int) error {
	if l.ptmx == nil {
		return nil
	}
	if err := l.ptmx.Resize(rows, cols); err != nil {
		return fmt.Errorf("set PTY size: %w", err)
	}
	return nil
}

func (l *localProc) PID() int {
	return l.pid
}
