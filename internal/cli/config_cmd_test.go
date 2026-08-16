package cli

import (
	"errors"
	"strings"
	"testing"

	"ezterm/internal/configstore"
	"ezterm/internal/sshconfig"
)

// TestConfigSSHRequiresConnParams asserts that `config ssh --name <n>` (and
// other partial invocations) are rejected instead of filling placeholder
// defaults: the user must provide host, user, an explicit auth mode, and that
// mode's connection parameter (password or key path).
func TestConfigSSHRequiresConnParams(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no params at all", []string{"--name", "aihub"}, "--host is required"},
		{"missing user", []string{"--name", "aihub", "--host", "h", "--auth", "password", "--password", "x"}, "--user is required"},
		{"missing auth mode", []string{"--name", "aihub", "--host", "h", "--user", "u"}, "choose an auth mode"},
		{"invalid auth mode", []string{"--name", "aihub", "--host", "h", "--user", "u", "--auth", "token"}, "choose an auth mode"},
		{"password mode without password", []string{"--name", "aihub", "--host", "h", "--user", "u", "--auth", "password"}, "--password is required"},
		{"key mode without key path", []string{"--name", "aihub", "--host", "h", "--user", "u", "--auth", "key"}, "--key-path is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := configstore.NewStore(t.TempDir())
			code := configSSH(false, store, tc.args)
			if code != 2 {
				t.Fatalf("configSSH exit = %d, want 2", code)
			}
			if _, err := store.GetSSH("aihub"); !errors.Is(err, configstore.ErrNotFound) {
				t.Fatalf("expected config not saved, got err=%v", err)
			}
		})
	}
}

func TestConfigSSHKeyMode(t *testing.T) {
	store := configstore.NewStore(t.TempDir())
	if code := configSSH(false, store, []string{
		"--name", "prod", "--host", "db.example.com", "--user", "deploy",
		"--auth", "key", "--key-path", "~/.ssh/id_ed25519",
	}); code != 0 {
		t.Fatalf("configSSH exit = %d, want 0", code)
	}
	p, err := store.GetSSH("prod")
	if err != nil {
		t.Fatal(err)
	}
	if p.Host != "db.example.com" || p.User != "deploy" || p.AuthMethod != sshconfig.AuthKey || p.KeyPath != "~/.ssh/id_ed25519" {
		t.Fatalf("unexpected profile: %+v", p)
	}
	if p.Password != "" {
		t.Fatalf("password should be empty for key mode, got %q", p.Password)
	}
}

func TestConfigSSHPasswordMode(t *testing.T) {
	store := configstore.NewStore(t.TempDir())
	if code := configSSH(false, store, []string{
		"--name", "box", "--host", "10.0.0.5", "--user", "root", "--auth", "password",
	}); code == 0 {
		t.Fatal("password mode without --password should fail")
	}
	if code := configSSH(false, store, []string{
		"--name", "box", "--host", "10.0.0.5", "--user", "root", "--auth", "password", "--password", "pw",
	}); code != 0 {
		t.Fatalf("configSSH exit = %d, want 0", code)
	}
	p, err := store.GetSSH("box")
	if err != nil {
		t.Fatal(err)
	}
	if p.AuthMethod != sshconfig.AuthPassword || p.Password != "pw" {
		t.Fatalf("unexpected profile: %+v", p)
	}
}

// TestConfigSSHJSONError ensures the human error text surfaces in JSON mode.
func TestConfigSSHJSONError(t *testing.T) {
	store := configstore.NewStore(t.TempDir())
	out := captureStdout(t, func() {
		if code := configSSH(true, store, []string{"--name", "aihub"}); code != 2 {
			t.Fatalf("configSSH --json exit = %d, want 2", code)
		}
	})
	if !strings.Contains(out, `{"error": "--host is required"}`) {
		t.Fatalf("unexpected JSON error output: %q", out)
	}
}
