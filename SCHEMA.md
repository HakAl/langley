# Langley Database Schema

SQLite with WAL mode, synchronous=NORMAL.

## Migration Tracking

```sql
CREATE TABLE schema_version (
  id INTEGER PRIMARY KEY CHECK (id = 1),  -- Single row
  version INTEGER NOT NULL,
  applied_at TEXT NOT NULL DEFAULT (datetime('now')),
  lock_holder TEXT  -- Process ID during migration
);

INSERT INTO schema_version (id, version) VALUES (1, 0);
```

## Core Tables

### flows

Primary record for each HTTP request/response pair.

```sql
CREATE TABLE flows (
  id TEXT PRIMARY KEY,

  -- Task assignment
  task_id TEXT,
  task_source TEXT CHECK (task_source IS NULL OR task_source IN ('explicit', 'metadata', 'inferred')),
  CHECK ((task_id IS NULL AND task_source IS NULL) OR (task_id IS NOT NULL AND task_source IS NOT NULL)),

  -- Request metadata
  host TEXT NOT NULL,
  method TEXT NOT NULL,
  path TEXT NOT NULL,
  url TEXT NOT NULL,

  -- Timing
  timestamp TEXT NOT NULL,              -- ISO 8601, wall time
  timestamp_mono INTEGER,               -- Monotonic nanoseconds for ordering
  duration_ms INTEGER,

  -- Response metadata
  status_code INTEGER,
  status_text TEXT,
  is_sse INTEGER DEFAULT 0,             -- Boolean

  -- Data integrity
  flow_integrity TEXT DEFAULT 'complete'
    CHECK (flow_integrity IN ('complete', 'partial', 'corrupted', 'interrupted')),
  events_dropped_count INTEGER DEFAULT 0,

  -- Bodies (redacted, may be truncated)
  request_body TEXT,
  request_body_truncated INTEGER DEFAULT 0,
  response_body TEXT,
  response_body_truncated INTEGER DEFAULT 0,

  -- Headers (redacted JSON, validated)
  request_headers TEXT CHECK (request_headers IS NULL OR json_valid(request_headers)),
  response_headers TEXT CHECK (response_headers IS NULL OR json_valid(response_headers)),

  -- Retry correlation (opt-in, structural signature)
  -- Computed at flow creation from REQUEST body: hash(method + path + sorted(request.tools[].name))
  -- Tool names come from request.tools array, NOT from parsed events
  -- NULL if opt-out or no tools in request
  request_signature TEXT,
  request_signature_version INTEGER DEFAULT 1,  -- Bump when hash algorithm/format changes

  -- Analytics (extracted from response)
  input_tokens INTEGER,
  output_tokens INTEGER,
  cache_creation_tokens INTEGER,
  cache_read_tokens INTEGER,
  total_cost REAL,        -- USD, nullable if unknown model
  cost_source TEXT CHECK (cost_source IS NULL OR cost_source IN ('exact', 'estimated')),
  model TEXT,
  provider TEXT DEFAULT 'anthropic' CHECK (provider IN ('anthropic', 'bedrock', 'other')),

  -- Retention
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  expires_at TEXT  -- For TTL-based cleanup
);
```

### events

SSE events associated with a flow.

```sql
CREATE TABLE events (
  id TEXT PRIMARY KEY,
  flow_id TEXT NOT NULL REFERENCES flows(id) ON DELETE CASCADE,

  -- Ordering
  sequence INTEGER NOT NULL,            -- Order within flow
  timestamp TEXT NOT NULL,
  timestamp_mono INTEGER,

  -- Event data
  event_type TEXT NOT NULL,             -- message_start, content_block_delta, etc.
  event_data TEXT CHECK (event_data IS NULL OR json_valid(event_data)),  -- JSON payload (redacted)

  -- Priority (for understanding what was dropped)
  priority TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('high', 'medium', 'low')),

  -- Retention
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  expires_at TEXT,

  UNIQUE (flow_id, sequence)
);
```

### tool_invocations

Extracted tool usage for analytics.

```sql
CREATE TABLE tool_invocations (
  id TEXT PRIMARY KEY,
  flow_id TEXT NOT NULL REFERENCES flows(id) ON DELETE CASCADE,
  task_id TEXT,  -- Denormalized for query performance

  -- Tool metadata
  tool_name TEXT NOT NULL,
  tool_type TEXT,  -- 'custom', 'bash_20250124', 'text_editor_20250124', etc.

  -- Timing
  timestamp TEXT NOT NULL,
  duration_ms INTEGER,

  -- Outcome
  success INTEGER,  -- Boolean: 1=success, 0=error, NULL=unknown
  error_message TEXT,

  -- Cost attribution (if calculable)
  input_tokens INTEGER,
  output_tokens INTEGER,
  cost REAL,

  -- Retention
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  expires_at TEXT
);
```

### tasks_view

Aggregated task-level statistics as a VIEW. Using a view instead of a table ensures
consistency with retention policy - when flows are deleted, aggregates automatically
reflect the current state. No sync issues.

Uses CTEs to avoid correlated subqueries and N+1 scan patterns.

```sql
CREATE VIEW tasks_view AS
WITH flow_stats AS (
  SELECT
    task_id,
    task_source,
    COUNT(*) AS flow_count,
    MIN(timestamp) AS first_seen,
    MAX(timestamp) AS last_seen,
    SUM(duration_ms) AS total_duration_ms,
    SUM(input_tokens) AS total_input_tokens,
    SUM(output_tokens) AS total_output_tokens,
    SUM(cache_creation_tokens) AS total_cache_creation_tokens,
    SUM(cache_read_tokens) AS total_cache_read_tokens,
    SUM(total_cost) AS total_cost,
    SUM(CASE WHEN flow_integrity != 'complete' THEN 1 ELSE 0 END) AS integrity_issues
  FROM flows
  WHERE task_id IS NOT NULL
  GROUP BY task_id, task_source
),
event_stats AS (
  SELECT
    f.task_id,
    f.task_source,
    COUNT(e.id) AS event_count,
    COUNT(DISTINCT e.flow_id) AS flows_with_events
  FROM flows f
  LEFT JOIN events e ON e.flow_id = f.id
  WHERE f.task_id IS NOT NULL
  GROUP BY f.task_id, f.task_source
),
tool_stats AS (
  SELECT
    f.task_id,
    f.task_source,
    COUNT(t.id) AS tool_count
  FROM flows f
  LEFT JOIN tool_invocations t ON t.task_id = f.task_id AND t.flow_id = f.id
  WHERE f.task_id IS NOT NULL
  GROUP BY f.task_id, f.task_source
)
SELECT
  fs.task_id AS id,
  fs.task_source AS source,
  fs.flow_count,
  COALESCE(es.flows_with_events, 0) AS flows_with_events,
  COALESCE(es.event_count, 0) AS event_count,
  COALESCE(ts.tool_count, 0) AS tool_count,
  fs.first_seen,
  fs.last_seen,
  fs.total_duration_ms,
  fs.total_input_tokens,
  fs.total_output_tokens,
  fs.total_cache_creation_tokens,
  fs.total_cache_read_tokens,
  fs.total_cost,
  fs.integrity_issues
FROM flow_stats fs
LEFT JOIN event_stats es ON es.task_id = fs.task_id AND es.task_source = fs.task_source
LEFT JOIN tool_stats ts ON ts.task_id = fs.task_id AND ts.task_source = fs.task_source;
```

Note: If this view becomes a performance bottleneck at scale (>100k flows),
consider materializing as a table with explicit sync during retention cleanup.

## Analytics Support Tables

### pricing

Model pricing configuration. User-editable.

```sql
CREATE TABLE pricing (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  provider TEXT NOT NULL,
  model_pattern TEXT NOT NULL,          -- LIKE pattern (use % for wildcards)
  input_cost_per_1k REAL NOT NULL,      -- USD per 1K tokens
  output_cost_per_1k REAL NOT NULL,
  cache_creation_per_1k REAL,
  cache_read_per_1k REAL,
  effective_date TEXT NOT NULL,
  notes TEXT,

  UNIQUE (provider, model_pattern, effective_date)
);

-- Default Anthropic pricing (as of 2025-01)
-- Note: Matching logic should ORDER BY length(model_pattern) DESC to match most specific first
INSERT INTO pricing (provider, model_pattern, input_cost_per_1k, output_cost_per_1k, cache_creation_per_1k, cache_read_per_1k, effective_date) VALUES
  ('anthropic', 'claude-3-5-sonnet%', 0.003, 0.015, 0.00375, 0.0003, '2025-01-01'),
  ('anthropic', 'claude-3-5-haiku%', 0.0008, 0.004, 0.001, 0.00008, '2025-01-01'),
  ('anthropic', 'claude-3-opus%', 0.015, 0.075, 0.01875, 0.0015, '2025-01-01'),
  ('anthropic', 'claude-sonnet-4%', 0.003, 0.015, 0.00375, 0.0003, '2025-01-01'),
  ('anthropic', 'claude-opus-4%', 0.015, 0.075, 0.01875, 0.0015, '2025-01-01');

-- Price lookup query example (most specific pattern first):
-- SELECT * FROM pricing
-- WHERE provider = ? AND ? LIKE model_pattern
-- ORDER BY length(model_pattern) DESC, effective_date DESC
-- LIMIT 1;
```

### metrics

Time-series metrics for dashboard (optional, can query flows directly).

```sql
CREATE TABLE metrics (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  metric_name TEXT NOT NULL,
  metric_value REAL NOT NULL,
  dimensions TEXT CHECK (dimensions IS NULL OR json_valid(dimensions)),  -- JSON: {"host": "...", "model": "..."}
  timestamp TEXT NOT NULL,
  granularity TEXT CHECK (granularity IN ('minute', 'hour', 'day')),

  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
```

### drop_log

Record of dropped events for debugging.

```sql
CREATE TABLE drop_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  flow_id TEXT,
  event_type TEXT,
  priority TEXT CHECK (priority IN ('high', 'medium', 'low')),
  reason TEXT,  -- 'queue_full', 'timeout', etc.
  timestamp TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_drop_log_priority_time ON drop_log(priority, timestamp DESC);
CREATE INDEX idx_drop_log_flow ON drop_log(flow_id) WHERE flow_id IS NOT NULL;
```

## Indexes

### Flow queries

```sql
CREATE INDEX idx_flows_timestamp ON flows(timestamp DESC);
CREATE INDEX idx_flows_task_timestamp ON flows(task_id, timestamp DESC);
CREATE INDEX idx_flows_host_timestamp ON flows(host, timestamp DESC);
CREATE INDEX idx_flows_expires ON flows(expires_at) WHERE expires_at IS NOT NULL;
```

### Analytics queries

```sql
CREATE INDEX idx_flows_analytics ON flows(task_id, timestamp, total_cost, duration_ms);
CREATE INDEX idx_flows_model ON flows(model, timestamp);
CREATE INDEX idx_flows_signature ON flows(request_signature, timestamp DESC) WHERE request_signature IS NOT NULL;
CREATE INDEX idx_tool_invocations_analytics ON tool_invocations(tool_name, timestamp, cost, duration_ms);
CREATE INDEX idx_tool_invocations_task ON tool_invocations(task_id, timestamp);
```

### Event queries

```sql
-- Note: UNIQUE (flow_id, sequence) already creates an index, no need for idx_events_flow
CREATE INDEX idx_events_type_time ON events(event_type, timestamp);
CREATE INDEX idx_events_expires ON events(expires_at) WHERE expires_at IS NOT NULL;
```

### Metrics queries

```sql
CREATE INDEX idx_metrics_name_time ON metrics(metric_name, timestamp DESC);
CREATE INDEX idx_metrics_granularity ON metrics(granularity, timestamp DESC);
```

## Retention

TTL enforcement via scheduled cleanup:

```sql
-- Delete expired flows (cascades to events, tool_invocations)
DELETE FROM flows WHERE expires_at < datetime('now');

-- Delete expired events not already cascade-deleted
DELETE FROM events WHERE expires_at < datetime('now');

-- Delete old drop_log entries (keep 7 days)
DELETE FROM drop_log WHERE timestamp < datetime('now', '-7 days');

-- Delete old metrics (keep based on granularity)
DELETE FROM metrics WHERE
  (granularity = 'minute' AND timestamp < datetime('now', '-1 day')) OR
  (granularity = 'hour' AND timestamp < datetime('now', '-7 days')) OR
  (granularity = 'day' AND timestamp < datetime('now', '-90 days'));
```

## Notes

1. **Text IDs**: Using UUIDs as TEXT for simplicity. Could use BLOB for space efficiency.

2. **JSON fields**: `request_headers`, `response_headers`, `event_data`, `dimensions` are JSON stored as TEXT. SQLite's JSON functions can query them.

3. **Monotonic timestamps**: `timestamp_mono` uses `process.hrtime.bigint()` for ordering. Wall clock `timestamp` for display.

4. **Denormalization**: `task_id` is duplicated in `tool_invocations` for query performance.

5. **Cascade deletes**: Events and tool_invocations are deleted when their parent flow is deleted.

6. **Idempotent migrations**: All CREATE statements should use `IF NOT EXISTS`. Index creation should check existence first.
