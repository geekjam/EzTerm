package daemon

import (
	"context"
	_ "embed"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"ezterm/internal/api"
	"ezterm/internal/session"
	"github.com/coder/websocket"
)

// The Web terminal has no build step. These files are served from the same
// binary as the daemon so the CLI remains a single-file deployment.
var (
	//go:embed web/index.html
	webIndexHTML []byte
	//go:embed web/app.js
	webAppJS []byte
	//go:embed web/style.css
	webStyleCSS []byte
	//go:embed web/config.html
	webConfigHTML []byte
	//go:embed web/config.js
	webConfigJS []byte
	//go:embed web/config.css
	webConfigCSS []byte
)

// webResizeMessage is the text-frame protocol sent by the browser when its
// xterm.js viewport changes size.
type webResizeMessage struct {
	Type string `json:"type"`
	Rows int    `json:"rows"`
	Cols int    `json:"cols"`
}

// webControlMessage is the text-frame envelope sent by the browser for control
// messages other than resize. Type selects the payload field: "secret" carries
// Text, and is the only message that may contain a credential.
type webControlMessage struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// webPromptMessage announces an active password prompt to the page.
type webPromptMessage struct {
	Type  string `json:"type"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// webSecretResultMessage reports the outcome of a submitted secret. It never
// echoes the secret itself.
type webSecretResultMessage struct {
	Type  string `json:"type"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// webSessionEndMessage is the text frame sent right before the connection
// closes because the session's process exited. The page uses it to tell a
// finished session apart from a dropped WebSocket connection: a broken
// transport never sends this frame.
const webSessionEndMessage = `{"type":"session-end"}`

// webPromptClearedMessage is the text frame sent when the active password
// prompt disappears (answered, or superseded by new output).
const webPromptClearedMessage = `{"type":"prompt-cleared"}`

// isLoopbackRemote reports whether the WebSocket peer is the local host.
// Secrets may only be submitted from loopback: the daemon has no other
// authentication, so a remote page must never be able to answer a prompt.
func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (h *Handler) webURL(id string) string {
	return "http://" + net.JoinHostPort(h.host, strconv.Itoa(h.port)) + "/web/" + id
}

// webSession checks the common access rules for the page and WebSocket
// endpoints. Pipe sessions are rejected before the Web-enabled check so the
// response distinguishes an unsupported terminal mode from a disabled page.
func (h *Handler) webSession(w http.ResponseWriter, r *http.Request) (*session.Session, bool) {
	s, ok := h.sessionFromRequest(w, r)
	if !ok {
		return nil, false
	}
	if info := s.Info(); info.Mode != api.ModePTY {
		writeError(w, http.StatusConflict, "session %q is %s mode; web terminal requires a PTY session", info.ID, info.Mode)
		return nil, false
	}
	if !s.IsWebEnabled() {
		writeError(w, http.StatusNotFound, "web terminal is not enabled for session %q", s.ID)
		return nil, false
	}
	return s, true
}

func (h *Handler) handleWebPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.webSession(w, r); !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(webIndexHTML)
}

func (h *Handler) handleWebApp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write(webAppJS)
}

func (h *Handler) handleWebStyle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write(webStyleCSS)
}

func (h *Handler) handleConfigPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(webConfigHTML)
}

func (h *Handler) handleConfigApp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write(webConfigJS)
}

func (h *Handler) handleConfigStyle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write(webConfigCSS)
}

// handleWebSocket bridges one browser WebSocket to the session's shared PTY
// output buffer. Binary frames carry raw terminal input/output; text frames
// carry JSON control messages: resize and secret from the browser, and prompt,
// prompt-cleared, secret-result, and session-end from the server. Secret text
// is never logged and never echoed back.
func (h *Handler) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	s, ok := h.webSession(w, r)
	if !ok {
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	readerID, err := s.AttachReader()
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "attach: "+err.Error())
		return
	}
	defer s.ReleaseReader(readerID)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	allowSecret := isLoopbackRemote(r.RemoteAddr)
	// secretInFlight lets the output loop wait for a submission result before it
	// sends session-end: a failed dial finalizes the session while the input
	// goroutine is still about to report secret-result.
	var secretInFlight atomic.Bool

	// The input goroutine consumes frames in the background; on any read or
	// send failure it cancels the context so the output loop stops too.
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		defer cancel()
		for {
			typ, data, readErr := conn.Read(ctx)
			if readErr != nil {
				return
			}
			switch typ {
			case websocket.MessageBinary:
				if err := s.SendInput(string(data), false); err != nil {
					return
				}
			case websocket.MessageText:
				if !h.handleWebControl(ctx, conn, s, data, allowSecret, &secretInFlight) {
					return
				}
			}
		}
	}()

	defer func() {
		cancel()
		_ = conn.Close(websocket.StatusNormalClosure, "")
		<-inputDone
	}()

	// Replay the active prompt first so a reconnect (or a page opened after the
	// session started waiting) still shows the dialog.
	sentPrompt := s.PendingPrompt()
	if sentPrompt != nil {
		if err := writePromptFrame(ctx, conn, sentPrompt); err != nil {
			return
		}
	}

	for {
		data, eof, readErr := s.ReadOutput(ctx, readerID, attachReadTimeout, attachChunkSize, true)
		if readErr != nil {
			return
		}
		if len(data) > 0 {
			if err := conn.Write(ctx, websocket.MessageBinary, []byte(data)); err != nil {
				return
			}
		}
		if current := s.PendingPrompt(); promptChanged(sentPrompt, current) {
			var writeErr error
			if current == nil {
				writeErr = conn.Write(ctx, websocket.MessageText, []byte(webPromptClearedMessage))
			} else {
				writeErr = writePromptFrame(ctx, conn, current)
			}
			if writeErr != nil {
				return
			}
			sentPrompt = current
		}
		if eof {
			// The process exited and retained output is drained, so flag the end
			// of the session before closing. The explicit close handshake keeps
			// the cancellation deferred below from tearing the transport down
			// abruptly, which the page would read as a dropped connection.
			waitSecretIdle(&secretInFlight)
			if writeErr := conn.Write(ctx, websocket.MessageText, []byte(webSessionEndMessage)); writeErr != nil {
				return
			}
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return
		}
	}
}

// waitSecretIdle blocks briefly until an in-flight secret submission has
// reported its result. The bound keeps a slow dial from delaying session-end
// indefinitely.
func waitSecretIdle(inFlight *atomic.Bool) {
	deadline := time.Now().Add(2 * time.Second)
	for inFlight.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}

// handleWebControl dispatches one browser text frame. It reports whether the
// connection should stay open; a false result ends the input loop.
func (h *Handler) handleWebControl(ctx context.Context, conn *websocket.Conn, s *session.Session, data []byte, allowSecret bool, secretInFlight *atomic.Bool) bool {
	var message webControlMessage
	if json.Unmarshal(data, &message) != nil {
		return true
	}
	switch message.Type {
	case "resize":
		var resize webResizeMessage
		if json.Unmarshal(data, &resize) != nil {
			return true
		}
		return s.Resize(resize.Rows, resize.Cols) == nil
	case "secret":
		// Never log or store message.Text: it is a credential.
		if !allowSecret {
			return writeSecretResult(ctx, conn, false, "password submission is only allowed from the local host") == nil
		}
		secretInFlight.Store(true)
		err := s.SubmitSecret(message.Text)
		errText := ""
		if err != nil {
			errText = err.Error()
		}
		resultErr := writeSecretResult(ctx, conn, err == nil, errText)
		secretInFlight.Store(false)
		return resultErr == nil
	}
	return true
}

// writePromptFrame sends one prompt announcement.
func writePromptFrame(ctx context.Context, conn *websocket.Conn, p *api.Prompt) error {
	payload, err := json.Marshal(webPromptMessage{Type: "prompt", Kind: p.Kind, Label: p.Label})
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, payload)
}

// writeSecretResult reports whether a submitted secret was accepted.
func writeSecretResult(ctx context.Context, conn *websocket.Conn, ok bool, errText string) error {
	payload, err := json.Marshal(webSecretResultMessage{Type: "secret-result", OK: ok, Error: errText})
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, payload)
}

// promptChanged reports whether the prompt shown to the page must be updated.
func promptChanged(sent, current *api.Prompt) bool {
	switch {
	case sent == nil && current == nil:
		return false
	case sent == nil || current == nil:
		return true
	}
	return sent.Kind != current.Kind || sent.Label != current.Label
}
