package provider

import (
	"encoding/json"
	"strings"
)

// Anthropic implements Provider for Anthropic's Claude API.
type Anthropic struct{}

// Name returns "anthropic".
func (a *Anthropic) Name() string {
	return "anthropic"
}

// DetectHost returns true for Anthropic API hosts.
func (a *Anthropic) DetectHost(host string) bool {
	return MatchDomainSuffix(host, "anthropic.com") || MatchDomainSuffix(host, "claude.ai")
}

// ParseUsage extracts token usage from Anthropic responses.
func (a *Anthropic) ParseUsage(body []byte, isSSE bool) (*Usage, error) {
	if isSSE {
		return a.parseSSE(body)
	}
	return a.parseJSON(body)
}

// parseJSON extracts usage from a non-streaming JSON response.
func (a *Anthropic) parseJSON(body []byte) (*Usage, error) {
	var response struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	return &Usage{
		Model:               response.Model,
		InputTokens:         response.Usage.InputTokens,
		OutputTokens:        response.Usage.OutputTokens,
		CacheCreationTokens: response.Usage.CacheCreationInputTokens,
		CacheReadTokens:     response.Usage.CacheReadInputTokens,
	}, nil
}

// parseSSE extracts usage from an SSE stream.
func (a *Anthropic) parseSSE(body []byte) (*Usage, error) {
	usage := &Usage{}
	lines := strings.Split(string(body), "\n")

	var currentEventType string
	var dataBuffer strings.Builder

	for _, line := range lines {
		if line == "" {
			// End of event, process it
			if currentEventType != "" && dataBuffer.Len() > 0 {
				a.processEvent(usage, currentEventType, dataBuffer.String())
			}
			currentEventType = ""
			dataBuffer.Reset()
			continue
		}

		if strings.HasPrefix(line, "event:") {
			currentEventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataBuffer.WriteString(strings.TrimPrefix(line, "data:"))
		}
	}

	// Handle final event if no trailing newline
	if currentEventType != "" && dataBuffer.Len() > 0 {
		a.processEvent(usage, currentEventType, dataBuffer.String())
	}

	return usage, nil
}

// processEvent extracts data from a single SSE event.
func (a *Anthropic) processEvent(usage *Usage, eventType, data string) {
	switch eventType {
	case "message_start":
		var event struct {
			Message struct {
				Model string `json:"model"`
				Usage struct {
					InputTokens              int `json:"input_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &event); err == nil {
			usage.Model = event.Message.Model
			usage.InputTokens = event.Message.Usage.InputTokens
			usage.CacheCreationTokens = event.Message.Usage.CacheCreationInputTokens
			usage.CacheReadTokens = event.Message.Usage.CacheReadInputTokens
		}

	case "message_delta":
		var event struct {
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &event); err == nil {
			usage.OutputTokens = event.Usage.OutputTokens
		}
	}
}
