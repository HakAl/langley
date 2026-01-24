package ws

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/langley/internal/config"
	"github.com/anthropics/langley/internal/store"
)

func testConfig() *config.Config {
	return &config.Config{
		Auth: config.AuthConfig{
			Token: "test-token",
		},
	}
}

func TestNewHub(t *testing.T) {
	cfg := testConfig()
	hub := NewHub(cfg, nil)

	if hub == nil {
		t.Fatal("NewHub returned nil")
	}
	if hub.clients == nil {
		t.Error("clients map not initialized")
	}
	if hub.broadcast == nil {
		t.Error("broadcast channel not initialized")
	}
}

func TestHubClientCount(t *testing.T) {
	cfg := testConfig()
	hub := NewHub(cfg, slog.Default())

	if hub.ClientCount() != 0 {
		t.Errorf("ClientCount() = %d, want 0", hub.ClientCount())
	}
}

func TestBroadcast(t *testing.T) {
	cfg := testConfig()
	hub := NewHub(cfg, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)

	// Give hub time to start
	time.Sleep(10 * time.Millisecond)

	// Should not block even with no clients
	hub.Broadcast(&Message{
		Type:      MessageTypePing,
		Timestamp: time.Now(),
	})

	// Verify no panic
}

func TestBroadcastFlowStart(t *testing.T) {
	cfg := testConfig()
	hub := NewHub(cfg, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)
	time.Sleep(10 * time.Millisecond)

	flow := &store.Flow{
		ID:     "flow-123",
		Host:   "api.anthropic.com",
		Method: "POST",
		Path:   "/v1/messages",
	}

	// Should not panic
	hub.BroadcastFlowStart(flow)
	hub.BroadcastFlowUpdate(flow)
	hub.BroadcastFlowComplete(flow)
}

func TestBroadcastEvent(t *testing.T) {
	cfg := testConfig()
	hub := NewHub(cfg, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)
	time.Sleep(10 * time.Millisecond)

	event := &store.Event{
		ID:        "event-1",
		FlowID:    "flow-1",
		EventType: "message_start",
	}

	// Should not panic
	hub.BroadcastEvent(event)
}

// TestConcurrentBroadcast verifies no race condition when broadcasting
// while clients connect/disconnect.
func TestConcurrentBroadcast(t *testing.T) {
	cfg := testConfig()
	hub := NewHub(cfg, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)
	time.Sleep(10 * time.Millisecond)

	// Simulate concurrent operations
	var wg sync.WaitGroup
	done := make(chan struct{})

	// Broadcaster goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			select {
			case <-done:
				return
			default:
				hub.Broadcast(&Message{
					Type:      MessageTypePing,
					Timestamp: time.Now(),
				})
			}
		}
	}()

	// Simulate client registration/unregistration
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			select {
			case <-done:
				return
			default:
				client := &Client{
					hub:  hub,
					send: make(chan []byte, 256),
				}
				hub.register <- client
				time.Sleep(time.Microsecond)
				hub.unregister <- client
			}
		}
	}()

	// Wait for operations or timeout
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("test timed out - possible deadlock")
	}
}

// TestSlowClientRemoval verifies that slow clients are removed
// without blocking the broadcast to other clients.
func TestSlowClientRemoval(t *testing.T) {
	cfg := testConfig()
	hub := NewHub(cfg, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)
	time.Sleep(10 * time.Millisecond)

	// Create a "slow" client with a tiny buffer that will fill up
	slowClient := &Client{
		hub:  hub,
		send: make(chan []byte, 1), // Very small buffer
	}
	hub.register <- slowClient
	time.Sleep(10 * time.Millisecond)

	if hub.ClientCount() != 1 {
		t.Fatalf("expected 1 client, got %d", hub.ClientCount())
	}

	// Flood with messages - slow client should be removed
	for i := 0; i < 10; i++ {
		hub.Broadcast(&Message{
			Type:      MessageTypePing,
			Timestamp: time.Now(),
		})
	}

	// Give hub time to process
	time.Sleep(50 * time.Millisecond)

	// Slow client should have been removed
	if hub.ClientCount() != 0 {
		t.Errorf("slow client should have been removed, got %d clients", hub.ClientCount())
	}
}

// TestGracefulShutdown verifies hub cleans up on context cancellation.
func TestGracefulShutdown(t *testing.T) {
	cfg := testConfig()
	hub := NewHub(cfg, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		hub.Run(ctx)
		close(done)
	}()

	time.Sleep(10 * time.Millisecond)

	// Add some clients
	for i := 0; i < 3; i++ {
		client := &Client{
			hub:  hub,
			send: make(chan []byte, 256),
		}
		hub.register <- client
	}

	time.Sleep(10 * time.Millisecond)
	if hub.ClientCount() != 3 {
		t.Fatalf("expected 3 clients, got %d", hub.ClientCount())
	}

	// Cancel context - should cleanup
	cancel()

	select {
	case <-done:
		// Hub exited
	case <-time.After(time.Second):
		t.Fatal("hub did not exit on context cancellation")
	}

	// All clients should be cleaned up
	if hub.ClientCount() != 0 {
		t.Errorf("expected 0 clients after shutdown, got %d", hub.ClientCount())
	}
}

// TestFlowToSummary verifies flow conversion for WebSocket broadcast.
func TestFlowToSummary(t *testing.T) {
	statusCode := 200
	durationMs := int64(1500)
	taskID := "task-123"
	model := "claude-3-opus"
	inputTokens := 100
	outputTokens := 500
	totalCost := 0.05

	flow := &store.Flow{
		ID:           "flow-1",
		Host:         "api.anthropic.com",
		Method:       "POST",
		Path:         "/v1/messages",
		StatusCode:   &statusCode,
		IsSSE:        true,
		DurationMs:   &durationMs,
		TaskID:       &taskID,
		Model:        &model,
		InputTokens:  &inputTokens,
		OutputTokens: &outputTokens,
		TotalCost:    &totalCost,
	}

	summary := flowToSummary(flow)

	if summary["id"] != "flow-1" {
		t.Errorf("id = %v, want flow-1", summary["id"])
	}
	if summary["host"] != "api.anthropic.com" {
		t.Errorf("host = %v", summary["host"])
	}
	if summary["status_code"] != 200 {
		t.Errorf("status_code = %v, want 200", summary["status_code"])
	}
	if summary["is_sse"] != true {
		t.Errorf("is_sse = %v, want true", summary["is_sse"])
	}
	if summary["model"] != "claude-3-opus" {
		t.Errorf("model = %v", summary["model"])
	}
	if summary["total_cost"] != 0.05 {
		t.Errorf("total_cost = %v, want 0.05", summary["total_cost"])
	}
}

// TestFlowToSummaryNilFields verifies nil pointer handling.
func TestFlowToSummaryNilFields(t *testing.T) {
	flow := &store.Flow{
		ID:     "flow-2",
		Host:   "api.openai.com",
		Method: "POST",
		Path:   "/v1/chat/completions",
		// All optional fields are nil
	}

	summary := flowToSummary(flow)

	// Required fields should be present
	if summary["id"] != "flow-2" {
		t.Errorf("id = %v", summary["id"])
	}

	// Optional fields should not be present
	if _, ok := summary["status_code"]; ok {
		t.Error("status_code should not be present when nil")
	}
	if _, ok := summary["model"]; ok {
		t.Error("model should not be present when nil")
	}
	if _, ok := summary["total_cost"]; ok {
		t.Error("total_cost should not be present when nil")
	}
}

// BenchmarkBroadcast measures broadcast performance.
func BenchmarkBroadcast(b *testing.B) {
	cfg := testConfig()
	hub := NewHub(cfg, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)
	time.Sleep(10 * time.Millisecond)

	// Add some clients
	for i := 0; i < 10; i++ {
		client := &Client{
			hub:  hub,
			send: make(chan []byte, 256),
		}
		hub.register <- client
		// Drain client channel
		go func(c *Client) {
			for range c.send {
			}
		}(client)
	}

	time.Sleep(10 * time.Millisecond)

	msg := &Message{
		Type:      MessageTypePing,
		Timestamp: time.Now(),
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		hub.Broadcast(msg)
	}
}
