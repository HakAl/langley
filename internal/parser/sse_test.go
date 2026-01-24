package parser

import (
	"strings"
	"testing"

	"github.com/anthropics/langley/internal/queue"
	"github.com/anthropics/langley/internal/store"
)

// mockLogger captures log messages for testing.
type mockLogger struct {
	warnings []string
}

func (m *mockLogger) Warn(msg string, args ...any) {
	m.warnings = append(m.warnings, msg)
}

func TestNewSSEParser(t *testing.T) {
	eventsCh := make(chan *store.Event, 10)
	p := NewSSEParser("flow-123", eventsCh)

	if p == nil {
		t.Fatal("NewSSEParser returned nil")
	}
	if p.flowID != "flow-123" {
		t.Errorf("flowID = %q, want %q", p.flowID, "flow-123")
	}
}

func TestParseBasicSSE(t *testing.T) {
	eventsCh := make(chan *store.Event, 10)
	p := NewSSEParser("flow-1", eventsCh)

	input := `event: message_start
data: {"type": "message", "id": "msg_123"}

event: content_block_delta
data: {"type": "content_block_delta", "delta": {"text": "Hello"}}

event: message_stop
data: {"type": "message_stop"}

`
	go func() {
		if err := p.Parse(strings.NewReader(input)); err != nil {
			t.Errorf("Parse() error = %v", err)
		}
	}()

	// Wait for parsing to complete
	<-p.Done()

	// Drain channel
	var events []*store.Event
	for len(eventsCh) > 0 {
		events = append(events, <-eventsCh)
	}

	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}

	// Verify event types
	expectedTypes := []string{"message_start", "content_block_delta", "message_stop"}
	for i, e := range events {
		if e.EventType != expectedTypes[i] {
			t.Errorf("events[%d].EventType = %q, want %q", i, e.EventType, expectedTypes[i])
		}
	}

	// Verify sequences
	for i, e := range events {
		if e.Sequence != i+1 {
			t.Errorf("events[%d].Sequence = %d, want %d", i, e.Sequence, i+1)
		}
	}

	// Verify flow ID
	for _, e := range events {
		if e.FlowID != "flow-1" {
			t.Errorf("FlowID = %q, want %q", e.FlowID, "flow-1")
		}
	}
}

func TestParsePriorities(t *testing.T) {
	eventsCh := make(chan *store.Event, 10)
	p := NewSSEParser("flow-2", eventsCh)

	input := `event: message_start
data: {}

event: content_block_start
data: {}

event: content_block_delta
data: {}

event: error
data: {}

`
	go func() {
		p.Parse(strings.NewReader(input))
	}()

	var events []*store.Event
	for {
		select {
		case e := <-eventsCh:
			events = append(events, e)
		case <-p.Done():
			goto done
		}
	}
done:

	expectedPriorities := map[string]string{
		"message_start":       queue.PriorityHigh,
		"content_block_start": queue.PriorityMedium,
		"content_block_delta": queue.PriorityLow,
		"error":               queue.PriorityHigh,
	}

	for _, e := range events {
		want := expectedPriorities[e.EventType]
		if e.Priority != want {
			t.Errorf("event %q priority = %q, want %q", e.EventType, e.Priority, want)
		}
	}
}

func TestParseComments(t *testing.T) {
	eventsCh := make(chan *store.Event, 10)
	p := NewSSEParser("flow-3", eventsCh)

	// SSE comments start with : and should be ignored
	input := `: this is a comment
event: message_start
data: {"hello": "world"}
: another comment

`
	go func() {
		p.Parse(strings.NewReader(input))
	}()

	var events []*store.Event
	for {
		select {
		case e := <-eventsCh:
			events = append(events, e)
		case <-p.Done():
			goto done
		}
	}
done:

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (comments should be ignored)", len(events))
	}
}

func TestParseMultilineData(t *testing.T) {
	eventsCh := make(chan *store.Event, 10)
	p := NewSSEParser("flow-4", eventsCh)

	// Multiple data: lines should be concatenated with newlines
	input := `event: content
data: line1
data: line2
data: line3

`
	go func() {
		p.Parse(strings.NewReader(input))
	}()

	var events []*store.Event
	for {
		select {
		case e := <-eventsCh:
			events = append(events, e)
		case <-p.Done():
			goto done
		}
	}
done:

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}

	// The data should contain all lines joined
	raw, ok := events[0].EventData["raw"].(string)
	if !ok {
		t.Fatal("expected raw data in event")
	}
	if !strings.Contains(raw, "line1") || !strings.Contains(raw, "line2") || !strings.Contains(raw, "line3") {
		t.Errorf("multiline data not properly joined: %q", raw)
	}
}

func TestParseEventCountLimit(t *testing.T) {
	eventsCh := make(chan *store.Event, 20000)
	logger := &mockLogger{}
	p := NewSSEParserWithLogger("flow-5", eventsCh, logger)

	// Generate more than maxEventsPerFlow (10K) events
	var sb strings.Builder
	for i := 0; i < 10005; i++ {
		sb.WriteString("event: ping\ndata: {}\n\n")
	}

	go func() {
		p.Parse(strings.NewReader(sb.String()))
	}()

	var count int
	for {
		select {
		case <-eventsCh:
			count++
		case <-p.Done():
			goto done
		}
	}
done:

	// Should be capped at maxEventsPerFlow (10000)
	if count > maxEventsPerFlow {
		t.Errorf("got %d events, want <= %d", count, maxEventsPerFlow)
	}

	// Should have logged a warning
	if len(logger.warnings) == 0 {
		t.Error("expected warning about event count limit")
	}
}

func TestParseEventSizeLimit(t *testing.T) {
	eventsCh := make(chan *store.Event, 10)
	logger := &mockLogger{}
	p := NewSSEParserWithLogger("flow-6", eventsCh, logger)

	// Create an event with data exceeding maxEventDataSize (2MB)
	// Using multiple data: lines to accumulate size
	var sb strings.Builder
	sb.WriteString("event: large_event\n")
	chunk := strings.Repeat("x", 100000) // 100KB per line
	for i := 0; i < 25; i++ { // 25 * 100KB = 2.5MB > 2MB limit
		sb.WriteString("data: " + chunk + "\n")
	}
	sb.WriteString("\n")

	go func() {
		p.Parse(strings.NewReader(sb.String()))
	}()

	var events []*store.Event
	for {
		select {
		case e := <-eventsCh:
			events = append(events, e)
		case <-p.Done():
			goto done
		}
	}
done:

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}

	// Should be marked as truncated
	if truncated, ok := events[0].EventData["_truncated"].(bool); !ok || !truncated {
		t.Error("expected event to be marked as truncated")
	}

	// Should have logged a warning
	if len(logger.warnings) == 0 {
		t.Error("expected warning about event size limit")
	}
}

func TestExtractUsage(t *testing.T) {
	events := []*store.Event{
		{
			EventType: "message_start",
			EventData: map[string]interface{}{
				"message": map[string]interface{}{
					"usage": map[string]interface{}{
						"input_tokens":              float64(100),
						"cache_creation_input_tokens": float64(50),
						"cache_read_input_tokens":    float64(25),
					},
				},
			},
		},
		{
			EventType: "message_delta",
			EventData: map[string]interface{}{
				"usage": map[string]interface{}{
					"output_tokens": float64(200),
				},
			},
		},
	}

	usage := ExtractUsage(events)

	if usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", usage.InputTokens)
	}
	if usage.OutputTokens != 200 {
		t.Errorf("OutputTokens = %d, want 200", usage.OutputTokens)
	}
	if usage.CacheCreationTokens != 50 {
		t.Errorf("CacheCreationTokens = %d, want 50", usage.CacheCreationTokens)
	}
	if usage.CacheReadTokens != 25 {
		t.Errorf("CacheReadTokens = %d, want 25", usage.CacheReadTokens)
	}
}

func TestExtractModel(t *testing.T) {
	events := []*store.Event{
		{
			EventType: "message_start",
			EventData: map[string]interface{}{
				"message": map[string]interface{}{
					"model": "claude-3-opus-20240229",
				},
			},
		},
		{
			EventType: "content_block_delta",
			EventData: map[string]interface{}{},
		},
	}

	model := ExtractModel(events)

	if model != "claude-3-opus-20240229" {
		t.Errorf("ExtractModel() = %q, want %q", model, "claude-3-opus-20240229")
	}
}

func TestExtractModelEmpty(t *testing.T) {
	events := []*store.Event{
		{
			EventType: "content_block_delta",
			EventData: map[string]interface{}{},
		},
	}

	model := ExtractModel(events)

	if model != "" {
		t.Errorf("ExtractModel() = %q, want empty", model)
	}
}

func TestExtractToolUses(t *testing.T) {
	events := []*store.Event{
		{
			EventType: "content_block_start",
			EventData: map[string]interface{}{
				"content_block": map[string]interface{}{
					"type": "tool_use",
					"id":   "toolu_123",
					"name": "read_file",
				},
			},
		},
		{
			EventType: "content_block_delta",
			EventData: map[string]interface{}{
				"index": float64(0),
				"delta": map[string]interface{}{
					"type":         "input_json_delta",
					"partial_json": `{"path": "/etc/`,
				},
			},
		},
		{
			EventType: "content_block_delta",
			EventData: map[string]interface{}{
				"index": float64(0),
				"delta": map[string]interface{}{
					"type":         "input_json_delta",
					"partial_json": `passwd"}`,
				},
			},
		},
		{
			EventType: "content_block_stop",
			EventData: map[string]interface{}{},
		},
	}

	tools := ExtractToolUses(events)

	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}

	tool := tools[0]
	if tool.ID != "toolu_123" {
		t.Errorf("tool.ID = %q, want %q", tool.ID, "toolu_123")
	}
	if tool.Name != "read_file" {
		t.Errorf("tool.Name = %q, want %q", tool.Name, "read_file")
	}
	if tool.Input["path"] != "/etc/passwd" {
		t.Errorf("tool.Input[path] = %v, want /etc/passwd", tool.Input["path"])
	}
}

func TestExtractToolUsesMultiple(t *testing.T) {
	events := []*store.Event{
		{
			EventType: "content_block_start",
			EventData: map[string]interface{}{
				"content_block": map[string]interface{}{
					"type": "tool_use",
					"id":   "toolu_1",
					"name": "tool_a",
				},
			},
		},
		{
			EventType: "content_block_start",
			EventData: map[string]interface{}{
				"content_block": map[string]interface{}{
					"type": "tool_use",
					"id":   "toolu_2",
					"name": "tool_b",
				},
			},
		},
	}

	tools := ExtractToolUses(events)

	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}
	if tools[0].Name != "tool_a" || tools[1].Name != "tool_b" {
		t.Errorf("tool names = [%q, %q], want [tool_a, tool_b]", tools[0].Name, tools[1].Name)
	}
}

func TestParseInvalidJSON(t *testing.T) {
	eventsCh := make(chan *store.Event, 10)
	p := NewSSEParser("flow-7", eventsCh)

	// Invalid JSON should be stored as raw
	input := `event: test
data: this is not valid json

`
	go func() {
		p.Parse(strings.NewReader(input))
	}()

	var events []*store.Event
	for {
		select {
		case e := <-eventsCh:
			events = append(events, e)
		case <-p.Done():
			goto done
		}
	}
done:

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}

	raw, ok := events[0].EventData["raw"].(string)
	if !ok {
		t.Fatal("expected raw data for invalid JSON")
	}
	if raw != " this is not valid json" {
		t.Errorf("raw = %q, want %q", raw, " this is not valid json")
	}
}

func TestParseNoTrailingNewline(t *testing.T) {
	eventsCh := make(chan *store.Event, 10)
	p := NewSSEParser("flow-8", eventsCh)

	// Event without trailing empty line should still be parsed
	input := `event: final
data: {"last": true}`

	go func() {
		p.Parse(strings.NewReader(input))
	}()

	var events []*store.Event
	for {
		select {
		case e := <-eventsCh:
			events = append(events, e)
		case <-p.Done():
			goto done
		}
	}
done:

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].EventType != "final" {
		t.Errorf("EventType = %q, want %q", events[0].EventType, "final")
	}
}

// Benchmark for SSE parsing performance
func BenchmarkParse(b *testing.B) {
	// Create a realistic SSE stream
	var sb strings.Builder
	sb.WriteString("event: message_start\ndata: {\"type\": \"message\", \"model\": \"claude-3-opus\"}\n\n")
	for i := 0; i < 100; i++ {
		sb.WriteString("event: content_block_delta\ndata: {\"delta\": {\"text\": \"Hello world \"}}\n\n")
	}
	sb.WriteString("event: message_delta\ndata: {\"usage\": {\"output_tokens\": 100}}\n\n")
	sb.WriteString("event: message_stop\ndata: {}\n\n")

	input := sb.String()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		eventsCh := make(chan *store.Event, 200)
		p := NewSSEParser("bench", eventsCh)

		go func() {
			p.Parse(strings.NewReader(input))
		}()

		// Drain channel
		for {
			select {
			case <-eventsCh:
			case <-p.Done():
				goto next
			}
		}
	next:
	}
}
