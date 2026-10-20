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
	StatusStarting Status = "starting"
	StatusRunning  Status = "running"
	// StatusAwaitingPassword marks a remote session that was created without a
	// stored password. It dials only after the user submits the secret from the
	// Web terminal page.
	StatusAwaitingPassword Status = "awaiting_password"
	StatusExited           Status = "exited"
	StatusTerminated       Status = "terminated"
)

// Prompt kinds reported in Session.PendingPrompt.
const (
	// PromptKindSSHPassword asks for the SSH login password before dialing.
	PromptKindSSHPassword = "ssh_password"
	// PromptKindTerminalPassword forwards a password prompt seen in the
	// session's terminal output (sudo, su, passwd, ssh, ...).
	PromptKindTerminalPassword = "terminal_password"
)

// Prompt describes a password prompt the Web terminal page should display. It
// never contains a secret: Label is the human-readable prompt text.
type Prompt struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// Session is the JSON representation of a managed process session.
type Session struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Command       string     `json:"command"`
	Args          []string   `json:"args"`
	Mode          Mode       `json:"mode"`
	Status        Status     `json:"status"`
	PID           int        `json:"pid"`
	ExitCode      int        `json:"exit_code"`
	Rows          int        `json:"rows"`
	Cols          int        `json:"cols"`
	SSHConfig     string     `json:"ssh_config"`
	WebURL        string     `json:"web_url"` // Web terminal URL; empty when not enabled
	PendingPrompt *Prompt    `json:"pending_prompt,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	FinishedAt    *time.Time `json:"finished_at"`
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
//
// Web is a pointer so an omitted field can default to enabled (the Web terminal
// is on unless it is set to false); see WebRequested.
type CreateSessionRequest struct {
	Command            string   `json:"command"`
	Args               []string `json:"args"`
	Mode               Mode     `json:"mode"`
	Name               string   `json:"name"`
	Rows               int      `json:"rows"`
	Cols               int      `json:"cols"`
	SSHConfig          string   `json:"ssh_config"`
	Web                *bool    `json:"web,omitempty"`
	DialTimeoutSeconds int      `json:"dial_timeout_seconds"`
}

// WebRequested reports whether the Web terminal is wanted and whether the
// caller set the field explicitly. An omitted field defaults to enabled; pipe
// sessions silently drop an omitted value but reject an explicit true.
func (r CreateSessionRequest) WebRequested() (enabled, explicit bool) {
	if r.Web == nil {
		return true, false
	}
	return *r.Web, true
}

// BoolPtr returns a pointer to b; it builds optional wire fields such as
// CreateSessionRequest.Web.
func BoolPtr(b bool) *bool { return &b }

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

// ConfigSummary is the non-secret representation of a launch config.
type ConfigSummary struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Command      string `json:"command,omitempty"`
	Mode         string `json:"mode,omitempty"`
	Host         string `json:"host,omitempty"`
	Port         int    `json:"port,omitempty"`
	User         string `json:"user,omitempty"`
	AuthMethod   string `json:"auth_method,omitempty"`
	DefaultShell string `json:"default_shell,omitempty"`
}

// ConfigDetail is the full, non-secret representation of a single launch
// config, including fields the list summary omits (such as a local config's
// args). The stored SSH password is never included in this wire type:
// HasPassword only reports whether one is stored, and PasswordPrompt reports
// that the password is requested in the Web terminal on every start.
type ConfigDetail struct {
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	Command        string   `json:"command,omitempty"`
	Args           []string `json:"args,omitempty"`
	Mode           string   `json:"mode,omitempty"`
	Host           string   `json:"host,omitempty"`
	Port           int      `json:"port,omitempty"`
	User           string   `json:"user,omitempty"`
	AuthMethod     string   `json:"auth_method,omitempty"`
	KeyPath        string   `json:"key_path,omitempty"`
	Shell          string   `json:"shell,omitempty"`
	HasPassword    bool     `json:"has_password"`
	PasswordPrompt bool     `json:"password_prompt"`
}

// ConfigUpsertRequest is the request body for POST /api/configs/{name}.
// Type discriminates a local from an ssh config. Password is write-only: an
// empty password on an existing password-auth SSH config preserves the stored
// value unless PasswordPrompt is true, which clears it so the password is
// requested in the Web terminal on every start.
type ConfigUpsertRequest struct {
	Type           string   `json:"type"`
	Command        string   `json:"command,omitempty"`
	Args           []string `json:"args,omitempty"`
	Mode           string   `json:"mode,omitempty"`
	Host           string   `json:"host,omitempty"`
	Port           int      `json:"port,omitempty"`
	User           string   `json:"user,omitempty"`
	AuthMethod     string   `json:"auth_method,omitempty"`
	Password       string   `json:"password,omitempty"`
	KeyPath        string   `json:"key_path,omitempty"`
	Shell          string   `json:"shell,omitempty"`
	PasswordPrompt bool     `json:"password_prompt,omitempty"`
}
