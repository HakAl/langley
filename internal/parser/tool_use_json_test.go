package parser

import (
	"testing"
)

func TestExtractToolUsesFromJSON_Anthropic_SingleTool(t *testing.T) {
	body := []byte(`{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"content": [
			{"type": "text", "text": "Let me check that."},
			{"type": "tool_use", "id": "toolu_01A", "name": "get_weather", "input": {"location": "London"}}
		]
	}`)

	tools := ExtractToolUsesFromJSON(body)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].ID != "toolu_01A" {
		t.Errorf("expected ID toolu_01A, got %s", tools[0].ID)
	}
	if tools[0].Name != "get_weather" {
		t.Errorf("expected name get_weather, got %s", tools[0].Name)
	}
	if tools[0].Input["location"] != "London" {
		t.Errorf("expected input location London, got %v", tools[0].Input["location"])
	}
}

func TestExtractToolUsesFromJSON_Anthropic_MultipleTools(t *testing.T) {
	body := []byte(`{
		"content": [
			{"type": "tool_use", "id": "toolu_01A", "name": "get_weather", "input": {"location": "London"}},
			{"type": "text", "text": "and also"},
			{"type": "tool_use", "id": "toolu_01B", "name": "get_time", "input": {"timezone": "UTC"}}
		]
	}`)

	tools := ExtractToolUsesFromJSON(body)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "get_weather" {
		t.Errorf("expected first tool get_weather, got %s", tools[0].Name)
	}
	if tools[1].Name != "get_time" {
		t.Errorf("expected second tool get_time, got %s", tools[1].Name)
	}
}

func TestExtractToolUsesFromJSON_Anthropic_NoTools(t *testing.T) {
	body := []byte(`{
		"content": [
			{"type": "text", "text": "Hello there!"}
		]
	}`)

	tools := ExtractToolUsesFromJSON(body)
	if len(tools) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(tools))
	}
}

func TestExtractToolUsesFromJSON_Anthropic_MissingIDSkipped(t *testing.T) {
	body := []byte(`{
		"content": [
			{"type": "tool_use", "id": "", "name": "get_weather", "input": {}},
			{"type": "tool_use", "id": "toolu_01A", "name": "get_time", "input": {}}
		]
	}`)

	tools := ExtractToolUsesFromJSON(body)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool (missing ID skipped), got %d", len(tools))
	}
	if tools[0].Name != "get_time" {
		t.Errorf("expected get_time, got %s", tools[0].Name)
	}
}

func TestExtractToolUsesFromJSON_Anthropic_MissingNameSkipped(t *testing.T) {
	body := []byte(`{
		"content": [
			{"type": "tool_use", "id": "toolu_01A", "name": "", "input": {}},
			{"type": "tool_use", "id": "toolu_01B", "name": "get_time", "input": {}}
		]
	}`)

	tools := ExtractToolUsesFromJSON(body)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool (missing name skipped), got %d", len(tools))
	}
	if tools[0].ID != "toolu_01B" {
		t.Errorf("expected toolu_01B, got %s", tools[0].ID)
	}
}

func TestExtractToolUsesFromJSON_OpenAI_SingleCall(t *testing.T) {
	body := []byte(`{
		"choices": [{
			"message": {
				"role": "assistant",
				"tool_calls": [{
					"id": "call_abc123",
					"type": "function",
					"function": {
						"name": "get_weather",
						"arguments": "{\"location\": \"London\"}"
					}
				}]
			}
		}]
	}`)

	tools := ExtractToolUsesFromJSON(body)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].ID != "call_abc123" {
		t.Errorf("expected ID call_abc123, got %s", tools[0].ID)
	}
	if tools[0].Name != "get_weather" {
		t.Errorf("expected name get_weather, got %s", tools[0].Name)
	}
	if tools[0].Input["location"] != "London" {
		t.Errorf("expected input location London, got %v", tools[0].Input["location"])
	}
}

func TestExtractToolUsesFromJSON_OpenAI_MultipleCalls(t *testing.T) {
	body := []byte(`{
		"choices": [{
			"message": {
				"tool_calls": [
					{"id": "call_1", "function": {"name": "get_weather", "arguments": "{}"}},
					{"id": "call_2", "function": {"name": "get_time", "arguments": "{}"}}
				]
			}
		}]
	}`)

	tools := ExtractToolUsesFromJSON(body)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "get_weather" {
		t.Errorf("expected first tool get_weather, got %s", tools[0].Name)
	}
	if tools[1].Name != "get_time" {
		t.Errorf("expected second tool get_time, got %s", tools[1].Name)
	}
}

func TestExtractToolUsesFromJSON_OpenAI_NoToolCalls(t *testing.T) {
	body := []byte(`{
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "Hello!"
			}
		}]
	}`)

	tools := ExtractToolUsesFromJSON(body)
	if len(tools) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(tools))
	}
}

func TestExtractToolUsesFromJSON_OpenAI_ArgumentsParsing(t *testing.T) {
	body := []byte(`{
		"choices": [{
			"message": {
				"tool_calls": [{
					"id": "call_1",
					"function": {
						"name": "search",
						"arguments": "{\"query\": \"test\", \"limit\": 10, \"nested\": {\"key\": \"value\"}}"
					}
				}]
			}
		}]
	}`)

	tools := ExtractToolUsesFromJSON(body)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Input["query"] != "test" {
		t.Errorf("expected query=test, got %v", tools[0].Input["query"])
	}
	// JSON numbers are float64
	if tools[0].Input["limit"] != float64(10) {
		t.Errorf("expected limit=10, got %v", tools[0].Input["limit"])
	}
	nested, ok := tools[0].Input["nested"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected nested map, got %T", tools[0].Input["nested"])
	}
	if nested["key"] != "value" {
		t.Errorf("expected nested key=value, got %v", nested["key"])
	}
}

func TestExtractToolUsesFromJSON_OpenAI_MalformedArguments(t *testing.T) {
	body := []byte(`{
		"choices": [{
			"message": {
				"tool_calls": [{
					"id": "call_1",
					"function": {
						"name": "search",
						"arguments": "not valid json {"
					}
				}]
			}
		}]
	}`)

	tools := ExtractToolUsesFromJSON(body)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool (malformed args kept with nil Input), got %d", len(tools))
	}
	if tools[0].Input != nil {
		t.Errorf("expected nil Input for malformed arguments, got %v", tools[0].Input)
	}
	if tools[0].Name != "search" {
		t.Errorf("expected name search, got %s", tools[0].Name)
	}
}

func TestExtractToolUsesFromJSON_EmptyBody(t *testing.T) {
	tools := ExtractToolUsesFromJSON(nil)
	if tools != nil {
		t.Fatalf("expected nil for nil body, got %v", tools)
	}

	tools = ExtractToolUsesFromJSON([]byte{})
	if tools != nil {
		t.Fatalf("expected nil for empty body, got %v", tools)
	}
}

func TestExtractToolUsesFromJSON_MalformedJSON(t *testing.T) {
	tools := ExtractToolUsesFromJSON([]byte(`{not valid json`))
	if tools != nil {
		t.Fatalf("expected nil for malformed JSON, got %v", tools)
	}
}

func TestExtractToolUsesFromJSON_UnrecognizedFormat(t *testing.T) {
	// Valid JSON but neither Anthropic nor OpenAI format
	body := []byte(`{"result": "success", "data": [1, 2, 3]}`)
	tools := ExtractToolUsesFromJSON(body)
	if tools != nil {
		t.Fatalf("expected nil for unrecognized format, got %v", tools)
	}
}

func TestExtractToolUsesFromJSON_NonJSON(t *testing.T) {
	tools := ExtractToolUsesFromJSON([]byte(`<html>not json</html>`))
	if tools != nil {
		t.Fatalf("expected nil for non-JSON body, got %v", tools)
	}
}

func TestExtractToolUsesFromJSON_Anthropic_EmptyInput(t *testing.T) {
	body := []byte(`{
		"content": [
			{"type": "tool_use", "id": "toolu_01A", "name": "list_files", "input": {}}
		]
	}`)

	tools := ExtractToolUsesFromJSON(body)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if len(tools[0].Input) != 0 {
		t.Errorf("expected empty input map, got %v", tools[0].Input)
	}
}
