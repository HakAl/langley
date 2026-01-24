package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	// CLI flags
	configPath := flag.String("config", "", "Path to config file")
	listenAddr := flag.String("listen", "", "Proxy listen address (overrides config)")
	apiAddr := flag.String("api", "localhost:9091", "API server listen address")
	showVersion := flag.Bool("version", false, "Show version and exit")
	showCA := flag.Bool("show-ca", false, "Show CA certificate path and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("langley %s (%s)\n", version, commit)
		os.Exit(0)
	}

	// Setup logging
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

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

	// Create task assigner
	taskAssigner := task.NewAssigner(task.AssignerConfig{
		IdleGapMinutes: 5,
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

	// Create API server
	apiServer := api.NewServer(cfg, dataStore, logger)
	apiMux := http.NewServeMux()
	apiMux.Handle("/", apiServer.Handler())
	apiMux.HandleFunc("/ws", wsHub.Handler(cfg.Auth.Token))

	// Start API server
	go func() {
		srv := &http.Server{
			Addr:    *apiAddr,
			Handler: apiMux,
		}
		slog.Info("API server starting", "addr", *apiAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("API server error", "error", err)
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

	// Give API server time to finish
	time.Sleep(100 * time.Millisecond)
	slog.Info("langley shutdown complete")
}
