---
name: e2e-confirm
description: >
  Behavioral E2E tests that confirm Langley features work correctly.
  Sends known traffic through the proxy and asserts on exact data in both the API and dashboard UI.
invoke: user
---

# E2E Feature Confirmation

Scenario-driven behavioral tests that confirm Langley features work correctly. Each scenario sends known data through the proxy and asserts on exact values in the API and dashboard UI.

**How this differs from `e2e-smoke`**: The smoke test checks *rendering* (did the page load?). This skill checks *behavior* (did the correct data appear after I sent known traffic?).

## Phase 0 - Server Bootstrap & Baseline

Identical to e2e-smoke Phase 1. Reuse the same steps:

1. **Read state file** to discover addresses:
   - Windows: `%APPDATA%\langley\state.json`
   - Unix: `~/.config/langley/state.json`
   - PowerShell: `%APPDATA%` is `$env:APPDATA`
   - Parse JSON -> `{ proxy_addr, api_addr, ca_path, pid, started_at }`

2. **Health check** the API:
   - `curl -sf http://{api_addr}/api/health`
   - Health endpoint is unauthenticated.

3. **If not running**, build and start:
   ```bash
   # Windows (PowerShell)
   go build -o langley.exe ./cmd/langley/; Start-Process -FilePath .\langley.exe

   # Unix
   go build -o langley ./cmd/langley/ && ./langley &
   ```
   - Wait up to 10 seconds, polling `/api/health` every 1s.
   - Re-read `state.json` after startup to get actual addresses.
   - **Remember that the skill started the server** (for cleanup in Phase 3).

4. **Read auth token** from config file:
   - Windows: `%APPDATA%\langley\config.yaml`
   - Unix: `~/.config/langley/config.yaml`
   - Field: `auth.token` (format: `langley_` + 64 hex chars)

5. **Generate a run ID** for tagging test traffic:
   - `RUN_ID` = short unique string (e.g. first 8 chars of a UUID, or `e2e-{unix_timestamp}`).
   - All test traffic will include `?_e2e={RUN_ID}` as a query parameter. **Note**: the Langley API does not support filtering by this param. It exists in the stored `path` field for post-hoc human inspection only. Assertions use delta-based counting and host filtering — not RUN_ID filtering.

6. **Capture baseline counts** before sending any traffic:
   ```bash
   curl -sf -H "Authorization: Bearer {TOKEN}" "http://{API_ADDR}/api/stats"
   ```
   - Record `BASELINE_FLOW_COUNT` = `total_flows` value.
   - Record `BASELINE_TASK_COUNT` = `total_tasks` value.

## Phase 0.5 - DOM Discovery

Before running any assertions, discover the dashboard's actual DOM structure. This separates "figure out the markup" from "verify the data."

1. `browser_navigate` to `http://{API_ADDR}`.
2. `browser_wait_for` any content (timeout 10s).
3. `browser_snapshot` to capture the full accessibility tree.
4. From the snapshot, identify and record selectors for:
   - **Flow rows**: the repeating element for each flow in the list (e.g. `tr`, `[data-flow-row]`, `.flow-row`). Record as `SEL_FLOW_ROW`.
   - **Flow host text**: where the host name appears within a flow row. Record as `SEL_FLOW_HOST`.
   - **Detail panel host/method/status**: after clicking a flow, where these values appear. Record as `SEL_DETAIL_HOST`, `SEL_DETAIL_METHOD`, `SEL_DETAIL_STATUS`.
   - **Task rows**: the repeating element for each task in the Tasks view. Record as `SEL_TASK_ROW`.
   - **Stat cards**: the elements showing analytics numbers. Record as `SEL_STAT_CARD`.
   - **Nav buttons**: the navigation elements for Flows, Tasks, Analytics, Settings. Record as `SEL_NAV_{VIEW}`.
   - **Theme toggle**: the button that switches dark/light theme. Record as `SEL_THEME_TOGGLE`.
   - **Primary button**: `.primary-btn` (e.g. Save in Settings). Record as `SEL_PRIMARY_BTN`.
   - **Secondary button**: `.secondary-btn` (e.g. Reset in Settings). Record as `SEL_SECONDARY_BTN`.
   - **Badge**: `.badge` element (visible when SSE or status badges are present). Record as `SEL_BADGE`.
   - **Error banner**: `.error-banner` (may not be visible if no errors). Record as `SEL_ERROR_BANNER`.

5. If the Flows view has no data yet (empty state), the selectors for flow rows may not be discoverable. In that case, proceed to Scenario 1 and discover flow-related selectors after the first traffic is generated (re-snapshot after polling confirms the flow appeared).

6. For any selector that cannot be discovered, record `null`. Assertions using that selector will FAIL with reason "selector not discovered in DOM discovery" rather than guessing.

All scenarios below use the discovered `SEL_*` variables instead of hardcoded selectors.

Store these values for all scenarios:
- `API_ADDR` - e.g. `localhost:9091`
- `PROXY_ADDR` - e.g. `localhost:9090`
- `CA_PATH` - e.g. `%APPDATA%\langley\certs\ca.crt`
- `TOKEN` - the auth token string
- `SKILL_STARTED_SERVER` - boolean flag
- `RUN_ID` - unique tag for this run (human inspection only — not used for API filtering)
- `BASELINE_FLOW_COUNT` - flow count before test traffic
- `BASELINE_TASK_COUNT` - task count before test traffic
- `SEL_*` - discovered DOM selectors from Phase 0.5

## Phase 1 - Scenarios

Execute scenarios **in order**. Scenarios 1-3 generate traffic. Scenarios 4 and 7 reuse that traffic. Scenario 9 inspects computed styles against `docs/style-guide.md`. Track per-assertion results as PASS/FAIL with reason.

**Grading rules**:
- **Grade strictly.** The purpose of this skill is to find flaws, not to produce a passing report. A green report with rationalized results is worthless.
- **Never explain away a failure.** If an assertion fails, it fails. Do not reinterpret the assertion to make it pass. Do not add caveats like "this is expected behavior" or "correct for incomplete flows." Record FAIL with the exact observed vs. expected values and move on.
- **The report is a diagnostic tool.** Failures are the valuable output — they surface API inconsistencies, missing fields, data contract violations, and UI bugs. A report with zero failures should be treated with suspicion.
- If an assertion fails, record it as FAIL with observed and expected values, then continue to the next assertion. Never abort a scenario or skip remaining scenarios.
- If a *setup* step fails (e.g. proxy request doesn't complete), mark all assertions in that scenario as FAIL with reason "Setup failed: {detail}" and move to the next scenario.
- **Record observed values for every assertion**, not just failures. This provides an audit trail. A report that says `[x] host == httpbin.org` with no observed value cannot be distinguished from a sycophantic pass. Write `[x] host == httpbin.org (observed: httpbin.org)`.

**Polling pattern** (replaces all fixed waits):
- Where the old skill said "wait N seconds," instead poll the relevant endpoint every 500ms, with a timeout of 10 seconds.
- Example: after sending proxy traffic, poll `GET /api/flows?host={TARGET_HOST}&limit=1` every 500ms until a matching flow appears or 10s elapse. If timeout, mark setup as failed.

**UI extraction pattern** (replaces `browser_snapshot` + "verify"):
- Use `browser_evaluate` with the selectors discovered in Phase 0.5 to programmatically extract DOM text, then compare the returned string to the expected value. This converts subjective LLM judgment into a binary string comparison.
- Example: instead of "snapshot and verify the host is visible," do:
  ```
  browser_evaluate: () => document.querySelector('{SEL_FLOW_HOST}')?.textContent?.trim()
  ```
  Then compare the returned string to `{TARGET_HOST}`.
- **If `browser_evaluate` returns null, that is a FAIL** with reason "selector returned null — DOM structure may have changed since discovery." Do NOT fall through to regex on `document.body.innerText`. A null return means the DOM doesn't match expectations, which is a real finding.
- **Extract first, compare second.** Never combine extraction and judgment. The agent must commit to an observed value before comparing it to expected.

**Role of `browser_wait_for` vs `browser_evaluate`**:
- `browser_wait_for` is a **gate** — it waits until the page is ready (text appears, content loads). It does NOT count as an assertion.
- `browser_evaluate` is the **assertion** — it extracts a value and the agent compares it to expected.
- Pattern: `browser_wait_for` (gate) → `browser_evaluate` (extract) → compare (assert). Never use `browser_wait_for` as the assertion itself.

---

### Scenario 1: Flow Capture Pipeline

**Tests**: Proxy captures traffic and stores it with correct metadata.

**Setup**:
```bash
curl -sf -x http://{PROXY_ADDR} --cacert "{CA_PATH}" "https://httpbin.org/get?_e2e={RUN_ID}"
```
- If httpbin.org fails, fall back to `https://example.com?_e2e={RUN_ID}` and adjust expected values below accordingly.
- Record which host was actually used as `TARGET_HOST` (e.g. `httpbin.org` or `example.com`).
- **Poll** `GET /api/flows?host={TARGET_HOST}&limit=1` every 500ms, timeout 10s, until a flow appears.

**API assertions**:
```bash
curl -sf -H "Authorization: Bearer {TOKEN}" "http://{API_ADDR}/api/flows?limit=5&host={TARGET_HOST}"
```
1. Response contains at least one flow.
2. First matching flow has `host` == `{TARGET_HOST}`.
3. First matching flow has `method` == `GET`.
4. First matching flow has `status_code` == `200`.
5. First matching flow has `path` that starts with `/get` (httpbin) or `/` (example.com).

**UI assertions**:
- `browser_navigate` to `http://{API_ADDR}`.
- `browser_wait_for` text: `{TARGET_HOST}` (timeout 10s).
- Extract and verify using `browser_evaluate` with discovered selectors:
6. Flow list contains `{TARGET_HOST}`:
   ```
   browser_evaluate: () => {
     const rows = document.querySelectorAll('{SEL_FLOW_ROW}');
     const texts = Array.from(rows).map(r => r.textContent);
     return texts.find(t => t.includes('{TARGET_HOST}')) || null;
   }
   ```
   PASS if returned string contains `{TARGET_HOST}`. FAIL if null (reason: "selector returned null"). Record the full returned text.
- Click the flow row containing `{TARGET_HOST}` (use `browser_snapshot` to find the clickable element ref, then `browser_click`).
- `browser_wait_for` text: `"Request"` or `"Response"` (gate — wait for detail panel to load, timeout 10s).
- Extract detail values:
7. Detail panel host:
   ```
   browser_evaluate: () => document.querySelector('{SEL_DETAIL_HOST}')?.textContent?.trim() || null
   ```
   PASS if returned string == `{TARGET_HOST}`. FAIL if null (reason: "selector returned null").
8. Detail panel method:
   ```
   browser_evaluate: () => document.querySelector('{SEL_DETAIL_METHOD}')?.textContent?.trim() || null
   ```
   PASS if returned string == `GET`. FAIL if null (reason: "selector returned null").
9. Detail panel status:
   ```
   browser_evaluate: () => document.querySelector('{SEL_DETAIL_STATUS}')?.textContent?.trim() || null
   ```
   PASS if returned string == `200`. FAIL if null (reason: "selector returned null").

Record the flow ID from the API response as `FLOW_1_ID` for potential later use.

---

### Scenario 2: Multi-Host Filtering

**Tests**: Flows from different hosts are captured and host filtering works.

**Setup**:
Send a request to a *different* host than Scenario 1:
```bash
# If Scenario 1 used httpbin.org, send to example.com (or vice versa)
curl -sf -x http://{PROXY_ADDR} --cacert "{CA_PATH}" "https://{SECOND_HOST}?_e2e={RUN_ID}"
```
- `SECOND_HOST` = if `TARGET_HOST` is `httpbin.org`, use `example.com`. Otherwise use `httpbin.org/get`.
- **Poll** `GET /api/flows?host={SECOND_HOST}&limit=1` every 500ms, timeout 10s.

**API assertions**:
```bash
curl -sf -H "Authorization: Bearer {TOKEN}" "http://{API_ADDR}/api/flows?host={TARGET_HOST}"
curl -sf -H "Authorization: Bearer {TOKEN}" "http://{API_ADDR}/api/flows?host={SECOND_HOST}"
```
1. Flows exist for `{TARGET_HOST}`.
2. Flows exist for `{SECOND_HOST}`.

**UI assertions**:
- Navigate to `http://{API_ADDR}` (Flows view).
- `browser_wait_for` text: `{SECOND_HOST}` (timeout 10s).
- Extract and verify:
3. Both hosts visible in flow rows:
   ```
   browser_evaluate: () => {
     const rows = document.querySelectorAll('{SEL_FLOW_ROW}');
     const texts = Array.from(rows).map(r => r.textContent);
     return JSON.stringify({
       hasFirst: texts.some(t => t.includes('{TARGET_HOST}')),
       hasSecond: texts.some(t => t.includes('{SECOND_HOST}')),
       rowCount: rows.length
     });
   }
   ```
   PASS if both are `true`. FAIL if null (reason: "selector returned null"). Record all values.
- If a host filter control exists in the UI (search box, filter dropdown, etc.), use it to filter by `{TARGET_HOST}`:
  - Extract and verify:
4. Only matching host shown after filter:
   ```
   browser_evaluate: () => {
     const rows = document.querySelectorAll('{SEL_FLOW_ROW}');
     const texts = Array.from(rows).map(r => r.textContent);
     return JSON.stringify({
       hasTarget: texts.some(t => t.includes('{TARGET_HOST}')),
       hasSecond: texts.some(t => t.includes('{SECOND_HOST}')),
       rowCount: rows.length
     });
   }
   ```
   PASS if `hasTarget` is `true` AND `hasSecond` is `false`. Record all values.
  - Clear the filter.
- If no host filter control exists in the UI, mark assertion 4 as SKIP with reason "No host filter control found in UI".

---

### Scenario 3: Task Grouping

**Tests**: Requests to the same host within the idle gap are grouped into a single task.

**Setup**:
Send 3 rapid requests to the same host (within idle gap window). Always use httpbin.org for this scenario — `example.com` may serve identical responses from cache, preventing the proxy from recording 3 distinct flows:
```bash
curl -sf -x http://{PROXY_ADDR} --cacert "{CA_PATH}" "https://httpbin.org/get?n=1&_e2e={RUN_ID}"
curl -sf -x http://{PROXY_ADDR} --cacert "{CA_PATH}" "https://httpbin.org/get?n=2&_e2e={RUN_ID}"
curl -sf -x http://{PROXY_ADDR} --cacert "{CA_PATH}" "https://httpbin.org/get?n=3&_e2e={RUN_ID}"
```
- If httpbin.org is unreachable (failed in Scenario 1), use `example.com`. If only 1 flow is captured instead of 3 (due to caching), that is a FAIL with reason "only {N} flows captured, expected 3 — example.com may have served cached responses." Do not weaken the assertion.
- **Poll** `GET /api/flows?host=httpbin.org&limit=10` every 500ms, timeout 10s, until at least 3 new flows appear (compare to count at end of Scenario 1).

**API assertions**:
```bash
curl -sf -H "Authorization: Bearer {TOKEN}" "http://{API_ADDR}/api/flows?host=httpbin.org&limit=10"
```
1. Flow count for `httpbin.org` increased by at least 3 compared to after Scenario 1 (delta-based — not "at least 3 total").
2. The 3 most recent flows share the same `task_id` value (task grouping worked).

```bash
curl -sf -H "Authorization: Bearer {TOKEN}" "http://{API_ADDR}/api/analytics/tasks?limit=10"
```
3. A task exists whose `flow_count` >= 3.

Record the task ID from assertion 3 as `TASK_ID_WITH_3` and its exact `flow_count` as `TASK_FLOW_COUNT`.

**UI assertions**:
- Click "Tasks" nav button.
- `browser_wait_for` text that matches `{TASK_ID_WITH_3}` (gate — timeout 10s).
- Extract and verify:
4. Tasks view has rows:
   ```
   browser_evaluate: () => {
     const rows = document.querySelectorAll('{SEL_TASK_ROW}');
     return rows.length;
   }
   ```
   PASS if returned number >= 1. FAIL if null (reason: "selector returned null").
5. The row containing the known task ID includes the expected flow count:
   ```
   browser_evaluate: () => {
     const rows = document.querySelectorAll('{SEL_TASK_ROW}');
     for (const row of rows) {
       if (row.textContent.includes('{TASK_ID_WITH_3}')) {
         return row.textContent.substring(0, 200);
       }
     }
     return null;
   }
   ```
   PASS if the returned text contains `{TASK_FLOW_COUNT}` (the exact number from the API). This is a string-contains check against a known value — not a judgment call. FAIL if null (reason: "task ID not found in UI"). Record the full row text.

---

### Scenario 4: Analytics Accuracy

**Tests**: Stats endpoint returns correct aggregated numbers matching actual traffic.

**Setup**: No new traffic. Uses flows from Scenarios 1-3.

**API assertions**:
```bash
curl -sf -H "Authorization: Bearer {TOKEN}" "http://{API_ADDR}/api/stats"
```
1. `total_flows` >= `BASELINE_FLOW_COUNT` + 5 (delta-based: 1 from S1 + 1 from S2 + 3 from S3, at minimum). Record both `total_flows` and `BASELINE_FLOW_COUNT`.
2. `total_tasks` >= `BASELINE_TASK_COUNT` + 1 (delta-based). Record both values.

```bash
curl -sf -H "Authorization: Bearer {TOKEN}" "http://{API_ADDR}/api/flows/count"
```
3. Flow count from `/api/flows/count` **equals** `total_flows` from `/api/stats`. If they differ, FAIL — report both values and note the discrepancy. Do not rationalize different time windows or scoping as "consistent enough."

**UI assertions**:
- Click "Analytics" nav button.
- `browser_wait_for` text: `"Total Flows"` (timeout 10s).
- Extract and verify:
4. Total Flows stat value:
   ```
   browser_evaluate: () => {
     const cards = document.querySelectorAll('{SEL_STAT_CARD}');
     for (const card of cards) {
       if (card.textContent.includes('Total Flows')) {
         const nums = card.textContent.match(/\d[\d,]*/g);
         return { label: 'Total Flows', value: nums ? nums[nums.length - 1] : null };
       }
     }
     return null;
   }
   ```
   PASS if extracted number >= `BASELINE_FLOW_COUNT` + 5. FAIL if null (reason: "selector returned null — stat card not found"). Record extracted value and baseline.

---

### Scenario 5: Real-Time Updates

**Tests**: New flows appear in the UI without a manual page refresh (via WebSocket).

**Setup**:
- Ensure browser is on the Flows view: click "Flows" nav button.
- `browser_wait_for` text: `{TARGET_HOST}` (timeout 10s) — ensures the page is fully loaded before capturing baseline.
- Capture baseline row count using `browser_evaluate`:
  ```
  browser_evaluate: () => {
    const rows = document.querySelectorAll('{SEL_FLOW_ROW}');
    return { count: rows.length, firstRowText: rows[0]?.textContent?.substring(0, 80) || null };
  }
  ```
  Record as `BEFORE_COUNT` and `BEFORE_FIRST_ROW`. FAIL if null (reason: "selector returned null").

**Action**:
Send a new request through the proxy with a distinguishing tag:
```bash
curl -sf -x http://{PROXY_ADDR} --cacert "{CA_PATH}" "https://httpbin.org/get?realtime={RUN_ID}&_e2e={RUN_ID}"
```
- If httpbin.org is unavailable, use `https://example.com?_e2e={RUN_ID}`.

**UI assertions**:
- **Poll** using `browser_evaluate` every 1s, timeout 10s:
1. New flow appeared:
   ```
   browser_evaluate: () => {
     const rows = document.querySelectorAll('{SEL_FLOW_ROW}');
     return { count: rows.length, firstRowText: rows[0]?.textContent?.substring(0, 80) || null };
   }
   ```
   PASS if `count` > `BEFORE_COUNT` OR `firstRowText` differs from `BEFORE_FIRST_ROW`. Record both before and after values.
2. WebSocket delivered the new flow's content. Use `browser_wait_for` as a gate (wait for `"realtime"` text if httpbin, timeout 10s), then **assert** with `browser_evaluate`:
   ```
   browser_evaluate: () => {
     const rows = document.querySelectorAll('{SEL_FLOW_ROW}');
     const texts = Array.from(rows).map(r => r.textContent);
     return texts.find(t => t.includes('realtime')) || null;
   }
   ```
   PASS if returned string contains `"realtime"` (httpbin) or if assertion 1 already confirmed a new row (example.com). FAIL if null after the gate timed out. Record the extracted text.

**API assertion** (supplementary):
```bash
curl -sf -H "Authorization: Bearer {TOKEN}" "http://{API_ADDR}/api/flows?limit=1"
```
3. Most recent flow has a path containing `realtime` (httpbin) or host == `example.com`. Record the actual flow returned.

---

### Scenario 6: Settings Round-Trip

**Tests**: Settings can be read, updated, and the change persists.

**Setup**:
Read current settings:
```bash
curl -sf -H "Authorization: Bearer {TOKEN}" "http://{API_ADDR}/api/settings"
```
- Record the current `idle_gap_minutes` value as `ORIGINAL_VALUE`.

**Action**:
Pick a new value different from `ORIGINAL_VALUE` (e.g. if original is 5, use 10; if original is 10, use 5):
```bash
curl -sf -X PUT -H "Authorization: Bearer {TOKEN}" -H "Content-Type: application/json" \
  -d '{"idle_gap_minutes": {NEW_VALUE}}' \
  "http://{API_ADDR}/api/settings"
```

**API assertions**:
```bash
curl -sf -H "Authorization: Bearer {TOKEN}" "http://{API_ADDR}/api/settings"
```
1. `idle_gap_minutes` now equals `{NEW_VALUE}`.

**UI assertions**:
- Click "Settings" nav button.
- `browser_wait_for` text: `{NEW_VALUE}` (timeout 10s).
- Extract and verify:
2. Settings view shows the updated value:
   ```
   browser_evaluate: () => {
     const text = document.body.innerText;
     const match = text.match(/idle.?gap.?minutes[:\s]*(\d+)/i);
     return match ? match[1] : null;
   }
   ```
   PASS if returned string == `{NEW_VALUE}` (as string). Record the extracted value.

**Cleanup** (mandatory - restore original value):
```bash
curl -sf -X PUT -H "Authorization: Bearer {TOKEN}" -H "Content-Type: application/json" \
  -d '{"idle_gap_minutes": {ORIGINAL_VALUE}}' \
  "http://{API_ADDR}/api/settings"
```
3. After restore, `GET /api/settings` returns `idle_gap_minutes` == `{ORIGINAL_VALUE}`. Record the observed value.

---

### Scenario 7: Export Integrity

**Tests**: Export endpoint returns correct data in the expected format.

**Setup**: No new traffic. Uses existing flows.

**API assertions**:
```bash
curl -sf -H "Authorization: Bearer {TOKEN}" "http://{API_ADDR}/api/flows/export?format=json"
```
1. Response is valid JSON.
2. Response contains a `flows` array (or top-level array) with at least one entry.
3. **Every** flow entry has all of these fields: `id`, `host`, `method`, `path`, `status_code`. If any flow is missing any field, FAIL — report which flow index and which field. Do not excuse missing fields for incomplete or pending flows; the export contract should be consistent.
4. At least one flow has `host` == `{TARGET_HOST}` (from our test traffic).

Check the row count header or metadata:
5. Look for `row_count` in the response JSON metadata, OR an `X-Export-Row-Count` response header. If found: PASS if the value is > 0 and matches the array length. If neither metadata field nor header exists: SKIP with reason "Export endpoint does not return row count metadata." Do not FAIL for a missing optional contract — the data integrity assertions above are the primary checks.

---

### Scenario 8: Negative Cases

**Tests**: The API rejects invalid requests with appropriate error responses.

**API assertions**:

1. Invalid auth token returns 401:
   ```bash
   curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer invalid_token_abc123" \
     "http://{API_ADDR}/api/flows?limit=1"
   ```
   PASS if HTTP status == `401`. Record the actual status code.

2. No auth header returns 401:
   ```bash
   curl -s -o /dev/null -w "%{http_code}" "http://{API_ADDR}/api/flows?limit=1"
   ```
   PASS if HTTP status == `401`. Record the actual status code.

3. Malformed settings update is rejected:
   ```bash
   curl -s -o /dev/null -w "%{http_code}" -X PUT \
     -H "Authorization: Bearer {TOKEN}" -H "Content-Type: application/json" \
     -d '{"idle_gap_minutes": -5}' \
     "http://{API_ADDR}/api/settings"
   ```
   PASS if HTTP status is `400` or `422` (any 4xx client error). FAIL if `200`. Record the actual status code and response body.

4. Out-of-range settings value is rejected:
   ```bash
   curl -s -o /dev/null -w "%{http_code}" -X PUT \
     -H "Authorization: Bearer {TOKEN}" -H "Content-Type: application/json" \
     -d '{"idle_gap_minutes": 9999}' \
     "http://{API_ADDR}/api/settings"
   ```
   PASS if HTTP status is `400` or `422` (any 4xx client error). FAIL if `200`. Record the actual status code and response body.

---

### Scenario 9: Style Guide Conformance

**Tests**: Computed styles match claims in `docs/style-guide.md`. No new traffic needed — uses the dashboard as rendered from prior scenarios.

**Source of truth**: `docs/style-guide.md`. Read it before running assertions to confirm expected values.

**Setup**:
- Ensure browser is on the Flows view (click "Flows" nav if needed).
- `browser_wait_for` any flow content (timeout 10s).

**Contrast helper** (reuse in all contrast assertions):
```javascript
function contrastRatio(rgb1, rgb2) {
  function luminance(r, g, b) {
    const [rs, gs, bs] = [r, g, b].map(c => {
      c = c / 255;
      return c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
    });
    return 0.2126 * rs + 0.7152 * gs + 0.0722 * bs;
  }
  function parseRgb(s) {
    const m = s.match(/(\d+)/g);
    return m ? m.map(Number) : null;
  }
  const c1 = parseRgb(rgb1), c2 = parseRgb(rgb2);
  if (!c1 || !c2) return null;
  const l1 = luminance(...c1), l2 = luminance(...c2);
  const lighter = Math.max(l1, l2), darker = Math.min(l1, l2);
  return (lighter + 0.05) / (darker + 0.05);
}
```

#### 9a: Token presence

1. Dark theme tokens defined:
   ```
   browser_evaluate: () => {
     const root = getComputedStyle(document.documentElement);
     return JSON.stringify({
       sse: root.getPropertyValue('--sse').trim(),
       errorBg: root.getPropertyValue('--error-bg').trim(),
       accent: root.getPropertyValue('--accent').trim(),
       textMuted: root.getPropertyValue('--text-muted').trim()
     });
   }
   ```
   PASS if `--sse`, `--error-bg`, `--accent`, `--text-muted` are all non-empty strings. Record all values.

2. Switch to light theme (click `SEL_THEME_TOGGLE`), then check:
   ```
   browser_evaluate: () => {
     const root = getComputedStyle(document.documentElement);
     return JSON.stringify({
       sse: root.getPropertyValue('--sse').trim(),
       errorBg: root.getPropertyValue('--error-bg').trim(),
       accent: root.getPropertyValue('--accent').trim(),
       textMuted: root.getPropertyValue('--text-muted').trim()
     });
   }
   ```
   PASS if all tokens are non-empty. FAIL if any are empty (reason: "token not defined in light theme"). Record all values. Switch back to dark theme before continuing.

#### 9b: Contrast ratios

3. Dark theme `--text-muted` contrast against `--bg-primary`:
   ```
   browser_evaluate: () => {
     // (inline the contrastRatio helper above)
     const root = getComputedStyle(document.documentElement);
     const muted = root.getPropertyValue('color');  // won't work — need an element
     const el = document.querySelector('.path') || document.querySelector('.timestamp');
     if (!el) return null;
     const style = getComputedStyle(el);
     const color = style.color;
     const bgEl = document.querySelector('.app') || document.body;
     const bg = getComputedStyle(bgEl).backgroundColor;
     // ... compute ratio using contrastRatio(color, bg)
     return JSON.stringify({ color, bg, ratio: ratio.toFixed(2) });
   }
   ```
   PASS if `ratio` >= 4.5. FAIL if < 4.5 (report observed ratio, color, and bg values). Use an element styled with `color: var(--text-muted)` — `.path`, `.timestamp`, or `.bar-label` are good candidates. For the background, use the nearest ancestor's `backgroundColor` (walk up if transparent).

   Full self-contained evaluation:
   ```
   browser_evaluate: () => {
     function lum(r,g,b){return[r,g,b].map(c=>{c/=255;return c<=0.04045?c/12.92:Math.pow((c+0.055)/1.055,2.4)}).reduce((a,v,i)=>a+[0.2126,0.7152,0.0722][i]*v,0)}
     function parseRgb(s){const m=s.match(/[\d.]+/g);return m?m.slice(0,3).map(Number):null}
     function ratio(c1,c2){const a=parseRgb(c1),b=parseRgb(c2);if(!a||!b)return null;const l1=lum(...a),l2=lum(...b);return(Math.max(l1,l2)+0.05)/(Math.min(l1,l2)+0.05)}
     function resolvedBg(el){while(el){const bg=getComputedStyle(el).backgroundColor;const p=parseRgb(bg);if(p&&(p.length<4||p[3]>0)&&!(p[0]===0&&p[1]===0&&p[2]===0&&p.length>=4&&p[3]===0))return bg;el=el.parentElement}return'rgb(0,0,0)'}
     const el=document.querySelector('.path')||document.querySelector('.timestamp')||document.querySelector('.bar-label');
     if(!el)return JSON.stringify({error:'no --text-muted element found'});
     const color=getComputedStyle(el).color;
     const bg=resolvedBg(el);
     const r=ratio(color,bg);
     return JSON.stringify({color,bg,ratio:r?r.toFixed(2):null,theme:'dark'});
   }
   ```
   PASS if `ratio` >= 4.5. Record all values.

4. Switch to light theme, repeat the same contrast check:
   Same `browser_evaluate` as assertion 3.
   PASS if `ratio` >= 4.5. Record all values including `theme: 'light'`. Switch back to dark theme.

#### 9c: Focus-visible outlines

5. Tab through interactive elements and verify outlines appear:
   ```
   // First, click the body to reset focus
   browser_click on document body
   // Then press Tab multiple times, checking each focused element
   ```
   For each Tab press (do 6 presses to cover nav buttons, theme toggle, and filter input):
   ```
   browser_press_key: Tab
   browser_evaluate: () => {
     const el = document.activeElement;
     if (!el || el === document.body) return null;
     const s = getComputedStyle(el);
     return JSON.stringify({
       tag: el.tagName,
       class: el.className,
       outlineStyle: s.outlineStyle,
       outlineWidth: s.outlineWidth,
       outlineColor: s.outlineColor
     });
   }
   ```
   Collect results for all 6 Tab stops. PASS if **at least 4 of 6** focused elements have `outlineStyle` != `'none'` and `outlineWidth` != `'0px'`. Record each element's tag, class, and outline values. FAIL if fewer than 4 show outlines (reason: "only N/6 elements showed :focus-visible outline").

   **Note**: Some elements may not show outline on `:focus` (only on `:focus-visible`). The keyboard Tab interaction triggers `:focus-visible` in all browsers, so this is the correct test.

#### 9d: Interactive element sizing

6. Buttons and inputs meet minimum 32px size:
   ```
   browser_evaluate: () => {
     const selectors = ['button', 'input', 'select'];
     const results = [];
     for (const sel of selectors) {
       document.querySelectorAll(sel).forEach(el => {
         if (el.offsetWidth === 0 && el.offsetHeight === 0) return; // hidden
         const rect = el.getBoundingClientRect();
         const minDim = Math.min(rect.width, rect.height);
         if (minDim < 32) {
           results.push({
             tag: el.tagName,
             class: el.className.substring(0, 50),
             width: Math.round(rect.width),
             height: Math.round(rect.height),
             text: el.textContent?.substring(0, 20) || el.type
           });
         }
       });
     }
     return JSON.stringify({ undersized: results, count: results.length });
   }
   ```
   PASS if `count` == 0. FAIL if any elements are under 32px — report each undersized element. Record the full list.

#### 9e: Border radius is 0 (except known exceptions)

7. Standard elements have no border-radius:
   ```
   browser_evaluate: () => {
     const exceptions = ['.status-dot', '.bar'];
     const checkSelectors = ['.flow-item', '.badge', '.stat-card', '.chart-section',
       '.settings-section', '.data-table', '.method', '.anomaly-item', 'kbd',
       '.help-modal', '.body-content', '.detail-section'];
     const violations = [];
     for (const sel of checkSelectors) {
       const el = document.querySelector(sel);
       if (!el) continue;
       const br = getComputedStyle(el).borderRadius;
       if (br !== '0px' && br !== '0') {
         violations.push({ selector: sel, borderRadius: br });
       }
     }
     return JSON.stringify({ violations, count: violations.length });
   }
   ```
   PASS if `count` == 0. FAIL if any element has non-zero border-radius — report each violation. Record the full list.

8. Known exceptions DO have border-radius:
   ```
   browser_evaluate: () => {
     const dot = document.querySelector('.status-dot');
     const bar = document.querySelector('.bar');
     return JSON.stringify({
       statusDot: dot ? getComputedStyle(dot).borderRadius : 'not found',
       bar: bar ? getComputedStyle(bar).borderRadius : 'not found'
     });
   }
   ```
   PASS if `.status-dot` has `borderRadius` == `'50%'` (or equivalent px value) and `.bar` has non-zero top-corner radius. If element is not found, SKIP with reason. Record observed values.

#### 9f: Disabled button states

9. Navigate to Settings view (click Settings nav). Check disabled button states:
   ```
   browser_evaluate: () => {
     // Primary btn with :disabled
     const primary = document.querySelector('.primary-btn:disabled');
     const secondary = document.querySelector('.secondary-btn:disabled');
     const results = {};
     if (primary) {
       const s = getComputedStyle(primary);
       results.primary = { opacity: s.opacity, cursor: s.cursor };
     } else {
       results.primary = 'no disabled .primary-btn found';
     }
     if (secondary) {
       const s = getComputedStyle(secondary);
       results.secondary = { opacity: s.opacity, cursor: s.cursor };
     } else {
       results.secondary = 'no disabled .secondary-btn found';
     }
     return JSON.stringify(results);
   }
   ```
   For each found disabled button: PASS if `opacity` < 1 and `cursor` == `'not-allowed'`. If no disabled button is in the DOM, SKIP with reason "no disabled button rendered in current state." Record all values.

#### 9g: Reduced-motion media query

10. The `prefers-reduced-motion` rule exists in stylesheets:
    ```
    browser_evaluate: () => {
      for (const sheet of document.styleSheets) {
        try {
          for (const rule of sheet.cssRules) {
            if (rule instanceof CSSMediaRule && rule.conditionText &&
                rule.conditionText.includes('prefers-reduced-motion')) {
              return JSON.stringify({
                found: true,
                conditionText: rule.conditionText,
                ruleCount: rule.cssRules.length
              });
            }
          }
        } catch (e) { /* cross-origin sheet, skip */ }
      }
      return JSON.stringify({ found: false });
    }
    ```
    PASS if `found` == `true`. FAIL if no `prefers-reduced-motion` media rule exists. Record the condition text and inner rule count.

#### 9h: Badge uses token (not hardcoded color)

11. If a `.badge.sse` element exists, verify its background matches `--sse`:
    ```
    browser_evaluate: () => {
      const badge = document.querySelector('.badge.sse');
      if (!badge) return JSON.stringify({ skip: 'no .badge.sse in DOM' });
      const bg = getComputedStyle(badge).backgroundColor;
      const root = getComputedStyle(document.documentElement);
      const expected = root.getPropertyValue('--sse').trim();
      return JSON.stringify({ observedBg: bg, expectedToken: expected });
    }
    ```
    PASS if the computed `backgroundColor` corresponds to the `--sse` token value (both will be in rgb format for comparison). SKIP if no `.badge.sse` exists. Record both values.

---

## Phase 2 - Report

Print per-scenario results with individual assertion outcomes. **Every assertion MUST include the observed value**, even on PASS. Use this format:

```
## E2E Feature Confirmation Results

Run ID: {RUN_ID}
Baseline: {BASELINE_FLOW_COUNT} flows, {BASELINE_TASK_COUNT} tasks

### Scenario 1: Flow Capture Pipeline
- [x] API: Flow exists for {TARGET_HOST} (observed: 1 flow returned)
- [x] API: host == {TARGET_HOST} (observed: httpbin.org)
- [x] API: method == GET (observed: GET)
- [x] API: status_code == 200 (observed: 200)
- [x] API: path starts with /get (observed: /get)
- [x] UI: Flow visible in flows list (observed: "httpbin.org GET /get 200")
- [x] UI: Detail shows correct host (observed: httpbin.org)
- [x] UI: Detail shows correct method (observed: GET)
- [x] UI: Detail shows correct status (observed: 200)

### Scenario 2: Multi-Host Filtering
- [x] API: Flows exist for {TARGET_HOST} (observed: 1 flow)
- [x] API: Flows exist for {SECOND_HOST} (observed: 1 flow)
- [x] UI: Both hosts visible (observed: {hasFirst: true, hasSecond: true})
- [~] UI: Host filter shows only matching flows (SKIP: No filter control)

### Scenario 3: Task Grouping
- [x] API: Flow count increased by >= 3 (observed: +3, was 2, now 5)
- [x] API: Recent flows share same task_id (observed: task_abc123)
- [x] API: Task with flow_count >= 3 exists (observed: flow_count=3)
- [x] UI: Tasks view shows task rows (observed: 2 rows)
- [x] UI: Task shows flow count >= 3 (observed: "3" in row text)

### Scenario 4: Analytics Accuracy
- [x] API: total_flows >= baseline + 5 (observed: 340, baseline: 335, delta: +5)
- [x] API: total_tasks >= baseline + 1 (observed: 12, baseline: 11, delta: +1)
- [x] API: Flow count consistent between endpoints (observed: both 340)
- [x] UI: Total Flows card >= baseline + 5 (observed: 340, baseline: 335)

### Scenario 5: Real-Time Updates
- [x] UI: New flow appeared without refresh (observed: count 6→7)
- [x] UI: WebSocket delivery confirmed (observed: "realtime" appeared in 2s)
- [x] API: Most recent flow matches sent request (observed: /get?realtime=e2e-abc)

### Scenario 6: Settings Round-Trip
- [x] API: Setting updated to {NEW_VALUE} (observed: 10)
- [x] UI: Settings view shows {NEW_VALUE} (observed: "10")
- [x] API: Setting restored to {ORIGINAL_VALUE} (observed: 5)

### Scenario 7: Export Integrity
- [x] API: Response is valid JSON (observed: parsed successfully)
- [x] API: Contains flows array with entries (observed: 340 entries)
- [x] API: Flows have required fields (observed: all 340 have id/host/method/path/status_code)
- [x] API: Contains flow for {TARGET_HOST} (observed: 5 matching flows)
- [~] API: row_count matches array length (SKIP: Export endpoint does not return row count metadata)

### Scenario 8: Negative Cases
- [x] API: Invalid token returns 401 (observed: 401)
- [x] API: No auth returns 401 (observed: 401)
- [x] API: Malformed settings rejected (observed: 400)
- [x] API: Out-of-range settings rejected (observed: 422)

### Scenario 9: Style Guide Conformance
- [x] 9a: Dark theme tokens defined (observed: --sse=#8b5cf6, --error-bg=#ef44441a)
- [x] 9a: Light theme tokens defined (observed: --sse=#8b5cf6, --error-bg=#dc26261a)
- [x] 9b: Dark --text-muted contrast >= 4.5:1 (observed: 5.48:1, color: rgb(138,138,142), bg: rgb(22,22,24))
- [x] 9b: Light --text-muted contrast >= 4.5:1 (observed: 5.31:1, color: rgb(89,89,96), bg: rgb(239,239,243))
- [x] 9c: Focus-visible outlines on Tab (observed: 5/6 elements showed outline)
- [x] 9d: Interactive elements >= 32px (observed: 0 undersized)
- [x] 9e: Border radius 0 on standard elements (observed: 0 violations)
- [x] 9e: Known exceptions have border-radius (observed: .status-dot=50%, .bar=2px 2px 0 0)
- [~] 9f: Disabled button states (SKIP: no disabled button rendered)
- [x] 9g: prefers-reduced-motion rule exists (observed: 1 rule, condition: (prefers-reduced-motion: reduce))
- [~] 9h: .badge.sse uses --sse token (SKIP: no .badge.sse in DOM)

### Summary
Passed: {pass_count}/{total_count} assertions across 9 scenarios
Failed: {fail_count} | Skipped: {skip_count}
```

Use `[x]` for PASS, `[ ]` for FAIL (with observed vs expected on same line), `[~]` for SKIP (with reason).

## Phase 2.5 - Review Failures

If any assertions failed, **present the failure list to the user** and ask whether to file issues.

Use AskUserQuestion:
- Question: "The following assertions failed: {list}. File issues for these failures?"
- Options: "Yes, file issues" / "No, just the report"

If the user chooses yes, create a `br` issue for each distinct failure. Group related assertion failures into a single issue (e.g. if S4.1 and S4.4 both fail because of the same stats endpoint problem, file one issue).

```bash
br create --title "E2E: {short description}" --label e2e --label bug
```

The issue body should include:
- Which scenario and assertion(s) failed
- Expected vs. observed values (exact)
- The API call or UI step that produced the failure
- No interpretation — just the facts

**Prerequisite**: The `br` tool must be available. If `br` is not found, print the failure details and instruct the user to file issues manually.

If all assertions passed, skip this phase — do not create issues for passing tests.

## Phase 3 - Cleanup

**Only if `SKILL_STARTED_SERVER` is true**: Ask the user whether to stop the server.

Use AskUserQuestion:
- Question: "The E2E feature confirmation started the Langley server. Stop it now?"
- Options: "Yes, stop it" / "No, keep it running"

If yes, kill the server process using the PID from state.json - but **first verify the PID belongs to a `langley` process** (e.g. `tasklist /FI "PID eq {pid}"` on Windows, `ps -p {pid} -o comm=` on Unix). If the PID belongs to a different process, warn the user and skip the kill.

If the skill did NOT start the server, skip this phase entirely.

## Technical Reference

- **Auth**: Dashboard auto-authenticates from localhost origin via `langley_session` HttpOnly cookie. API calls need `Authorization: Bearer {token}`.
- **State struct**: `{ proxy_addr, api_addr, ca_path, pid, started_at }`
- **Config path**: Same directory as state.json, file `config.yaml`, field `auth.token`.
- **Screenshots**: Only take screenshots on assertion failure or at scenario boundaries. Not every step.
- **WebSocket**: `ws://{api_addr}/ws` - dashboard connects automatically for live flow updates.
- **Rate limit**: API has 20 req/s sustained, 100 burst - not a concern for this test.
- **Export formats**: `ndjson` (default), `json`, `csv`. This skill uses `json` format for easier assertion on structure.
- **Task idle gap**: Default 5 minutes. Rapid requests within the gap share a task_id.
- **Settings API**: `GET /api/settings` returns current values. `PUT /api/settings` accepts `{"idle_gap_minutes": N}` where N is 1-60.
- **`br` tool**: Local issue tracker used in Phase 2.5. If unavailable, report failures as text and skip auto-filing.
- **RUN_ID tagging**: All test traffic includes `_e2e={RUN_ID}` query param. The API does **not** support filtering by this param — it exists only in the stored `path` field for post-hoc human inspection. Assertions rely on delta-based counting and host filtering, not RUN_ID filtering. Do not assume RUN_ID can scope API queries.
- **DOM selectors**: All `browser_evaluate` calls use `SEL_*` variables discovered in Phase 0.5. If a selector returns null during an assertion, that is a FAIL — do not fall back to regex on `document.body.innerText`. A null return means the DOM structure has changed since discovery, which is a real finding worth reporting.
- **Style guide**: `docs/style-guide.md` is the source of truth for Scenario 9. Read it before running style assertions to confirm expected token names, contrast thresholds, and component rules. The WCAG contrast formula in Scenario 9 is self-contained — no external libraries needed.
- **Theme switching**: Scenario 9 toggles the theme to test both dark and light modes. Always switch back to dark theme before moving to the next sub-group to avoid contaminating subsequent assertions.
