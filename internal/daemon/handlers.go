package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ezterm/internal/api"
	"ezterm/internal/configstore"
	"ezterm/internal/session"
	"ezterm/internal/sshconfig"
)

// maxBodyBytes caps request bodies to protect the daemon.
const maxBodyBytes = 1 << 20

// maxReadTimeout caps blocking output reads.
const maxReadTimeout = 300 * time.Second

// Handler serves the ezterm HTTP API and the embedded Web terminal.
type Handler struct {
	mgr      *session.Manager
	cfgStore *configstore.Store
	host     string
	port     int
	mux      *http.ServeMux
}

// NewHandler builds the HTTP API router with the documented local defaults.
// NewHandlerWithAddress is used by the daemon when its bind address is known.
func NewHandler(mgr *session.Manager, cfgStore *configstore.Store) http.Handler {
	return NewHandlerWithAddress(mgr, cfgStore, "127.0.0.1", 18766)
}

// NewHandlerWithAddress builds the HTTP API router and advertises Web URLs
// using the daemon's bind address.
func NewHandlerWithAddress(mgr *session.Manager, cfgStore *configstore.Store, host string, port int) http.Handler {
	h := &Handler{mgr: mgr, cfgStore: cfgStore, host: host, port: port, mux: http.NewServeMux()}

	h.mux.HandleFunc("GET /health", h.handleHealth)
	h.mux.HandleFunc("GET /api/sessions", h.handleListSessions)
	h.mux.HandleFunc("POST /api/sessions", h.handleCreateSession)
	h.mux.HandleFunc("GET /api/sessions/{id}", h.handleGetSession)
	h.mux.HandleFunc("POST /api/sessions/{id}/input", h.handleInput)
	h.mux.HandleFunc("GET /api/sessions/{id}/output", h.handleOutput)
	h.mux.HandleFunc("GET /api/sessions/{id}/attach", h.handleAttach)
	h.mux.HandleFunc("POST /api/sessions/{id}/readers", h.handleNewReader)
	h.mux.HandleFunc("POST /api/sessions/{id}/terminate", h.handleTerminate)
	h.mux.HandleFunc("DELETE /api/sessions/{id}", h.handleDelete)
	h.mux.HandleFunc("POST /api/sessions/{id}/resize", h.handleResize)
	h.mux.HandleFunc("GET /api/configs", h.handleListConfigs)
	h.mux.HandleFunc("GET /api/configs/{name}", h.handleGetConfig)
	h.mux.HandleFunc("POST /api/configs/{name}", h.handleUpsertConfig)
	h.mux.HandleFunc("DELETE /api/configs/{name}", h.handleDeleteConfig)
	h.mux.HandleFunc("GET /web/{id}", h.handleWebPage)
	h.mux.HandleFunc("GET /web/app.js", h.handleWebApp)
	h.mux.HandleFunc("GET /web/style.css", h.handleWebStyle)
	h.mux.HandleFunc("GET /web/{id}/ws", h.handleWebSocket)
	h.mux.HandleFunc("GET /config", h.handleConfigPage)
	h.mux.HandleFunc("GET /config/app.js", h.handleConfigApp)
	h.mux.HandleFunc("GET /config/style.css", h.handleConfigStyle)

	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("encode response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, api.ErrorResponse{Error: fmt.Sprintf(format, args...)})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: %v", err)
		return false
	}
	return true
}

func (h *Handler) sessionFromRequest(w http.ResponseWriter, r *http.Request) (*session.Session, bool) {
	id := r.PathValue("id")
	s := h.mgr.Get(id)
	if s == nil {
		writeError(w, http.StatusNotFound, "session %q not found", id)
		return nil, false
	}
	return s, true
}

// --- endpoints ---

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.HealthResponse{Status: "ok"})
}

func (h *Handler) handleListSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sessions": h.mgr.ListAll()})
}

func (h *Handler) handleGetSession(w http.ResponseWriter, r *http.Request) {
	s, ok := h.sessionFromRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.Info())
}

// handleCreateSession creates a session. Web sessions (--web) are only valid
// in PTY mode; pipe mode returns 409 so callers learn about the conflict
// immediately. For enabled sessions the daemon advertises the Web terminal
// URL on web_url.
func (h *Handler) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req api.CreateSessionRequest
	if !decodeBody(w, r, &req) {
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = api.ModePTY
	}
	if req.Web && mode != api.ModePTY {
		writeError(w, http.StatusConflict, "web terminal requires a PTY session (mode %q)", mode)
		return
	}
	cfg := session.Config{
		Command:     strings.TrimSpace(req.Command),
		Args:        req.Args,
		Mode:        mode,
		Name:        strings.TrimSpace(req.Name),
		Rows:        req.Rows,
		Cols:        req.Cols,
		SSHConfig:   strings.TrimSpace(req.SSHConfig),
		DialTimeout: time.Duration(req.DialTimeoutSeconds) * time.Second,
		WebEnabled:  req.Web,
	}
	if cfg.Command == "" && !session.IsRemote(cfg.SSHConfig) && cfg.Mode == api.ModePipe {
		writeError(w, http.StatusBadRequest, "command is required for local pipe sessions")
		return
	}
	s, err := h.mgr.Create(cfg)
	if err != nil {
		writeError(w, http.StatusBadRequest, "create session: %v", err)
		return
	}
	if s.IsWebEnabled() {
		s.SetWebURL(h.webURL(s.ID))
		h.mgr.Persist()
	}
	writeJSON(w, http.StatusCreated, s.Info())
}

func (h *Handler) handleInput(w http.ResponseWriter, r *http.Request) {
	s, ok := h.sessionFromRequest(w, r)
	if !ok {
		return
	}
	var req api.InputRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.SendInput(req.Text, req.PressEnter); err != nil {
		writeError(w, http.StatusConflict, "send input: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleOutput(w http.ResponseWriter, r *http.Request) {
	s, ok := h.sessionFromRequest(w, r)
	if !ok {
		return
	}
	readerID, err := intQuery(r, "reader_id", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	timeout := durationQuery(r, "timeout", 30*time.Second)
	if timeout > maxReadTimeout {
		timeout = maxReadTimeout
	}
	raw := r.URL.Query().Get("raw") == "true"
	maxBytes, err := intQuery(r, "max_bytes", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	data, eof, err := s.ReadOutput(r.Context(), readerID, timeout, maxBytes, raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read output: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, api.OutputResponse{Data: data, EOF: eof})
}

// attachReadTimeout paces each blocking read on the output buffer while an
// attach stream is idle. The buffer wakes readers immediately on new output,
// so this only bounds idle polling without adding latency to live bytes.
const attachReadTimeout = 250 * time.Millisecond

// attachChunkSize caps the bytes forwarded per flush to bound memory.
const attachChunkSize = 64 * 1024

// handleAttach streams the raw PTY output of a session to an attach client.
// The stream starts by replaying everything retained in the output buffer
// (restoring the current screen), then follows live output until the session
// ends (io.EOF) or the client disconnects (context cancel). Pipe-mode
// sessions cannot be attached and return 409.
func (h *Handler) handleAttach(w http.ResponseWriter, r *http.Request) {
	s, ok := h.sessionFromRequest(w, r)
	if !ok {
		return
	}
	if info := s.Info(); info.Mode != api.ModePTY {
		writeError(w, http.StatusConflict, "session %q is %s mode; attach requires a PTY session", info.ID, info.Mode)
		return
	}
	readerID, err := s.AttachReader()
	if err != nil {
		writeError(w, http.StatusConflict, "attach: %v", err)
		return
	}
	defer s.ReleaseReader(readerID)

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}

	ctx := r.Context()
	for {
		data, eof, err := s.ReadOutput(ctx, readerID, attachReadTimeout, attachChunkSize, true)
		if err != nil {
			return // client disconnected or reader released; the stream is over
		}
		if len(data) > 0 {
			if _, werr := io.WriteString(w, data); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if eof {
			return // session ended; the client observes EOF and exits
		}
	}
}

func (h *Handler) handleNewReader(w http.ResponseWriter, r *http.Request) {
	s, ok := h.sessionFromRequest(w, r)
	if !ok {
		return
	}
	id, err := s.NewReader()
	if err != nil {
		writeError(w, http.StatusConflict, "new reader: %v", err)
		return
	}
	writeJSON(w, http.StatusCreated, api.ReaderResponse{ReaderID: id})
}

func (h *Handler) handleTerminate(w http.ResponseWriter, r *http.Request) {
	s, ok := h.sessionFromRequest(w, r)
	if !ok {
		return
	}
	force := r.URL.Query().Get("force") == "true"
	grace := durationQuery(r, "grace", 5*time.Second)
	h.mgr.Terminate(s.ID, grace, force)
	writeJSON(w, http.StatusOK, api.TerminateResponse{Session: s.Info()})
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	s, ok := h.sessionFromRequest(w, r)
	if !ok {
		return
	}
	if err := h.mgr.Delete(s.ID); err != nil {
		writeError(w, http.StatusConflict, "delete session: %v", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleResize(w http.ResponseWriter, r *http.Request) {
	s, ok := h.sessionFromRequest(w, r)
	if !ok {
		return
	}
	var req struct {
		Rows int `json:"rows"`
		Cols int `json:"cols"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.Resize(req.Rows, req.Cols); err != nil {
		writeError(w, http.StatusBadRequest, "resize: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, s.Info())
}

func (h *Handler) handleListConfigs(w http.ResponseWriter, r *http.Request) {
	configs, err := h.cfgStore.ListAll()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list configs: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configs": configs})
}

func (h *Handler) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	detail, err := h.getConfigDetail(name)
	if err != nil {
		writeConfigStoreError(w, "get config", err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) handleUpsertConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req api.ConfigUpsertRequest
	if !decodeBody(w, r, &req) {
		return
	}

	typeName := strings.ToLower(strings.TrimSpace(req.Type))
	if typeName != string(configstore.TypeLocal) && typeName != string(configstore.TypeSSH) {
		writeError(w, http.StatusBadRequest, "type must be %q or %q", configstore.TypeLocal, configstore.TypeSSH)
		return
	}

	existingType, exists, err := h.configType(name)
	if err != nil {
		writeConfigStoreError(w, "check config", err)
		return
	}
	requestedType := configstore.Type(typeName)
	if exists && existingType != requestedType {
		writeError(w, http.StatusConflict, "config %q already exists as a %s config", name, existingType)
		return
	}

	switch requestedType {
	case configstore.TypeLocal:
		cfg := &configstore.LocalConfig{
			Command: req.Command,
			Args:    append([]string(nil), req.Args...),
			Mode:    api.Mode(req.Mode),
		}
		if cfg.Mode == api.ModePipe && strings.TrimSpace(cfg.Command) == "" {
			writeError(w, http.StatusBadRequest, "command is required for a pipe config")
			return
		}
		if err := h.cfgStore.SaveLocal(name, cfg); err != nil {
			writeConfigStoreError(w, "save local config", err)
			return
		}
	case configstore.TypeSSH:
		profile := &sshconfig.Profile{
			Host:         req.Host,
			Port:         req.Port,
			User:         req.User,
			AuthMethod:   sshconfig.AuthMethod(strings.TrimSpace(req.AuthMethod)),
			Password:     req.Password,
			KeyPath:      req.KeyPath,
			DefaultShell: req.Shell,
		}
		if err := profile.Validate(); err != nil {
			writeConfigStoreError(w, "validate SSH config", err)
			return
		}
		if profile.AuthMethod == sshconfig.AuthPassword {
			if strings.TrimSpace(profile.Password) == "" && exists {
				previous, getErr := h.cfgStore.GetSSH(name)
				if getErr != nil {
					writeConfigStoreError(w, "read existing SSH config", getErr)
					return
				}
				if previous.AuthMethod == sshconfig.AuthPassword {
					profile.Password = previous.Password
				}
			}
			if strings.TrimSpace(profile.Password) == "" {
				writeError(w, http.StatusBadRequest, "password is required for password authentication (leave it blank only when retaining an existing password)")
				return
			}
		} else {
			// A key-auth update must not retain an old password in the stored
			// profile or accidentally expose two authentication methods.
			profile.Password = ""
		}
		if err := h.cfgStore.SaveSSH(name, profile); err != nil {
			writeConfigStoreError(w, "save SSH config", err)
			return
		}
	}

	detail, err := h.getConfigDetail(name)
	if err != nil {
		writeConfigStoreError(w, "read saved config", err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) handleDeleteConfig(w http.ResponseWriter, r *http.Request) {
	if err := h.cfgStore.Delete(r.PathValue("name")); err != nil {
		writeConfigStoreError(w, "delete config", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// configType finds the type currently using name. It is used before an upsert
// so a request cannot accidentally turn a same-named local config into SSH (or
// vice versa), preserving configstore's global name uniqueness rule.
func (h *Handler) configType(name string) (configstore.Type, bool, error) {
	if _, err := h.cfgStore.GetLocal(name); err == nil {
		return configstore.TypeLocal, true, nil
	} else if !errors.Is(err, configstore.ErrNotFound) {
		return "", false, err
	}
	if _, err := h.cfgStore.GetSSH(name); err == nil {
		return configstore.TypeSSH, true, nil
	} else if !errors.Is(err, configstore.ErrNotFound) {
		return "", false, err
	}
	return "", false, nil
}

func (h *Handler) getConfigDetail(name string) (api.ConfigDetail, error) {
	if cfg, err := h.cfgStore.GetLocal(name); err == nil {
		return api.ConfigDetail{
			Name:    name,
			Type:    string(configstore.TypeLocal),
			Command: cfg.Command,
			Args:    append([]string(nil), cfg.Args...),
			Mode:    string(cfg.Mode),
		}, nil
	} else if !errors.Is(err, configstore.ErrNotFound) {
		return api.ConfigDetail{}, err
	}

	profile, err := h.cfgStore.GetSSH(name)
	if err != nil {
		return api.ConfigDetail{}, err
	}
	return api.ConfigDetail{
		Name:       name,
		Type:       string(configstore.TypeSSH),
		Host:       profile.Host,
		Port:       profile.Port,
		User:       profile.User,
		AuthMethod: string(profile.AuthMethod),
		KeyPath:    profile.KeyPath,
		Shell:      profile.DefaultShell,
	}, nil
}

func writeConfigStoreError(w http.ResponseWriter, action string, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, configstore.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, configstore.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, configstore.ErrExists):
		status = http.StatusConflict
	}
	writeError(w, status, "%s: %v", action, err)
}

// --- helpers for query parsing ---

func intQuery(r *http.Request, name string, def int) (int, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return def, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %q", name, value)
	}
	return parsed, nil
}

func durationQuery(r *http.Request, name string, def time.Duration) time.Duration {
	value := r.URL.Query().Get(name)
	if value == "" {
		return def
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || seconds < 0 {
		return def
	}
	return time.Duration(seconds * float64(time.Second))
}
