package parser

import "encoding/json"

// ExtractToolUsesFromJSON extracts tool invocations from a non-streaming JSON
// response body. It auto-detects the format (Anthropic or OpenAI) from the
// JSON structure and returns []*ToolUse (same struct used by SSE extraction).
// Returns nil for empty, malformed, or unrecognized bodies.
func ExtractToolUsesFromJSON(body []byte) []*ToolUse {
	if len(body) == 0 {
		return nil
	}

	// Quick-reject non-JSON (must start with '{')
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case '{':
			goto parse
		default:
			return nil
		}
	}
	return nil

parse:
	// Try Anthropic format first (has top-level "content" array)
	if tools := extractAnthropicToolsFromJSON(body); tools != nil {
		return tools
	}

	// Try OpenAI format (has "choices" array)
	if tools := extractOpenAIToolsFromJSON(body); tools != nil {
		return tools
	}

	return nil
}

// extractAnthropicToolsFromJSON parses Anthropic-format responses:
//
//	{ "content": [ { "type": "tool_use", "id": "...", "name": "...", "input": {...} }, ... ] }
func extractAnthropicToolsFromJSON(body []byte) []*ToolUse {
	var resp struct {
		Content []struct {
			Type  string                 `json:"type"`
			ID    string                 `json:"id"`
			Name  string                 `json:"name"`
			Input map[string]interface{} `json:"input"`
		} `json:"content"`
	}

	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Content) == 0 {
		return nil
	}

	var tools []*ToolUse
	for _, block := range resp.Content {
		if block.Type != "tool_use" {
			continue
		}
		if block.ID == "" || block.Name == "" {
			continue
		}
		tools = append(tools, &ToolUse{
			ID:    block.ID,
			Name:  block.Name,
			Input: block.Input,
		})
	}

	return tools
}

// extractOpenAIToolsFromJSON parses OpenAI-format responses:
//
//	{ "choices": [ { "message": { "tool_calls": [ { "id": "...", "function": { "name": "...", "arguments": "{...}" } } ] } } ] }
func extractOpenAIToolsFromJSON(body []byte) []*ToolUse {
	var resp struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Choices) == 0 {
		return nil
	}

	var tools []*ToolUse
	for _, choice := range resp.Choices {
		for _, call := range choice.Message.ToolCalls {
			if call.ID == "" || call.Function.Name == "" {
				continue
			}
			tool := &ToolUse{
				ID:   call.ID,
				Name: call.Function.Name,
			}

			// Decode arguments JSON string into map
			if call.Function.Arguments != "" {
				var args map[string]interface{}
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err == nil {
					tool.Input = args
				}
				// Malformed arguments: keep tool with nil Input rather than dropping it
			}

			tools = append(tools, tool)
		}
	}

	return tools
}
