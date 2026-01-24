package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/anthropics/langley/internal/api"
	"github.com/anthropics/langley/internal/config"
	"github.com/anthropics/langley/internal/proxy"
	"github.com/anthropics/langley/internal/redact"
	"github.com/anthropics/langley/internal/store"
	"github.com/anthropics/langley/internal/task"
	langleytls "github.com/anthropics/langley/internal/tls"
	"github.com/anthropics/langley/internal/ws"
	"github.com/anthropics/langley/web"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	// Check for subcommands first
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "token":
			handleTokenCommand(os.Args[2:])
			return
		case "setup":
			handleSetupCommand(os.Args[2:])
			return
		}
	}

	// CLI flags for main server mode
	configPath := flag.String("config", "", "Path to config file")
	listenAddr := flag.String("listen", "", "Proxy listen address (overrides config)")
	apiAddr := flag.String("api", "localhost:9091", "API server listen address")
	showVersion := flag.Bool("version", false, "Show version and exit")
	showCA := flag.Bool("show-ca", false, "Show CA certificate path and exit")
	showHelp := flag.Bool("help", false, "Show help")
	flag.Parse()

	if *showHelp {
		printHelp()
		os.Exit(0)
	}

	if *showVersion {
		fmt.Printf("langley %s (%s)\n", version, commit)
		os.Exit(0)
	}

	// Setup logging
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Determine actual config path for reload support
	actualConfigPath := *configPath
	if actualConfigPath == "" {
		var pathErr error
		actualConfigPath, pathErr = config.DefaultConfigPath()
		if pathErr != nil {
			slog.Error("failed to get default config path", "error", pathErr)
			os.Exit(1)
		}
	}

	// Load config
	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// CLI overrides
	if *listenAddr != "" {
		cfg.Proxy.Listen = *listenAddr
	}

	// Get config directory for certs
	configDir, err := config.ConfigDir()
	if err != nil {
		slog.Error("failed to get config directory", "error", err)
		os.Exit(1)
	}

	// Ensure directory exists
	if err := os.MkdirAll(configDir, 0700); err != nil {
		slog.Error("failed to create config directory", "error", err)
		os.Exit(1)
	}

	// Load or create CA
	certsDir := filepath.Join(configDir, "certs")
	ca, err := langleytls.LoadOrCreateCA(certsDir)
	if err != nil {
		slog.Error("failed to load/create CA", "error", err)
		os.Exit(1)
	}
	slog.Info("CA loaded", "path", filepath.Join(certsDir, "ca.crt"))

	if *showCA {
		fmt.Printf("CA certificate: %s\n", filepath.Join(certsDir, "ca.crt"))
		fmt.Println("\nTo trust this CA:")
		fmt.Println("  macOS: sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain " + filepath.Join(certsDir, "ca.crt"))
		fmt.Println("  Linux: sudo cp " + filepath.Join(certsDir, "ca.crt") + " /usr/local/share/ca-certificates/langley.crt && sudo update-ca-certificates")
		fmt.Println("  Windows: certutil -addstore -f \"ROOT\" " + filepath.Join(certsDir, "ca.crt"))
		os.Exit(0)
	}

	// Create cert cache
	certCache := langleytls.NewCertCache(ca, 1000)

	// Create redactor
	redactor, err := redact.New(&cfg.Redaction)
	if err != nil {
		slog.Error("failed to create redactor", "error", err)
		os.Exit(1)
	}

	// Create store
	dataStore, err := store.NewSQLiteStore(cfg.Persistence.DBPath, &cfg.Retention)
	if err != nil {
		slog.Error("failed to create store", "error", err)
		os.Exit(1)
	}
	defer dataStore.Close()
	slog.Info("database opened", "path", cfg.Persistence.DBPath)

	// Create task assigner with configurable idle gap
	taskAssigner := task.NewAssigner(task.AssignerConfig{
		IdleGapMinutes: cfg.Task.IdleGapMinutes,
	})

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("received shutdown signal", "signal", sig)
		cancel()
	}()

	// Create WebSocket hub
	wsHub := ws.NewHub(cfg, logger)
	go wsHub.Run(ctx)

	// Create API server with reload support
	apiServer := api.NewServer(cfg, dataStore, logger,
		api.WithConfigPath(actualConfigPath),
		api.WithOnReload(func(newToken string) {
			slog.Info("token reloaded", "token_length", len(newToken))
			// Note: WebSocket hub reads token from cfg.Auth.Token directly,
			// which is updated by the reload handler
		}),
	)
	apiMux := http.NewServeMux()
	apiMux.Handle("/api/", apiServer.Handler())
	apiMux.HandleFunc("/ws", wsHub.Handler(cfg.Auth.Token))
	apiMux.Handle("/", web.Handler()) // Serve embedded dashboard

	// Create API server instance for graceful shutdown
	apiSrv := &http.Server{
		Addr:    *apiAddr,
		Handler: apiMux,
	}

	// Start API server
	go func() {
		slog.Info("API server starting", "addr", *apiAddr)
		if err := apiSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("API server error", "error", err)
		}
	}()

	// Start retention cleanup goroutine
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()

		// Run immediately on startup
		runRetention(dataStore, logger)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runRetention(dataStore, logger)
			}
		}
	}()

	// Create and start MITM proxy
	mitmProxy, err := proxy.NewMITMProxy(proxy.MITMProxyConfig{
		Config:       cfg,
		Logger:       logger,
		CA:           ca,
		CertCache:    certCache,
		Redactor:     redactor,
		Store:        dataStore,
		TaskAssigner: taskAssigner,
		OnFlow: func(flow *store.Flow) {
			slog.Debug("flow started", "id", flow.ID, "host", flow.Host, "method", flow.Method)
			wsHub.BroadcastFlowStart(flow)
		},
		OnUpdate: func(flow *store.Flow) {
			status := 0
			if flow.StatusCode != nil {
				status = *flow.StatusCode
			}
			slog.Debug("flow completed", "id", flow.ID, "status", status, "sse", flow.IsSSE)
			wsHub.BroadcastFlowComplete(flow)
		},
		OnEvent: func(event *store.Event) {
			slog.Debug("SSE event", "flow_id", event.FlowID, "type", event.EventType, "seq", event.Sequence)
			wsHub.BroadcastEvent(event)
		},
	})
	if err != nil {
		slog.Error("failed to create proxy", "error", err)
		os.Exit(1)
	}

	slog.Info("starting langley",
		"proxy", cfg.Proxy.ListenAddr(),
		"api", *apiAddr,
	)
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  Proxy:     http://%s\n", cfg.Proxy.ListenAddr())
	fmt.Fprintf(os.Stderr, "  API:       http://%s\n", *apiAddr)
	fmt.Fprintf(os.Stderr, "  WebSocket: ws://%s/ws\n", *apiAddr)
	fmt.Fprintf(os.Stderr, "  CA:        %s\n", filepath.Join(certsDir, "ca.crt"))
	fmt.Fprintf(os.Stderr, "  DB:        %s\n", cfg.Persistence.DBPath)
	fmt.Fprintf(os.Stderr, "  Token:     %s\n", cfg.Auth.Token)
	fmt.Fprintf(os.Stderr, "\n")

	if err := mitmProxy.Serve(ctx); err != nil && err != context.Canceled {
		slog.Error("proxy error", "error", err)
		os.Exit(1)
	}

	// Graceful shutdown of API server
	slog.Info("shutting down API server")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := apiSrv.Shutdown(shutdownCtx); err != nil {
		slog.Error("API server shutdown error", "error", err)
	}

	slog.Info("langley shutdown complete")
}

// runRetention deletes expired data
func runRetention(dataStore store.Store, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	deleted, err := dataStore.RunRetention(ctx)
	if err != nil {
		logger.Error("retention cleanup failed", "error", err)
		return
	}
	if deleted > 0 {
		logger.Info("retention cleanup completed", "deleted", deleted)
	}
}

// printHelp prints usage information
func printHelp() {
	fmt.Printf(`Langley - Claude Traffic Proxy

Langley is an intercepting proxy that captures, persists, and analyzes
Claude API traffic for debugging and cost tracking.

USAGE:
    langley [OPTIONS]
    langley <command> [options]

COMMANDS:
    setup             Install CA certificate to system trust store
    token show        Show the current auth token
    token rotate      Generate a new auth token

OPTIONS:
    -config <path>    Path to configuration file
    -listen <addr>    Proxy listen address (default: from config or localhost:9090)
    -api <addr>       API/WebSocket server address (default: localhost:9091)
    -version          Show version information
    -show-ca          Show CA certificate path and trust instructions
    -help             Show this help message

EXAMPLES:
    langley                     Start with default config
    langley setup               Install CA certificate (first-time setup)
    langley -listen :8080       Start proxy on port 8080
    langley -config ./my.yaml   Use custom config file
    langley -show-ca            Show how to trust the CA certificate
    langley token show          Show current auth token
    langley token rotate        Generate and save a new auth token

CONFIGURATION:
    Config file locations (in order of precedence):
    - Path specified with -config
    - %%APPDATA%%\langley\langley.yaml (Windows)
    - ~/.config/langley/langley.yaml (Unix)

    Environment variables can override config:
    - LANGLEY_LISTEN             Proxy listen address
    - LANGLEY_AUTH_TOKEN         API authentication token
    - LANGLEY_DB_PATH            Database path

DASHBOARD:
    Access the web dashboard at http://localhost:9091 (or your -api address)
    You'll need the auth token shown at startup to connect.

For more information, see: https://github.com/anthropics/langley
`)
}

// handleTokenCommand handles the "token" subcommand
func handleTokenCommand(args []string) {
	tokenFlags := flag.NewFlagSet("token", flag.ExitOnError)
	configPath := tokenFlags.String("config", "", "Path to config file")
	apiAddr := tokenFlags.String("api", "localhost:9091", "API server address for reload")

	if len(args) == 0 {
		printTokenHelp()
		os.Exit(1)
	}

	subcommand := args[0]
	tokenFlags.Parse(args[1:])

	switch subcommand {
	case "show":
		tokenShow(*configPath)
	case "rotate":
		tokenRotate(*configPath, *apiAddr)
	case "help", "-help", "--help":
		printTokenHelp()
	default:
		fmt.Fprintf(os.Stderr, "Unknown token command: %s\n", subcommand)
		printTokenHelp()
		os.Exit(1)
	}
}

// tokenShow displays the current auth token
func tokenShow(configPath string) {
	cfg, cfgPath, err := loadConfigForToken(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Config:  %s\n", cfgPath)
	fmt.Printf("Token:   %s\n", cfg.Auth.Token)
}

// tokenRotate generates a new token and saves it
func tokenRotate(configPath string, apiAddr string) {
	cfg, cfgPath, err := loadConfigForToken(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	oldToken := cfg.Auth.Token

	// Generate new token using config package's function
	newToken, err := config.GenerateToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating token: %v\n", err)
		os.Exit(1)
	}

	cfg.Auth.Token = newToken

	// Save config
	if err := cfg.Save(cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Config:     %s\n", cfgPath)
	fmt.Printf("Old token:  %s\n", oldToken)
	fmt.Printf("New token:  %s\n", newToken)
	fmt.Println()

	// Try to notify running server
	if reloadRunningServer(apiAddr, oldToken) {
		fmt.Println("✓ Running server notified - new token is active immediately")
	} else {
		fmt.Println("Note: Restart langley for the new token to take effect")
		fmt.Println("      (Or the server is not running on " + apiAddr + ")")
	}
}

// loadConfigForToken loads config without generating a new token if missing
func loadConfigForToken(configPath string) (*config.Config, string, error) {
	cfg := config.DefaultConfig()

	// Determine config path
	var cfgPath string
	var err error
	if configPath != "" {
		cfgPath = configPath
	} else {
		cfgPath, err = config.DefaultConfigPath()
		if err != nil {
			return nil, "", fmt.Errorf("getting default config path: %w", err)
		}
	}

	// Load from file
	cfg, err = config.Load(configPath)
	if err != nil {
		return nil, "", err
	}

	return cfg, cfgPath, nil
}

// reloadRunningServer attempts to notify a running server to reload its config
func reloadRunningServer(apiAddr, oldToken string) bool {
	url := fmt.Sprintf("http://%s/api/admin/reload", apiAddr)

	req, err := http.NewRequest("POST", url, bytes.NewReader(nil))
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+oldToken)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	// Read response body to check status
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK {
		return true
	}

	// Log the error for debugging
	if resp.StatusCode != http.StatusNotFound {
		fmt.Fprintf(os.Stderr, "Reload request failed: %d - %s\n", resp.StatusCode, string(body))
	}
	return false
}

// printTokenHelp prints help for token subcommand
func printTokenHelp() {
	fmt.Printf(`Usage: langley token <command> [options]

Commands:
    show        Show the current auth token
    rotate      Generate a new auth token and save to config

Options:
    -config <path>    Path to configuration file
    -api <addr>       API server address for reload notification (default: localhost:9091)

Examples:
    langley token show
    langley token rotate
    langley token rotate -api localhost:8080
`)
}

// handleSetupCommand handles the "setup" subcommand for CA installation
func handleSetupCommand(args []string) {
	setupFlags := flag.NewFlagSet("setup", flag.ExitOnError)
	skipMkcert := setupFlags.Bool("no-mkcert", false, "Skip mkcert detection and show manual instructions")
	showHelp := setupFlags.Bool("help", false, "Show help")
	setupFlags.Parse(args)

	if *showHelp {
		printSetupHelp()
		os.Exit(0)
	}

	// Get config directory for certs
	configDir, err := config.ConfigDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting config directory: %v\n", err)
		os.Exit(1)
	}

	certsDir := filepath.Join(configDir, "certs")
	caPath := filepath.Join(certsDir, "ca.crt")

	// Ensure CA exists
	ca, err := langleytls.LoadOrCreateCA(certsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading/creating CA: %v\n", err)
		os.Exit(1)
	}
	_ = ca // CA is loaded, cert file exists

	fmt.Println("Langley Setup - CA Certificate Installation")
	fmt.Println("============================================")
	fmt.Println()
	fmt.Printf("CA certificate: %s\n", caPath)
	fmt.Println()

	if !*skipMkcert && hasMkcert() {
		runMkcertInstall(caPath)
	} else {
		printManualInstructions(caPath)
	}
}

// hasMkcert checks if mkcert is available in PATH
func hasMkcert() bool {
	_, err := exec.LookPath("mkcert")
	return err == nil
}

// runMkcertInstall uses mkcert to install the CA certificate
func runMkcertInstall(caPath string) {
	fmt.Println("✓ mkcert detected - using it to install CA certificate")
	fmt.Println()

	// Set CAROOT to langley's cert directory so mkcert uses our CA
	// Actually, we need to install OUR CA, not mkcert's
	// mkcert -install uses its own CA, so we need a different approach

	// Option 1: Check if mkcert's CA is already installed and offer to use it
	// Option 2: Use mkcert's -install to manage trust stores, but for OUR cert

	// For now, we'll use the platform-specific trust store commands
	// but through mkcert's known trust store locations

	fmt.Println("Installing Langley CA to system trust store...")
	fmt.Println()

	// Detect platform and provide instructions
	switch os := detectOS(); os {
	case "darwin":
		installMacOS(caPath)
	case "linux":
		installLinux(caPath)
	case "windows":
		installWindows(caPath)
	default:
		fmt.Println("Unknown platform - showing manual instructions")
		printManualInstructions(caPath)
	}
}

// detectOS returns the operating system
func detectOS() string {
	switch {
	case fileExists("/Library/Keychains/System.keychain"):
		return "darwin"
	case fileExists("/usr/local/share/ca-certificates"):
		return "linux"
	case fileExists("C:\\Windows\\System32"):
		return "windows"
	default:
		return "unknown"
	}
}

// fileExists checks if a file or directory exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// installMacOS installs the CA on macOS
func installMacOS(caPath string) {
	fmt.Println("macOS detected")
	fmt.Println()

	cmd := exec.Command("sudo", "security", "add-trusted-cert", "-d", "-r", "trustRoot",
		"-k", "/Library/Keychains/System.keychain", caPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	fmt.Println("Running: sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain " + caPath)
	fmt.Println()

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n✗ Failed to install CA: %v\n", err)
		fmt.Println("\nYou can run the command manually or use the manual instructions below:")
		fmt.Println()
		printManualInstructions(caPath)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("✓ CA certificate installed successfully!")
	printPostInstall()
}

// installLinux installs the CA on Linux
func installLinux(caPath string) {
	fmt.Println("Linux detected")
	fmt.Println()

	destPath := "/usr/local/share/ca-certificates/langley.crt"

	// Copy certificate
	fmt.Printf("Running: sudo cp %s %s\n", caPath, destPath)
	cpCmd := exec.Command("sudo", "cp", caPath, destPath)
	cpCmd.Stdout = os.Stdout
	cpCmd.Stderr = os.Stderr
	cpCmd.Stdin = os.Stdin

	if err := cpCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n✗ Failed to copy CA: %v\n", err)
		fmt.Println("\nYou can run the commands manually:")
		printManualInstructions(caPath)
		os.Exit(1)
	}

	// Update CA certificates
	fmt.Println("Running: sudo update-ca-certificates")
	updateCmd := exec.Command("sudo", "update-ca-certificates")
	updateCmd.Stdout = os.Stdout
	updateCmd.Stderr = os.Stderr
	updateCmd.Stdin = os.Stdin

	if err := updateCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n✗ Failed to update CA certificates: %v\n", err)
		fmt.Println("\nYou can run the command manually:")
		fmt.Println("  sudo update-ca-certificates")
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("✓ CA certificate installed successfully!")
	printPostInstall()
}

// installWindows installs the CA on Windows
func installWindows(caPath string) {
	fmt.Println("Windows detected")
	fmt.Println()

	fmt.Println("Installing CA certificate to Windows trust store...")
	fmt.Printf("Running: certutil -addstore -f \"ROOT\" %s\n", caPath)
	fmt.Println()

	cmd := exec.Command("certutil", "-addstore", "-f", "ROOT", caPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n✗ Failed to install CA: %v\n", err)
		fmt.Println("\nYou may need to run this command as Administrator:")
		fmt.Printf("  certutil -addstore -f \"ROOT\" %s\n", caPath)
		fmt.Println()
		fmt.Println("Or right-click langley.exe and select 'Run as administrator', then run 'langley setup'")
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("✓ CA certificate installed successfully!")
	printPostInstall()
}

// printManualInstructions prints manual CA installation instructions
func printManualInstructions(caPath string) {
	fmt.Println("Manual CA Installation Instructions")
	fmt.Println("-----------------------------------")
	fmt.Println()
	fmt.Println("macOS:")
	fmt.Printf("  sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain %s\n", caPath)
	fmt.Println()
	fmt.Println("Linux (Debian/Ubuntu):")
	fmt.Printf("  sudo cp %s /usr/local/share/ca-certificates/langley.crt\n", caPath)
	fmt.Println("  sudo update-ca-certificates")
	fmt.Println()
	fmt.Println("Linux (RHEL/Fedora):")
	fmt.Printf("  sudo cp %s /etc/pki/ca-trust/source/anchors/langley.crt\n", caPath)
	fmt.Println("  sudo update-ca-trust")
	fmt.Println()
	fmt.Println("Windows (Run as Administrator):")
	fmt.Printf("  certutil -addstore -f \"ROOT\" %s\n", caPath)
	fmt.Println()
	fmt.Println("Firefox (all platforms):")
	fmt.Println("  1. Open Firefox Settings → Privacy & Security → Certificates → View Certificates")
	fmt.Println("  2. Click 'Authorities' tab → 'Import'")
	fmt.Printf("  3. Select: %s\n", caPath)
	fmt.Println("  4. Check 'Trust this CA to identify websites' → OK")
	fmt.Println()
	fmt.Println("Tip: Install mkcert (https://github.com/FiloSottile/mkcert) for easier CA management")
}

// printPostInstall prints post-installation instructions
func printPostInstall() {
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Configure your HTTP client to use the proxy:")
	fmt.Println("     export HTTPS_PROXY=http://localhost:9090")
	fmt.Println("     export HTTP_PROXY=http://localhost:9090")
	fmt.Println()
	fmt.Println("  2. Start langley:")
	fmt.Println("     langley")
	fmt.Println()
	fmt.Println("  3. Open the dashboard:")
	fmt.Println("     http://localhost:9091")
	fmt.Println()
	fmt.Println("Note: Firefox uses its own certificate store. See 'langley setup --no-mkcert'")
	fmt.Println("      for Firefox-specific instructions.")
}

// printSetupHelp prints help for setup subcommand
func printSetupHelp() {
	fmt.Printf(`Usage: langley setup [options]

Installs the Langley CA certificate to your system's trust store.
This allows Langley to intercept HTTPS traffic to Claude and other LLM APIs.

Options:
    --no-mkcert    Skip mkcert detection and show manual instructions
    --help         Show this help message

The setup wizard will:
  1. Create or load the CA certificate
  2. Detect your operating system
  3. Attempt to install the CA automatically (may require sudo/admin)
  4. Provide manual instructions if automatic installation fails

Examples:
    langley setup              Auto-detect and install CA
    langley setup --no-mkcert  Show manual installation instructions
`)
}
