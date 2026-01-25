package provider

import (
	"encoding/json"
	"strings"
)

// OpenAI implements Provider for OpenAI's API.
type OpenAI struct{}

// Name returns "openai".
func (o *OpenAI) Name() string {
	return "openai"
}

// DetectHost returns true for OpenAI API hosts.
func (o *OpenAI) DetectHost(host string) bool {
	return strings.Contains(host, "openai.com")
}

// ParseUsage extracts token usage from OpenAI responses.
func (o *OpenAI) ParseUsage(body []byte, isSSE bool) (*Usage, error) {
	if isSSE {
		return o.parseSSE(body)
	}
	return o.parseJSON(body)
}

// parseJSON extracts usage from a non-streaming JSON response.
func (o *OpenAI) parseJSON(body []byte) (*Usage, error) {
	var response struct {
		Model string `json:"model"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	return &Usage{
		Model:        response.Model,
		InputTokens:  response.Usage.PromptTokens,
		OutputTokens: response.Usage.CompletionTokens,
	}, nil
}

// parseSSE extracts usage from an SSE stream.
// OpenAI includes usage in the final chunk when stream_options.include_usage is true.
// The stream ends with "data: [DONE]".
func (o *OpenAI) parseSSE(body []byte) (*Usage, error) {
	usage := &Usage{}
	lines := strings.Split(string(body), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))

		// Skip the [DONE] marker
		if data == "[DONE]" {
			continue
		}

		// Try to parse as JSON chunk
		var chunk struct {
			Model string `json:"model"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}

		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // Skip malformed chunks
		}

		// Capture model from any chunk
		if chunk.Model != "" {
			usage.Model = chunk.Model
		}

		// Usage appears in the final chunk (when stream_options.include_usage is true)
		if chunk.Usage != nil {
			usage.InputTokens = chunk.Usage.PromptTokens
			usage.OutputTokens = chunk.Usage.CompletionTokens
		}
	}

	return usage, nil
}
