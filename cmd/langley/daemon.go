package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/HakAl/langley/internal/config"
	langleytls "github.com/HakAl/langley/internal/tls"
)

// Capture states reported by GET/POST /api/capture.
const (
	capturePassthrough = "passthrough"
	captureEnabling    = "enabling"
	captureCapturing   = "capturing"
	captureStopping    = "stopping"
)

const (
	daemonReadyTimeout = 5 * time.Second
	daemonStopTimeout  = 5 * time.Second
	daemonPollInterval = 100 * time.Millisecond
)

// CaptureStatus mirrors the /api/capture response body.
type CaptureStatus struct {
	State                      string   `json:"state"`
	DrainingPassthroughTunnels int      `json:"draining_passthrough_tunnels"`
	DrainingMITMRequests       int      `json:"draining_mitm_requests"`
	InterceptHosts             []string `json:"intercept_hosts"`
	ProxyAddr                  string   `json:"proxy_addr"`
}

// CaptureClient reads and changes a running daemon's capture state.
type CaptureClient interface {
	GetCapture(ctx context.Context, apiAddr, token string) (*CaptureStatus, error)
	SetCapture(ctx context.Context, apiAddr, token string, enabled, force bool) (*CaptureStatus, error)
}

// DaemonController finds, starts, and stops the background daemon.
type DaemonController interface {
	// Running returns the verified running instance, or nil.
	Running(ctx context.Context) *ServerState
	// Start launches the daemon in passthrough mode unless one is already
	// running; started reports whether a new process was launched.
	Start(ctx context.Context) (state *ServerState, started bool, err error)
	// Stop terminates every verified running instance and returns their PIDs.
	Stop(ctx context.Context) ([]int, error)
}

// TrustStoreUninstaller removes the Langley CA from the OS trust store.
type TrustStoreUninstaller interface {
	// Commands returns the shell commands that remove the CA (for display).
	Commands(caPath string) [][]string
	Uninstall(caPath string) error
}

// CommandRunner runs an external command attached to the user's terminal.
type CommandRunner interface {
	Run(name string, args ...string) error
}

// StateRemover deletes the state records for a stopped instance.
type StateRemover interface {
	DeletePID(pid int) error
}

// DeletePID removes the state records for pid, restoring state.json to any
// surviving instance.
func (s *FileStateStore) DeletePID(pid int) error {
	s.lastPID = pid
	return s.Delete()
}

// --- HTTP capture client ---

// HTTPCaptureClient talks to /api/capture over HTTP.
type HTTPCaptureClient struct {
	client *http.Client
}

// NewHTTPCaptureClient creates a capture client with a request timeout.
func NewHTTPCaptureClient() *HTTPCaptureClient {
	return NewHTTPCaptureClientWith(&http.Client{Timeout: 10 * time.Second})
}

// NewHTTPCaptureClientWith creates a capture client using the given HTTP client.
func NewHTTPCaptureClientWith(client *http.Client) *HTTPCaptureClient {
	return &HTTPCaptureClient{client: client}
}

// GetCapture returns the current capture state.
func (c *HTTPCaptureClient) GetCapture(ctx context.Context, apiAddr, token string) (*CaptureStatus, error) {
	return c.do(ctx, http.MethodGet, apiAddr, token, nil)
}

// SetCapture requests a capture state transition.
func (c *HTTPCaptureClient) SetCapture(ctx context.Context, apiAddr, token string, enabled, force bool) (*CaptureStatus, error) {
	body, err := json.Marshal(struct {
		Enabled bool `json:"enabled"`
		Force   bool `json:"force"`
	}{enabled, force})
	if err != nil {
		return nil, err
	}
	return c.do(ctx, http.MethodPost, apiAddr, token, body)
}

func (c *HTTPCaptureClient) do(ctx context.Context, method, apiAddr, token string, body []byte) (*CaptureStatus, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+apiAddr+"/api/capture", rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading capture response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s /api/capture returned %d: %s", method, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var st CaptureStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("decoding capture response: %w", err)
	}
	if st.State == "" {
		return nil, errors.New("capture response missing state")
	}
	return &st, nil
}

// --- Process daemon controller ---

// DaemonProcess is a launched daemon child.
type DaemonProcess interface {
	PID() int
	// Exited is closed when the child process exits.
	Exited() <-chan struct{}
}

// DaemonSpawner launches the daemon process detached from the terminal.
type DaemonSpawner interface {
	Spawn(exe string, args, env []string, logPath string) (DaemonProcess, error)
}

// ProcessSignaler asks a process to shut down gracefully.
type ProcessSignaler interface {
	Terminate(pid int) error
}

// ProcessDaemonController manages the daemon as a detached child process.
type ProcessDaemonController struct {
	states       StateReader
	remover      StateRemover
	health       HealthChecker
	spawner      DaemonSpawner
	signaler     ProcessSignaler
	alive        func(pid int) bool
	executable   func() (string, error)
	logPath      string
	readyTimeout time.Duration
	stopTimeout  time.Duration
	poll         time.Duration
}

// NewProcessDaemonController creates a controller with production dependencies.
func NewProcessDaemonController(store *FileStateStore, configDir string) *ProcessDaemonController {
	return &ProcessDaemonController{
		states:       store,
		remover:      store,
		health:       &HTTPHealthChecker{},
		spawner:      execDaemonSpawner{},
		signaler:     osProcessSignaler{},
		alive:        isProcessAlive,
		executable:   os.Executable,
		logPath:      filepath.Join(configDir, "langley.log"),
		readyTimeout: daemonReadyTimeout,
		stopTimeout:  daemonStopTimeout,
		poll:         daemonPollInterval,
	}
}

// Running returns the verified running instance, or nil.
func (d *ProcessDaemonController) Running(ctx context.Context) *ServerState {
	return runningInstance(ctx, d.states, d.health)
}

// Start launches `<exe> -no-shell -no-open -passthrough` in the background and
// waits for its state file and health endpoint.
func (d *ProcessDaemonController) Start(ctx context.Context) (*ServerState, bool, error) {
	if st := d.Running(ctx); st != nil {
		return st, false, nil
	}
	exe, err := d.executable()
	if err != nil {
		return nil, false, fmt.Errorf("locating langley executable: %w", err)
	}
	env := append(daemonBaseEnv(os.Environ()), "LANGLEY_DAEMON=1", "LANGLEY_START_PASSTHROUGH=1", "LANGLEY_NO_SHELL=1", "LANGLEY_NO_OPEN=1")
	proc, err := d.spawner.Spawn(exe, []string{"-no-shell", "-no-open", "-passthrough"}, env, d.logPath)
	if err != nil {
		return nil, false, fmt.Errorf("starting daemon: %w", err)
	}

	deadline := time.Now().Add(d.readyTimeout)
	for {
		if st := d.Running(ctx); st != nil && st.PID == proc.PID() {
			return st, true, nil
		}
		select {
		case <-proc.Exited():
			return nil, false, fmt.Errorf("daemon exited during startup; see %s", d.logPath)
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-time.After(d.poll):
		}
		if time.Now().After(deadline) {
			return nil, false, fmt.Errorf("daemon (PID %d) not ready after %s; see %s", proc.PID(), d.readyTimeout, d.logPath)
		}
	}
}

// Stop sends SIGTERM to every health-verified instance, waits for exit, and
// removes its state records. Live PIDs that fail the health check are left
// alone since the PID may have been reused by an unrelated process.
func (d *ProcessDaemonController) Stop(ctx context.Context) ([]int, error) {
	var stopped []int
	var errs []error
	for _, st := range candidateStates(d.states) {
		if st.PID <= 0 || !d.alive(st.PID) {
			continue
		}
		healthCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		err := checkStateHealth(healthCtx, d.health, st)
		cancel()
		if err != nil {
			continue
		}
		if err := d.signaler.Terminate(st.PID); err != nil {
			errs = append(errs, fmt.Errorf("signaling PID %d: %w", st.PID, err))
			continue
		}
		if !d.waitExit(ctx, st.PID) {
			errs = append(errs, fmt.Errorf("PID %d did not exit within %s", st.PID, d.stopTimeout))
			continue
		}
		if err := d.remover.DeletePID(st.PID); err != nil {
			errs = append(errs, fmt.Errorf("removing state for PID %d: %w", st.PID, err))
		}
		stopped = append(stopped, st.PID)
	}
	return stopped, errors.Join(errs...)
}

func (d *ProcessDaemonController) waitExit(ctx context.Context, pid int) bool {
	deadline := time.Now().Add(d.stopTimeout)
	for d.alive(pid) {
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d.poll):
		}
	}
	return true
}

// daemonBaseEnv drops proxy/CA variables inherited from a Langley shell so
// the daemon never routes its own upstream traffic through itself.
func daemonBaseEnv(environ []string) []string {
	drop := map[string]bool{}
	for _, k := range strings.Fields(strings.TrimPrefix(unsetProxyEnv, "unset ")) {
		drop[strings.ToUpper(k)] = true
	}
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[strings.ToUpper(k)] {
			out = append(out, kv)
		}
	}
	return out
}

type execDaemonSpawner struct{}

type execDaemonProcess struct {
	pid    int
	exited chan struct{}
}

func (p *execDaemonProcess) PID() int                { return p.pid }
func (p *execDaemonProcess) Exited() <-chan struct{} { return p.exited }

// Spawn starts exe in a new session with output appended to logPath. The
// reaper goroutine ends when the child exits or this CLI process exits.
func (execDaemonSpawner) Spawn(exe string, args, env []string, logPath string) (DaemonProcess, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	defer func() { _ = logFile.Close() }() // the child keeps its own descriptor

	cmd := exec.Command(exe, args...)
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &execDaemonProcess{pid: cmd.Process.Pid, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.exited)
	}()
	return p, nil
}

type osProcessSignaler struct{}

func (osProcessSignaler) Terminate(pid int) error { return terminateProcess(pid) }

// --- Trust store ---

// OSTrustStoreUninstaller removes the Langley CA from the platform trust store.
type OSTrustStoreUninstaller struct {
	goos   string
	runner CommandRunner
}

// NewOSTrustStoreUninstaller creates an uninstaller for the current platform.
func NewOSTrustStoreUninstaller(runner CommandRunner) *OSTrustStoreUninstaller {
	return &OSTrustStoreUninstaller{goos: runtime.GOOS, runner: runner}
}

// Commands returns the removal commands for the platform.
func (u *OSTrustStoreUninstaller) Commands(caPath string) [][]string {
	switch u.goos {
	case "darwin":
		if hash := certSHA1(caPath); hash != "" {
			return [][]string{{"sudo", "security", "delete-certificate", "-Z", hash, "/Library/Keychains/System.keychain"}}
		}
		return [][]string{{"sudo", "security", "delete-certificate", "-c", "Langley CA", "/Library/Keychains/System.keychain"}}
	case "windows":
		return [][]string{{"certutil", "-delstore", "ROOT", "Langley CA"}}
	default:
		return [][]string{
			{"sudo", "rm", "-f", "/usr/local/share/ca-certificates/langley.crt"},
			{"sudo", "update-ca-certificates"},
		}
	}
}

// Uninstall runs the removal commands, stopping at the first failure.
func (u *OSTrustStoreUninstaller) Uninstall(caPath string) error {
	for _, c := range u.Commands(caPath) {
		if err := u.runner.Run(c[0], c[1:]...); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(c, " "), err)
		}
	}
	return nil
}

// certSHA1 returns the uppercase hex SHA-1 of the PEM certificate at path, or "".
func certSHA1(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return ""
	}
	sum := sha1.Sum(block.Bytes)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// TerminalCommandRunner runs commands attached to the user's terminal.
type TerminalCommandRunner struct{}

// Run executes name with args, inheriting stdio.
func (TerminalCommandRunner) Run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// --- CLI ---

// DaemonCLI implements pause/resume/dashboard/status/daemon/uninstall.
type DaemonCLI struct {
	daemon    DaemonController
	capture   CaptureClient
	browser   BrowserOpener
	trust     TrustStoreUninstaller
	runner    CommandRunner
	loadCfg   func() (*config.Config, error)
	configDir string
	homeDir   string
	shellPath string
	goos      string
	stdin     io.Reader
	stdout    io.Writer
	stderr    io.Writer
}

// NewDaemonCLI creates a DaemonCLI with production dependencies.
func NewDaemonCLI() (*DaemonCLI, error) {
	configDir, err := config.ConfigDir()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	store := NewFileStateStoreWithPath(filepath.Join(configDir, "state.json"))
	runner := TerminalCommandRunner{}
	return &DaemonCLI{
		daemon:    NewProcessDaemonController(store, configDir),
		capture:   NewHTTPCaptureClient(),
		browser:   NewOSBrowserOpener(),
		trust:     NewOSTrustStoreUninstaller(runner),
		runner:    runner,
		loadCfg:   func() (*config.Config, error) { return config.Load("") },
		configDir: configDir,
		homeDir:   home,
		shellPath: os.Getenv("SHELL"),
		goos:      runtime.GOOS,
		stdin:     os.Stdin,
		stdout:    os.Stdout,
		stderr:    os.Stderr,
	}, nil
}

// handleDaemonCLICommand is the entry point for the daemon-related subcommands.
func handleDaemonCLICommand(name string, args []string) {
	cli, err := NewDaemonCLI()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	os.Exit(cli.Execute(context.Background(), name, args))
}

// Execute dispatches a subcommand and returns the exit code.
func (c *DaemonCLI) Execute(ctx context.Context, name string, args []string) int {
	switch name {
	case "pause":
		return c.Pause(ctx, args)
	case "resume", "start":
		return c.Resume(ctx, name, args)
	case "dashboard":
		return c.Dashboard(ctx)
	case "status":
		return c.Status(ctx)
	case "daemon":
		return c.Daemon(ctx, args)
	case "setup":
		return c.Setup(ctx, args)
	case "uninstall":
		return c.Uninstall(ctx, args)
	}
	fmt.Fprintf(c.stderr, "Unknown command: %s\n", name)
	return 1
}

// DefaultStart implements the bare `langley` invocation: ensures the background
// daemon is running, enables capture (unless startPassthrough is true), opens
// the dashboard (when openBrowser is true), prints status, and returns immediately.
func (c *DaemonCLI) DefaultStart(ctx context.Context, force, openBrowser, startPassthrough bool) int {
	st, started, err := c.daemon.Start(ctx)
	if err != nil {
		fmt.Fprintf(c.stderr, "Error: %v\n", err)
		return 1
	}
	if started {
		fmt.Fprintf(c.stdout, "Daemon started (PID %d)\n", st.PID)
	}
	if !startPassthrough {
		cs, err := c.capture.SetCapture(ctx, st.APIAddr, c.token(), true, force)
		if err != nil {
			fmt.Fprintf(c.stderr, "Error: failed to enable capture: %v\n", err)
			return 1
		}
		fmt.Fprintln(c.stdout, captureSummary(cs))
	} else {
		var cs *CaptureStatus
		if !started && c.capture != nil {
			cs, err = c.capture.SetCapture(ctx, st.APIAddr, c.token(), false, force)
			if err != nil {
				fmt.Fprintf(c.stderr, "Error: failed to pause capture: %v\n", err)
				return 1
			}
		} else if c.capture != nil {
			cs, _ = c.capture.GetCapture(ctx, st.APIAddr, c.token())
		}
		if cs == nil || cs.State == "" {
			cs = &CaptureStatus{State: capturePassthrough}
		}
		fmt.Fprintln(c.stdout, captureSummary(cs))
	}
	url := "http://" + st.APIAddr
	fmt.Fprintf(c.stdout, "Proxy:     http://%s\nDashboard: %s\n", st.ProxyAddr, url)
	if openBrowser && c.browser != nil {
		if err := c.browser.Open(url); err != nil {
			fmt.Fprintf(c.stderr, "Could not open browser: %v\n", err)
		}
	}
	return 0
}

func parseForce(name string, args []string, stderr io.Writer) (bool, bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	force := fs.Bool("force", false, "Switch immediately instead of draining active connections")
	if err := fs.Parse(args); err != nil {
		return false, false
	}
	return *force, true
}

func (c *DaemonCLI) token() string {
	if c.loadCfg == nil {
		return ""
	}
	cfg, err := c.loadCfg()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.Auth.Token
}

// Pause switches capture to passthrough without stopping the daemon.
func (c *DaemonCLI) Pause(ctx context.Context, args []string) int {
	force, ok := parseForce("pause", args, c.stderr)
	if !ok {
		return 2
	}
	st := c.daemon.Running(ctx)
	if st == nil {
		fmt.Fprintln(c.stdout, "Langley daemon is not running — nothing is being captured")
		return 0
	}
	cs, err := c.capture.SetCapture(ctx, st.APIAddr, c.token(), false, force)
	if err != nil {
		fmt.Fprintf(c.stderr, "Error: failed to pause capture: %v\n", err)
		return 1
	}
	fmt.Fprintln(c.stdout, captureSummary(cs))
	return 0
}

// Resume ensures the daemon is running and enables capture.
func (c *DaemonCLI) Resume(ctx context.Context, name string, args []string) int {
	force, ok := parseForce(name, args, c.stderr)
	if !ok {
		return 2
	}
	st, started, err := c.daemon.Start(ctx)
	if err != nil {
		fmt.Fprintf(c.stderr, "Error: %v\n", err)
		return 1
	}
	if started {
		fmt.Fprintf(c.stdout, "Daemon started (PID %d)\n", st.PID)
	}
	cs, err := c.capture.SetCapture(ctx, st.APIAddr, c.token(), true, force)
	if err != nil {
		fmt.Fprintf(c.stderr, "Error: failed to enable capture: %v\n", err)
		return 1
	}
	fmt.Fprintln(c.stdout, captureSummary(cs))
	return 0
}

// Dashboard ensures the daemon is running and opens the dashboard without
// changing capture state.
func (c *DaemonCLI) Dashboard(ctx context.Context) int {
	st, started, err := c.daemon.Start(ctx)
	if err != nil {
		fmt.Fprintf(c.stderr, "Error: %v\n", err)
		return 1
	}
	if started {
		fmt.Fprintf(c.stdout, "Daemon started in passthrough mode (PID %d)\n", st.PID)
	}
	url := "http://" + st.APIAddr
	fmt.Fprintf(c.stdout, "Dashboard: %s\n", url)
	if c.browser != nil {
		if err := c.browser.Open(url); err != nil {
			fmt.Fprintf(c.stderr, "Could not open browser: %v\n", err)
		}
	}
	return 0
}

// Status prints daemon and capture state.
func (c *DaemonCLI) Status(ctx context.Context) int {
	var scope []string
	cfg, cfgErr := c.loadCfg()
	if cfgErr == nil && cfg != nil {
		scope = cfg.Proxy.InterceptHosts
	}
	st := c.daemon.Running(ctx)
	if st == nil {
		fmt.Fprintln(c.stdout, "Daemon:  stopped")
		if len(scope) > 0 {
			fmt.Fprintf(c.stdout, "Scope:   %s\n", strings.Join(scope, ", "))
		}
		return 0
	}
	token := ""
	if cfg != nil {
		token = cfg.Auth.Token
	}
	fmt.Fprintf(c.stdout, "Daemon:  running (PID %d)\n", st.PID)
	proxyAddr := st.ProxyAddr
	cs, err := c.capture.GetCapture(ctx, st.APIAddr, token)
	if err != nil {
		fmt.Fprintf(c.stdout, "Capture: unknown (%v)\n", err)
	} else {
		fmt.Fprintf(c.stdout, "Capture: %s\n", captureStateLine(cs))
		if len(cs.InterceptHosts) > 0 {
			scope = cs.InterceptHosts
		}
		if cs.ProxyAddr != "" {
			proxyAddr = cs.ProxyAddr
		}
	}
	fmt.Fprintf(c.stdout, "Proxy:   http://%s\n", proxyAddr)
	fmt.Fprintf(c.stdout, "API:     http://%s\n", st.APIAddr)
	if len(scope) > 0 {
		fmt.Fprintf(c.stdout, "Scope:   %s\n", strings.Join(scope, ", "))
	} else {
		fmt.Fprintln(c.stdout, "Scope:   built-in LLM provider hosts")
	}
	return 0
}

// captureStateLine renders a state with its draining counts for `status`.
func captureStateLine(cs *CaptureStatus) string {
	switch cs.State {
	case captureEnabling:
		return fmt.Sprintf("%s (%d active connection(s) will switch after current request)", cs.State, cs.DrainingPassthroughTunnels)
	case captureStopping:
		return fmt.Sprintf("%s (finishing %d active request(s))", cs.State, cs.DrainingMITMRequests)
	}
	return cs.State
}

// captureSummary renders the truthful one-line result of a capture transition.
func captureSummary(cs *CaptureStatus) string {
	switch cs.State {
	case captureStopping:
		return fmt.Sprintf("Stopping capture — finishing %d active request(s)…", cs.DrainingMITMRequests)
	case capturePassthrough:
		return "Langley paused (passthrough — TLS payloads are not decrypted, parsed, or stored)"
	case captureEnabling:
		return fmt.Sprintf("Capturing (%d active connection(s) will switch after current request)", cs.DrainingPassthroughTunnels)
	case captureCapturing:
		return "Capturing"
	}
	return "Capture: " + cs.State
}

// Daemon handles `langley daemon <start|stop|restart|status>`.
func (c *DaemonCLI) Daemon(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(c.stderr, "Usage: langley daemon <start|stop|restart|status>")
		return 2
	}
	switch args[0] {
	case "start":
		return c.daemonStart(ctx)
	case "stop":
		return c.daemonStop(ctx)
	case "restart":
		if code := c.daemonStop(ctx); code != 0 {
			return code
		}
		return c.daemonStart(ctx)
	case "status":
		return c.Status(ctx)
	}
	fmt.Fprintf(c.stderr, "Unknown daemon command: %s\n", args[0])
	fmt.Fprintln(c.stderr, "Usage: langley daemon <start|stop|restart|status>")
	return 2
}

func (c *DaemonCLI) daemonStart(ctx context.Context) int {
	st, started, err := c.daemon.Start(ctx)
	if err != nil {
		fmt.Fprintf(c.stderr, "Error: %v\n", err)
		return 1
	}
	if started {
		fmt.Fprintf(c.stdout, "Daemon started in passthrough mode (PID %d)\n", st.PID)
	} else {
		fmt.Fprintf(c.stdout, "Daemon already running (PID %d)\n", st.PID)
	}
	fmt.Fprintf(c.stdout, "Proxy:   http://%s\nAPI:     http://%s\n", st.ProxyAddr, st.APIAddr)
	return 0
}

func (c *DaemonCLI) daemonStop(ctx context.Context) int {
	pids, err := c.daemon.Stop(ctx)
	for _, pid := range pids {
		fmt.Fprintf(c.stdout, "Daemon stopped (PID %d)\n", pid)
	}
	if err != nil {
		fmt.Fprintf(c.stderr, "Error: %v\n", err)
		return 1
	}
	if len(pids) == 0 {
		fmt.Fprintln(c.stdout, "Daemon is not running")
	}
	return 0
}

// serviceFile returns the platform's user service unit path and unload command.
func (c *DaemonCLI) serviceFile() (path string, unload []string) {
	switch c.goos {
	case "darwin":
		return filepath.Join(c.homeDir, "Library", "LaunchAgents", "com.langley.daemon.plist"),
			[]string{"launchctl", "bootout", fmt.Sprintf("gui/%d/com.langley.daemon", os.Getuid())}
	case "linux":
		return filepath.Join(c.homeDir, ".config", "systemd", "user", "langley.service"),
			[]string{"systemctl", "--user", "disable", "--now", "langley.service"}
	}
	return "", nil
}

func (c *DaemonCLI) installServiceUnit(manual bool) error {
	path, _ := c.serviceFile()
	if path == "" || c.homeDir == "" {
		return nil
	}
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "langley"
	}
	logPath := filepath.Join(c.configDir, "langley.log")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	var content string
	var loadCmd []string
	switch c.goos {
	case "darwin":
		content = fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.langley.daemon</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>-no-shell</string>
    <string>-no-open</string>
    <string>-passthrough</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>LANGLEY_DAEMON</key>
    <string>1</string>
    <key>LANGLEY_START_PASSTHROUGH</key>
    <string>1</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, exe, logPath, logPath)
		loadCmd = []string{"launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), path}
	case "linux":
		content = fmt.Sprintf(`[Unit]
Description=Langley LLM Proxy Daemon (Passthrough by default)

[Service]
Type=simple
Environment=LANGLEY_DAEMON=1
Environment=LANGLEY_START_PASSTHROUGH=1
ExecStart=%s -no-shell -no-open -passthrough
Restart=on-failure

[Install]
WantedBy=default.target
`, exe)
		loadCmd = []string{"systemctl", "--user", "enable", "--now", "langley.service"}
	}
	if err := writeFileAtomic(path, []byte(content), 0644); err != nil {
		return err
	}
	if !manual && c.runner != nil && len(loadCmd) > 0 {
		if err := c.runner.Run(loadCmd[0], loadCmd[1:]...); err != nil {
			return fmt.Errorf("activating user service (%s): %w", strings.Join(loadCmd, " "), err)
		}
	}
	return nil
}

func (c *DaemonCLI) waitForServiceStart(ctx context.Context) *ServerState {
	if st := c.daemon.Running(ctx); st != nil {
		return st
	}
	pdc, ok := c.daemon.(*ProcessDaemonController)
	if !ok {
		return nil
	}
	timeout := pdc.readyTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	poll := pdc.poll
	if poll <= 0 {
		poll = 25 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st := pdc.Running(ctx); st != nil {
			return st
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(poll):
		}
	}
	return nil
}

func (c *DaemonCLI) unresolvedLiveDaemonPID(stoppedPIDs []int) int {
	stopped := make(map[int]bool, len(stoppedPIDs))
	for _, pid := range stoppedPIDs {
		stopped[pid] = true
	}
	var states []ServerState
	aliveFn := isProcessAlive
	if pdc, ok := c.daemon.(*ProcessDaemonController); ok {
		states = candidateStates(pdc.states)
		if pdc.alive != nil {
			aliveFn = pdc.alive
		}
	} else if c.configDir != "" {
		store := NewFileStateStoreWithPath(filepath.Join(c.configDir, "state.json"))
		states = candidateStates(store)
	}
	for _, st := range states {
		if st.PID > 0 && !stopped[st.PID] && aliveFn(st.PID) {
			return st.PID
		}
	}
	return 0
}

// Setup generates the CA and combined bundle, installs system trust (unless
// --manual/--no-mkcert), writes the user service unit, starts the daemon in
// passthrough mode, and installs the guarded shell hook in ~/.zshrc / ~/.bashrc.
func (c *DaemonCLI) Setup(ctx context.Context, args []string) int {
	setupFlags := flag.NewFlagSet("setup", flag.ContinueOnError)
	setupFlags.SetOutput(c.stderr)
	manualOnly := setupFlags.Bool("manual", false, "Show manual instructions without installing system trust")
	skipMkcert := setupFlags.Bool("no-mkcert", false, "Show manual instructions without installing (alias for --manual)")
	showHelp := setupFlags.Bool("help", false, "Show help")
	if err := setupFlags.Parse(args); err != nil {
		return 2
	}
	if *showHelp {
		printSetupHelp()
		return 0
	}

	certsDir := filepath.Join(c.configDir, "certs")
	caPath := filepath.Join(certsDir, "ca.crt")
	ca, err := langleytls.LoadOrCreateCA(certsDir)
	if err != nil {
		fmt.Fprintf(c.stderr, "Error loading/creating CA: %v\n", err)
		return 1
	}
	if _, err := ca.EnsureCombinedBundle(certsDir); err != nil {
		fmt.Fprintf(c.stderr, "Warning: combined CA bundle unavailable (%v); preserving existing system trust environment\n", err)
	}

	fmt.Fprintln(c.stdout, "Langley Setup - CA, Daemon & Shell Integration")
	fmt.Fprintln(c.stdout, "==============================================")
	fmt.Fprintln(c.stdout)
	fmt.Fprintf(c.stdout, "CA certificate: %s\n", caPath)
	if bundle := langleytls.BundlePath(certsDir); fileExists(bundle) {
		fmt.Fprintf(c.stdout, "CA bundle:      %s\n", bundle)
	}
	fmt.Fprintln(c.stdout)

	manual := *manualOnly || *skipMkcert
	if !manual {
		installSystemCA(caPath)
	} else {
		printManualInstructions(caPath)
	}

	if err := c.installServiceUnit(manual); err != nil {
		fmt.Fprintf(c.stderr, "Error installing user service unit: %v\n", err)
		return 1
	}
	svcPath, _ := c.serviceFile()
	if svcPath != "" && fileExists(svcPath) {
		fmt.Fprintf(c.stdout, "  ✓ Installed service unit %s\n", svcPath)
	}

	var (
		st      *ServerState
		started bool
	)
	if !manual && svcPath != "" && c.runner != nil {
		if st = c.waitForServiceStart(ctx); st != nil {
			started = true
		}
	}
	if st == nil {
		st, started, err = c.daemon.Start(ctx)
		if err != nil {
			fmt.Fprintf(c.stderr, "Error starting daemon: %v\n", err)
			return 1
		}
	}
	mode := capturePassthrough
	if !started && c.capture != nil {
		cs, err := c.capture.SetCapture(ctx, st.APIAddr, c.token(), false, false)
		if err != nil {
			fmt.Fprintf(c.stderr, "Error transitioning daemon to passthrough: %v\n", err)
			return 1
		}
		if cs != nil && cs.State != "" {
			mode = cs.State
		}
	}
	fmt.Fprintf(c.stdout, "  ✓ Daemon running in %s mode (PID %d, proxy http://%s)\n", mode, st.PID, st.ProxyAddr)

	hookMgr := NewFileShellHookManager(c.configDir, c.homeDir, c.shellPath)
	if err := hookMgr.Activate(st.ProxyAddr, caPath, st.PID); err != nil {
		fmt.Fprintf(c.stderr, "Error installing shell hook: %v\n", err)
		return 1
	}
	if activeData, err := os.ReadFile(filepath.Join(c.configDir, "active.env")); err == nil {
		_ = writeFileAtomic(filepath.Join(c.configDir, "shell.env"), activeData, 0600)
	}
	fmt.Fprintln(c.stdout, "  ✓ Installed guarded shell hook in ~/.zshrc / ~/.bashrc")
	return 0
}

// Uninstall stops the daemon and removes every artifact Langley created.
func (c *DaemonCLI) Uninstall(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	keepData := fs.Bool("keep-data", false, "Keep the captured traffic database")
	yes := fs.Bool("yes", false, "Do not prompt for confirmation")
	noTrust := fs.Bool("no-system-trust", false, "Print, instead of running, the trust-store removal command")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dbPath := filepath.Join(c.configDir, "langley.db")
	if cfg, err := c.loadCfg(); err == nil && cfg != nil && cfg.Persistence.DBPath != "" {
		dbPath = cfg.Persistence.DBPath
	}

	if !*yes {
		fmt.Fprintln(c.stdout, "This will stop Langley, remove its shell hooks, service files, certificates and state")
		if *keepData {
			fmt.Fprintf(c.stdout, "(keeping %s).\n", dbPath)
		} else {
			fmt.Fprintf(c.stdout, "and delete captured traffic in %s.\n", dbPath)
		}
		fmt.Fprint(c.stdout, "Continue? [y/N] ")
		line, _ := bufio.NewReader(c.stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(c.stdout, "Aborted")
			return 1
		}
	}

	var errs []error
	report := func(err error) {
		if err != nil {
			errs = append(errs, err)
			fmt.Fprintf(c.stderr, "  ✗ %v\n", err)
		}
	}

	// 1. Stop the daemon. Abort immediately if the daemon cannot be stopped or if
	// a recorded live PID could not be verified, so state.json and runtime
	// discovery files are never deleted for a still-live daemon.
	pids, err := c.daemon.Stop(ctx)
	for _, pid := range pids {
		fmt.Fprintf(c.stdout, "  ✓ Daemon stopped (PID %d)\n", pid)
	}
	if err != nil {
		report(err)
		fmt.Fprintln(c.stderr, "Langley uninstall aborted: daemon could not be stopped")
		return 1
	}
	if st := c.daemon.Running(ctx); st != nil {
		report(fmt.Errorf("daemon (PID %d) is still running", st.PID))
		fmt.Fprintln(c.stderr, "Langley uninstall aborted: daemon is still running")
		return 1
	}
	if unresolvedPID := c.unresolvedLiveDaemonPID(pids); unresolvedPID > 0 {
		report(fmt.Errorf("recorded daemon (PID %d) is still alive but its health/identity could not be verified", unresolvedPID))
		fmt.Fprintln(c.stderr, "Langley uninstall aborted: refusing to delete state for an unverified live process")
		return 1
	}

	// 2. Remove the user service unit.
	if path, unload := c.serviceFile(); path != "" && fileExists(path) {
		if c.runner != nil && unload != nil {
			_ = c.runner.Run(unload[0], unload[1:]...) // best effort: may not be loaded
		}
		if err := removeIfExists(path); err != nil {
			report(err)
		} else {
			fmt.Fprintf(c.stdout, "  ✓ Removed %s\n", path)
		}
	}

	// 3. Remove shell hook blocks (restoring backups) and runtime env/state.
	if err := NewFileShellHookManager(c.configDir, c.homeDir, c.shellPath).Deactivate(); err != nil {
		report(fmt.Errorf("removing shell hooks: %w", err))
	} else {
		fmt.Fprintln(c.stdout, "  ✓ Removed shell hooks from ~/.zshrc / ~/.bashrc")
	}
	for _, pattern := range []string{"active.env", "active.*.env", "shell.env", "state.json", "state.*.json", "hook.lock"} {
		matches, _ := filepath.Glob(filepath.Join(c.configDir, pattern))
		for _, m := range matches {
			report(removeIfExists(m))
		}
	}

	// 4. Remove the CA from the system trust store (needs ca.crt, so before 5).
	certsDir := filepath.Join(c.configDir, "certs")
	caPath := filepath.Join(certsDir, "ca.crt")
	if c.trust != nil {
		cmds := c.trust.Commands(caPath)
		if *noTrust {
			fmt.Fprintln(c.stdout, "  Remove the Langley CA from the system trust store with:")
			for _, cmd := range cmds {
				fmt.Fprintf(c.stdout, "    %s\n", strings.Join(cmd, " "))
			}
		} else {
			for _, cmd := range cmds {
				fmt.Fprintf(c.stdout, "  Running: %s\n", strings.Join(cmd, " "))
			}
			if err := c.trust.Uninstall(caPath); err != nil {
				report(fmt.Errorf("removing CA from system trust store: %w (run the command above manually)", err))
			} else {
				fmt.Fprintln(c.stdout, "  ✓ Removed Langley CA from the system trust store")
			}
		}
	}

	// 5. Remove certificates, logs, and (optionally) data.
	files := []string{
		caPath,
		filepath.Join(certsDir, "ca.key"),
		filepath.Join(certsDir, "ca-bundle.pem"),
		filepath.Join(c.configDir, "langley.log"),
	}
	if !*keepData {
		files = append(files, dbPath, dbPath+"-wal", dbPath+"-shm")
	}
	for _, f := range files {
		report(removeIfExists(f))
	}
	_ = os.Remove(certsDir) // only succeeds when empty
	fmt.Fprintln(c.stdout, "  ✓ Removed certificates and logs")
	if *keepData {
		fmt.Fprintf(c.stdout, "  Kept %s\n", dbPath)
	} else {
		fmt.Fprintf(c.stdout, "  ✓ Removed %s\n", dbPath)
	}

	if len(errs) > 0 {
		fmt.Fprintln(c.stderr, "Langley uninstall finished with errors")
		return 1
	}
	fmt.Fprintln(c.stdout, "Langley uninstalled")
	return 0
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	return nil
}
