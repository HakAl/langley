// Package pricing provides model pricing data from LiteLLM's maintained database.
package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// LiteLLMPricingURL is the raw GitHub URL for LiteLLM's pricing JSON.
	LiteLLMPricingURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

	// DefaultTTL is how long to cache pricing data before refreshing.
	DefaultTTL = 24 * time.Hour

	// DefaultTimeout for HTTP requests.
	DefaultTimeout = 30 * time.Second
)

// ModelPrice contains pricing information for a model.
type ModelPrice struct {
	Provider              string   // Normalized provider name
	Model                 string   // Model identifier
	InputCostPer1k        float64  // Cost per 1000 input tokens
	OutputCostPer1k       float64  // Cost per 1000 output tokens
	CacheCreationPer1k    *float64 // Cost per 1000 cache creation tokens (nil if not supported)
	CacheReadPer1k        *float64 // Cost per 1000 cache read tokens (nil if not supported)
	MaxInputTokens        int      // Maximum input context
	MaxOutputTokens       int      // Maximum output tokens
	SupportsPromptCaching bool
	SupportsVision        bool
	SupportsFunctionCall  bool
}

// litellmEntry represents a single model entry in LiteLLM's JSON.
type litellmEntry struct {
	LiteLLMProvider             string   `json:"litellm_provider"`
	InputCostPerToken           *float64 `json:"input_cost_per_token"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost"`
	MaxInputTokens              int      `json:"max_input_tokens"`
	MaxOutputTokens             int      `json:"max_output_tokens"`
	Mode                        string   `json:"mode"`
	SupportsPromptCaching       bool     `json:"supports_prompt_caching"`
	SupportsVision              bool     `json:"supports_vision"`
	SupportsFunctionCalling     bool     `json:"supports_function_calling"`
}

// Source provides model pricing lookups.
type Source struct {
	mu        sync.RWMutex
	prices    map[string]*ModelPrice // Keyed by normalized model name
	fetchedAt time.Time
	ttl       time.Duration
	cacheDir  string
	logger    *slog.Logger
}

// Config configures the pricing source.
type Config struct {
	CacheDir string        // Directory for caching pricing data
	TTL      time.Duration // How long before refreshing (0 = use default)
	Logger   *slog.Logger
}

// NewSource creates a new pricing source.
func NewSource(cfg Config) *Source {
	ttl := cfg.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Source{
		prices:   make(map[string]*ModelPrice),
		ttl:      ttl,
		cacheDir: cfg.CacheDir,
		logger:   logger,
	}
}

// Load fetches pricing data from LiteLLM (or cache) and loads it into memory.
func (s *Source) Load(ctx context.Context) error {
	// Try to load from cache first
	if s.cacheDir != "" {
		if err := s.loadFromCache(); err == nil {
			s.logger.Info("pricing loaded from cache", "models", len(s.prices))
			return nil
		}
	}

	// Fetch from LiteLLM
	if err := s.fetchFromLiteLLM(ctx); err != nil {
		// If we have any cached data (even stale), use it
		if len(s.prices) > 0 {
			s.logger.Warn("failed to fetch pricing, using stale cache", "error", err)
			return nil
		}
		return fmt.Errorf("fetching pricing: %w", err)
	}

	// Save to cache
	if s.cacheDir != "" {
		if err := s.saveToCache(); err != nil {
			s.logger.Warn("failed to cache pricing", "error", err)
		}
	}

	return nil
}

// GetPrice looks up pricing for a model.
// It tries exact match first, then prefix match for versioned models.
func (s *Source) GetPrice(provider, model string) *ModelPrice {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Normalize provider name
	provider = normalizeProvider(provider)

	// Try exact match first
	key := normalizeModelKey(provider, model)
	if price, ok := s.prices[key]; ok {
		return price
	}

	// Try without version suffix (e.g., "claude-3-5-sonnet-20241022" -> "claude-3-5-sonnet")
	baseModel := stripModelVersion(model)
	if baseModel != model {
		key = normalizeModelKey(provider, baseModel)
		if price, ok := s.prices[key]; ok {
			return price
		}
	}

	// Try prefix match for partial model names
	prefix := key + "-"
	for k, price := range s.prices {
		if strings.HasPrefix(k, prefix) || strings.HasPrefix(k, key) {
			return price
		}
	}

	return nil
}

// RefreshIfStale checks if pricing data needs refresh and fetches if needed.
func (s *Source) RefreshIfStale(ctx context.Context) {
	s.mu.RLock()
	needsRefresh := time.Since(s.fetchedAt) > s.ttl
	s.mu.RUnlock()

	if needsRefresh {
		if err := s.fetchFromLiteLLM(ctx); err != nil {
			s.logger.Warn("failed to refresh pricing", "error", err)
		} else {
			s.logger.Info("pricing refreshed", "models", len(s.prices))
			if s.cacheDir != "" {
				_ = s.saveToCache()
			}
		}
	}
}

// Count returns the number of loaded pricing entries.
func (s *Source) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.prices)
}

// fetchFromLiteLLM downloads and parses pricing data from LiteLLM.
func (s *Source) fetchFromLiteLLM(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, LiteLLMPricingURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "langley-proxy/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from LiteLLM", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	return s.parseAndLoad(data)
}

// parseAndLoad parses LiteLLM JSON and loads into memory.
func (s *Source) parseAndLoad(data []byte) error {
	var entries map[string]litellmEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("parsing JSON: %w", err)
	}

	prices := make(map[string]*ModelPrice, len(entries))

	for modelName, entry := range entries {
		// Skip non-chat models (embeddings, image gen, etc.)
		if entry.Mode != "" && entry.Mode != "chat" {
			continue
		}

		// Skip models without token pricing
		if entry.InputCostPerToken == nil || entry.OutputCostPerToken == nil {
			continue
		}

		provider := normalizeProvider(entry.LiteLLMProvider)
		key := normalizeModelKey(provider, modelName)

		price := &ModelPrice{
			Provider:              provider,
			Model:                 modelName,
			InputCostPer1k:        *entry.InputCostPerToken * 1000,
			OutputCostPer1k:       *entry.OutputCostPerToken * 1000,
			MaxInputTokens:        entry.MaxInputTokens,
			MaxOutputTokens:       entry.MaxOutputTokens,
			SupportsPromptCaching: entry.SupportsPromptCaching,
			SupportsVision:        entry.SupportsVision,
			SupportsFunctionCall:  entry.SupportsFunctionCalling,
		}

		if entry.CacheCreationInputTokenCost != nil {
			cost := *entry.CacheCreationInputTokenCost * 1000
			price.CacheCreationPer1k = &cost
		}
		if entry.CacheReadInputTokenCost != nil {
			cost := *entry.CacheReadInputTokenCost * 1000
			price.CacheReadPer1k = &cost
		}

		prices[key] = price
	}

	s.mu.Lock()
	s.prices = prices
	s.fetchedAt = time.Now()
	s.mu.Unlock()

	return nil
}

// loadFromCache loads pricing from local cache file.
func (s *Source) loadFromCache() error {
	path := s.cachePath()
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	// Check if cache is still valid
	if time.Since(info.ModTime()) > s.ttl {
		return fmt.Errorf("cache expired")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	if err := s.parseAndLoad(data); err != nil {
		return err
	}

	s.mu.Lock()
	s.fetchedAt = info.ModTime()
	s.mu.Unlock()

	return nil
}

// saveToCache saves pricing data to local cache file.
func (s *Source) saveToCache() error {
	if err := os.MkdirAll(s.cacheDir, 0700); err != nil {
		return err
	}

	// We don't have the raw JSON anymore, so we fetch again
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, LiteLLMPricingURL, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	return os.WriteFile(s.cachePath(), data, 0600)
}

func (s *Source) cachePath() string {
	return filepath.Join(s.cacheDir, "litellm_pricing.json")
}

// normalizeProvider maps LiteLLM provider names to our canonical names.
func normalizeProvider(provider string) string {
	provider = strings.ToLower(provider)
	switch {
	case strings.Contains(provider, "anthropic"):
		return "anthropic"
	case strings.Contains(provider, "azure"): // Check azure before openai
		return "azure"
	case strings.Contains(provider, "openai"):
		return "openai"
	case strings.Contains(provider, "bedrock"):
		return "bedrock"
	case strings.Contains(provider, "vertex") || strings.Contains(provider, "google"):
		return "google"
	case strings.Contains(provider, "cohere"):
		return "cohere"
	case strings.Contains(provider, "mistral"):
		return "mistral"
	case strings.Contains(provider, "groq"):
		return "groq"
	default:
		return provider
	}
}

// normalizeModelKey creates a lookup key from provider and model.
func normalizeModelKey(provider, model string) string {
	// Remove provider prefixes that LiteLLM uses (e.g., "anthropic/claude-3-5-sonnet")
	model = strings.TrimPrefix(model, provider+"/")
	model = strings.TrimPrefix(model, "anthropic/")
	model = strings.TrimPrefix(model, "openai/")
	model = strings.ToLower(model)
	return provider + "/" + model
}

// stripModelVersion removes version suffixes from model names.
// e.g., "claude-3-5-sonnet-20241022" -> "claude-3-5-sonnet"
func stripModelVersion(model string) string {
	// Check if model ends with a date pattern (YYYYMMDD or -vN:N)
	parts := strings.Split(model, "-")
	if len(parts) < 2 {
		return model
	}

	last := parts[len(parts)-1]
	// Check for date suffix (8 digits)
	if len(last) == 8 {
		allDigits := true
		for _, c := range last {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return strings.Join(parts[:len(parts)-1], "-")
		}
	}

	return model
}
