package api

import "time"

// Mode identifies how a session connects its standard streams.
type Mode string

const (
	ModePTY  Mode = "pty"
	ModePipe Mode = "pipe"
)

// Status identifies the lifecycle state of a session.
type Status string

const (
	StatusStarting   Status = "starting"
	StatusRunning    Status = "running"
	StatusExited     Status = "exited"
	StatusTerminated Status = "terminated"
)

// Session is the JSON representation of a managed process session.
type Session struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Command    string     `json:"command"`
	Args       []string   `json:"args"`
	Mode       Mode       `json:"mode"`
	Status     Status     `json:"status"`
	PID        int        `json:"pid"`
	ExitCode   int        `json:"exit_code"`
	Rows       int        `json:"rows"`
	Cols       int        `json:"cols"`
	SSHConfig  string     `json:"ssh_config"`
	WebURL     string     `json:"web_url"` // Web terminal URL; empty when not enabled
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// Message is a persisted input, output, or system event.
type Message struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// MessageIndexEntry stores the searchable metadata for one message file.
type MessageIndexEntry struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateSessionRequest is the request body for POST /api/sessions.
type CreateSessionRequest struct {
	Command            string   `json:"command"`
	Args               []string `json:"args"`
	Mode               Mode     `json:"mode"`
	Name               string   `json:"name"`
	Rows               int      `json:"rows"`
	Cols               int      `json:"cols"`
	SSHConfig          string   `json:"ssh_config"`
	Web                bool     `json:"web"` // enable the Web terminal at /web/<id>
	DialTimeoutSeconds int      `json:"dial_timeout_seconds"`
}

// InputRequest is the request body for a session input operation.
type InputRequest struct {
	Text       string `json:"text"`
	PressEnter bool   `json:"press_enter"`
}

// OutputResponse contains output read from a session buffer.
type OutputResponse struct {
	Data string `json:"data"`
	EOF  bool   `json:"eof"`
}

// ReaderResponse contains a newly allocated reader ID.
type ReaderResponse struct {
	ReaderID int `json:"reader_id"`
}

// TerminateResponse reports the session state after a terminate request.
type TerminateResponse struct {
	Session Session `json:"session"`
}

// HealthResponse is returned by GET /health.
type HealthResponse struct {
	Status string `json:"status"`
}

// ErrorResponse is the stable JSON error envelope used by the daemon.
type ErrorResponse struct {
	Error string `json:"error"`
}

// SSHProfileSummary is the non-secret representation of an SSH profile.
type SSHProfileSummary struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	User         string `json:"user"`
	AuthMethod   string `json:"auth_method"`
	DefaultShell string `json:"default_shell"`
}
