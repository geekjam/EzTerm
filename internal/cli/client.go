package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"ezterm/internal/api"
)

var (
	errSessionNotFound = errors.New("session not found")
	errConflict        = errors.New("conflict")
)

// client talks to the ezterm daemon over HTTP.
type client struct {
	baseURL    string
	httpClient *http.Client
}

func newClient(port int) *client {
	return &client{
		baseURL:    fmt.Sprintf("http://127.0.0.1:%d", port),
		httpClient: &http.Client{},
	}
}

// checkHealth probes GET /health without following the normal error mapping.
func (c *client) checkHealth() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// do performs a request and decodes the response into out, mapping HTTP errors.
func (c *client) do(ctx context.Context, method, path string, body, out any, timeout time.Duration) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return fmt.Errorf("read response: %w", readErr)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope api.ErrorResponse
		if json.Unmarshal(raw, &envelope) == nil && envelope.Error != "" {
			return classifyError(resp.StatusCode, envelope.Error)
		}
		return classifyError(resp.StatusCode, fmt.Sprintf("HTTP %d", resp.StatusCode))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func classifyError(status int, message string) error {
	switch status {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", errSessionNotFound, message)
	case http.StatusConflict:
		return fmt.Errorf("%w: %s", errConflict, message)
	default:
		return errors.New(message)
	}
}

func (c *client) create(req api.CreateSessionRequest, timeout time.Duration) (api.Session, error) {
	var out api.Session
	if err := c.do(context.Background(), http.MethodPost, "/api/sessions", req, &out, timeout); err != nil {
		return api.Session{}, err
	}
	return out, nil
}

func (c *client) send(id, text string, pressEnter bool) error {
	return c.do(context.Background(), http.MethodPost, "/api/sessions/"+id+"/input",
		api.InputRequest{Text: text, PressEnter: pressEnter}, nil, 15*time.Second)
}

func (c *client) read(id string, readerID int, timeout time.Duration, raw bool, maxBytes int) (api.OutputResponse, error) {
	path := fmt.Sprintf("/api/sessions/%s/output?reader_id=%d&timeout=%.1f&raw=%t&max_bytes=%d",
		id, readerID, timeout.Seconds(), raw, maxBytes)
	var out api.OutputResponse
	clientTimeout := timeout + 15*time.Second
	if err := c.do(context.Background(), http.MethodGet, path, nil, &out, clientTimeout); err != nil {
		return api.OutputResponse{}, err
	}
	return out, nil
}

func (c *client) terminate(id string) (api.Session, error) {
	var out api.TerminateResponse
	if err := c.do(context.Background(), http.MethodPost, "/api/sessions/"+id+"/terminate?grace=5", nil, &out, 20*time.Second); err != nil {
		return api.Session{}, err
	}
	return out.Session, nil
}

func (c *client) delete(id string) error {
	return c.do(context.Background(), http.MethodDelete, "/api/sessions/"+id, nil, nil, 15*time.Second)
}

func (c *client) list() ([]api.Session, error) {
	var out struct {
		Sessions []api.Session `json:"sessions"`
	}
	if err := c.do(context.Background(), http.MethodGet, "/api/sessions", nil, &out, 15*time.Second); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

func (c *client) newReader(id string) (int, error) {
	var out api.ReaderResponse
	if err := c.do(context.Background(), http.MethodPost, "/api/sessions/"+id+"/readers", nil, &out, 15*time.Second); err != nil {
		return 0, err
	}
	return out.ReaderID, nil
}

// getSession fetches one session's metadata; used by attach to resolve the
// session before entering raw mode so error precedence is deterministic.
func (c *client) getSession(id string) (api.Session, error) {
	var out api.Session
	if err := c.do(context.Background(), http.MethodGet, "/api/sessions/"+id, nil, &out, 15*time.Second); err != nil {
		return api.Session{}, err
	}
	return out, nil
}

// attachStream opens the raw attach stream for a session. The returned body
// yields raw PTY bytes and ends with EOF when the session exits; the caller
// must close it. The shared http.Client has no overall timeout so the stream
// can stay open for the whole attach session.
func (c *client) attachStream(ctx context.Context, id string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/sessions/"+id+"/attach", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request attach: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var envelope api.ErrorResponse
		if json.Unmarshal(raw, &envelope) == nil && envelope.Error != "" {
			return nil, classifyError(resp.StatusCode, envelope.Error)
		}
		return nil, classifyError(resp.StatusCode, fmt.Sprintf("HTTP %d", resp.StatusCode))
	}
	return resp.Body, nil
}

// sendRawInput forwards raw bytes to the session without appending a newline.
func (c *client) sendRawInput(id string, data []byte) error {
	return c.do(context.Background(), http.MethodPost, "/api/sessions/"+id+"/input",
		api.InputRequest{Text: string(data), PressEnter: false}, nil, 15*time.Second)
}

// resize updates the PTY dimensions of a session.
func (c *client) resize(id string, rows, cols int) error {
	var out api.Session
	return c.do(context.Background(), http.MethodPost, "/api/sessions/"+id+"/resize",
		resizeRequest{Rows: rows, Cols: cols}, &out, 15*time.Second)
}

// resizeRequest is the JSON body of POST /api/sessions/{id}/resize.
type resizeRequest struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

// parseIDArg extracts a session ID from the first positional argument.
func parseIDArg(args []string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("missing session ID")
	}
	id := args[0]
	if len(id) < 1 || len(id) > 64 {
		return "", fmt.Errorf("invalid session ID %q", id)
	}
	return id, nil
}

// atoiFlag is a convenience for flags whose value must be a positive int.
func atoiFlag(value string, def int) int {
	if value == "" {
		return def
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return def
	}
	return n
}
