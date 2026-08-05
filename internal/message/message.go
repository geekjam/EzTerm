package message

import (
	"fmt"
	"sync"
	"time"

	"ezterm/internal/api"
	"ezterm/internal/storage"
	"github.com/google/uuid"
)

const (
	KindSystem = "system"
	KindInput  = "input"
	KindOutput = "output"
)

// Manager appends session messages and persists both index and content files.
type Manager struct {
	store *storage.Store
	mu    sync.Mutex
	index map[string][]api.MessageIndexEntry
}

// NewManager creates a message manager backed by store.
func NewManager(store *storage.Store) *Manager {
	return &Manager{store: store, index: make(map[string][]api.MessageIndexEntry)}
}

// Append persists a new message and updates its session index.
func (m *Manager) Append(sessionID, kind, text string) (api.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msg := api.Message{
		ID:        uuid.NewString(),
		SessionID: sessionID,
		Kind:      kind,
		Text:      text,
		CreatedAt: time.Now().UTC(),
	}
	if err := m.store.SaveMessage(sessionID, msg); err != nil {
		return api.Message{}, fmt.Errorf("save message: %w", err)
	}
	entries, ok := m.index[sessionID]
	if !ok {
		loaded, err := m.store.LoadMessageIndex(sessionID)
		if err != nil {
			return api.Message{}, fmt.Errorf("load message index: %w", err)
		}
		entries = loaded
	}
	entries = append(entries, api.MessageIndexEntry{ID: msg.ID, Kind: msg.Kind, CreatedAt: msg.CreatedAt})
	if err := m.store.SaveMessageIndex(sessionID, entries); err != nil {
		return api.Message{}, fmt.Errorf("save message index: %w", err)
	}
	m.index[sessionID] = entries
	return msg, nil
}

// Index returns a copy of the persisted index for a session.
func (m *Manager) Index(sessionID string) ([]api.MessageIndexEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entries, ok := m.index[sessionID]; ok {
		return append([]api.MessageIndexEntry(nil), entries...), nil
	}
	entries, err := m.store.LoadMessageIndex(sessionID)
	if err != nil {
		return nil, fmt.Errorf("load message index: %w", err)
	}
	m.index[sessionID] = entries
	return append([]api.MessageIndexEntry(nil), entries...), nil
}
