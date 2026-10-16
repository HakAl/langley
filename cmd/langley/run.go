package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// StateReader reads server state.
type StateReader interface {
	Read() (*ServerState, error)
}

// StateWriter writes server state.
type StateWriter interface {
	Write(state ServerState) error
	Delete() error
}

// HealthChecker verifies the server is responding.
type HealthChecker interface {
	Check(ctx context.Context, apiAddr string) error
}

// EnvBuilder constructs the proxy environment.
type EnvBuilder interface {
	Build(proxyAddr, caPath string) []string
}

// ProcessRunner executes a subprocess.
type ProcessRunner interface {
	Run(ctx context.Context, command string, args []string, env []string) (exitCode int)
}

// FileChecker verifies files exist.
type FileChecker interface {
	Exists(path string) bool
}

// ShellHookManager manages temporary shell hook activation while Langley is running.
type ShellHookManager interface {
	Activate(proxyAddr, caPath string, pid int) error
	Deactivate() error
}

// BrowserOpener opens a URL in the user's default browser.
type BrowserOpener interface {
	Open(url string) error
}

// ShellDetector determines the user's interactive shell and whether stdin is a TTY.
type ShellDetector interface {
	IsInteractiveTTY() bool
	UserShell() (shell string, args []string)
}

// RunCommand orchestrates the run subcommand with injected dependencies.
type RunCommand struct {
	stateReader   StateReader
	healthChecker HealthChecker
	envBuilder    EnvBuilder
	fileChecker   FileChecker
	processRunner ProcessRunner
	stderr        io.Writer
}

// NewRunCommand creates a RunCommand with production dependencies.
func NewRunCommand() (*RunCommand, error) {
	stateStore, err := NewFileStateStore()
	if err != nil {
		return nil, err
	}
	return &RunCommand{
		stateReader:   stateStore,
		healthChecker: &HTTPHealthChecker{},
		envBuilder:    &ProxyEnvBuilder{},
		fileChecker:   &OSFileChecker{},
		processRunner: &ExecProcessRunner{},
		stderr:        os.Stderr,
	}, nil
}

// Execute runs the command and returns the exit code.
func (r *RunCommand) Execute(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(r.stderr, "Usage: langley run <command> [args...]")
		fmt.Fprintln(r.stderr, "\nRun a command with Langley proxy environment configured.")
		fmt.Fprintln(r.stderr, "\nExamples:")
		fmt.Fprintln(r.stderr, "  langley run claude")
		fmt.Fprintln(r.stderr, "  langley run python script.py")
		fmt.Fprintln(r.stderr, "  langley run curl https://api.anthropic.com/v1/messages")
		return 1
	}

	// Read server state
	state, err := r.stateReader.Read()
	if err != nil {
		if errors.Is(err, ErrServerNotRunning) {
			fmt.Fprintln(r.stderr, "Langley server is not running.")
			fmt.Fprintln(r.stderr, "\nStart the server first:")
			fmt.Fprintln(r.stderr, "    langley")
			fmt.Fprintln(r.stderr, "\nThen retry:")
			fmt.Fprintln(r.stderr, "    langley run <command>")
		} else {
			fmt.Fprintln(r.stderr, "Error:", err)
		}
		return 1
	}

	// Health check with timeout
	healthCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := r.healthChecker.Check(healthCtx, state.APIAddr); err != nil {
		fmt.Fprintln(r.stderr, "Error: Langley server is not responding.")
		fmt.Fprintln(r.stderr, "\nThe state file exists but the server may have crashed.")
		fmt.Fprintln(r.stderr, "Restart the server and try again.")
		return 1
	}

	// Verify CA exists
	if !r.fileChecker.Exists(state.CAPath) {
		fmt.Fprintf(r.stderr, "Error: CA certificate not found at %s\n", state.CAPath)
		fmt.Fprintln(r.stderr, "\nRun 'langley setup' to install the CA certificate.")
		return 1
	}

	// Build environment and run
	env := r.envBuilder.Build(state.ProxyAddr, state.CAPath)
	return r.processRunner.Run(ctx, args[0], args[1:], env)
}

// handleRunCommand is the entry point called from main.go.
func handleRunCommand(args []string) {
	cmd, err := NewRunCommand()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	os.Exit(cmd.Execute(context.Background(), args))
}

// --- Implementation types ---

// HTTPHealthChecker checks server health via HTTP.
type HTTPHealthChecker struct{}

// Check verifies the server is healthy by hitting the health endpoint.
func (h *HTTPHealthChecker) Check(ctx context.Context, apiAddr string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+apiAddr+"/api/health", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned %d", resp.StatusCode)
	}
	return nil
}

// ProxyEnvBuilder constructs proxy environment variables.
type ProxyEnvBuilder struct{}

// Build returns the current environment with proxy variables set.
func (b *ProxyEnvBuilder) Build(proxyAddr, caPath string) []string {
	proxyURL := "http://" + proxyAddr

	// Define all overrides (both upper and lower case for compatibility)
	overrides := map[string]string{
		"HTTPS_PROXY":         proxyURL,
		"https_proxy":         proxyURL,
		"HTTP_PROXY":          proxyURL,
		"http_proxy":          proxyURL,
		"NODE_EXTRA_CA_CERTS": caPath,
		"SSL_CERT_FILE":       caPath,
		"REQUESTS_CA_BUNDLE":  caPath,
		"LANGLEY_ACTIVE":      "1",
	}

	// Build case-insensitive lookup for filtering.
	// On Windows, env vars are case-insensitive but os.Environ() preserves
	// original casing, so we normalize to uppercase to catch all variants.
	overrideKeysUpper := make(map[string]bool, len(overrides))
	for k := range overrides {
		overrideKeysUpper[strings.ToUpper(k)] = true
	}

	// Filter parent env to remove keys we're about to set
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !overrideKeysUpper[strings.ToUpper(key)] {
			env = append(env, entry)
		}
	}

	// Append overrides
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}

	return env
}

// OSFileChecker checks file existence via OS.
type OSFileChecker struct{}

// Exists returns true if the file at path exists.
func (f *OSFileChecker) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ExecProcessRunner runs processes via os/exec.
type ExecProcessRunner struct{}

// Run executes a subprocess with the given environment and returns its exit code.
func (r *ExecProcessRunner) Run(ctx context.Context, command string, args []string, env []string) int {
	cmd := exec.Command(command, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env

	// Platform-specific setup and signal handling
	return runProcess(ctx, cmd)
}

// getExitCode extracts the exit code from an exec error.
func getExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

// --- Single-terminal session support (langley-cup3) ---

const (
	hookStartMarker = "# >>> langley hook >>>"
	hookEndMarker   = "# <<< langley hook <<<"
)

// NoopShellHookManager is used when the shell hook is disabled.
type NoopShellHookManager struct{}

// Activate does nothing.
func (NoopShellHookManager) Activate(proxyAddr, caPath string, pid int) error { return nil }

// Deactivate does nothing.
func (NoopShellHookManager) Deactivate() error { return nil }

// Test fault-injection hooks (nil in production).
var (
	lookoutAfterEnvRead    func(string)
	lookoutAfterRCTruncate func(string) error
	hookProcessMu          sync.Mutex
	nextHookSessionID      uint64
)

// rcEdit records how an RC file was modified so Deactivate can undo it.
type rcEdit struct {
	path            string      // resolved (symlink-followed) path that was written
	backupPath      string      // persistent JSON backup in configDir
	backupFile      *os.File    // open O_RDWR handle to backupPath when repair is pending
	createdMarker   string      // marker file in configDir when Langley created the RC file
	original        []byte      // pre-Langley content before any hook block was appended
	preimage        []byte      // content immediately before this transaction's write
	written         []byte      // content written by Activate
	mode            os.FileMode // file permissions before modification
	created         bool        // file did not exist before Langley first activated
	preimageExisted bool        // file existed immediately before this transaction's write
	writeFailed     bool        // last in-place write + rollback left a partial/corrupted file
	metadataPending bool        // RC file repair succeeded, only backup metadata update remains
	ownerPID        int         // PID of the manager session that recorded this edit
	sessionID       uint64      // process-local monotonic session ID for this activation
}

// rcBackup persists the pre-Langley state of an RC file across concurrent
// Langley instances and across mid-write failures. State ("s": 1=pending/failed,
// 2=healthy) is placed first so a completed write or repair can flip '1' -> '2'
// via a single-byte pwrite at offset 5 without truncating or partially writing
// the backup JSON.
type rcBackup struct {
	State           int         `json:"s"`
	WriteFailed     bool        `json:"write_failed"`
	Path            string      `json:"path"`
	Original        []byte      `json:"original"`
	Preimage        []byte      `json:"preimage"`
	Written         []byte      `json:"written"`
	Mode            os.FileMode `json:"mode"`
	Created         bool        `json:"created"`
	PreimageExisted bool        `json:"preimage_existed"`
	OwnerPID        int         `json:"owner_pid,omitempty"`
	SessionID       uint64      `json:"session_id,omitempty"`
}

// FileShellHookManager writes <configDir>/active.env and a managed block in
// ~/.zshrc / ~/.bashrc that sources it, so new terminal tabs pick up the proxy
// environment while Langley is running. The env file is guarded by
// `kill -0 <pid>` so a crashed Langley never leaves shells pointed at a dead proxy.
type FileShellHookManager struct {
	configDir  string
	homeDir    string
	shellName  string // basename of the user's $SHELL ("zsh", "bash", ...)
	pid        int
	envWritten []byte
	edits      []rcEdit
}

// NewFileShellHookManager creates a hook manager rooted at the given config and
// home directories. shellPath is the user's login shell (usually $SHELL); an RC
// file that does not yet exist is only created for that shell.
func NewFileShellHookManager(configDir, homeDir, shellPath string) *FileShellHookManager {
	return &FileShellHookManager{
		configDir: configDir,
		homeDir:   homeDir,
		shellName: filepath.Base(shellPath),
	}
}

func (m *FileShellHookManager) envPath() string {
	return filepath.Join(m.configDir, "active.env")
}

func (m *FileShellHookManager) pidEnvPath(pid int) string {
	return filepath.Join(m.configDir, fmt.Sprintf("active.%d.env", pid))
}

func (m *FileShellHookManager) createdMarkerPath(rcName string) string {
	return filepath.Join(m.configDir, ".created-"+rcName)
}

func (m *FileShellHookManager) rcBackupPath(rcName string) string {
	return filepath.Join(m.configDir, "rc-backup."+strings.TrimPrefix(rcName, ".")+".json")
}

func (m *FileShellHookManager) lockPath() string {
	return filepath.Join(m.configDir, "hook.lock")
}

func (m *FileShellHookManager) withHookLock(fn func() error) error {
	if err := os.MkdirAll(m.configDir, 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	hookProcessMu.Lock()
	defer hookProcessMu.Unlock()

	f, err := os.OpenFile(m.lockPath(), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("opening hook lock: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := lockFile(f); err != nil {
		return fmt.Errorf("locking hook state: %w", err)
	}
	defer func() { _ = unlockFile(f) }()
	return fn()
}

// shellQuote single-quotes s for POSIX shells.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

const activeEnvHeader = "# Generated by Langley — automatically removed on exit\n"

// renderActiveEnv returns the PID-guarded env file content.
func renderActiveEnv(proxyAddr, caPath string, pid int) []byte {
	proxyURL := "http://" + proxyAddr
	vars := []struct{ k, v string }{
		{"HTTPS_PROXY", proxyURL},
		{"https_proxy", proxyURL},
		{"HTTP_PROXY", proxyURL},
		{"http_proxy", proxyURL},
		{"NODE_EXTRA_CA_CERTS", caPath},
		{"SSL_CERT_FILE", caPath},
		{"REQUESTS_CA_BUNDLE", caPath},
		{"LANGLEY_ACTIVE", "1"},
	}
	var b strings.Builder
	b.WriteString(activeEnvHeader)
	fmt.Fprintf(&b, "if kill -0 %d 2>/dev/null; then\n", pid)
	for _, kv := range vars {
		fmt.Fprintf(&b, "  export %s=%s\n", kv.k, shellQuote(kv.v))
	}
	b.WriteString("fi\n")
	return []byte(b.String())
}

// renderCombinedActiveEnv combines one or more single-session active.<pid>.env
// payloads (ordered newest-first) into an `if ... elif ... fi` chain so that if
// the newest Langley process crashes with SIGKILL, newly opened shells
// automatically fall back to the next live Langley instance.
func renderCombinedActiveEnv(sessions [][]byte) []byte {
	if len(sessions) == 0 {
		return nil
	}
	if len(sessions) == 1 {
		return sessions[0]
	}
	var b strings.Builder
	b.WriteString(activeEnvHeader)
	wroteFirst := false
	for _, raw := range sessions {
		clause := strings.TrimPrefix(string(raw), activeEnvHeader)
		clause = strings.TrimSuffix(clause, "fi\n")
		if !strings.HasPrefix(clause, "if kill -0 ") {
			continue
		}
		if wroteFirst {
			clause = "el" + clause
		}
		b.WriteString(clause)
		wroteFirst = true
	}
	if !wroteFirst {
		return sessions[0]
	}
	b.WriteString("fi\n")
	return []byte(b.String())
}

// activeEnvPID extracts the primary guarded PID from an active.env payload, or 0 if not found.
func activeEnvPID(content []byte) int {
	const prefix = "if kill -0 "
	s := string(content)
	idx := strings.Index(s, prefix)
	if idx < 0 {
		return 0
	}
	rest := s[idx+len(prefix):]
	end := strings.IndexByte(rest, ' ')
	if end <= 0 {
		return 0
	}
	pid, err := strconv.Atoi(rest[:end])
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// renderHookBlock returns the managed RC block that sources envPath.
func renderHookBlock(envPath string) string {
	q := shellQuote(envPath)
	return hookStartMarker + "\n" +
		"[ -f " + q + " ] && . " + q + "\n" +
		hookEndMarker + "\n"
}

// stripHookBlock removes every complete managed block (marker lines inclusive).
// An unterminated start marker is left untouched rather than truncating the file.
func stripHookBlock(content []byte) []byte {
	s := string(content)
	for {
		start := markerLineIndex(s, hookStartMarker, 0)
		if start < 0 {
			break
		}
		end := markerLineIndex(s, hookEndMarker, start)
		if end < 0 {
			break
		}
		stop := end + len(hookEndMarker)
		if stop < len(s) && s[stop] == '\n' {
			stop++
		}
		s = s[:start] + s[stop:]
	}
	return []byte(s)
}

// markerLineIndex finds marker at the start of a line at or after from.
func markerLineIndex(s, marker string, from int) int {
	for i := from; i <= len(s)-len(marker); {
		j := strings.Index(s[i:], marker)
		if j < 0 {
			return -1
		}
		pos := i + j
		if pos == 0 || s[pos-1] == '\n' {
			return pos
		}
		i = pos + 1
	}
	return -1
}

// Activate writes active.env and installs the RC hook block. Re-activating
// replaces any existing (including stale) block rather than duplicating it.
func (m *FileShellHookManager) Activate(proxyAddr, caPath string, pid int) error {
	return m.withHookLock(func() error {
		env := renderActiveEnv(proxyAddr, caPath, pid)
		if pid > 0 {
			if err := writeFileAtomic(m.pidEnvPath(pid), env, 0600); err != nil {
				return fmt.Errorf("writing session env: %w", err)
			}
		}
		sessions := append([][]byte{env}, m.collectLiveSessionEnvs(pid)...)
		combined := renderCombinedActiveEnv(sessions)
		if err := writeFileAtomic(m.envPath(), combined, 0600); err != nil {
			return fmt.Errorf("writing active.env: %w", err)
		}
		m.pid = pid
		m.envWritten = combined

		if m.homeDir == "" {
			return nil
		}
		block := renderHookBlock(m.envPath())
		var errs []error
		for _, rc := range []struct{ name, shell string }{{".zshrc", "zsh"}, {".bashrc", "bash"}} {
			if m.hasEdit(filepath.Join(m.homeDir, rc.name)) {
				continue
			}
			if err := m.installBlock(filepath.Join(m.homeDir, rc.name), rc.name, rc.shell, block); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	})
}

func resolveRCPath(rcPath string) (string, os.FileInfo, error) {
	linfo, err := os.Lstat(rcPath)
	if err != nil {
		return "", nil, err
	}
	if linfo.Mode()&os.ModeSymlink == 0 {
		return rcPath, linfo, nil
	}
	resolved, err := filepath.EvalSymlinks(rcPath)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, err
	}
	return resolved, info, nil
}

func (m *FileShellHookManager) hasEdit(rcPath string) bool {
	resolved, _, err := resolveRCPath(rcPath)
	if err != nil {
		return false
	}
	for _, e := range m.edits {
		if e.path == resolved && !e.writeFailed {
			return true
		}
	}
	return false
}

func (m *FileShellHookManager) recordEdit(edit rcEdit) {
	for i := range m.edits {
		if m.edits[i].path == edit.path {
			if m.edits[i].backupFile != nil && m.edits[i].backupFile != edit.backupFile {
				_ = m.edits[i].backupFile.Close()
			}
			m.edits[i] = edit
			return
		}
	}
	m.edits = append(m.edits, edit)
}

func removeRCBackup(backupPath string) error {
	if backupPath == "" {
		return nil
	}
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

const (
	backupStateByteOffset  = 0
	backupStatePendingByte = ' '
	backupStateHealthyByte = '\n'
)

func loadRCBackup(backupPath string) (*rcBackup, bool) {
	raw, err := os.ReadFile(backupPath)
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	var b rcBackup
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, false
	}
	switch raw[0] {
	case backupStatePendingByte:
		b.WriteFailed = true
	case backupStateHealthyByte:
		b.WriteFailed = false
	default:
		switch b.State {
		case 1:
			b.WriteFailed = true
		case 2:
			b.WriteFailed = false
		}
	}
	return &b, true
}

func marshalRCBackup(b rcBackup) ([]byte, error) {
	lead := byte(backupStateHealthyByte)
	if b.WriteFailed {
		b.State = 1
		lead = backupStatePendingByte
	} else {
		b.State = 2
	}
	body, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 1+len(body))
	out[0] = lead
	copy(out[1:], body)
	return out, nil
}

// markRCBackupStateInPlace sets the leading JSON whitespace byte at offset 0
// (' ' for pending/failed, '\n' for healthy) via a single-byte pwrite without
// O_TRUNC so an existing backup file is never truncated or partially written
// even under RLIMIT_FSIZE=1 or read-only directory/file permissions.
func markRCBackupStateInPlace(backupPath string, openFile *os.File, state byte) error {
	if openFile != nil {
		if _, err := openFile.WriteAt([]byte{state}, backupStateByteOffset); err == nil {
			return nil
		}
	}
	if backupPath == "" {
		return nil
	}
	f, err := os.OpenFile(backupPath, os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteAt([]byte{state}, backupStateByteOffset)
	return err
}

func markRCBackupHealthyInPlace(backupPath string, openFile *os.File) error {
	return markRCBackupStateInPlace(backupPath, openFile, backupStateHealthyByte)
}

func saveRCBackup(backupPath string, b rcBackup) error {
	raw, err := marshalRCBackup(b)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(backupPath, raw, 0600); err != nil {
		if !b.WriteFailed && fileExists(backupPath) {
			return markRCBackupHealthyInPlace(backupPath, nil)
		}
		return err
	}
	return nil
}

func (m *FileShellHookManager) installBlock(rcPath, rcName, shell, block string) error {
	markerPath := m.createdMarkerPath(rcName)
	backupPath := m.rcBackupPath(rcName)
	sessionID := atomic.AddUint64(&nextHookSessionID, 1)
	edit := rcEdit{
		path:          rcPath,
		backupPath:    backupPath,
		createdMarker: markerPath,
		mode:          0644,
		ownerPID:      m.pid,
		sessionID:     sessionID,
	}
	if resolved, info, err := resolveRCPath(rcPath); err == nil {
		data, err := os.ReadFile(resolved)
		if err != nil {
			return fmt.Errorf("reading %s: %w", rcPath, err)
		}
		edit.path = resolved
		edit.mode = info.Mode().Perm()
		edit.preimage = data
		edit.preimageExisted = true
		stripped := stripHookBlock(data)
		hasHook := string(stripped) != string(data)
		if prev, ok := loadRCBackup(backupPath); ok && prev.Path == resolved && (prev.WriteFailed || (hasHook && string(data) == string(prev.Written))) {
			// Another Langley instance already installed the hook block on this RC file
			// (or a prior write failed mid-flight); preserve the exact pre-Langley
			// original bytes for final cleanup while keeping the immediate preimage
			// for transaction rollback.
			edit.original = prev.Original
			edit.created = prev.Created
			if prev.Mode != 0 {
				edit.mode = prev.Mode
			}
			if prev.WriteFailed {
				edit.preimage = prev.Preimage
				edit.preimageExisted = prev.PreimageExisted
			}
		} else {
			edit.original = stripped
			if _, mErr := os.Stat(markerPath); mErr == nil && len(edit.original) == 0 {
				edit.created = true
			}
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if shell != m.shellName {
			return nil
		}
		edit.created = true
		edit.preimageExisted = false
		if err := writeFileAtomic(markerPath, []byte("1"), 0600); err != nil {
			return fmt.Errorf("writing created marker for %s: %w", rcPath, err)
		}
	} else {
		return fmt.Errorf("resolving %s: %w", rcPath, err)
	}

	var b []byte
	b = append(b, edit.original...)
	if len(b) > 0 && b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	b = append(b, block...)
	edit.written = b

	// Persist a durable pending/uncertain state (WriteFailed=true) BEFORE
	// truncating the RC file in-place so an interruption or mid-write failure
	// after O_TRUNC is always distinguishable from a completed activation, even
	// if configDir becomes unwritable during the write.
	bkp := rcBackup{
		WriteFailed:     true,
		Path:            edit.path,
		Original:        edit.original,
		Preimage:        edit.preimage,
		Written:         edit.written,
		Mode:            edit.mode,
		Created:         edit.created,
		PreimageExisted: edit.preimageExisted,
		OwnerPID:        m.pid,
		SessionID:       sessionID,
	}
	if err := saveRCBackup(backupPath, bkp); err != nil {
		if edit.created && !edit.preimageExisted {
			_ = os.Remove(markerPath)
		}
		return fmt.Errorf("saving RC backup for %s: %w", rcPath, err)
	}
	backupFile, _ := os.OpenFile(backupPath, os.O_RDWR, 0600)
	closeBackupFile := true
	defer func() {
		if closeBackupFile && backupFile != nil {
			_ = backupFile.Close()
		}
	}()

	if err := writeFileInPlace(edit.path, b, edit.mode); err != nil {
		// Roll back to the immediate transaction preimage (preserving any live
		// peer's hook block if one was already installed before this attempt).
		if rbErr := rollbackRCEdit(edit.path, edit.preimage, edit.preimageExisted, edit.mode, edit.createdMarker); rbErr != nil {
			edit.writeFailed = true
			edit.backupFile = backupFile
			closeBackupFile = false
			m.recordEdit(edit)
			return fmt.Errorf("writing %s: %v (rollback failed: %w)", rcPath, err, rbErr)
		}
		if edit.preimageExisted {
			bkp.Written = edit.preimage
			bkp.WriteFailed = false
			if sErr := saveRCBackup(backupPath, bkp); sErr != nil {
				if markRCBackupHealthyInPlace(backupPath, backupFile) != nil {
					edit.writeFailed = false
					edit.metadataPending = true
					edit.written = edit.preimage
					edit.backupFile = backupFile
					closeBackupFile = false
					m.recordEdit(edit)
					return fmt.Errorf("writing %s: %v (backup update after rollback failed: %w)", rcPath, err, sErr)
				}
			}
			edit.writeFailed = false
			edit.metadataPending = false
			edit.written = edit.preimage
			m.recordEdit(edit)
		} else {
			if rErr := removeRCBackup(backupPath); rErr != nil {
				edit.writeFailed = false
				edit.metadataPending = true
				m.recordEdit(edit)
				return fmt.Errorf("writing %s: %v (backup cleanup after rollback failed: %w)", rcPath, err, rErr)
			}
			edit.writeFailed = false
			edit.metadataPending = false
		}
		return fmt.Errorf("writing %s: %w", rcPath, err)
	}

	bkp.WriteFailed = false
	if err := markRCBackupHealthyInPlace(backupPath, backupFile); err != nil {
		if sErr := saveRCBackup(backupPath, bkp); sErr != nil {
			edit.backupFile = backupFile
			closeBackupFile = false
		}
	}
	edit.writeFailed = false
	edit.metadataPending = false
	m.recordEdit(edit)
	return nil
}

// rollbackRCEdit restores path to target without invoking fault-injection hooks
// when an in-place RC write fails after truncation.
func rollbackRCEdit(path string, target []byte, existed bool, mode os.FileMode, createdMarker string) error {
	if !existed {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		if createdMarker != "" {
			_ = os.Remove(createdMarker)
		}
		return err
	}
	if mode == 0 {
		mode = 0644
	}
	return writeFileInPlaceWithFault(path, target, mode, nil)
}

// collectLiveSessionEnvs scans configDir for active.<pid>.env files belonging
// to other still-running Langley processes (ordered newest-first), removing
// stale ones along the way.
func (m *FileShellHookManager) collectLiveSessionEnvs(excludePID int) [][]byte {
	envs, _ := m.collectLiveSessionEnvsWithDeactivated(excludePID)
	return envs
}

func (m *FileShellHookManager) collectLiveSessionEnvsWithDeactivated(excludePID int) ([][]byte, map[int]bool) {
	matches, err := filepath.Glob(filepath.Join(m.configDir, "active.*.env"))
	if err != nil {
		return nil, nil
	}
	type liveEntry struct {
		pid     int
		modTime time.Time
		data    []byte
	}
	var entries []liveEntry
	deactivated := make(map[int]bool)
	for _, p := range matches {
		base := filepath.Base(p)
		pidStr := strings.TrimSuffix(strings.TrimPrefix(base, "active."), ".env")
		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid <= 0 || pid == excludePID {
			continue
		}
		if !isProcessAlive(pid) {
			_ = os.Remove(p)
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			deactivated[pid] = true
			_ = os.Remove(p)
			continue
		}
		entries = append(entries, liveEntry{
			pid:     pid,
			modTime: info.ModTime(),
			data:    data,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].modTime.Equal(entries[j].modTime) {
			return entries[i].modTime.After(entries[j].modTime)
		}
		return entries[i].pid > entries[j].pid
	})
	out := make([][]byte, len(entries))
	for i, e := range entries {
		out[i] = e.data
	}
	return out, deactivated
}

// Deactivate removes this session's env file and, when no other Langley
// instance remains active, removes active.env and strips the hook block from
// every RC file Activate modified. Safe to call repeatedly.
func (m *FileShellHookManager) Deactivate() error {
	if lookoutAfterEnvRead != nil && m.envWritten != nil {
		if _, err := os.ReadFile(m.envPath()); err == nil {
			lookoutAfterEnvRead(m.envPath())
		}
	}

	return m.withHookLock(func() error {
		var errs []error
		myPID := m.pid
		if myPID > 0 {
			if err := os.Remove(m.pidEnvPath(myPID)); err != nil && !errors.Is(err, os.ErrNotExist) {
				_ = os.Truncate(m.pidEnvPath(myPID), 0)
				errs = append(errs, fmt.Errorf("removing session env: %w", err))
			} else {
				m.pid = 0
			}
		}

		peerEnvs, deactivatedPIDs := m.collectLiveSessionEnvsWithDeactivated(myPID)
		otherActive := len(peerEnvs) > 0

		if m.envWritten != nil {
			cur, err := os.ReadFile(m.envPath())
			if errors.Is(err, os.ErrNotExist) {
				m.envWritten = nil
			} else if err != nil {
				errs = append(errs, fmt.Errorf("reading active.env: %w", err))
			} else {
				if otherActive {
					combined := renderCombinedActiveEnv(peerEnvs)
					if err := writeFileAtomic(m.envPath(), combined, 0600); err != nil {
						errs = append(errs, fmt.Errorf("restoring active.env for peer session: %w", err))
					} else {
						m.envWritten = nil
					}
				} else if string(cur) != string(m.envWritten) {
					// Another instance overwrote active.env directly. Keep it if its PID is alive and not deactivated.
					if pid := activeEnvPID(cur); pid != 0 && pid != myPID && !deactivatedPIDs[pid] && isProcessAlive(pid) {
						otherActive = true
						m.envWritten = nil
					} else {
						if err := os.Remove(m.envPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
							errs = append(errs, fmt.Errorf("removing active.env: %w", err))
						} else {
							m.envWritten = nil
						}
					}
				} else {
					if err := os.Remove(m.envPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
						errs = append(errs, fmt.Errorf("removing active.env: %w", err))
					} else {
						m.envWritten = nil
					}
				}
			}
		}

		if otherActive {
			// If this session suffered a failed RC write + rollback while a peer
			// was active, repair the RC file back to the peer's preimage before
			// leaving hooks in place for the remaining live instance(s). Track
			// metadataPending separately so a retry after successful RC repair
			// never replays a stale preimage over later user edits.
			var pending []rcEdit
			for _, e := range m.edits {
				if (e.writeFailed || e.metadataPending) && e.backupPath != "" {
					if !fileExists(e.backupPath) {
						e.writeFailed = false
						e.metadataPending = false
					} else if b, ok := loadRCBackup(e.backupPath); ok && !b.WriteFailed {
						e.writeFailed = false
					}
				}
				if !e.writeFailed && !e.metadataPending {
					if e.backupFile != nil {
						_ = e.backupFile.Close()
					}
					continue
				}
				if e.writeFailed {
					if err := rollbackRCEdit(e.path, e.preimage, e.preimageExisted, e.mode, e.createdMarker); err != nil {
						errs = append(errs, fmt.Errorf("repairing peer RC %s: %w", e.path, err))
						pending = append(pending, e)
						continue
					}
					e.writeFailed = false
				}
				if e.backupFile != nil {
					if e.preimageExisted {
						_, _ = e.backupFile.WriteAt([]byte{backupStateHealthyByte}, backupStateByteOffset)
					}
					_ = e.backupFile.Close()
					e.backupFile = nil
				}
				if e.backupPath != "" && e.preimageExisted {
					if err := saveRCBackup(e.backupPath, rcBackup{
						WriteFailed:     false,
						Path:            e.path,
						Original:        e.original,
						Preimage:        e.preimage,
						Written:         e.preimage,
						Mode:            e.mode,
						Created:         e.created,
						PreimageExisted: e.preimageExisted,
						OwnerPID:        e.ownerPID,
						SessionID:       e.sessionID,
					}); err != nil {
						errs = append(errs, fmt.Errorf("updating RC backup for %s: %w", e.path, err))
						e.metadataPending = true
						pending = append(pending, e)
						continue
					}
				} else if e.backupPath != "" {
					if err := removeRCBackup(e.backupPath); err != nil {
						errs = append(errs, fmt.Errorf("removing RC backup for %s: %w", e.path, err))
						e.metadataPending = true
						pending = append(pending, e)
						continue
					}
				}
				e.metadataPending = false
			}
			m.edits = pending
			return errors.Join(errs...)
		}

		handled := make(map[string]bool, len(m.edits))
		var remaining []rcEdit
		for _, e := range m.edits {
			handled[e.path] = true
			if err := restoreRC(&e); err != nil {
				errs = append(errs, err)
				remaining = append(remaining, e)
			}
		}
		m.edits = remaining

		if m.homeDir != "" {
			for _, rcName := range []string{".zshrc", ".bashrc"} {
				rcPath := filepath.Join(m.homeDir, rcName)
				backupPath := m.rcBackupPath(rcName)
				markerPath := m.createdMarkerPath(rcName)
				resolved, _, err := resolveRCPath(rcPath)
				if err != nil {
					if b, ok := loadRCBackup(backupPath); ok && b.Path != "" {
						if !handled[b.Path] {
							if cErr := cleanupPeerRC(b.Path, backupPath, markerPath); cErr != nil {
								errs = append(errs, cErr)
							}
						}
						continue
					}
					_ = removeRCBackup(backupPath)
					_ = os.Remove(markerPath)
					continue
				}
				if handled[resolved] {
					continue
				}
				if err := cleanupPeerRC(resolved, backupPath, markerPath); err != nil {
					errs = append(errs, err)
				}
			}
		}

		return errors.Join(errs...)
	})
}

func cleanupPeerRC(path, backupPath, markerPath string) error {
	if b, ok := loadRCBackup(backupPath); ok && b.Path == path {
		edit := rcEdit{
			path:            path,
			backupPath:      backupPath,
			createdMarker:   markerPath,
			original:        b.Original,
			preimage:        b.Preimage,
			written:         b.Written,
			mode:            b.Mode,
			created:         b.Created,
			preimageExisted: b.PreimageExisted,
			writeFailed:     b.WriteFailed,
		}
		return restoreRC(&edit)
	}
	_, markerErr := os.Stat(markerPath)
	created := markerErr == nil

	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(markerPath)
		return removeRCBackup(backupPath)
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	restored := stripHookBlock(cur)
	if created && len(restored) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing %s: %w", path, err)
		}
		_ = os.Remove(markerPath)
		return removeRCBackup(backupPath)
	}
	if string(restored) != string(cur) {
		if err := writeFileInPlace(path, restored, info.Mode().Perm()); err != nil {
			_ = rollbackRCEdit(path, restored, true, info.Mode().Perm(), "")
			return fmt.Errorf("restoring %s: %w", path, err)
		}
	}
	_ = os.Remove(markerPath)
	return removeRCBackup(backupPath)
}

func restoreRC(e *rcEdit) error {
	prevBackupFile := e.backupFile
	e.backupFile = nil
	closePrevBackupFile := true
	defer func() {
		if closePrevBackupFile && prevBackupFile != nil {
			_ = prevBackupFile.Close()
		}
	}()

	if e.backupPath != "" {
		if b, ok := loadRCBackup(e.backupPath); ok && b.Path == e.path {
			sameSession := e.sessionID != 0 && b.OwnerPID == e.ownerPID && b.SessionID == e.sessionID
			if !sameSession || (e.writeFailed && !b.WriteFailed) {
				if !e.writeFailed || !b.WriteFailed {
					e.original = b.Original
					e.preimage = b.Preimage
					e.written = b.Written
					e.created = b.Created
					e.preimageExisted = b.PreimageExisted
				}
				if b.Mode != 0 {
					e.mode = b.Mode
				}
				e.writeFailed = b.WriteFailed
				if !b.WriteFailed {
					e.metadataPending = false
				}
			}
		} else if !fileExists(e.backupPath) {
			// Another manager already completed restoration and removed the backup.
			e.writeFailed = false
			e.metadataPending = false
		}
	}

	info, err := os.Stat(e.path)
	if errors.Is(err, os.ErrNotExist) {
		info = nil
		if !e.writeFailed || e.created {
			if e.createdMarker != "" {
				_ = os.Remove(e.createdMarker)
			}
			return removeRCBackup(e.backupPath)
		}
	} else if err != nil {
		e.backupFile = prevBackupFile
		closePrevBackupFile = false
		return fmt.Errorf("stat %s: %w", e.path, err)
	}

	var cur []byte
	if info != nil {
		cur, err = os.ReadFile(e.path)
		if err != nil {
			e.backupFile = prevBackupFile
			closePrevBackupFile = false
			return fmt.Errorf("reading %s: %w", e.path, err)
		}
	}

	if e.writeFailed && info != nil && len(e.written) == len(e.original) && bytes.Equal(e.written, e.original) && bytes.Equal(stripHookBlock(e.written), e.written) {
		if (len(cur) >= len(e.original) || !bytes.HasPrefix(e.original, cur)) && bytes.Equal(stripHookBlock(cur), cur) {
			e.writeFailed = false
		}
	}

	restored := e.original
	userEdited := false
	if !e.writeFailed && info != nil && string(cur) != string(e.written) {
		// Activate succeeded cleanly and the user edited the file while Langley
		// ran (including intentionally emptying it to ""); keep their edits.
		restored = stripHookBlock(cur)
		userEdited = true
	}

	if e.created && len(restored) == 0 && !userEdited {
		if err := os.Remove(e.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			e.backupFile = prevBackupFile
			closePrevBackupFile = false
			return fmt.Errorf("removing %s: %w", e.path, err)
		}
		e.writeFailed = false
		e.metadataPending = false
		if e.createdMarker != "" {
			_ = os.Remove(e.createdMarker)
		}
		return removeRCBackup(e.backupPath)
	}

	if info == nil || string(restored) != string(cur) {
		perm := e.mode
		if info != nil && info.Mode().Perm() != 0 {
			perm = info.Mode().Perm()
		}
		if perm == 0 {
			perm = 0644
		}
		// Require saving the latest restoration target with WriteFailed=true
		// BEFORE truncating the RC file so that if backup persistence fails
		// (e.g. RLIMIT_FSIZE set before Deactivate), we abort without truncating
		// the RC file on disk; and if truncation/write subsequently fails, the
		// updated restoration target is already persisted on disk for retry.
		var backupFile *os.File
		closeBackupFile := true
		defer func() {
			if closeBackupFile && backupFile != nil {
				_ = backupFile.Close()
			}
		}()
		if e.backupPath != "" {
			if err := saveRCBackup(e.backupPath, rcBackup{
				WriteFailed:     true,
				Path:            e.path,
				Original:        restored,
				Preimage:        restored,
				Written:         restored,
				Mode:            perm,
				Created:         e.created,
				PreimageExisted: true,
				OwnerPID:        e.ownerPID,
				SessionID:       e.sessionID,
			}); err != nil {
				b, ok := loadRCBackup(e.backupPath)
				if !ok || b.Path != e.path || string(b.Original) != string(restored) {
					e.backupFile = prevBackupFile
					closePrevBackupFile = false
					return fmt.Errorf("saving RC restore backup for %s: %w", e.path, err)
				}
				if !b.WriteFailed {
					if mErr := markRCBackupStateInPlace(e.backupPath, prevBackupFile, backupStatePendingByte); mErr != nil {
						e.backupFile = prevBackupFile
						closePrevBackupFile = false
						return fmt.Errorf("saving RC restore backup for %s: %w", e.path, err)
					}
				}
				backupFile = prevBackupFile
				prevBackupFile = nil
			} else {
				backupFile, _ = os.OpenFile(e.backupPath, os.O_RDWR, 0600)
			}
		}
		if err := writeFileInPlace(e.path, restored, perm); err != nil {
			if rbErr := rollbackRCEdit(e.path, restored, true, perm, ""); rbErr != nil {
				e.writeFailed = true
				e.original = restored
				e.backupFile = backupFile
				closeBackupFile = false
				return fmt.Errorf("restoring %s: %v (rollback failed: %w)", e.path, err, rbErr)
			}
			// Rollback succeeded; mark backup healthy before attempting removal.
			_ = markRCBackupHealthyInPlace(e.backupPath, backupFile)
			e.writeFailed = false
			e.metadataPending = false
			e.original = restored
			e.written = restored
			if e.createdMarker != "" {
				_ = os.Remove(e.createdMarker)
			}
			_ = removeRCBackup(e.backupPath)
			return fmt.Errorf("restoring %s: %w", e.path, err)
		}
		_ = markRCBackupHealthyInPlace(e.backupPath, backupFile)
		e.original = restored
		e.written = restored
	} else {
		_ = markRCBackupHealthyInPlace(e.backupPath, prevBackupFile)
	}

	e.writeFailed = false
	e.metadataPending = false
	if e.createdMarker != "" {
		_ = os.Remove(e.createdMarker)
	}
	return removeRCBackup(e.backupPath)
}

// writeFileAtomic writes data to a temp file in the same directory and renames it.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// writeFileInPlace truncates and rewrites path, preserving its inode (and thus
// hard links, ownership, and any symlink pointing at it).
func writeFileInPlace(path string, data []byte, perm os.FileMode) error {
	return writeFileInPlaceWithFault(path, data, perm, lookoutAfterRCTruncate)
}

func writeFileInPlaceWithFault(path string, data []byte, perm os.FileMode, afterTrunc func(string) error) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if afterTrunc != nil {
		if err := afterTrunc(path); err != nil {
			_ = f.Close()
			return err
		}
	}
	_ = f.Chmod(perm)
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// OSBrowserOpener opens URLs with the platform's default handler.
type OSBrowserOpener struct {
	goos     string
	lookPath func(file string) (string, error)
	start    func(name string, args ...string) error
}

// NewOSBrowserOpener creates a browser opener for the current platform.
func NewOSBrowserOpener() *OSBrowserOpener {
	return &OSBrowserOpener{
		goos:     runtime.GOOS,
		lookPath: exec.LookPath,
		start:    startDetached,
	}
}

// startDetached starts a command without waiting for it, reaping it in the background.
func startDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// Open validates rawURL is an http(s) URL and launches the platform opener.
func (o *OSBrowserOpener) Open(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("refusing to open non-http(s) URL %q", rawURL)
	}
	switch o.goos {
	case "darwin":
		return o.start("open", rawURL)
	case "windows":
		// cmd.exe re-parses its command line; refuse its metacharacters.
		if strings.ContainsAny(rawURL, "&|^<>\"%") {
			return fmt.Errorf("refusing to open URL with shell metacharacters %q", rawURL)
		}
		return o.start("cmd", "/c", "start", "", rawURL)
	default:
		if _, err := o.lookPath("xdg-open"); err != nil {
			return fmt.Errorf("xdg-open not found: %w", err)
		}
		return o.start("xdg-open", rawURL)
	}
}

// OSShellDetector inspects the process environment and stdio.
type OSShellDetector struct {
	goos   string
	getenv func(string) string
	isExec func(path string) bool
	stdin  *os.File
	stderr *os.File
}

// NewOSShellDetector creates a detector for the current process.
func NewOSShellDetector() *OSShellDetector {
	return &OSShellDetector{
		goos:   runtime.GOOS,
		getenv: os.Getenv,
		isExec: isExecutableFile,
		stdin:  os.Stdin,
		stderr: os.Stderr,
	}
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
}

func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	return isTerminalFD(f.Fd())
}

// IsInteractiveTTY reports whether stdin and stderr are both terminals.
func (d *OSShellDetector) IsInteractiveTTY() bool {
	return isTerminal(d.stdin) && isTerminal(d.stderr)
}

// UserShell returns $SHELL when it is an absolute, executable path; otherwise
// /bin/sh (or %ComSpec% / cmd.exe on Windows).
func (d *OSShellDetector) UserShell() (string, []string) {
	if d.goos == "windows" {
		if comspec := d.getenv("ComSpec"); comspec != "" {
			return comspec, nil
		}
		return "cmd.exe", nil
	}
	if sh := d.getenv("SHELL"); filepath.IsAbs(sh) && d.isExec(sh) {
		return sh, nil
	}
	return "/bin/sh", nil
}
