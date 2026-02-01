package parser

import "encoding/json"

// ToolResult represents a tool_result block extracted from a request body.
type ToolResult struct {
	ToolUseID string  // The tool_use_id this result corresponds to
	IsError   bool    // Whether the tool reported an error
	Content   *string // Optional error content
}

// ExtractToolResults parses an Anthropic API request body for tool_result blocks.
// Returns nil for non-JSON bodies or bodies without tool_results.
func ExtractToolResults(body []byte) []*ToolResult {
	if len(body) == 0 {
		return nil
	}

	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}

	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}

	var results []*ToolResult
	for _, msg := range req.Messages {
		// content can be a string or an array of content blocks
		var blocks []struct {
			Type      string          `json:"type"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
			Content   json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(msg.Content, &blocks); err != nil {
			// content is a string, not an array — skip
			continue
		}

		for _, block := range blocks {
			if block.Type != "tool_result" || block.ToolUseID == "" {
				continue
			}

			result := &ToolResult{
				ToolUseID: block.ToolUseID,
				IsError:   block.IsError,
			}

			// Extract error content if present
			if block.IsError && len(block.Content) > 0 {
				var text string
				if err := json.Unmarshal(block.Content, &text); err == nil && text != "" {
					result.Content = &text
				}
			}

			results = append(results, result)
		}
	}

	return results
}
