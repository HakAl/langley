package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/HakAl/langley/internal/config"
	_ "modernc.org/sqlite"
)

// SQLiteStore implements Store using SQLite.
type SQLiteStore struct {
	db        *sql.DB
	retention *config.RetentionConfig
}

// NewSQLiteStore creates a new SQLite store.
func NewSQLiteStore(dbPath string, retention *config.RetentionConfig) (*SQLiteStore, error) {
	// Open database with WAL mode and recommended pragmas
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Force a connection to ensure the file is created
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connecting to database: %w", err)
	}

	// Enable foreign keys for CASCADE behavior
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enabling foreign keys: %w", err)
	}

	// SECURITY: Set restrictive file permissions (2.2.11)
	// Database may contain sensitive request/response data
	if err := setSecureFilePermissions(dbPath); err != nil {
		// Log warning but don't fail - Windows may not support this
		// The file will still be created, just with default permissions
		_ = err // Intentionally ignoring - best effort
	}

	// Set connection pool (SQLite with WAL can handle some concurrency)
	db.SetMaxOpenConns(1) // SQLite is single-writer
	db.SetMaxIdleConns(1)

	store := &SQLiteStore{
		db:        db,
		retention: retention,
	}

	// Run migrations
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return store, nil
}

// setSecureFilePermissions sets restrictive permissions on the database file.
// On Unix: 0600 (owner read/write only)
// On Windows: This is best-effort as file permissions work differently
func setSecureFilePermissions(path string) error {
	if runtime.GOOS == "windows" {
		// Windows doesn't use Unix permissions
		// File security is handled via ACLs, which is more complex
		// For a single-user local tool, this is acceptable
		return nil
	}

	// Unix: Set 0600 on database and WAL files
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", path, err)
	}

	// Also try to set permissions on WAL and SHM files if they exist
	walPath := path + "-wal"
	shmPath := path + "-shm"
	os.Chmod(walPath, 0600) // Ignore errors - files may not exist yet
	os.Chmod(shmPath, 0600)

	return nil
}

// migrate runs database migrations.
func (s *SQLiteStore) migrate() error {
	// Check current version
	var version int
	err := s.db.QueryRow("SELECT version FROM schema_version WHERE id = 1").Scan(&version)
	if err != nil {
		// Table doesn't exist, create it
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS schema_version (
				id INTEGER PRIMARY KEY CHECK (id = 1),
				version INTEGER NOT NULL,
				applied_at TEXT NOT NULL DEFAULT (datetime('now')),
				lock_holder TEXT
			);
			INSERT OR IGNORE INTO schema_version (id, version) VALUES (1, 0);
		`); err != nil {
			return fmt.Errorf("creating schema_version: %w", err)
		}
		version = 0
	}

	// Run migrations
	migrations := []string{
		migrationV1, // Initial schema
	}

	for i := version; i < len(migrations); i++ {
		if _, err := s.db.Exec(migrations[i]); err != nil {
			return fmt.Errorf("running migration %d: %w", i+1, err)
		}
		if _, err := s.db.Exec("UPDATE schema_version SET version = ?, applied_at = datetime('now') WHERE id = 1", i+1); err != nil {
			return fmt.Errorf("updating version to %d: %w", i+1, err)
		}
	}

	return nil
}

const migrationV1 = `
-- Flows table
CREATE TABLE IF NOT EXISTS flows (
	id TEXT PRIMARY KEY,
	task_id TEXT,
	task_source TEXT CHECK (task_source IS NULL OR task_source IN ('explicit', 'metadata', 'inferred')),
	host TEXT NOT NULL,
	method TEXT NOT NULL,
	path TEXT NOT NULL,
	url TEXT NOT NULL,
	timestamp TEXT NOT NULL,
	timestamp_mono INTEGER,
	duration_ms INTEGER,
	status_code INTEGER,
	status_text TEXT,
	is_sse INTEGER DEFAULT 0,
	flow_integrity TEXT DEFAULT 'complete' CHECK (flow_integrity IN ('complete', 'partial', 'corrupted', 'interrupted')),
	events_dropped_count INTEGER DEFAULT 0,
	request_body TEXT,
	request_body_truncated INTEGER DEFAULT 0,
	response_body TEXT,
	response_body_truncated INTEGER DEFAULT 0,
	request_headers TEXT,
	response_headers TEXT,
	request_signature TEXT,
	request_signature_version INTEGER DEFAULT 1,
	input_tokens INTEGER,
	output_tokens INTEGER,
	cache_creation_tokens INTEGER,
	cache_read_tokens INTEGER,
	total_cost REAL,
	cost_source TEXT CHECK (cost_source IS NULL OR cost_source IN ('exact', 'estimated')),
	model TEXT,
	provider TEXT DEFAULT 'anthropic' CHECK (provider IN ('anthropic', 'openai', 'bedrock', 'gemini', 'other')),
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	expires_at TEXT
);

-- Events table
CREATE TABLE IF NOT EXISTS events (
	id TEXT PRIMARY KEY,
	flow_id TEXT NOT NULL REFERENCES flows(id) ON DELETE CASCADE,
	sequence INTEGER NOT NULL,
	timestamp TEXT NOT NULL,
	timestamp_mono INTEGER,
	event_type TEXT NOT NULL,
	event_data TEXT,
	priority TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('high', 'medium', 'low')),
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	expires_at TEXT,
	UNIQUE (flow_id, sequence)
);

-- Tool invocations table
CREATE TABLE IF NOT EXISTS tool_invocations (
	id TEXT PRIMARY KEY,
	flow_id TEXT NOT NULL REFERENCES flows(id) ON DELETE CASCADE,
	task_id TEXT,
	tool_name TEXT NOT NULL,
	tool_type TEXT,
	timestamp TEXT NOT NULL,
	duration_ms INTEGER,
	success INTEGER,
	error_message TEXT,
	input_tokens INTEGER,
	output_tokens INTEGER,
	cost REAL,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	expires_at TEXT
);

-- Pricing table
CREATE TABLE IF NOT EXISTS pricing (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	provider TEXT NOT NULL,
	model_pattern TEXT NOT NULL,
	input_cost_per_1k REAL NOT NULL,
	output_cost_per_1k REAL NOT NULL,
	cache_creation_per_1k REAL,
	cache_read_per_1k REAL,
	effective_date TEXT NOT NULL,
	notes TEXT,
	UNIQUE (provider, model_pattern, effective_date)
);

-- Default pricing
INSERT OR IGNORE INTO pricing (provider, model_pattern, input_cost_per_1k, output_cost_per_1k, cache_creation_per_1k, cache_read_per_1k, effective_date) VALUES
	('anthropic', 'claude-3-5-sonnet%', 0.003, 0.015, 0.00375, 0.0003, '2025-01-01'),
	('anthropic', 'claude-3-5-haiku%', 0.0008, 0.004, 0.001, 0.00008, '2025-01-01'),
	('anthropic', 'claude-3-opus%', 0.015, 0.075, 0.01875, 0.0015, '2025-01-01'),
	('anthropic', 'claude-sonnet-4%', 0.003, 0.015, 0.00375, 0.0003, '2025-01-01'),
	('anthropic', 'claude-opus-4%', 0.015, 0.075, 0.01875, 0.0015, '2025-01-01');

-- Drop log table
CREATE TABLE IF NOT EXISTS drop_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	flow_id TEXT,
	event_type TEXT,
	priority TEXT CHECK (priority IN ('high', 'medium', 'low')),
	reason TEXT,
	timestamp TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Flow indexes
CREATE INDEX IF NOT EXISTS idx_flows_timestamp ON flows(timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_flows_task_timestamp ON flows(task_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_flows_host_timestamp ON flows(host, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_flows_expires ON flows(expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_flows_model ON flows(model, timestamp);

-- Event indexes
CREATE INDEX IF NOT EXISTS idx_events_type_time ON events(event_type, timestamp);
CREATE INDEX IF NOT EXISTS idx_events_expires ON events(expires_at) WHERE expires_at IS NOT NULL;

-- Tool invocation indexes
CREATE INDEX IF NOT EXISTS idx_tool_invocations_analytics ON tool_invocations(tool_name, timestamp, cost, duration_ms);
CREATE INDEX IF NOT EXISTS idx_tool_invocations_task ON tool_invocations(task_id, timestamp);

-- Drop log indexes
CREATE INDEX IF NOT EXISTS idx_drop_log_priority_time ON drop_log(priority, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_drop_log_flow ON drop_log(flow_id) WHERE flow_id IS NOT NULL;
`

// SaveFlow inserts a new flow.
func (s *SQLiteStore) SaveFlow(ctx context.Context, flow *Flow) error {
	reqHeaders, _ := json.Marshal(flow.RequestHeaders)
	respHeaders, _ := json.Marshal(flow.ResponseHeaders)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO flows (
			id, task_id, task_source, host, method, path, url,
			timestamp, timestamp_mono, duration_ms, status_code, status_text,
			is_sse, flow_integrity, events_dropped_count,
			request_body, request_body_truncated, response_body, response_body_truncated,
			request_headers, response_headers, request_signature,
			input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			total_cost, cost_source, model, provider, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		flow.ID, flow.TaskID, flow.TaskSource, flow.Host, flow.Method, flow.Path, flow.URL,
		flow.Timestamp.Format(time.RFC3339Nano), flow.TimestampMono, flow.DurationMs, flow.StatusCode, flow.StatusText,
		flow.IsSSE, flow.FlowIntegrity, flow.EventsDroppedCount,
		flow.RequestBody, flow.RequestBodyTruncated, flow.ResponseBody, flow.ResponseBodyTruncated,
		string(reqHeaders), string(respHeaders), flow.RequestSignature,
		flow.InputTokens, flow.OutputTokens, flow.CacheCreationTokens, flow.CacheReadTokens,
		flow.TotalCost, flow.CostSource, flow.Model, flow.Provider, formatNullableTime(flow.ExpiresAt),
	)
	return err
}

// UpdateFlow updates an existing flow.
func (s *SQLiteStore) UpdateFlow(ctx context.Context, flow *Flow) error {
	reqHeaders, _ := json.Marshal(flow.RequestHeaders)
	respHeaders, _ := json.Marshal(flow.ResponseHeaders)

	_, err := s.db.ExecContext(ctx, `
		UPDATE flows SET
			task_id = ?, task_source = ?, duration_ms = ?, status_code = ?, status_text = ?,
			is_sse = ?, flow_integrity = ?, events_dropped_count = ?,
			response_body = ?, response_body_truncated = ?,
			request_headers = ?, response_headers = ?,
			input_tokens = ?, output_tokens = ?, cache_creation_tokens = ?, cache_read_tokens = ?,
			total_cost = ?, cost_source = ?, model = ?
		WHERE id = ?
	`,
		flow.TaskID, flow.TaskSource, flow.DurationMs, flow.StatusCode, flow.StatusText,
		flow.IsSSE, flow.FlowIntegrity, flow.EventsDroppedCount,
		flow.ResponseBody, flow.ResponseBodyTruncated,
		string(reqHeaders), string(respHeaders),
		flow.InputTokens, flow.OutputTokens, flow.CacheCreationTokens, flow.CacheReadTokens,
		flow.TotalCost, flow.CostSource, flow.Model,
		flow.ID,
	)
	return err
}

// GetFlow retrieves a flow by ID.
func (s *SQLiteStore) GetFlow(ctx context.Context, id string) (*Flow, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, task_id, task_source, host, method, path, url,
			timestamp, timestamp_mono, duration_ms, status_code, status_text,
			is_sse, flow_integrity, events_dropped_count,
			request_body, request_body_truncated, response_body, response_body_truncated,
			request_headers, response_headers, request_signature,
			input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			total_cost, cost_source, model, provider, created_at, expires_at
		FROM flows WHERE id = ?
	`, id)

	return s.scanFlow(row)
}

// ListFlows returns flows matching the filter.
func (s *SQLiteStore) ListFlows(ctx context.Context, filter FlowFilter) ([]*Flow, error) {
	query := strings.Builder{}
	query.WriteString(`
		SELECT id, task_id, task_source, host, method, path, url,
			timestamp, timestamp_mono, duration_ms, status_code, status_text,
			is_sse, flow_integrity, events_dropped_count,
			request_body, request_body_truncated, response_body, response_body_truncated,
			request_headers, response_headers, request_signature,
			input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			total_cost, cost_source, model, provider, created_at, expires_at
		FROM flows WHERE 1=1
	`)

	args := []interface{}{}

	if filter.Host != nil {
		query.WriteString(" AND host = ?")
		args = append(args, *filter.Host)
	}
	if filter.TaskID != nil {
		query.WriteString(" AND task_id = ?")
		args = append(args, *filter.TaskID)
	}
	if filter.TaskSource != nil {
		query.WriteString(" AND task_source = ?")
		args = append(args, *filter.TaskSource)
	}
	if filter.Model != nil {
		query.WriteString(" AND model = ?")
		args = append(args, *filter.Model)
	}
	if filter.StartTime != nil {
		query.WriteString(" AND timestamp >= ?")
		args = append(args, filter.StartTime.Format(time.RFC3339Nano))
	}
	if filter.EndTime != nil {
		query.WriteString(" AND timestamp <= ?")
		args = append(args, filter.EndTime.Format(time.RFC3339Nano))
	}

	query.WriteString(" ORDER BY timestamp DESC")

	if filter.Limit > 0 {
		query.WriteString(" LIMIT ?")
		args = append(args, filter.Limit)
	}
	if filter.Offset > 0 {
		query.WriteString(" OFFSET ?")
		args = append(args, filter.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var flows []*Flow
	for rows.Next() {
		flow, err := s.scanFlowRows(rows)
		if err != nil {
			return nil, err
		}
		flows = append(flows, flow)
	}

	return flows, rows.Err()
}

// CountFlows returns the count of flows matching the filter (ignores Limit/Offset).
func (s *SQLiteStore) CountFlows(ctx context.Context, filter FlowFilter) (int, error) {
	query := strings.Builder{}
	query.WriteString("SELECT COUNT(*) FROM flows WHERE 1=1")

	args := []interface{}{}

	if filter.Host != nil {
		query.WriteString(" AND host = ?")
		args = append(args, *filter.Host)
	}
	if filter.TaskID != nil {
		query.WriteString(" AND task_id = ?")
		args = append(args, *filter.TaskID)
	}
	if filter.TaskSource != nil {
		query.WriteString(" AND task_source = ?")
		args = append(args, *filter.TaskSource)
	}
	if filter.Model != nil {
		query.WriteString(" AND model = ?")
		args = append(args, *filter.Model)
	}
	if filter.StartTime != nil {
		query.WriteString(" AND timestamp >= ?")
		args = append(args, filter.StartTime.Format(time.RFC3339Nano))
	}
	if filter.EndTime != nil {
		query.WriteString(" AND timestamp <= ?")
		args = append(args, filter.EndTime.Format(time.RFC3339Nano))
	}

	var count int
	err := s.db.QueryRowContext(ctx, query.String(), args...).Scan(&count)
	return count, err
}

// DeleteFlow deletes a flow and its associated data.
func (s *SQLiteStore) DeleteFlow(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM flows WHERE id = ?", id)
	return err
}

// SaveEvent inserts a new event.
func (s *SQLiteStore) SaveEvent(ctx context.Context, event *Event) error {
	eventData, _ := json.Marshal(event.EventData)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO events (id, flow_id, sequence, timestamp, timestamp_mono, event_type, event_data, priority, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		event.ID, event.FlowID, event.Sequence, event.Timestamp.Format(time.RFC3339Nano),
		event.TimestampMono, event.EventType, string(eventData), event.Priority,
		formatNullableTime(event.ExpiresAt),
	)
	return err
}

// SaveEvents inserts multiple events in a batch.
func (s *SQLiteStore) SaveEvents(ctx context.Context, events []*Event) error {
	if len(events) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO events (id, flow_id, sequence, timestamp, timestamp_mono, event_type, event_data, priority, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, event := range events {
		eventData, _ := json.Marshal(event.EventData)
		_, err := stmt.ExecContext(ctx,
			event.ID, event.FlowID, event.Sequence, event.Timestamp.Format(time.RFC3339Nano),
			event.TimestampMono, event.EventType, string(eventData), event.Priority,
			formatNullableTime(event.ExpiresAt),
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetEventsByFlow returns events for a flow.
func (s *SQLiteStore) GetEventsByFlow(ctx context.Context, flowID string) ([]*Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, flow_id, sequence, timestamp, timestamp_mono, event_type, event_data, priority, created_at, expires_at
		FROM events WHERE flow_id = ? ORDER BY sequence
	`, flowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*Event
	for rows.Next() {
		var event Event
		var ts, createdAt string
		var expiresAt sql.NullString
		var eventData sql.NullString

		err := rows.Scan(
			&event.ID, &event.FlowID, &event.Sequence, &ts, &event.TimestampMono,
			&event.EventType, &eventData, &event.Priority, &createdAt, &expiresAt,
		)
		if err != nil {
			return nil, err
		}

		event.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		event.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		if expiresAt.Valid {
			t, _ := time.Parse(time.RFC3339Nano, expiresAt.String)
			event.ExpiresAt = &t
		}
		if eventData.Valid {
			json.Unmarshal([]byte(eventData.String), &event.EventData)
		}

		events = append(events, &event)
	}

	return events, rows.Err()
}

// SaveToolInvocation inserts a tool invocation.
func (s *SQLiteStore) SaveToolInvocation(ctx context.Context, inv *ToolInvocation) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tool_invocations (id, flow_id, task_id, tool_name, tool_type, timestamp, duration_ms, success, error_message, input_tokens, output_tokens, cost, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		inv.ID, inv.FlowID, inv.TaskID, inv.ToolName, inv.ToolType,
		inv.Timestamp.Format(time.RFC3339Nano), inv.DurationMs, inv.Success, inv.ErrorMessage,
		inv.InputTokens, inv.OutputTokens, inv.Cost, formatNullableTime(inv.ExpiresAt),
	)
	return err
}

// GetToolInvocationsByFlow returns tool invocations for a flow.
func (s *SQLiteStore) GetToolInvocationsByFlow(ctx context.Context, flowID string) ([]*ToolInvocation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, flow_id, task_id, tool_name, tool_type, timestamp, duration_ms, success, error_message, input_tokens, output_tokens, cost, created_at, expires_at
		FROM tool_invocations WHERE flow_id = ? ORDER BY timestamp
	`, flowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invocations []*ToolInvocation
	for rows.Next() {
		var inv ToolInvocation
		var ts, createdAt string
		var expiresAt, taskID, toolType, errorMsg sql.NullString
		var durationMs, inputTokens, outputTokens sql.NullInt64
		var success sql.NullBool
		var cost sql.NullFloat64

		err := rows.Scan(
			&inv.ID, &inv.FlowID, &taskID, &inv.ToolName, &toolType,
			&ts, &durationMs, &success, &errorMsg,
			&inputTokens, &outputTokens, &cost, &createdAt, &expiresAt,
		)
		if err != nil {
			return nil, err
		}

		inv.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		inv.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		if taskID.Valid {
			inv.TaskID = &taskID.String
		}
		if toolType.Valid {
			inv.ToolType = &toolType.String
		}
		if errorMsg.Valid {
			inv.ErrorMessage = &errorMsg.String
		}
		if durationMs.Valid {
			inv.DurationMs = &durationMs.Int64
		}
		if success.Valid {
			inv.Success = &success.Bool
		}
		if inputTokens.Valid {
			i := int(inputTokens.Int64)
			inv.InputTokens = &i
		}
		if outputTokens.Valid {
			o := int(outputTokens.Int64)
			inv.OutputTokens = &o
		}
		if cost.Valid {
			inv.Cost = &cost.Float64
		}
		if expiresAt.Valid {
			t, _ := time.Parse(time.RFC3339Nano, expiresAt.String)
			inv.ExpiresAt = &t
		}

		invocations = append(invocations, &inv)
	}

	return invocations, rows.Err()
}

// LogDrop records a dropped event.
func (s *SQLiteStore) LogDrop(ctx context.Context, entry *DropLogEntry) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO drop_log (flow_id, event_type, priority, reason) VALUES (?, ?, ?, ?)
	`, entry.FlowID, entry.EventType, entry.Priority, entry.Reason)
	return err
}

// RunRetention deletes expired data.
func (s *SQLiteStore) RunRetention(ctx context.Context) (int64, error) {
	var totalDeleted int64

	// Delete expired flows (cascades to events and tool_invocations)
	res, err := s.db.ExecContext(ctx, "DELETE FROM flows WHERE expires_at < datetime('now')")
	if err != nil {
		return totalDeleted, err
	}
	n, _ := res.RowsAffected()
	totalDeleted += n

	// Delete old drop_log
	res, err = s.db.ExecContext(ctx,
		"DELETE FROM drop_log WHERE timestamp < datetime('now', ?)",
		fmt.Sprintf("-%d days", s.retention.DropLogTTLDays),
	)
	if err != nil {
		return totalDeleted, err
	}
	n, _ = res.RowsAffected()
	totalDeleted += n

	return totalDeleted, nil
}

// Close closes the database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// DB returns the underlying database connection for analytics queries.
func (s *SQLiteStore) DB() interface{} {
	return s.db
}

// scanFlow scans a flow from a single row.
func (s *SQLiteStore) scanFlow(row *sql.Row) (*Flow, error) {
	var flow Flow
	var ts, createdAt string
	var expiresAt, taskID, taskSource, statusText, reqBody, respBody sql.NullString
	var reqHeaders, respHeaders, reqSig, costSource, model sql.NullString
	var timestampMono, durationMs sql.NullInt64
	var statusCode, inputTokens, outputTokens, cacheCreation, cacheRead sql.NullInt64
	var totalCost sql.NullFloat64

	err := row.Scan(
		&flow.ID, &taskID, &taskSource, &flow.Host, &flow.Method, &flow.Path, &flow.URL,
		&ts, &timestampMono, &durationMs, &statusCode, &statusText,
		&flow.IsSSE, &flow.FlowIntegrity, &flow.EventsDroppedCount,
		&reqBody, &flow.RequestBodyTruncated, &respBody, &flow.ResponseBodyTruncated,
		&reqHeaders, &respHeaders, &reqSig,
		&inputTokens, &outputTokens, &cacheCreation, &cacheRead,
		&totalCost, &costSource, &model, &flow.Provider, &createdAt, &expiresAt,
	)
	if err != nil {
		return nil, err
	}

	flow.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	flow.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)

	if taskID.Valid {
		flow.TaskID = &taskID.String
	}
	if taskSource.Valid {
		flow.TaskSource = &taskSource.String
	}
	if statusCode.Valid {
		sc := int(statusCode.Int64)
		flow.StatusCode = &sc
	}
	if statusText.Valid {
		flow.StatusText = &statusText.String
	}
	if reqBody.Valid {
		flow.RequestBody = &reqBody.String
	}
	if respBody.Valid {
		flow.ResponseBody = &respBody.String
	}
	if reqHeaders.Valid {
		json.Unmarshal([]byte(reqHeaders.String), &flow.RequestHeaders)
	}
	if respHeaders.Valid {
		json.Unmarshal([]byte(respHeaders.String), &flow.ResponseHeaders)
	}
	if reqSig.Valid {
		flow.RequestSignature = &reqSig.String
	}
	if timestampMono.Valid {
		flow.TimestampMono = timestampMono.Int64
	}
	if durationMs.Valid {
		flow.DurationMs = &durationMs.Int64
	}
	if inputTokens.Valid {
		i := int(inputTokens.Int64)
		flow.InputTokens = &i
	}
	if outputTokens.Valid {
		o := int(outputTokens.Int64)
		flow.OutputTokens = &o
	}
	if cacheCreation.Valid {
		c := int(cacheCreation.Int64)
		flow.CacheCreationTokens = &c
	}
	if cacheRead.Valid {
		c := int(cacheRead.Int64)
		flow.CacheReadTokens = &c
	}
	if totalCost.Valid {
		flow.TotalCost = &totalCost.Float64
	}
	if costSource.Valid {
		flow.CostSource = &costSource.String
	}
	if model.Valid {
		flow.Model = &model.String
	}
	if expiresAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, expiresAt.String)
		flow.ExpiresAt = &t
	}

	return &flow, nil
}

// scanFlowRows scans a flow from rows.
func (s *SQLiteStore) scanFlowRows(rows *sql.Rows) (*Flow, error) {
	var flow Flow
	var ts, createdAt string
	var expiresAt, taskID, taskSource, statusText, reqBody, respBody sql.NullString
	var reqHeaders, respHeaders, reqSig, costSource, model sql.NullString
	var timestampMono, durationMs sql.NullInt64
	var statusCode, inputTokens, outputTokens, cacheCreation, cacheRead sql.NullInt64
	var totalCost sql.NullFloat64

	err := rows.Scan(
		&flow.ID, &taskID, &taskSource, &flow.Host, &flow.Method, &flow.Path, &flow.URL,
		&ts, &timestampMono, &durationMs, &statusCode, &statusText,
		&flow.IsSSE, &flow.FlowIntegrity, &flow.EventsDroppedCount,
		&reqBody, &flow.RequestBodyTruncated, &respBody, &flow.ResponseBodyTruncated,
		&reqHeaders, &respHeaders, &reqSig,
		&inputTokens, &outputTokens, &cacheCreation, &cacheRead,
		&totalCost, &costSource, &model, &flow.Provider, &createdAt, &expiresAt,
	)
	if err != nil {
		return nil, err
	}

	flow.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	flow.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)

	if taskID.Valid {
		flow.TaskID = &taskID.String
	}
	if taskSource.Valid {
		flow.TaskSource = &taskSource.String
	}
	if statusCode.Valid {
		sc := int(statusCode.Int64)
		flow.StatusCode = &sc
	}
	if statusText.Valid {
		flow.StatusText = &statusText.String
	}
	if reqBody.Valid {
		flow.RequestBody = &reqBody.String
	}
	if respBody.Valid {
		flow.ResponseBody = &respBody.String
	}
	if reqHeaders.Valid {
		json.Unmarshal([]byte(reqHeaders.String), &flow.RequestHeaders)
	}
	if respHeaders.Valid {
		json.Unmarshal([]byte(respHeaders.String), &flow.ResponseHeaders)
	}
	if reqSig.Valid {
		flow.RequestSignature = &reqSig.String
	}
	if timestampMono.Valid {
		flow.TimestampMono = timestampMono.Int64
	}
	if durationMs.Valid {
		flow.DurationMs = &durationMs.Int64
	}
	if inputTokens.Valid {
		i := int(inputTokens.Int64)
		flow.InputTokens = &i
	}
	if outputTokens.Valid {
		o := int(outputTokens.Int64)
		flow.OutputTokens = &o
	}
	if cacheCreation.Valid {
		c := int(cacheCreation.Int64)
		flow.CacheCreationTokens = &c
	}
	if cacheRead.Valid {
		c := int(cacheRead.Int64)
		flow.CacheReadTokens = &c
	}
	if totalCost.Valid {
		flow.TotalCost = &totalCost.Float64
	}
	if costSource.Valid {
		flow.CostSource = &costSource.String
	}
	if model.Valid {
		flow.Model = &model.String
	}
	if expiresAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, expiresAt.String)
		flow.ExpiresAt = &t
	}

	return &flow, nil
}

func formatNullableTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339Nano)
}
