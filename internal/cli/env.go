package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ezterm/internal/config"
)

// defaultDataDir returns ~/.ezterm resolved for the current user.
func defaultDataDir() (string, error) {
	return config.DefaultDataDir()
}

// expandTilde expands a leading ~ to the user's home directory.
func expandTilde(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return defaultDataDir()
	}
	if path == "~" {
		return defaultDataDir()
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand path: %w", err)
		}
		path = filepath.Join(home, path[2:])
	}
	return filepath.Clean(path), nil
}
