package provider

import (
	"testing"
)

func TestGemini_Name(t *testing.T) {
	g := &Gemini{}
	if got := g.Name(); got != "gemini" {
		t.Errorf("Name() = %q, want %q", got, "gemini")
	}
}

func TestGemini_DetectHost(t *testing.T) {
	g := &Gemini{}

	tests := []struct {
		host string
		want bool
	}{
		// Valid Gemini endpoints
		{"generativelanguage.googleapis.com", true},
		{"generativelanguage.googleapis.com:443", true},

		// False positives that MUST NOT match (domain boundary safety)
		{"generativelanguage.googleapis.com.evil.com", false},
		{"fakegenerativelanguage.googleapis.com", false},
		{"notgoogleapis.com", false},

		// Unrelated hosts
		{"api.anthropic.com", false},
		{"api.openai.com", false},
		{"example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := g.DetectHost(tt.host); got != tt.want {
				t.Errorf("DetectHost(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestGemini_ParseUsage_JSON(t *testing.T) {
	g := &Gemini{}

	body := []byte(`{
		"candidates": [
			{
				"content": {
					"parts": [{"text": "Hello!"}],
					"role": "model"
				},
				"finishReason": "STOP"
			}
		],
		"usageMetadata": {
			"promptTokenCount": 100,
			"candidatesTokenCount": 50,
			"totalTokenCount": 150
		},
		"modelVersion": "gemini-1.5-pro-001"
	}`)

	usage, err := g.ParseUsage(body, false)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	if usage.Model != "gemini-1.5-pro-001" {
		t.Errorf("Model = %q, want %q", usage.Model, "gemini-1.5-pro-001")
	}
	if usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want %d", usage.InputTokens, 100)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want %d", usage.OutputTokens, 50)
	}
}

func TestGemini_ParseUsage_SSE(t *testing.T) {
	g := &Gemini{}

	// Gemini streaming format - JSON objects with final chunk containing usage
	body := []byte(`data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]}}],"modelVersion":"gemini-1.5-flash"}

data: {"candidates":[{"content":{"parts":[{"text":" world"}]}}]}

data: {"candidates":[{"content":{"parts":[{"text":"!"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":60,"totalTokenCount":180},"modelVersion":"gemini-1.5-flash"}

`)

	usage, err := g.ParseUsage(body, true)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	if usage.Model != "gemini-1.5-flash" {
		t.Errorf("Model = %q, want %q", usage.Model, "gemini-1.5-flash")
	}
	if usage.InputTokens != 120 {
		t.Errorf("InputTokens = %d, want %d", usage.InputTokens, 120)
	}
	if usage.OutputTokens != 60 {
		t.Errorf("OutputTokens = %d, want %d", usage.OutputTokens, 60)
	}
}

func TestGemini_ParseUsage_EmptyBody(t *testing.T) {
	g := &Gemini{}

	usage, err := g.ParseUsage([]byte(`{}`), false)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}
	if usage.Model != "" || usage.InputTokens != 0 {
		t.Errorf("Expected empty usage for empty JSON")
	}
}

func TestGemini_ParseUsage_InvalidJSON(t *testing.T) {
	g := &Gemini{}

	_, err := g.ParseUsage([]byte(`not valid json`), false)
	if err == nil {
		t.Error("ParseUsage() expected error for invalid JSON")
	}
}

func TestGemini_ParseUsage_NoCacheTokens(t *testing.T) {
	g := &Gemini{}

	body := []byte(`{
		"usageMetadata": {
			"promptTokenCount": 100,
			"candidatesTokenCount": 50
		}
	}`)

	usage, err := g.ParseUsage(body, false)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	// Gemini doesn't have cache tokens
	if usage.CacheCreationTokens != 0 {
		t.Errorf("CacheCreationTokens = %d, want %d", usage.CacheCreationTokens, 0)
	}
	if usage.CacheReadTokens != 0 {
		t.Errorf("CacheReadTokens = %d, want %d", usage.CacheReadTokens, 0)
	}
}

func TestGemini_ParseUsage_SSE_RawJSON(t *testing.T) {
	g := &Gemini{}

	// Some Gemini responses may come as raw JSON lines without data: prefix
	body := []byte(`{"candidates":[{"content":{"parts":[{"text":"Hi"}]}}],"modelVersion":"gemini-pro"}
{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}
`)

	usage, err := g.ParseUsage(body, true)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	if usage.Model != "gemini-pro" {
		t.Errorf("Model = %q, want %q", usage.Model, "gemini-pro")
	}
	if usage.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want %d", usage.InputTokens, 10)
	}
	if usage.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d, want %d", usage.OutputTokens, 5)
	}
}

func TestGemini_ParseUsage_NoUsageMetadata(t *testing.T) {
	g := &Gemini{}

	// Response without usage metadata
	body := []byte(`{
		"candidates": [{"content": {"parts": [{"text": "Hello"}]}}],
		"modelVersion": "gemini-1.5-pro"
	}`)

	usage, err := g.ParseUsage(body, false)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	if usage.Model != "gemini-1.5-pro" {
		t.Errorf("Model = %q, want %q", usage.Model, "gemini-1.5-pro")
	}
	if usage.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want %d", usage.InputTokens, 0)
	}
	if usage.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d, want %d", usage.OutputTokens, 0)
	}
}
