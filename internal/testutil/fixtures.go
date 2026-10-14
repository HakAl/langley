// Package testutil provides shared test fixtures for consistent, realistic test data.
package testutil

import (
	"embed"
	"encoding/json"
	"path"
	"testing"
	"time"

	"github.com/HakAl/langley/internal/store"
)

//go:embed flows/*.json sse/*.txt responses/*.json
var fixtures embed.FS

// FlowBuilder provides a fluent API for building test flows.
type FlowBuilder struct {
	flow *store.Flow
}

// NewFlow creates a new FlowBuilder with sensible defaults.
func NewFlow() *FlowBuilder {
	now := time.Now()
	status := 200
	statusText := "OK"
	durationMs := int64(150)
	inputTokens := 100
	outputTokens := 50
	model := "claude-sonnet-4-20250514"

	return &FlowBuilder{
		flow: &store.Flow{
			ID:            "flow-test-001",
			Host:          "api.anthropic.com",
			Method:        "POST",
			Path:          "/v1/messages",
			URL:           "https://api.anthropic.com/v1/messages",
			Timestamp:     now,
			TimestampMono: now.UnixNano(),
			DurationMs:    &durationMs,
			StatusCode:    &status,
			StatusText:    &statusText,
			IsSSE:         false,
			FlowIntegrity: "complete",
			Provider:      "anthropic",
			InputTokens:   &inputTokens,
			OutputTokens:  &outputTokens,
			Model:         &model,
			CreatedAt:     now,
		},
	}
}

// WithID sets the flow ID.
func (b *FlowBuilder) WithID(id string) *FlowBuilder {
	b.flow.ID = id
	return b
}

// WithProvider sets the provider and updates host accordingly.
func (b *FlowBuilder) WithProvider(provider string) *FlowBuilder {
	b.flow.Provider = provider
	switch provider {
	case "anthropic":
		b.flow.Host = "api.anthropic.com"
		b.flow.URL = "https://api.anthropic.com/v1/messages"
		b.flow.Path = "/v1/messages"
	case "openai":
		b.flow.Host = "api.openai.com"
		b.flow.URL = "https://api.openai.com/v1/chat/completions"
		b.flow.Path = "/v1/chat/completions"
	case "bedrock":
		b.flow.Host = "bedrock-runtime.us-east-1.amazonaws.com"
		b.flow.URL = "https://bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude-3-sonnet-20240229-v1:0/converse"
		b.flow.Path = "/model/anthropic.claude-3-sonnet-20240229-v1:0/converse"
	case "gemini":
		b.flow.Host = "generativelanguage.googleapis.com"
		b.flow.URL = "https://generativelanguage.googleapis.com/v1beta/models/gemini-1.5-pro:generateContent"
		b.flow.Path = "/v1beta/models/gemini-1.5-pro:generateContent"
	}
	return b
}

// WithTaskID sets the task ID.
func (b *FlowBuilder) WithTaskID(id string) *FlowBuilder {
	b.flow.TaskID = &id
	return b
}

// WithStatus sets the HTTP status code and text.
func (b *FlowBuilder) WithStatus(code int) *FlowBuilder {
	b.flow.StatusCode = &code
	text := statusTextFor(code)
	b.flow.StatusText = &text
	return b
}

// Streaming marks the flow as SSE streaming.
func (b *FlowBuilder) Streaming() *FlowBuilder {
	b.flow.IsSSE = true
	return b
}

// WithModel sets the model name.
func (b *FlowBuilder) WithModel(model string) *FlowBuilder {
	b.flow.Model = &model
	return b
}

// WithTokens sets the input and output token counts.
func (b *FlowBuilder) WithTokens(input, output int) *FlowBuilder {
	b.flow.InputTokens = &input
	b.flow.OutputTokens = &output
	return b
}

// WithCacheTokens sets the cache token counts.
func (b *FlowBuilder) WithCacheTokens(creation, read int) *FlowBuilder {
	b.flow.CacheCreationTokens = &creation
	b.flow.CacheReadTokens = &read
	return b
}

// WithDuration sets the request duration in milliseconds.
func (b *FlowBuilder) WithDuration(ms int64) *FlowBuilder {
	b.flow.DurationMs = &ms
	return b
}

// WithIntegrity sets the flow integrity status.
func (b *FlowBuilder) WithIntegrity(integrity string) *FlowBuilder {
	b.flow.FlowIntegrity = integrity
	return b
}

// WithRequestBody sets the request body.
func (b *FlowBuilder) WithRequestBody(body string) *FlowBuilder {
	b.flow.RequestBody = &body
	return b
}

// WithResponseBody sets the response body.
func (b *FlowBuilder) WithResponseBody(body string) *FlowBuilder {
	b.flow.ResponseBody = &body
	return b
}

// Build returns the constructed Flow.
func (b *FlowBuilder) Build() *store.Flow {
	return b.flow
}

// LoadFlow loads a flow fixture from JSON file.
// The name should not include the .json extension.
func LoadFlow(t testing.TB, name string) *store.Flow {
	t.Helper()

	data, err := fixtures.ReadFile(path.Join("flows", name+".json"))
	if err != nil {
		t.Fatalf("failed to load flow fixture %q: %v", name, err)
	}

	var flowData flowJSON
	if err := json.Unmarshal(data, &flowData); err != nil {
		t.Fatalf("failed to parse flow fixture %q: %v", name, err)
	}

	return flowData.toFlow()
}

// LoadSSE loads an SSE stream fixture.
// The name should not include the .txt extension.
func LoadSSE(t testing.TB, name string) string {
	t.Helper()

	data, err := fixtures.ReadFile(path.Join("sse", name+".txt"))
	if err != nil {
		t.Fatalf("failed to load SSE fixture %q: %v", name, err)
	}

	return string(data)
}

// LoadResponse loads a provider response fixture.
// The name should not include the .json extension.
func LoadResponse(t testing.TB, name string) []byte {
	t.Helper()

	data, err := fixtures.ReadFile(path.Join("responses", name+".json"))
	if err != nil {
		t.Fatalf("failed to load response fixture %q: %v", name, err)
	}

	return data
}

// flowJSON represents the JSON structure of flow fixtures.
type flowJSON struct {
	ID                  string  `json:"id"`
	Host                string  `json:"host"`
	Method              string  `json:"method"`
	Path                string  `json:"path"`
	URL                 string  `json:"url"`
	Provider            string  `json:"provider"`
	StatusCode          *int    `json:"status_code,omitempty"`
	StatusText          *string `json:"status_text,omitempty"`
	IsSSE               bool    `json:"is_sse"`
	FlowIntegrity       string  `json:"flow_integrity"`
	InputTokens         *int    `json:"input_tokens,omitempty"`
	OutputTokens        *int    `json:"output_tokens,omitempty"`
	CacheCreationTokens *int    `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     *int    `json:"cache_read_tokens,omitempty"`
	Model               *string `json:"model,omitempty"`
	DurationMs          *int64  `json:"duration_ms,omitempty"`
}

func (f *flowJSON) toFlow() *store.Flow {
	now := time.Now()
	return &store.Flow{
		ID:                  f.ID,
		Host:                f.Host,
		Method:              f.Method,
		Path:                f.Path,
		URL:                 f.URL,
		Provider:            f.Provider,
		StatusCode:          f.StatusCode,
		StatusText:          f.StatusText,
		IsSSE:               f.IsSSE,
		FlowIntegrity:       f.FlowIntegrity,
		InputTokens:         f.InputTokens,
		OutputTokens:        f.OutputTokens,
		CacheCreationTokens: f.CacheCreationTokens,
		CacheReadTokens:     f.CacheReadTokens,
		Model:               f.Model,
		DurationMs:          f.DurationMs,
		Timestamp:           now,
		TimestampMono:       now.UnixNano(),
		CreatedAt:           now,
	}
}

func statusTextFor(code int) string {
	switch code {
	case 200:
		return "OK"
	case 201:
		return "Created"
	case 400:
		return "Bad Request"
	case 401:
		return "Unauthorized"
	case 403:
		return "Forbidden"
	case 404:
		return "Not Found"
	case 429:
		return "Too Many Requests"
	case 500:
		return "Internal Server Error"
	case 502:
		return "Bad Gateway"
	case 503:
		return "Service Unavailable"
	default:
		return "Unknown"
	}
}
