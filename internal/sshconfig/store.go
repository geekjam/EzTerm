package sshconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"

	"ezterm/internal/api"
	"ezterm/internal/storage"
)

var validNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Store manages SSH profiles under <dataDir>/ssh_configs/<name>/config.json.
type Store struct {
	dataDir string
	mu      sync.RWMutex
}

// NewStore creates a profile store rooted at the daemon data directory.
func NewStore(dataDir string) *Store {
	return &Store{dataDir: filepath.Join(dataDir, "ssh_configs")}
}

func (s *Store) profileDir(name string) string {
	return filepath.Join(s.dataDir, name)
}

// ProfilePath returns the config file path for a profile.
func (s *Store) ProfilePath(name string) string {
	return filepath.Join(s.profileDir(name), "config.json")
}

func validateName(name string) error {
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("%w: invalid profile name %q", ErrInvalid, name)
	}
	return nil
}

// Init creates a profile skeleton with placeholder values for the user to edit.
func (s *Store) Init(name string) (*Profile, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.ProfilePath(name)
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("profile %q already exists", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat existing profile: %w", err)
	}

	profile := &Profile{
		Host:       "change-me.example.com",
		Port:       DefaultPort,
		User:       "change-me",
		AuthMethod: AuthPassword,
		Password:   "change-me",
	}
	if err := s.saveLocked(name, profile); err != nil {
		return nil, err
	}
	return profile, nil
}

// Save validates and atomically writes a profile.
func (s *Store) Save(name string, profile *Profile) error {
	if err := validateName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(name, profile)
}

func (s *Store) saveLocked(name string, profile *Profile) error {
	if profile == nil {
		return fmt.Errorf("%w: nil profile", ErrInvalid)
	}
	cp := *profile
	if err := cp.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return fmt.Errorf("encode profile: %w", err)
	}
	return storage.WriteFileAtomic(s.ProfilePath(name), data, 0o600)
}

// Get loads a profile by name.
func (s *Store) Get(name string) (*Profile, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := os.ReadFile(s.ProfilePath(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
		}
		return nil, fmt.Errorf("read profile: %w", err)
	}
	var profile Profile
	if err := json.Unmarshal(data, &profile); err != nil {
		return nil, fmt.Errorf("%w: %q: %v", storage.ErrCorrupt, name, err)
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	return &profile, nil
}

// List returns non-secret summaries of every profile.
func (s *Store) List() ([]api.SSHProfileSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []api.SSHProfileSummary{}, nil
		}
		return nil, fmt.Errorf("list profile directory: %w", err)
	}
	result := make([]api.SSHProfileSummary, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(s.dataDir, entry.Name(), "config.json"))
		if readErr != nil {
			continue
		}
		var profile Profile
		if json.Unmarshal(data, &profile) != nil {
			continue
		}
		result = append(result, api.SSHProfileSummary{
			Name:         entry.Name(),
			Host:         profile.Host,
			Port:         profile.Port,
			User:         profile.User,
			AuthMethod:   string(profile.AuthMethod),
			DefaultShell: profile.DefaultShell,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// Exists reports whether a profile is present.
func (s *Store) Exists(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, err := os.Stat(s.ProfilePath(name))
	return err == nil
}
