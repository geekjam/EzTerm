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
	"ezterm/internal/prompt"
	"ezterm/internal/sshclient"
	"ezterm/internal/sshconfig"
	"github.com/google/uuid"
)

const (
	cliReaderID  = 0
	logReaderID  = 1
	defaultGrace = 5 * time.Second
	// defaultPasswordTimeout bounds how long a session waits for a password
	// submitted from the Web terminal page before giving up.
	defaultPasswordTimeout = 300 * time.Second
)

// ErrPasswordRequired reports that a remote session cannot dial because its
// config uses password authentication without a stored password. The caller
// creates the session in awaiting_password state and collects the secret from
// the Web terminal page instead.
var ErrPasswordRequired = errors.New("session: ssh password required")

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
	// PasswordTimeout bounds the wait for a password submitted from the Web
	// terminal page. Zero means defaultPasswordTimeout.
	PasswordTimeout time.Duration
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

// bufWriter adapts the output buffer to io.Writer. An optional sink observes
// every chunk so a session can scan its own output for password prompts; the
// sink never receives anything the buffer did not also store.
type bufWriter struct {
	b    *buffer.Buffer
	sink func([]byte)
}

func (w bufWriter) Write(p []byte) (int, error) {
	if err := w.b.Write(p); err != nil {
		return 0, err
	}
	if w.sink != nil {
		w.sink(p)
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

	// Remote dial state. An awaiting_password session keeps cfg and cfgStore so
	// SubmitSecret can dial later; proc is nil until the dial succeeds.
	cfg          Config
	cfgStore     *configstore.Store
	pwTimeout    time.Duration
	detector     *prompt.Detector
	detectOutput bool

	mu      sync.RWMutex
	stdinMu sync.Mutex
	// connectMu serializes dialing and the awaiting_password transitions so a
	// submitted secret, the password timeout, and Terminate cannot race.
	connectMu     sync.Mutex
	exitOnce      sync.Once
	terminateOnce sync.Once
	logDone       chan struct{}
	teardown      chan struct{}
	onExit        func()
}

// New starts a session according to cfg. A remote config that authenticates
// with a password but stores none is returned in awaiting_password state: the
// session is registered immediately and dials only after SubmitSecret receives
// the password from the Web terminal page.
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

	now := time.Now().UTC()
	s := &Session{
		Session: api.Session{
			ID:        id,
			Name:      name,
			Command:   cfg.Command,
			Args:      cfg.Args,
			Mode:      cfg.Mode,
			Status:    api.StatusStarting,
			Rows:      cfg.Rows,
			Cols:      cfg.Cols,
			SSHConfig: cfg.SSHConfig,
			CreatedAt: now,
			UpdatedAt: now,
		},
		buf:        buf,
		msgMgr:     msgMgr,
		webEnabled: cfg.WebEnabled,
		cfg:        cfg,
		cfgStore:   cfgStore,
		pwTimeout:  cfg.PasswordTimeout,
		logDone:    make(chan struct{}),
		teardown:   make(chan struct{}),
	}
	if s.pwTimeout <= 0 {
		s.pwTimeout = defaultPasswordTimeout
	}
	// Password prompts are only scanned for PTY sessions with a Web page:
	// without a page there is no way to submit the secret.
	if cfg.Mode == api.ModePTY && cfg.WebEnabled {
		s.detector = prompt.New()
		s.detectOutput = true
	}
	out := bufWriter{b: buf, sink: s.scanOutput}

	var p proc
	var err error
	if isRemote(cfg.SSHConfig) {
		var profile *sshconfig.Profile
		profile, err = remoteProfile(cfg, cfgStore)
		if err == nil {
			p, err = dialRemote(cfg, profile, profile.Password, out)
			if errors.Is(err, ErrPasswordRequired) {
				s.awaitPassword(profile)
				slog.Debug("session awaiting password", "session_id", s.ID, "ssh_config", cfg.SSHConfig)
				return s, nil
			}
		}
	} else {
		p, err = newLocalProc(cfg.Command, cfg.Args, cfg.Mode, cfg.Rows, cfg.Cols, cfg.Env, out)
	}
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.proc = p
	s.Status = api.StatusRunning
	s.PID = p.PID()
	s.mu.Unlock()

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

// remoteProfile loads the SSH profile referenced by cfg.
func remoteProfile(cfg Config, cfgStore *configstore.Store) (*sshconfig.Profile, error) {
	if cfgStore == nil {
		return nil, fmt.Errorf("config store is not configured")
	}
	profile, err := cfgStore.GetSSH(cfg.SSHConfig)
	if err != nil {
		return nil, fmt.Errorf("load SSH config: %w", err)
	}
	return profile, nil
}

// dialRemote dials the remote host with an explicit password. Password
// authentication with an empty password yields ErrPasswordRequired without
// touching the network, so the caller can defer dialing until the user
// supplies the secret.
func dialRemote(cfg Config, profile *sshconfig.Profile, password string, out io.Writer) (proc, error) {
	if profile.AuthMethod == sshconfig.AuthPassword && password == "" {
		return nil, ErrPasswordRequired
	}
	command := cfg.Command
	if strings.TrimSpace(command) == "" {
		command = strings.TrimSpace(profile.DefaultShell)
	}
	rs, err := sshclient.Start(sshclient.Options{
		Host:        profile.Host,
		Port:        profile.Port,
		User:        profile.User,
		Password:    password,
		KeyPath:     profile.KeyPath,
		DialTimeout: cfg.DialTimeout,
	}, command, cfg.Args, cfg.Mode == api.ModePTY, cfg.Rows, cfg.Cols, out)
	if err != nil {
		return nil, err
	}
	return &remoteProc{rs: rs}, nil
}

// passwordPromptLabel describes the SSH password request shown in the Web page.
func passwordPromptLabel(profile *sshconfig.Profile) string {
	port := profile.Port
	if port == 0 {
		port = sshconfig.DefaultPort
	}
	return fmt.Sprintf("Password for %s@%s:%d", profile.User, profile.Host, port)
}

// awaitPassword switches a freshly created session into awaiting_password
// state and starts the submission deadline.
func (s *Session) awaitPassword(profile *sshconfig.Profile) {
	now := time.Now().UTC()
	s.mu.Lock()
	s.Status = api.StatusAwaitingPassword
	s.Session.PendingPrompt = &api.Prompt{Kind: api.PromptKindSSHPassword, Label: passwordPromptLabel(profile)}
	s.UpdatedAt = now
	s.mu.Unlock()
	s.runPasswordTimeout()
}

// runExitWatcher waits for process exit and finalizes session state exactly once.
func (s *Session) runExitWatcher() {
	p := s.currentProc()
	if p == nil {
		return
	}
	go func() {
		defer close(s.teardown)
		<-p.Done()
		exitCode := p.ExitCode()
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
	finished := s.Status != api.StatusRunning && s.Status != api.StatusStarting && s.Status != api.StatusAwaitingPassword
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
	p := s.proc
	s.mu.RUnlock()
	if !running || p == nil {
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
	_, writeErr := p.Stdin().Write([]byte(payload))
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
// An awaiting_password session has no process yet: it is finalized directly so
// the page stops showing the password dialog.
func (s *Session) Terminate(grace time.Duration, force bool) {
	if grace <= 0 {
		grace = defaultGrace
	}
	s.terminateOnce.Do(func() {
		s.terminated.Store(true)
		s.connectMu.Lock()
		s.mu.RLock()
		status := s.Status
		s.mu.RUnlock()
		if status == api.StatusAwaitingPassword {
			s.finishAwaiting(api.StatusTerminated, 0, "session terminated before a password was provided")
			s.connectMu.Unlock()
			return
		}
		p := s.currentProc()
		s.connectMu.Unlock()
		if p != nil {
			p.Terminate(grace, force)
		}
	})
	// Wait briefly for the exit watcher to set a final status.
	deadline := time.Now().Add(grace + 3*time.Second)
	for time.Now().Before(deadline) {
		s.mu.RLock()
		status := s.Status
		s.mu.RUnlock()
		if status != api.StatusRunning && status != api.StatusStarting && status != api.StatusAwaitingPassword {
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

// Resize updates the PTY dimensions for the session. An awaiting_password
// session has no PTY yet; the size is recorded and applied once it dials, and
// the call succeeds so the Web page does not treat it as a fatal error.
func (s *Session) Resize(rows, cols int) error {
	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}
	if p := s.currentProc(); p != nil {
		if err := p.Resize(rows, cols); err != nil {
			return fmt.Errorf("resize session: %w", err)
		}
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
	if info.PendingPrompt != nil {
		p := *info.PendingPrompt
		info.PendingPrompt = &p
	}
	return info
}

// currentProc returns the live process, or nil while an awaiting_password
// session has not dialed yet.
func (s *Session) currentProc() proc {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.proc
}

// PendingPrompt returns a copy of the active password prompt, or nil when the
// session is not waiting for a secret.
func (s *Session) PendingPrompt() *api.Prompt {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.Session.PendingPrompt == nil {
		return nil
	}
	p := *s.Session.PendingPrompt
	return &p
}

// setPrompt records the password prompt currently shown on the terminal.
func (s *Session) setPrompt(p *api.Prompt) {
	cp := *p
	s.mu.Lock()
	s.Session.PendingPrompt = &cp
	s.UpdatedAt = time.Now().UTC()
	s.mu.Unlock()
}

// clearPrompt removes the active password prompt, if any.
func (s *Session) clearPrompt() {
	s.mu.Lock()
	if s.Session.PendingPrompt != nil {
		s.Session.PendingPrompt = nil
		s.UpdatedAt = time.Now().UTC()
	}
	s.mu.Unlock()
}

// scanOutput feeds session output to the password-prompt detector. It is the
// buffer sink and therefore runs on the process output goroutine, which may be
// shared by multiple streams; the detector serializes its own state.
func (s *Session) scanOutput(chunk []byte) {
	if !s.detectOutput || s.detector == nil {
		return
	}
	label, changed := s.detector.Feed(chunk)
	if !changed {
		return
	}
	if label == "" {
		s.clearPrompt()
		return
	}
	s.setPrompt(&api.Prompt{Kind: api.PromptKindTerminalPassword, Label: label})
}

// SubmitSecret hands a password to the session. The secret is never logged,
// never persisted as a message, and never echoed back to the caller.
//
// For an awaiting_password session it dials the remote host and starts the
// process. For a running PTY session with a detected terminal prompt it writes
// the secret (plus a carriage return) straight to the PTY.
func (s *Session) SubmitSecret(text string) error {
	if text == "" {
		return fmt.Errorf("session %s: password must not be empty", s.ID)
	}
	s.connectMu.Lock()
	defer s.connectMu.Unlock()

	s.mu.RLock()
	status := s.Status
	pendingKind := ""
	if s.Session.PendingPrompt != nil {
		pendingKind = s.Session.PendingPrompt.Kind
	}
	s.mu.RUnlock()

	switch {
	case status == api.StatusAwaitingPassword:
		return s.connectWithSecret(text)
	case status == api.StatusRunning && pendingKind == api.PromptKindTerminalPassword:
		return s.writeTerminalSecret(text)
	default:
		return fmt.Errorf("session %s has no pending password prompt", s.ID)
	}
}

// connectWithSecret dials the remote host using secret and starts the process.
// The caller holds connectMu.
func (s *Session) connectWithSecret(secret string) error {
	profile, err := remoteProfile(s.cfg, s.cfgStore)
	if err != nil {
		s.finishAwaiting(api.StatusExited, 1, fmt.Sprintf("ssh: %v", err))
		return err
	}
	p, err := dialRemote(s.cfg, profile, secret, bufWriter{b: s.buf, sink: s.scanOutput})
	if err != nil {
		s.finishAwaiting(api.StatusExited, 1, fmt.Sprintf("ssh: authentication failed: %v", err))
		return fmt.Errorf("ssh: authentication failed: %w", err)
	}

	now := time.Now().UTC()
	s.mu.Lock()
	s.proc = p
	s.PID = p.PID()
	s.Status = api.StatusRunning
	s.Session.PendingPrompt = nil
	s.UpdatedAt = now
	s.mu.Unlock()

	s.runExitWatcher()
	s.runLogCollector()
	if s.msgMgr != nil {
		_, _ = s.msgMgr.Append(s.ID, message.KindSystem, "Process started")
	}
	slog.Debug("session connected after password prompt", "session_id", s.ID)
	return nil
}

// writeTerminalSecret answers a terminal password prompt. It writes to the
// PTY directly instead of going through SendInput so the secret is not stored
// as an input message.
func (s *Session) writeTerminalSecret(text string) error {
	p := s.currentProc()
	if p == nil {
		return fmt.Errorf("session %s has no live process", s.ID)
	}
	s.stdinMu.Lock()
	_, writeErr := p.Stdin().Write([]byte(text + "\r"))
	s.stdinMu.Unlock()
	if writeErr != nil {
		return fmt.Errorf("write password: %w", writeErr)
	}
	s.clearPrompt()
	if s.detector != nil {
		s.detector.Reset()
	}
	return nil
}

// finishAwaiting ends a session that never had a live process (password
// timeout, termination, or failed authentication). It mirrors the exit watcher:
// the final status is written once, readers wake with EOF, and the lifecycle
// callback persists the final state.
func (s *Session) finishAwaiting(status api.Status, exitCode int, note string) {
	finalized := false
	s.exitOnce.Do(func() {
		finalized = true
		if note != "" {
			_ = s.buf.Write([]byte(note + "\r\n"))
		}
		now := time.Now().UTC()
		s.mu.Lock()
		s.Status = status
		s.ExitCode = exitCode
		s.FinishedAt = &now
		s.UpdatedAt = now
		s.Session.PendingPrompt = nil
		s.mu.Unlock()
		s.buf.Close()
		close(s.teardown)
	})
	if !finalized {
		return
	}
	if s.msgMgr != nil {
		_, _ = s.msgMgr.Append(s.ID, message.KindSystem, fmt.Sprintf("Process %s with exit code %d", status, exitCode))
	}
	if fn := s.getOnExit(); fn != nil {
		fn()
	}
	slog.Debug("session finished without dialing", "session_id", s.ID, "status", status)
}

// runPasswordTimeout terminates an awaiting_password session when no secret
// arrives before the deadline.
func (s *Session) runPasswordTimeout() {
	timeout := s.pwTimeout
	if timeout <= 0 {
		timeout = defaultPasswordTimeout
	}
	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-s.teardown:
			return
		}
		s.connectMu.Lock()
		defer s.connectMu.Unlock()
		s.mu.RLock()
		awaiting := s.Status == api.StatusAwaitingPassword
		s.mu.RUnlock()
		if !awaiting {
			return
		}
		s.finishAwaiting(api.StatusTerminated, 0, passwordTimeoutNote(timeout))
		slog.Debug("session password prompt timed out", "session_id", s.ID)
	}()
}

// passwordTimeoutNote renders the user-facing timeout explanation. Whole
// seconds match the documented "after 300s" wording; sub-second timeouts
// (used by tests) keep the duration form.
func passwordTimeoutNote(timeout time.Duration) string {
	if seconds := int(timeout / time.Second); seconds > 0 {
		return fmt.Sprintf("password prompt timed out after %ds", seconds)
	}
	return fmt.Sprintf("password prompt timed out after %s", timeout)
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
