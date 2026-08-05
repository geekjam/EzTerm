package sshconfig

import (
	"errors"
	"fmt"
	"strings"
)

// AuthMethod identifies how a profile authenticates to the remote host.
type AuthMethod string

const (
	AuthPassword AuthMethod = "password"
	AuthKey      AuthMethod = "key"
)

// Profile is the JSON representation of one remote SSH profile.
type Profile struct {
	Host         string     `json:"host"`
	Port         int        `json:"port"`
	User         string     `json:"user"`
	AuthMethod   AuthMethod `json:"auth_method"`
	Password     string     `json:"password,omitempty"`
	KeyPath      string     `json:"key_path,omitempty"`
	DefaultShell string     `json:"default_shell,omitempty"`
}

var (
	// ErrNotFound indicates that a profile does not exist.
	ErrNotFound = errors.New("sshconfig: profile not found")
	// ErrInvalid indicates that a profile failed validation.
	ErrInvalid = errors.New("sshconfig: invalid profile")
)

// DefaultPort is the SSH port used when a profile omits one.
const DefaultPort = 22

// Validate checks a profile's required fields and value ranges.
func (p *Profile) Validate() error {
	if strings.TrimSpace(p.Host) == "" {
		return fmt.Errorf("%w: host is required", ErrInvalid)
	}
	if strings.TrimSpace(p.User) == "" {
		return fmt.Errorf("%w: user is required", ErrInvalid)
	}
	if p.Port == 0 {
		p.Port = DefaultPort
	}
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("%w: port must be between 1 and 65535, got %d", ErrInvalid, p.Port)
	}
	switch p.AuthMethod {
	case "":
		p.AuthMethod = AuthPassword
	case AuthPassword, AuthKey:
	default:
		return fmt.Errorf("%w: auth_method must be %q or %q, got %q", ErrInvalid, AuthPassword, AuthKey, p.AuthMethod)
	}
	if p.AuthMethod == AuthKey && strings.TrimSpace(p.KeyPath) == "" {
		return fmt.Errorf("%w: key_path is required for key auth", ErrInvalid)
	}
	return nil
}
