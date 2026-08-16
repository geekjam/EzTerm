package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"ezterm/internal/api"
)

var (
	ErrInvalidID = errors.New("storage: invalid ID")
	ErrCorrupt   = errors.New("storage: corrupt JSON")
	validIDRe    = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// Store persists daemon state under one data directory.
type Store struct {
	dataDir string
	mu      sync.RWMutex
}

// New creates a Store rooted at dataDir.
func New(dataDir string) *Store {
	return &Store{dataDir: dataDir}
}

// DataDir returns the root directory used by the store.
func (s *Store) DataDir() string {
	return s.dataDir
}

// Init creates the persistent directory layout and an empty sessions file.
func (s *Store) Init() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(s.dataDir, "messages"), 0o700); err != nil {
		return fmt.Errorf("create messages directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(s.dataDir, "configs"), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	path := filepath.Join(s.dataDir, "sessions.json")
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat sessions file: %w", err)
		}
		data, marshalErr := json.MarshalIndent([]api.Session{}, "", "  ")
		if marshalErr != nil {
			return fmt.Errorf("encode empty sessions: %w", marshalErr)
		}
		if err := atomicWriteFile(path, data, 0o600); err != nil {
			return fmt.Errorf("create sessions file: %w", err)
		}
	}
	return nil
}

func validateID(id string) error {
	if !validIDRe.MatchString(id) {
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return nil
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".ezterm-tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpPath := file.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := file.Chmod(perm); err != nil {
		_ = file.Close()
		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	removeTemp = false
	return nil
}

// WriteFileAtomic exposes the atomic write helper for other persistence packages.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return atomicWriteFile(path, data, perm)
}

// SaveSessions atomically replaces sessions.json.
func (s *Store) SaveSessions(sessions []api.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		return fmt.Errorf("encode sessions: %w", err)
	}
	return atomicWriteFile(filepath.Join(s.dataDir, "sessions.json"), data, 0o600)
}

// LoadSessions reads sessions.json. A corrupt file is moved aside so the next
// daemon start can recover with an empty session list.
func (s *Store) LoadSessions() ([]api.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	path := filepath.Join(s.dataDir, "sessions.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []api.Session{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sessions: %w", err)
	}
	var sessions []api.Session
	if err := json.Unmarshal(data, &sessions); err != nil {
		backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano())
		if renameErr := os.Rename(path, backup); renameErr != nil {
			return nil, fmt.Errorf("%w: %v; backup corrupt file: %v", ErrCorrupt, err, renameErr)
		}
		return []api.Session{}, fmt.Errorf("%w: %v; moved to %s", ErrCorrupt, err, backup)
	}
	return sessions, nil
}

// SaveMessageIndex atomically writes a session's message index.
func (s *Store) SaveMessageIndex(sessionID string, entries []api.MessageIndexEntry) error {
	if err := validateID(sessionID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("encode message index: %w", err)
	}
	path := filepath.Join(s.dataDir, "messages", sessionID, "index.json")
	return atomicWriteFile(path, data, 0o600)
}

// LoadMessageIndex reads a session's message index.
func (s *Store) LoadMessageIndex(sessionID string) ([]api.MessageIndexEntry, error) {
	if err := validateID(sessionID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	path := filepath.Join(s.dataDir, "messages", sessionID, "index.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []api.MessageIndexEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read message index: %w", err)
	}
	var entries []api.MessageIndexEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("%w: message index: %v", ErrCorrupt, err)
	}
	return entries, nil
}

// SaveMessage writes one message file.
func (s *Store) SaveMessage(sessionID string, msg api.Message) error {
	if err := validateID(sessionID); err != nil {
		return err
	}
	if err := validateID(msg.ID); err != nil {
		return fmt.Errorf("message ID: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(msg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode message: %w", err)
	}
	path := filepath.Join(s.dataDir, "messages", sessionID, "messages", msg.ID+".json")
	return atomicWriteFile(path, data, 0o600)
}

// LoadMessage reads one message file.
func (s *Store) LoadMessage(sessionID, messageID string) (api.Message, error) {
	if err := validateID(sessionID); err != nil {
		return api.Message{}, err
	}
	if err := validateID(messageID); err != nil {
		return api.Message{}, fmt.Errorf("message ID: %w", err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := os.ReadFile(filepath.Join(s.dataDir, "messages", sessionID, "messages", messageID+".json"))
	if err != nil {
		return api.Message{}, fmt.Errorf("read message: %w", err)
	}
	var msg api.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return api.Message{}, fmt.Errorf("%w: message %s: %v", ErrCorrupt, messageID, err)
	}
	return msg, nil
}
