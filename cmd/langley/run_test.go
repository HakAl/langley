package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
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
	if err := os.WriteFile(path, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}

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
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

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
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

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
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

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

func TestRunProcess_WrapperHelper(t *testing.T) {
	if os.Getenv("GO_WANT_RUN_WRAPPER_HELPER") != "1" {
		return
	}
	args := flag.Args()
	if len(args) == 0 {
		os.Exit(2)
	}
	runner := &ExecProcessRunner{}
	os.Exit(runner.Run(context.Background(), args[0], args[1:], os.Environ()))
}

func TestRunProcess_DirectPIDSignals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix signal forwarding test")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		sig  syscall.Signal
	}{
		{name: "SIGINT", sig: syscall.SIGINT},
		{name: "SIGTERM", sig: syscall.SIGTERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			readyFile := filepath.Join(dir, "ready")
			receivedFile := filepath.Join(dir, "received")

			script := `trap 'echo got > "$2"; exit 0' INT TERM; echo $$ > "$1"; while :; do sleep 0.05; done`
			wrapper := exec.Command(exe, "-test.run=^TestRunProcess_WrapperHelper$", "--", "sh", "-c", script, "sh", readyFile, receivedFile)
			wrapper.Env = append(os.Environ(), "GO_WANT_RUN_WRAPPER_HELPER=1")

			if err := wrapper.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if wrapper.Process != nil {
					_ = wrapper.Process.Kill()
				}
			}()

			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(readyFile); err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if _, err := os.Stat(readyFile); err != nil {
				t.Fatalf("child did not become ready: %v", err)
			}

			if err := wrapper.Process.Signal(tc.sig); err != nil {
				t.Fatalf("failed to signal wrapper: %v", err)
			}

			waitCh := make(chan error, 1)
			go func() { waitCh <- wrapper.Wait() }()

			select {
			case err := <-waitCh:
				if err != nil {
					t.Fatalf("expected clean wrapper exit after %s, got %v", tc.name, err)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("wrapper did not exit after direct %s", tc.name)
			}

			if _, err := os.Stat(receivedFile); err != nil {
				t.Fatalf("expected child to receive %s, but marker missing: %v", tc.name, err)
			}
		})
	}
}

func TestRunProcess_TerminatesDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix descendant process group test")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	parentReady := filepath.Join(dir, "parent.ready")
	grandReady := filepath.Join(dir, "grand.ready")
	parentDone := filepath.Join(dir, "parent.done")
	grandDone := filepath.Join(dir, "grand.done")

	script := `
sh -c 'trap "echo TERM > \"$2\"; exit 0" TERM; echo $$ > "$1"; while :; do sleep 0.05; done' sh "$2" "$4" &
trap 'echo TERM > "$3"; exit 0' TERM
echo $$ > "$1"
while :; do sleep 0.05; done
`
	wrapper := exec.Command(exe, "-test.run=^TestRunProcess_WrapperHelper$", "--", "sh", "-c", script, "sh", parentReady, grandReady, parentDone, grandDone)
	wrapper.Env = append(os.Environ(), "GO_WANT_RUN_WRAPPER_HELPER=1")

	if err := wrapper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if wrapper.Process != nil {
			_ = wrapper.Process.Kill()
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, err1 := os.Stat(parentReady)
		_, err2 := os.Stat(grandReady)
		if err1 == nil && err2 == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(grandReady); err != nil {
		t.Fatalf("grandchild did not become ready: %v", err)
	}

	if err := wrapper.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to signal wrapper: %v", err)
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- wrapper.Wait() }()

	select {
	case err := <-waitCh:
		if err != nil {
			t.Fatalf("expected clean wrapper exit, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("wrapper did not exit after SIGTERM")
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, err1 := os.Stat(parentDone)
		_, err2 := os.Stat(grandDone)
		if err1 == nil && err2 == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected both parent and grandchild to receive SIGTERM (parentDone=%v, grandDone=%v)",
		fileExists(parentDone), fileExists(grandDone))
}

func TestRunProcess_PTYForegroundRawReadAndCtrlC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix PTY foreground test")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available for PTY harness")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	pyHarness := `
import errno, os, pty, select, signal, sys, time

exe = sys.argv[1]
mode = sys.argv[2]

child_code = """
import os, signal, sys, termios, time, tty
mode = sys.argv[1]
if mode == 'raw-read':
    old = termios.tcgetattr(0)
    tty.setraw(0)
    os.write(1, b'READY')
    b = os.read(0, 1)
    termios.tcsetattr(0, termios.TCSANOW, old)
    os.write(1, b'READ_OK ' + b.hex().encode('ascii'))
else:
    signal.signal(signal.SIGINT, lambda *a: (os.write(1, b'ONE_INT'), os._exit(0)))
    os.write(1, b'READY')
    while True:
        time.sleep(0.05)
"""

pid, fd = pty.fork()
if pid == 0:
    env = dict(os.environ)
    env["GO_WANT_RUN_WRAPPER_HELPER"] = "1"
    args = [exe, "-test.run=^TestRunProcess_WrapperHelper$", "--", sys.executable, "-c", child_code, mode]
    os.execve(exe, args, env)

output = b""
sent = False
reaped = False
end = time.monotonic() + 3.0
try:
    while time.monotonic() < end:
        if select.select([fd], [], [], 0.03)[0]:
            try:
                chunk = os.read(fd, 4096)
            except OSError as e:
                if e.errno == errno.EIO:
                    break
                raise
            if not chunk:
                break
            output += chunk
        if b"READY" in output and not sent:
            os.write(fd, b"x" if mode == "raw-read" else b"\x03")
            sent = True
        done, _ = os.waitpid(pid, os.WNOHANG)
        if done:
            reaped = True
            break
    if not reaped:
        deadline = time.monotonic() + 1.0
        while time.monotonic() < deadline:
            done, _ = os.waitpid(pid, os.WNOHANG)
            if done:
                reaped = True
                break
            time.sleep(0.01)
finally:
    if not reaped:
        try:
            os.kill(pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        os.waitpid(pid, 0)
    os.close(fd)

text = output.decode(errors="replace")
if mode == "raw-read":
    if "READ_OK 78" not in text or not reaped:
        sys.stderr.write(f"raw-read failed: reaped={reaped} output={text!r}\n")
        sys.exit(1)
else:
    if text.count("ONE_INT") != 1 or not reaped:
        sys.stderr.write(f"terminal-int failed: count={text.count('ONE_INT')} reaped={reaped} output={text!r}\n")
        sys.exit(1)
`

	for _, mode := range []string{"raw-read", "terminal-int"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(python, "-c", pyHarness, exe, mode)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("PTY %s failed: %v\n%s", mode, err, string(out))
			}
		})
	}
}
