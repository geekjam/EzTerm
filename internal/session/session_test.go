package session

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"ezterm/internal/api"
	"ezterm/internal/buffer"
	"ezterm/internal/configstore"
	"ezterm/internal/message"
	"ezterm/internal/prompt"
	"ezterm/internal/sshconfig"
	"ezterm/internal/storage"
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
	case "prompt":
		// Mimics sudo: print a password prompt without a trailing newline, then
		// read one line of input and report what arrived.
		fmt.Fprint(os.Stdout, "[sudo] password for tester: ")
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			fmt.Fprintf(os.Stdout, "answer:%s\n", scanner.Text())
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

// --- password prompt support ---

// newPromptSession creates a session from an SSH config that stores no password
// and therefore waits for a secret submitted from the Web terminal page.
func newPromptSession(t *testing.T, passwordTimeout time.Duration) *Session {
	t.Helper()
	dir := t.TempDir()
	store := configstore.NewStore(dir)
	if err := store.SaveSSH("prompt", &sshconfig.Profile{
		Host: "127.0.0.1", Port: 1, User: "tester", AuthMethod: sshconfig.AuthPassword,
	}); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		SSHConfig:       "prompt",
		Mode:            api.ModePTY,
		WebEnabled:      true,
		DialTimeout:     500 * time.Millisecond,
		PasswordTimeout: passwordTimeout,
	}, nil, store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.setOnExit(func() {})
	return s
}

func TestAwaitingPasswordSession(t *testing.T) {
	s := newPromptSession(t, 30*time.Second)
	info := s.Info()
	if info.Status != api.StatusAwaitingPassword {
		t.Fatalf("status = %s, want awaiting_password", info.Status)
	}
	if info.PendingPrompt == nil || info.PendingPrompt.Kind != api.PromptKindSSHPassword {
		t.Fatalf("pending prompt = %+v", info.PendingPrompt)
	}
	if !strings.Contains(info.PendingPrompt.Label, "tester@127.0.0.1:1") {
		t.Fatalf("prompt label = %q", info.PendingPrompt.Label)
	}
	if info.PID != 0 {
		t.Fatalf("pid = %d, want 0 before dialing", info.PID)
	}
	// The Web page resizes as soon as it opens; that must not fail while the
	// session has no PTY yet.
	if err := s.Resize(30, 100); err != nil {
		t.Fatalf("Resize before dialing: %v", err)
	}
	if got := s.Info(); got.Rows != 30 || got.Cols != 100 {
		t.Fatalf("size = %dx%d, want 30x100", got.Rows, got.Cols)
	}
	// Sending input before the password arrives is rejected.
	if err := s.SendInput("x", true); err == nil {
		t.Fatal("expected SendInput to fail before dialing")
	}
	s.Terminate(time.Second, true)
	if got := s.Info().Status; got != api.StatusTerminated {
		t.Fatalf("status = %s, want terminated", got)
	}
	if s.Info().PendingPrompt != nil {
		t.Fatal("pending prompt survived termination")
	}
}

func TestAwaitingPasswordFailedDial(t *testing.T) {
	s := newPromptSession(t, 30*time.Second)
	// Port 1 on loopback refuses the connection, so the dial fails and the
	// session ends instead of retrying.
	err := s.SubmitSecret("wrong-secret")
	if err == nil {
		t.Fatal("expected the dial to fail")
	}
	if strings.Contains(err.Error(), "wrong-secret") {
		t.Fatalf("error leaked the secret: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && s.Info().Status == api.StatusAwaitingPassword {
		time.Sleep(10 * time.Millisecond)
	}
	info := s.Info()
	if info.Status != api.StatusExited {
		t.Fatalf("status = %s, want exited", info.Status)
	}
	if info.PendingPrompt != nil {
		t.Fatal("pending prompt survived a failed dial")
	}
	data, _, readErr := s.ReadOutput(context.Background(), cliReaderID, 0, 0, true)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(data, "ssh: authentication failed") {
		t.Fatalf("output missing the failure note: %q", data)
	}
	// A late submission must not resurrect an ended session.
	if err := s.SubmitSecret("again"); err == nil {
		t.Fatal("expected SubmitSecret to fail on an ended session")
	}
}

func TestAwaitingPasswordTimeout(t *testing.T) {
	s := newPromptSession(t, 150*time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && s.Info().Status == api.StatusAwaitingPassword {
		time.Sleep(10 * time.Millisecond)
	}
	info := s.Info()
	if info.Status != api.StatusTerminated {
		t.Fatalf("status = %s, want terminated after the timeout", info.Status)
	}
	if info.PendingPrompt != nil {
		t.Fatal("pending prompt survived the timeout")
	}
	data, _, err := s.ReadOutput(context.Background(), cliReaderID, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "password prompt timed out") {
		t.Fatalf("output missing the timeout note: %q", data)
	}
}

func TestSubmitSecretWithoutPromptRejected(t *testing.T) {
	s := newSession(t, Config{
		Command: os.Args[0],
		Args:    helperArgs("interactive"),
		Mode:    api.ModePipe,
	})
	defer func() {
		s.Terminate(time.Second, true)
		waitForExit(t, s)
	}()
	if err := s.SubmitSecret("secret"); err == nil {
		t.Fatal("expected SubmitSecret to fail without a pending prompt")
	}
	if err := s.SubmitSecret(""); err == nil {
		t.Fatal("expected SubmitSecret to reject an empty secret")
	}
}

// TestTerminalPromptDetectedAndAnswered covers the session-internal prompt path:
// PTY output is scanned, the prompt surfaces as pending_prompt, and the
// submitted secret reaches the process without being stored as a message.
func TestTerminalPromptDetectedAndAnswered(t *testing.T) {
	if os.Getenv("EZTERM_SKIP_PTY") != "" {
		t.Skip("PTY test skipped via EZTERM_SKIP_PTY")
	}
	s, err := New(Config{
		Command:    os.Args[0],
		Args:       helperArgs("prompt"),
		Mode:       api.ModePTY,
		Rows:       24,
		Cols:       80,
		WebEnabled: true,
	}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		s.Terminate(time.Second, true)
		waitForExit(t, s)
	}()

	deadline := time.Now().Add(15 * time.Second)
	var pending *api.Prompt
	for time.Now().Before(deadline) {
		pending = s.PendingPrompt()
		if pending != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pending == nil {
		t.Fatal("password prompt was not detected in the PTY output")
	}
	if pending.Kind != api.PromptKindTerminalPassword {
		t.Fatalf("prompt kind = %q, want terminal_password", pending.Kind)
	}
	if !strings.Contains(pending.Label, "password for tester") {
		t.Fatalf("prompt label = %q", pending.Label)
	}

	const secret = "s3cret-not-logged"
	if err := s.SubmitSecret(secret); err != nil {
		t.Fatalf("SubmitSecret: %v", err)
	}
	if s.PendingPrompt() != nil {
		t.Fatal("pending prompt survived the submission")
	}

	// The program must receive the answer, which proves the secret reached the
	// PTY (the echo is terminal-level, not a message).
	rid, err := s.AttachReader()
	if err != nil {
		t.Fatal(err)
	}
	defer s.ReleaseReader(rid)
	var sb strings.Builder
	for time.Now().Before(deadline) {
		data, eof, readErr := s.ReadOutput(context.Background(), rid, 250*time.Millisecond, 0, false)
		if readErr != nil {
			t.Fatal(readErr)
		}
		sb.WriteString(data)
		if strings.Contains(sb.String(), "answer:") {
			break
		}
		if eof {
			break
		}
	}
	if !strings.Contains(sb.String(), "answer:") {
		t.Fatalf("program never reported the answer: %q", sb.String())
	}

	// The secret must never be persisted as a message; the fake-process test
	// below covers that directly (a real PTY may echo what it receives).
	if s.PendingPrompt() != nil {
		t.Fatal("the same prompt fired again after the submission")
	}
}

func TestScanOutputDisabledWithoutWeb(t *testing.T) {
	dir := t.TempDir()
	store := storage.New(dir)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		Command:    os.Args[0],
		Args:       helperArgs("prompt"),
		Mode:       api.ModePTY,
		WebEnabled: false,
	}, message.NewManager(store), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		s.Terminate(time.Second, true)
		waitForExit(t, s)
	}()
	// Wait for the prompt to be printed, then confirm no prompt is reported:
	// without a Web page nobody could answer it.
	time.Sleep(750 * time.Millisecond)
	if p := s.PendingPrompt(); p != nil {
		t.Fatalf("prompt reported for a session without a Web terminal: %+v", p)
	}
}

// TestDetectorDrivesPendingPrompt checks the buffer sink wiring directly, with a
// fake process so no PTY is required.
func TestDetectorDrivesPendingPrompt(t *testing.T) {
	s := &Session{detector: prompt.New(), detectOutput: true, buf: buffer.New()}
	sink := bufWriter{b: s.buf, sink: s.scanOutput}
	if _, err := sink.Write([]byte("Password: ")); err != nil {
		t.Fatal(err)
	}
	if p := s.PendingPrompt(); p == nil || p.Kind != api.PromptKindTerminalPassword {
		t.Fatalf("pending prompt = %+v", p)
	}
	if _, err := sink.Write([]byte("\r\n")); err != nil {
		t.Fatal(err)
	}
	if p := s.PendingPrompt(); p != nil {
		t.Fatalf("pending prompt not cleared by a newline: %+v", p)
	}
}

// fakeProc is a proc that records stdin writes without spawning a process.
type fakeProc struct {
	stdin bytes.Buffer
	done  chan struct{}
}

func (p *fakeProc) Stdin() io.Writer              { return &p.stdin }
func (p *fakeProc) Done() <-chan struct{}         { return p.done }
func (p *fakeProc) ExitCode() int                 { return 0 }
func (p *fakeProc) Terminate(time.Duration, bool) {}
func (p *fakeProc) Close() error                  { return nil }
func (p *fakeProc) Resize(rows, cols int) error   { return nil }
func (p *fakeProc) PID() int                      { return 4242 }

// TestSubmitTerminalSecretNotLogged verifies the terminal-password path writes
// the secret to stdin (with a carriage return) and never records it as an input
// message.
func TestSubmitTerminalSecretNotLogged(t *testing.T) {
	dir := t.TempDir()
	store := storage.New(dir)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	msgMgr := message.NewManager(store)
	fp := &fakeProc{done: make(chan struct{})}
	s := &Session{
		Session:  api.Session{ID: "secretsession", Status: api.StatusRunning, Mode: api.ModePTY},
		proc:     fp,
		buf:      buffer.New(),
		msgMgr:   msgMgr,
		detector: prompt.New(),
		teardown: make(chan struct{}),
	}
	s.detectOutput = true
	s.webEnabled = true

	if err := s.SendInput("echo hi", false); err != nil {
		t.Fatal(err)
	}
	s.scanOutput([]byte("[sudo] password for tester: "))
	if s.PendingPrompt() == nil {
		t.Fatal("prompt not detected")
	}
	const secret = "hunter2-never-logged"
	if err := s.SubmitSecret(secret); err != nil {
		t.Fatal(err)
	}
	if s.PendingPrompt() != nil {
		t.Fatal("pending prompt survived the submission")
	}
	if got, want := fp.stdin.String(), "echo hi"+secret+""; got != want {
		t.Fatalf("stdin = %q, want %q", got, want)
	}
	entries, err := msgMgr.Index(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("message count = %d, want only the explicit input", len(entries))
	}
	for _, entry := range entries {
		msg, loadErr := store.LoadMessage(s.ID, entry.ID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if strings.Contains(msg.Text, secret) {
			t.Fatalf("secret leaked into a %s message: %q", msg.Kind, msg.Text)
		}
	}
	if err := s.SubmitSecret(secret); err == nil {
		t.Fatal("expected a second submission to fail: no prompt is pending")
	}
}
