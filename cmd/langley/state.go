package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
// Alongside the primary state.json file, it maintains per-PID state.<pid>.json
// records so that when multiple Langley instances overlap, one instance's
// Write/Delete does not erase a surviving instance's discoverable state.
type FileStateStore struct {
	path    string
	lastPID int
}

// NewFileStateStore creates a state store at the default location.
func NewFileStateStore() (*FileStateStore, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return nil, err
	}
	return NewFileStateStoreWithPath(filepath.Join(dir, "state.json")), nil
}

// NewFileStateStoreWithPath creates a state store at a specific state.json path.
func NewFileStateStoreWithPath(path string) *FileStateStore {
	return &FileStateStore{path: path}
}

func (s *FileStateStore) pidPath(pid int) string {
	return filepath.Join(filepath.Dir(s.path), fmt.Sprintf("state.%d.json", pid))
}

func readStateFile(path string) (*ServerState, error) {
	data, err := os.ReadFile(path)
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

func writeStateFileAtomic(path string, state ServerState) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Rename(tmpPath, path)
}

// survivingStates returns all live ServerState records in the state directory
// (excluding excludePID), pruning any stale per-PID state files for dead PIDs.
func (s *FileStateStore) survivingStates(excludePID int) []ServerState {
	dir := filepath.Dir(s.path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	byPID := make(map[int]ServerState)
	base := filepath.Base(s.path)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == base || !strings.HasPrefix(name, "state.") || !strings.HasSuffix(name, ".json") {
			continue
		}
		fullPath := filepath.Join(dir, name)
		st, readErr := readStateFile(fullPath)
		if readErr != nil || st == nil || st.PID <= 0 || !isProcessAlive(st.PID) {
			_ = os.Remove(fullPath)
			continue
		}
		if st.PID == excludePID {
			continue
		}
		byPID[st.PID] = *st
	}
	if cur, readErr := readStateFile(s.path); readErr == nil && cur != nil && cur.PID > 0 && cur.PID != excludePID && isProcessAlive(cur.PID) {
		if _, ok := byPID[cur.PID]; !ok {
			byPID[cur.PID] = *cur
		}
	}
	out := make([]ServerState, 0, len(byPID))
	for _, st := range byPID {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].PID > out[j].PID
	})
	return out
}

// ReadAll returns all currently live ServerState records known to the store.
func (s *FileStateStore) ReadAll() ([]ServerState, error) {
	states := s.survivingStates(0)
	if len(states) > 0 {
		return states, nil
	}
	st, err := readStateFile(s.path)
	if err != nil {
		return nil, err
	}
	return []ServerState{*st}, nil
}

// Read reads server state from the state file, recovering from surviving per-PID
// state records if the primary state.json was overwritten or deleted by another instance.
func (s *FileStateStore) Read() (*ServerState, error) {
	state, err := readStateFile(s.path)
	if err == nil && state != nil {
		if state.PID > 0 && isProcessAlive(state.PID) {
			return state, nil
		}
		if surviving := s.survivingStates(state.PID); len(surviving) > 0 {
			_ = writeStateFileAtomic(s.path, surviving[0])
			return &surviving[0], nil
		}
		return state, nil
	}
	if errors.Is(err, ErrServerNotRunning) {
		if surviving := s.survivingStates(0); len(surviving) > 0 {
			_ = writeStateFileAtomic(s.path, surviving[0])
			return &surviving[0], nil
		}
	}
	return nil, err
}

// Write writes server state to the state file atomically and records a per-PID state file.
func (s *FileStateStore) Write(state ServerState) error {
	s.lastPID = state.PID
	if state.PID > 0 {
		if err := writeStateFileAtomic(s.pidPath(state.PID), state); err != nil {
			return err
		}
	}
	return writeStateFileAtomic(s.path, state)
}

// Delete removes the state record for the instance written by this store (or the
// current state.json owner) and restores state.json to any surviving live instance.
func (s *FileStateStore) Delete() error {
	targetPID := s.lastPID
	if targetPID <= 0 {
		if cur, err := readStateFile(s.path); err == nil && cur != nil {
			targetPID = cur.PID
		}
	}
	s.lastPID = 0
	if targetPID > 0 {
		_ = os.Remove(s.pidPath(targetPID))
	}
	if surviving := s.survivingStates(targetPID); len(surviving) > 0 {
		return writeStateFileAtomic(s.path, surviving[0])
	}
	err := os.Remove(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
