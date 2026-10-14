package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/HakAl/langley/internal/config"
)

// ServerState holds the running server's configuration.
type ServerState struct {
	ProxyAddr string    `json:"proxy_addr"`
	APIAddr   string    `json:"api_addr"`
	CAPath    string    `json:"ca_path"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// ErrServerNotRunning indicates no state file exists (server not started).
var ErrServerNotRunning = errors.New("server not running")

// FileStateStore implements StateReader and StateWriter using the filesystem.
type FileStateStore struct {
	path string
}

// NewFileStateStore creates a state store at the default location.
func NewFileStateStore() (*FileStateStore, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return nil, err
	}
	return &FileStateStore{
		path: filepath.Join(dir, "state.json"),
	}, nil
}

// Read reads server state from the state file.
func (s *FileStateStore) Read() (*ServerState, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrServerNotRunning
		}
		return nil, err
	}
	var state ServerState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("corrupted state file: %w", err)
	}
	if state.ProxyAddr == "" || state.APIAddr == "" {
		return nil, fmt.Errorf("corrupted state file: missing proxy_addr or api_addr")
	}
	return &state, nil
}

// Write writes server state to the state file atomically.
func (s *FileStateStore) Write(state ServerState) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	// Best-effort atomic write: temp file + rename.
	// On Windows, os.Rename fails if destination exists, so remove first.
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	os.Remove(s.path) // ignore error (may not exist yet)
	return os.Rename(tmpPath, s.path)
}

// Delete removes the state file.
func (s *FileStateStore) Delete() error {
	err := os.Remove(s.path)
	if os.IsNotExist(err) {
		return nil // Already gone, not an error
	}
	return err
}
