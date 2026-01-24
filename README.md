# Langley

An intercepting proxy for Claude API traffic with persistence, real-time monitoring, and analytics.

## Overview

Langley sits between your application and the Claude API, capturing all traffic for debugging, cost tracking, and analysis. It provides:

- **Real-time Dashboard** - WebSocket-powered flow viewer with instant updates
- **Token & Cost Tracking** - Per-request and per-task cost breakdown
- **TLS Interception** - Transparent MITM proxy with dynamic certificates
- **Credential Redaction** - API keys and sensitive data never hit disk
- **Anomaly Detection** - Alerts for large contexts, slow responses, rapid retries
- **Task Grouping** - Group related requests by task boundary detection

## Quick Start

### 1. Build

```bash
go build -o langley ./cmd/langley
```

### 2. Trust the CA Certificate

On first run, Langley generates a CA certificate. You must trust it to intercept HTTPS:

```bash
# Show the CA path and trust instructions
./langley -show-ca

# macOS
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain ~/.config/langley/certs/ca.crt

# Linux
sudo cp ~/.config/langley/certs/ca.crt /usr/local/share/ca-certificates/langley.crt
sudo update-ca-certificates

# Windows (run as Administrator)
certutil -addstore -f "ROOT" %APPDATA%\langley\certs\ca.crt
```

### 3. Start Langley

```bash
./langley
```

You'll see output like:
```
  Proxy:     http://localhost:9090
  API:       http://localhost:9091
  WebSocket: ws://localhost:9091/ws
  CA:        ~/.config/langley/certs/ca.crt
  DB:        ~/.config/langley/langley.db
  Token:     langley_abc123...
```

### 4. Configure Your Client

Set environment variables to route traffic through Langley:

```bash
export HTTP_PROXY=http://localhost:9090
export HTTPS_PROXY=http://localhost:9090
```

Or configure Claude Code directly:
```bash
claude-code --proxy http://localhost:9090
```

### 5. Open the Dashboard

Navigate to `http://localhost:9091` and enter your auth token (shown at startup).

## Features

### Real-time Flow Monitoring
- Watch requests flow through in real-time via WebSocket
- Click any flow to see full request/response details
- Filter by host, task, or status code

### Analytics Dashboard
- Total cost, tokens, and request counts
- Daily cost chart
- Per-task breakdown with token usage
- Tool invocation statistics with success rates

### Anomaly Detection
Langley automatically flags:
- **Large Context** - Input tokens exceeding threshold (default: 100k)
- **Slow Responses** - Requests taking longer than threshold (default: 30s)
- **Rapid Repeats** - Multiple similar requests in short window (possible retries)
- **High Cost** - Single requests costing more than threshold (default: $1)
- **Tool Failures** - Failed tool invocations
- **Dropped Events** - Events lost due to backpressure

### Task Grouping
Langley groups related requests into tasks using:
1. **Explicit** - `X-Langley-Task` header
2. **Metadata** - User ID from request body
3. **Inferred** - Same host with 5-minute idle gap

## Configuration

Langley uses YAML configuration with environment variable overrides.

### Config File Locations
- **Windows**: `%APPDATA%\langley\langley.yaml`
- **Unix**: `~/.config/langley/langley.yaml`

### Example Configuration

```yaml
# Proxy settings
proxy:
  listen: "localhost:9090"

# Authentication
auth:
  token: "your-secret-token"  # Auto-generated if not set

# Data persistence
persistence:
  db_path: "langley.db"
  body_max_bytes: 1048576  # 1MB

# Credential redaction
redaction:
  always_redact_headers:     # Headers always redacted
    - "authorization"
    - "x-api-key"
    - "cookie"
    - "set-cookie"
  pattern_redact_headers:    # Regex patterns for headers
    - "^x-.*-token$"
    - "^x-.*-key$"
  redact_api_keys: true      # Redact sk-*, AKIA*, AIza* patterns in body
  redact_base64_images: true # Replace base64 images with placeholders
  raw_body_storage: false    # Store redacted bodies (security: keep OFF)

# Data retention
retention:
  flows_ttl_days: 30
  events_ttl_days: 7
  drop_log_ttl_days: 1
```

### Environment Variables

| Variable | Description |
|----------|-------------|
| `LANGLEY_LISTEN` | Proxy listen address (e.g., `localhost:9090`) |
| `LANGLEY_AUTH_TOKEN` | API authentication token |
| `LANGLEY_DB_PATH` | Database file path |

**Note**: Relative paths in `LANGLEY_DB_PATH` are relative to the current working directory. When running as a Windows service, use absolute paths or set the service's "Start in" directory.

## CLI Reference

```
langley [OPTIONS]

OPTIONS:
    -config <path>    Path to configuration file
    -listen <addr>    Proxy listen address (default: from config or localhost:9090)
    -api <addr>       API/WebSocket server address (default: localhost:9091)
    -version          Show version information
    -show-ca          Show CA certificate path and trust instructions
    -help             Show this help message
```

## API Endpoints

All endpoints require `Authorization: Bearer <token>` header.

### Flows
- `GET /api/flows` - List flows (supports `?limit=`, `?host=`, `?task_id=`, `?model=`)
- `GET /api/flows/{id}` - Get flow details
- `GET /api/flows/{id}/events` - Get SSE events for a flow
- `GET /api/flows/{id}/anomalies` - Get anomalies for a flow

### Analytics
- `GET /api/stats` - Overall statistics
- `GET /api/analytics/tasks` - Task summaries
- `GET /api/analytics/tasks/{id}` - Single task details
- `GET /api/analytics/tools` - Tool usage statistics
- `GET /api/analytics/cost/daily` - Daily cost breakdown
- `GET /api/analytics/cost/model` - Cost by model
- `GET /api/analytics/anomalies` - Recent anomalies

### WebSocket
- `ws://localhost:9091/ws?token=<token>` - Real-time flow updates

## Security

### Credential Redaction
- Authorization headers, API keys, and cookies are redacted before storage
- Configurable patterns for custom secrets
- Base64-encoded images are replaced with placeholders

### TLS Security
- Upstream TLS certificates are validated by default
- CA private key uses 0600 permissions
- Certificates use random serial numbers

### Authentication
- All API endpoints require bearer token authentication
- WebSocket connections validate origin (localhost only)
- Tokens are auto-generated and stored securely

## Architecture

```
Client -> [HTTPS] -> Langley Proxy -> [HTTPS] -> Claude API
                          |
                          v
                     SQLite DB
                          |
                          v
              WebSocket -> Dashboard
```

- **Go Backend** - HTTP/TLS proxy, REST API, WebSocket server
- **SQLite** - WAL mode, async writes, TTL-based retention
- **React Frontend** - Real-time updates, responsive design

## Development

```bash
# Build
go build ./cmd/langley

# Run tests
go test ./...

# Build frontend
cd web && npm install && npm run build
```

## License

MIT
