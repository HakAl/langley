package testutil

import (
	"strings"
	"testing"
)

func TestFlowBuilder_Defaults(t *testing.T) {
	flow := NewFlow().Build()

	if flow.ID != "flow-test-001" {
		t.Errorf("ID = %q, want %q", flow.ID, "flow-test-001")
	}
	if flow.Host != "api.anthropic.com" {
		t.Errorf("Host = %q, want %q", flow.Host, "api.anthropic.com")
	}
	if flow.Method != "POST" {
		t.Errorf("Method = %q, want %q", flow.Method, "POST")
	}
	if flow.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", flow.Provider, "anthropic")
	}
	if *flow.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want %d", *flow.StatusCode, 200)
	}
	if flow.IsSSE {
		t.Error("IsSSE should be false by default")
	}
	if flow.FlowIntegrity != "complete" {
		t.Errorf("FlowIntegrity = %q, want %q", flow.FlowIntegrity, "complete")
	}
}

func TestFlowBuilder_WithProvider(t *testing.T) {
	tests := []struct {
		provider   string
		wantHost   string
		wantPath   string
	}{
		{"anthropic", "api.anthropic.com", "/v1/messages"},
		{"openai", "api.openai.com", "/v1/chat/completions"},
		{"bedrock", "bedrock-runtime.us-east-1.amazonaws.com", "/model/anthropic.claude-3-sonnet-20240229-v1:0/converse"},
		{"gemini", "generativelanguage.googleapis.com", "/v1beta/models/gemini-1.5-pro:generateContent"},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			flow := NewFlow().WithProvider(tt.provider).Build()

			if flow.Provider != tt.provider {
				t.Errorf("Provider = %q, want %q", flow.Provider, tt.provider)
			}
			if flow.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", flow.Host, tt.wantHost)
			}
			if flow.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", flow.Path, tt.wantPath)
			}
		})
	}
}

func TestFlowBuilder_Streaming(t *testing.T) {
	flow := NewFlow().Streaming().Build()

	if !flow.IsSSE {
		t.Error("IsSSE should be true after Streaming()")
	}
}

func TestFlowBuilder_WithStatus(t *testing.T) {
	tests := []struct {
		code     int
		wantText string
	}{
		{200, "OK"},
		{400, "Bad Request"},
		{401, "Unauthorized"},
		{500, "Internal Server Error"},
	}

	for _, tt := range tests {
		t.Run(tt.wantText, func(t *testing.T) {
			flow := NewFlow().WithStatus(tt.code).Build()

			if *flow.StatusCode != tt.code {
				t.Errorf("StatusCode = %d, want %d", *flow.StatusCode, tt.code)
			}
			if *flow.StatusText != tt.wantText {
				t.Errorf("StatusText = %q, want %q", *flow.StatusText, tt.wantText)
			}
		})
	}
}

func TestFlowBuilder_ChainedMethods(t *testing.T) {
	taskID := "task-123"
	flow := NewFlow().
		WithID("custom-id").
		WithProvider("openai").
		WithTaskID(taskID).
		WithStatus(201).
		WithModel("gpt-4-turbo").
		WithTokens(200, 100).
		WithDuration(500).
		Streaming().
		Build()

	if flow.ID != "custom-id" {
		t.Errorf("ID = %q, want %q", flow.ID, "custom-id")
	}
	if flow.Provider != "openai" {
		t.Errorf("Provider = %q, want %q", flow.Provider, "openai")
	}
	if *flow.TaskID != taskID {
		t.Errorf("TaskID = %q, want %q", *flow.TaskID, taskID)
	}
	if *flow.StatusCode != 201 {
		t.Errorf("StatusCode = %d, want %d", *flow.StatusCode, 201)
	}
	if *flow.Model != "gpt-4-turbo" {
		t.Errorf("Model = %q, want %q", *flow.Model, "gpt-4-turbo")
	}
	if *flow.InputTokens != 200 {
		t.Errorf("InputTokens = %d, want %d", *flow.InputTokens, 200)
	}
	if *flow.OutputTokens != 100 {
		t.Errorf("OutputTokens = %d, want %d", *flow.OutputTokens, 100)
	}
	if *flow.DurationMs != 500 {
		t.Errorf("DurationMs = %d, want %d", *flow.DurationMs, 500)
	}
	if !flow.IsSSE {
		t.Error("IsSSE should be true")
	}
}

func TestLoadFlow_AnthropicSuccess(t *testing.T) {
	flow := LoadFlow(t, "anthropic_success")

	if flow.ID != "flow-anthropic-001" {
		t.Errorf("ID = %q, want %q", flow.ID, "flow-anthropic-001")
	}
	if flow.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", flow.Provider, "anthropic")
	}
	if flow.Host != "api.anthropic.com" {
		t.Errorf("Host = %q, want %q", flow.Host, "api.anthropic.com")
	}
	if *flow.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want %d", *flow.StatusCode, 200)
	}
	if *flow.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want %d", *flow.InputTokens, 100)
	}
	if *flow.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want %d", *flow.OutputTokens, 50)
	}
	if *flow.CacheCreationTokens != 10 {
		t.Errorf("CacheCreationTokens = %d, want %d", *flow.CacheCreationTokens, 10)
	}
	if flow.IsSSE {
		t.Error("IsSSE should be false for non-streaming")
	}
}

func TestLoadFlow_AnthropicStreaming(t *testing.T) {
	flow := LoadFlow(t, "anthropic_streaming")

	if flow.ID != "flow-anthropic-stream-001" {
		t.Errorf("ID = %q, want %q", flow.ID, "flow-anthropic-stream-001")
	}
	if !flow.IsSSE {
		t.Error("IsSSE should be true for streaming")
	}
	if *flow.InputTokens != 150 {
		t.Errorf("InputTokens = %d, want %d", *flow.InputTokens, 150)
	}
}

func TestLoadFlow_AllProviders(t *testing.T) {
	tests := []struct {
		name     string
		provider string
	}{
		{"anthropic_success", "anthropic"},
		{"openai_success", "openai"},
		{"bedrock_success", "bedrock"},
		{"gemini_success", "gemini"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flow := LoadFlow(t, tt.name)

			if flow.Provider != tt.provider {
				t.Errorf("Provider = %q, want %q", flow.Provider, tt.provider)
			}
			if flow.FlowIntegrity != "complete" {
				t.Errorf("FlowIntegrity = %q, want %q", flow.FlowIntegrity, "complete")
			}
		})
	}
}

func TestLoadFlow_Error500(t *testing.T) {
	flow := LoadFlow(t, "error_500")

	if *flow.StatusCode != 500 {
		t.Errorf("StatusCode = %d, want %d", *flow.StatusCode, 500)
	}
	if flow.InputTokens != nil {
		t.Error("InputTokens should be nil for error response")
	}
	if flow.OutputTokens != nil {
		t.Error("OutputTokens should be nil for error response")
	}
}

func TestLoadSSE_Anthropic(t *testing.T) {
	sse := LoadSSE(t, "anthropic_conversation")

	if !strings.Contains(sse, "event: message_start") {
		t.Error("SSE should contain message_start event")
	}
	if !strings.Contains(sse, "event: content_block_delta") {
		t.Error("SSE should contain content_block_delta event")
	}
	if !strings.Contains(sse, "event: message_stop") {
		t.Error("SSE should contain message_stop event")
	}
	if !strings.Contains(sse, "claude-sonnet-4-20250514") {
		t.Error("SSE should contain model name")
	}
}

func TestLoadSSE_OpenAI(t *testing.T) {
	sse := LoadSSE(t, "openai_stream")

	if !strings.Contains(sse, "data: {") {
		t.Error("SSE should contain data lines")
	}
	if !strings.Contains(sse, "gpt-4o") {
		t.Error("SSE should contain model name")
	}
	if !strings.Contains(sse, "[DONE]") {
		t.Error("SSE should contain [DONE] marker")
	}
}

func TestLoadSSE_Gemini(t *testing.T) {
	sse := LoadSSE(t, "gemini_stream")

	if !strings.Contains(sse, "data: {") {
		t.Error("SSE should contain data lines")
	}
	if !strings.Contains(sse, "gemini-1.5-pro") {
		t.Error("SSE should contain model name")
	}
	if !strings.Contains(sse, "usageMetadata") {
		t.Error("SSE should contain usage metadata")
	}
}

func TestLoadResponse_Anthropic(t *testing.T) {
	resp := LoadResponse(t, "anthropic")

	if !strings.Contains(string(resp), "claude-sonnet-4-20250514") {
		t.Error("Response should contain model name")
	}
	if !strings.Contains(string(resp), "input_tokens") {
		t.Error("Response should contain input_tokens")
	}
	if !strings.Contains(string(resp), "output_tokens") {
		t.Error("Response should contain output_tokens")
	}
}

func TestLoadResponse_OpenAI(t *testing.T) {
	resp := LoadResponse(t, "openai")

	if !strings.Contains(string(resp), "gpt-4o") {
		t.Error("Response should contain model name")
	}
	if !strings.Contains(string(resp), "prompt_tokens") {
		t.Error("Response should contain prompt_tokens")
	}
	if !strings.Contains(string(resp), "completion_tokens") {
		t.Error("Response should contain completion_tokens")
	}
}

func TestLoadResponse_Bedrock(t *testing.T) {
	resp := LoadResponse(t, "bedrock")

	if !strings.Contains(string(resp), "inputTokens") {
		t.Error("Response should contain inputTokens")
	}
	if !strings.Contains(string(resp), "outputTokens") {
		t.Error("Response should contain outputTokens")
	}
}

func TestLoadResponse_Gemini(t *testing.T) {
	resp := LoadResponse(t, "gemini")

	if !strings.Contains(string(resp), "gemini-1.5-pro") {
		t.Error("Response should contain model name")
	}
	if !strings.Contains(string(resp), "promptTokenCount") {
		t.Error("Response should contain promptTokenCount")
	}
	if !strings.Contains(string(resp), "candidatesTokenCount") {
		t.Error("Response should contain candidatesTokenCount")
	}
}

func TestFlowBuilder_WithCacheTokens(t *testing.T) {
	flow := NewFlow().WithCacheTokens(50, 25).Build()

	if *flow.CacheCreationTokens != 50 {
		t.Errorf("CacheCreationTokens = %d, want %d", *flow.CacheCreationTokens, 50)
	}
	if *flow.CacheReadTokens != 25 {
		t.Errorf("CacheReadTokens = %d, want %d", *flow.CacheReadTokens, 25)
	}
}

func TestFlowBuilder_WithIntegrity(t *testing.T) {
	tests := []string{"complete", "partial", "corrupted", "interrupted"}

	for _, integrity := range tests {
		t.Run(integrity, func(t *testing.T) {
			flow := NewFlow().WithIntegrity(integrity).Build()
			if flow.FlowIntegrity != integrity {
				t.Errorf("FlowIntegrity = %q, want %q", flow.FlowIntegrity, integrity)
			}
		})
	}
}

func TestFlowBuilder_WithRequestBody(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"Hello"}]}`
	flow := NewFlow().WithRequestBody(body).Build()

	if *flow.RequestBody != body {
		t.Errorf("RequestBody = %q, want %q", *flow.RequestBody, body)
	}
}

func TestFlowBuilder_WithResponseBody(t *testing.T) {
	body := `{"content":"Hello back!"}`
	flow := NewFlow().WithResponseBody(body).Build()

	if *flow.ResponseBody != body {
		t.Errorf("ResponseBody = %q, want %q", *flow.ResponseBody, body)
	}
}
