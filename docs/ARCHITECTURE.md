# Langley Architecture

This document describes the internal architecture of Langley, an HTTPS intercepting proxy for monitoring LLM API traffic.

## Overview

Langley captures HTTP/HTTPS requests flowing through it, stores them in SQLite, and exposes a REST API + WebSocket for real-time monitoring via a React dashboard.

```
                        ┌──────────────────────────────────────────────────┐
                        │                    Langley                       │
                        │                                                  │
┌──────────┐  HTTPS     │  ┌──────────┐    ┌──────────┐    ┌──────────┐    │     ┌──────────┐
│  Client  │───────────►│  │  MITM    │───►│ Provider │───►│  Store   │    │────►│  SQLite  │
│ (Claude  │            │  │  Proxy   │    │ Registry │    │(persist) │    │     │    DB    │
│  Code)   │◄───────────│  │          │◄───│          │    │          │    │     └──────────┘
└──────────┘  Response  │  └─────┬────┘    └──────────┘    └─────┬────┘    │
                        │        │                               │         │
                        │        │  Flow/Event callbacks         │         │
                        │        ▼                               │         │
                        │   ┌──────────┐    ┌──────────┐    ┌──────────┐   │     ┌──────────┐
                        │   │ WebSocket│◄───│   API    │◄───│Analytics │   │────►│ Dashboard│
                        │   │   Hub    │    │  Server  │    │  Engine  │   │     │  (React) │
                        │   └──────────┘    └──────────┘    └──────────┘   │     └──────────┘
                        │                                                  │
                        └──────────────────────────────────────────────────┘
```

## Data Flow

### 1. Request Capture (Proxy → Store)

1. **Client connects** via HTTP CONNECT for HTTPS tunneling
2. **TLS handshake** - Proxy generates a certificate signed by its CA for the target host
3. **Request interception**:
   - Read request from client
   - Extract headers and body (with size limits from `config.Persistence.BodyMaxBytes`)
   - Assign a `task_id` via `TaskAssigner` (from headers, request metadata, or inference)
   - Redact sensitive data via `Redactor` (API keys, credentials, images)
   - Save initial `Flow` to store immediately (so SSE events can reference it)
   - Forward request to upstream server
4. **Response interception**:
   - Read response from upstream
   - For SSE responses: parse events via `SSEParser`, save each `Event` to store
   - Extract token usage via `Provider.ParseUsage()`
   - Calculate cost via `Analytics.CalculateCost()` using pricing table
   - Update `Flow` with response data, duration, token counts, cost
5. **Real-time notification** - Call `onFlow`, `onUpdate`, `onEvent` callbacks to broadcast via WebSocket

### 2. Storage Layer (Store Interface)

The `Store` interface (`internal/store/store.go`) defines the persistence contract:

```go
type Store interface {
    // Flows - HTTP request/response pairs
    SaveFlow(ctx context.Context, flow *Flow) error
    UpdateFlow(ctx context.Context, flow *Flow) error
    GetFlow(ctx context.Context, id string) (*Flow, error)
    ListFlows(ctx context.Context, filter FlowFilter) ([]*Flow, error)
    CountFlows(ctx context.Context, filter FlowFilter) (int, error)

    // Events - SSE events parsed from streaming responses
    SaveEvent(ctx context.Context, event *Event) error
    SaveEvents(ctx context.Context, events []*Event) error
    GetEventsByFlow(ctx context.Context, flowID string) ([]*Event, error)

    // Tool Invocations - extracted from Claude tool_use responses
    SaveToolInvocation(ctx context.Context, inv *ToolInvocation) error

    // Maintenance
    RunRetention(ctx context.Context) (deleted int64, err error)
    Close() error
    DB() interface{} // Exposes underlying *sql.DB for analytics queries
}
```

### 3. API Layer (Store → Dashboard)

The REST API (`internal/api/api.go`) serves the dashboard:

- **Authentication**: Bearer token or session cookie (localhost origins auto-authenticated)
- **Rate limiting**: Token bucket (20 req/sec sustained, 100 burst)
- **CORS**: Only localhost origins allowed
- **Middleware chain**: CORS → Rate Limit → Auth → Handler

### 4. Real-time Updates (WebSocket Hub)

The WebSocket hub (`internal/ws/websocket.go`) broadcasts events to connected clients:

```go
type Message struct {
    Type      string      // "flow_start", "flow_update", "flow_complete", "event", "ping"
    Timestamp time.Time
    Data      interface{} // Flow summary or Event
}
```

## Key Abstractions

### Store (`internal/store/store.go`)

Defines the persistence interface. Current implementation: SQLiteStore.

**Data models:**
- `Flow` - Complete HTTP request/response with metadata, tokens, cost
- `Event` - Individual SSE event (type, data, sequence number)
- `ToolInvocation` - Tool usage record (name, duration, success)
- `FlowFilter` - Query parameters for listing/filtering flows

### Provider (`internal/provider/provider.go`)

Interface for LLM provider detection and response parsing:

```go
type Provider interface {
    Name() string                              // "anthropic", "openai", "bedrock", etc.
    DetectHost(host string) bool               // Does this provider handle this host?
    ParseUsage(body []byte, isSSE bool) (*Usage, error)  // Extract token counts
}
```

**Implementations:**
- `AnthropicProvider` - api.anthropic.com
- `OpenAIProvider` - api.openai.com
- `BedrockProvider` - bedrock-runtime.*.amazonaws.com
- `GeminiProvider` - generativelanguage.googleapis.com

### Redactor (`internal/redact/redact.go`)

Masks sensitive data before storage:

- **Headers**: Authorization, x-api-key, cookies, etc.
- **Bodies**: API keys (sk-ant-*, sk-*, AKIA*, AIza*), credential JSON fields, base64 images
- **Configuration**: `AlwaysRedactHeaders`, `PatternRedactHeaders`, `RedactAPIKeys`, `RedactBase64Images`

### Analytics Engine (`internal/analytics/analytics.go`)

Calculates costs and aggregates metrics:

- `CalculateCost()` - Uses pricing table to compute cost from token counts
- `GetTaskSummary()` - Aggregates flows by task_id
- `GetToolStats()` - Tool usage statistics
- `GetCostByDay()` / `GetCostByModel()` - Cost breakdowns
- `DetectFlowAnomalies()` - Identifies unusual patterns

### Task Assigner (`internal/task/assignment.go`)

Groups related flows by task ID:

1. Check explicit `X-Task-ID` header
2. Check request metadata (e.g., Claude's session identifiers)
3. Infer from timing (flows within `IdleGapMinutes` of each other)

### Certificate Authority (`internal/tls/ca.go`)

Manages TLS interception:

- `LoadOrCreateCA()` - Load or generate root CA
- `CertCache` - LRU cache of per-host certificates
- Certificates include CRL URL for Windows compatibility

## Project Structure

```
langley/
├── cmd/langley/main.go       # Entry point, wiring
├── internal/
│   ├── api/
│   │   ├── api.go            # REST API handlers
│   │   ├── export.go         # NDJSON/CSV export
│   │   └── ratelimit.go      # Token bucket rate limiter
│   ├── analytics/
│   │   ├── analytics.go      # Cost/metrics calculations
│   │   └── anomaly.go        # Anomaly detection
│   ├── config/config.go      # Configuration loading
│   ├── parser/sse.go         # SSE event parser
│   ├── provider/
│   │   ├── provider.go       # Provider interface
│   │   ├── registry.go       # Provider registry
│   │   ├── anthropic.go      # Anthropic implementation
│   │   ├── openai.go         # OpenAI implementation
│   │   ├── bedrock.go        # AWS Bedrock implementation
│   │   └── gemini.go         # Google Gemini implementation
│   ├── proxy/
│   │   ├── proxy.go          # Basic HTTP proxy
│   │   └── mitm.go           # MITM TLS interception
│   ├── redact/redact.go      # Credential redaction
│   ├── store/
│   │   ├── store.go          # Store interface + models
│   │   └── sqlite.go         # SQLite implementation
│   ├── task/assignment.go    # Task ID grouping
│   ├── tls/
│   │   ├── ca.go             # Certificate authority
│   │   └── certcache.go      # Certificate cache
│   └── ws/websocket.go       # WebSocket hub
├── web/                      # React dashboard (Vite)
└── Makefile
```

## Extension Points

### Adding a New Provider

1. **Create implementation** in `internal/provider/`:

```go
// internal/provider/newprovider.go
package provider

type NewProvider struct{}

func (p *NewProvider) Name() string { return "newprovider" }

func (p *NewProvider) DetectHost(host string) bool {
    return strings.Contains(host, "newprovider.com")
}

func (p *NewProvider) ParseUsage(body []byte, isSSE bool) (*Usage, error) {
    // Parse provider-specific response format
    var resp struct {
        Usage struct {
            InputTokens  int `json:"input_tokens"`
            OutputTokens int `json:"output_tokens"`
        } `json:"usage"`
        Model string `json:"model"`
    }
    if err := json.Unmarshal(body, &resp); err != nil {
        return nil, err
    }
    return &Usage{
        InputTokens:  resp.Usage.InputTokens,
        OutputTokens: resp.Usage.OutputTokens,
        Model:        resp.Model,
    }, nil
}
```

2. **Register in registry** (`internal/provider/registry.go`):

```go
func NewRegistry() *Registry {
    r := &Registry{providers: make(map[string]Provider)}
    r.Register(&AnthropicProvider{})
    r.Register(&OpenAIProvider{})
    r.Register(&NewProvider{})  // Add here
    return r
}
```

3. **Add pricing data** - Insert rows into the `pricing` table:

```sql
INSERT INTO pricing (provider, model_pattern, input_cost_per_1k, output_cost_per_1k, effective_date)
VALUES ('newprovider', '%', 0.001, 0.002, '2024-01-01');
```

4. **Add tests** in `internal/provider/newprovider_test.go`

### Adding a New API Endpoint

1. **Define handler** in `internal/api/api.go`:

```go
func (s *Server) getNewThing(w http.ResponseWriter, r *http.Request) {
    ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
    defer cancel()

    // Parse params
    id := r.PathValue("id")  // Go 1.22+ path parameters

    // Query store or analytics
    result, err := s.store.GetNewThing(ctx, id)
    if err != nil {
        s.logger.Error("failed to get new thing", "error", err)
        http.Error(w, "Internal error", http.StatusInternalServerError)
        return
    }

    s.writeJSON(w, result)
}
```

2. **Register route** in `NewServer()`:

```go
s.mux.HandleFunc("GET /api/newthing/{id}", s.authMiddleware(s.getNewThing))
```

3. **Define response types** at the bottom of `api.go`:

```go
type NewThingResponse struct {
    ID    string `json:"id"`
    Value string `json:"value"`
}
```

4. **Update OpenAPI spec** in `openapi.yaml`

5. **Add tests** in `internal/api/api_test.go`

### Adding Store Methods

1. **Add to interface** in `internal/store/store.go`:

```go
type Store interface {
    // ... existing methods
    GetNewThing(ctx context.Context, id string) (*NewThing, error)
}
```

2. **Implement in SQLite** (`internal/store/sqlite.go`):

```go
func (s *SQLiteStore) GetNewThing(ctx context.Context, id string) (*NewThing, error) {
    row := s.db.QueryRowContext(ctx, `
        SELECT id, value FROM new_things WHERE id = ?
    `, id)

    var thing NewThing
    if err := row.Scan(&thing.ID, &thing.Value); err != nil {
        return nil, err
    }
    return &thing, nil
}
```

3. **Add schema migration** if needed (in `initSchema()`)

## Testing Strategy

### Unit Tests

Test individual components in isolation:

- **Provider tests** (`*_test.go`) - Parse real API response samples
- **Redactor tests** - Verify sensitive data is masked
- **Store tests** - Use in-memory SQLite with `:memory:`

### Integration Tests

- **API tests** (`internal/api/api_test.go`) - Test HTTP handlers with mock store
- **Proxy tests** (`internal/proxy/proxy_test.go`) - Test MITM with test servers

### E2E Tests

- **Playwright tests** (`web/tests/`) - Test React dashboard interactions
- **Go E2E tests** (`test/e2e/`) - Full proxy + API integration

### Running Tests

```bash
# All tests with race detector
make test

# Specific package
go test -v ./internal/store/...

# Coverage report
make test-cover
```

### Test Guidelines

1. **Mock external dependencies** - Never call real LLM APIs in tests
2. **Use interfaces** - Store, Provider, etc. are interfaces for test doubles
3. **Test behavior, not implementation** - Tests should survive refactoring
4. **Cover edge cases** - Empty inputs, timeouts, malformed data

## Configuration

Configuration is loaded from (in order of precedence):

1. CLI flags (`-listen`, `-api`, etc.)
2. Environment variables (`LANGLEY_*`)
3. Config file (`config.yaml`)
4. Default values

Key configuration sections:

| Section | Purpose |
|---------|---------|
| `proxy` | Listen address for the proxy |
| `persistence` | Database path, body size limits |
| `retention` | TTL for flows, events, bodies |
| `redaction` | What to redact (headers, API keys, images) |
| `auth` | API authentication token |
| `task` | Task grouping (idle gap) |
| `analytics` | Anomaly detection thresholds |

## Security Considerations

- **TLS validation** - Upstream connections validate certificates by default
- **Credential redaction** - API keys masked before storage
- **Body storage** - Request/response bodies stored after redaction (can be disabled via `disable_body_storage`)
- **Localhost-only** - Dashboard/API only accessible from localhost
- **Token auth** - All API endpoints require Bearer token
- **No URL tokens** - Tokens in query strings are rejected

## Performance Notes

- **SQLite WAL mode** - Enables concurrent reads during writes
- **Batch event writes** - SSE events batched for efficiency
- **LRU certificate cache** - Avoids regenerating TLS certs
- **Rate limiting** - Protects API from abuse
- **Body size limits** - Prevents memory exhaustion
- **Retention cleanup** - Hourly job deletes expired data
