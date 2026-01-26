package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HakAl/langley/internal/config"
)

// testRetention returns a retention config for tests
func testRetention() *config.RetentionConfig {
	return &config.RetentionConfig{
		FlowsTTLDays:   7,
		EventsTTLDays:  3,
		DropLogTTLDays: 1,
	}
}

// setupTestDB creates an in-memory SQLite database for testing
func setupTestDB(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(":memory:", testRetention())
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
	})
	return store
}

// setupTestDBFile creates a file-based SQLite database for testing
func setupTestDBFile(t *testing.T) (*SQLiteStore, string) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := NewSQLiteStore(dbPath, testRetention())
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
	})
	return store, dbPath
}

func TestNewSQLiteStore(t *testing.T) {
	t.Parallel()

	t.Run("in-memory database", func(t *testing.T) {
		store := setupTestDB(t)
		if store == nil {
			t.Fatal("store is nil")
		}
		if store.db == nil {
			t.Fatal("db connection is nil")
		}
	})

	t.Run("file database", func(t *testing.T) {
		store, dbPath := setupTestDBFile(t)
		if store == nil {
			t.Fatal("store is nil")
		}
		// Verify file was created
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			t.Errorf("database file not created at %s", dbPath)
		}
	})

	t.Run("schema version created", func(t *testing.T) {
		store := setupTestDB(t)
		var version int
		err := store.db.QueryRow("SELECT version FROM schema_version WHERE id = 1").Scan(&version)
		if err != nil {
			t.Fatalf("failed to query schema version: %v", err)
		}
		if version < 1 {
			t.Errorf("schema version = %d, want >= 1", version)
		}
	})

	t.Run("tables created", func(t *testing.T) {
		store := setupTestDB(t)
		tables := []string{"flows", "events", "tool_invocations", "pricing", "drop_log"}
		for _, table := range tables {
			var name string
			err := store.db.QueryRow(
				"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
			).Scan(&name)
			if err != nil {
				t.Errorf("table %s not found: %v", table, err)
			}
		}
	})
}

func TestSaveFlow_GetFlow(t *testing.T) {
	t.Parallel()
	store := setupTestDB(t)
	ctx := context.Background()

	// Create a flow
	taskID := "task-123"
	taskSource := "explicit"
	statusCode := 200
	statusText := "200 OK"
	duration := int64(150)
	reqBody := `{"prompt": "hello"}`
	respBody := `{"response": "hi"}`
	inputTokens := 10
	outputTokens := 20
	totalCost := 0.001
	costSource := "exact"
	model := "claude-3-sonnet"

	flow := &Flow{
		ID:              "flow-1",
		TaskID:          &taskID,
		TaskSource:      &taskSource,
		Host:            "api.anthropic.com",
		Method:          "POST",
		Path:            "/v1/messages",
		URL:             "https://api.anthropic.com/v1/messages",
		Timestamp:       time.Now().Truncate(time.Microsecond),
		TimestampMono:   time.Now().UnixNano(),
		DurationMs:      &duration,
		StatusCode:      &statusCode,
		StatusText:      &statusText,
		IsSSE:           true,
		FlowIntegrity:   "complete",
		RequestBody:     &reqBody,
		ResponseBody:    &respBody,
		RequestHeaders:  map[string][]string{"Content-Type": {"application/json"}},
		ResponseHeaders: map[string][]string{"Content-Type": {"text/event-stream"}},
		InputTokens:     &inputTokens,
		OutputTokens:    &outputTokens,
		TotalCost:       &totalCost,
		CostSource:      &costSource,
		Model:           &model,
		Provider:        "anthropic",
	}

	// Save
	err := store.SaveFlow(ctx, flow)
	if err != nil {
		t.Fatalf("SaveFlow failed: %v", err)
	}

	// Retrieve
	got, err := store.GetFlow(ctx, "flow-1")
	if err != nil {
		t.Fatalf("GetFlow failed: %v", err)
	}

	// Verify fields
	if got.ID != flow.ID {
		t.Errorf("ID = %q, want %q", got.ID, flow.ID)
	}
	if got.Host != flow.Host {
		t.Errorf("Host = %q, want %q", got.Host, flow.Host)
	}
	if got.Method != flow.Method {
		t.Errorf("Method = %q, want %q", got.Method, flow.Method)
	}
	if got.IsSSE != flow.IsSSE {
		t.Errorf("IsSSE = %v, want %v", got.IsSSE, flow.IsSSE)
	}
	if *got.TaskID != *flow.TaskID {
		t.Errorf("TaskID = %q, want %q", *got.TaskID, *flow.TaskID)
	}
	if *got.StatusCode != *flow.StatusCode {
		t.Errorf("StatusCode = %d, want %d", *got.StatusCode, *flow.StatusCode)
	}
	if *got.InputTokens != *flow.InputTokens {
		t.Errorf("InputTokens = %d, want %d", *got.InputTokens, *flow.InputTokens)
	}
	if *got.Model != *flow.Model {
		t.Errorf("Model = %q, want %q", *got.Model, *flow.Model)
	}
	if got.Provider != flow.Provider {
		t.Errorf("Provider = %q, want %q", got.Provider, flow.Provider)
	}
}

func TestUpdateFlow(t *testing.T) {
	t.Parallel()
	store := setupTestDB(t)
	ctx := context.Background()

	// Create initial flow
	flow := &Flow{
		ID:            "flow-update-1",
		Host:          "api.anthropic.com",
		Method:        "POST",
		Path:          "/v1/messages",
		URL:           "https://api.anthropic.com/v1/messages",
		Timestamp:     time.Now(),
		TimestampMono: time.Now().UnixNano(),
		FlowIntegrity: "partial",
		Provider:      "anthropic",
	}

	err := store.SaveFlow(ctx, flow)
	if err != nil {
		t.Fatalf("SaveFlow failed: %v", err)
	}

	// Update with completion data
	statusCode := 200
	duration := int64(500)
	outputTokens := 100
	flow.StatusCode = &statusCode
	flow.DurationMs = &duration
	flow.OutputTokens = &outputTokens
	flow.FlowIntegrity = "complete"

	err = store.UpdateFlow(ctx, flow)
	if err != nil {
		t.Fatalf("UpdateFlow failed: %v", err)
	}

	// Verify
	got, err := store.GetFlow(ctx, "flow-update-1")
	if err != nil {
		t.Fatalf("GetFlow failed: %v", err)
	}
	if *got.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", *got.StatusCode)
	}
	if *got.DurationMs != 500 {
		t.Errorf("DurationMs = %d, want 500", *got.DurationMs)
	}
	if got.FlowIntegrity != "complete" {
		t.Errorf("FlowIntegrity = %q, want %q", got.FlowIntegrity, "complete")
	}
}

func TestDeleteFlow(t *testing.T) {
	t.Parallel()
	store := setupTestDB(t)
	ctx := context.Background()

	// Create flow
	flow := &Flow{
		ID:            "flow-delete-1",
		Host:          "api.anthropic.com",
		Method:        "POST",
		Path:          "/v1/messages",
		URL:           "https://api.anthropic.com/v1/messages",
		Timestamp:     time.Now(),
		TimestampMono: time.Now().UnixNano(),
		FlowIntegrity: "complete",
		Provider:      "anthropic",
	}

	err := store.SaveFlow(ctx, flow)
	if err != nil {
		t.Fatalf("SaveFlow failed: %v", err)
	}

	// Delete
	err = store.DeleteFlow(ctx, "flow-delete-1")
	if err != nil {
		t.Fatalf("DeleteFlow failed: %v", err)
	}

	// Verify deleted
	_, err = store.GetFlow(ctx, "flow-delete-1")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestListFlows(t *testing.T) {
	t.Parallel()
	store := setupTestDB(t)
	ctx := context.Background()

	// Create multiple flows
	hosts := []string{"api.anthropic.com", "api.openai.com", "api.anthropic.com"}
	taskIDs := []string{"task-1", "task-2", "task-1"}

	for i, host := range hosts {
		taskID := taskIDs[i]
		flow := &Flow{
			ID:            "flow-list-" + string(rune('a'+i)),
			TaskID:        &taskID,
			Host:          host,
			Method:        "POST",
			Path:          "/v1/messages",
			URL:           "https://" + host + "/v1/messages",
			Timestamp:     time.Now().Add(time.Duration(i) * time.Second),
			TimestampMono: time.Now().UnixNano(),
			FlowIntegrity: "complete",
			Provider:      "anthropic",
		}
		if err := store.SaveFlow(ctx, flow); err != nil {
			t.Fatalf("SaveFlow %d failed: %v", i, err)
		}
	}

	t.Run("no filter", func(t *testing.T) {
		flows, err := store.ListFlows(ctx, FlowFilter{})
		if err != nil {
			t.Fatalf("ListFlows failed: %v", err)
		}
		if len(flows) != 3 {
			t.Errorf("len(flows) = %d, want 3", len(flows))
		}
	})

	t.Run("filter by host", func(t *testing.T) {
		host := "api.anthropic.com"
		flows, err := store.ListFlows(ctx, FlowFilter{Host: &host})
		if err != nil {
			t.Fatalf("ListFlows failed: %v", err)
		}
		if len(flows) != 2 {
			t.Errorf("len(flows) = %d, want 2", len(flows))
		}
	})

	t.Run("filter by task ID", func(t *testing.T) {
		taskID := "task-1"
		flows, err := store.ListFlows(ctx, FlowFilter{TaskID: &taskID})
		if err != nil {
			t.Fatalf("ListFlows failed: %v", err)
		}
		if len(flows) != 2 {
			t.Errorf("len(flows) = %d, want 2", len(flows))
		}
	})

	t.Run("limit and offset", func(t *testing.T) {
		flows, err := store.ListFlows(ctx, FlowFilter{Limit: 2})
		if err != nil {
			t.Fatalf("ListFlows failed: %v", err)
		}
		if len(flows) != 2 {
			t.Errorf("len(flows) = %d, want 2", len(flows))
		}

		flows, err = store.ListFlows(ctx, FlowFilter{Limit: 2, Offset: 2})
		if err != nil {
			t.Fatalf("ListFlows failed: %v", err)
		}
		if len(flows) != 1 {
			t.Errorf("len(flows) = %d, want 1", len(flows))
		}
	})
}

func TestSaveEvent_GetEventsByFlow(t *testing.T) {
	t.Parallel()
	store := setupTestDB(t)
	ctx := context.Background()

	// Create parent flow first
	flow := &Flow{
		ID:            "flow-events-1",
		Host:          "api.anthropic.com",
		Method:        "POST",
		Path:          "/v1/messages",
		URL:           "https://api.anthropic.com/v1/messages",
		Timestamp:     time.Now(),
		TimestampMono: time.Now().UnixNano(),
		FlowIntegrity: "complete",
		Provider:      "anthropic",
		IsSSE:         true,
	}
	if err := store.SaveFlow(ctx, flow); err != nil {
		t.Fatalf("SaveFlow failed: %v", err)
	}

	// Create events
	events := []*Event{
		{
			ID:            "event-1",
			FlowID:        "flow-events-1",
			Sequence:      1,
			Timestamp:     time.Now(),
			TimestampMono: time.Now().UnixNano(),
			EventType:     "message_start",
			EventData:     map[string]interface{}{"type": "message_start"},
			Priority:      "high",
		},
		{
			ID:            "event-2",
			FlowID:        "flow-events-1",
			Sequence:      2,
			Timestamp:     time.Now(),
			TimestampMono: time.Now().UnixNano(),
			EventType:     "content_block_delta",
			EventData:     map[string]interface{}{"delta": "hello"},
			Priority:      "medium",
		},
	}

	// Save individually
	for _, event := range events {
		if err := store.SaveEvent(ctx, event); err != nil {
			t.Fatalf("SaveEvent failed: %v", err)
		}
	}

	// Retrieve
	got, err := store.GetEventsByFlow(ctx, "flow-events-1")
	if err != nil {
		t.Fatalf("GetEventsByFlow failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(got))
	}

	// Verify order (by sequence)
	if got[0].Sequence != 1 || got[1].Sequence != 2 {
		t.Errorf("events not in sequence order")
	}
	if got[0].EventType != "message_start" {
		t.Errorf("first event type = %q, want %q", got[0].EventType, "message_start")
	}
}

func TestSaveEvents_Batch(t *testing.T) {
	t.Parallel()
	store := setupTestDB(t)
	ctx := context.Background()

	// Create parent flow
	flow := &Flow{
		ID:            "flow-batch-1",
		Host:          "api.anthropic.com",
		Method:        "POST",
		Path:          "/v1/messages",
		URL:           "https://api.anthropic.com/v1/messages",
		Timestamp:     time.Now(),
		TimestampMono: time.Now().UnixNano(),
		FlowIntegrity: "complete",
		Provider:      "anthropic",
	}
	if err := store.SaveFlow(ctx, flow); err != nil {
		t.Fatalf("SaveFlow failed: %v", err)
	}

	// Create batch of events
	var events []*Event
	for i := 1; i <= 100; i++ {
		events = append(events, &Event{
			ID:            "batch-event-" + string(rune(i)),
			FlowID:        "flow-batch-1",
			Sequence:      i,
			Timestamp:     time.Now(),
			TimestampMono: time.Now().UnixNano(),
			EventType:     "content_block_delta",
			Priority:      "medium",
		})
	}

	// Save batch
	err := store.SaveEvents(ctx, events)
	if err != nil {
		t.Fatalf("SaveEvents failed: %v", err)
	}

	// Verify count
	got, err := store.GetEventsByFlow(ctx, "flow-batch-1")
	if err != nil {
		t.Fatalf("GetEventsByFlow failed: %v", err)
	}
	if len(got) != 100 {
		t.Errorf("len(events) = %d, want 100", len(got))
	}
}

func TestSaveToolInvocation_GetToolInvocationsByFlow(t *testing.T) {
	t.Parallel()
	store := setupTestDB(t)
	ctx := context.Background()

	// Create parent flow
	flow := &Flow{
		ID:            "flow-tools-1",
		Host:          "api.anthropic.com",
		Method:        "POST",
		Path:          "/v1/messages",
		URL:           "https://api.anthropic.com/v1/messages",
		Timestamp:     time.Now(),
		TimestampMono: time.Now().UnixNano(),
		FlowIntegrity: "complete",
		Provider:      "anthropic",
	}
	if err := store.SaveFlow(ctx, flow); err != nil {
		t.Fatalf("SaveFlow failed: %v", err)
	}

	// Create tool invocation
	taskID := "task-1"
	toolType := "bash"
	duration := int64(100)
	success := true
	inputTokens := 50
	outputTokens := 25
	cost := 0.0005

	inv := &ToolInvocation{
		ID:           "tool-1",
		FlowID:       "flow-tools-1",
		TaskID:       &taskID,
		ToolName:     "execute_command",
		ToolType:     &toolType,
		Timestamp:    time.Now(),
		DurationMs:   &duration,
		Success:      &success,
		InputTokens:  &inputTokens,
		OutputTokens: &outputTokens,
		Cost:         &cost,
	}

	// Save
	err := store.SaveToolInvocation(ctx, inv)
	if err != nil {
		t.Fatalf("SaveToolInvocation failed: %v", err)
	}

	// Retrieve
	got, err := store.GetToolInvocationsByFlow(ctx, "flow-tools-1")
	if err != nil {
		t.Fatalf("GetToolInvocationsByFlow failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(invocations) = %d, want 1", len(got))
	}

	if got[0].ToolName != "execute_command" {
		t.Errorf("ToolName = %q, want %q", got[0].ToolName, "execute_command")
	}
	if *got[0].Success != true {
		t.Errorf("Success = %v, want true", *got[0].Success)
	}
}

func TestLogDrop(t *testing.T) {
	t.Parallel()
	store := setupTestDB(t)
	ctx := context.Background()

	flowID := "flow-drop-1"
	eventType := "content_block_delta"

	entry := &DropLogEntry{
		FlowID:    &flowID,
		EventType: &eventType,
		Priority:  "low",
		Reason:    "queue full",
	}

	err := store.LogDrop(ctx, entry)
	if err != nil {
		t.Fatalf("LogDrop failed: %v", err)
	}

	// Verify by direct query
	var count int
	err = store.db.QueryRow("SELECT COUNT(*) FROM drop_log").Scan(&count)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("drop_log count = %d, want 1", count)
	}
}

func TestRunRetention(t *testing.T) {
	t.Parallel()
	store := setupTestDB(t)
	ctx := context.Background()

	// Create expired flow
	expiredTime := time.Now().Add(-24 * time.Hour)
	expiredFlow := &Flow{
		ID:            "flow-expired",
		Host:          "api.anthropic.com",
		Method:        "POST",
		Path:          "/v1/messages",
		URL:           "https://api.anthropic.com/v1/messages",
		Timestamp:     expiredTime,
		TimestampMono: expiredTime.UnixNano(),
		FlowIntegrity: "complete",
		Provider:      "anthropic",
		ExpiresAt:     &expiredTime,
	}
	if err := store.SaveFlow(ctx, expiredFlow); err != nil {
		t.Fatalf("SaveFlow failed: %v", err)
	}

	// Create non-expired flow
	futureTime := time.Now().Add(7 * 24 * time.Hour)
	validFlow := &Flow{
		ID:            "flow-valid",
		Host:          "api.anthropic.com",
		Method:        "POST",
		Path:          "/v1/messages",
		URL:           "https://api.anthropic.com/v1/messages",
		Timestamp:     time.Now(),
		TimestampMono: time.Now().UnixNano(),
		FlowIntegrity: "complete",
		Provider:      "anthropic",
		ExpiresAt:     &futureTime,
	}
	if err := store.SaveFlow(ctx, validFlow); err != nil {
		t.Fatalf("SaveFlow failed: %v", err)
	}

	// Run retention
	deleted, err := store.RunRetention(ctx)
	if err != nil {
		t.Fatalf("RunRetention failed: %v", err)
	}
	if deleted < 1 {
		t.Errorf("deleted = %d, want >= 1", deleted)
	}

	// Verify expired flow deleted
	_, err = store.GetFlow(ctx, "flow-expired")
	if err != sql.ErrNoRows {
		t.Errorf("expected expired flow to be deleted, got err: %v", err)
	}

	// Verify valid flow still exists
	got, err := store.GetFlow(ctx, "flow-valid")
	if err != nil {
		t.Errorf("expected valid flow to exist, got err: %v", err)
	}
	if got == nil {
		t.Error("valid flow should still exist")
	}
}

func TestCascadeDelete(t *testing.T) {
	t.Parallel()
	store := setupTestDB(t)
	ctx := context.Background()

	// Create flow with events
	flow := &Flow{
		ID:            "flow-cascade-1",
		Host:          "api.anthropic.com",
		Method:        "POST",
		Path:          "/v1/messages",
		URL:           "https://api.anthropic.com/v1/messages",
		Timestamp:     time.Now(),
		TimestampMono: time.Now().UnixNano(),
		FlowIntegrity: "complete",
		Provider:      "anthropic",
	}
	if err := store.SaveFlow(ctx, flow); err != nil {
		t.Fatalf("SaveFlow failed: %v", err)
	}

	event := &Event{
		ID:            "cascade-event-1",
		FlowID:        "flow-cascade-1",
		Sequence:      1,
		Timestamp:     time.Now(),
		TimestampMono: time.Now().UnixNano(),
		EventType:     "message_start",
		Priority:      "high",
	}
	if err := store.SaveEvent(ctx, event); err != nil {
		t.Fatalf("SaveEvent failed: %v", err)
	}

	// Delete flow
	if err := store.DeleteFlow(ctx, "flow-cascade-1"); err != nil {
		t.Fatalf("DeleteFlow failed: %v", err)
	}

	// Verify events cascaded
	events, err := store.GetEventsByFlow(ctx, "flow-cascade-1")
	if err != nil {
		t.Fatalf("GetEventsByFlow failed: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("events should be cascaded deleted, got %d", len(events))
	}
}

func TestDBInterface(t *testing.T) {
	t.Parallel()
	store := setupTestDB(t)

	db := store.DB()
	if db == nil {
		t.Fatal("DB() returned nil")
	}

	// Should be *sql.DB
	if _, ok := db.(*sql.DB); !ok {
		t.Errorf("DB() returned %T, want *sql.DB", db)
	}
}

func TestFTS5Support(t *testing.T) {
	t.Parallel()
	store := setupTestDB(t)

	// PoC: Verify modernc/sqlite includes FTS5
	_, err := store.db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS test_fts USING fts5(content)`)
	if err != nil {
		t.Errorf("FTS5 not supported: %v", err)
		t.Log("FTS5 may not be available in this SQLite build")
		return
	}

	// Test basic FTS5 operations
	_, err = store.db.Exec(`INSERT INTO test_fts (content) VALUES ('hello world')`)
	if err != nil {
		t.Fatalf("FTS5 insert failed: %v", err)
	}

	var content string
	err = store.db.QueryRow(`SELECT content FROM test_fts WHERE test_fts MATCH 'hello'`).Scan(&content)
	if err != nil {
		t.Fatalf("FTS5 query failed: %v", err)
	}
	if content != "hello world" {
		t.Errorf("FTS5 content = %q, want %q", content, "hello world")
	}

	t.Log("FTS5 is available in modernc/sqlite")
}

func TestMigrationIdempotent(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "migration-test.db")

	// Create store first time
	store1, err := NewSQLiteStore(dbPath, testRetention())
	if err != nil {
		t.Fatalf("first NewSQLiteStore failed: %v", err)
	}

	// Get schema version
	var version1 int
	_ = store1.db.QueryRow("SELECT version FROM schema_version WHERE id = 1").Scan(&version1)
	store1.Close()

	// Create store second time (should be idempotent)
	store2, err := NewSQLiteStore(dbPath, testRetention())
	if err != nil {
		t.Fatalf("second NewSQLiteStore failed: %v", err)
	}

	var version2 int
	_ = store2.db.QueryRow("SELECT version FROM schema_version WHERE id = 1").Scan(&version2)
	store2.Close()

	if version1 != version2 {
		t.Errorf("schema versions differ: %d vs %d", version1, version2)
	}
}

func TestNullableFields(t *testing.T) {
	t.Parallel()
	store := setupTestDB(t)
	ctx := context.Background()

	// Create minimal flow with no optional fields
	flow := &Flow{
		ID:            "flow-nullable",
		Host:          "api.anthropic.com",
		Method:        "POST",
		Path:          "/v1/messages",
		URL:           "https://api.anthropic.com/v1/messages",
		Timestamp:     time.Now(),
		TimestampMono: time.Now().UnixNano(),
		FlowIntegrity: "partial",
		Provider:      "anthropic",
		// All pointer fields left nil
	}

	err := store.SaveFlow(ctx, flow)
	if err != nil {
		t.Fatalf("SaveFlow with nullable fields failed: %v", err)
	}

	got, err := store.GetFlow(ctx, "flow-nullable")
	if err != nil {
		t.Fatalf("GetFlow failed: %v", err)
	}

	// Verify nullable fields are nil
	if got.TaskID != nil {
		t.Errorf("TaskID should be nil, got %v", got.TaskID)
	}
	if got.StatusCode != nil {
		t.Errorf("StatusCode should be nil, got %v", got.StatusCode)
	}
	if got.Model != nil {
		t.Errorf("Model should be nil, got %v", got.Model)
	}
}

func TestCountFlows(t *testing.T) {
	t.Parallel()
	store := setupTestDB(t)
	ctx := context.Background()

	// Create multiple flows with different hosts
	hosts := []string{"api.anthropic.com", "api.openai.com", "api.anthropic.com"}
	for i, host := range hosts {
		flow := &Flow{
			ID:            "flow-count-" + string(rune('a'+i)),
			Host:          host,
			Method:        "POST",
			Path:          "/v1/messages",
			URL:           "https://" + host + "/v1/messages",
			Timestamp:     time.Now(),
			TimestampMono: time.Now().UnixNano(),
			FlowIntegrity: "complete",
			Provider:      "anthropic",
		}
		if err := store.SaveFlow(ctx, flow); err != nil {
			t.Fatalf("SaveFlow %d failed: %v", i, err)
		}
	}

	t.Run("count all", func(t *testing.T) {
		count, err := store.CountFlows(ctx, FlowFilter{})
		if err != nil {
			t.Fatalf("CountFlows failed: %v", err)
		}
		if count != 3 {
			t.Errorf("count = %d, want 3", count)
		}
	})

	t.Run("count with filter", func(t *testing.T) {
		host := "api.anthropic.com"
		count, err := store.CountFlows(ctx, FlowFilter{Host: &host})
		if err != nil {
			t.Fatalf("CountFlows failed: %v", err)
		}
		if count != 2 {
			t.Errorf("count = %d, want 2", count)
		}
	})

	t.Run("count ignores limit", func(t *testing.T) {
		count, err := store.CountFlows(ctx, FlowFilter{Limit: 1})
		if err != nil {
			t.Fatalf("CountFlows failed: %v", err)
		}
		if count != 3 {
			t.Errorf("count = %d, want 3 (should ignore limit)", count)
		}
	})
}

func TestProviderConstraint(t *testing.T) {
	t.Parallel()
	store := setupTestDB(t)
	ctx := context.Background()

	// All valid provider values should be insertable
	providers := []string{"anthropic", "openai", "bedrock", "gemini", "other"}

	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			flow := &Flow{
				ID:            "flow-provider-" + provider,
				Host:          "api.example.com",
				Method:        "POST",
				Path:          "/v1/messages",
				URL:           "https://api.example.com/v1/messages",
				Timestamp:     time.Now(),
				TimestampMono: time.Now().UnixNano(),
				FlowIntegrity: "complete",
				Provider:      provider,
			}

			err := store.SaveFlow(ctx, flow)
			if err != nil {
				t.Fatalf("SaveFlow with provider=%q failed: %v", provider, err)
			}

			// Verify it was saved correctly
			got, err := store.GetFlow(ctx, flow.ID)
			if err != nil {
				t.Fatalf("GetFlow failed: %v", err)
			}
			if got.Provider != provider {
				t.Errorf("Provider = %q, want %q", got.Provider, provider)
			}
		})
	}
}

func TestConcurrentAccess(t *testing.T) {
	t.Parallel()
	store := setupTestDB(t)
	ctx := context.Background()

	// Stress test concurrent writes
	const numGoroutines = 10
	const flowsPerGoroutine = 20

	done := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(workerID int) {
			for j := 0; j < flowsPerGoroutine; j++ {
				flow := &Flow{
					ID:            "concurrent-" + string(rune('a'+workerID)) + "-" + string(rune('0'+j)),
					Host:          "api.anthropic.com",
					Method:        "POST",
					Path:          "/v1/messages",
					URL:           "https://api.anthropic.com/v1/messages",
					Timestamp:     time.Now(),
					TimestampMono: time.Now().UnixNano(),
					FlowIntegrity: "complete",
					Provider:      "anthropic",
				}
				if err := store.SaveFlow(ctx, flow); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent write failed: %v", err)
		}
	}

	// Verify all flows saved
	flows, err := store.ListFlows(ctx, FlowFilter{})
	if err != nil {
		t.Fatalf("ListFlows failed: %v", err)
	}
	expected := numGoroutines * flowsPerGoroutine
	if len(flows) != expected {
		t.Errorf("len(flows) = %d, want %d", len(flows), expected)
	}
}
