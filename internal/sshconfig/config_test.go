package sshconfig

import (
	"errors"
	"testing"
)

func TestValidateDefaults(t *testing.T) {
	p := &Profile{Host: "h", User: "u"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.Port != DefaultPort {
		t.Fatalf("port should default to %d, got %d", DefaultPort, p.Port)
	}
	if p.AuthMethod != AuthPassword {
		t.Fatalf("auth should default to password, got %q", p.AuthMethod)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []struct {
		name string
		p    Profile
	}{
		{"missing host", Profile{User: "u"}},
		{"missing user", Profile{Host: "h"}},
		{"bad port", Profile{Host: "h", User: "u", Port: 70000}},
		{"bad auth", Profile{Host: "h", User: "u", AuthMethod: "token"}},
		{"key without path", Profile{Host: "h", User: "u", AuthMethod: AuthKey}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.p.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}
}

func TestStoreSaveGetList(t *testing.T) {
	s := NewStore(t.TempDir())
	p := &Profile{Host: "host.example", Port: 2222, User: "alice", AuthMethod: AuthKey, KeyPath: "~/.ssh/id_ed25519"}
	if err := s.Save("prod", p); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("prod")
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "host.example" || got.Port != 2222 || got.User != "alice" {
		t.Fatalf("unexpected profile: %+v", got)
	}

	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "prod" || list[0].AuthMethod != "key" {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestStoreGetNotFound(t *testing.T) {
	s := NewStore(t.TempDir())
	_, err := s.Get("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStoreBadName(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Save("../evil", &Profile{Host: "h", User: "u"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}
