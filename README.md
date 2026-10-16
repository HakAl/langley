# Langley

A transparent proxy that records every LLM API call — full request/response bodies, token counts, costs, tool use, and anomalies — in a real-time dashboard. Supports Anthropic, OpenAI, AWS Bedrock, and Google Gemini.

## What It Does

Langley sits between your application and LLM provider APIs. Every request and response passes through, gets stored in SQLite, and appears in a real-time dashboard. Nothing reaches your agent that didn't come through the provider, and nothing leaves without being recorded.

```
Your App  --HTTPS-->  Langley Proxy  --HTTPS-->  LLM API
                          |
                       SQLite
                          |
                   WebSocket --> Dashboard
```

**Why this exists:** LLM agents make opaque, expensive API calls. Langley makes them visible — every message, tool call, token, and dollar. You see exactly what your agent sends, what it gets back, and where it wastes money.

## Quick Start

**Prerequisites:** Go 1.24+ and Node 20.19+ or 22.12+ (`^20.19.0 || >=22.12.0`).

```bash
# 1. Build (or `make install` to install `langley` onto your PATH)
make build
# (Or manually: cd web && npm install && npm run build && cd .. && go build -o langley ./cmd/langley)

# 2. Trust the CA certificate (one-time)
./langley setup

# 3. Start Langley — proxy, dashboard, and a proxied shell in this terminal
./langley

# 4. Run your agent right there
claude
python script.py
exit            # (or Ctrl+D) stops Langley
```

`make build` produces `langley` on Unix (macOS/Linux) and `langley.exe` on Windows. Run `make install` to install `langley` into `/usr/local/bin` (falling back to `~/.local/bin` when `/usr/local/bin` is not writable) so you can run `langley` from any directory.

In an interactive terminal, `langley` is a single-terminal session:

- Starts the proxy (`:9090`) and the embedded dashboard (`:9091`) in-process, and opens the dashboard in your browser. Enter the auth token shown in the startup banner (no separate web server is needed).
- Drops you into your `$SHELL` with the proxy environment already set, plus `LANGLEY_ACTIVE=1`.
- Installs a temporary hook in `~/.zshrc` / `~/.bashrc` (between `# >>> langley hook >>>` markers) that sources `~/.config/langley/active.env` and registers a prompt hook (`_langley_sync`), so new terminal tabs opened while Langley runs are proxied too and automatically clear proxy variables when Langley exits.
- In terminal tabs that were already open before Langley started, run `langley` to attach a proxied subshell to the running instance, or run `eval "$(langley env)"` to adopt the proxy environment and automatic prompt synchronization in-place.
- Writes server logs to `~/.config/langley/langley.log` so they don't interfere with interactive tools.

Use `-no-shell` to run as a foreground server instead (logs to the terminal; use `langley run <cmd>` from another terminal), `-no-open` to skip opening the browser, and `-no-hook` to leave your RC files alone. The `LANGLEY_NO_SHELL`, `LANGLEY_NO_OPEN`, and `LANGLEY_NO_HOOK` environment variables do the same. Non-interactive runs (no TTY) always use foreground server mode and never touch RC files.

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
| **Flows** | Every request/response with method, host, path, status, tokens, cost. Click any flow to inspect full request/response headers and bodies — including decompressed content, with a tabbed viewer. Filter by host, task, status, method, path, or model; sort by time, cost, tokens, or duration. Export to NDJSON, JSON, or CSV. |
| **Analytics** | Total cost, token counts, daily cost chart. The numbers that answer "how much did that session cost?" |
| **Tasks** | Flows grouped by task. See token usage and cost per logical unit of work, not just per request. |
| **Tools** | Tool invocation stats: call counts, success rates, average duration. Click any tool to drill into individual invocations — see the exact input JSON sent to each tool and the result returned, with duration and parent flow context. |
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

All thresholds are configurable in `config.yaml`.

## CLI Reference

```
langley [OPTIONS]
langley <command> [args]

COMMANDS:
  run <cmd> [args]    Run a command with proxy environment configured
  env                 Print shell snippet to adopt proxy & prompt sync in-place
  setup               Install CA certificate to system trust store
  token show          Show the current auth token
  token rotate        Generate a new auth token

OPTIONS:
  -config <path>      Path to configuration file
  -listen <addr>      Proxy listen address (default: localhost:9090)
  -api <addr>         API server address (default: localhost:9091)
  -version            Show version information
  -show-ca            Show CA certificate path and trust instructions
  -no-shell           Run as a foreground server (no interactive subshell)
  -no-open            Do not open the dashboard in a browser on startup
  -no-hook            Do not install the temporary shell hook for new tabs
  -help               Show help
```

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
make install        # Build and install to /usr/local/bin (falls back to ~/.local/bin)
make install-deps   # Go modules + npm install (first time)
make dev            # Backend + Vite dev server with hot reload
make test           # All tests with race detector
make check          # Lint + test (quality gate)
make build          # Production binary + frontend bundle
make stop           # Kill orphaned dev processes (Windows Ctrl+C workaround)
```

Dashboard at `http://localhost:3000` during development. Proxy at `localhost:9090`.

Frontend tests use Playwright. Backend tests use Go's race detector.

Use `make install PREFIX=/custom/prefix` or `make install BINDIR=/custom/bin` to choose the installation directory. Ensure the installed directory is on your `PATH`.
