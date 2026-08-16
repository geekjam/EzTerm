package session

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"ezterm/internal/api"
	"ezterm/internal/configstore"
	"ezterm/internal/message"
	"ezterm/internal/storage"
)

// Manager is a thread-safe registry of sessions with persistence.
type Manager struct {
	sessions   sync.Map // string -> *Session
	store      *storage.Store
	msgMgr     *message.Manager
	cfgStore   *configstore.Store
	onChangeMu sync.RWMutex
	onChange   func()
}

// NewManager creates a session manager.
func NewManager(store *storage.Store, msgMgr *message.Manager, cfgStore *configstore.Store) *Manager {
	return &Manager{store: store, msgMgr: msgMgr, cfgStore: cfgStore}
}

// SetChangeListener registers a callback invoked on lifecycle changes.
func (m *Manager) SetChangeListener(fn func()) {
	m.onChangeMu.Lock()
	m.onChange = fn
	m.onChangeMu.Unlock()
}

func (m *Manager) notify() {
	m.onChangeMu.RLock()
	fn := m.onChange
	m.onChangeMu.RUnlock()
	if fn != nil {
		fn()
	}
}

// Restore loads historical sessions from sessions.json as exited records.
func (m *Manager) Restore() error {
	sessions, err := m.store.LoadSessions()
	if err != nil && !errors.Is(err, storage.ErrCorrupt) {
		return err
	}
	for _, info := range sessions {
		s := &Session{Session: info}
		s.webEnabled = info.WebURL != ""
		s.buf = newClosedBuffer()
		s.logDone = make(chan struct{})
		close(s.logDone)
		s.teardown = make(chan struct{})
		close(s.teardown)
		if s.Status == api.StatusStarting || s.Status == api.StatusRunning {
			s.Status = api.StatusExited
			now := time.Now().UTC()
			s.FinishedAt = &now
		}
		m.sessions.Store(s.ID, s)
	}
	return nil
}

// Create starts and registers a new session.
func (m *Manager) Create(cfg Config) (*Session, error) {
	s, err := New(cfg, m.msgMgr, m.cfgStore)
	if err != nil {
		return nil, err
	}
	m.sessions.Store(s.ID, s)
	s.setOnExit(func() {
		m.persist()
		m.notify()
	})
	m.persist()
	m.notify()
	return s, nil
}

// Get returns a session by ID.
func (m *Manager) Get(id string) *Session {
	if v, ok := m.sessions.Load(id); ok {
		return v.(*Session)
	}
	return nil
}

// Wait blocks until a session has fully torn down (exit watcher and message
// collector finished), so callers can safely remove its data directory.
func (m *Manager) Wait(id string) {
	s := m.Get(id)
	if s == nil {
		return
	}
	if s.teardown != nil {
		select {
		case <-s.teardown:
		case <-time.After(5 * time.Second):
		}
	}
}

// ListAll returns metadata for every session, ordered by creation time.
func (m *Manager) ListAll() []api.Session {
	result := make([]api.Session, 0)
	m.sessions.Range(func(_, v any) bool {
		result = append(result, v.(*Session).Info())
		return true
	})
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}

// Terminate stops a session and waits for its full teardown.
func (m *Manager) Terminate(id string, grace time.Duration, force bool) error {
	s := m.Get(id)
	if s == nil {
		return fmt.Errorf("session %q not found", id)
	}
	s.Terminate(grace, force)
	m.Wait(id)
	m.persist()
	m.notify()
	return nil
}

// Delete removes an exited session from the registry.
func (m *Manager) Delete(id string) error {
	s := m.Get(id)
	if s == nil {
		return fmt.Errorf("session %q not found", id)
	}
	if s.Info().Status == api.StatusRunning || s.Info().Status == api.StatusStarting {
		return fmt.Errorf("cannot delete running session %q, terminate it first", id)
	}
	m.sessions.Delete(id)
	m.persist()
	m.notify()
	return nil
}

// Persist writes the current session metadata immediately. It is used when a
// handler adds metadata after Create has completed, such as a Web URL.
func (m *Manager) Persist() {
	m.persist()
}

func (m *Manager) persist() {
	if m.store == nil {
		return
	}
	if err := m.store.SaveSessions(m.ListAll()); err != nil {
		m.logPersistErr(err)
	}
}

func (m *Manager) logPersistErr(err error) {
	// Persistence failure must not break request handling; surfaced via logs.
	slog.Warn("persist sessions", "error", err)
}
