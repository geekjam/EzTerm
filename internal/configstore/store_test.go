package configstore

import (
	"errors"
	"testing"

	"ezterm/internal/api"
	"ezterm/internal/sshconfig"
)

func TestLocalRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())
	cfg := &LocalConfig{Command: "bash", Args: []string{"-l"}}
	if err := s.SaveLocal("dev", cfg); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetLocal("dev")
	if err != nil {
		t.Fatal(err)
	}
	if got.Command != "bash" || len(got.Args) != 1 || got.Mode != api.ModePTY {
		t.Fatalf("unexpected local config: %+v", got)
	}
}

func TestSSHRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())
	p := &sshconfig.Profile{Host: "host.example", Port: 2222, User: "alice", AuthMethod: sshconfig.AuthKey, KeyPath: "~/.ssh/id_ed25519"}
	if err := s.SaveSSH("prod", p); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSSH("prod")
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "host.example" || got.User != "alice" || got.AuthMethod != sshconfig.AuthKey {
		t.Fatalf("unexpected SSH config: %+v", got)
	}
}

func TestCrossTypeUniqueness(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.SaveLocal("dev", &LocalConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSSH("dev", &sshconfig.Profile{Host: "h", User: "u"}); !errors.Is(err, ErrExists) {
		t.Fatalf("expected ErrExists, got %v", err)
	}
	// Overwriting the same type is allowed.
	if err := s.SaveLocal("dev", &LocalConfig{Command: "zsh"}); err != nil {
		t.Fatalf("overwrite same type should succeed: %v", err)
	}
}

func TestDelete(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.SaveLocal("dev", &LocalConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSSH("prod", &sshconfig.Profile{Host: "h", User: "u"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSSH("prod"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
	if err := s.Delete("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound deleting missing, got %v", err)
	}
}

func TestResolve(t *testing.T) {
	s := NewStore(t.TempDir())
	_, err := s.Resolve("missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := s.SaveLocal("dev", &LocalConfig{Command: "bash"}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Resolve("dev")
	if err != nil || r.Type != TypeLocal || r.Local == nil {
		t.Fatalf("resolve local: r=%+v err=%v", r, err)
	}
	if err := s.SaveSSH("prod", &sshconfig.Profile{Host: "h", User: "u"}); err != nil {
		t.Fatal(err)
	}
	r, err = s.Resolve("prod")
	if err != nil || r.Type != TypeSSH || r.Profile == nil {
		t.Fatalf("resolve ssh: r=%+v err=%v", r, err)
	}
}

func TestListAll(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.SaveLocal("dev", &LocalConfig{Command: "bash", Mode: api.ModePTY}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSSH("prod", &sshconfig.Profile{Host: "db", User: "u"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Name != "dev" || list[0].Type != string(TypeLocal) || list[0].Host != "" {
		t.Fatalf("unexpected list: %+v", list)
	}
	if list[1].Name != "prod" || list[1].Type != string(TypeSSH) || list[1].Host != "db" {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestBadName(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.SaveLocal("../evil", &LocalConfig{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestNotFound(t *testing.T) {
	s := NewStore(t.TempDir())
	if _, err := s.GetLocal("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
