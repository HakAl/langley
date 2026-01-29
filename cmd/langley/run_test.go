package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- Test doubles ---

type mockStateReader struct {
	state *ServerState
	err   error
}

func (m *mockStateReader) Read() (*ServerState, error) {
	return m.state, m.err
}

type mockHealthChecker struct {
	err error
}

func (m *mockHealthChecker) Check(ctx context.Context, addr string) error {
	return m.err
}

type mockEnvBuilder struct {
	env []string
}

func (m *mockEnvBuilder) Build(proxyAddr, caPath string) []string {
	return m.env
}

type mockFileChecker struct {
	exists bool
}

func (m *mockFileChecker) Exists(path string) bool {
	return m.exists
}

type mockProcessRunner struct {
	exitCode int
}

func (m *mockProcessRunner) Run(ctx context.Context, cmd string, args []string, env []string) int {
	return m.exitCode
}

// --- RunCommand tests ---

func TestRunCommand_NoArgs(t *testing.T) {
	var stderr bytes.Buffer
	cmd := &RunCommand{stderr: &stderr}

	code := cmd.Execute(context.Background(), []string{})

	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("Usage:")) {
		t.Error("expected usage message in stderr")
	}
}

func TestRunCommand_ServerNotRunning(t *testing.T) {
	var stderr bytes.Buffer
	cmd := &RunCommand{
		stateReader: &mockStateReader{err: ErrServerNotRunning},
		stderr:      &stderr,
	}

	code := cmd.Execute(context.Background(), []string{"echo"})

	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("not running")) {
		t.Error("expected 'not running' message in stderr")
	}
}

func TestRunCommand_StateReadError(t *testing.T) {
	var stderr bytes.Buffer
	cmd := &RunCommand{
		stateReader: &mockStateReader{err: errors.New("disk failure")},
		stderr:      &stderr,
	}

	code := cmd.Execute(context.Background(), []string{"echo"})

	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("disk failure")) {
		t.Error("expected error message in stderr")
	}
}

func TestRunCommand_HealthCheckFails(t *testing.T) {
	var stderr bytes.Buffer
	cmd := &RunCommand{
		stateReader:   &mockStateReader{state: &ServerState{APIAddr: "localhost:8080"}},
		healthChecker: &mockHealthChecker{err: errors.New("connection refused")},
		stderr:        &stderr,
	}

	code := cmd.Execute(context.Background(), []string{"echo"})

	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("not responding")) {
		t.Error("expected 'not responding' message")
	}
}

func TestRunCommand_CAMissing(t *testing.T) {
	var stderr bytes.Buffer
	cmd := &RunCommand{
		stateReader:   &mockStateReader{state: &ServerState{CAPath: "/missing/ca.crt"}},
		healthChecker: &mockHealthChecker{err: nil},
		fileChecker:   &mockFileChecker{exists: false},
		stderr:        &stderr,
	}

	code := cmd.Execute(context.Background(), []string{"echo"})

	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("CA certificate not found")) {
		t.Error("expected CA error message")
	}
}

func TestRunCommand_Success(t *testing.T) {
	var stderr bytes.Buffer
	cmd := &RunCommand{
		stateReader:   &mockStateReader{state: &ServerState{ProxyAddr: "localhost:9090", CAPath: "/ca.crt"}},
		healthChecker: &mockHealthChecker{err: nil},
		fileChecker:   &mockFileChecker{exists: true},
		envBuilder:    &mockEnvBuilder{env: []string{"HTTPS_PROXY=http://localhost:9090"}},
		processRunner: &mockProcessRunner{exitCode: 0},
		stderr:        &stderr,
	}

	code := cmd.Execute(context.Background(), []string{"echo", "hello"})

	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
}

func TestRunCommand_PropagatesExitCode(t *testing.T) {
	cmd := &RunCommand{
		stateReader:   &mockStateReader{state: &ServerState{}},
		healthChecker: &mockHealthChecker{err: nil},
		fileChecker:   &mockFileChecker{exists: true},
		envBuilder:    &mockEnvBuilder{},
		processRunner: &mockProcessRunner{exitCode: 42},
		stderr:        &bytes.Buffer{},
	}

	code := cmd.Execute(context.Background(), []string{"exit", "42"})

	if code != 42 {
		t.Errorf("expected exit code 42, got %d", code)
	}
}

// --- EnvBuilder tests ---

func TestProxyEnvBuilder_Deduplication(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://old-proxy:8080")

	builder := &ProxyEnvBuilder{}
	env := builder.Build("localhost:9090", "/path/to/ca.crt")

	count := 0
	var value string
	for _, e := range env {
		if key, val, _ := strings.Cut(e, "="); key == "HTTPS_PROXY" {
			count++
			value = val
		}
	}

	if count != 1 {
		t.Errorf("expected exactly 1 HTTPS_PROXY, got %d", count)
	}
	if value != "http://localhost:9090" {
		t.Errorf("expected new proxy value, got %s", value)
	}
}

func TestProxyEnvBuilder_BothCases(t *testing.T) {
	builder := &ProxyEnvBuilder{}
	env := builder.Build("localhost:9090", "/path/to/ca.crt")

	hasUpper := false
	hasLower := false
	for _, e := range env {
		if strings.HasPrefix(e, "HTTPS_PROXY=") {
			hasUpper = true
		}
		if strings.HasPrefix(e, "https_proxy=") {
			hasLower = true
		}
	}

	if !hasUpper || !hasLower {
		t.Error("expected both HTTPS_PROXY and https_proxy to be set")
	}
}

func TestProxyEnvBuilder_AllVarsPresent(t *testing.T) {
	builder := &ProxyEnvBuilder{}
	env := builder.Build("localhost:9090", "/path/to/ca.crt")

	envMap := make(map[string]string)
	for _, e := range env {
		key, val, _ := strings.Cut(e, "=")
		envMap[key] = val
	}

	expected := map[string]string{
		"HTTPS_PROXY":         "http://localhost:9090",
		"https_proxy":         "http://localhost:9090",
		"HTTP_PROXY":          "http://localhost:9090",
		"http_proxy":          "http://localhost:9090",
		"NODE_EXTRA_CA_CERTS": "/path/to/ca.crt",
		"SSL_CERT_FILE":       "/path/to/ca.crt",
		"REQUESTS_CA_BUNDLE":  "/path/to/ca.crt",
	}

	for key, want := range expected {
		got, ok := envMap[key]
		if !ok {
			t.Errorf("missing env var %s", key)
			continue
		}
		if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestProxyEnvBuilder_PreservesOtherVars(t *testing.T) {
	t.Setenv("MY_CUSTOM_VAR", "keep-me")

	builder := &ProxyEnvBuilder{}
	env := builder.Build("localhost:9090", "/ca.crt")

	found := false
	for _, e := range env {
		if e == "MY_CUSTOM_VAR=keep-me" {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected MY_CUSTOM_VAR to be preserved in environment")
	}
}

// --- FileStateStore tests ---

func TestFileStateStore_ReadMissingFile(t *testing.T) {
	store := &FileStateStore{path: filepath.Join(t.TempDir(), "nonexistent.json")}

	_, err := store.Read()

	if !errors.Is(err, ErrServerNotRunning) {
		t.Errorf("expected ErrServerNotRunning, got %v", err)
	}
}

func TestFileStateStore_ReadMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	os.WriteFile(path, []byte("not json"), 0600)

	store := &FileStateStore{path: path}
	_, err := store.Read()

	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "corrupted") {
		t.Errorf("expected 'corrupted' in error, got %v", err)
	}
}

func TestFileStateStore_ReadMissingFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	data, _ := json.Marshal(ServerState{ProxyAddr: "localhost:9090"}) // missing api_addr
	os.WriteFile(path, data, 0600)

	store := &FileStateStore{path: path}
	_, err := store.Read()

	if err == nil {
		t.Fatal("expected error for missing fields")
	}
	if !strings.Contains(err.Error(), "corrupted") {
		t.Errorf("expected 'corrupted' in error, got %v", err)
	}
}

func TestFileStateStore_ReadValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	now := time.Now().Truncate(time.Second)
	state := ServerState{
		ProxyAddr: "localhost:9090",
		APIAddr:   "localhost:9091",
		CAPath:    "/path/to/ca.crt",
		PID:       1234,
		StartedAt: now,
	}
	data, _ := json.Marshal(state)
	os.WriteFile(path, data, 0600)

	store := &FileStateStore{path: path}
	got, err := store.Read()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ProxyAddr != state.ProxyAddr {
		t.Errorf("ProxyAddr = %q, want %q", got.ProxyAddr, state.ProxyAddr)
	}
	if got.APIAddr != state.APIAddr {
		t.Errorf("APIAddr = %q, want %q", got.APIAddr, state.APIAddr)
	}
	if got.PID != state.PID {
		t.Errorf("PID = %d, want %d", got.PID, state.PID)
	}
}

func TestFileStateStore_WriteAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	store := &FileStateStore{path: path}
	state := ServerState{
		ProxyAddr: "localhost:9090",
		APIAddr:   "localhost:9091",
		CAPath:    "/ca.crt",
		PID:       5678,
		StartedAt: time.Now().Truncate(time.Second),
	}

	if err := store.Write(state); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	got, err := store.Read()
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if got.ProxyAddr != state.ProxyAddr || got.APIAddr != state.APIAddr {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}

func TestFileStateStore_WriteCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subdir", "nested")
	path := filepath.Join(dir, "state.json")

	store := &FileStateStore{path: path}
	err := store.Write(ServerState{
		ProxyAddr: "localhost:9090",
		APIAddr:   "localhost:9091",
	})

	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("expected state file to exist after write")
	}
}

func TestFileStateStore_Delete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	os.WriteFile(path, []byte("{}"), 0600)

	store := &FileStateStore{path: path}
	if err := store.Delete(); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected state file to be deleted")
	}
}

func TestFileStateStore_DeleteNonexistent(t *testing.T) {
	store := &FileStateStore{path: filepath.Join(t.TempDir(), "nonexistent.json")}

	err := store.Delete()

	if err != nil {
		t.Errorf("Delete of nonexistent file should return nil, got %v", err)
	}
}

// --- getExitCode tests ---

func TestGetExitCode_NilError(t *testing.T) {
	if code := getExitCode(nil); code != 0 {
		t.Errorf("expected 0, got %d", code)
	}
}

func TestGetExitCode_GenericError(t *testing.T) {
	if code := getExitCode(errors.New("something")); code != 1 {
		t.Errorf("expected 1, got %d", code)
	}
}
