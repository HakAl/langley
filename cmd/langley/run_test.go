package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
		"LANGLEY_ACTIVE":      "1",
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

// --- Single-terminal session tests (langley-cup3) ---

func TestProxyEnvBuilder_OverridesInheritedLangleyActive(t *testing.T) {
	t.Setenv("LANGLEY_ACTIVE", "0")
	env := (&ProxyEnvBuilder{}).Build("localhost:9090", "/ca.crt")

	var values []string
	for _, e := range env {
		if key, val, _ := strings.Cut(e, "="); strings.EqualFold(key, "LANGLEY_ACTIVE") {
			values = append(values, val)
		}
	}
	if len(values) != 1 || values[0] != "1" {
		t.Errorf("LANGLEY_ACTIVE values = %v, want exactly [1]", values)
	}
}

// newTestHookManager returns a hook manager rooted in temp dirs (never the real $HOME).
func newTestHookManager(t *testing.T, shellPath string) (m *FileShellHookManager, configDir, home string) {
	t.Helper()
	root := t.TempDir()
	configDir = filepath.Join(root, "config")
	home = filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	return NewFileShellHookManager(configDir, home, shellPath), configDir, home
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeFile(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}
}

func TestFileShellHookManager_ActivateWritesGuardedEnvFile(t *testing.T) {
	m, configDir, _ := newTestHookManager(t, "/bin/zsh")

	if err := m.Activate("localhost:9090", "/certs/ca.crt", 4242); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	envPath := filepath.Join(configDir, "active.env")
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatalf("active.env not written: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Errorf("active.env mode = %o, want 600", info.Mode().Perm())
	}

	content := readFile(t, envPath)
	for _, want := range []string{
		"if kill -0 4242 2>/dev/null; then\n",
		"export HTTPS_PROXY='http://localhost:9090'\n",
		"export https_proxy='http://localhost:9090'\n",
		"export HTTP_PROXY='http://localhost:9090'\n",
		"export http_proxy='http://localhost:9090'\n",
		"export NODE_EXTRA_CA_CERTS='/certs/ca.crt'\n",
		"export SSL_CERT_FILE='/certs/ca.crt'\n",
		"export REQUESTS_CA_BUNDLE='/certs/ca.crt'\n",
		"export LANGLEY_ACTIVE='1'\n",
		"fi\n",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("active.env missing %q\n---\n%s", want, content)
		}
	}
}

func TestFileShellHookManager_AppendsBlockPreservingContentAndMode(t *testing.T) {
	m, configDir, home := newTestHookManager(t, "/bin/zsh")
	zshrc := filepath.Join(home, ".zshrc")
	bashrc := filepath.Join(home, ".bashrc")
	const zshOrig = "export PATH=$HOME/bin:$PATH\nalias ll='ls -l'" // no trailing newline
	const bashOrig = "# bash config\n"
	writeFile(t, zshrc, zshOrig, 0640)
	writeFile(t, bashrc, bashOrig, 0600)

	if err := m.Activate("localhost:9090", "/ca.crt", os.Getpid()); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	block := renderHookBlock(filepath.Join(configDir, "active.env"))
	if got := readFile(t, zshrc); got != zshOrig+"\n"+block {
		t.Errorf(".zshrc =\n%q\nwant\n%q", got, zshOrig+"\n"+block)
	}
	if got := readFile(t, bashrc); got != bashOrig+block {
		t.Errorf(".bashrc =\n%q\nwant\n%q", got, bashOrig+block)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(zshrc); info.Mode().Perm() != 0640 {
			t.Errorf(".zshrc mode = %o, want 640", info.Mode().Perm())
		}
		if info, _ := os.Stat(bashrc); info.Mode().Perm() != 0600 {
			t.Errorf(".bashrc mode = %o, want 600", info.Mode().Perm())
		}
	}

	if err := m.Deactivate(); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	if got := readFile(t, zshrc); got != zshOrig {
		t.Errorf(".zshrc after Deactivate = %q, want %q", got, zshOrig)
	}
	if got := readFile(t, bashrc); got != bashOrig {
		t.Errorf(".bashrc after Deactivate = %q, want %q", got, bashOrig)
	}
	if _, err := os.Stat(filepath.Join(configDir, "active.env")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("active.env still exists after Deactivate (err=%v)", err)
	}
}

func TestFileShellHookManager_OnlyCreatesRCForUserShell(t *testing.T) {
	m, _, home := newTestHookManager(t, "/usr/local/bin/bash")

	if err := m.Activate("localhost:9090", "/ca.crt", os.Getpid()); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".zshrc created for a bash user (err=%v)", err)
	}
	if !strings.Contains(readFile(t, filepath.Join(home, ".bashrc")), hookStartMarker) {
		t.Error(".bashrc missing hook block for bash user")
	}

	if err := m.Deactivate(); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".bashrc")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".bashrc created by Activate should be removed on Deactivate (err=%v)", err)
	}
}

func TestFileShellHookManager_IdempotentWithStaleBlock(t *testing.T) {
	m, configDir, home := newTestHookManager(t, "/bin/zsh")
	zshrc := filepath.Join(home, ".zshrc")
	// Simulate a block left behind by a SIGKILLed Langley.
	stale := "line1\n" + hookStartMarker + "\n[ -f '/old/active.env' ] && . '/old/active.env'\n" + hookEndMarker + "\nline2\n"
	writeFile(t, zshrc, stale, 0644)

	for i := 0; i < 2; i++ {
		if err := m.Activate("localhost:9090", "/ca.crt", os.Getpid()); err != nil {
			t.Fatalf("Activate #%d: %v", i+1, err)
		}
	}

	got := readFile(t, zshrc)
	if n := strings.Count(got, hookStartMarker); n != 1 {
		t.Errorf("hook block count = %d, want 1\n%s", n, got)
	}
	if strings.Contains(got, "/old/active.env") {
		t.Error("stale block not replaced")
	}
	want := "line1\nline2\n" + renderHookBlock(filepath.Join(configDir, "active.env"))
	if got != want {
		t.Errorf(".zshrc =\n%q\nwant\n%q", got, want)
	}

	for i := 0; i < 2; i++ {
		if err := m.Deactivate(); err != nil {
			t.Fatalf("Deactivate #%d: %v", i+1, err)
		}
	}
	if got := readFile(t, zshrc); got != "line1\nline2\n" {
		t.Errorf(".zshrc after Deactivate = %q", got)
	}
}

func TestFileShellHookManager_DeactivateKeepsUserEdits(t *testing.T) {
	m, _, home := newTestHookManager(t, "/bin/zsh")
	zshrc := filepath.Join(home, ".zshrc")
	writeFile(t, zshrc, "orig\n", 0644)

	if err := m.Activate("localhost:9090", "/ca.crt", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(zshrc, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("added-while-running\n")
	_ = f.Close()

	if err := m.Deactivate(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, zshrc); got != "orig\nadded-while-running\n" {
		t.Errorf(".zshrc after Deactivate = %q", got)
	}
}

func TestFileShellHookManager_PreservesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require privileges on Windows")
	}
	m, _, home := newTestHookManager(t, "/bin/zsh")
	target := filepath.Join(t.TempDir(), "dotfiles-zshrc")
	writeFile(t, target, "dotfiles\n", 0644)
	zshrc := filepath.Join(home, ".zshrc")
	if err := os.Symlink(target, zshrc); err != nil {
		t.Fatal(err)
	}

	if err := m.Activate("localhost:9090", "/ca.crt", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(zshrc); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".zshrc is no longer a symlink (err=%v)", err)
	}
	if !strings.Contains(readFile(t, target), hookStartMarker) {
		t.Error("hook block not written through symlink")
	}

	if err := m.Deactivate(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(zshrc); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".zshrc is no longer a symlink after Deactivate (err=%v)", err)
	}
	if got := readFile(t, target); got != "dotfiles\n" {
		t.Errorf("symlink target after Deactivate = %q", got)
	}
}

func TestFileShellHookManager_DeactivateLeavesOtherInstanceEnvFile(t *testing.T) {
	m, configDir, home := newTestHookManager(t, "/bin/zsh")
	zshrc := filepath.Join(home, ".zshrc")
	writeFile(t, zshrc, "orig\n", 0644)

	if err := m.Activate("localhost:9090", "/ca.crt", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	// A second Langley instance takes over active.env.
	other := renderActiveEnv("localhost:9092", "/ca.crt", os.Getppid())
	envPath := filepath.Join(configDir, "active.env")
	if err := os.WriteFile(envPath, other, 0600); err != nil {
		t.Fatal(err)
	}

	if err := m.Deactivate(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, envPath); got != string(other) {
		t.Error("Deactivate removed or changed another instance's active.env")
	}
	if !strings.Contains(readFile(t, zshrc), hookStartMarker) {
		t.Error("Deactivate removed RC hook while another instance owned active.env")
	}
}

func TestFileShellHookManager_MultiInstanceCoordination(t *testing.T) {
	pidA := os.Getpid()
	pidB := os.Getppid()
	if pidB <= 0 || pidB == pidA {
		t.Skip("distinct live parent PID not available")
	}

	for _, order := range []string{"A_then_B", "B_then_A"} {
		t.Run(order, func(t *testing.T) {
			root := t.TempDir()
			configDir := filepath.Join(root, "config")
			home := filepath.Join(root, "home")
			if err := os.MkdirAll(home, 0755); err != nil {
				t.Fatal(err)
			}
			zshrc := filepath.Join(home, ".zshrc")
			writeFile(t, zshrc, "orig\n", 0644)

			mA := NewFileShellHookManager(configDir, home, "/bin/zsh")
			mB := NewFileShellHookManager(configDir, home, "/bin/zsh")

			if err := mA.Activate("localhost:9090", "/ca.crt", pidA); err != nil {
				t.Fatalf("mA.Activate: %v", err)
			}
			if err := mB.Activate("localhost:9092", "/ca.crt", pidB); err != nil {
				t.Fatalf("mB.Activate: %v", err)
			}

			envPath := filepath.Join(configDir, "active.env")
			if order == "A_then_B" {
				if err := mA.Deactivate(); err != nil {
					t.Fatalf("mA.Deactivate: %v", err)
				}
				if got := readFile(t, envPath); !strings.Contains(got, "localhost:9092") {
					t.Errorf("after A exited first, active.env = %q, want B's 9092", got)
				}
				if !strings.Contains(readFile(t, zshrc), hookStartMarker) {
					t.Error("after A exited first, .zshrc hook was removed while B still active")
				}
				if err := mB.Deactivate(); err != nil {
					t.Fatalf("mB.Deactivate: %v", err)
				}
			} else {
				if err := mB.Deactivate(); err != nil {
					t.Fatalf("mB.Deactivate: %v", err)
				}
				if got := readFile(t, envPath); !strings.Contains(got, "localhost:9090") {
					t.Errorf("after B exited first, active.env = %q, want A's 9090", got)
				}
				if !strings.Contains(readFile(t, zshrc), hookStartMarker) {
					t.Error("after B exited first, .zshrc hook was removed while A still active")
				}
				if err := mA.Deactivate(); err != nil {
					t.Fatalf("mA.Deactivate: %v", err)
				}
			}

			if _, err := os.Stat(envPath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("active.env still exists after both exited (err=%v)", err)
			}
			if got := readFile(t, zshrc); got != "orig\n" {
				t.Errorf(".zshrc after both exited = %q, want %q", got, "orig\n")
			}
		})
	}

	t.Run("mixed_shells_created_rc_B_then_A", func(t *testing.T) {
		root := t.TempDir()
		configDir := filepath.Join(root, "config")
		home := filepath.Join(root, "home")
		if err := os.MkdirAll(home, 0755); err != nil {
			t.Fatal(err)
		}

		mA := NewFileShellHookManager(configDir, home, "/bin/zsh")
		mB := NewFileShellHookManager(configDir, home, "/bin/bash")

		if err := mA.Activate("localhost:9090", "/ca.crt", pidA); err != nil {
			t.Fatalf("mA.Activate: %v", err)
		}
		if err := mB.Activate("localhost:9092", "/ca.crt", pidB); err != nil {
			t.Fatalf("mB.Activate: %v", err)
		}
		if err := mB.Deactivate(); err != nil {
			t.Fatalf("mB.Deactivate: %v", err)
		}
		if err := mA.Deactivate(); err != nil {
			t.Fatalf("mA.Deactivate: %v", err)
		}

		for _, name := range []string{".zshrc", ".bashrc"} {
			if _, err := os.Stat(filepath.Join(home, name)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s still exists after both mixed-shell sessions exited (err=%v)", name, err)
			}
		}
	})
}

func TestFileShellHookManager_NoHomeOnlyWritesEnv(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	m := NewFileShellHookManager(configDir, "", "/bin/zsh")
	if err := m.Activate("localhost:9090", "/ca.crt", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := m.Deactivate(); err != nil {
		t.Fatal(err)
	}
}

func TestStripHookBlock(t *testing.T) {
	block := hookStartMarker + "\nx\n" + hookEndMarker + "\n"
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"no block", "a\nb\n", "a\nb\n"},
		{"block only", block, ""},
		{"middle", "a\n" + block + "b\n", "a\nb\n"},
		{"two blocks", block + "a\n" + block, "a\n"},
		{"end marker without newline", "a\n" + hookStartMarker + "\nx\n" + hookEndMarker, "a\n"},
		{"unterminated block left intact", "a\n" + hookStartMarker + "\nx\n", "a\n" + hookStartMarker + "\nx\n"},
		{"marker mid-line ignored", "echo '" + hookStartMarker + "'\n", "echo '" + hookStartMarker + "'\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(stripHookBlock([]byte(tt.in))); got != tt.want {
				t.Errorf("stripHookBlock(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// sourceEnvInSh sources envPath in a clean POSIX sh (preferring dash when available) and prints the named variables.
func sourceEnvInSh(t *testing.T, envPath string, vars ...string) []string {
	t.Helper()
	shBin := "sh"
	if dashPath, err := exec.LookPath("dash"); err == nil {
		shBin = dashPath
	}
	script := `. "$1"; shift; for v in "$@"; do eval "printf '%s\n' \"\${$v-<unset>}\""; done`
	args := append([]string{"-c", script, "sh", envPath}, vars...)
	cmd := exec.Command(shBin, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sh failed: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
}

func TestFileShellHookManager_EnvFileSourcing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX sh")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	// A PID that has certainly exited.
	dead := exec.Command("sh", "-c", "exit 0")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	deadPID := dead.ProcessState.Pid()

	caPath := "/certs/it's a \"path\" $(touch pwned)/ca.crt"
	tests := []struct {
		name string
		pid  int
		want []string
	}{
		{"live pid", os.Getpid(), []string{"http://localhost:9090", "http://localhost:9090", caPath, caPath, caPath, "1"}},
		{"dead pid", deadPID, []string{"<unset>", "<unset>", "<unset>", "<unset>", "<unset>", "<unset>"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, configDir, _ := newTestHookManager(t, "/bin/zsh")
			if err := m.Activate("localhost:9090", caPath, tt.pid); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = m.Deactivate() }()

			got := sourceEnvInSh(t, filepath.Join(configDir, "active.env"),
				"HTTPS_PROXY", "http_proxy", "NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "LANGLEY_ACTIVE")
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("sourced values = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFileShellHookManager_RCBlockSourcesEnvFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX sh")
	}
	m, _, home := newTestHookManager(t, "/bin/zsh")
	zshrc := filepath.Join(home, ".zshrc")
	writeFile(t, zshrc, "", 0644)
	if err := m.Activate("localhost:9090", "/ca.crt", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if got := sourceEnvInSh(t, zshrc, "HTTPS_PROXY"); got[0] != "http://localhost:9090" {
		t.Errorf("HTTPS_PROXY via rc = %q", got[0])
	}

	// After Deactivate a stale rc block (e.g. from another file) is a no-op.
	if err := m.Deactivate(); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(t.TempDir(), "rc")
	writeFile(t, stale, renderHookBlock(filepath.Join(home, "missing.env")), 0644)
	if got := sourceEnvInSh(t, stale, "HTTPS_PROXY"); got[0] != "<unset>" {
		t.Errorf("stale block set HTTPS_PROXY = %q", got[0])
	}
}

type recordedStart struct {
	name string
	args []string
}

func newTestBrowserOpener(goos string, hasXdg bool) (*OSBrowserOpener, *[]recordedStart) {
	var calls []recordedStart
	return &OSBrowserOpener{
		goos: goos,
		lookPath: func(file string) (string, error) {
			if hasXdg {
				return "/usr/bin/" + file, nil
			}
			return "", exec.ErrNotFound
		},
		start: func(name string, args ...string) error {
			calls = append(calls, recordedStart{name, args})
			return nil
		},
	}, &calls
}

func TestOSBrowserOpener_RejectsNonHTTPURLs(t *testing.T) {
	for _, u := range []string{
		"", "localhost:9091", "file:///etc/passwd", "javascript:alert(1)",
		"ftp://example.com", "http://", "-a Calculator", "http//localhost",
	} {
		o, calls := newTestBrowserOpener("darwin", true)
		if err := o.Open(u); err == nil {
			t.Errorf("Open(%q) succeeded, want error", u)
		}
		if len(*calls) != 0 {
			t.Errorf("Open(%q) launched %v", u, *calls)
		}
	}
}

func TestOSBrowserOpener_PlatformCommands(t *testing.T) {
	const u = "http://localhost:9091"
	tests := []struct {
		goos string
		want recordedStart
	}{
		{"darwin", recordedStart{"open", []string{u}}},
		{"windows", recordedStart{"cmd", []string{"/c", "start", "", u}}},
		{"linux", recordedStart{"xdg-open", []string{u}}},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			o, calls := newTestBrowserOpener(tt.goos, true)
			if err := o.Open(u); err != nil {
				t.Fatalf("Open: %v", err)
			}
			if len(*calls) != 1 || (*calls)[0].name != tt.want.name ||
				strings.Join((*calls)[0].args, "\x00") != strings.Join(tt.want.args, "\x00") {
				t.Errorf("launched %v, want %v", *calls, tt.want)
			}
		})
	}
}

func TestOSBrowserOpener_WindowsRejectsCmdMetacharacters(t *testing.T) {
	o, calls := newTestBrowserOpener("windows", false)
	if err := o.Open("http://localhost:9091/?a=1&calc"); err == nil {
		t.Error("expected error for URL containing '&' on Windows")
	}
	if len(*calls) != 0 {
		t.Errorf("launched %v", *calls)
	}
}

func TestOSBrowserOpener_LinuxWithoutXdgOpen(t *testing.T) {
	o, calls := newTestBrowserOpener("linux", false)
	if err := o.Open("https://localhost:9091"); err == nil {
		t.Error("expected error when xdg-open is missing")
	}
	if len(*calls) != 0 {
		t.Errorf("launched %v", *calls)
	}
}

func TestOSShellDetector_UserShell(t *testing.T) {
	tests := []struct {
		name      string
		goos      string
		env       map[string]string
		execPaths map[string]bool
		want      string
	}{
		{"executable $SHELL", "darwin", map[string]string{"SHELL": "/bin/zsh"}, map[string]bool{"/bin/zsh": true}, "/bin/zsh"},
		{"unset $SHELL", "linux", nil, nil, "/bin/sh"},
		{"non-executable $SHELL", "linux", map[string]string{"SHELL": "/bin/nope"}, nil, "/bin/sh"},
		{"relative $SHELL", "linux", map[string]string{"SHELL": "zsh"}, map[string]bool{"zsh": true}, "/bin/sh"},
		{"windows comspec", "windows", map[string]string{"ComSpec": `C:\Windows\system32\cmd.exe`}, nil, `C:\Windows\system32\cmd.exe`},
		{"windows default", "windows", nil, nil, "cmd.exe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &OSShellDetector{
				goos:   tt.goos,
				getenv: func(k string) string { return tt.env[k] },
				isExec: func(p string) bool { return tt.execPaths[p] },
			}
			if got, _ := d.UserShell(); got != tt.want {
				t.Errorf("UserShell() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsExecutableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "exe")
	plain := filepath.Join(dir, "plain")
	writeFile(t, exe, "#!/bin/sh\n", 0755)
	writeFile(t, plain, "", 0644)

	if !isExecutableFile(exe) {
		t.Error("executable file not detected")
	}
	if isExecutableFile(plain) {
		t.Error("non-executable file reported executable")
	}
	if isExecutableFile(dir) {
		t.Error("directory reported executable")
	}
	if isExecutableFile(filepath.Join(dir, "missing")) {
		t.Error("missing file reported executable")
	}
}

func TestOSShellDetector_IsInteractiveTTY_NonTTY(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devNull.Close() }()

	for _, d := range []*OSShellDetector{
		{stdin: f, stderr: f},
		{stdin: r, stderr: w},
		{stdin: devNull, stderr: devNull},
		{stdin: nil, stderr: nil},
	} {
		if d.IsInteractiveTTY() {
			t.Errorf("IsInteractiveTTY() = true for non-terminal stdio %+v", d)
		}
	}
}

func TestNoopShellHookManager(t *testing.T) {
	var m ShellHookManager = NoopShellHookManager{}
	if err := m.Activate("localhost:9090", "/ca.crt", 1); err != nil {
		t.Error(err)
	}
	if err := m.Deactivate(); err != nil {
		t.Error(err)
	}
}

func TestClientConnectAddr(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{":9091", "localhost:9091"},
		{"0.0.0.0:9091", "localhost:9091"},
		{"[::]:9091", "localhost:9091"},
		{"localhost:9091", "localhost:9091"},
		{"127.0.0.1:9091", "127.0.0.1:9091"},
		{"invalid", "invalid"},
	}
	for _, tt := range tests {
		if got := clientConnectAddr(tt.in); got != tt.want {
			t.Errorf("clientConnectAddr(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRunProcess_ContextCancellationTerminatesProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process group SIGTERM test")
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan int, 1)
	go func() {
		runner := &ExecProcessRunner{}
		doneCh <- runner.Run(ctx, "sh", []string{"-c", script, "sh", parentReady, grandReady, parentDone, grandDone}, os.Environ())
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

	cancel()

	select {
	case code := <-doneCh:
		if code != 0 {
			t.Fatalf("expected clean exit code 0 after context cancel SIGTERM, got %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after context cancellation")
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
	t.Fatalf("expected both parent and grandchild to receive SIGTERM on ctx cancel (parentDone=%v, grandDone=%v)",
		fileExists(parentDone), fileExists(grandDone))
}

func TestMainServer_Helper(t *testing.T) {
	if os.Getenv("GO_WANT_MAIN_SERVER_HELPER") != "1" {
		return
	}
	os.Args = append([]string{"langley"}, flag.Args()...)
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	main()
	os.Exit(0)
}

func TestSingleTerminalSession_PTYEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix PTY end-to-end test")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available for PTY harness")
	}
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	homeDir := filepath.Join(root, "home")
	configDir := filepath.Join(homeDir, ".config", "langley")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Pre-seed pricing cache so Source.Load() never makes a network request in tests.
	writeFile(t, filepath.Join(configDir, "litellm_pricing.json"), "{}", 0600)

	zshrc := filepath.Join(homeDir, ".zshrc")
	origZshrc := "# existing user zshrc\nexport FOO=bar\n"
	writeFile(t, zshrc, origZshrc, 0644)

	cfgPath := filepath.Join(root, "config.yaml")
	dbPath := filepath.Join(root, "langley.db")
	cfgContent := "proxy:\n  listen: localhost:0\npersistence:\n  db_path: " + dbPath + "\n"
	writeFile(t, cfgPath, cfgContent, 0600)

	probeFile := filepath.Join(root, "probe.txt")

	pyHarness := `
import errno, os, pty, select, signal, sys, time

exe, home_dir, sh_path, cfg_path, probe_file = sys.argv[1:6]

pid, fd = pty.fork()
if pid == 0:
    env = dict(os.environ)
    env["GO_WANT_MAIN_SERVER_HELPER"] = "1"
    env["HOME"] = home_dir
    env["SHELL"] = sh_path
    env.pop("LANGLEY_ACTIVE", None)
    env.pop("LANGLEY_NO_SHELL", None)
    env.pop("LANGLEY_NO_HOOK", None)
    args = [
        exe,
        "-test.run=^TestMainServer_Helper$",
        "--",
        "-no-open",
        "-config", cfg_path,
        "-listen", "localhost:0",
        "-api", "localhost:0",
    ]
    os.execve(exe, args, env)

output = b""
sent = False
reaped = False
exit_status = None
end = time.monotonic() + 8.0
try:
    while time.monotonic() < end:
        if select.select([fd], [], [], 0.05)[0]:
            try:
                chunk = os.read(fd, 4096)
            except OSError as e:
                if e.errno == errno.EIO:
                    break
                raise
            if not chunk:
                break
            output += chunk
        if b"Langley active" in output and not sent:
            time.sleep(0.15)
            cmd = (
                f'printf "ACTIVE=%s\\nPROXY=%s\\nCA=%s\\n" "$LANGLEY_ACTIVE" "$HTTPS_PROXY" "$SSL_CERT_FILE" > "{probe_file}"\n'
                f'grep -q "langley hook" "{home_dir}/.zshrc" && echo "HOOK_PRESENT=1" >> "{probe_file}"\n'
                'exit\n'
            ).encode("utf-8")
            os.write(fd, cmd)
            sent = True
        done, status = os.waitpid(pid, os.WNOHANG)
        if done:
            reaped = True
            exit_status = status
            break
    if not reaped:
        deadline = time.monotonic() + 3.0
        while time.monotonic() < deadline:
            done, status = os.waitpid(pid, os.WNOHANG)
            if done:
                reaped = True
                exit_status = status
                break
            time.sleep(0.02)
finally:
    if not reaped:
        try:
            os.kill(pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        os.waitpid(pid, 0)
    os.close(fd)

text = output.decode(errors="replace")
if not reaped or exit_status != 0:
    sys.stderr.write(f"PTY session did not exit cleanly: reaped={reaped} status={exit_status} output={text!r}\n")
    sys.exit(1)
`
	cmd := exec.Command(python, "-c", pyHarness, exe, homeDir, shPath, cfgPath, probeFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PTY end-to-end session failed: %v\n%s", err, string(out))
	}

	probe := readFile(t, probeFile)
	for _, want := range []string{
		"ACTIVE=1\n",
		"PROXY=http://localhost:",
		"HOOK_PRESENT=1\n",
	} {
		if !strings.Contains(probe, want) {
			t.Errorf("probe output missing %q:\n%s", want, probe)
		}
	}

	// Verify hook was cleaned up from .zshrc on exit
	if got := readFile(t, zshrc); got != origZshrc {
		t.Errorf(".zshrc after exit = %q, want %q", got, origZshrc)
	}

	// Verify active.env was removed and langley.log was written with 0600 permissions
	if _, err := os.Stat(filepath.Join(configDir, "active.env")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("active.env still exists after exit (err=%v)", err)
	}
	logPath := filepath.Join(configDir, "langley.log")
	logInfo, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("langley.log not found: %v", err)
	}
	if logInfo.Mode().Perm() != 0600 {
		t.Errorf("langley.log mode = %o, want 600", logInfo.Mode().Perm())
	}
	if logContent := readFile(t, logPath); !strings.Contains(logContent, "langley shutdown complete") {
		t.Errorf("langley.log missing shutdown message:\n%s", logContent)
	}
}

func TestFileShellHookManager_CrashFallbackToEarlierLiveSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX sh")
	}
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	rc := filepath.Join(home, ".zshrc")
	writeFile(t, rc, "original\n", 0644)

	a := NewFileShellHookManager(configDir, home, "/bin/zsh")
	b := NewFileShellHookManager(configDir, home, "/bin/zsh")

	sleeper := exec.Command("sh", "-c", "sleep 30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if sleeper.Process != nil {
			_ = sleeper.Process.Kill()
			_, _ = sleeper.Process.Wait()
		}
	}()

	if err := a.Activate("127.0.0.1:9090", "/certs/ca-a.pem", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Deactivate() }()

	if err := b.Activate("127.0.0.1:9092", "/certs/ca-b.pem", sleeper.Process.Pid); err != nil {
		t.Fatal(err)
	}

	// Simulate abrupt crash (SIGKILL) of the newest instance B without Deactivate().
	if err := sleeper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = sleeper.Process.Wait()
	sleeper.Process = nil

	if got := sourceEnvInSh(t, rc, "HTTPS_PROXY")[0]; got != "http://127.0.0.1:9090" {
		t.Fatalf("HTTPS_PROXY after newest instance crash = %q, want %q", got, "http://127.0.0.1:9090")
	}
}

func TestFileShellHookManager_MultiInstanceNoFinalNewline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX sh")
	}
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	rc := filepath.Join(home, ".zshrc")
	writeFile(t, rc, "original", 0644)

	a := NewFileShellHookManager(configDir, home, "/bin/zsh")
	b := NewFileShellHookManager(configDir, home, "/bin/zsh")

	sleeper := exec.Command("sh", "-c", "sleep 30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = sleeper.Process.Kill()
		_, _ = sleeper.Process.Wait()
	}()

	if err := a.Activate("127.0.0.1:9090", "/certs/ca-a.pem", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := b.Activate("127.0.0.1:9092", "/certs/ca-b.pem", sleeper.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := a.Deactivate(); err != nil {
		t.Fatal(err)
	}
	if err := b.Deactivate(); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, rc); got != "original" {
		t.Fatalf("restored RC = %q, want %q", got, "original")
	}
}

func TestFileShellHookManager_ConcurrentHandoff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX sh")
	}
	sleeper := exec.Command("sh", "-c", "sleep 30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = sleeper.Process.Kill()
		_, _ = sleeper.Process.Wait()
	}()

	for i := 0; i < 40; i++ {
		root := t.TempDir()
		configDir := filepath.Join(root, "config")
		home := filepath.Join(root, "home")
		if err := os.MkdirAll(home, 0755); err != nil {
			t.Fatal(err)
		}
		rc := filepath.Join(home, ".zshrc")
		writeFile(t, rc, "original\n", 0644)

		a := NewFileShellHookManager(configDir, home, "/bin/zsh")
		b := NewFileShellHookManager(configDir, home, "/bin/zsh")

		if err := a.Activate("127.0.0.1:9090", "/certs/ca-a.pem", os.Getpid()); err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var errA, errB error
		go func() {
			defer wg.Done()
			<-start
			errA = a.Deactivate()
		}()
		go func() {
			defer wg.Done()
			<-start
			errB = b.Activate("127.0.0.1:9092", "/certs/ca-b.pem", sleeper.Process.Pid)
		}()
		close(start)
		wg.Wait()

		if errA != nil {
			t.Fatalf("iter %d: a.Deactivate failed: %v", i, errA)
		}
		if errB != nil {
			t.Fatalf("iter %d: b.Activate failed: %v", i, errB)
		}

		if got := sourceEnvInSh(t, rc, "HTTPS_PROXY")[0]; got != "http://127.0.0.1:9092" {
			t.Fatalf("iter %d: HTTPS_PROXY after concurrent handoff = %q, want %q", i, got, "http://127.0.0.1:9092")
		}
		if err := b.Deactivate(); err != nil {
			t.Fatalf("iter %d: b.Deactivate failed: %v", i, err)
		}
	}
}

func TestFileShellHookManager_PausedHandoff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX sh")
	}
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	rc := filepath.Join(home, ".zshrc")
	writeFile(t, rc, "original\n", 0644)

	a := NewFileShellHookManager(configDir, home, "/bin/zsh")
	b := NewFileShellHookManager(configDir, home, "/bin/zsh")

	sleeper := exec.Command("sh", "-c", "sleep 30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = sleeper.Process.Kill()
		_, _ = sleeper.Process.Wait()
	}()

	if err := a.Activate("127.0.0.1:9090", "/certs/ca-a.pem", os.Getpid()); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	lookoutAfterEnvRead = func(string) {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	defer func() { lookoutAfterEnvRead = nil }()

	done := make(chan error, 1)
	go func() {
		done <- a.Deactivate()
	}()

	<-entered
	if err := b.Activate("127.0.0.1:9092", "/certs/ca-b.pem", sleeper.Process.Pid); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if got := sourceEnvInSh(t, rc, "HTTPS_PROXY")[0]; got != "http://127.0.0.1:9092" {
		t.Fatalf("HTTPS_PROXY after paused handoff = %q, want %q", got, "http://127.0.0.1:9092")
	}
}

func TestFileShellHookManager_WriteFailureRestoresOriginalRC(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	rc := filepath.Join(home, ".zshrc")
	const original = "export PATH=\"/custom/bin:$PATH\"\n"
	writeFile(t, rc, original, 0644)

	m := NewFileShellHookManager(configDir, home, "/bin/zsh")
	lookoutAfterRCTruncate = func(path string) error {
		if path == rc {
			return syscall.ENOSPC
		}
		return nil
	}
	defer func() { lookoutAfterRCTruncate = nil }()

	if err := m.Activate("127.0.0.1:9090", "/certs/ca.pem", os.Getpid()); err == nil {
		t.Fatal("Activate succeeded despite simulated write failure")
	}
	if got := readFile(t, rc); got != original {
		t.Fatalf("failed Activate left corrupted RC before Deactivate: got %q, want %q", got, original)
	}
	_ = m.Deactivate()

	if got := readFile(t, rc); got != original {
		t.Fatalf("failed Activate lost original RC content after Deactivate: got %q, want %q", got, original)
	}
}

func TestSingleTerminalSession_InteractiveShellJobTermination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix interactive shell job-control test")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, sh := range []string{"/bin/bash", "/bin/zsh"} {
		if !isExecutableFile(sh) {
			continue
		}
		t.Run(filepath.Base(sh), func(t *testing.T) {
			root := t.TempDir()
			homeDir := filepath.Join(root, "home")
			configDir := filepath.Join(homeDir, ".config", "langley")
			if err := os.MkdirAll(configDir, 0700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(configDir, "litellm_pricing.json"), "{}", 0600)

			cfgPath := filepath.Join(root, "config.yaml")
			dbPath := filepath.Join(root, "langley.db")
			writeFile(t, cfgPath, "proxy:\n  listen: localhost:0\npersistence:\n  db_path: "+dbPath+"\n", 0600)

			jobPy := filepath.Join(root, "job.py")
			jobScript := `import os, signal, sys, time
info_file, sig_file = sys.argv[1], sys.argv[2]
def handler(signum, _frame):
    with open(sig_file, "w") as fh:
        fh.write(str(signum))
    sys.exit(0)
signal.signal(signal.SIGTERM, handler)
signal.signal(signal.SIGHUP, handler)
with open(info_file, "w") as fh:
    fh.write(f"pid={os.getpid()}\nppid={os.getppid()}\npgid={os.getpgrp()}\n")
while True:
    time.sleep(0.1)
`
			writeFile(t, jobPy, jobScript, 0600)
			infoFile := filepath.Join(root, "job.info")
			sigFile := filepath.Join(root, "job.sig")

			pyHarness := `
import errno, os, pty, select, signal, sys, time

exe, home_dir, sh_path, cfg_path, job_py, info_file, sig_file = sys.argv[1:8]

pid, fd = pty.fork()
if pid == 0:
    env = dict(os.environ)
    env["GO_WANT_MAIN_SERVER_HELPER"] = "1"
    env["HOME"] = home_dir
    env["SHELL"] = sh_path
    env["PS1"] = "READY> "
    env["PROMPT"] = "READY> "
    env.pop("LANGLEY_ACTIVE", None)
    env.pop("LANGLEY_NO_SHELL", None)
    env.pop("LANGLEY_NO_HOOK", None)
    args = [
        exe,
        "-test.run=^TestMainServer_Helper$",
        "--",
        "-no-open",
        "-config", cfg_path,
        "-listen", "localhost:0",
        "-api", "localhost:0",
    ]
    os.execve(exe, args, env)

output = b""
sent = False
end = time.monotonic() + 8.0
while time.monotonic() < end and not os.path.exists(info_file):
    if select.select([fd], [], [], 0.05)[0]:
        try:
            chunk = os.read(fd, 4096)
        except OSError as e:
            if e.errno == errno.EIO:
                break
            raise
        if not chunk:
            break
        output += chunk
    if b"Langley active" in output and not sent:
        time.sleep(0.25)
        cmd = f'python3 "{job_py}" "{info_file}" "{sig_file}"\n'.encode("utf-8")
        os.write(fd, cmd)
        sent = True

if not os.path.exists(info_file):
    os.kill(pid, signal.SIGKILL)
    os.waitpid(pid, 0)
    sys.stderr.write(f"job did not start; output={output.decode(errors='replace')!r}\n")
    sys.exit(1)

os.kill(pid, signal.SIGTERM)
reaped = False
deadline = time.monotonic() + 5.0
while time.monotonic() < deadline:
    if select.select([fd], [], [], 0.05)[0]:
        try:
            os.read(fd, 4096)
        except OSError:
            pass
    done, _ = os.waitpid(pid, os.WNOHANG)
    if done:
        reaped = True
        break
os.close(fd)
if not reaped:
    os.kill(pid, signal.SIGKILL)
    os.waitpid(pid, 0)
    sys.stderr.write("langley did not exit after SIGTERM\n")
    sys.exit(1)
`
			cmd := exec.Command(python, "-c", pyHarness, exe, homeDir, sh, cfgPath, jobPy, infoFile, sigFile)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("interactive %s job termination failed: %v\n%s", sh, err, string(out))
			}

			sigData, err := os.ReadFile(sigFile)
			if err != nil || strings.TrimSpace(string(sigData)) == "" {
				t.Fatalf("expected foreground job in %s to receive signal on Langley SIGTERM, sigFile err=%v", sh, err)
			}
		})
	}
}

// runInRLimitSubprocess runs t in a dedicated child test binary without
// -test.testlogfile so setting RLIMIT_FSIZE=8 cannot trigger EFBIG when Go's
// internal testdeps log buffer happens to flush during an os.Stat/os.Open call.
func runInRLimitSubprocess(t *testing.T) bool {
	t.Helper()
	if os.Getenv("LANGLEY_RLIMIT_SUBPROC") == "1" {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), "LANGLEY_RLIMIT_SUBPROC=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess %s failed: %v\n%s", t.Name(), err, string(out))
	}
	return true
}

func TestFileShellHookManager_PersistentWriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix RLIMIT_FSIZE")
	}
	if runInRLimitSubprocess(t) {
		return
	}
	m, configDir, home := newTestHookManager(t, "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	const original = "export ORIGINAL_CONFIGURATION=keep_every_byte\n"
	writeFile(t, rc, original, 0600)

	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	defer func() { _ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old) }()

	lookoutAfterRCTruncate = func(path string) error {
		if path == rc {
			limited := old
			limited.Cur = 8
			return syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited)
		}
		return nil
	}
	activateErr := m.Activate("localhost:19090", "/certs/ca.pem", os.Getpid())
	lookoutAfterRCTruncate = nil
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	if activateErr == nil {
		t.Fatal("expected Activate error under RLIMIT_FSIZE=8")
	}
	if !fileExists(filepath.Join(configDir, "rc-backup.zshrc.json")) {
		t.Fatal("expected rc-backup.zshrc.json to be retained after failed primary+rollback write")
	}
	if err := m.Deactivate(); err != nil {
		t.Fatalf("Deactivate failed after restoring RLIMIT_FSIZE: %v", err)
	}
	if got := readFile(t, rc); got != original {
		t.Fatalf("after cleanup got %q, want %q", got, original)
	}
}

func TestFileShellHookManager_BackupFailurePreventsMutation(t *testing.T) {
	m, configDir, home := newTestHookManager(t, "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	const original = "original"
	writeFile(t, rc, original, 0600)

	if err := os.MkdirAll(filepath.Join(configDir, "rc-backup.zshrc.json"), 0700); err != nil {
		t.Fatal(err)
	}
	err := m.Activate("localhost:19090", "/certs/ca.pem", os.Getpid())
	after := readFile(t, rc)
	_ = m.Deactivate()
	if err == nil || after != original {
		t.Fatalf("expected Activate error and unchanged RC when backup cannot be persisted; err=%v after=%q", err, after)
	}
}

func TestFileShellHookManager_FailedPeerActivationPreservesLiveHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX sh")
	}
	a, configDir, home := newTestHookManager(t, "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	writeFile(t, rc, "original\n", 0600)

	if err := a.Activate("localhost:19090", "/certs/ca.pem", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Deactivate() }()

	b := NewFileShellHookManager(configDir, home, "/bin/zsh")
	lookoutAfterRCTruncate = func(path string) error {
		if path == rc {
			return syscall.ENOSPC
		}
		return nil
	}
	err := b.Activate("localhost:19092", "/certs/ca.pem", os.Getppid())
	lookoutAfterRCTruncate = nil
	if err == nil {
		t.Fatal("expected B Activate to fail under simulated ENOSPC")
	}
	if err := b.Deactivate(); err != nil {
		t.Fatalf("b.Deactivate: %v", err)
	}
	if got := sourceEnvInSh(t, rc, "HTTPS_PROXY")[0]; got != "http://localhost:19090" {
		t.Fatalf("fresh shell HTTPS_PROXY after failed B cleanup = %q, want %q", got, "http://localhost:19090")
	}
}

func TestFileShellHookManager_EmptyUserEditPreserved(t *testing.T) {
	m, _, home := newTestHookManager(t, "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	writeFile(t, rc, "original\n", 0600)

	if err := m.Activate("localhost:19090", "/certs/ca.pem", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	// User intentionally empties their existing .zshrc while Langley is running.
	writeFile(t, rc, "", 0600)

	if err := m.Deactivate(); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	if got := readFile(t, rc); got != "" {
		t.Fatalf("cleanup overwrote user's intentional empty RC edit: got %q, want empty", got)
	}
}

func TestFileShellHookManager_PersistentRestoreFailureRetryRecovers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix RLIMIT_FSIZE")
	}
	if runInRLimitSubprocess(t) {
		return
	}
	m, configDir, home := newTestHookManager(t, "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	const original = "export ORIGINAL_CONFIGURATION=keep_every_byte\n"
	writeFile(t, rc, original, 0600)

	if err := m.Activate("localhost:19090", "/certs/ca.pem", os.Getpid()); err != nil {
		t.Fatal(err)
	}

	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	defer func() { _ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old) }()

	lookoutAfterRCTruncate = func(path string) error {
		if path == rc {
			limited := old
			limited.Cur = 8
			return syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited)
		}
		return nil
	}
	firstErr := m.Deactivate()
	lookoutAfterRCTruncate = nil
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	if firstErr == nil {
		t.Fatal("expected first Deactivate to fail under RLIMIT_FSIZE=8")
	}
	if !fileExists(filepath.Join(configDir, "rc-backup.zshrc.json")) {
		t.Fatal("expected rc-backup.zshrc.json to remain on disk after failed restore")
	}
	if err := m.Deactivate(); err != nil {
		t.Fatalf("retry Deactivate failed: %v", err)
	}
	if got := readFile(t, rc); got != original {
		t.Fatalf("after retry Deactivate got %q, want %q", got, original)
	}
}

func TestRunProcess_NonInteractiveSIGTERMGracePeriod(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix SIGTERM grace period test")
	}
	dir := t.TempDir()
	readyFile := filepath.Join(dir, "ready")
	gotFile := filepath.Join(dir, "got")
	doneFile := filepath.Join(dir, "done")

	script := `
trap 'echo TERM > "$2"; sleep 0.6; echo CLEANUP_COMPLETE > "$3"; exit 0' TERM
echo $$ > "$1"
while :; do sleep 0.05; done
`
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan int, 1)
	go func() {
		runner := &ExecProcessRunner{}
		doneCh <- runner.Run(ctx, "sh", []string{"-c", script, "sh", readyFile, gotFile, doneFile}, os.Environ())
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !fileExists(readyFile) {
		time.Sleep(10 * time.Millisecond)
	}
	if !fileExists(readyFile) {
		t.Fatal("child did not become ready")
	}

	cancel()

	select {
	case code := <-doneCh:
		if code != 0 {
			t.Fatalf("expected exit code 0 after 600ms SIGTERM cleanup, got %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("child did not exit within grace period")
	}

	if !fileExists(doneFile) {
		t.Fatal("child SIGTERM cleanup was killed prematurely before writing completion marker")
	}
}

func TestFileShellHookManager_ActivationBackupCommitFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions")
	}
	m, configDir, home := newTestHookManager(t, "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	const original = "BASE_SETTINGS=keep\n"
	const edit = "USER_ADDED=yes\n"
	writeFile(t, rc, original, 0600)

	lookoutAfterRCTruncate = func(path string) error {
		if path == rc {
			return os.Chmod(configDir, 0500)
		}
		return nil
	}
	defer func() {
		lookoutAfterRCTruncate = nil
		_ = os.Chmod(configDir, 0700)
	}()

	if err := m.Activate("localhost:19090", "/certs/ca.pem", os.Getpid()); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}
	lookoutAfterRCTruncate = nil
	if err := os.Chmod(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, rc, readFile(t, rc)+edit, 0600)
	if err := m.Deactivate(); err != nil {
		t.Fatalf("Deactivate failed: %v", err)
	}
	if got := readFile(t, rc); got != original+edit {
		t.Fatalf("after Deactivate got %q, want %q", got, original+edit)
	}
}

func TestFileShellHookManager_RestoreBackupUpdateFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix RLIMIT_FSIZE")
	}
	if runInRLimitSubprocess(t) {
		return
	}
	for _, fresh := range []bool{false, true} {
		name := "same-manager"
		if fresh {
			name = "fresh-manager"
		}
		t.Run(name, func(t *testing.T) {
			m, configDir, home := newTestHookManager(t, "/bin/zsh")
			rc := filepath.Join(home, ".zshrc")
			const original = "BASE_SETTINGS=keep\n"
			const edit = "USER_ADDED=yes\n"
			writeFile(t, rc, original, 0600)

			if err := m.Activate("localhost:19090", "/certs/ca.pem", os.Getpid()); err != nil {
				t.Fatal(err)
			}
			writeFile(t, rc, readFile(t, rc)+edit, 0600)

			var old syscall.Rlimit
			if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
				t.Fatal(err)
			}
			signal.Ignore(syscall.SIGXFSZ)
			defer signal.Reset(syscall.SIGXFSZ)
			defer func() { _ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old) }()

			limited := old
			limited.Cur = 8
			if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
				t.Fatal(err)
			}
			firstErr := m.Deactivate()
			if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
				t.Fatal(err)
			}
			if firstErr == nil {
				t.Fatal("expected first Deactivate to fail under RLIMIT_FSIZE=8")
			}
			if fresh {
				m = NewFileShellHookManager(configDir, home, "/bin/zsh")
			}
			if err := m.Deactivate(); err != nil {
				t.Fatalf("retry Deactivate failed: %v", err)
			}
			if got := readFile(t, rc); got != original+edit {
				t.Fatalf("after retry Deactivate got %q, want %q", got, original+edit)
			}
		})
	}
}

func TestFileShellHookManager_InterruptedActivationRecovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX subprocess test")
	}
	if os.Getenv("LANGLEY_CRASH_HELPER") == "1" {
		config := os.Getenv("LANGLEY_FIXTURE_CONFIG")
		home := os.Getenv("LANGLEY_FIXTURE_HOME")
		m := NewFileShellHookManager(config, home, "/bin/zsh")
		lookoutAfterRCTruncate = func(path string) error {
			if path == filepath.Join(home, ".zshrc") {
				os.Exit(91)
			}
			return nil
		}
		_ = m.Activate("localhost:19090", "/ca", os.Getpid())
		os.Exit(92)
	}
	_, config, home := newTestHookManager(t, "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	const original = "BASE_SETTINGS=keep\n"
	writeFile(t, rc, original, 0600)

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestFileShellHookManager_InterruptedActivationRecovery$")
	cmd.Env = append(os.Environ(),
		"LANGLEY_CRASH_HELPER=1",
		"LANGLEY_FIXTURE_CONFIG="+config,
		"LANGLEY_FIXTURE_HOME="+home,
	)
	runErr := cmd.Run()
	ee, ok := runErr.(*exec.ExitError)
	if !ok || ee.ExitCode() != 91 {
		t.Fatalf("helper not interrupted at intended point: %v", runErr)
	}

	m := NewFileShellHookManager(config, home, "/bin/zsh")
	if err := m.Deactivate(); err != nil {
		t.Fatalf("fresh Deactivate failed: %v", err)
	}
	if got := readFile(t, rc); got != original {
		t.Fatalf("after fresh Deactivate got %q, want %q", got, original)
	}
}

func TestFileShellHookManager_UnavailableFailureMarkerRecovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix RLIMIT_FSIZE")
	}
	if runInRLimitSubprocess(t) {
		return
	}
	m, config, home := newTestHookManager(t, "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	const original = "BASE_SETTINGS=keep\n"
	writeFile(t, rc, original, 0600)

	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	defer func() {
		lookoutAfterRCTruncate = nil
		_ = os.Chmod(config, 0700)
		_ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old)
	}()

	lookoutAfterRCTruncate = func(path string) error {
		if path == rc {
			if err := os.Chmod(config, 0500); err != nil {
				return err
			}
			lim := old
			lim.Cur = 8
			return syscall.Setrlimit(syscall.RLIMIT_FSIZE, &lim)
		}
		return nil
	}
	activateErr := m.Activate("localhost:19090", "/ca", os.Getpid())
	lookoutAfterRCTruncate = nil
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(config, 0700); err != nil {
		t.Fatal(err)
	}
	if activateErr == nil {
		t.Fatal("expected sustained write failure")
	}
	if err := os.Remove(m.pidEnvPath(os.Getpid())); err != nil {
		t.Fatal(err)
	}
	fresh := NewFileShellHookManager(config, home, "/bin/zsh")
	if err := fresh.Deactivate(); err != nil {
		t.Fatalf("fresh Deactivate failed: %v", err)
	}
	if got := readFile(t, rc); got != original {
		t.Fatalf("after fresh Deactivate got %q, want %q", got, original)
	}
}

func TestFileShellHookManager_PeerRecoveryMetadataFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions and subprocess test")
	}
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not available")
	}
	for _, denyMetadata := range []bool{false, true} {
		name := "metadata-writable-control"
		if denyMetadata {
			name = "metadata-unwritable"
		}
		t.Run(name, func(t *testing.T) {
			a, config, home := newTestHookManager(t, "/bin/zsh")
			rc := filepath.Join(home, ".zshrc")
			const original = "BASE_SETTINGS=keep\n"
			const edit = "USER_ADDED=yes\n"
			writeFile(t, rc, original, 0600)

			if err := a.Activate("localhost:19090", "/ca", os.Getpid()); err != nil {
				t.Fatal(err)
			}
			peer := exec.Command(sleepPath, "30")
			if err := peer.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = peer.Process.Kill()
				_ = peer.Wait()
			}()

			b := NewFileShellHookManager(config, home, "/bin/zsh")
			lookoutAfterRCTruncate = func(path string) error {
				if path == rc {
					if err := os.Chmod(rc, 0400); err != nil {
						return err
					}
					return syscall.ENOSPC
				}
				return nil
			}
			defer func() {
				lookoutAfterRCTruncate = nil
				_ = os.Chmod(rc, 0600)
				_ = os.Chmod(config, 0700)
			}()

			activateErr := b.Activate("localhost:19092", "/ca", peer.Process.Pid)
			lookoutAfterRCTruncate = nil
			if activateErr == nil {
				t.Fatal("expected failed activation and rollback")
			}
			if err := os.Chmod(rc, 0600); err != nil {
				t.Fatal(err)
			}
			if denyMetadata {
				if err := os.Chmod(config, 0500); err != nil {
					t.Fatal(err)
				}
			}
			_ = b.Deactivate()
			if err := os.Chmod(config, 0700); err != nil {
				t.Fatal(err)
			}
			_ = peer.Process.Kill()
			_ = peer.Wait()

			writeFile(t, rc, readFile(t, rc)+edit, 0600)
			if err := a.Deactivate(); err != nil {
				t.Fatalf("a.Deactivate failed: %v", err)
			}
			if got := readFile(t, rc); got != original+edit {
				t.Fatalf("after a.Deactivate got %q, want %q", got, original+edit)
			}
		})
	}
}

func TestFileShellHookManager_PartialPeerRollbackWithCompleteHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix RLIMIT_FSIZE")
	}
	if runInRLimitSubprocess(t) {
		return
	}
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not available")
	}
	a, config, home := newTestHookManager(t, "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	const original = "BASE_SETTINGS=keep\n"
	writeFile(t, rc, original, 0600)

	if err := a.Activate("localhost:19090", "/ca", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	installed := readFile(t, rc)
	edit := "USER_ADDED=" + strings.Repeat("x", len(installed)*2) + "\n"
	writeFile(t, rc, installed+edit, 0600)

	peer := exec.Command(sleepPath, "30")
	if err := peer.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = peer.Process.Kill()
		_ = peer.Wait()
	}()

	b := NewFileShellHookManager(config, home, "/bin/zsh")
	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	defer func() {
		lookoutAfterRCTruncate = nil
		_ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old)
	}()

	lookoutAfterRCTruncate = func(path string) error {
		if path == rc {
			lim := old
			lim.Cur = uint64(len(installed))
			return syscall.Setrlimit(syscall.RLIMIT_FSIZE, &lim)
		}
		return nil
	}
	activateErr := b.Activate("localhost:19092", "/ca", peer.Process.Pid)
	lookoutAfterRCTruncate = nil
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	if activateErr == nil {
		t.Fatal("expected both writes to exceed the actual size limit")
	}
	_ = peer.Process.Kill()
	_ = peer.Wait()

	if err := a.Deactivate(); err != nil {
		t.Fatalf("a.Deactivate failed: %v", err)
	}
	if got := readFile(t, rc); got != original+edit {
		t.Fatalf("after a.Deactivate got %q, want %q", got, original+edit)
	}
}

func TestFileShellHookManager_HealthyBackupFallbackPreservesOriginal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix RLIMIT_FSIZE")
	}
	if runInRLimitSubprocess(t) {
		return
	}
	m, config, home := newTestHookManager(t, "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	const original = "BASE_SETTINGS=keep"
	writeFile(t, rc, original, 0600)

	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	defer func() {
		lookoutAfterRCTruncate = nil
		_ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old)
	}()

	lookoutAfterRCTruncate = func(path string) error {
		if path == rc {
			b, ok := loadRCBackup(filepath.Join(config, "rc-backup.zshrc.json"))
			if !ok {
				t.Fatal("missing pre-write backup")
			}
			lim := old
			lim.Cur = uint64(len(b.Written))
			return syscall.Setrlimit(syscall.RLIMIT_FSIZE, &lim)
		}
		return nil
	}
	if err := m.Activate("localhost:19090", "/ca", os.Getpid()); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}
	lookoutAfterRCTruncate = nil
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(config, "rc-backup.zshrc.json")
	if _, valid := loadRCBackup(backup); !valid {
		t.Fatal("healthy-state transition destroyed the intact backup")
	}
	if err := os.Remove(m.pidEnvPath(os.Getpid())); err != nil {
		t.Fatal(err)
	}
	fresh := NewFileShellHookManager(config, home, "/bin/zsh")
	if err := fresh.Deactivate(); err != nil {
		t.Fatalf("fresh.Deactivate failed: %v", err)
	}
	if got := readFile(t, rc); got != original {
		t.Fatalf("fresh cleanup got %q, want %q", got, original)
	}
}

func TestFileShellHookManager_UnavailableCommitThenEmptyEdit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions test")
	}
	for _, fresh := range []bool{false, true} {
		name := "same-manager"
		if fresh {
			name = "fresh-manager"
		}
		t.Run(name, func(t *testing.T) {
			m, config, home := newTestHookManager(t, "/bin/zsh")
			rc := filepath.Join(home, ".zshrc")
			backup := filepath.Join(config, "rc-backup.zshrc.json")
			const original = "BASE_SETTINGS=keep\n"
			writeFile(t, rc, original, 0600)

			lookoutAfterRCTruncate = func(path string) error {
				if path == rc {
					if err := os.Chmod(backup, 0400); err != nil {
						return err
					}
					return os.Chmod(config, 0500)
				}
				return nil
			}
			defer func() {
				lookoutAfterRCTruncate = nil
				_ = os.Chmod(config, 0700)
				_ = os.Chmod(backup, 0600)
			}()

			if err := m.Activate("localhost:19090", "/ca", os.Getpid()); err != nil {
				t.Fatalf("Activate failed: %v", err)
			}
			lookoutAfterRCTruncate = nil
			if err := os.Chmod(config, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(backup, 0600); err != nil {
				t.Fatal(err)
			}
			writeFile(t, rc, "", 0600)
			if fresh {
				if err := os.Remove(m.pidEnvPath(os.Getpid())); err != nil {
					t.Fatal(err)
				}
				m = NewFileShellHookManager(config, home, "/bin/zsh")
			}
			if err := m.Deactivate(); err != nil {
				t.Fatalf("Deactivate failed: %v", err)
			}
			if got := readFile(t, rc); got != "" {
				t.Fatalf("deliberately emptied RC got %q, want empty", got)
			}
		})
	}
}

func TestFileShellHookManager_PeerMetadataRetryDoesNotReplayRepair(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions test")
	}
	for _, deny := range []bool{false, true} {
		name := "healthy-control"
		if deny {
			name = "metadata-pending"
		}
		t.Run(name, func(t *testing.T) {
			a, config, home := newTestHookManager(t, "/bin/zsh")
			rc := filepath.Join(home, ".zshrc")
			backup := filepath.Join(config, "rc-backup.zshrc.json")
			const original = "BASE_SETTINGS=keep\n"
			const edit = "USER_ADDED_AFTER_REPAIR=yes\n"
			writeFile(t, rc, original, 0600)

			if err := a.Activate("localhost:19090", "/ca", os.Getpid()); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = a.Deactivate() }()

			b := NewFileShellHookManager(config, home, "/bin/zsh")
			lookoutAfterRCTruncate = func(path string) error {
				if path == rc {
					if err := os.Chmod(rc, 0400); err != nil {
						return err
					}
					return syscall.ENOSPC
				}
				return nil
			}
			defer func() {
				lookoutAfterRCTruncate = nil
				_ = os.Chmod(config, 0700)
				_ = os.Chmod(backup, 0600)
				_ = os.Chmod(rc, 0600)
			}()

			if err := b.Activate("localhost:19092", "/ca", os.Getppid()); err == nil {
				t.Fatal("expected failed activation and rollback")
			}
			lookoutAfterRCTruncate = nil
			if err := os.Chmod(rc, 0600); err != nil {
				t.Fatal(err)
			}
			if deny {
				if err := os.Chmod(config, 0500); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(backup, 0400); err != nil {
					t.Fatal(err)
				}
			}
			_ = b.Deactivate()
			if err := os.Chmod(config, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(backup, 0600); err != nil {
				t.Fatal(err)
			}
			repaired := readFile(t, rc)
			writeFile(t, rc, repaired+edit, 0600)
			if err := b.Deactivate(); err != nil {
				t.Fatalf("retry b.Deactivate failed: %v", err)
			}
			if got := readFile(t, rc); got != repaired+edit {
				t.Fatalf("retrying metadata persistence replayed stale RC preimage: got %q, want %q", got, repaired+edit)
			}
		})
	}
}

func TestFileShellHookManager_RepairCompletionThenFreshCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions test")
	}
	for _, deny := range []bool{false, true} {
		name := "unlink-allowed-control"
		if deny {
			name = "unlink-denied"
		}
		t.Run(name, func(t *testing.T) {
			m, config, home := newTestHookManager(t, "/bin/zsh")
			rc := filepath.Join(home, ".zshrc")
			const original = "BASE_SETTINGS=keep\n"
			const edit = "USER_ADDED_AFTER_REPAIR=yes\n"
			writeFile(t, rc, original, 0600)

			if err := m.Activate("localhost:19090", "/ca", os.Getpid()); err != nil {
				t.Fatal(err)
			}
			defer func() {
				lookoutAfterRCTruncate = nil
				_ = os.Chmod(config, 0700)
			}()
			if deny {
				lookoutAfterRCTruncate = func(path string) error {
					if path == rc {
						return os.Chmod(config, 0500)
					}
					return nil
				}
			}
			_ = m.Deactivate()
			lookoutAfterRCTruncate = nil
			if err := os.Chmod(config, 0700); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, rc); got != original {
				t.Fatalf("repair did not restore original: %q", got)
			}
			writeFile(t, rc, original+edit, 0600)
			fresh := NewFileShellHookManager(config, home, "/bin/zsh")
			if err := fresh.Deactivate(); err != nil {
				t.Fatalf("fresh.Deactivate failed: %v", err)
			}
			if got := readFile(t, rc); got != original+edit {
				t.Fatalf("fresh cleanup replayed stale backup: got %q, want %q", got, original+edit)
			}
		})
	}
}

func TestFileShellHookManager_PeerMetadataPendingOwnerExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions test")
	}
	for _, deny := range []bool{false, true} {
		name := "metadata-writable-control"
		if deny {
			name = "metadata-unwritable"
		}
		t.Run(name, func(t *testing.T) {
			a, config, home := newTestHookManager(t, "/bin/zsh")
			rc := filepath.Join(home, ".zshrc")
			backup := filepath.Join(config, "rc-backup.zshrc.json")
			const original = "BASE_SETTINGS=keep\n"
			const edit = "USER_ADDED_AFTER_REPAIR=yes\n"
			writeFile(t, rc, original, 0600)

			if err := a.Activate("localhost:19090", "/ca", os.Getpid()); err != nil {
				t.Fatal(err)
			}

			b := NewFileShellHookManager(config, home, "/bin/zsh")
			lookoutAfterRCTruncate = func(path string) error {
				if path == rc {
					if err := os.Chmod(rc, 0400); err != nil {
						return err
					}
					return syscall.ENOSPC
				}
				return nil
			}
			defer func() {
				lookoutAfterRCTruncate = nil
				_ = os.Chmod(config, 0700)
				_ = os.Chmod(backup, 0600)
				_ = os.Chmod(rc, 0600)
			}()

			if err := b.Activate("localhost:19092", "/ca", os.Getppid()); err == nil {
				t.Fatal("expected failed activation and rollback")
			}
			lookoutAfterRCTruncate = nil
			if err := os.Chmod(rc, 0600); err != nil {
				t.Fatal(err)
			}
			if deny {
				if err := os.Chmod(config, 0500); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(backup, 0400); err != nil {
					t.Fatal(err)
				}
			}
			_ = b.Deactivate()
			if err := os.Chmod(config, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(backup, 0600); err != nil {
				t.Fatal(err)
			}
			writeFile(t, rc, readFile(t, rc)+edit, 0600)
			if err := a.Deactivate(); err != nil {
				t.Fatalf("a.Deactivate failed: %v", err)
			}
			if got := readFile(t, rc); got != original+edit {
				t.Fatalf("live peer cleanup lost post-repair edit: got %q, want %q", got, original+edit)
			}
		})
	}
}

func TestFileShellHookManager_ShortHealthyFlagWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix RLIMIT_FSIZE")
	}
	if runInRLimitSubprocess(t) {
		return
	}
	m, config, home := newTestHookManager(t, "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	writeFile(t, rc, "BASE_SETTINGS=keep\n", 0600)
	if err := m.Activate("localhost:19090", "/ca", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(config, "rc-backup.zshrc.json")
	b, ok := loadRCBackup(backup)
	if !ok {
		t.Fatal("missing initial backup")
	}
	b.WriteFailed = true
	if err := saveRCBackup(backup, *b); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(backup, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	defer func() { _ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old) }()

	lim := old
	lim.Cur = 18
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &lim); err != nil {
		t.Fatal(err)
	}
	_ = markRCBackupHealthyInPlace(backup, f)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	if _, valid := loadRCBackup(backup); !valid {
		t.Fatalf("short pwrite corrupted backup JSON: %q", readFile(t, backup))
	}
}

func TestFileShellHookManager_PeerEnvFallbackFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix RLIMIT_FSIZE and POSIX sh")
	}
	if runInRLimitSubprocess(t) {
		return
	}
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not available")
	}
	a, config, home := newTestHookManager(t, "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	writeFile(t, rc, "BASE=keep\n", 0600)

	if err := a.Activate("localhost:19090", "/ca", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Deactivate() }()

	peer := exec.Command(sleepPath, "30")
	if err := peer.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = peer.Process.Kill()
		_ = peer.Wait()
	}()

	b := NewFileShellHookManager(config, home, "/bin/zsh")
	if err := b.Activate("localhost:19092", "/ca", peer.Process.Pid); err != nil {
		t.Fatal(err)
	}

	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	defer func() { _ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old) }()

	lim := old
	lim.Cur = 8
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &lim); err != nil {
		t.Fatal(err)
	}
	firstErr := b.Deactivate()
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	if firstErr == nil {
		t.Fatal("expected first b.Deactivate to fail under RLIMIT_FSIZE=8")
	}
	_ = peer.Process.Kill()
	_ = peer.Wait()

	if err := b.Deactivate(); err != nil {
		t.Fatalf("retry b.Deactivate failed: %v", err)
	}
	if got := sourceEnvInSh(t, rc, "HTTPS_PROXY")[0]; got != "http://localhost:19090" {
		t.Fatalf("fresh shell HTTPS_PROXY = %q, want %q", got, "http://localhost:19090")
	}
}

func TestFileShellHookManager_CompletionByteWriteUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix RLIMIT_FSIZE")
	}
	if runInRLimitSubprocess(t) {
		return
	}
	m, config, home := newTestHookManager(t, "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	backup := filepath.Join(config, "rc-backup.zshrc.json")
	const original = "X=1\n"
	const edit = "USER_ADDED_AFTER_REPAIR=yes\n"
	writeFile(t, rc, original, 0600)

	if err := m.Activate("localhost:19090", "/ca", os.Getpid()); err != nil {
		t.Fatal(err)
	}

	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	defer func() {
		lookoutAfterRCTruncate = nil
		_ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old)
		_ = os.Chmod(config, 0700)
	}()

	lookoutAfterRCTruncate = func(path string) error {
		if path == rc {
			if err := os.Chmod(config, 0500); err != nil {
				return err
			}
			lim := old
			lim.Cur = 5
			return syscall.Setrlimit(syscall.RLIMIT_FSIZE, &lim)
		}
		return nil
	}
	_ = m.Deactivate()
	lookoutAfterRCTruncate = nil
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(config, 0700); err != nil {
		t.Fatal(err)
	}

	repaired := readFile(t, rc)
	if repaired != original {
		t.Fatalf("repaired RC = %q, want %q", repaired, original)
	}
	if state, ok := loadRCBackup(backup); !ok || state.WriteFailed {
		t.Fatalf("expected parseable healthy backup after completion write, ok=%v state=%+v", ok, state)
	}
	writeFile(t, rc, repaired+edit, 0600)

	fresh := NewFileShellHookManager(config, home, "/bin/zsh")
	if err := fresh.Deactivate(); err != nil {
		t.Fatalf("fresh.Deactivate failed: %v", err)
	}
	if got := readFile(t, rc); got != original+edit {
		t.Fatalf("fresh cleanup lost post-repair edit: got %q, want %q", got, original+edit)
	}
}

func TestFileShellHookManager_OlderManagerAfterFreshRepair(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix RLIMIT_FSIZE")
	}
	if runInRLimitSubprocess(t) {
		return
	}
	for _, failFirst := range []bool{false, true} {
		name := "healthy-control"
		if failFirst {
			name = "failed-old-manager"
		}
		t.Run(name, func(t *testing.T) {
			m, config, home := newTestHookManager(t, "/bin/zsh")
			rc := filepath.Join(home, ".zshrc")
			const original = "BASE_SETTINGS=keep\n"
			const edit = "USER_ADDED_AFTER_FRESH_REPAIR=yes\n"
			writeFile(t, rc, original, 0600)

			if err := m.Activate("localhost:19090", "/ca", os.Getpid()); err != nil {
				t.Fatal(err)
			}

			var old syscall.Rlimit
			if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
				t.Fatal(err)
			}
			signal.Ignore(syscall.SIGXFSZ)
			defer signal.Reset(syscall.SIGXFSZ)
			defer func() {
				lookoutAfterRCTruncate = nil
				_ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old)
			}()

			if failFirst {
				lookoutAfterRCTruncate = func(path string) error {
					if path == rc {
						lim := old
						lim.Cur = 8
						return syscall.Setrlimit(syscall.RLIMIT_FSIZE, &lim)
					}
					return nil
				}
			}
			_ = m.Deactivate()
			lookoutAfterRCTruncate = nil
			if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
				t.Fatal(err)
			}

			fresh := NewFileShellHookManager(config, home, "/bin/zsh")
			if err := fresh.Deactivate(); err != nil {
				t.Fatalf("fresh.Deactivate failed: %v", err)
			}
			repaired := readFile(t, rc)
			if repaired != original {
				t.Fatalf("fresh repair got %q, want %q", repaired, original)
			}
			writeFile(t, rc, repaired+edit, 0600)

			if err := m.Deactivate(); err != nil {
				t.Fatalf("old manager retry Deactivate failed: %v", err)
			}
			if got := readFile(t, rc); got != original+edit {
				t.Fatalf("old manager replayed already completed restoration over newer user edit: got %q, want %q", got, original+edit)
			}
		})
	}
}

func TestLangleyEnvLiveness(t *testing.T) {
	for _, tc := range []struct {
		name, marker string
		state        *ServerState
		err          error
		want         bool
	}{
		{name: "missing marker", state: &ServerState{PID: os.Getpid()}},
		{name: "live", marker: "1", state: &ServerState{PID: os.Getpid()}, want: true},
		{name: "missing state", marker: "1", err: ErrServerNotRunning},
		{name: "invalid pid", marker: "1", state: &ServerState{}},
		{name: "dead pid", marker: "1", state: &ServerState{PID: 2147483647}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LANGLEY_ACTIVE", tc.marker)
			if got := isLangleyEnvActive(&mockStateReader{state: tc.state, err: tc.err}); got != tc.want {
				t.Fatalf("active=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunningInstance(t *testing.T) {
	state := &ServerState{PID: os.Getpid(), APIAddr: "localhost:9091"}
	for _, tc := range []struct {
		name  string
		state *ServerState
		err   error
		want  bool
	}{
		{"live healthy", state, nil, true}, {"unhealthy", state, errors.New("offline"), false},
		{"dead", &ServerState{PID: 2147483647}, nil, false}, {"missing", nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runningInstance(context.Background(), &mockStateReader{state: tc.state}, &mockHealthChecker{err: tc.err})
			if (got != nil) != tc.want {
				t.Fatalf("state=%v, want live=%v", got, tc.want)
			}
		})
	}
}

func TestActiveEnvClearsDeadSessions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX shell")
	}
	dead := renderActiveEnv("dead:9090", "/dead", 2147483647)
	live := renderActiveEnv("live:9090", "/live", os.Getpid())
	for _, tc := range []struct {
		name    string
		content []byte
		want    string
	}{
		{"single dead", dead, "unset"},
		{"all dead", renderCombinedActiveEnv([][]byte{dead, dead}), "unset"},
		{"fallback", renderCombinedActiveEnv([][]byte{dead, live}), "http://live:9090"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := "export LANGLEY_ACTIVE=1 HTTPS_PROXY=stale\n" + string(tc.content) + "printf '%s' \"${HTTPS_PROXY-unset}\""
			out, err := exec.Command("bash", "-c", script).CombinedOutput()
			if err != nil || string(out) != tc.want {
				t.Fatalf("output=%q err=%v, want %q", out, err, tc.want)
			}
		})
	}
}

func TestPromptHookSync(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires bash")
	}
	path := filepath.Join(t.TempDir(), "active.env")
	writeFile(t, path, string(renderActiveEnv("live:9090", "/ca", os.Getpid())), 0600)
	hook := renderHookBlock(path)
	script := "PROMPT_COMMAND='echo preserved'\n" + hook + hook + `[[ "$PROMPT_COMMAND" == '_langley_sync; echo preserved' ]] || exit 2
[[ "$HTTPS_PROXY" == 'http://live:9090' ]] || exit 3
rm -- ` + shellQuote(path) + `
_langley_sync
[[ -z "${LANGLEY_ACTIVE:-}" && -z "${HTTPS_PROXY:-}" ]] || exit 4
`
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("bash hook sync: %v: %s", err, out)
	}
	if zshBin, err := exec.LookPath("zsh"); err == nil {
		writeFile(t, path, string(renderActiveEnv("live:9090", "/ca", os.Getpid())), 0600)
		zshScript := hook + hook + `(( ${precmd_functions[(Ie)_langley_sync]} == 1 && ${#precmd_functions} == 1 )) || exit 2
[[ "$HTTPS_PROXY" == 'http://live:9090' ]] || exit 3
rm -- ` + shellQuote(path) + `
_langley_sync
[[ -z "${LANGLEY_ACTIVE:-}" && -z "${HTTPS_PROXY:-}" ]] || exit 4
`
		if out, err := exec.Command(zshBin, "-f", "-c", zshScript).CombinedOutput(); err != nil {
			t.Fatalf("zsh hook sync: %v: %s", err, out)
		}
	}
}

func TestAttachInstance(t *testing.T) {
	state := &ServerState{PID: os.Getpid(), APIAddr: "localhost:9091", ProxyAddr: "localhost:9090", CAPath: "/ca"}
	for _, tc := range []struct {
		name, marker, proxy string
		want                int
		banner              string
	}{
		{"already proxied", "1", "http://localhost:9090", 0, "already active in this shell"},
		{"older tab", "", "", 17, "attached to the running instance"},
		{"other proxy", "1", "http://other:9090", 17, "attached to the running instance"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LANGLEY_ACTIVE", tc.marker)
			t.Setenv("HTTPS_PROXY", tc.proxy)
			var stderr bytes.Buffer
			code := attachInstance(context.Background(), state, NewOSShellDetector(), &mockProcessRunner{exitCode: 17}, &ProxyEnvBuilder{}, nil, false, &stderr)
			if code != tc.want || !strings.Contains(stderr.String(), tc.banner) {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
		})
	}
}

func TestSurvivingInstanceDiscovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX sleep")
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	storeA := NewFileStateStoreWithPath(statePath)
	storeB := NewFileStateStoreWithPath(statePath)

	stateA := ServerState{
		ProxyAddr: "localhost:9090",
		APIAddr:   "localhost:9091",
		CAPath:    "/certs/ca.crt",
		PID:       os.Getpid(),
		StartedAt: time.Now().Add(-time.Minute),
	}
	if err := storeA.Write(stateA); err != nil {
		t.Fatalf("storeA.Write: %v", err)
	}

	child := exec.Command("sleep", "5")
	if err := child.Start(); err != nil {
		t.Fatalf("child.Start: %v", err)
	}
	stateB := ServerState{
		ProxyAddr: "localhost:9093",
		APIAddr:   "localhost:9092",
		CAPath:    "/certs/ca.crt",
		PID:       child.Process.Pid,
		StartedAt: time.Now(),
	}
	if err := storeB.Write(stateB); err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatalf("storeB.Write: %v", err)
	}

	_ = child.Process.Kill()
	_ = child.Wait()
	if err := storeB.Delete(); err != nil {
		t.Fatalf("storeB.Delete: %v", err)
	}

	reader := NewFileStateStoreWithPath(statePath)
	got := runningInstance(context.Background(), reader, &mockHealthChecker{err: nil})
	if got == nil {
		t.Fatalf("runningInstance returned nil after B exited while A (PID %d) is still alive", stateA.PID)
	}
	if got.ProxyAddr != stateA.ProxyAddr || got.APIAddr != stateA.APIAddr {
		t.Fatalf("runningInstance = %+v, want %+v", got, stateA)
	}

	t.Setenv("LANGLEY_ACTIVE", "1")
	t.Setenv("HTTPS_PROXY", "http://localhost:9090")
	if !isLangleyEnvActive(reader) {
		t.Fatalf("isLangleyEnvActive returned false for live server A")
	}

	t.Setenv("HTTPS_PROXY", "http://localhost:9093")
	if isLangleyEnvActive(reader) {
		t.Fatalf("isLangleyEnvActive returned true for exited server B")
	}
}

func TestShellEnvAdoptionAcrossSessions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires bash")
	}
	m, configDir, _ := newTestHookManager(t, "/bin/bash")
	envPath := filepath.Join(configDir, "active.env")
	snippet := renderShellEnv(envPath)

	// 1. Activate session 1, verify snippet sets proxy; deactivate, verify _langley_sync unsets;
	//    reactivate session 2, verify _langley_sync adopts new session without re-sourcing RC.
	if err := m.Activate("localhost:19090", "/ca1", os.Getpid()); err != nil {
		t.Fatalf("Activate 1: %v", err)
	}
	env1 := string(readFile(t, envPath))
	if err := m.Deactivate(); err != nil {
		t.Fatalf("Deactivate 1: %v", err)
	}
	if err := m.Activate("localhost:19092", "/ca2", os.Getpid()); err != nil {
		t.Fatalf("Activate 2: %v", err)
	}
	env2 := string(readFile(t, envPath))
	if err := m.Deactivate(); err != nil {
		t.Fatalf("Deactivate 2: %v", err)
	}

	script := snippet + "\n" +
		`[[ -z "${HTTPS_PROXY:-}" && -z "${LANGLEY_ACTIVE:-}" ]] || exit 10` + "\n" +
		"cat << 'EOF' > " + shellQuote(envPath) + "\n" + env1 + "EOF\n" +
		"_langley_sync\n" +
		`[[ "$HTTPS_PROXY" == 'http://localhost:19090' && "$LANGLEY_ACTIVE" == '1' ]] || exit 11` + "\n" +
		"rm -f -- " + shellQuote(envPath) + "\n" +
		"_langley_sync\n" +
		`[[ -z "${HTTPS_PROXY:-}" && -z "${LANGLEY_ACTIVE:-}" ]] || exit 12` + "\n" +
		"cat << 'EOF' > " + shellQuote(envPath) + "\n" + env2 + "EOF\n" +
		"_langley_sync\n" +
		`[[ "$HTTPS_PROXY" == 'http://localhost:19092' && "$LANGLEY_ACTIVE" == '1' ]] || exit 13` + "\n"

	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("shell env adoption across sessions failed: %v: %s", err, out)
	}
}
