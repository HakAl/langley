package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HakAl/langley/internal/config"
)

// --- test doubles ---

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type captureCall struct {
	apiAddr, token string
	enabled, force bool
	isSet          bool
}

type fakeCaptureClient struct {
	calls  []captureCall
	status *CaptureStatus
	err    error
}

func (f *fakeCaptureClient) GetCapture(_ context.Context, apiAddr, token string) (*CaptureStatus, error) {
	f.calls = append(f.calls, captureCall{apiAddr: apiAddr, token: token})
	return f.status, f.err
}

func (f *fakeCaptureClient) SetCapture(_ context.Context, apiAddr, token string, enabled, force bool) (*CaptureStatus, error) {
	f.calls = append(f.calls, captureCall{apiAddr: apiAddr, token: token, enabled: enabled, force: force, isSet: true})
	return f.status, f.err
}

type fakeDaemon struct {
	running    *ServerState
	startState *ServerState
	startErr   error
	stopPIDs   []int
	stopErr    error
	ops        []string
}

func (f *fakeDaemon) Running(context.Context) *ServerState { return f.running }

func (f *fakeDaemon) Start(context.Context) (*ServerState, bool, error) {
	f.ops = append(f.ops, "start")
	if f.startErr != nil {
		return nil, false, f.startErr
	}
	if f.running != nil {
		return f.running, false, nil
	}
	f.running = f.startState
	return f.startState, true, nil
}

func (f *fakeDaemon) Stop(context.Context) ([]int, error) {
	f.ops = append(f.ops, "stop")
	if f.stopErr != nil {
		return f.stopPIDs, f.stopErr
	}
	pids := f.stopPIDs
	if f.running != nil && pids == nil {
		pids = []int{f.running.PID}
	}
	f.running = nil
	return pids, nil
}

type fakeBrowser struct{ urls []string }

func (f *fakeBrowser) Open(u string) error { f.urls = append(f.urls, u); return nil }

type fakeTrust struct {
	uninstalled []string
	err         error
}

func (f *fakeTrust) Commands(caPath string) [][]string {
	return [][]string{{"sudo", "trust-remove", caPath}}
}

func (f *fakeTrust) Uninstall(caPath string) error {
	f.uninstalled = append(f.uninstalled, caPath)
	return f.err
}

type fakeRunner struct{ cmds []string }

func (f *fakeRunner) Run(name string, args ...string) error {
	f.cmds = append(f.cmds, strings.Join(append([]string{name}, args...), " "))
	return nil
}

var testState = &ServerState{ProxyAddr: "127.0.0.1:9090", APIAddr: "127.0.0.1:9091", PID: 4242}

type testCLI struct {
	*DaemonCLI
	daemon  *fakeDaemon
	capture *fakeCaptureClient
	browser *fakeBrowser
	trust   *fakeTrust
	runner  *fakeRunner
	out     *bytes.Buffer
	errOut  *bytes.Buffer
}

func newTestCLI(t *testing.T) *testCLI {
	t.Helper()
	tc := &testCLI{
		daemon:  &fakeDaemon{startState: testState},
		capture: &fakeCaptureClient{},
		browser: &fakeBrowser{},
		trust:   &fakeTrust{},
		runner:  &fakeRunner{},
		out:     &bytes.Buffer{},
		errOut:  &bytes.Buffer{},
	}
	configDir := filepath.Join(t.TempDir(), "langley")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	tc.DaemonCLI = &DaemonCLI{
		daemon:  tc.daemon,
		capture: tc.capture,
		browser: tc.browser,
		trust:   tc.trust,
		runner:  tc.runner,
		loadCfg: func() (*config.Config, error) {
			cfg := config.DefaultConfig()
			cfg.Auth.Token = "tok-123"
			cfg.Persistence.DBPath = filepath.Join(configDir, "langley.db")
			cfg.Proxy.InterceptHosts = []string{"api.anthropic.com", "api.openai.com"}
			return cfg, nil
		},
		configDir: configDir,
		homeDir:   t.TempDir(),
		shellPath: "/bin/zsh",
		goos:      "linux",
		stdin:     strings.NewReader(""),
		stdout:    tc.out,
		stderr:    tc.errOut,
	}
	return tc
}

// --- HTTP capture client ---

func TestHTTPCaptureClient_SetCaptureSendsAuthAndBody(t *testing.T) {
	var gotMethod, gotURL, gotAuth string
	var gotBody map[string]bool
	client := NewHTTPCaptureClientWith(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotMethod, gotURL, gotAuth = r.Method, r.URL.String(), r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decoding body: %v", err)
		}
		return jsonResponse(200, `{"state":"stopping","draining_mitm_requests":2}`), nil
	})})

	st, err := client.SetCapture(context.Background(), "127.0.0.1:9091", "tok", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotURL != "http://127.0.0.1:9091/api/capture" {
		t.Errorf("request = %s %s", gotMethod, gotURL)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBody["enabled"] != false || gotBody["force"] != true {
		t.Errorf("body = %v", gotBody)
	}
	if st.State != captureStopping || st.DrainingMITMRequests != 2 {
		t.Errorf("status = %+v", st)
	}
}

func TestHTTPCaptureClient_ErrorsOnNon200AndMissingState(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"unauthorized", 401, "unauthorized", "401"},
		{"missing state", 200, `{}`, "missing state"},
		{"bad json", 200, `nope`, "decoding"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := NewHTTPCaptureClientWith(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return jsonResponse(tt.status, tt.body), nil
			})})
			_, err := client.GetCapture(context.Background(), "127.0.0.1:9091", "tok")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestHTTPHealthChecker_RejectsForeignService(t *testing.T) {
	for _, tt := range []struct {
		body    string
		wantErr bool
	}{
		{`{"status":"ok","service":"langley"}`, false},
		{`{"status":"ok"}`, false}, // older Langley builds
		{`{"status":"ok","service":"grafana"}`, true},
	} {
		h := &HTTPHealthChecker{client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(200, tt.body), nil
		})}}
		err := h.Check(context.Background(), "127.0.0.1:9091")
		if (err != nil) != tt.wantErr {
			t.Errorf("body %s: err = %v, wantErr %v", tt.body, err, tt.wantErr)
		}
	}
}

// --- pause / resume ---

func TestPause_Draining(t *testing.T) {
	tc := newTestCLI(t)
	tc.daemon.running = testState
	tc.capture.status = &CaptureStatus{State: captureStopping, DrainingMITMRequests: 3}

	if code := tc.Execute(context.Background(), "pause", nil); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, tc.errOut)
	}
	if len(tc.capture.calls) != 1 {
		t.Fatalf("calls = %+v", tc.capture.calls)
	}
	c := tc.capture.calls[0]
	if !c.isSet || c.enabled || c.force || c.token != "tok-123" || c.apiAddr != testState.APIAddr {
		t.Errorf("call = %+v", c)
	}
	if got := tc.out.String(); got != "Stopping capture — finishing 3 active request(s)…\n" {
		t.Errorf("output = %q", got)
	}
	if len(tc.daemon.ops) != 0 {
		t.Errorf("pause must not start/stop the daemon: %v", tc.daemon.ops)
	}
}

func TestPause_ForcePassthrough(t *testing.T) {
	tc := newTestCLI(t)
	tc.daemon.running = testState
	tc.capture.status = &CaptureStatus{State: capturePassthrough}

	if code := tc.Execute(context.Background(), "pause", []string{"--force"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !tc.capture.calls[0].force {
		t.Error("--force not forwarded")
	}
	if got := tc.out.String(); got != "Langley paused (passthrough — TLS payloads are not decrypted, parsed, or stored)\n" {
		t.Errorf("output = %q", got)
	}
}

func TestPause_NotRunning(t *testing.T) {
	tc := newTestCLI(t)
	if code := tc.Execute(context.Background(), "pause", nil); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(tc.capture.calls) != 0 || len(tc.daemon.ops) != 0 {
		t.Errorf("unexpected calls: capture=%v daemon=%v", tc.capture.calls, tc.daemon.ops)
	}
	if !strings.Contains(tc.out.String(), "not running") {
		t.Errorf("output = %q", tc.out.String())
	}
}

func TestPause_APIErrorFails(t *testing.T) {
	tc := newTestCLI(t)
	tc.daemon.running = testState
	tc.capture.err = errors.New("401 unauthorized")
	if code := tc.Execute(context.Background(), "pause", nil); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

func TestResume_StartsDaemonAndEnablesCapture(t *testing.T) {
	tc := newTestCLI(t)
	tc.capture.status = &CaptureStatus{State: captureEnabling, DrainingPassthroughTunnels: 2}

	if code := tc.Execute(context.Background(), "resume", []string{"--force"}); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, tc.errOut)
	}
	if strings.Join(tc.daemon.ops, ",") != "start" {
		t.Errorf("daemon ops = %v", tc.daemon.ops)
	}
	c := tc.capture.calls[0]
	if !c.isSet || !c.enabled || !c.force {
		t.Errorf("call = %+v", c)
	}
	if !strings.Contains(tc.out.String(), "Capturing (2 active connection(s) will switch after current request)") {
		t.Errorf("output = %q", tc.out.String())
	}
}

func TestResume_StartAliasCapturing(t *testing.T) {
	tc := newTestCLI(t)
	tc.daemon.running = testState
	tc.capture.status = &CaptureStatus{State: captureCapturing}

	if code := tc.Execute(context.Background(), "start", nil); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if got := tc.out.String(); got != "Capturing\n" {
		t.Errorf("output = %q", got)
	}
	if tc.capture.calls[0].force {
		t.Error("force should default to false")
	}
}

// --- dashboard ---

func TestDashboard_PreservesCaptureState(t *testing.T) {
	tc := newTestCLI(t)

	if code := tc.Execute(context.Background(), "dashboard", nil); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(tc.capture.calls) != 0 {
		t.Errorf("dashboard must not touch /api/capture: %+v", tc.capture.calls)
	}
	if len(tc.browser.urls) != 1 || tc.browser.urls[0] != "http://127.0.0.1:9091" {
		t.Errorf("opened = %v", tc.browser.urls)
	}
	if !strings.Contains(tc.out.String(), "Dashboard: http://127.0.0.1:9091") ||
		!strings.Contains(tc.out.String(), "passthrough mode") {
		t.Errorf("output = %q", tc.out.String())
	}
}

// --- status ---

func TestStatus_RunningWithDrainingCounts(t *testing.T) {
	tc := newTestCLI(t)
	tc.daemon.running = testState
	tc.capture.status = &CaptureStatus{State: captureStopping, DrainingMITMRequests: 1, InterceptHosts: []string{"api.anthropic.com"}}

	if code := tc.Execute(context.Background(), "status", nil); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	want := "Daemon:  running (PID 4242)\n" +
		"Capture: stopping (finishing 1 active request(s))\n" +
		"Proxy:   http://127.0.0.1:9090\n" +
		"API:     http://127.0.0.1:9091\n" +
		"Scope:   api.anthropic.com\n"
	if got := tc.out.String(); got != want {
		t.Errorf("output =\n%s\nwant\n%s", got, want)
	}
	if c := tc.capture.calls[0]; c.isSet || c.token != "tok-123" {
		t.Errorf("status must GET with token: %+v", c)
	}
}

func TestStatus_Stopped(t *testing.T) {
	tc := newTestCLI(t)
	if code := tc.Execute(context.Background(), "status", nil); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	want := "Daemon:  stopped\nScope:   api.anthropic.com, api.openai.com\n"
	if got := tc.out.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if len(tc.capture.calls) != 0 {
		t.Error("stopped status must not query capture")
	}
}

func TestStatus_CaptureUnavailable(t *testing.T) {
	tc := newTestCLI(t)
	tc.daemon.running = testState
	tc.capture.err = errors.New("404")
	_ = tc.Execute(context.Background(), "status", nil)
	if !strings.Contains(tc.out.String(), "Capture: unknown (404)") {
		t.Errorf("output = %q", tc.out.String())
	}
}

// --- daemon subcommand ---

func TestDaemonCLI_StartStopRestart(t *testing.T) {
	tc := newTestCLI(t)
	ctx := context.Background()

	if code := tc.Execute(ctx, "daemon", []string{"start"}); code != 0 {
		t.Fatalf("start exit = %d", code)
	}
	if !strings.Contains(tc.out.String(), "Daemon started in passthrough mode (PID 4242)") {
		t.Errorf("start output = %q", tc.out.String())
	}

	tc.out.Reset()
	if code := tc.Execute(ctx, "daemon", []string{"start"}); code != 0 {
		t.Fatalf("second start exit = %d", code)
	}
	if !strings.Contains(tc.out.String(), "already running") {
		t.Errorf("second start output = %q", tc.out.String())
	}

	tc.out.Reset()
	tc.daemon.ops = nil
	if code := tc.Execute(ctx, "daemon", []string{"restart"}); code != 0 {
		t.Fatalf("restart exit = %d", code)
	}
	if strings.Join(tc.daemon.ops, ",") != "stop,start" {
		t.Errorf("restart ops = %v", tc.daemon.ops)
	}
	if !strings.Contains(tc.out.String(), "Daemon stopped (PID 4242)") || !strings.Contains(tc.out.String(), "passthrough mode") {
		t.Errorf("restart output = %q", tc.out.String())
	}

	tc.out.Reset()
	if code := tc.Execute(ctx, "daemon", []string{"stop"}); code != 0 {
		t.Fatalf("stop exit = %d", code)
	}
	tc.out.Reset()
	if code := tc.Execute(ctx, "daemon", []string{"stop"}); code != 0 {
		t.Fatalf("idle stop exit = %d", code)
	}
	if got := tc.out.String(); got != "Daemon is not running\n" {
		t.Errorf("idle stop output = %q", got)
	}
	if len(tc.capture.calls) != 0 {
		t.Error("daemon lifecycle must never enable capture")
	}
}

func TestDaemonCLI_UsageErrors(t *testing.T) {
	tc := newTestCLI(t)
	if code := tc.Execute(context.Background(), "daemon", nil); code != 2 {
		t.Errorf("no args exit = %d", code)
	}
	if code := tc.Execute(context.Background(), "daemon", []string{"bogus"}); code != 2 {
		t.Errorf("bogus exit = %d", code)
	}
}

// --- ProcessDaemonController ---

type mapHealth struct {
	mu sync.Mutex
	ok map[string]bool
}

func (h *mapHealth) Check(_ context.Context, apiAddr string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ok[apiAddr] {
		return nil
	}
	return errors.New("unhealthy")
}

type fakeProc struct {
	pid    int
	exited chan struct{}
}

func (p *fakeProc) PID() int                { return p.pid }
func (p *fakeProc) Exited() <-chan struct{} { return p.exited }

type fakeSpawner struct {
	exe     string
	args    []string
	env     []string
	logPath string
	onSpawn func() *fakeProc
}

func (f *fakeSpawner) Spawn(exe string, args, env []string, logPath string) (DaemonProcess, error) {
	f.exe, f.args, f.env, f.logPath = exe, args, env, logPath
	return f.onSpawn(), nil
}

type fakeSignaler struct {
	mu   sync.Mutex
	sent []int
	dead map[int]bool
}

func (f *fakeSignaler) Terminate(pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, pid)
	f.dead[pid] = true
	return nil
}

func (f *fakeSignaler) alive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.dead[pid]
}

func newTestController(t *testing.T) (*ProcessDaemonController, *FileStateStore, *mapHealth, *fakeSpawner, *fakeSignaler) {
	t.Helper()
	dir := t.TempDir()
	store := NewFileStateStoreWithPath(filepath.Join(dir, "state.json"))
	health := &mapHealth{ok: map[string]bool{}}
	sig := &fakeSignaler{dead: map[int]bool{}}
	sp := &fakeSpawner{}
	d := &ProcessDaemonController{
		states:       store,
		remover:      store,
		health:       health,
		spawner:      sp,
		signaler:     sig,
		alive:        sig.alive,
		executable:   func() (string, error) { return "/usr/local/bin/langley", nil },
		logPath:      filepath.Join(dir, "langley.log"),
		readyTimeout: 2 * time.Second,
		stopTimeout:  time.Second,
		poll:         5 * time.Millisecond,
	}
	return d, store, health, sp, sig
}

func TestDaemonStart_SpawnsPassthroughAndWaitsForReady(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9999")
	t.Setenv("LANGLEY_ACTIVE", "1")
	d, store, health, sp, _ := newTestController(t)
	pid := os.Getpid() // a live PID stands in for the child
	sp.onSpawn = func() *fakeProc {
		go func() {
			time.Sleep(20 * time.Millisecond)
			_ = store.Write(ServerState{ProxyAddr: "127.0.0.1:9090", APIAddr: "127.0.0.1:9091", PID: pid, StartedAt: time.Now()})
			health.mu.Lock()
			health.ok["127.0.0.1:9091"] = true
			health.mu.Unlock()
		}()
		return &fakeProc{pid: pid, exited: make(chan struct{})}
	}

	st, started, err := d.Start(context.Background())
	if err != nil || !started || st.PID != pid {
		t.Fatalf("Start = %+v, %v, %v", st, started, err)
	}
	if strings.Join(sp.args, " ") != "-no-shell -no-open -passthrough" {
		t.Errorf("args = %v", sp.args)
	}
	env := strings.Join(sp.env, "\n")
	if !strings.Contains(env, "LANGLEY_START_PASSTHROUGH=1") {
		t.Error("child env missing LANGLEY_START_PASSTHROUGH=1")
	}
	for _, kv := range sp.env {
		if k, _, _ := strings.Cut(kv, "="); k == "HTTPS_PROXY" || k == "LANGLEY_ACTIVE" {
			t.Errorf("child env must not inherit Langley proxy var %s", k)
		}
	}
	if sp.logPath != d.logPath {
		t.Errorf("logPath = %q", sp.logPath)
	}

	// Already running: no second spawn.
	sp.onSpawn = func() *fakeProc { t.Fatal("unexpected second spawn"); return nil }
	if _, started, err := d.Start(context.Background()); err != nil || started {
		t.Errorf("second Start started=%v err=%v", started, err)
	}
}

func TestDaemonStart_ChildExitsEarly(t *testing.T) {
	d, _, _, sp, _ := newTestController(t)
	sp.onSpawn = func() *fakeProc {
		p := &fakeProc{pid: 999999, exited: make(chan struct{})}
		close(p.exited)
		return p
	}
	_, _, err := d.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exited during startup") {
		t.Errorf("err = %v", err)
	}
}

func TestDaemonStart_ReadyTimeout(t *testing.T) {
	d, _, _, sp, _ := newTestController(t)
	d.readyTimeout = 30 * time.Millisecond
	sp.onSpawn = func() *fakeProc { return &fakeProc{pid: 999999, exited: make(chan struct{})} }
	_, _, err := d.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Errorf("err = %v", err)
	}
}

func TestDaemonStop_SignalsVerifiedInstanceAndCleansState(t *testing.T) {
	d, store, health, _, sig := newTestController(t)
	pid := os.Getpid()
	if err := store.Write(ServerState{ProxyAddr: "127.0.0.1:9090", APIAddr: "127.0.0.1:9091", PID: pid, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	health.ok["127.0.0.1:9091"] = true

	pids, err := d.Stop(context.Background())
	if err != nil || len(pids) != 1 || pids[0] != pid {
		t.Fatalf("Stop = %v, %v", pids, err)
	}
	if len(sig.sent) != 1 || sig.sent[0] != pid {
		t.Errorf("signals = %v", sig.sent)
	}
	dir := filepath.Dir(store.path)
	for _, name := range []string{"state.json", "state." + strconv.Itoa(pid) + ".json"} {
		if fileExists(filepath.Join(dir, name)) {
			t.Errorf("%s not removed", name)
		}
	}
}

func TestDaemonStop_SkipsUnverifiedPID(t *testing.T) {
	d, store, _, _, sig := newTestController(t)
	if err := store.Write(ServerState{ProxyAddr: "127.0.0.1:9090", APIAddr: "127.0.0.1:9091", PID: os.Getpid(), StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	pids, err := d.Stop(context.Background())
	if err != nil || len(pids) != 0 || len(sig.sent) != 0 {
		t.Errorf("Stop = %v, %v; signals = %v", pids, err, sig.sent)
	}
}

func TestDaemonStop_TimeoutReportsError(t *testing.T) {
	d, store, health, _, _ := newTestController(t)
	d.alive = func(int) bool { return true } // never exits
	d.stopTimeout = 20 * time.Millisecond
	if err := store.Write(ServerState{ProxyAddr: "127.0.0.1:9090", APIAddr: "127.0.0.1:9091", PID: os.Getpid(), StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	health.ok["127.0.0.1:9091"] = true
	pids, err := d.Stop(context.Background())
	if err == nil || len(pids) != 0 {
		t.Errorf("Stop = %v, %v; want timeout error", pids, err)
	}
	if !fileExists(store.path) {
		t.Error("state must be kept when the process did not exit")
	}
}

// --- uninstall ---

type uninstallFixture struct {
	configDir, home, zshrc, service string
	userRC                          string
}

func setupUninstall(t *testing.T, tc *testCLI) uninstallFixture {
	t.Helper()
	f := uninstallFixture{configDir: tc.configDir, home: tc.homeDir}
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	certs := filepath.Join(f.configDir, "certs")
	for _, name := range []string{"ca.crt", "ca.key", "ca-bundle.pem"} {
		write(filepath.Join(certs, name), "x")
	}
	for _, name := range []string{"langley.log", "langley.db", "langley.db-wal", "active.env", "shell.env", "state.json", "state.77.json", "config.yaml"} {
		write(filepath.Join(f.configDir, name), "x")
	}
	f.userRC = "export PATH=$HOME/bin:$PATH\n"
	f.zshrc = filepath.Join(f.home, ".zshrc")
	write(f.zshrc, f.userRC+renderHookBlock(filepath.Join(f.configDir, "active.env")))
	f.service = filepath.Join(f.home, ".config", "systemd", "user", "langley.service")
	write(f.service, "[Unit]\n")
	return f
}

func TestUninstall_Full(t *testing.T) {
	tc := newTestCLI(t)
	tc.daemon.running = testState
	f := setupUninstall(t, tc)

	if code := tc.Execute(context.Background(), "uninstall", []string{"--yes"}); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, tc.errOut)
	}
	if strings.Join(tc.daemon.ops, ",") != "stop" {
		t.Errorf("daemon ops = %v", tc.daemon.ops)
	}
	if fileExists(f.service) {
		t.Error("systemd unit not removed")
	}
	if len(tc.runner.cmds) != 1 || tc.runner.cmds[0] != "systemctl --user disable --now langley.service" {
		t.Errorf("runner cmds = %v", tc.runner.cmds)
	}
	if got, _ := os.ReadFile(f.zshrc); string(got) != f.userRC {
		t.Errorf(".zshrc = %q, want %q", got, f.userRC)
	}
	for _, rel := range []string{"certs/ca.crt", "certs/ca.key", "certs/ca-bundle.pem", "certs", "langley.log", "langley.db", "langley.db-wal", "active.env", "shell.env", "state.json", "state.77.json"} {
		if fileExists(filepath.Join(f.configDir, rel)) {
			t.Errorf("%s not removed", rel)
		}
	}
	if !fileExists(filepath.Join(f.configDir, "config.yaml")) {
		t.Error("config.yaml should be preserved")
	}
	if len(tc.trust.uninstalled) != 1 || tc.trust.uninstalled[0] != filepath.Join(f.configDir, "certs", "ca.crt") {
		t.Errorf("trust uninstall = %v", tc.trust.uninstalled)
	}
}

func TestUninstall_KeepDataAndNoSystemTrust(t *testing.T) {
	tc := newTestCLI(t)
	f := setupUninstall(t, tc)

	if code := tc.Execute(context.Background(), "uninstall", []string{"--keep-data", "--yes", "--no-system-trust"}); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, tc.errOut)
	}
	for _, rel := range []string{"langley.db", "langley.db-wal"} {
		if !fileExists(filepath.Join(f.configDir, rel)) {
			t.Errorf("%s removed despite --keep-data", rel)
		}
	}
	if fileExists(filepath.Join(f.configDir, "certs", "ca.crt")) {
		t.Error("ca.crt not removed")
	}
	if len(tc.trust.uninstalled) != 0 {
		t.Error("--no-system-trust must not run the trust-store command")
	}
	if !strings.Contains(tc.out.String(), "sudo trust-remove") {
		t.Errorf("trust command not printed: %q", tc.out.String())
	}
}

func TestUninstall_DeclinedPromptChangesNothing(t *testing.T) {
	tc := newTestCLI(t)
	tc.stdin = strings.NewReader("n\n")
	f := setupUninstall(t, tc)

	if code := tc.Execute(context.Background(), "uninstall", nil); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if len(tc.daemon.ops) != 0 || !fileExists(filepath.Join(f.configDir, "certs", "ca.crt")) || !fileExists(f.service) {
		t.Error("declined uninstall must not change anything")
	}
}

func TestUninstall_TrustFailureReported(t *testing.T) {
	tc := newTestCLI(t)
	tc.trust.err = errors.New("sudo: a password is required")
	setupUninstall(t, tc)
	if code := tc.Execute(context.Background(), "uninstall", []string{"--yes"}); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(tc.errOut.String(), "system trust store") {
		t.Errorf("stderr = %q", tc.errOut.String())
	}
}

func TestOSTrustStoreUninstaller_Commands(t *testing.T) {
	r := &fakeRunner{}
	u := &OSTrustStoreUninstaller{goos: "linux", runner: r}
	if err := u.Uninstall("/x/ca.crt"); err != nil {
		t.Fatal(err)
	}
	want := []string{"sudo rm -f /usr/local/share/ca-certificates/langley.crt", "sudo update-ca-certificates"}
	if strings.Join(r.cmds, "|") != strings.Join(want, "|") {
		t.Errorf("cmds = %v", r.cmds)
	}
	darwin := &OSTrustStoreUninstaller{goos: "darwin"}
	if c := darwin.Commands(filepath.Join(t.TempDir(), "missing.crt")); strings.Join(c[0], " ") != "sudo security delete-certificate -c Langley CA /Library/Keychains/System.keychain" {
		t.Errorf("darwin fallback = %v", c)
	}
}

// --- bundle path resolution ---

func TestBundlePathForCA(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.crt")
	if got := bundlePathForCA(caPath); got != caPath {
		t.Errorf("without bundle (non-existent path) = %q, want synthetic path fallback", got)
	}
	if err := os.WriteFile(caPath, []byte("ca-only"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := bundlePathForCA(caPath); got != "" {
		t.Errorf("real ca.crt without ca-bundle.pem = %q, want empty string so SSL_CERT_FILE/REQUESTS_CA_BUNDLE are omitted", got)
	}
	noBundleEnv := strings.Join((&ProxyEnvBuilder{}).Build("127.0.0.1:9090", caPath), "\n")
	if strings.Contains(noBundleEnv, "SSL_CERT_FILE=") || strings.Contains(noBundleEnv, "REQUESTS_CA_BUNDLE=") {
		t.Errorf("ProxyEnvBuilder exported CA-only bundle when ca-bundle.pem is missing:\n%s", noBundleEnv)
	}
	noBundleActive := string(renderActiveEnv("127.0.0.1:9090", caPath, 1))
	if strings.Contains(noBundleActive, "SSL_CERT_FILE=") || strings.Contains(noBundleActive, "REQUESTS_CA_BUNDLE=") {
		t.Errorf("renderActiveEnv exported CA-only bundle when ca-bundle.pem is missing:\n%s", noBundleActive)
	}
	noBundlePrinted := formatEnvVars("127.0.0.1:9090", caPath, "linux")
	if strings.Contains(noBundlePrinted, "SSL_CERT_FILE=") || strings.Contains(noBundlePrinted, "REQUESTS_CA_BUNDLE=") {
		t.Errorf("formatEnvVars exported CA-only bundle when ca-bundle.pem is missing:\n%s", noBundlePrinted)
	}

	if got := bundlePathForCA(""); got != "" {
		t.Errorf("empty = %q", got)
	}
	bundle := filepath.Join(dir, "ca-bundle.pem")
	if err := os.WriteFile(bundle, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := bundlePathForCA(caPath); got != bundle {
		t.Errorf("with bundle = %q, want %q", got, bundle)
	}

	env := map[string]string{}
	for _, kv := range (&ProxyEnvBuilder{}).Build("127.0.0.1:9090", caPath) {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	if env["NODE_EXTRA_CA_CERTS"] != caPath || env["SSL_CERT_FILE"] != bundle || env["REQUESTS_CA_BUNDLE"] != bundle {
		t.Errorf("env CA vars = %q / %q / %q", env["NODE_EXTRA_CA_CERTS"], env["SSL_CERT_FILE"], env["REQUESTS_CA_BUNDLE"])
	}
	if env["NO_PROXY"] != noProxyHosts || env["no_proxy"] != noProxyHosts {
		t.Errorf("NO_PROXY = %q / %q", env["NO_PROXY"], env["no_proxy"])
	}

	active := string(renderActiveEnv("127.0.0.1:9090", caPath, 1))
	for _, want := range []string{
		"export NODE_EXTRA_CA_CERTS=" + shellQuote(caPath),
		"export SSL_CERT_FILE=" + shellQuote(bundle),
		"export REQUESTS_CA_BUNDLE=" + shellQuote(bundle),
		"export NO_PROXY=" + shellQuote(noProxyHosts),
	} {
		if !strings.Contains(active, want) {
			t.Errorf("active.env missing %q", want)
		}
	}
	printed := formatEnvVars("127.0.0.1:9090", caPath, "linux")
	if !strings.Contains(printed, "export SSL_CERT_FILE="+bundle) || !strings.Contains(printed, "export NODE_EXTRA_CA_CERTS="+caPath) {
		t.Errorf("formatEnvVars = %q", printed)
	}
}

func TestDaemonCLI_LiveSubprocessLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live subprocess daemon lifecycle test in -short mode")
	}

	binPath := filepath.Join(t.TempDir(), "langley-test-bin")
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}

	homeDir := t.TempDir()
	configDir := filepath.Join(homeDir, ".config", "langley")
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(homeDir, ".config"))
	t.Setenv("APPDATA", configDir)
	t.Setenv("LANGLEY_LISTEN", "127.0.0.1:0")

	store := NewFileStateStoreWithPath(filepath.Join(configDir, "state.json"))
	controller := NewProcessDaemonController(store, configDir)
	controller.executable = func() (string, error) { return binPath, nil }
	t.Cleanup(func() {
		_, _ = controller.Stop(context.Background())
	})

	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	browser := &fakeBrowser{}
	cli := &DaemonCLI{
		daemon:    controller,
		capture:   NewHTTPCaptureClient(),
		browser:   browser,
		trust:     &fakeTrust{},
		runner:    &fakeRunner{},
		loadCfg:   func() (*config.Config, error) { return config.Load("") },
		configDir: configDir,
		homeDir:   homeDir,
		shellPath: "/bin/zsh",
		goos:      "linux",
		stdin:     strings.NewReader(""),
		stdout:    out,
		stderr:    errOut,
	}

	ctx := context.Background()

	// 1. daemon start -> starts in passthrough mode
	if code := cli.Execute(ctx, "daemon", []string{"start"}); code != 0 {
		t.Fatalf("daemon start exit = %d, stderr = %s", code, errOut.String())
	}
	st := controller.Running(ctx)
	if st == nil {
		t.Fatal("expected daemon to be running after 'daemon start'")
	}

	// Verify /api/health returns service="langley" and capture_state="passthrough"
	resp, err := http.Get("http://" + st.APIAddr + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	var healthBody struct {
		Status       string `json:"status"`
		Service      string `json:"service"`
		CaptureState string `json:"capture_state"`
		PID          int    `json:"pid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&healthBody); err != nil {
		_ = resp.Body.Close()
		t.Fatalf("decode /api/health: %v", err)
	}
	_ = resp.Body.Close()
	if healthBody.Service != "langley" || healthBody.CaptureState != capturePassthrough || healthBody.PID != st.PID {
		t.Fatalf("unexpected /api/health = %+v (want service=langley, capture_state=passthrough, pid=%d)", healthBody, st.PID)
	}

	// 2. dashboard -> opens browser and preserves passthrough
	out.Reset()
	if code := cli.Execute(ctx, "dashboard", nil); code != 0 {
		t.Fatalf("dashboard exit = %d, stderr = %s", code, errOut.String())
	}
	out.Reset()
	if code := cli.Execute(ctx, "status", nil); code != 0 {
		t.Fatalf("status exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Capture: passthrough") {
		t.Fatalf("expected status after dashboard to remain passthrough, got:\n%s", out.String())
	}

	// 3. resume -> enables capture
	out.Reset()
	if code := cli.Execute(ctx, "resume", nil); code != 0 {
		t.Fatalf("resume exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Capturing") {
		t.Fatalf("expected resume output to contain Capturing, got %q", out.String())
	}
	out.Reset()
	if code := cli.Execute(ctx, "status", nil); code != 0 {
		t.Fatalf("status exit = %d", code)
	}
	if !strings.Contains(out.String(), "Capture: capturing") {
		t.Fatalf("expected status to report capturing, got:\n%s", out.String())
	}

	// 4. pause -> returns to passthrough without stopping daemon
	out.Reset()
	if code := cli.Execute(ctx, "pause", nil); code != 0 {
		t.Fatalf("pause exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Langley paused (passthrough") {
		t.Fatalf("expected pause output, got %q", out.String())
	}
	out.Reset()
	if code := cli.Execute(ctx, "status", nil); code != 0 {
		t.Fatalf("status exit = %d", code)
	}
	if !strings.Contains(out.String(), "Capture: passthrough") {
		t.Fatalf("expected status to report passthrough after pause, got:\n%s", out.String())
	}

	// 5. Enable capture again, then daemon restart -> must reset to passthrough
	if code := cli.Execute(ctx, "resume", nil); code != 0 {
		t.Fatalf("resume before restart exit = %d", code)
	}
	out.Reset()
	if code := cli.Execute(ctx, "daemon", []string{"restart"}); code != 0 {
		t.Fatalf("daemon restart exit = %d, stderr = %s", code, errOut.String())
	}
	out.Reset()
	if code := cli.Execute(ctx, "status", nil); code != 0 {
		t.Fatalf("status after restart exit = %d", code)
	}
	if !strings.Contains(out.String(), "Capture: passthrough") {
		t.Fatalf("expected daemon restart to reset capture to passthrough, got:\n%s", out.String())
	}

	// 6. daemon stop -> stops daemon
	out.Reset()
	if code := cli.Execute(ctx, "daemon", []string{"stop"}); code != 0 {
		t.Fatalf("daemon stop exit = %d, stderr = %s", code, errOut.String())
	}
	if controller.Running(ctx) != nil {
		t.Fatal("expected daemon to be stopped after 'daemon stop'")
	}
}

func TestDaemonStop_RefusesForeignPIDAndMismatchedInstance(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStateStoreWithPath(filepath.Join(dir, "state.json"))
	sig := &fakeSignaler{dead: map[int]bool{}}

	victim := exec.Command("sleep", "60")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = victim.Process.Kill()
		_ = victim.Wait()
	}()
	victimPID := victim.Process.Pid

	checker := &HTTPHealthChecker{
		client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{"status":"ok","service":"langley","instance_id":"inst-real","pid":12345,"proxy_addr":"127.0.0.1:9090","api_addr":"127.0.0.1:9091"}`), nil
		})},
	}
	d := &ProcessDaemonController{
		states:      store,
		remover:     store,
		health:      checker,
		signaler:    sig,
		alive:       sig.alive,
		stopTimeout: 500 * time.Millisecond,
		poll:        5 * time.Millisecond,
	}

	// Forged state file pointing at victimPID while health reports PID 12345.
	if err := store.Write(ServerState{
		ProxyAddr:  "127.0.0.1:9090",
		APIAddr:    "127.0.0.1:9091",
		PID:        victimPID,
		InstanceID: "inst-real",
		StartedAt:  time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	pids, err := d.Stop(context.Background())
	if err != nil || len(pids) != 0 || len(sig.sent) != 0 {
		t.Fatalf("Stop signaled foreign PID: pids=%v err=%v sent=%v", pids, err, sig.sent)
	}

	// Forged state file with matching PID but mismatched InstanceID.
	if err := store.Write(ServerState{
		ProxyAddr:  "127.0.0.1:9090",
		APIAddr:    "127.0.0.1:9091",
		PID:        12345,
		InstanceID: "inst-stale",
		StartedAt:  time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	pids, err = d.Stop(context.Background())
	if err != nil || len(pids) != 0 || len(sig.sent) != 0 {
		t.Fatalf("Stop signaled mismatched instance: pids=%v err=%v sent=%v", pids, err, sig.sent)
	}
}

func TestDefaultStart_StartsDaemonEnablesCaptureAndOpensBrowser(t *testing.T) {
	tc := newTestCLI(t)
	tc.capture.status = &CaptureStatus{State: captureCapturing, InterceptHosts: []string{"api.anthropic.com"}}

	if code := tc.DefaultStart(context.Background(), false, true, false); code != 0 {
		t.Fatalf("DefaultStart exit = %d, stderr = %s", code, tc.errOut.String())
	}
	if strings.Join(tc.daemon.ops, ",") != "start" {
		t.Fatalf("daemon ops = %v, want [start]", tc.daemon.ops)
	}
	if len(tc.capture.calls) != 1 || !tc.capture.calls[0].isSet || !tc.capture.calls[0].enabled {
		t.Fatalf("capture calls = %+v, want SetCapture(enabled=true)", tc.capture.calls)
	}
	if len(tc.browser.urls) != 1 || tc.browser.urls[0] != "http://127.0.0.1:9091" {
		t.Fatalf("browser urls = %v, want [http://127.0.0.1:9091]", tc.browser.urls)
	}
	if !strings.Contains(tc.out.String(), "Capturing") {
		t.Fatalf("stdout = %q, want Capturing status", tc.out.String())
	}
}

func TestDefaultStart_PassthroughTransitionsExistingCapturingDaemon(t *testing.T) {
	tc := newTestCLI(t)
	tc.daemon.running = testState
	tc.capture.status = &CaptureStatus{State: captureStopping, DrainingMITMRequests: 1}

	if code := tc.DefaultStart(context.Background(), false, false, true); code != 0 {
		t.Fatalf("DefaultStart(--passthrough) exit = %d, stderr = %s", code, tc.errOut.String())
	}
	if len(tc.capture.calls) != 1 || !tc.capture.calls[0].isSet || tc.capture.calls[0].enabled {
		t.Fatalf("expected SetCapture(enabled=false) when --passthrough reuses running daemon, got %+v", tc.capture.calls)
	}
	if !strings.Contains(tc.out.String(), "Stopping capture — finishing 1 active request(s)…") {
		t.Fatalf("expected truthful stopping status output, got %q", tc.out.String())
	}
}

func TestSetup_CreatesCombinedBundleServiceAndStartsPassthroughDaemon(t *testing.T) {
	tc := newTestCLI(t)
	zshrc := filepath.Join(tc.homeDir, ".zshrc")
	if err := os.WriteFile(zshrc, []byte("# existing user rc\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if code := tc.Setup(context.Background(), []string{"--manual"}); code != 0 {
		t.Fatalf("Setup exit = %d, stderr = %s", code, tc.errOut.String())
	}
	for _, rel := range []string{"certs/ca.crt", "certs/ca.key", "certs/ca-bundle.pem", "active.env", "shell.env"} {
		if !fileExists(filepath.Join(tc.configDir, rel)) {
			t.Errorf("Setup did not create %s", rel)
		}
	}
	servicePath := filepath.Join(tc.homeDir, ".config", "systemd", "user", "langley.service")
	if !fileExists(servicePath) {
		t.Errorf("Setup did not create service unit %s", servicePath)
	}
	rcData, err := os.ReadFile(zshrc)
	if err != nil || !strings.Contains(string(rcData), hookStartMarker) {
		t.Errorf(".zshrc missing hook block: %q (%v)", string(rcData), err)
	}
	if strings.Join(tc.daemon.ops, ",") != "start" {
		t.Errorf("daemon ops = %v, want [start]", tc.daemon.ops)
	}
	if len(tc.capture.calls) != 0 {
		t.Errorf("Setup must keep daemon in passthrough without enabling capture, got calls = %+v", tc.capture.calls)
	}
}

type failingRunner struct {
	err error
}

func (f *failingRunner) Run(string, ...string) error { return f.err }

func TestSetup_PropagatesServiceActivationFailure(t *testing.T) {
	tc := newTestCLI(t)
	tc.runner = &fakeRunner{}
	tc.DaemonCLI.runner = &failingRunner{err: errors.New("systemctl --user failed")}
	if code := tc.Setup(context.Background(), []string{"--manual"}); code != 1 {
		t.Fatalf("Setup exit = %d, want 1 when service activation fails", code)
	}
	if !strings.Contains(tc.errOut.String(), "service installation failed") {
		t.Fatalf("stderr = %q, want service installation failed", tc.errOut.String())
	}
}

func TestUninstall_AbortsWhenDaemonStopFails(t *testing.T) {
	tc := newTestCLI(t)
	tc.daemon.running = testState
	tc.daemon.stopErr = errors.New("daemon PID 4242 did not exit within 5s")
	f := setupUninstall(t, tc)

	if code := tc.Execute(context.Background(), "uninstall", []string{"--yes"}); code != 1 {
		t.Fatalf("Uninstall exit = %d, want 1 when Stop fails", code)
	}
	for _, path := range []string{
		f.service,
		filepath.Join(f.configDir, "state.json"),
		filepath.Join(f.configDir, "certs", "ca.crt"),
		filepath.Join(f.configDir, "active.env"),
	} {
		if !fileExists(path) {
			t.Errorf("Uninstall deleted %s despite daemon still running", path)
		}
	}
	if got, _ := os.ReadFile(f.zshrc); !strings.Contains(string(got), hookStartMarker) {
		t.Error("Uninstall stripped .zshrc hook despite daemon still running")
	}
}

func TestUninstall_PreservesUnverifiableLiveDaemon(t *testing.T) {
	tc := newTestCLI(t)
	f := setupUninstall(t, tc)

	// Write a valid ServerState whose PID is alive (current process PID), while
	// daemon.Stop() returns 0 stopped PIDs and nil error (simulating an unreachable/unverified /api/health).
	store := NewFileStateStoreWithPath(filepath.Join(f.configDir, "state.json"))
	if err := store.Write(ServerState{
		InstanceID: "unverified-live",
		ProxyAddr:  "127.0.0.1:9090",
		APIAddr:    "127.0.0.1:9091",
		PID:        os.Getpid(),
		StartedAt:  time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	if code := tc.Execute(context.Background(), "uninstall", []string{"--yes"}); code != 1 {
		t.Fatalf("Uninstall exit = %d, want 1 when a live daemon PID could not be verified/stopped", code)
	}
	for _, path := range []string{
		f.service,
		filepath.Join(f.configDir, "state.json"),
		filepath.Join(f.configDir, "certs", "ca.crt"),
		filepath.Join(f.configDir, "active.env"),
	} {
		if !fileExists(path) {
			t.Errorf("Uninstall deleted %s despite unverifiable live daemon PID", path)
		}
	}
	if got, _ := os.ReadFile(f.zshrc); !strings.Contains(string(got), hookStartMarker) {
		t.Error("Uninstall stripped .zshrc hook despite unverifiable live daemon PID")
	}
}
