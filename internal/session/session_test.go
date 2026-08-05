package session

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"ezterm/internal/api"
	"ezterm/internal/buffer"
)

// TestHelperProcess runs as a subprocess when invoked by the tests below.
// It is triggered by a "-- <mode>" argument, so it can also be launched
// through the daemon HTTP API.
func TestHelperProcess(t *testing.T) {
	mode := helperModeFromArgs()
	if mode == "" {
		t.Skip("not a helper subprocess")
	}
	switch mode {
	case "output":
		fmt.Fprintln(os.Stdout, "HELLO-STDOUT")
		fmt.Fprintln(os.Stderr, "HELLO-STDERR")
		os.Exit(3)
	case "interactive":
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			fmt.Fprintf(os.Stdout, "echo:%s\n", scanner.Text())
		}
		os.Exit(0)
	default:
		fmt.Fprintln(os.Stdout, "UNKNOWN-MODE")
		os.Exit(2)
	}
}

func helperModeFromArgs() string {
	for i := 0; i < len(os.Args)-1; i++ {
		if os.Args[i] == "--" {
			return os.Args[i+1]
		}
	}
	return ""
}

func helperArgs(mode string) []string {
	return []string{"-test.run=^TestHelperProcess$", "--", mode}
}

// newSession creates a local session for testing.
func newSession(t *testing.T, cfg Config) *Session {
	t.Helper()
	s, err := New(cfg, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.setOnExit(func() {})
	return s
}

func waitForExit(t *testing.T, s *Session) {
	t.Helper()
	select {
	case <-s.proc.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for process exit")
	}
	// The exit watcher finalizes status and closes the buffer shortly after
	// Done fires; poll until that completes.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := s.Info().Status
		if status != api.StatusRunning && status != api.StatusStarting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for session finalization")
}

func TestSessionIDFormat(t *testing.T) {
	const sampleSize = 1000
	seen := make(map[string]struct{}, sampleSize)

	for i := 0; i < sampleSize; i++ {
		id := newSessionID()
		if len(id) != 12 {
			t.Fatalf("session ID length = %d for %q, want 12", len(id), id)
		}
		if strings.Contains(id, "-") {
			t.Fatalf("session ID contains a hyphen: %q", id)
		}
		for _, ch := range id {
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
				t.Fatalf("session ID contains non-lowercase-hex character %q: %q", ch, id)
			}
		}
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate session ID generated: %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestPipeLifecycleAndExitCode(t *testing.T) {
	s := newSession(t, Config{
		Command: os.Args[0],
		Args:    helperArgs("output"),
		Mode:    api.ModePipe,
	})
	waitForExit(t, s)

	info := s.Info()
	if info.Status != api.StatusExited {
		t.Fatalf("status = %s, want exited", info.Status)
	}
	if info.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", info.ExitCode)
	}
	if info.PID <= 0 {
		t.Fatalf("expected a positive PID, got %d", info.PID)
	}

	data, _, err := s.ReadOutput(context.Background(), cliReaderID, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "HELLO-STDOUT") || !strings.Contains(data, "HELLO-STDERR") {
		t.Fatalf("output missing merged stdout/stderr: %q", data)
	}
}

func TestAttachReaderLifecycle(t *testing.T) {
	s := newSession(t, Config{
		Command: os.Args[0],
		Args:    helperArgs("interactive"),
		Mode:    api.ModePipe,
	})
	base := s.buf.ReaderCount()
	rid, err := s.AttachReader()
	if err != nil {
		t.Fatal(err)
	}
	if got := s.buf.ReaderCount(); got != base+1 {
		t.Fatalf("reader count = %d, want %d", got, base+1)
	}
	s.ReleaseReader(rid)
	if got := s.buf.ReaderCount(); got != base {
		t.Fatalf("reader count after release = %d, want %d", got, base)
	}
	if _, err := s.buf.Read(context.Background(), rid, 0, 0); !errors.Is(err, buffer.ErrReader) {
		t.Fatalf("read on released reader: %v, want ErrReader", err)
	}
	s.Terminate(3*time.Second, false)
	waitForExit(t, s)
}

func TestAttachReaderReplaysEndedSession(t *testing.T) {
	s := newSession(t, Config{
		Command: os.Args[0],
		Args:    helperArgs("output"),
		Mode:    api.ModePipe,
	})
	waitForExit(t, s)
	rid, err := s.AttachReader()
	if err != nil {
		t.Fatalf("AttachReader on ended session: %v", err)
	}
	defer s.ReleaseReader(rid)
	data, eof, err := s.ReadOutput(context.Background(), rid, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "HELLO-STDOUT") {
		t.Fatalf("replay missing retained output: %q", data)
	}
	if eof {
		t.Fatal("first read should return data before EOF")
	}
	// The next read reports EOF: retained data is drained and no more output
	// will ever arrive on this (closed) buffer.
	if _, eof, err := s.ReadOutput(context.Background(), rid, 0, 0, true); err != nil || !eof {
		t.Fatalf("expected EOF after replay, got eof=%v err=%v", eof, err)
	}
}

func TestSendInputAndRead(t *testing.T) {
	s := newSession(t, Config{
		Command: os.Args[0],
		Args:    helperArgs("interactive"),
		Mode:    api.ModePipe,
	})
	if err := s.SendInput("ping", true); err != nil {
		t.Fatal(err)
	}
	data, _, err := s.ReadOutput(context.Background(), cliReaderID, 5*time.Second, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "echo:ping") {
		t.Fatalf("expected echoed input, got %q", data)
	}
	s.Terminate(3*time.Second, false)
	waitForExit(t, s)
	if got := s.Info().Status; got != api.StatusTerminated {
		t.Fatalf("status = %s, want terminated", got)
	}
}

func TestTerminateIdempotent(t *testing.T) {
	s := newSession(t, Config{
		Command: os.Args[0],
		Args:    helperArgs("interactive"),
		Mode:    api.ModePipe,
	})
	s.Terminate(3*time.Second, false)
	s.Terminate(3*time.Second, false) // must not hang or panic
	waitForExit(t, s)
	if got := s.Info().Status; got != api.StatusTerminated {
		t.Fatalf("status = %s, want terminated", got)
	}
}

func TestReadEOFAfterExit(t *testing.T) {
	s := newSession(t, Config{
		Command: os.Args[0],
		Args:    helperArgs("output"),
		Mode:    api.ModePipe,
	})
	waitForExit(t, s)
	// First read returns the retained output.
	if _, eof, _ := s.ReadOutput(context.Background(), cliReaderID, 0, 0, true); eof {
		t.Fatal("first read should not be EOF while data remains")
	}
	// Second read signals EOF.
	if _, eof, err := s.ReadOutput(context.Background(), cliReaderID, 0, 0, true); err != nil || !eof {
		t.Fatalf("expected EOF=true, got eof=%v err=%v", eof, err)
	}
}

func TestSendInputAfterExitRejected(t *testing.T) {
	s := newSession(t, Config{
		Command: os.Args[0],
		Args:    helperArgs("output"),
		Mode:    api.ModePipe,
	})
	waitForExit(t, s)
	if err := s.SendInput("x", true); err == nil {
		t.Fatal("expected error sending input to an exited session")
	}
}

func TestPTYLifecycle(t *testing.T) {
	if os.Getenv("EZTERM_SKIP_PTY") != "" {
		t.Skip("PTY test skipped via EZTERM_SKIP_PTY")
	}
	s := newSession(t, Config{
		Command: os.Args[0],
		Args:    helperArgs("interactive"),
		Mode:    api.ModePTY,
		Rows:    24,
		Cols:    80,
	})
	if err := s.SendInput("pty-hello", true); err != nil {
		t.Fatal(err)
	}
	// The child needs time to attach to the PTY and start reading; poll for
	// the echoed input instead of relying on a single blocking read.
	deadline := time.Now().Add(15 * time.Second)
	var data string
	for time.Now().Before(deadline) {
		chunk, eof, readErr := s.ReadOutput(context.Background(), cliReaderID, 750*time.Millisecond, 0, false)
		if readErr != nil {
			t.Fatal(readErr)
		}
		data += chunk
		if strings.Contains(data, "pty-hello") || strings.Contains(data, "echo:pty-hello") {
			break
		}
		if eof {
			break
		}
	}
	// In PTY mode the input is echoed, so it must appear in the output stream.
	if !strings.Contains(data, "pty-hello") && !strings.Contains(data, "echo:pty-hello") {
		t.Fatalf("expected echoed PTY input in output, got %q", data)
	}
	s.Terminate(3*time.Second, false)
	waitForExit(t, s)
	if got := s.Info().Status; got != api.StatusTerminated {
		t.Fatalf("status = %s, want terminated", got)
	}
}
