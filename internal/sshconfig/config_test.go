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
