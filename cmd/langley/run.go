package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
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
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env

	// Platform-specific setup and signal handling
	return runProcess(cmd)
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
