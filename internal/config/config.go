// Package config handles configuration loading from YAML, CLI flags, and environment variables.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration structure.
type Config struct {
	Proxy       ProxyConfig       `yaml:"proxy"`
	Memory      MemoryConfig      `yaml:"memory"`
	Persistence PersistenceConfig `yaml:"persistence"`
	Analytics   AnalyticsConfig   `yaml:"analytics"`
	Retention   RetentionConfig   `yaml:"retention"`
	Redaction   RedactionConfig   `yaml:"redaction"`
	Auth        AuthConfig        `yaml:"auth"`
}

// ProxyConfig configures the HTTP/TLS proxy.
type ProxyConfig struct {
	Listen string `yaml:"listen"` // e.g., "localhost:9090"
	Host   string `yaml:"host"`   // Bind host
	Port   int    `yaml:"port"`   // Bind port (alternative to listen)
}

// MemoryConfig configures in-memory caching.
type MemoryConfig struct {
	MaxFlows         int `yaml:"max_flows"`           // N - flows in RAM
	MaxEventsPerFlow int `yaml:"max_events_per_flow"` // M - events per flow in RAM
}

// PersistenceConfig configures SQLite persistence.
type PersistenceConfig struct {
	DBPath             string `yaml:"db_path"`
	BodyMaxBytes       int    `yaml:"body_max_bytes"`
	EventBatchSize     int    `yaml:"event_batch_size"`
	EventBatchTimeoutMs int    `yaml:"event_batch_timeout_ms"`
	QueueMaxSize       int    `yaml:"queue_max_size"`
}

// AnalyticsConfig configures anomaly detection thresholds.
type AnalyticsConfig struct {
	AnomalyContextTokens      int `yaml:"anomaly_context_tokens"`
	AnomalyToolDelayMs        int `yaml:"anomaly_tool_delay_ms"`
	AnomalyRapidCallsWindowS  int `yaml:"anomaly_rapid_calls_window_s"`
	AnomalyRapidCallsThreshold int `yaml:"anomaly_rapid_calls_threshold"`
}

// RetentionConfig configures data retention TTLs.
type RetentionConfig struct {
	FlowsTTLDays   int `yaml:"flows_ttl_days"`
	EventsTTLDays  int `yaml:"events_ttl_days"`
	BodiesTTLDays  int `yaml:"bodies_ttl_days"`
	DropLogTTLDays int `yaml:"drop_log_ttl_days"`
}

// RedactionConfig configures credential redaction.
type RedactionConfig struct {
	AlwaysRedactHeaders []string `yaml:"always_redact_headers"`
	PatternRedactHeaders []string `yaml:"pattern_redact_headers"`
	RedactAPIKeys       bool     `yaml:"redact_api_keys"`
	RedactBase64Images  bool     `yaml:"redact_base64_images"`
	RawBodyStorage      bool     `yaml:"raw_body_storage"` // Default OFF per security spec
}

// AuthConfig configures API authentication.
type AuthConfig struct {
	Token string `yaml:"token"` // Bearer token for API access
}

// DefaultConfig returns a Config with secure defaults.
func DefaultConfig() *Config {
	return &Config{
		Proxy: ProxyConfig{
			Listen: "localhost:9090",
		},
		Memory: MemoryConfig{
			MaxFlows:         1000,
			MaxEventsPerFlow: 500,
		},
		Persistence: PersistenceConfig{
			DBPath:             "", // Set in Load based on platform
			BodyMaxBytes:       1048576, // 1MB
			EventBatchSize:     50,
			EventBatchTimeoutMs: 1000,
			QueueMaxSize:       10000,
		},
		Analytics: AnalyticsConfig{
			AnomalyContextTokens:      100000,
			AnomalyToolDelayMs:        30000,
			AnomalyRapidCallsWindowS:  10,
			AnomalyRapidCallsThreshold: 5,
		},
		Retention: RetentionConfig{
			FlowsTTLDays:   30,
			EventsTTLDays:  7,
			BodiesTTLDays:  3,
			DropLogTTLDays: 7,
		},
		Redaction: RedactionConfig{
			AlwaysRedactHeaders: []string{
				"authorization",
				"x-api-key",
				"x-amz-security-token", // AWS session tokens
				"cookie",
				"set-cookie",
			},
			PatternRedactHeaders: []string{
				`^x-.*-token$`,
				`^x-.*-key$`,
			},
			RedactAPIKeys:      true,
			RedactBase64Images: true,
			RawBodyStorage:     false, // Security: OFF by default
		},
		Auth: AuthConfig{
			Token: "", // Generated on first run if empty
		},
	}
}

// ConfigDir returns the platform-specific config directory.
func ConfigDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", fmt.Errorf("APPDATA environment variable not set")
		}
		return filepath.Join(appData, "langley"), nil
	default: // linux, darwin, etc.
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("getting home directory: %w", err)
		}
		return filepath.Join(home, ".config", "langley"), nil
	}
}

// DefaultConfigPath returns the default config file path.
func DefaultConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// DefaultDBPath returns the default database path.
func DefaultDBPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "langley.db"), nil
}

// Load loads configuration from file, with environment variable overrides.
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	// Set default DB path
	dbPath, err := DefaultDBPath()
	if err != nil {
		return nil, fmt.Errorf("getting default db path: %w", err)
	}
	cfg.Persistence.DBPath = dbPath

	// Determine config path
	if path == "" {
		path, err = DefaultConfigPath()
		if err != nil {
			return nil, fmt.Errorf("getting default config path: %w", err)
		}
	}

	// Try to load from file
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No config file - use defaults and generate token
			if cfg.Auth.Token == "" {
				cfg.Auth.Token, err = generateToken()
				if err != nil {
					return nil, fmt.Errorf("generating auth token: %w", err)
				}
				// Save config with generated token
				if err := cfg.Save(path); err != nil {
					return nil, fmt.Errorf("saving config: %w", err)
				}
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	// Parse YAML
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	// Apply environment variable overrides
	cfg.applyEnvOverrides()

	// Generate token if not set
	if cfg.Auth.Token == "" {
		cfg.Auth.Token, err = generateToken()
		if err != nil {
			return nil, fmt.Errorf("generating auth token: %w", err)
		}
		// Save config with generated token
		if err := cfg.Save(path); err != nil {
			return nil, fmt.Errorf("saving config: %w", err)
		}
	}

	return cfg, nil
}

// Save writes the config to the specified path with secure permissions.
func (c *Config) Save(path string) error {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	// Write with restrictive permissions (owner read/write only)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}

	return nil
}

// applyEnvOverrides applies environment variable overrides to the config.
func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("LANGLEY_LISTEN"); v != "" {
		c.Proxy.Listen = v
	}
	if v := os.Getenv("LANGLEY_DB_PATH"); v != "" {
		c.Persistence.DBPath = v
	}
	if v := os.Getenv("LANGLEY_AUTH_TOKEN"); v != "" {
		c.Auth.Token = v
	}
}

// generateToken generates a cryptographically random auth token.
func generateToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "langley_" + hex.EncodeToString(bytes), nil
}

// Listen returns the listen address, handling host:port vs listen field.
func (c *ProxyConfig) ListenAddr() string {
	if c.Listen != "" {
		return c.Listen
	}
	host := c.Host
	if host == "" {
		host = "localhost"
	}
	port := c.Port
	if port == 0 {
		port = 9090
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// HeaderShouldRedact checks if a header name should be redacted.
func (c *RedactionConfig) HeaderShouldRedact(name string) bool {
	nameLower := strings.ToLower(name)

	// Check always-redact list
	for _, h := range c.AlwaysRedactHeaders {
		if strings.ToLower(h) == nameLower {
			return true
		}
	}

	// Check pattern list
	for _, pattern := range c.PatternRedactHeaders {
		// Simple pattern matching - for MVP, just check prefix/suffix
		// Full regex can be added later
		pattern = strings.ToLower(pattern)
		pattern = strings.Trim(pattern, "^$")
		if strings.HasPrefix(pattern, "x-") && strings.HasSuffix(pattern, "-token") {
			prefix := strings.TrimSuffix(pattern, "-token")
			suffix := "-token"
			if strings.HasPrefix(nameLower, prefix) && strings.HasSuffix(nameLower, suffix) {
				return true
			}
		}
		if strings.HasPrefix(pattern, "x-") && strings.HasSuffix(pattern, "-key") {
			prefix := strings.TrimSuffix(pattern, "-key")
			suffix := "-key"
			if strings.HasPrefix(nameLower, prefix) && strings.HasSuffix(nameLower, suffix) {
				return true
			}
		}
	}

	return false
}
