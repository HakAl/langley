# Langley

An intercepting proxy that captures LLM API traffic, tracks costs, and surfaces anomalies. Supports Anthropic, OpenAI, AWS Bedrock, and Google Gemini.

## What It Does

Langley sits between your application and LLM provider APIs. Every request and response passes through, gets stored in SQLite, and appears in a real-time dashboard. Nothing reaches your agent that didn't come through the provider, and nothing leaves without being recorded.

```
Your App  --HTTPS-->  Langley Proxy  --HTTPS-->  LLM API
                          |
                       SQLite
                          |
                   WebSocket --> Dashboard
```

**Why this exists:** LLM agents make opaque, expensive API calls. Langley makes them visible. You see what your agent sends, what it gets back, what it costs, and where it wastes tokens.

## Quick Start

```bash
# 1. Build
go build -o langley ./cmd/langley

# 2. Trust the CA certificate (one-time)
langley setup

# 3. Start
./langley

# 4. Run your agent through the proxy
langley run claude
langley run python script.py
```

Open `http://localhost:9091` and enter the auth token shown at startup.

`langley run` sets `HTTPS_PROXY`, `HTTP_PROXY`, `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`, and `REQUESTS_CA_BUNDLE` automatically. Or set them yourself:

```bash
# Unix
export HTTPS_PROXY=http://localhost:9090

# PowerShell
$env:HTTPS_PROXY = "http://localhost:9090"
```

## Dashboard

Six views, all fed by real-time WebSocket updates:

| View | What you see |
|------|-------------|
| **Flows** | Every request/response with method, host, path, status, tokens, cost. Click to inspect headers and bodies. Filter by host, task, or status. |
| **Analytics** | Total cost, token counts, daily cost chart. The numbers that answer "how much did that session cost?" |
| **Tasks** | Flows grouped by task. See token usage and cost per logical unit of work, not just per request. |
| **Tools** | Tool invocation stats: call counts, success rates, average duration. Find the tools that fail or drag. |
| **Anomalies** | Flagged events: large contexts (>100k tokens), slow responses (>30s), rapid retries, high-cost requests (>$1), tool failures. |
| **Settings** | Configure task idle gap. |

Keyboard navigation: `j`/`k` to move, `Enter` to select, `Esc` to close, `?` for help. Dark/light theme toggle in the header.

## How Task Grouping Works

Langley groups related requests into tasks using a layered strategy:

1. **Explicit header** -- `X-Langley-Task` on the request
2. **Request metadata** -- User ID from the request body
3. **Inferred** -- Same host, gap of less than 5 minutes between requests

Inferred tasks are marked `task_source: 'inferred'` so you can filter them in analytics.

## Anomaly Detection

Langley flags unusual patterns automatically:

| Anomaly | Default Threshold | What it catches |
|---------|------------------|-----------------|
| Large context | >100k input tokens | Runaway context windows |
| Slow response | >30s | Stuck or overloaded endpoints |
| Rapid repeats | >5 calls in 10s | Retry loops |
| High cost | >$1 per request | Expensive single calls |
| Tool failures | Any failure | Broken tool integrations |
| Dropped events | Any drop | Backpressure in the proxy pipeline |

All thresholds are configurable in `langley.yaml`.

## Configuration

YAML config with environment variable overrides. Config file locations:

- **Unix**: `~/.config/langley/langley.yaml`
- **Windows**: `%APPDATA%\langley\langley.yaml`

```yaml
proxy:
  listen: "localhost:9090"

auth:
  token: "your-secret-token"  # Auto-generated if not set

persistence:
  body_max_bytes: 1048576     # 1MB max body storage per flow

redaction:
  always_redact_headers:
    - authorization
    - x-api-key
    - cookie
    - set-cookie
  pattern_redact_headers:
    - "^x-.*-token$"
    - "^x-.*-key$"
  redact_api_keys: true       # Masks sk-*, AKIA*, AIza* patterns
  redact_base64_images: true  # Replaces images with placeholders
  disable_body_storage: false  # Set to true to stop storing bodies

retention:
  flows_ttl_days: 30
  events_ttl_days: 7
  drop_log_ttl_days: 1

analytics:
  anomaly_context_tokens: 100000
  anomaly_tool_delay_ms: 30000
  anomaly_rapid_calls_window_s: 10
  anomaly_rapid_calls_threshold: 5
```

See `langley.example.yaml` for the full annotated config.

### Environment Variables

| Variable | Overrides |
|----------|-----------|
| `LANGLEY_LISTEN` | `proxy.listen` |
| `LANGLEY_AUTH_TOKEN` | `auth.token` |
| `LANGLEY_DB_PATH` | `persistence.db_path` |

Relative paths in `LANGLEY_DB_PATH` resolve from the working directory. Use absolute paths when running as a service.

## CLI Reference

```
langley [OPTIONS]
langley <command> [args]

COMMANDS:
  run <cmd> [args]    Run a command with proxy environment configured
  setup               Install CA certificate to system trust store
  token show          Show the current auth token
  token rotate        Generate a new auth token

OPTIONS:
  -config <path>      Path to configuration file
  -listen <addr>      Proxy listen address (default: localhost:9090)
  -api <addr>         API server address (default: localhost:9091)
  -version            Show version information
  -show-ca            Show CA certificate path and trust instructions
  -help               Show help
```

## API

All endpoints require `Authorization: Bearer <token>`. Rate limited to 20 req/sec sustained, 100 burst.

### Flows

| Endpoint | Description |
|----------|-------------|
| `GET /api/flows` | List flows. Params: `limit`, `host`, `task_id`, `model` |
| `GET /api/flows/{id}` | Single flow with full detail |
| `GET /api/flows/{id}/events` | SSE events for a streaming flow |
| `GET /api/flows/{id}/anomalies` | Anomalies linked to a flow |
| `GET /api/flows/export` | Export. Params: `format` (ndjson/json/csv), `max_rows`, `include_bodies` |
| `GET /api/flows/count` | Count flows matching filters |

### Analytics

| Endpoint | Description |
|----------|-------------|
| `GET /api/stats` | Overall statistics |
| `GET /api/analytics/tasks` | Per-task summaries |
| `GET /api/analytics/tasks/{id}` | Single task detail |
| `GET /api/analytics/tools` | Tool invocation stats |
| `GET /api/analytics/cost/daily` | Daily cost breakdown |
| `GET /api/analytics/cost/model` | Cost by model |
| `GET /api/analytics/anomalies` | Recent anomalies |

### System

| Endpoint | Description |
|----------|-------------|
| `GET /api/health` | Health check (no auth required) |
| `GET /api/settings` | Current settings |
| `PUT /api/settings` | Update settings |
| `WS /ws` | Real-time flow updates. Auth via `token` query param. |

Full API spec in `openapi.yaml`.

## Security

**Credential redaction** happens on write. Sensitive data never reaches disk:
- Authorization headers, API keys, cookies stripped before storage
- Body patterns (`sk-ant-*`, `sk-*`, `AKIA*`, `AIza*`) masked
- Base64 images replaced with `[IMAGE base64 redacted]`
- Configurable patterns for custom secrets

**TLS**: Upstream certificates validated by default. CA private key at 0600 permissions. Certificates use random serial numbers.

**Authentication**: Bearer token on all API endpoints. WebSocket validates localhost origin. Tokens auto-generated and stored in config.

**Network**: Proxy and API bind to localhost only. No remote access by default.

## Architecture

- **Go backend** -- MITM proxy, REST API, WebSocket server, analytics engine
- **SQLite** -- WAL mode, async batch writes, TTL-based retention, hourly cleanup
- **React frontend** -- Vite build, WebSocket for live updates, hash-based routing

Provider detection is pluggable. Each provider (Anthropic, OpenAI, Bedrock, Gemini) implements host detection and response parsing through a common interface.

See `docs/ARCHITECTURE.md` for internals: data flow, store interface, extension points.

## Development

```bash
make install-deps   # Go modules + npm install (first time)
make dev            # Backend + Vite dev server with hot reload
make test           # All tests with race detector
make check          # Lint + test (quality gate)
make build          # Production binary + frontend bundle
make stop           # Kill orphaned dev processes (Windows Ctrl+C workaround)
```

Dashboard at `http://localhost:5173` during development. Proxy at `localhost:9090`.

Frontend tests use Playwright. Backend tests use Go's race detector.

## License

MIT
