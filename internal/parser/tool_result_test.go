package parser

import (
	"testing"
)

func TestExtractToolResults_Success(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"messages": [
			{
				"role": "user",
				"content": "Read the file /etc/hosts"
			},
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "toolu_abc123",
						"name": "read_file",
						"input": {"path": "/etc/hosts"}
					}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_abc123",
						"content": "127.0.0.1 localhost"
					}
				]
			}
		]
	}`)

	results := ExtractToolResults(body)

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}

	r := results[0]
	if r.ToolUseID != "toolu_abc123" {
		t.Errorf("ToolUseID = %q, want %q", r.ToolUseID, "toolu_abc123")
	}
	if r.IsError {
		t.Error("IsError = true, want false")
	}
}

func TestExtractToolResults_Error(t *testing.T) {
	body := []byte(`{
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_err456",
						"is_error": true,
						"content": "file not found"
					}
				]
			}
		]
	}`)

	results := ExtractToolResults(body)

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}

	r := results[0]
	if r.ToolUseID != "toolu_err456" {
		t.Errorf("ToolUseID = %q, want %q", r.ToolUseID, "toolu_err456")
	}
	if !r.IsError {
		t.Error("IsError = false, want true")
	}
	if r.Content == nil || *r.Content != "file not found" {
		t.Errorf("Content = %v, want 'file not found'", r.Content)
	}
}

func TestExtractToolResults_Multiple(t *testing.T) {
	body := []byte(`{
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_1",
						"content": "ok"
					},
					{
						"type": "tool_result",
						"tool_use_id": "toolu_2",
						"is_error": true,
						"content": "timeout"
					}
				]
			}
		]
	}`)

	results := ExtractToolResults(body)

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	if results[0].ToolUseID != "toolu_1" || results[0].IsError {
		t.Errorf("first result: ToolUseID=%q IsError=%v", results[0].ToolUseID, results[0].IsError)
	}
	if results[1].ToolUseID != "toolu_2" || !results[1].IsError {
		t.Errorf("second result: ToolUseID=%q IsError=%v", results[1].ToolUseID, results[1].IsError)
	}
}

func TestExtractToolResults_NoToolResults(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"messages": [
			{
				"role": "user",
				"content": "Hello, how are you?"
			}
		]
	}`)

	results := ExtractToolResults(body)
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

func TestExtractToolResults_MalformedJSON(t *testing.T) {
	results := ExtractToolResults([]byte(`not json at all`))
	if len(results) != 0 {
		t.Errorf("got %d results, want 0 for malformed JSON", len(results))
	}
}

func TestExtractToolResults_EmptyBody(t *testing.T) {
	results := ExtractToolResults(nil)
	if len(results) != 0 {
		t.Errorf("got %d results, want 0 for nil body", len(results))
	}

	results = ExtractToolResults([]byte{})
	if len(results) != 0 {
		t.Errorf("got %d results, want 0 for empty body", len(results))
	}
}

func TestExtractToolResults_MixedContentBlocks(t *testing.T) {
	// content array with tool_result mixed with other block types
	body := []byte(`{
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "text",
						"text": "Here's the result:"
					},
					{
						"type": "tool_result",
						"tool_use_id": "toolu_mixed",
						"content": "done"
					}
				]
			}
		]
	}`)

	results := ExtractToolResults(body)

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].ToolUseID != "toolu_mixed" {
		t.Errorf("ToolUseID = %q, want %q", results[0].ToolUseID, "toolu_mixed")
	}
}

func TestExtractToolResults_SkipsMissingToolUseID(t *testing.T) {
	body := []byte(`{
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"content": "no id"
					}
				]
			}
		]
	}`)

	results := ExtractToolResults(body)
	if len(results) != 0 {
		t.Errorf("got %d results, want 0 for missing tool_use_id", len(results))
	}
}
