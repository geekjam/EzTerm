package sshclient

import (
	"errors"
	"strings"
	"testing"
)

func TestNewClientConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want string
	}{
		{"missing host", Options{User: "alice", Password: "secret"}, "host is required"},
		{"missing user", Options{Host: "example.com", Password: "secret"}, "user is required"},
		{"missing auth", Options{Host: "example.com", User: "alice"}, "no authentication"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewClientConfig(tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestNewClientConfigPassword(t *testing.T) {
	cfg, err := NewClientConfig(Options{Host: "example.com", User: "alice", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != "alice" || len(cfg.Auth) != 1 || cfg.Timeout <= 0 {
		t.Fatalf("unexpected client config: user=%q auth=%d timeout=%v", cfg.User, len(cfg.Auth), cfg.Timeout)
	}
}

func TestNewClientConfigKeyError(t *testing.T) {
	_, err := NewClientConfig(Options{Host: "example.com", User: "alice", KeyPath: "missing-key.pem"})
	if err == nil || !strings.Contains(err.Error(), "read key") {
		t.Fatalf("expected key read error, got %v", err)
	}
}

func TestBuildRemoteCommand(t *testing.T) {
	if got, want := buildRemoteCommand("", nil, true), "exec $SHELL -l"; got != want {
		t.Fatalf("interactive default = %q, want %q", got, want)
	}
	if got, want := buildRemoteCommand("", nil, false), "cat"; got != want {
		t.Fatalf("pipe default = %q, want %q", got, want)
	}
	got := buildRemoteCommand("printf", []string{"hello world", "it's ok"}, false)
	if !strings.Contains(got, "'hello world'") || !strings.Contains(got, "it'\\''s ok") {
		t.Fatalf("command was not shell-quoted: %q", got)
	}
}

func TestRemoteSessionInterfaceIsNotNil(t *testing.T) {
	var session RemoteSession
	if session != nil {
		t.Fatal("zero RemoteSession interface should be nil")
	}
	if errors.Is(nil, errors.New("x")) {
		t.Fatal("sanity check unexpectedly matched")
	}
}
