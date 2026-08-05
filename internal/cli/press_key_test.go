package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ezterm/internal/api"
)

// newFakeSendClient starts a daemon-shaped server that accepts POST /input and
// returns a client wired to it. The captured request and request counter are
// updated on every call.
func newFakeSendClient(t *testing.T, req *api.InputRequest, requests *int) *client {
	t.Helper()
	logged := 0
	count := requests
	if count == nil {
		count = &logged
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*count++
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions/sess12345/input" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(req); err != nil {
			t.Errorf("decode input request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	t.Cleanup(server.Close)
	return &client{baseURL: server.URL, httpClient: server.Client()}
}

func TestCmdSendPressKeyWritesRawBytes(t *testing.T) {
	var req api.InputRequest
	requests := 0
	c := newFakeSendClient(t, &req, &requests)

	out := captureStdout(t, func() {
		if code := cmdSend(c, false, []string{"sess12345", "--press-key", "ctrl+c"}); code != 0 {
			t.Fatalf("cmdSend exit = %d", code)
		}
	})
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if req.Text != "\x03" {
		t.Fatalf("input text = %q, want %q", req.Text, "\x03")
	}
	if req.PressEnter {
		t.Fatal("press_enter must be false for --press-key")
	}
	if !strings.Contains(out, "input sent to session sess12345") {
		t.Fatalf("human output = %q", out)
	}
}

func TestCmdSendPressKeyJSON(t *testing.T) {
	var req api.InputRequest
	requests := 0
	c := newFakeSendClient(t, &req, &requests)

	jsonOut := captureStdout(t, func() {
		if code := cmdSend(c, true, []string{"sess12345", "--press-key", "enter"}); code != 0 {
			t.Fatalf("cmdSend exit = %d", code)
		}
	})
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if req.Text != "\r" {
		t.Fatalf("input text = %q, want CR", req.Text)
	}
	if !strings.Contains(jsonOut, `"ok":true`) || !strings.Contains(jsonOut, `"session_id":"sess12345"`) {
		t.Fatalf("JSON output = %q", jsonOut)
	}
}

func TestCmdSendPressKeyConflicts(t *testing.T) {
	var req api.InputRequest
	requests := 0
	c := newFakeSendClient(t, &req, &requests)

	cases := [][]string{
		{"sess12345", "--press-key", "ctrl+c", "--text", "x"},
		{"sess12345", "--press-key", "ctrl+c", "--press-enter"},
		// An explicitly empty --text still counts as given and conflicts.
		{"sess12345", "--text", "", "--press-key", "enter"},
	}
	for _, args := range cases {
		out := captureStdout(t, func() {
			if code := cmdSend(c, true, args); code != 2 {
				t.Fatalf("cmdSend(%v) exit = %d, want 2", args, code)
			}
		})
		if !strings.Contains(out, "mutually exclusive") {
			t.Fatalf("cmdSend(%v) output = %q", args, out)
		}
	}
	if requests != 0 {
		t.Fatalf("conflicting invocations made %d requests, want 0", requests)
	}
}

func TestCmdSendPressKeyUnknownKey(t *testing.T) {
	var req api.InputRequest
	requests := 0
	c := newFakeSendClient(t, &req, &requests)

	out := captureStdout(t, func() {
		if code := cmdSend(c, true, []string{"sess12345", "--press-key", "foo"}); code != 2 {
			t.Fatalf("cmdSend exit = %d, want 2", code)
		}
	})
	if requests != 0 {
		t.Fatalf("unknown key made %d requests, want 0", requests)
	}
	if !strings.Contains(out, "press-key: unknown key") {
		t.Fatalf("error output = %q", out)
	}
}
