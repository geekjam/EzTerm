package cli

import (
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"ezterm/internal/api"
	"ezterm/internal/configstore"
	"ezterm/internal/sshconfig"
)

func reorderFS() *flag.FlagSet {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("text", "", "text")
	fs.Bool("press-enter", false, "newline")
	fs.Int("reader", 0, "reader")
	fs.Bool("raw", false, "raw")
	fs.Int("timeout", 0, "timeout")
	fs.String("command", "", "command")
	fs.Var(&stringList{}, "args", "args")
	fs.String("mode", "", "mode")
	return fs
}

func TestReorderPositionals(t *testing.T) {
	fs := reorderFS()
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"id then flag", []string{"abc", "--text", "hi"}, []string{"--text", "hi", "abc"}},
		{"flag then id", []string{"--text", "hi", "abc"}, []string{"--text", "hi", "abc"}},
		{"inline value", []string{"--timeout=5", "abc"}, []string{"--timeout=5", "abc"}},
		{"bool does not consume", []string{"--raw", "abc"}, []string{"--raw", "abc"}},
		{"negative arg value", []string{"--command", "sh", "--args", "-c", "--args", "echo hi", "abc"},
			[]string{"--command", "sh", "--args", "-c", "--args", "echo hi", "abc"}},
		{"id only", []string{"abc"}, []string{"abc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reorderPositionals(fs, tc.args)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("reorderPositionals(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestSplitGlobalFlags(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantGlob []string
		wantRest []string
	}{
		{"json before command", []string{"--json", "list"}, []string{"--json"}, []string{"list"}},
		{"json after command", []string{"list", "--json"}, []string{"--json"}, []string{"list"}},
		{"port with value", []string{"--port", "18766", "--data-dir", "X", "start"},
			[]string{"--port", "18766", "--data-dir", "X"}, []string{"start"}},
		{"config keeps own port", []string{"config", "ssh", "--port", "2222", "mybox"},
			nil, []string{"config", "ssh", "--port", "2222", "mybox"}},
		{"config ssh port kept with global", []string{"--json", "config", "ssh", "--port", "2222", "mybox"},
			[]string{"--json"}, []string{"config", "ssh", "--port", "2222", "mybox"}},
		{"config global flag after subcommand", []string{"config", "list", "--json"},
			[]string{"--json"}, []string{"config", "list"}},
		{"daemon keeps own flags", []string{"daemon", "--port", "18770"},
			nil, []string{"daemon", "--port", "18770"}},
		{"send with globals and sub flags", []string{"--json", "send", "abc", "--text", "hi"},
			[]string{"--json"}, []string{"send", "abc", "--text", "hi"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			globals, rest := splitGlobalFlags(tc.args)
			if !reflect.DeepEqual(globals, tc.wantGlob) {
				t.Fatalf("globals = %v, want %v", globals, tc.wantGlob)
			}
			if !reflect.DeepEqual(rest, tc.wantRest) {
				t.Fatalf("rest = %v, want %v", rest, tc.wantRest)
			}
		})
	}
}

// captureStdout runs fn with os.Stdout redirected and returns everything it
// wrote. Restoring os.Stdout is t.Cleanup-safe so parallel tests are unaffected.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writeEnd
	t.Cleanup(func() {
		os.Stdout = old
		_ = readEnd.Close()
		_ = writeEnd.Close()
	})
	fn()
	_ = writeEnd.Close()
	data, err := io.ReadAll(readEnd)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// startCapture returns a client backed by a stub daemon plus the create request
// it received last and the session it replies with.
func startCapture(t *testing.T, session api.Session) (*client, *api.CreateSessionRequest) {
	t.Helper()
	got := &api.CreateSessionRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(got); err != nil {
			t.Errorf("decode create request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(session)
	}))
	t.Cleanup(server.Close)
	return &client{baseURL: server.URL, httpClient: server.Client()}, got
}

// TestStartWebTerminalByDefault covers the default of cmdStart: the Web terminal
// is requested without any flag, and the returned web_url surfaces in both
// human output and the --json session object.
func TestStartWebTerminalByDefault(t *testing.T) {
	const webURL = "http://127.0.0.1:18766/web/abc123abc123"
	c, got := startCapture(t, api.Session{
		ID:        "abc123abc123",
		Name:      "dev",
		Mode:      api.ModePTY,
		Status:    api.StatusRunning,
		Rows:      24,
		Cols:      80,
		WebURL:    webURL,
		CreatedAt: time.Now().UTC(),
	})
	dir := t.TempDir()
	store := configstore.NewStore(dir)
	if err := store.SaveLocal("dev", &configstore.LocalConfig{Mode: api.ModePTY}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if code := cmdStart(c, false, dir, []string{"--name", "dev"}); code != 0 {
			t.Fatalf("cmdStart exit = %d", code)
		}
	})
	if got.Web == nil || !*got.Web {
		t.Fatalf("create request web = %v, want explicit true", got.Web)
	}
	if !strings.Contains(out, "web terminal: "+webURL) {
		t.Fatalf("human output missing web URL: %q", out)
	}

	jsonOut := captureStdout(t, func() {
		if code := cmdStart(c, true, dir, []string{"--name", "dev"}); code != 0 {
			t.Fatalf("cmdStart --json exit = %d", code)
		}
	})
	if !strings.Contains(jsonOut, `"web_url":"`+webURL+`"`) {
		t.Fatalf("JSON output missing web_url: %q", jsonOut)
	}
}

// TestStartNoWebFlag asserts --noweb disables the Web terminal: web=false on the
// wire and no web URL line in human output.
func TestStartNoWebFlag(t *testing.T) {
	c, got := startCapture(t, api.Session{ID: "xyz123xyz123", Name: "dev", Mode: api.ModePTY})
	dir := t.TempDir()
	store := configstore.NewStore(dir)
	if err := store.SaveLocal("dev", &configstore.LocalConfig{Mode: api.ModePTY}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if code := cmdStart(c, false, dir, []string{"--name", "dev", "--noweb"}); code != 0 {
			t.Fatalf("cmdStart exit = %d", code)
		}
	})
	if got.Web == nil || *got.Web {
		t.Fatalf("create request web = %v, want explicit false", got.Web)
	}
	if strings.Contains(out, "web terminal:") {
		t.Fatalf("--noweb start printed a web URL: %q", out)
	}
}

// TestStartPipeSessionDisablesWeb asserts pipe configs are started with the Web
// terminal disabled instead of letting the daemon reject the request.
func TestStartPipeSessionDisablesWeb(t *testing.T) {
	c, got := startCapture(t, api.Session{ID: "pipe123pipe1", Name: "repl", Mode: api.ModePipe})
	dir := t.TempDir()
	store := configstore.NewStore(dir)
	if err := store.SaveLocal("repl", &configstore.LocalConfig{Command: "python3", Mode: api.ModePipe}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if code := cmdStart(c, false, dir, []string{"--name", "repl"}); code != 0 {
			t.Fatalf("cmdStart exit = %d", code)
		}
	})
	if got.Mode != api.ModePipe {
		t.Fatalf("create request mode = %q, want pipe", got.Mode)
	}
	if got.Web == nil || *got.Web {
		t.Fatalf("create request web = %v, want explicit false for pipe", got.Web)
	}
	if strings.Contains(out, "web terminal:") {
		t.Fatalf("pipe start printed a web URL: %q", out)
	}
}

// TestStartRejectsWebFlag asserts the removed --web flag now fails parsing.
func TestStartRejectsWebFlag(t *testing.T) {
	c, _ := startCapture(t, api.Session{ID: "abc123abc123", Mode: api.ModePTY})
	dir := t.TempDir()
	if err := configstore.NewStore(dir).SaveLocal("dev", &configstore.LocalConfig{Mode: api.ModePTY}); err != nil {
		t.Fatal(err)
	}
	if code := cmdStart(c, false, dir, []string{"--name", "dev", "--web"}); code != 2 {
		t.Fatalf("cmdStart --web exit = %d, want 2", code)
	}
}

// TestStartResolvesConfig verifies cmdStart loads a saved config by name and
// fills the create request accordingly: local configs carry command/args/mode,
// SSH configs carry ssh_config and a pty mode.
func TestStartResolvesConfig(t *testing.T) {
	var got api.CreateSessionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(api.Session{ID: "abc123abc123", Mode: got.Mode, SSHConfig: got.SSHConfig})
	}))
	defer server.Close()
	c := &client{baseURL: server.URL, httpClient: server.Client()}
	dir := t.TempDir()
	store := configstore.NewStore(dir)
	if err := store.SaveLocal("repl", &configstore.LocalConfig{Command: "python3", Mode: api.ModePipe}); err != nil {
		t.Fatal(err)
	}
	if code := cmdStart(c, false, dir, []string{"--name", "repl"}); code != 0 {
		t.Fatalf("local start exit = %d", code)
	}
	if got.Command != "python3" || got.Mode != api.ModePipe || got.Name != "" {
		t.Fatalf("local request = %+v", got)
	}

	if err := store.SaveSSH("prod", &sshconfig.Profile{Host: "example.com", User: "deploy"}); err != nil {
		t.Fatal(err)
	}
	if code := cmdStart(c, false, dir, []string{"--name", "prod"}); code != 0 {
		t.Fatalf("ssh start exit = %d", code)
	}
	if got.SSHConfig != "prod" || got.Mode != api.ModePTY {
		t.Fatalf("ssh request = %+v", got)
	}
}

// TestStartUnknownConfig asserts start fails fast (exit 2) before any daemon
// call when the named config does not exist.
func TestStartUnknownConfig(t *testing.T) {
	c := &client{baseURL: "http://127.0.0.1:1", httpClient: &http.Client{}}
	dir := t.TempDir()
	if code := cmdStart(c, false, dir, []string{"--name", "missing"}); code != 2 {
		t.Fatalf("start missing config exit = %d, want 2", code)
	}
}
