package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultHost = "127.0.0.1"
	DefaultPort = 18766
)

// Config contains the daemon connection and persistence settings.
type Config struct {
	Host     string
	Port     int
	DataDir  string
	LogLevel string
}

// DefaultDataDir returns the platform-specific ~/.ezterm path.
func DefaultDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find user home directory: %w", err)
	}
	return filepath.Join(home, ".ezterm"), nil
}

// New returns configuration populated with the documented defaults.
func New() (Config, error) {
	dataDir, err := DefaultDataDir()
	if err != nil {
		return Config{}, err
	}
	return Config{
		Host:     DefaultHost,
		Port:     DefaultPort,
		DataDir:  dataDir,
		LogLevel: "info",
	}, nil
}

// ExpandDataDir expands a leading ~ and cleans the supplied path.
func ExpandDataDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return DefaultDataDir()
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand data directory: %w", err)
		}
		return home, nil
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand data directory: %w", err)
		}
		path = filepath.Join(home, path[2:])
	}
	return filepath.Clean(path), nil
}

// Validate checks values that affect daemon binding.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("host must not be empty")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", c.Port)
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return fmt.Errorf("data directory must not be empty")
	}
	return nil
}
