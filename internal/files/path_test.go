package files

import (
	"errors"
	"path"
	"path/filepath"
	"testing"

	"ezterm/internal/api"
)

func TestResolveLocal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home", "tester")
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{"empty is root", "", root, nil},
		{"tilde is root", "~", root, nil},
		{"tilde child", "~/logs/app.log", filepath.Join(root, "logs", "app.log"), nil},
		{"relative", "logs/app.log", filepath.Join(root, "logs", "app.log"), nil},
		{"dot relative", "./a", filepath.Join(root, "a"), nil},
		{"dotdot", "../shared/x", filepath.Join(root, "..", "shared", "x"), nil},
		{"absolute", filepath.Join(root, "abs", "file"), filepath.Join(root, "abs", "file"), nil},
		{"redundant separators", "a//b/", filepath.Join(root, "a", "b"), nil},
		{"other user", "~other/x", "", ErrInvalidPath},
		{"nul", "a\x00b", "", ErrInvalidPath},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveLocal(root, tc.input)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != filepath.Clean(tc.want) {
				t.Fatalf("got %q, want %q", got, filepath.Clean(tc.want))
			}
		})
	}
}

func TestResolveRemote(t *testing.T) {
	const root = "/home/tester"
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{"empty is root", "", root, nil},
		{"tilde is root", "~", root, nil},
		{"tilde child", "~/logs/app.log", "/home/tester/logs/app.log", nil},
		{"relative", "logs/app.log", "/home/tester/logs/app.log", nil},
		{"dotdot", "../shared/x", "/home/shared/x", nil},
		{"absolute", "/var/log/syslog", "/var/log/syslog", nil},
		{"redundant separators", "a//b/", "/home/tester/a/b", nil},
		{"other user", "~other/x", "", ErrInvalidPath},
		{"nul", "a\x00b", "", ErrInvalidPath},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveRemote(root, tc.input)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != path.Clean(tc.want) {
				t.Fatalf("got %q, want %q", got, path.Clean(tc.want))
			}
		})
	}
}

func TestBaseName(t *testing.T) {
	cases := map[string]string{
		"/home/a.txt":      "a.txt",
		"/home/dir/":       "dir",
		"C:\\Users\\a.txt": "a.txt",
		"plain":            "plain",
	}
	for input, want := range cases {
		if got := baseName(input); got != want {
			t.Fatalf("baseName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSortEntries(t *testing.T) {
	entries := []api.FileEntry{
		{Name: "zeta.txt"},
		{Name: "Alpha"},
		{Name: "beta", Dir: true},
		{Name: "link", Symlink: true},
		{Name: "alpha"},
	}
	sortEntries(entries)
	want := []string{"beta", "link", "Alpha", "alpha", "zeta.txt"}
	for i, name := range want {
		if entries[i].Name != name {
			t.Fatalf("entry %d = %q, want %q (order %v)", i, entries[i].Name, name, names(entries))
		}
	}
}

// names flattens entries for failure messages.
func names(entries []api.FileEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name
	}
	return out
}

func TestFormatMode(t *testing.T) {
	if got := formatMode(0o644); got != "0644" {
		t.Fatalf("formatMode(0644) = %q", got)
	}
	if got := formatMode(0o755 | 1<<31); got != "0755" {
		t.Fatalf("formatMode with mode bits = %q, want 0755", got)
	}
}
