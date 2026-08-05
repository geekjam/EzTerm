package daemon

import (
	"context"
	_ "embed"
	"encoding/json"
	"net"
	"net/http"
	"strconv"

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
)

// webResizeMessage is the text-frame protocol sent by the browser when its
// xterm.js viewport changes size.
type webResizeMessage struct {
	Type string `json:"type"`
	Rows int    `json:"rows"`
	Cols int    `json:"cols"`
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

// handleWebSocket bridges one browser WebSocket to the session's shared PTY
// output buffer. Binary frames carry raw terminal input/output; text frames
// carry resize JSON messages.
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
				var message webResizeMessage
				if json.Unmarshal(data, &message) != nil || message.Type != "resize" {
					continue
				}
				if err := s.Resize(message.Rows, message.Cols); err != nil {
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
		if eof {
			return
		}
	}
}
