package pricing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Sample LiteLLM pricing data for testing.
const testPricingJSON = `{
	"claude-3-5-sonnet-20241022": {
		"litellm_provider": "anthropic",
		"input_cost_per_token": 0.000003,
		"output_cost_per_token": 0.000015,
		"cache_creation_input_token_cost": 0.00000375,
		"cache_read_input_token_cost": 0.0000003,
		"max_input_tokens": 200000,
		"max_output_tokens": 8192,
		"mode": "chat",
		"supports_prompt_caching": true,
		"supports_vision": true,
		"supports_function_calling": true
	},
	"gpt-4o": {
		"litellm_provider": "openai",
		"input_cost_per_token": 0.0000025,
		"output_cost_per_token": 0.00001,
		"max_input_tokens": 128000,
		"max_output_tokens": 16384,
		"mode": "chat",
		"supports_function_calling": true
	},
	"text-embedding-3-small": {
		"litellm_provider": "openai",
		"input_cost_per_token": 0.00000002,
		"output_cost_per_token": 0,
		"mode": "embedding"
	},
	"dall-e-3": {
		"litellm_provider": "openai",
		"mode": "image_generation"
	}
}`

func TestSource_ParseAndLoad(t *testing.T) {
	s := NewSource(Config{})

	err := s.parseAndLoad([]byte(testPricingJSON))
	if err != nil {
		t.Fatalf("parseAndLoad failed: %v", err)
	}

	// Should have loaded 2 chat models (not embedding or image_generation)
	if s.Count() != 2 {
		t.Errorf("expected 2 models, got %d", s.Count())
	}
}

func TestSource_GetPrice_ExactMatch(t *testing.T) {
	s := NewSource(Config{})
	_ = s.parseAndLoad([]byte(testPricingJSON))

	price := s.GetPrice("anthropic", "claude-3-5-sonnet-20241022")
	if price == nil {
		t.Fatal("expected to find claude-3-5-sonnet-20241022")
	}

	// Check pricing conversion (per-token * 1000 = per-1k)
	// Use tolerance for floating point comparison
	const tolerance = 0.0000001
	if diff := price.InputCostPer1k - 0.003; diff < -tolerance || diff > tolerance {
		t.Errorf("InputCostPer1k = %v, want 0.003", price.InputCostPer1k)
	}
	if diff := price.OutputCostPer1k - 0.015; diff < -tolerance || diff > tolerance {
		t.Errorf("OutputCostPer1k = %v, want 0.015", price.OutputCostPer1k)
	}

	// Check cache pricing
	if price.CacheCreationPer1k == nil {
		t.Error("expected CacheCreationPer1k to be set")
	} else if diff := *price.CacheCreationPer1k - 0.00375; diff < -tolerance || diff > tolerance {
		t.Errorf("CacheCreationPer1k = %v, want 0.00375", *price.CacheCreationPer1k)
	}

	// Check capabilities
	if !price.SupportsPromptCaching {
		t.Error("expected SupportsPromptCaching to be true")
	}
	if !price.SupportsVision {
		t.Error("expected SupportsVision to be true")
	}
}

func TestSource_GetPrice_VersionStripping(t *testing.T) {
	s := NewSource(Config{})
	_ = s.parseAndLoad([]byte(testPricingJSON))

	// Should find with base model name (without date version)
	price := s.GetPrice("anthropic", "claude-3-5-sonnet")
	if price == nil {
		t.Fatal("expected to find claude-3-5-sonnet (without version)")
	}
}

func TestSource_GetPrice_NotFound(t *testing.T) {
	s := NewSource(Config{})
	_ = s.parseAndLoad([]byte(testPricingJSON))

	price := s.GetPrice("anthropic", "nonexistent-model")
	if price != nil {
		t.Error("expected nil for nonexistent model")
	}
}

func TestSource_GetPrice_OpenAI(t *testing.T) {
	s := NewSource(Config{})
	_ = s.parseAndLoad([]byte(testPricingJSON))

	price := s.GetPrice("openai", "gpt-4o")
	if price == nil {
		t.Fatal("expected to find gpt-4o")
	}

	if price.InputCostPer1k != 0.0025 {
		t.Errorf("InputCostPer1k = %f, want 0.0025", price.InputCostPer1k)
	}

	// OpenAI doesn't have cache pricing in this test data
	if price.CacheCreationPer1k != nil {
		t.Error("expected CacheCreationPer1k to be nil for OpenAI")
	}
}

func TestSource_FetchFromLiteLLM(t *testing.T) {
	// Create a test server that serves our test data
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testPricingJSON))
	}))
	defer server.Close()

	// We can't easily override the URL, so this test just verifies the parse logic
	// The actual HTTP fetch is tested via integration tests
	t.Skip("HTTP fetch requires URL override - covered by integration tests")
}

func TestSource_CacheOperations(t *testing.T) {
	// Create a temp directory for cache
	tmpDir := t.TempDir()

	s := NewSource(Config{
		CacheDir: tmpDir,
		TTL:      1 * time.Hour,
	})

	// Manually load test data
	err := s.parseAndLoad([]byte(testPricingJSON))
	if err != nil {
		t.Fatalf("parseAndLoad failed: %v", err)
	}

	// Write cache manually (simulating saveToCache with local data)
	cachePath := filepath.Join(tmpDir, "litellm_pricing.json")
	err = os.WriteFile(cachePath, []byte(testPricingJSON), 0600)
	if err != nil {
		t.Fatalf("failed to write cache: %v", err)
	}

	// Create a new source and load from cache
	s2 := NewSource(Config{
		CacheDir: tmpDir,
		TTL:      1 * time.Hour,
	})

	err = s2.loadFromCache()
	if err != nil {
		t.Fatalf("loadFromCache failed: %v", err)
	}

	if s2.Count() != 2 {
		t.Errorf("expected 2 models from cache, got %d", s2.Count())
	}
}

func TestSource_CacheExpired(t *testing.T) {
	tmpDir := t.TempDir()

	// Write cache with old timestamp
	cachePath := filepath.Join(tmpDir, "litellm_pricing.json")
	err := os.WriteFile(cachePath, []byte(testPricingJSON), 0600)
	if err != nil {
		t.Fatalf("failed to write cache: %v", err)
	}

	// Set file time to past
	oldTime := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(cachePath, oldTime, oldTime)

	s := NewSource(Config{
		CacheDir: tmpDir,
		TTL:      1 * time.Hour, // Cache should be expired
	})

	err = s.loadFromCache()
	if err == nil {
		t.Error("expected error for expired cache")
	}
}

func TestNormalizeProvider(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"anthropic", "anthropic"},
		{"Anthropic", "anthropic"},
		{"openai", "openai"},
		{"OpenAI", "openai"},
		{"bedrock", "bedrock"},
		{"bedrock_converse", "bedrock"},
		{"vertex_ai", "google"},
		{"google", "google"},
		{"azure", "azure"},
		{"azure_openai", "azure"},
		{"unknown_provider", "unknown_provider"},
	}

	for _, tt := range tests {
		got := normalizeProvider(tt.input)
		if got != tt.want {
			t.Errorf("normalizeProvider(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestStripModelVersion(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude-3-5-sonnet-20241022", "claude-3-5-sonnet"},
		{"claude-3-5-sonnet", "claude-3-5-sonnet"},
		{"gpt-4o", "gpt-4o"},
		{"claude-opus-4-20250514", "claude-opus-4"},
		{"simple", "simple"},
	}

	for _, tt := range tests {
		got := stripModelVersion(tt.input)
		if got != tt.want {
			t.Errorf("stripModelVersion(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSource_Load_GracefulFallback(t *testing.T) {
	tmpDir := t.TempDir()

	// Write cache file
	cachePath := filepath.Join(tmpDir, "litellm_pricing.json")
	err := os.WriteFile(cachePath, []byte(testPricingJSON), 0600)
	if err != nil {
		t.Fatalf("failed to write cache: %v", err)
	}

	s := NewSource(Config{
		CacheDir: tmpDir,
		TTL:      1 * time.Hour,
	})

	// Load should succeed from cache even without network
	ctx := context.Background()
	err = s.Load(ctx)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if s.Count() == 0 {
		t.Error("expected loaded models")
	}
}
