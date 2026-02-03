package provider

import (
	"testing"
)

func TestBedrock_Name(t *testing.T) {
	b := &Bedrock{}
	if got := b.Name(); got != "bedrock" {
		t.Errorf("Name() = %q, want %q", got, "bedrock")
	}
}

func TestBedrock_DetectHost(t *testing.T) {
	b := &Bedrock{}

	tests := []struct {
		host string
		want bool
	}{
		// Valid Bedrock endpoints
		{"bedrock-runtime.us-east-1.amazonaws.com", true},
		{"bedrock-runtime.us-west-2.amazonaws.com", true},
		{"bedrock-runtime.eu-west-1.amazonaws.com", true},
		{"bedrock-runtime.us-east-1.amazonaws.com:443", true},

		// Not runtime endpoint
		{"bedrock.us-east-1.amazonaws.com", false},

		// False positives that MUST NOT match (domain boundary safety)
		{"bedrock-runtime.evil-amazonaws.com", false},
		{"bedrock-runtime.us-east-1.notamazonaws.com", false},
		{"fake-bedrock-runtime.us-east-1.amazonaws.com", false},
		{"bedrock-runtime.us-east-1.amazonaws.com.evil.com", false},

		// Unrelated hosts
		{"api.anthropic.com", false},
		{"api.openai.com", false},
		{"example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := b.DetectHost(tt.host); got != tt.want {
				t.Errorf("DetectHost(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestBedrock_ParseUsage_ConverseAPI(t *testing.T) {
	b := &Bedrock{}

	// Bedrock Converse API response format
	body := []byte(`{
		"output": {
			"message": {
				"role": "assistant",
				"content": [{"text": "Hello!"}]
			}
		},
		"stopReason": "end_turn",
		"usage": {
			"inputTokens": 100,
			"outputTokens": 50
		}
	}`)

	usage, err := b.ParseUsage(body, false)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	if usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want %d", usage.InputTokens, 100)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want %d", usage.OutputTokens, 50)
	}
}

func TestBedrock_ParseUsage_InvokeModelAPI(t *testing.T) {
	b := &Bedrock{}

	// Bedrock InvokeModel API response format
	body := []byte(`{
		"inputTokenCount": 150,
		"outputTokenCount": 75,
		"stopReason": "end_turn"
	}`)

	usage, err := b.ParseUsage(body, false)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	if usage.InputTokens != 150 {
		t.Errorf("InputTokens = %d, want %d", usage.InputTokens, 150)
	}
	if usage.OutputTokens != 75 {
		t.Errorf("OutputTokens = %d, want %d", usage.OutputTokens, 75)
	}
}

func TestBedrock_ParseUsage_ClaudePassthrough(t *testing.T) {
	b := &Bedrock{}

	// Claude's native format passed through InvokeModel
	body := []byte(`{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "text", "text": "Hello"}],
		"usage": {
			"input_tokens": 200,
			"output_tokens": 100
		}
	}`)

	usage, err := b.ParseUsage(body, false)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	if usage.InputTokens != 200 {
		t.Errorf("InputTokens = %d, want %d", usage.InputTokens, 200)
	}
	if usage.OutputTokens != 100 {
		t.Errorf("OutputTokens = %d, want %d", usage.OutputTokens, 100)
	}
}

func TestBedrock_ParseUsage_SSE_ConverseStream(t *testing.T) {
	b := &Bedrock{}

	// Bedrock ConverseStream format with metadata event
	body := []byte(`data: {"contentBlockDelta":{"delta":{"text":"Hello"},"contentBlockIndex":0}}

data: {"contentBlockStop":{"contentBlockIndex":0}}

data: {"metadata":{"usage":{"inputTokens":120,"outputTokens":60},"metrics":{"latencyMs":500}}}

`)

	usage, err := b.ParseUsage(body, true)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	if usage.InputTokens != 120 {
		t.Errorf("InputTokens = %d, want %d", usage.InputTokens, 120)
	}
	if usage.OutputTokens != 60 {
		t.Errorf("OutputTokens = %d, want %d", usage.OutputTokens, 60)
	}
}

func TestBedrock_ParseUsage_SSE_DirectTokenCounts(t *testing.T) {
	b := &Bedrock{}

	// Some Bedrock responses include token counts directly in events
	body := []byte(`data: {"chunk":{"bytes":"SGVsbG8="}}

data: {"inputTokenCount":80,"outputTokenCount":40}

`)

	usage, err := b.ParseUsage(body, true)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	if usage.InputTokens != 80 {
		t.Errorf("InputTokens = %d, want %d", usage.InputTokens, 80)
	}
	if usage.OutputTokens != 40 {
		t.Errorf("OutputTokens = %d, want %d", usage.OutputTokens, 40)
	}
}

func TestBedrock_ParseUsage_EmptyBody(t *testing.T) {
	b := &Bedrock{}

	usage, err := b.ParseUsage([]byte(`{}`), false)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}
	if usage.InputTokens != 0 || usage.OutputTokens != 0 {
		t.Errorf("Expected empty usage for empty JSON")
	}
}

func TestBedrock_ParseUsage_InvalidJSON(t *testing.T) {
	b := &Bedrock{}

	_, err := b.ParseUsage([]byte(`not valid json`), false)
	if err == nil {
		t.Error("ParseUsage() expected error for invalid JSON")
	}
}

func TestBedrock_ParseUsage_NoCacheTokens(t *testing.T) {
	b := &Bedrock{}

	body := []byte(`{
		"usage": {
			"inputTokens": 100,
			"outputTokens": 50
		}
	}`)

	usage, err := b.ParseUsage(body, false)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}

	// Bedrock doesn't expose cache tokens
	if usage.CacheCreationTokens != 0 {
		t.Errorf("CacheCreationTokens = %d, want %d", usage.CacheCreationTokens, 0)
	}
	if usage.CacheReadTokens != 0 {
		t.Errorf("CacheReadTokens = %d, want %d", usage.CacheReadTokens, 0)
	}
}
