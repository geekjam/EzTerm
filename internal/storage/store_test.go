package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ezterm/internal/api"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s := New(dir)
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSessionsSaveLoadRoundTrip(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	sessions := []api.Session{
		{ID: "abc123", Name: "one", Mode: api.ModePipe, Status: api.StatusExited, CreatedAt: now},
	}
	if err := s.SaveSessions(sessions); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].ID != "abc123" || loaded[0].Name != "one" {
		t.Fatalf("unexpected loaded sessions: %+v", loaded)
	}
}

func TestLoadSessionsEmpty(t *testing.T) {
	s := newTestStore(t)
	loaded, err := s.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 0 {
		t.Fatalf("expected empty, got %+v", loaded)
	}
}

func TestLoadSessionsCorruptQuarantined(t *testing.T) {
	s := newTestStore(t)
	path := filepath.Join(s.DataDir(), "sessions.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadSessions()
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt, got %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("expected empty after corruption, got %+v", loaded)
	}
	// The corrupt file must have been moved aside.
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("corrupt sessions.json should be quarantined, stat err=%v", statErr)
	}
}

func TestMessagePersistence(t *testing.T) {
	s := newTestStore(t)
	msg := api.Message{ID: "m1", SessionID: "sess1", Kind: "input", Text: "hi", CreatedAt: time.Now().UTC()}
	if err := s.SaveMessage("sess1", msg); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadMessage("sess1", "m1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Text != "hi" || loaded.Kind != "input" {
		t.Fatalf("unexpected message: %+v", loaded)
	}

	index := []api.MessageIndexEntry{{ID: "m1", Kind: "input", CreatedAt: msg.CreatedAt}}
	if err := s.SaveMessageIndex("sess1", index); err != nil {
		t.Fatal(err)
	}
	loadedIndex, err := s.LoadMessageIndex("sess1")
	if err != nil {
		t.Fatal(err)
	}
	if len(loadedIndex) != 1 || loadedIndex[0].ID != "m1" {
		t.Fatalf("unexpected index: %+v", loadedIndex)
	}
}

func TestInvalidIDRejected(t *testing.T) {
	s := newTestStore(t)
	if err := s.SaveMessage("../evil", api.Message{}); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("expected ErrInvalidID, got %v", err)
	}
	if err := s.SaveMessageIndex("../evil", nil); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("expected ErrInvalidID, got %v", err)
	}
}

func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.json")
	if err := WriteFileAtomic(path, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v2" {
		t.Fatalf("got %q", data)
	}
}
