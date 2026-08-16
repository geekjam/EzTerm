// Package session manages process sessions: local (PTY/pipe) and remote (SSH).
package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ezterm/internal/ansi"
	"ezterm/internal/api"
	"ezterm/internal/buffer"
	"ezterm/internal/configstore"
	"ezterm/internal/message"
	"ezterm/internal/sshclient"
	"github.com/google/uuid"
)

const (
	cliReaderID  = 0
	logReaderID  = 1
	defaultGrace = 5 * time.Second
)

// Config describes how to create a new session.
type Config struct {
	Command     string
	Args        []string
	Mode        api.Mode
	Name        string
	Rows        int
	Cols        int
	SSHConfig   string // "", "internal" (local), or a profile name
	DialTimeout time.Duration
	WebEnabled  bool     // expose a Web terminal (PTY only)
	Env         []string // optional process environment (defaults to os.Environ)
}

// proc abstracts a running process, local or remote.
type proc interface {
	Stdin() io.Writer
	Done() <-chan struct{}
	ExitCode() int
	Terminate(grace time.Duration, force bool)
	Close() error
	Resize(rows, cols int) error
	PID() int
}

// bufWriter adapts the output buffer to io.Writer.
type bufWriter struct {
	b *buffer.Buffer
}

func (w bufWriter) Write(p []byte) (int, error) {
	if err := w.b.Write(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Session couples the public session model with a live process and output buffer.
type Session struct {
	api.Session
	proc       proc
	buf        *buffer.Buffer
	msgMgr     *message.Manager
	terminated atomic.Bool
	webEnabled bool

	mu            sync.RWMutex
	stdinMu       sync.Mutex
	exitOnce      sync.Once
	terminateOnce sync.Once
	logDone       chan struct{}
	teardown      chan struct{}
	onExit        func()
}

// New starts a session according to cfg.
func New(cfg Config, msgMgr *message.Manager, cfgStore *configstore.Store) (*Session, error) {
	if err := normalizeConfig(&cfg); err != nil {
		return nil, err
	}

	id := newSessionID()
	name := cfg.Name
	if name == "" {
		name = fmt.Sprintf("session-%s", id)
	}

	buf := buffer.New()
	if _, err := buf.NewReaderFromStart(); err != nil { // cliReaderID
		return nil, fmt.Errorf("create output reader: %w", err)
	}
	if _, err := buf.NewReaderFromStart(); err != nil { // logReaderID
		return nil, fmt.Errorf("create log reader: %w", err)
	}

	var p proc
	var err error
	if isRemote(cfg.SSHConfig) {
		p, err = startRemote(cfg, cfgStore, bufWriter{b: buf})
	} else {
		p, err = newLocalProc(cfg.Command, cfg.Args, cfg.Mode, cfg.Rows, cfg.Cols, cfg.Env, bufWriter{b: buf})
	}
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	s := &Session{
		Session: api.Session{
			ID:        id,
			Name:      name,
			Command:   cfg.Command,
			Args:      cfg.Args,
			Mode:      cfg.Mode,
			Status:    api.StatusRunning,
			PID:       p.PID(),
			Rows:      cfg.Rows,
			Cols:      cfg.Cols,
			SSHConfig: cfg.SSHConfig,
			CreatedAt: now,
			UpdatedAt: now,
		},
		proc:       p,
		buf:        buf,
		msgMgr:     msgMgr,
		webEnabled: cfg.WebEnabled,
		logDone:    make(chan struct{}),
		teardown:   make(chan struct{}),
	}

	s.runExitWatcher()
	s.runLogCollector()

	if msgMgr != nil {
		_, _ = msgMgr.Append(s.ID, message.KindSystem, "Process started")
	}
	slog.Debug("session started", "session_id", s.ID, "command", cfg.Command, "mode", cfg.Mode, "ssh_config", cfg.SSHConfig)
	return s, nil
}

func newSessionID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
}

func normalizeConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("session: nil config")
	}
	if cfg.Mode == "" {
		cfg.Mode = api.ModePTY
	}
	if cfg.Mode != api.ModePTY && cfg.Mode != api.ModePipe {
		return fmt.Errorf("session: invalid mode %q (want pty or pipe)", cfg.Mode)
	}
	if cfg.Rows <= 0 {
		cfg.Rows = 24
	}
	if cfg.Cols <= 0 {
		cfg.Cols = 80
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 15 * time.Second
	}
	return nil
}

// isRemote reports whether the config targets an SSH profile rather than the local host.
func isRemote(sshConfig string) bool {
	s := strings.TrimSpace(sshConfig)
	return s != "" && s != "internal"
}

// IsRemote reports whether an SSH config string selects a remote profile.
func IsRemote(sshConfig string) bool {
	return isRemote(sshConfig)
}

func startRemote(cfg Config, cfgStore *configstore.Store, out io.Writer) (proc, error) {
	if cfgStore == nil {
		return nil, fmt.Errorf("config store is not configured")
	}
	profile, err := cfgStore.GetSSH(cfg.SSHConfig)
	if err != nil {
		return nil, fmt.Errorf("load SSH config: %w", err)
	}
	command := cfg.Command
	if strings.TrimSpace(command) == "" {
		command = strings.TrimSpace(profile.DefaultShell)
	}
	rs, err := sshclient.Start(sshclient.Options{
		Host:        profile.Host,
		Port:        profile.Port,
		User:        profile.User,
		Password:    profile.Password,
		KeyPath:     profile.KeyPath,
		DialTimeout: cfg.DialTimeout,
	}, command, cfg.Args, cfg.Mode == api.ModePTY, cfg.Rows, cfg.Cols, out)
	if err != nil {
		return nil, err
	}
	return &remoteProc{rs: rs}, nil
}

// runExitWatcher waits for process exit and finalizes session state exactly once.
func (s *Session) runExitWatcher() {
	go func() {
		defer close(s.teardown)
		<-s.proc.Done()
		exitCode := s.proc.ExitCode()
		status := api.StatusExited
		if s.terminated.Load() {
			status = api.StatusTerminated
		}
		s.exitOnce.Do(func() {
			now := time.Now().UTC()
			s.mu.Lock()
			s.Status = status
			s.ExitCode = exitCode
			s.FinishedAt = &now
			s.UpdatedAt = now
			s.mu.Unlock()
			s.buf.Close() // wake readers; pending data still drains before io.EOF
		})
		if s.msgMgr != nil {
			_, _ = s.msgMgr.Append(s.ID, message.KindSystem, fmt.Sprintf("Process %s with exit code %d", status, exitCode))
		}
		if fn := s.getOnExit(); fn != nil {
			fn()
		}
		slog.Debug("session finished", "session_id", s.ID, "status", status, "exit_code", exitCode)
	}()
}

// setOnExit registers the lifecycle callback. If the session already finished,
// the callback runs immediately so no persist/notify is lost.
func (s *Session) setOnExit(fn func()) {
	s.mu.Lock()
	s.onExit = fn
	finished := s.Status != api.StatusRunning && s.Status != api.StatusStarting
	s.mu.Unlock()
	if finished && fn != nil {
		fn()
	}
}

func (s *Session) getOnExit() func() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.onExit
}

// runLogCollector periodically persists stripped output for the session.
func (s *Session) runLogCollector() {
	go func() {
		defer close(s.logDone)
		for {
			data, err := s.buf.Read(context.Background(), logReaderID, 500*time.Millisecond, 64*1024)
			if len(data) > 0 && s.msgMgr != nil {
				text := ansi.Compact(ansi.Strip(string(data)))
				if text != "" {
					_, _ = s.msgMgr.Append(s.ID, message.KindOutput, text)
				}
			}
			if errors.Is(err, io.EOF) {
				return
			}
		}
	}()
}

// newClosedBuffer returns a buffer that reads EOF immediately; used for
// restored (historical) sessions that no longer have a live process.
func newClosedBuffer() *buffer.Buffer {
	b := buffer.New()
	b.Close()
	return b
}

// SendInput writes text to the process stdin, optionally appending a newline.
func (s *Session) SendInput(text string, pressEnter bool) error {
	if text == "" && !pressEnter {
		return nil
	}
	s.mu.RLock()
	running := s.Status == api.StatusRunning
	s.mu.RUnlock()
	if !running {
		return fmt.Errorf("session %s is %s, cannot send input", s.ID, s.Status)
	}
	newline := "\n"
	if runtime.GOOS == "windows" {
		newline = "\r\n"
	}
	payload := text
	if pressEnter {
		payload = text + newline
	}
	s.stdinMu.Lock()
	_, writeErr := s.proc.Stdin().Write([]byte(payload))
	s.stdinMu.Unlock()
	if writeErr != nil {
		return fmt.Errorf("write input: %w", writeErr)
	}
	if s.msgMgr != nil {
		_, _ = s.msgMgr.Append(s.ID, message.KindInput, payload)
	}
	return nil
}

// ReadOutput reads new output for a reader. eof is true once the session has
// exited and every retained byte was consumed. raw preserves ANSI sequences.
func (s *Session) ReadOutput(ctx context.Context, readerID int, timeout time.Duration, maxBytes int, raw bool) (string, bool, error) {
	data, err := s.buf.Read(ctx, readerID, timeout, maxBytes)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false, err
	}
	output := string(data)
	if !raw {
		output = ansi.Compact(ansi.Strip(output))
	}
	return output, errors.Is(err, io.EOF), nil
}

// Terminate stops the session, then waits for the exit watcher to finalize state.
func (s *Session) Terminate(grace time.Duration, force bool) {
	if grace <= 0 {
		grace = defaultGrace
	}
	s.terminateOnce.Do(func() {
		s.terminated.Store(true)
		if s.proc != nil {
			s.proc.Terminate(grace, force)
		}
	})
	// Wait briefly for the exit watcher to set a final status.
	deadline := time.Now().Add(grace + 3*time.Second)
	for time.Now().Before(deadline) {
		s.mu.RLock()
		status := s.Status
		s.mu.RUnlock()
		if status != api.StatusRunning && status != api.StatusStarting {
			// Wait for the log collector and exit watcher to finish so that
			// callers can safely remove the data directory afterwards.
			if s.teardown != nil {
				select {
				case <-s.teardown:
				case <-time.After(2 * time.Second):
				}
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// NewReader allocates a new reader positioned at the current end of output.
func (s *Session) NewReader() (int, error) {
	if s.buf == nil {
		return 0, fmt.Errorf("session output unavailable")
	}
	return s.buf.NewReader()
}

// AttachReader registers a reader at the beginning of retained output so an
// attach client can replay the current screen state before receiving live
// output. It also works on ended sessions, which drain retained data and then
// report EOF. Callers must ReleaseReader when the connection ends.
func (s *Session) AttachReader() (int, error) {
	if s.buf == nil {
		return 0, fmt.Errorf("session output unavailable")
	}
	return s.buf.NewReaderFromStart()
}

// ReleaseReader unregisters an attach reader so the buffer can reclaim
// retained memory and stop tracking the disconnected client.
func (s *Session) ReleaseReader(id int) {
	if s.buf != nil {
		s.buf.Unregister(id)
	}
}

// Resize updates the PTY dimensions for the session.
func (s *Session) Resize(rows, cols int) error {
	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}
	if s.proc == nil {
		return fmt.Errorf("session has no live process")
	}
	if err := s.proc.Resize(rows, cols); err != nil {
		return fmt.Errorf("resize session: %w", err)
	}
	s.mu.Lock()
	s.Rows, s.Cols = rows, cols
	s.UpdatedAt = time.Now().UTC()
	s.mu.Unlock()
	return nil
}

// Info returns a copy of the public session metadata.
func (s *Session) Info() api.Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	info := s.Session
	if !s.webEnabled {
		info.WebURL = ""
	}
	return info
}

// IsWebEnabled reports whether the session was created with a Web terminal.
// The web_url is built by the daemon (which knows its bind host and port) and
// stored on the session; enabled here means the /web endpoints are available.
func (s *Session) IsWebEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.webEnabled
}

// SetWebURL records the public Web terminal URL for an enabled session.
func (s *Session) SetWebURL(url string) {
	s.mu.Lock()
	s.WebURL = url
	s.UpdatedAt = time.Now().UTC()
	s.mu.Unlock()
}
