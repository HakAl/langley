package provider

import (
	"testing"
)

func TestOpenAI_Name(t *testing.T) {
	o := &OpenAI{}
	if got := o.Name(); got != "openai" {
		t.Errorf("Name() = %q, want %q", got, "openai")
	}
}

func TestOpenAI_DetectHost(t *testing.T) {
	o := &OpenAI{}

	tests := []struct {
		host string
		want bool
	}{
		{"api.openai.com", true},
		{"openai.com", true},
		{"api.anthropic.com", false},
		{"example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := o.DetectHost(tt.host); got != tt.want {
				t.Errorf("DetectHost(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestOpenAI_ParseUsage_JSON(t *testing.T) {
	o := &OpenAI{}

	body := []byte(`{
		"id": "chatcmpl-abc123",
		"model": "gpt-4o",
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 50,
			"total_tokens": 150
		}
	}`)

	usage, err := o.ParseUsage(body, false)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	if usage.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", usage.Model, "gpt-4o")
	}
	if usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want %d", usage.InputTokens, 100)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want %d", usage.OutputTokens, 50)
	}
	// OpenAI doesn't have cache tokens
	if usage.CacheCreationTokens != 0 {
		t.Errorf("CacheCreationTokens = %d, want %d", usage.CacheCreationTokens, 0)
	}
	if usage.CacheReadTokens != 0 {
		t.Errorf("CacheReadTokens = %d, want %d", usage.CacheReadTokens, 0)
	}
}

func TestOpenAI_ParseUsage_SSE(t *testing.T) {
	o := &OpenAI{}

	// OpenAI SSE with usage in final chunk (stream_options.include_usage: true)
	body := []byte(`data: {"id":"chatcmpl-abc","model":"gpt-4o","choices":[{"delta":{"content":"Hello"}}]}

data: {"id":"chatcmpl-abc","model":"gpt-4o","choices":[{"delta":{"content":" world"}}]}

data: {"id":"chatcmpl-abc","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":150,"completion_tokens":75,"total_tokens":225}}

data: [DONE]
`)

	usage, err := o.ParseUsage(body, true)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	if usage.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", usage.Model, "gpt-4o")
	}
	if usage.InputTokens != 150 {
		t.Errorf("InputTokens = %d, want %d", usage.InputTokens, 150)
	}
	if usage.OutputTokens != 75 {
		t.Errorf("OutputTokens = %d, want %d", usage.OutputTokens, 75)
	}
}

func TestOpenAI_ParseUsage_EmptyBody(t *testing.T) {
	o := &OpenAI{}

	// Empty JSON should return empty usage without error
	usage, err := o.ParseUsage([]byte(`{}`), false)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}
	if usage.Model != "" || usage.InputTokens != 0 {
		t.Errorf("Expected empty usage for empty JSON")
	}
}

func TestOpenAI_ParseUsage_InvalidJSON(t *testing.T) {
	o := &OpenAI{}

	_, err := o.ParseUsage([]byte(`not valid json`), false)
	if err == nil {
		t.Error("ParseUsage() expected error for invalid JSON")
	}
}

func TestOpenAI_ParseUsage_SSE_NoUsage(t *testing.T) {
	o := &OpenAI{}

	// SSE without usage (stream_options.include_usage not set)
	body := []byte(`data: {"id":"chatcmpl-abc","model":"gpt-4-turbo","choices":[{"delta":{"content":"Hi"}}]}

data: {"id":"chatcmpl-abc","model":"gpt-4-turbo","choices":[{"delta":{}}],"finish_reason":"stop"}

data: [DONE]
`)

	usage, err := o.ParseUsage(body, true)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	// Model should still be captured
	if usage.Model != "gpt-4-turbo" {
		t.Errorf("Model = %q, want %q", usage.Model, "gpt-4-turbo")
	}
	// No usage data
	if usage.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want %d", usage.InputTokens, 0)
	}
	if usage.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d, want %d", usage.OutputTokens, 0)
	}
}

func TestOpenAI_ParseUsage_SSE_MalformedChunks(t *testing.T) {
	o := &OpenAI{}

	// SSE with some malformed chunks - should skip them gracefully
	body := []byte(`data: not json at all

data: {"id":"chatcmpl-abc","model":"gpt-3.5-turbo","usage":{"prompt_tokens":10,"completion_tokens":5}}

data: [DONE]
`)

	usage, err := o.ParseUsage(body, true)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	if usage.Model != "gpt-3.5-turbo" {
		t.Errorf("Model = %q, want %q", usage.Model, "gpt-3.5-turbo")
	}
	if usage.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want %d", usage.InputTokens, 10)
	}
	if usage.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d, want %d", usage.OutputTokens, 5)
	}
}
