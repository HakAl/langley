package provider

import (
	"testing"
)

func TestAnthropic_Name(t *testing.T) {
	a := &Anthropic{}
	if got := a.Name(); got != "anthropic" {
		t.Errorf("Name() = %q, want %q", got, "anthropic")
	}
}

func TestAnthropic_DetectHost(t *testing.T) {
	a := &Anthropic{}

	tests := []struct {
		host string
		want bool
	}{
		{"api.anthropic.com", true},
		{"anthropic.com", true},
		{"claude.ai", true},
		{"api.claude.ai", true},
		{"api.openai.com", false},
		{"example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := a.DetectHost(tt.host); got != tt.want {
				t.Errorf("DetectHost(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestAnthropic_ParseUsage_JSON(t *testing.T) {
	a := &Anthropic{}

	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cache_creation_input_tokens": 10,
			"cache_read_input_tokens": 5
		}
	}`)

	usage, err := a.ParseUsage(body, false)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	if usage.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", usage.Model, "claude-sonnet-4-20250514")
	}
	if usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want %d", usage.InputTokens, 100)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want %d", usage.OutputTokens, 50)
	}
	if usage.CacheCreationTokens != 10 {
		t.Errorf("CacheCreationTokens = %d, want %d", usage.CacheCreationTokens, 10)
	}
	if usage.CacheReadTokens != 5 {
		t.Errorf("CacheReadTokens = %d, want %d", usage.CacheReadTokens, 5)
	}
}

func TestAnthropic_ParseUsage_SSE(t *testing.T) {
	a := &Anthropic{}

	body := []byte(`event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":150,"cache_creation_input_tokens":20,"cache_read_input_tokens":10}}}

event: content_block_delta
data: {"type":"content_block_delta","delta":{"text":"Hello"}}

event: message_delta
data: {"type":"message_delta","usage":{"output_tokens":75}}

event: message_stop
data: {"type":"message_stop"}
`)

	usage, err := a.ParseUsage(body, true)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	if usage.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", usage.Model, "claude-sonnet-4-20250514")
	}
	if usage.InputTokens != 150 {
		t.Errorf("InputTokens = %d, want %d", usage.InputTokens, 150)
	}
	if usage.OutputTokens != 75 {
		t.Errorf("OutputTokens = %d, want %d", usage.OutputTokens, 75)
	}
	if usage.CacheCreationTokens != 20 {
		t.Errorf("CacheCreationTokens = %d, want %d", usage.CacheCreationTokens, 20)
	}
	if usage.CacheReadTokens != 10 {
		t.Errorf("CacheReadTokens = %d, want %d", usage.CacheReadTokens, 10)
	}
}

func TestAnthropic_ParseUsage_EmptyBody(t *testing.T) {
	a := &Anthropic{}

	// Empty JSON should return empty usage without error
	usage, err := a.ParseUsage([]byte(`{}`), false)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}
	if usage.Model != "" || usage.InputTokens != 0 {
		t.Errorf("Expected empty usage for empty JSON")
	}
}

func TestAnthropic_ParseUsage_InvalidJSON(t *testing.T) {
	a := &Anthropic{}

	_, err := a.ParseUsage([]byte(`not valid json`), false)
	if err == nil {
		t.Error("ParseUsage() expected error for invalid JSON")
	}
}

func TestAnthropic_ParseUsage_SSE_NoTrailingNewline(t *testing.T) {
	a := &Anthropic{}

	// SSE without trailing newline after final event
	body := []byte(`event: message_start
data: {"type":"message_start","message":{"model":"claude-3-opus","usage":{"input_tokens":50}}}

event: message_delta
data: {"type":"message_delta","usage":{"output_tokens":25}}`)

	usage, err := a.ParseUsage(body, true)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	if usage.Model != "claude-3-opus" {
		t.Errorf("Model = %q, want %q", usage.Model, "claude-3-opus")
	}
	if usage.InputTokens != 50 {
		t.Errorf("InputTokens = %d, want %d", usage.InputTokens, 50)
	}
	if usage.OutputTokens != 25 {
		t.Errorf("OutputTokens = %d, want %d", usage.OutputTokens, 25)
	}
}
