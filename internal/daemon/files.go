package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"ezterm/internal/api"
	"ezterm/internal/files"
	"ezterm/internal/session"
)

// allowFilePeer enforces the file transfer access boundary: the request must
// come from loopback, and a browser Origin (when present) must match the host.
// This keeps a page on another site from driving the unauthenticated daemon,
// and keeps file access local even when the daemon binds a wider address.
func allowFilePeer(r *http.Request) bool {
	if !isLoopbackRemote(r.RemoteAddr) {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

// filesSession resolves the session and its file backend after enforcing the
// peer boundary. Authentication runs before the session lookup so a
// non-loopback caller cannot probe which session IDs exist.
func (h *Handler) filesSession(w http.ResponseWriter, r *http.Request) (*session.Session, files.Backend, bool) {
	if !allowFilePeer(r) {
		writeError(w, http.StatusForbidden, "file transfer is only available from the local host")
		return nil, nil, false
	}
	s, ok := h.sessionFromRequest(w, r)
	if !ok {
		return nil, nil, false
	}
	backend, err := s.Files()
	if err != nil {
		if errors.Is(err, session.ErrFilesNotRunning) {
			writeError(w, http.StatusConflict, "file transfer requires a running session")
			return nil, nil, false
		}
		writeError(w, http.StatusBadGateway, "open file transfer: %v", err)
		return nil, nil, false
	}
	return s, backend, true
}

// writeFilesError maps backend sentinel errors onto HTTP status codes. The
// upload direction treats a directory target as a conflict; downloads treat it
// as a bad request.
func writeFilesError(w http.ResponseWriter, err error, uploading bool) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, files.ErrNotExist):
		status = http.StatusNotFound
	case errors.Is(err, files.ErrPermission), errors.Is(err, files.ErrDenied):
		status = http.StatusForbidden
	case errors.Is(err, files.ErrExists), errors.Is(err, files.ErrNotEmpty):
		status = http.StatusConflict
	case errors.Is(err, files.ErrIsDir):
		status = http.StatusBadRequest
		if uploading {
			status = http.StatusConflict
		}
	case errors.Is(err, files.ErrNotDir),
		errors.Is(err, files.ErrSymlink),
		errors.Is(err, files.ErrSpecial),
		errors.Is(err, files.ErrInvalidPath),
		errors.Is(err, files.ErrRootDelete):
		status = http.StatusBadRequest
	case errors.Is(err, files.ErrTransport):
		status = http.StatusBadGateway
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status = http.StatusBadRequest
	}
	writeError(w, status, "%v", err)
}

// handleFilesList returns a directory listing, or a single entry when path
// names a file or symlink.
func (h *Handler) handleFilesList(w http.ResponseWriter, r *http.Request) {
	s, backend, ok := h.filesSession(w, r)
	if !ok {
		return
	}
	resp, err := backend.List(r.URL.Query().Get("path"))
	if err != nil {
		writeFilesError(w, err, false)
		return
	}
	slog.Debug("file transfer", "session_id", s.ID, "op", "list", "entries", len(resp.Entries))
	writeJSON(w, http.StatusOK, resp)
}

// handleFilesMkdir creates one directory below the session root.
func (h *Handler) handleFilesMkdir(w http.ResponseWriter, r *http.Request) {
	s, backend, ok := h.filesSession(w, r)
	if !ok {
		return
	}
	var req api.MkdirRequest
	if !decodeBody(w, r, &req) {
		return
	}
	target := strings.TrimSpace(req.Path)
	if target == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	entry, err := backend.Mkdir(target)
	if err != nil {
		writeFilesError(w, err, false)
		return
	}
	slog.Debug("file transfer", "session_id", s.ID, "op", "mkdir")
	writeJSON(w, http.StatusCreated, api.FileResponse{File: entry})
}

// handleFilesDelete removes a file, a symlink, or an empty directory.
func (h *Handler) handleFilesDelete(w http.ResponseWriter, r *http.Request) {
	s, backend, ok := h.filesSession(w, r)
	if !ok {
		return
	}
	target := strings.TrimSpace(r.URL.Query().Get("path"))
	if target == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	if err := backend.Remove(target); err != nil {
		writeFilesError(w, err, false)
		return
	}
	slog.Debug("file transfer", "session_id", s.ID, "op", "remove")
	w.WriteHeader(http.StatusNoContent)
}

// handleFileDownload streams a file as an attachment. Bytes are forwarded as
// they arrive, so memory use does not grow with the file size.
func (h *Handler) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	s, backend, ok := h.filesSession(w, r)
	if !ok {
		return
	}
	target := strings.TrimSpace(r.URL.Query().Get("path"))
	if target == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	body, entry, err := backend.OpenRead(target)
	if err != nil {
		writeFilesError(w, err, false)
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": entry.Name})
	if disposition == "" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	written, copyErr := io.Copy(w, body)
	if copyErr != nil {
		// Headers are already sent; the client sees a truncated body.
		slog.Debug("file transfer", "session_id", s.ID, "op", "download", "bytes", written, "error", copyErr)
		return
	}
	slog.Debug("file transfer", "session_id", s.ID, "op", "download", "bytes", written)
}

// handleFileUpload streams the request body into a file. The body is never
// buffered; the 1 MiB JSON cap does not apply to this endpoint.
func (h *Handler) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	s, backend, ok := h.filesSession(w, r)
	if !ok {
		return
	}
	target := strings.TrimSpace(r.URL.Query().Get("path"))
	if target == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	overwrite := true
	if raw := r.URL.Query().Get("overwrite"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid overwrite value %q", raw)
			return
		}
		overwrite = parsed
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/octet-stream") {
		writeError(w, http.StatusUnsupportedMediaType, "upload body must be application/octet-stream")
		return
	}
	entry, err := backend.Upload(r.Context(), target, r.Body, overwrite)
	if err != nil {
		writeFilesError(w, err, true)
		return
	}
	slog.Debug("file transfer", "session_id", s.ID, "op", "upload", "bytes", entry.Size)
	writeJSON(w, http.StatusOK, api.FileResponse{File: entry})
}
