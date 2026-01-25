package provider

import (
	"encoding/json"
	"strings"
)

// Bedrock implements Provider for AWS Bedrock API.
type Bedrock struct{}

// Name returns "bedrock".
func (b *Bedrock) Name() string {
	return "bedrock"
}

// DetectHost returns true for AWS Bedrock runtime hosts.
// Bedrock uses regional endpoints: bedrock-runtime.{region}.amazonaws.com
func (b *Bedrock) DetectHost(host string) bool {
	return strings.Contains(host, "bedrock-runtime") && strings.Contains(host, "amazonaws.com")
}

// ParseUsage extracts token usage from Bedrock responses.
// Bedrock supports two APIs:
// - Converse API: usage in top-level "usage" object
// - InvokeModel API: passes through model's native format
func (b *Bedrock) ParseUsage(body []byte, isSSE bool) (*Usage, error) {
	if isSSE {
		return b.parseSSE(body)
	}
	return b.parseJSON(body)
}

// parseJSON extracts usage from a non-streaming Bedrock response.
func (b *Bedrock) parseJSON(body []byte) (*Usage, error) {
	// Parse into a struct that can handle all Bedrock response formats
	var resp struct {
		// Converse API format
		Usage *struct {
			InputTokens  int `json:"inputTokens"`
			OutputTokens int `json:"outputTokens"`
		} `json:"usage"`
		// InvokeModel API format (Bedrock wrapper fields)
		InputTokenCount  int `json:"inputTokenCount"`
		OutputTokenCount int `json:"outputTokenCount"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	// Check Converse API format (inputTokens with camelCase)
	if resp.Usage != nil && (resp.Usage.InputTokens > 0 || resp.Usage.OutputTokens > 0) {
		return &Usage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		}, nil
	}

	// Check InvokeModel API format (Bedrock wrapper fields)
	if resp.InputTokenCount > 0 || resp.OutputTokenCount > 0 {
		return &Usage{
			InputTokens:  resp.InputTokenCount,
			OutputTokens: resp.OutputTokenCount,
		}, nil
	}

	// Try Claude's native format passed through InvokeModel
	var claudeResp struct {
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(body, &claudeResp); err != nil {
		return nil, err
	}

	if claudeResp.Usage != nil {
		return &Usage{
			InputTokens:  claudeResp.Usage.InputTokens,
			OutputTokens: claudeResp.Usage.OutputTokens,
		}, nil
	}

	return &Usage{}, nil
}

// parseSSE extracts usage from a Bedrock streaming response.
// Bedrock ConverseStream uses contentBlockDelta events and a final metadata event.
func (b *Bedrock) parseSSE(body []byte) (*Usage, error) {
	usage := &Usage{}
	lines := strings.Split(string(body), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}

		// Try to parse as Bedrock event
		var event struct {
			// ConverseStream metadata event
			Metadata *struct {
				Usage *struct {
					InputTokens  int `json:"inputTokens"`
					OutputTokens int `json:"outputTokens"`
				} `json:"usage"`
			} `json:"metadata"`
			// InvokeModelWithResponseStream chunk
			Chunk *struct {
				Bytes string `json:"bytes"` // base64 encoded
			} `json:"chunk"`
			// Direct usage (some responses)
			InputTokenCount  int `json:"inputTokenCount"`
			OutputTokenCount int `json:"outputTokenCount"`
		}

		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		// ConverseStream metadata event contains usage
		if event.Metadata != nil && event.Metadata.Usage != nil {
			usage.InputTokens = event.Metadata.Usage.InputTokens
			usage.OutputTokens = event.Metadata.Usage.OutputTokens
		}

		// Direct token counts in event
		if event.InputTokenCount > 0 {
			usage.InputTokens = event.InputTokenCount
		}
		if event.OutputTokenCount > 0 {
			usage.OutputTokens = event.OutputTokenCount
		}
	}

	return usage, nil
}
