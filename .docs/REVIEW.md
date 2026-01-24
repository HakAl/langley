Architecture Review Summary

Refinement

CRITICAL: Task Boundary Source

There is no conversation_id in the Claude SSE stream. message.id is per API call.

Revised approach:

Task assignment (explicit and honest about limitations):
- Priority 1: X-Langley-Task header (explicit, reliable)
- Priority 2: request.metadata.user_id (if present)
- Priority 3: host + idle gap heuristic (5 min idle = new task)

Notes:
- Heuristic grouping is best-effort and marked task_source: inferred.
- Users who care about accuracy should use X-Langley-Task.
- Analytics should clearly label inferred tasks.

HIGH: Priority Queue Full of Critical Events

Defined behavior:
- < 80% full: normal
- 80-95% full: warn + flush faster
- 95-99% full: drop LOW only
- 100% full (all HIGH): drop oldest HIGH, increment critical_drop_counter

Invariant: proxy never blocks on persistence.
Alternative: spill to disk and replay (more complex).

HIGH: Redaction Hash Safety

No hash correlation. Store only [REDACTED].
Reason: key patterns are low entropy and hashable.

HIGH: Auth Token Retrieval

No API endpoint.
Token acquisition:
- Generated on first run, written to config file
- Displayed once on startup with warning
- User copies from file or console

Config location:
- Linux/Mac: ~/.config/langley/config.yaml
- Windows: %APPDATA%\langley\config.yaml

MEDIUM: File Permissions Cross-Platform

Use platform-specific protection:
- Windows: icacls to restrict file to current user
- Unix: chmod 600

Documentation should state that Windows uses ACLs, not chmod.

LOW: Mojibake

Use ASCII for diagrams and tables.

Updated Task Boundary Design

task_assignment:
  header: X-Langley-Task
  metadata_field: metadata.user_id
  heuristic:
    enabled: true
    idle_gap_minutes: 5
    group_by: [host]
  sources:
    - explicit
    - metadata
    - inferred

UI labels:
- [EXPLICIT] user-provided
- [METADATA] from request
- [INFERRED] heuristic (may be wrong)

Analytics should allow filtering by task_source.

Persistence Pipeline (Revised)

Proxy thread
  -> Priority queue (bounded)
     - HIGH: message_start, message_stop, usage
     - MED: content_block_start/stop
     - LOW: content_block_delta (drop first)
  -> Writer thread (better-sqlite3 in worker)
     - flows: immediate write
     - events: batch by time/count
     - ack back to memory manager
  -> Memory manager
     - evict only after persistence ack
     - track last_persisted_index per flow
     - LRU for flows (keep last N)

Security Layer (Revised)

Request in -> TLS terminate -> redaction -> storage
- Redact headers: auth, api-key, cookie, x-*-token, x-*-key
- Redact bodies: sk-*, base64 images, JSON credential fields
- Never log raw values
- Gated raw body storage (config flag, default OFF)

TLS Security (Revised)
- CA key protected (chmod/ACL)
- Optional passphrase support
- Random serial numbers
- Upstream TLS validation ON by default
- InsecureSkipVerify only via opt-in per host with warning
- Cert cache LRU, max 1000 entries

Access Control (Revised)
- Bearer token auth for API
- Token in config/env or generated on first run
- No token endpoint
- WebSocket origin validation (reject non-localhost origins)
- Optional token in query param
- Unix socket permissions 0600 / Windows ACLs

Graceful Shutdown (Revised)
- Stop accepting new connections
- Drain queue with timeout
- Mark active flows status=interrupted
- Flush pending batches
- Close SQLite cleanly

Indexes (Revised)

Flow queries:
- idx_flows_timestamp (timestamp DESC)
- idx_flows_task_timestamp (task_id, timestamp DESC)
- idx_flows_host_timestamp (host, timestamp DESC)

Analytics queries:
- idx_flows_analytics (task_id, timestamp, total_cost, duration_ms)
- idx_tool_invocations_analytics (tool_name, timestamp, cost, duration_ms)

Event queries:
- idx_events_flow (flow_id, sequence)
- idx_events_type_time (event_type, timestamp)

Updated Scope

Additions:
- Priority queue with event criticality levels
- Persistence ack before memory eviction
- Task assignment: explicit > metadata > heuristic
- Redaction layer (no hashes)
- Bearer token authentication (no token endpoint)
- CA key protection
- TLS validation by default
- Graceful shutdown with queue drain
- Request body streaming with limits
- LRU cert cache
- Retry correlation via explicit request IDs (structural signature hashing opt-in only)
- Monotonic time for ordering

Deferred (post-MVP):
- Complex anomaly detection (query ad-hoc first)
- Bedrock pricing and provider abstraction
- Structural signature hashing for retry detection (opt-in)

Data Retention

retention:
  flows_ttl_days: 30
  events_ttl_days: 7
  bodies_ttl_days: 3
  drop_log_ttl_days: 7

maintenance:
  vacuum_threshold_percent: 25   # VACUUM only when free pages > 25%
  wal_autocheckpoint: 1000       # Pages
  cleanup_interval_hours: 1

Implementation Notes (Final)

- Task heuristic: add interleaved-traffic warning when inferred tasks see overlapping requests from same host.
- Queue drops: if any LOW events dropped for a flow, mark flow_integrity=partial; if HIGH missing, mark flow_integrity=corrupted and create skeleton rows as needed.
- Queue sizing: enforce byte-based limits and per-flow byte caps; truncate/redact bodies before enqueue.
- SQLite: enforce WAL mode and synchronous=NORMAL in writer worker.
