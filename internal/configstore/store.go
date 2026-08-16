// Package configstore manages ezterm launch configurations for both local
// and SSH sessions, persisted as single JSON map files in the data directory.
package configstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"ezterm/internal/api"
	"ezterm/internal/sshconfig"
	"ezterm/internal/storage"
)

// Type identifies whether a config launches a local process or a remote SSH
// session. Names are unique across both types.
type Type string

const (
	TypeLocal Type = "local"
	TypeSSH   Type = "ssh"
)

// LocalConfig is the persisted definition of a local session launch.
type LocalConfig struct {
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Mode    api.Mode `json:"mode,omitempty"`
}

var (
	// ErrNotFound indicates that a config does not exist.
	ErrNotFound = errors.New("configstore: config not found")
	// ErrInvalid indicates that a config failed validation.
	ErrInvalid = errors.New("configstore: invalid config")
	// ErrExists indicates that a name is already used by the other type.
	ErrExists = errors.New("configstore: config name already in use")
)

var validNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Store manages local and SSH configs under <dataDir>/configs/local.json and
// <dataDir>/configs/ssh.json. Both files are maps from config name to the
// config object, atomically rewritten on every change.
type Store struct {
	dataDir string
	mu      sync.RWMutex
}

// NewStore creates a config store rooted at the daemon data directory.
func NewStore(dataDir string) *Store {
	return &Store{dataDir: dataDir}
}

func (s *Store) localPath() string {
	return filepath.Join(s.dataDir, "configs", "local.json")
}

func (s *Store) sshPath() string {
	return filepath.Join(s.dataDir, "configs", "ssh.json")
}

// Validate checks a local config's fields, applying defaults.
func (c *LocalConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("%w: nil local config", ErrInvalid)
	}
	if c.Mode == "" {
		c.Mode = api.ModePTY
	}
	if c.Mode != api.ModePTY && c.Mode != api.ModePipe {
		return fmt.Errorf("%w: mode must be 'pty' or 'pipe', got %q", ErrInvalid, c.Mode)
	}
	return nil
}

func validateName(name string) error {
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("%w: invalid config name %q", ErrInvalid, name)
	}
	return nil
}

func loadMap(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read config file: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: %s: %v", storage.ErrCorrupt, path, err)
	}
	return nil
}

func saveMap(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return storage.WriteFileAtomic(path, data, 0o600)
}

func (s *Store) loadLocal() (map[string]*LocalConfig, error) {
	m := map[string]*LocalConfig{}
	if err := loadMap(s.localPath(), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Store) loadSSH() (map[string]*sshconfig.Profile, error) {
	m := map[string]*sshconfig.Profile{}
	if err := loadMap(s.sshPath(), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// SaveLocal validates and writes a local config. Re-saving an existing name of
// the same type overwrites it; names already used by the SSH type are rejected.
func (s *Store) SaveLocal(name string, cfg *LocalConfig) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ssh, err := s.loadSSH()
	if err != nil {
		return err
	}
	if _, ok := ssh[name]; ok {
		return fmt.Errorf("%w: %q is already an SSH config", ErrExists, name)
	}
	local, err := s.loadLocal()
	if err != nil {
		return err
	}
	local[name] = cfg
	return saveMap(s.localPath(), local)
}

// SaveSSH validates and writes an SSH config. Re-saving an existing name of
// the same type overwrites it; names already used by the local type are rejected.
func (s *Store) SaveSSH(name string, profile *sshconfig.Profile) error {
	if err := validateName(name); err != nil {
		return err
	}
	if profile == nil {
		return fmt.Errorf("%w: nil profile", ErrInvalid)
	}
	cp := *profile
	if err := cp.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	local, err := s.loadLocal()
	if err != nil {
		return err
	}
	if _, ok := local[name]; ok {
		return fmt.Errorf("%w: %q is already a local config", ErrExists, name)
	}
	ssh, err := s.loadSSH()
	if err != nil {
		return err
	}
	ssh[name] = &cp
	return saveMap(s.sshPath(), ssh)
}

// GetLocal loads a local config by name.
func (s *Store) GetLocal(name string) (*LocalConfig, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	local, err := s.loadLocal()
	if err != nil {
		return nil, err
	}
	cfg, ok := local[name]
	if !ok || cfg == nil {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// GetSSH loads an SSH profile by name.
func (s *Store) GetSSH(name string) (*sshconfig.Profile, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ssh, err := s.loadSSH()
	if err != nil {
		return nil, err
	}
	profile, ok := ssh[name]
	if !ok || profile == nil {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	cp := *profile
	if err := cp.Validate(); err != nil {
		return nil, err
	}
	return &cp, nil
}

// Delete removes a config by name from whichever type holds it.
func (s *Store) Delete(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	local, err := s.loadLocal()
	if err != nil {
		return err
	}
	if _, ok := local[name]; ok {
		delete(local, name)
		return saveMap(s.localPath(), local)
	}
	ssh, err := s.loadSSH()
	if err != nil {
		return err
	}
	if _, ok := ssh[name]; ok {
		delete(ssh, name)
		return saveMap(s.sshPath(), ssh)
	}
	return fmt.Errorf("%w: %q", ErrNotFound, name)
}

// Resolved describes the config selected for launch.
type Resolved struct {
	Type    Type
	Local   *LocalConfig
	Profile *sshconfig.Profile
}

// Resolve looks up a config by name across both types. Names are globally
// unique, so the lookup is unambiguous.
func (s *Store) Resolve(name string) (*Resolved, error) {
	local, err := s.GetLocal(name)
	if err == nil {
		return &Resolved{Type: TypeLocal, Local: local}, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	profile, err := s.GetSSH(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	return &Resolved{Type: TypeSSH, Profile: profile}, nil
}

// ListAll returns non-secret summaries of every config, ordered by name.
func (s *Store) ListAll() ([]api.ConfigSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]api.ConfigSummary, 0)
	local, err := s.loadLocal()
	if err != nil {
		return nil, err
	}
	for name, cfg := range local {
		if cfg == nil {
			continue
		}
		result = append(result, api.ConfigSummary{
			Name:    name,
			Type:    string(TypeLocal),
			Command: cfg.Command,
			Mode:    string(cfg.Mode),
		})
	}
	ssh, err := s.loadSSH()
	if err != nil {
		return nil, err
	}
	for name, p := range ssh {
		if p == nil {
			continue
		}
		cp := *p
		if err := cp.Validate(); err != nil {
			return nil, fmt.Errorf("validate SSH config %q: %w", name, err)
		}
		result = append(result, api.ConfigSummary{
			Name:         name,
			Type:         string(TypeSSH),
			Host:         cp.Host,
			Port:         cp.Port,
			User:         cp.User,
			AuthMethod:   string(cp.AuthMethod),
			DefaultShell: cp.DefaultShell,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
