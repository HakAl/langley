---
name: e2e-smoke
description: >
  End-to-end smoke tests against a live Langley instance using Playwright MCP browser.
  Starts server if needed, generates traffic, verifies dashboard rendering and navigation.
invoke: user
---

# E2E Smoke Test

Run a full end-to-end smoke test against Langley: server health, proxy traffic, and dashboard UI verification via Playwright MCP browser tools.

## Phase 1 - Ensure Server Running

1. **Read state file** to discover addresses:
   - Windows: `%APPDATA%\langley\state.json`
   - Unix: `~/.config/langley/state.json`
   - PowerShell: `%APPDATA%` is `$env:APPDATA`
   - Parse JSON -> `{ proxy_addr, api_addr, ca_path, pid, started_at }`

2. **Health check** the API:
   - `curl -sf http://{api_addr}/api/health`
   - Health endpoint is unauthenticated - no token needed.

3. **If not running**, build and start:
   ```bash
   # Windows (PowerShell)
   go build -o langley.exe ./cmd/langley/; Start-Process -FilePath .\langley.exe

   # Unix
   go build -o langley ./cmd/langley/ && ./langley &
   ```
   - Wait up to 10 seconds, polling `/api/health` every 1s.
   - Re-read `state.json` after startup to get actual addresses.
   - **Remember that the skill started the server** (for cleanup in Phase 5).

4. **Read auth token** from config file:
   - Windows: `%APPDATA%\langley\config.yaml`
   - Unix: `~/.config/langley/config.yaml`
   - Field: `auth.token` (format: `langley_` + 64 hex chars)
   - This token is needed for API curl calls in Phase 2, NOT for browser.

Store these values for later phases:
- `API_ADDR` - e.g. `localhost:9091`
- `PROXY_ADDR` - e.g. `localhost:9090`
- `CA_PATH` - e.g. `%APPDATA%\langley\certs\ca.crt`
- `TOKEN` - the auth token string
- `SKILL_STARTED_SERVER` - boolean flag

## Phase 2 - Generate Test Traffic

1. **Send a request through the proxy** to create a captured flow:
   ```bash
   curl -sf -x http://{PROXY_ADDR} --cacert "{CA_PATH}" https://httpbin.org/get
   ```
   - If httpbin.org fails, fall back to: `curl -sf -x http://{PROXY_ADDR} --cacert "{CA_PATH}" https://example.com`
   - A flow is created once the proxy tunnel is established and an HTTP request is sent - even if the upstream response fails. However, if the upstream host is completely unreachable at the TCP/TLS level (e.g. DNS failure), no flow is created because the tunnel never completes.

2. **Wait 2 seconds** for async processing.

3. **Verify flow was captured** via API:
   ```bash
   curl -sf -H "Authorization: Bearer {TOKEN}" http://{API_ADDR}/api/flows?limit=5
   ```
   - Confirm at least one flow exists in the response.
   - Note the target host (e.g. `httpbin.org` or `example.com`) for browser verification.

## Phase 3 - Dashboard Browser Checks

Use Playwright MCP browser tools. The dashboard auto-authenticates via localhost origin (sets HttpOnly cookie automatically). No token input needed in the browser.

Dashboard URL: `http://{API_ADDR}`

Execute these checks in order:

| #  | Tool                      | Action / Target                                    | Verify                                            |
|----|---------------------------|---------------------------------------------------|---------------------------------------------------|
| 1  | `browser_navigate`        | `http://{API_ADDR}`                               | Page loads without error                          |
| 2  | `browser_wait_for`        | text: `"Langley"`                                 | SPA has rendered                                  |
| 3  | `browser_snapshot`        |                                                   | Nav buttons present (Flows, Analytics, Tasks, Tools, Anomalies, Settings). No error banner. |
| 4  | `browser_take_screenshot` | filename: `e2e-dashboard.png`                     | Visual baseline of dashboard                      |
| 5  | `browser_wait_for`        | text: target host from Phase 2 (e.g. `"httpbin"`) | Flows list populated with traffic                 |
| 6  | `browser_snapshot`        |                                                   | Flow items visible in the list                    |
| 7  | `browser_click`           | First flow item in the list                       | Detail panel opens                                |
| 8  | `browser_wait_for`        | text: `"Request"` or `"Response"`                 | Detail content rendered                           |
| 9  | `browser_snapshot`        |                                                   | URL, host, status code visible in detail          |
| 10 | `browser_take_screenshot` | filename: `e2e-flow-detail.png`                   | Visual record of flow detail                      |
| 11 | `browser_click`           | "Analytics" nav button                            | Switches to Analytics view                        |
| 12 | `browser_wait_for`        | text: `"Total Flows"`                             | Analytics stats loaded                            |
| 13 | `browser_snapshot`        |                                                   | Stat cards present (Total Flows, etc.)            |
| 14 | `browser_take_screenshot` | filename: `e2e-analytics.png`                     | Visual record of analytics                        |
| 15 | `browser_click`           | "Tasks" nav button                                | Switches to Tasks view                            |
| 16 | `browser_snapshot`        |                                                   | Table headers present or empty state              |
| 17 | `browser_click`           | "Tools" nav button                                | Switches to Tools view                            |
| 18 | `browser_snapshot`        |                                                   | Table headers present or empty state              |
| 19 | `browser_click`           | "Anomalies" nav button                            | Switches to Anomalies view                        |
| 20 | `browser_snapshot`        |                                                   | Anomaly items or empty state message              |
| 21 | `browser_click`           | "Settings" nav button                             | Switches to Settings view                         |
| 22 | `browser_snapshot`        |                                                   | Settings form present                             |
| 23 | `browser_click`           | "Flows" nav button                                | Returns to Flows view                             |
| 24 | `browser_snapshot`        |                                                   | "Connected" visible in WebSocket status area      |

**Handling failures**: If any step fails (element not found, timeout, unexpected content), record it as FAIL and continue with the remaining checks. Do not abort the entire test.

## Phase 4 - Report

Print a checklist summarizing all results:

```
## E2E Smoke Test Results

### Phase 1 - Server
- [x] State file read
- [x] Health check passed
- [ ] Server started by skill (was already running)

### Phase 2 - Traffic
- [x] Proxy request sent (httpbin.org)
- [x] Flow captured (verified via API)

### Phase 3 - Dashboard (24 checks)
- [x] 1. Dashboard loads
- [x] 2. SPA rendered ("Langley" visible)
- [x] 3. Nav buttons present
- [x] 4. Screenshot: e2e-dashboard.png
...
- [x] 24. WebSocket "Connected" status

### Summary
Passed: 22/24
Failed: 2/24
Screenshots: e2e-dashboard.png, e2e-flow-detail.png, e2e-analytics.png
```

## Phase 5 - Cleanup

**Only if `SKILL_STARTED_SERVER` is true**: Ask the user whether they want to stop the server.

Use AskUserQuestion:
- Question: "The E2E smoke test started the Langley server. Stop it now?"
- Options: "Yes, stop it" / "No, keep it running"

If yes, kill the server process using the PID from state.json - but **first verify the PID belongs to a `langley` process** (e.g. `tasklist /FI "PID eq {pid}"` on Windows, `ps -p {pid} -o comm=` on Unix). If the PID belongs to a different process, warn the user and skip the kill - the state file is stale.

If the skill did NOT start the server, skip this phase entirely.

## Technical Reference

- **Auth**: Dashboard auto-authenticates from localhost origin via `langley_session` HttpOnly cookie. API calls need `Authorization: Bearer {token}`.
- **State struct**: `{ proxy_addr, api_addr, ca_path, pid, started_at }`
- **Config path**: Same directory as state.json, file `config.yaml`, field `auth.token`.
- **Screenshots**: Saved by Playwright MCP to its configured output directory.
- **WebSocket**: `ws://{api_addr}/ws` - dashboard connects automatically for live flow updates.
- **Rate limit**: API has 20 req/s sustained, 100 burst - not a concern for this test.

