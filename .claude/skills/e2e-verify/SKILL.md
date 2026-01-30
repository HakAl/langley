---
name: e2e-verify
description: >
  General-purpose E2E verification skill that reads acceptance criteria from work artifacts
  (bd issues, .docs/ plans, git diff), converts each criterion into an executable assertion,
  runs them against a live Langley instance, and reports pass/fail with observed values.
invoke: user
---

# E2E Verify

Reads acceptance criteria from the current work context (bd issues, plan files, git diff), classifies each into an executable assertion type, runs them against a live Langley instance, and reports pass/fail with observed values.

**How this differs from other skills**:
- `e2e-smoke` checks *rendering* ("did the page load?").
- `e2e-confirm` checks *behavior* across a fixed set of 8 scenarios.
- `ux-audit` checks *experience quality* against UX principles.
- `e2e-verify` checks *whatever the current ticket says* — the AC come from the work artifact, not the skill.

The skill does NOT invent what to verify. It reads well-defined AC from existing artifacts and mechanically checks each one. Subjectivity is the enemy.

## Phase 0 — Server Bootstrap

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
   - Wait up to 10 seconds, polling `/api/health` every 1s. (Server startup uses 1s intervals because the process may still be binding ports; assertion polling in Phase 3 uses 500ms.)
   - Re-read `state.json` after startup to get actual addresses.
   - **Remember that the skill started the server** (for cleanup in Phase 5).

4. **Read auth token** from config file:
   - Windows: `%APPDATA%\langley\config.yaml`
   - Unix: `~/.config/langley/config.yaml`
   - Field: `auth.token` (format: `langley_` + 64 hex chars)

Store these values for later phases:
- `API_ADDR` — e.g. `localhost:9091`
- `PROXY_ADDR` — e.g. `localhost:9090`
- `CA_PATH` — e.g. `%APPDATA%\langley\certs\ca.crt`
- `TOKEN` — the auth token string
- `SKILL_STARTED_SERVER` — boolean flag

## Phase 1 — Gather Acceptance Criteria

Identify the work context and extract AC. Check sources in priority order — stop when AC are found.

### Step 1: Identify work context

```bash
git branch --show-current
git log --oneline -5
```

Record `BRANCH_NAME` and recent commit subjects. These are used for matching bd issues and plan files.

### Step 2: Check bd issues

```bash
bd list --status open
```

Look for:
1. Issues with label `in-progress`, OR
2. Issues whose title or ID matches the branch name (e.g., branch `fix/ISSUE-123` matches issue `ISSUE-123`), OR
3. If invoked with an argument (e.g., `/e2e-verify ISSUE-123`), use that issue directly.

For each matching issue, extract AC from:
- `acceptance_criteria` field (if present — this is a JSONL field some issues have)
- `description` field — look for lines starting with `- [ ]`, `- [x]`, or bullet points under "Acceptance" / "Acceptance Criteria" headers
- Comments (if the issue has notes)

If bd is not available or no matching issues found, proceed to Step 3.

### Step 3: Check plan files

```bash
ls .docs/plans/
```

Look for:
1. Plan files whose name matches the branch name (e.g., branch `feat/langley-run-command` matches `feat-langley-run-command.md`), OR
2. Files ending in `_wip.md` (most recent), OR
3. If Step 2 found a bd issue referencing a plan file, use that file.

In the matched plan file, extract AC from sections titled:
- `## Acceptance Criteria`
- `### Functional Requirements`
- `### Non-Functional Requirements`
- `### Quality Gates`

Each line starting with `- [ ]` or `- [x]` is one AC item.

If no plan files match, proceed to Step 4.

### Step 4: Git diff for scoping

```bash
git diff main...HEAD --stat
git diff main...HEAD
```

The diff is NOT a source of AC — it cannot invent acceptance criteria. It serves two purposes:
1. **Scoping**: If Steps 2-3 found AC, the diff helps determine which AC are relevant to the current changes (skip AC about unchanged areas).
2. **Fallback context**: If no bd issue or plan file matched, present the diff summary to the user and ask them to provide AC manually.

### Step 5: Present gathered AC

Print all gathered AC to the user:

```
## Gathered Acceptance Criteria

Source: {bd issue ID / plan file path / user-provided}
Branch: {BRANCH_NAME}

Found {N} acceptance criteria:

1. {criterion text} — from {source:line}
2. {criterion text} — from {source:line}
...

Proceed with verification?
```

Use AskUserQuestion:
- Question: "Found {N} acceptance criteria from {source}. Proceed with verification, or edit the list?"
- Options: "Proceed" / "Edit list" / "Cancel"

If "Edit list": ask the user to provide the corrected AC list.
If "Cancel": skip to Phase 5 (cleanup).

## Phase 2 — Classify & Plan

For each accepted AC item, classify it into an executable assertion type and write the specific check.

### Classification rules

| Type | When to use | Tool |
|------|-------------|------|
| `api` | AC involves API behavior (endpoints, responses, status codes, validation) | Bash curl + jq |
| `ui` | AC involves dashboard/frontend behavior (rendering, interaction, display) | browser_evaluate |
| `cli` | AC involves CLI behavior (commands, exit codes, output) | Bash |
| `unit-test` | AC says "unit test for X" or "test coverage for X" | Bash `go test` |
| `manual-only` | AC requires human interaction that cannot be automated (e.g., "Ctrl+C terminates both processes", "works interactively") | N/A — listed in report, skipped |

Classification rules:
- If the AC mentions a specific API endpoint or HTTP status code → `api`.
- If the AC mentions UI, dashboard, view, display, rendering → `ui`.
- If the AC mentions running a command, exit code, or CLI output → `cli`.
- If the AC mentions unit tests, test coverage, or `-race` → `unit-test`.
- If the AC requires interactive terminal input, physical observation, or subjective judgment → `manual-only` with reason.
- If ambiguous, prefer `api` over `ui` (API assertions are more reliable — programmatic extraction, no DOM interpretation).
- A single AC can map to multiple assertions (e.g., "settings should reject out-of-range values" → one `api` assertion for the endpoint + one `ui` assertion for the error message).
- **State isolation**: If the AC involves a count, list, or existence check on shared state (e.g., "5 flows should exist", "task appears in list"), the plan MUST include a **baseline capture step** before execution. Record the current value before acting, then assert on the **delta** (`flow count increased by >= 5 since baseline`), not the absolute (`total_flows >= 5`). Absolute assertions on shared state are unfalsifiable — they pass before the skill runs.
- **Negative assertions**: For each AC, consider whether the correct behavior also implies something should NOT happen (e.g., "valid input is accepted" also implies "no error message is shown"). Include at least one negative assertion per verification plan — "does the wrong thing NOT happen?" catches error-path bugs that happy-path-only verification misses.

### Verification item format

Each AC line becomes a verification item:

```
V{N}: [{type}] {criterion}
  Source: {bd issue ID / plan file:line}
  Assertion: {the exact command or check}
  Expected: {value or condition}
```

### Output the plan

Print the numbered verification plan:

```
## Verification Plan

V1: [api] Endpoint should reject invalid input
  Source: ISSUE-123
  Assertion: curl -s -o /dev/null -w "%{http_code}" -X PUT -H "Authorization: Bearer {TOKEN}" -H "Content-Type: application/json" -d '{"field": "bad_value"}' "http://{API_ADDR}/api/resource"
  Expected: 400 or 422

V2: [ui] Form should show validation error for invalid input
  Source: ISSUE-123
  Assertion: browser_evaluate(() => document.querySelector('[data-test="validation-error"]')?.textContent)
  Expected: string containing "must be between X and Y"

V3: [manual-only] Ctrl+C terminates both processes
  Source: plan-file.md:42
  Reason: requires interactive terminal — cannot automate
  Action: listed in report, skipped

V4: [unit-test] Unit tests for module pass
  Source: plan-file.md:58
  Assertion: go test -v -run TestModuleName ./path/to/package/
  Expected: exit code 0
```

Do NOT proceed without the plan. The plan forces the agent to commit to specific assertions before executing — preventing post-hoc rationalization.

## Phase 3 — Execute

For each verification item (not `manual-only`), execute the assertion and record results.

### Execution rules

1. **Extract-then-compare, never interpret-and-decide.** Run the tool, capture the raw value, print it, THEN compare to expected. Never combine extraction and judgment.

2. **Poll with timeout, never fixed sleep.** If waiting for a state change, poll every 500ms with a 10s timeout. Do not use `sleep`.

3. **Include observed values for ALL assertions** — pass and fail. A report entry without an observed value has no audit trail.

4. **Never explain away a failure.** If the assertion fails, it fails. Do not reinterpret the assertion to make it pass. Do not add caveats.

5. **Never reinterpret an assertion to make it pass.** The assertion was committed in Phase 2. If the observed value doesn't match expected, that's a FAIL.

6. **If a setup step fails** (e.g., server unreachable), mark the assertion as FAIL with reason "Setup failed: {detail}" and continue to the next item.

7. **Take screenshots on failure only** (for `ui` type assertions). Use `browser_take_screenshot` with descriptive filename.

### Type-specific execution patterns

**`api` type:**
```bash
# Unix/bash
RESULT=$(curl -s -o /dev/null -w "%{http_code}" ...)
# Or for body inspection:
RESULT=$(curl -sf -H "Authorization: Bearer {TOKEN}" "http://{API_ADDR}/api/...")
```
```powershell
# Windows (PowerShell)
$RESULT = (curl -s -o $null -w "%{http_code}" ...)
# Or for body inspection:
$RESULT = (curl -sf -H "Authorization: Bearer {TOKEN}" "http://{API_ADDR}/api/...")
```
Compare the response status code or body field to expected. Record exact observed value.

**`ui` type:**
1. `browser_navigate` to the relevant page.
2. `browser_wait_for` with a known text string that signals the page is ready (e.g., a heading or label unique to that view). If the text does not appear within 10s, FAIL with reason "page did not become ready within timeout."
3. `browser_evaluate` to extract the specific DOM value programmatically.
4. Compare extracted value to expected. Record exact observed value.
5. If `browser_evaluate` returns null → FAIL with reason "selector returned null — DOM structure may not match expectations." Do NOT fall back to regex on `document.body.innerText`.

**`cli` type:**
```bash
# Unix/bash
OUTPUT=$(command args 2>&1)
EXIT_CODE=$?
```
```powershell
# Windows (PowerShell)
$OUTPUT = & command args 2>&1
$EXIT_CODE = $LASTEXITCODE
```
Compare exit code and/or output to expected. Record both.

**`unit-test` type:**
```bash
# Unix/bash
go test -v -run {TestPattern} {package} 2>&1
```
```powershell
# Windows (PowerShell)
go test -v -run {TestPattern} {package} 2>&1 | Out-String
```
- PASS if exit code is 0.
- FAIL if exit code is non-zero. Record the test output (first 50 lines).
- If the test file or package doesn't exist, FAIL with reason "test not found: {path}".

### Result recording

For each item, record:
- **Status**: PASS / FAIL / SKIP
- **Observed value**: the exact value returned by the tool
- **Expected value**: from the plan
- **Detail**: for FAIL — what went wrong; for SKIP — why it was skipped

## Phase 4 — Report

Print the verification report in this exact format:

```
## E2E Verification Report

Branch: {BRANCH_NAME}
Source: {bd issue ID / plan file / user-provided}
Date: {YYYY-MM-DD}

### Verification Items
- [x] V1: {criterion} (observed: {value}, expected: {value}) [api]
- [ ] V2: {criterion} (observed: {value}, expected: {value}) [ui] — FAIL
- [~] V3: {criterion} — SKIP (manual-only: {reason})
- [x] V4: {criterion} (observed: 0 exit code, expected: 0) [unit-test]

### Summary
Total: {n} | Pass: {n} | Fail: {n} | Skip: {n}

### Failures
V2: {detailed failure description with observed vs expected}
```

Format rules:
- `[x]` for PASS — include observed value, even on pass.
- `[ ]` for FAIL — include observed AND expected values. Add `— FAIL` suffix.
- `[~]` for SKIP — include reason. Used for `manual-only` items.
- Every assertion MUST include the observed value. A report entry without one cannot be distinguished from a sycophantic pass.
- The `[type]` tag at the end of each line shows the assertion type.
- The `### Failures` section expands each failure with full detail (command run, observed output, expected output).
- If ALL assertions passed, still include the `### Failures` section with "None." Do not omit it.
- If no AC were found (Phase 1 returned nothing), report: "No acceptance criteria found. Provide AC manually or ensure a bd issue or plan file exists for this branch."

### Review failures

If any assertions failed, present the failure list to the user and ask whether to file issues.

Use AskUserQuestion:
- Question: "The following assertions failed: {list}. File bd issues for these failures?"
- Options: "Yes, file issues" / "No, just the report"

If yes:
```bash
bd create --title "E2E-verify: {short description}" --label e2e --label bug
```

Issue body should include:
- Which verification item (V{N}) failed
- The AC text (criterion)
- Expected vs. observed values (exact)
- The command or check that produced the failure
- No interpretation — just the facts

Group related failures into a single issue (e.g., if V1 and V2 both fail because of the same endpoint, file one issue).

If `bd` is not available, print failure details and instruct the user to file issues manually.

If all assertions passed, skip issue filing.

## Phase 5 — Cleanup

**Only if `SKILL_STARTED_SERVER` is true**: Ask the user whether to stop the server.

Use AskUserQuestion:
- Question: "The E2E verification started the Langley server. Stop it now?"
- Options: "Yes, stop it" / "No, keep it running"

If yes, kill the server process using the PID from state.json — but **first verify the PID belongs to a `langley` process** (e.g., `tasklist /FI "PID eq {pid}"` on Windows, `ps -p {pid} -o comm=` on Unix). If the PID belongs to a different process, warn the user and skip the kill.

If the skill did NOT start the server, skip this phase entirely.

## Technical Reference

- **Auth**: Dashboard auto-authenticates from localhost origin via `langley_session` HttpOnly cookie. API calls need `Authorization: Bearer {token}`.
- **State struct**: `{ proxy_addr, api_addr, ca_path, pid, started_at }`
- **Config path**: Same directory as state.json, file `config.yaml`, field `auth.token`.
- **Screenshots**: Only on assertion failure (for `ui` type). Save with descriptive filenames.
- **WebSocket**: `ws://{api_addr}/ws` — dashboard connects automatically for live flow updates.
- **Rate limit**: API has 20 req/s sustained, 100 burst — not a concern for this test.
- **Settings API**: `GET /api/settings` returns current values. `PUT /api/settings` accepts `{"idle_gap_minutes": N}` where N is 1-60.
- **`bd` tool**: Local issue tracker. `bd list --status open` shows open issues. `bd show {id}` shows detail. If unavailable, report failures as text.
- **DOM selectors**: Use `browser_evaluate` for programmatic extraction. If a selector returns null, that is a FAIL — do not fall back to regex on `document.body.innerText`.
- **Polling pattern**: Poll every 500ms, timeout after 10s. Replace all fixed sleeps.
- **Beads issue fields**: Issues may have `acceptance_criteria` (string), `description` (with bullet AC), and `notes`. Check all three.
- **Plan file AC sections**: Look for `## Acceptance Criteria`, `### Functional Requirements`, `### Non-Functional Requirements`, `### Quality Gates`. Lines starting with `- [ ]` or `- [x]` are AC items.
