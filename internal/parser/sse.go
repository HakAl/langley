// Package parser provides parsers for SSE and Claude-specific event streams.
package parser

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/anthropics/langley/internal/queue"
	"github.com/anthropics/langley/internal/store"
	"github.com/google/uuid"
)

// EventPriority determines which events to keep under backpressure.
var EventPriority = map[string]string{
	// HIGH - critical for flow integrity and analytics
	"message_start": queue.PriorityHigh,
	"message_stop":  queue.PriorityHigh,
	"message_delta": queue.PriorityHigh, // Contains usage stats
	"error":         queue.PriorityHigh,

	// MEDIUM - structural events
	"content_block_start": queue.PriorityMedium,
	"content_block_stop":  queue.PriorityMedium,
	"ping":                queue.PriorityMedium,

	// LOW - can drop under pressure
	"content_block_delta": queue.PriorityLow,
}

// SSEParser parses Server-Sent Events streams.
type SSEParser struct {
	flowID    string
	sequence  int
	eventsCh  chan *store.Event
	doneCh    chan struct{}
}

// NewSSEParser creates a new SSE parser for a flow.
func NewSSEParser(flowID string, eventsCh chan *store.Event) *SSEParser {
	return &SSEParser{
		flowID:   flowID,
		sequence: 0,
		eventsCh: eventsCh,
		doneCh:   make(chan struct{}),
	}
}

// Parse reads SSE events from a reader and sends them to the events channel.
// Returns when the reader is exhausted or an error occurs.
func (p *SSEParser) Parse(r io.Reader) error {
	scanner := bufio.NewScanner(r)

	var eventType string
	var dataLines []string

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			// Empty line = end of event
			if eventType != "" && len(dataLines) > 0 {
				data := strings.Join(dataLines, "\n")
				p.emitEvent(eventType, data)
			}
			eventType = ""
			dataLines = nil
			continue
		}

		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data:"))
		} else if strings.HasPrefix(line, ":") {
			// Comment, ignore
			continue
		}
	}

	// Handle final event if no trailing newline
	if eventType != "" && len(dataLines) > 0 {
		data := strings.Join(dataLines, "\n")
		p.emitEvent(eventType, data)
	}

	close(p.doneCh)
	return scanner.Err()
}

// emitEvent creates and sends an event.
func (p *SSEParser) emitEvent(eventType, data string) {
	p.sequence++

	// Parse JSON data
	var eventData map[string]interface{}
	if err := json.Unmarshal([]byte(data), &eventData); err != nil {
		// Store raw data if not valid JSON
		eventData = map[string]interface{}{"raw": data}
	}

	// Determine priority
	priority, ok := EventPriority[eventType]
	if !ok {
		priority = queue.PriorityMedium
	}

	event := &store.Event{
		ID:            uuid.New().String(),
		FlowID:        p.flowID,
		Sequence:      p.sequence,
		Timestamp:     time.Now(),
		TimestampMono: time.Now().UnixNano(),
		EventType:     eventType,
		EventData:     eventData,
		Priority:      priority,
	}

	select {
	case p.eventsCh <- event:
	case <-p.doneCh:
	}
}

// Done returns a channel that's closed when parsing is complete.
func (p *SSEParser) Done() <-chan struct{} {
	return p.doneCh
}

// ClaudeUsage represents token usage from a Claude response.
type ClaudeUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// ClaudeMessage represents a parsed Claude message.
type ClaudeMessage struct {
	ID           string      `json:"id"`
	Type         string      `json:"type"`
	Role         string      `json:"role"`
	Model        string      `json:"model"`
	StopReason   string      `json:"stop_reason"`
	StopSequence string      `json:"stop_sequence"`
	Usage        ClaudeUsage `json:"usage"`
}

// ExtractUsage extracts usage statistics from Claude SSE events.
func ExtractUsage(events []*store.Event) *ClaudeUsage {
	var usage ClaudeUsage

	for _, event := range events {
		switch event.EventType {
		case "message_start":
			// message_start contains initial message metadata
			if msg, ok := event.EventData["message"].(map[string]interface{}); ok {
				if u, ok := msg["usage"].(map[string]interface{}); ok {
					if v, ok := u["input_tokens"].(float64); ok {
						usage.InputTokens = int(v)
					}
					if v, ok := u["cache_creation_input_tokens"].(float64); ok {
						usage.CacheCreationTokens = int(v)
					}
					if v, ok := u["cache_read_input_tokens"].(float64); ok {
						usage.CacheReadTokens = int(v)
					}
				}
			}
		case "message_delta":
			// message_delta contains final usage stats
			if u, ok := event.EventData["usage"].(map[string]interface{}); ok {
				if v, ok := u["output_tokens"].(float64); ok {
					usage.OutputTokens = int(v)
				}
			}
		}
	}

	return &usage
}

// ExtractModel extracts the model name from Claude SSE events.
func ExtractModel(events []*store.Event) string {
	for _, event := range events {
		if event.EventType == "message_start" {
			if msg, ok := event.EventData["message"].(map[string]interface{}); ok {
				if model, ok := msg["model"].(string); ok {
					return model
				}
			}
		}
	}
	return ""
}

// ToolUse represents a tool invocation extracted from Claude events.
type ToolUse struct {
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

// ExtractToolUses extracts tool invocations from Claude SSE events.
func ExtractToolUses(events []*store.Event) []*ToolUse {
	var tools []*ToolUse
	toolInputs := make(map[string]string) // ID -> accumulated input JSON

	for _, event := range events {
		switch event.EventType {
		case "content_block_start":
			if cb, ok := event.EventData["content_block"].(map[string]interface{}); ok {
				if cb["type"] == "tool_use" {
					tool := &ToolUse{
						ID:   getString(cb, "id"),
						Name: getString(cb, "name"),
					}
					if tool.ID != "" && tool.Name != "" {
						tools = append(tools, tool)
						toolInputs[tool.ID] = ""
					}
				}
			}
		case "content_block_delta":
			if delta, ok := event.EventData["delta"].(map[string]interface{}); ok {
				if delta["type"] == "input_json_delta" {
					// Find which tool this belongs to by index
					if idx, ok := event.EventData["index"].(float64); ok {
						if int(idx) < len(tools) {
							tool := tools[int(idx)]
							if partial, ok := delta["partial_json"].(string); ok {
								toolInputs[tool.ID] += partial
							}
						}
					}
				}
			}
		}
	}

	// Parse accumulated inputs
	for _, tool := range tools {
		if input, ok := toolInputs[tool.ID]; ok && input != "" {
			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(input), &parsed); err == nil {
				tool.Input = parsed
			}
		}
	}

	return tools
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
