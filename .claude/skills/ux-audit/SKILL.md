---
name: ux-audit
description: >
  Systematic UX audit of the Langley dashboard via Playwright.
  Evaluates experience quality across navigation, feedback, accessibility, view-specific issues, and responsiveness.
  Files findings as bd issues.
invoke: user
---

# UX Audit

Principle-based evaluation of the Langley dashboard user experience via Playwright MCP browser tools. Each run evaluates the current state of the experience against UX principles — not a fixed checklist.

**How this differs from `e2e-smoke` and `e2e-confirm`:**
- Smoke checks: "Did the page render?"
- Confirm checks: "Did the correct data appear?"
- UX audit: "Is the experience good? What's the worst thing about it right now?"

**Why open-ended, not a checklist:** A fixed list of 33 known issues becomes a rubber stamp after the issues are fixed. This skill works like a human auditor — it evaluates against principles, surfaces new issues each run, and re-evaluates past fixes to judge whether they're actually good. Different runs should produce different findings as the dashboard evolves.

## Phase 0 — Server Bootstrap

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
   - **Remember that the skill started the server** (for cleanup in Phase 7).

4. **Read auth token** from config file:
   - Windows: `%APPDATA%\langley\config.yaml`
   - Unix: `~/.config/langley/config.yaml`
   - Field: `auth.token` (format: `langley_` + 64 hex chars)
   - Token is needed for API curl calls, NOT for browser (dashboard auto-authenticates via localhost cookie).

Store these values for all phases:
- `API_ADDR` — e.g. `localhost:9091`
- `PROXY_ADDR` — e.g. `localhost:9090`
- `CA_PATH` — e.g. `%APPDATA%\langley\certs\ca.crt`
- `TOKEN` — the auth token string
- `SKILL_STARTED_SERVER` — boolean flag

**No traffic generation.** This skill works with whatever data already exists. If the flows list is empty, that itself is a valid state to audit (empty states are part of the experience).

## Phase 0.5 — Prior Findings

Before auditing, check what issues already exist from prior runs:
```bash
bd list --label ux-audit --status open
bd list --label ux-audit --status closed
```

Record both lists. Open issues tell you what to re-evaluate (was it fixed? was the fix good?). Closed issues tell you what was already addressed (don't re-file, but do re-evaluate quality).

## Phase 1 — Systematic Walkthrough

Navigate every view and interaction path in the dashboard. The goal is to **observe and document**, not to judge yet. Build a picture of the current state.

1. `browser_navigate` to `http://{API_ADDR}`. Take screenshot: `ux-audit-initial.png`.
2. Visit each view in order: Flows, Analytics, Tasks, Tools, Anomalies, Settings.
   - On each view: `browser_snapshot`, take a screenshot (`ux-audit-{view}.png`).
   - Note what's present: controls, data, empty states, indicators, labels.
3. Test core interactions on the Flows view (if data exists):
   - Click a flow row to open the detail panel. Screenshot.
   - Use filters if they exist. Screenshot.
   - Try export controls if they exist. Screenshot.
4. Test keyboard shortcuts: `?`, `j`, `k`, `1`-`6`, `/`, `Escape`.
5. Test viewports: resize to 480px, 768px, 1200px. Screenshot each.
6. Check the WebSocket status indicator.
7. Test browser back/forward after navigating between views.
8. Test deep linking: navigate directly to `http://{API_ADDR}/#analytics`.

Reset viewport to 1280x720 after responsive checks.

**Output of this phase**: A set of screenshots and raw observations. No judgments yet.

---

## Phase 2 — Evaluate

Evaluate the dashboard against the areas below. For each area, answer the guiding questions by examining what you observed in Phase 1. Every observation must cite concrete evidence (a screenshot, a snapshot detail, an extracted value).

### Evaluation method

For each area:
1. **Observe**: What does the dashboard actually do? Use `browser_evaluate` to extract concrete values where possible (element counts, text content, attribute presence). Use snapshots for structural understanding.
2. **Evaluate**: How does the observed behavior compare to the UX principle? Is it good, adequate, or problematic?
3. **Find**: If something is problematic, describe the finding with observed evidence and what good would look like. Assign severity.
4. **Re-evaluate prior findings**: If a previous audit found an issue in this area, check whether it's been fixed. If fixed, evaluate whether the fix is good — a bad fix is a new finding.

### Severity levels

Assign during evaluation, not in advance:
- **P1 (Critical)**: User cannot complete a workflow, or system state is ambiguous (data loss risk, silent failures, broken core paths).
- **P2 (Major)**: Significant friction, violates common patterns, or degrades a class of users (accessibility, mobile).
- **P3 (Minor)**: Polish issue, inconsistency, missing nice-to-have.

---

### Area 1: Navigation & Wayfinding

**Principle**: The user should always know where they are, be able to get where they want to go, and never feel lost.

**Guiding questions**:
- Is the user's current location obvious? (active state on nav, URL reflects view, page title/heading)
- Can the user move between all views without friction? (nav responsiveness, keyboard shortcuts, breadcrumbs)
- Does browser history work? (back/forward, deep linking, bookmarkable URLs)
- Is state preserved across navigation? (filter values, scroll position, selected items)
- Are there dead ends — views where the user can't easily get back or continue?
- On mobile viewports, is navigation still accessible?

**Things to try**: Click each nav item. Use back/forward. Deep-link via URL hash. Switch views and return. Resize to mobile.

---

### Area 2: Feedback & Communication

**Principle**: The system should always communicate its state. The user should never wonder "did that work?" or "is it loading?"

**Guiding questions**:
- Does the initial page load show a loading indicator, or does it flash blank then pop in?
- When switching views, is there feedback during data fetch?
- After user actions (save settings, export, filter), is there confirmation?
- Is the WebSocket connection status communicated clearly? (not just color — also text)
- When something goes wrong (failed save, lost connection, empty results), does the UI explain it?
- Are success and error states distinguishable?

**Things to try**: Reload the page. Switch views rapidly. Save settings (then restore). Export data. Disconnect WebSocket if possible.

---

### Area 3: Accessibility & Keyboard

**Principle**: Every feature should be usable without a mouse and perceivable without sight. Keyboard shortcuts should be discoverable, consistent, and not conflict with browser/OS defaults.

**Guiding questions**:
- Can every interactive element be reached and activated via keyboard?
- Do keyboard shortcuts work as documented (`?`, `j`/`k`, `1`-`6`, `/`, `Escape`)?
- Are shortcuts discoverable (help overlay, tooltips)?
- Is focus management correct? (modals trap focus, panels receive focus on open, focus returns on close)
- Are ARIA roles correct and complete? (listbox vs. grid, labeled controls, live regions for updates)
- Does the status indicator communicate via both visual and textual means (not color-only)?
- Can a screen reader user understand the flow list structure?

**Things to try**: Navigate with Tab and arrow keys. Open/close panels. Open help overlay (`?`). Check ARIA attributes via snapshot. Evaluate focus after opening/closing modals.

---

### Area 4: View-Specific Quality

**Principle**: Each view should serve its purpose clearly. Data should be comprehensible, controls discoverable, and empty states helpful.

**Evaluate each view**:

**Flows view**:
- Is the flow list scannable? (visual hierarchy, column alignment, truncation)
- Is filtering discoverable and clearable?
- Does the list communicate its scope? ("Showing X of Y", pagination, load-more)
- Is the detail panel well-structured? (header with method/path, organized sections, close affordance)
- Does the export function have clear labeling and feedback?

**Analytics view**:
- Are stat cards self-explanatory? (labels include units — "$", "tokens", etc.)
- Do charts handle zero-data gracefully?
- Is the time range or scope of analytics clear?

**Tasks view**:
- Are task rows interactive-looking? (hover states, click affordances)
- Is the relationship between tasks and flows clear?
- Does the empty state explain what tasks are and how they're created?

**Tools view**:
- Does the view explain what it tracks?
- Is the empty state helpful?

**Anomalies view**:
- Does the view explain what anomalies are?
- Is the empty state helpful?

**Settings view**:
- Are input constraints communicated before the user submits? (range labels, placeholder text)
- Does validation catch invalid input with clear error messages?
- Is save confirmation clear?

---

### Area 5: Responsiveness & Layout

**Principle**: The dashboard should be usable at any viewport width. Layout should adapt — not just shrink or overflow.

**Guiding questions**:
- At 480px (mobile): Is navigation accessible? Are controls usable? Is text readable without horizontal scroll?
- At 768px (tablet): Does the layout adapt? (collapsing sidebar, reflowing grid)
- At 1200px (desktop with detail panel): Does the detail panel sit beside the list or overlap it?
- At the user's actual viewport: Is there wasted space or cramped space?
- Do tables/lists handle narrow widths? (text truncation, column hiding, horizontal scroll with sticky headers)

---

## Phase 3 — Report

### Report Format

```
## UX Audit Results

Date: {date}
Dashboard state: {flow count} flows, {task count} tasks
Prior open issues: {count}

### Area 1: Navigation & Wayfinding
**Overall**: {1-2 sentence summary of current state}

Findings:
- [!] P2: {short title} — {observed behavior}. Expected: {what good looks like}. Evidence: {screenshot or snapshot detail}.
- [x] Previously reported {issue-id} ({title}) — now fixed. Fix quality: {good / adequate / poor, with reason}.

### Area 2: Feedback & Communication
**Overall**: {summary}

Findings:
- [!] P1: {title} — {observed}. Expected: {good}. Evidence: {ref}.
...

### Area 3: Accessibility & Keyboard
**Overall**: {summary}
...

### Area 4: View-Specific Quality
**Overall**: {summary}
...

### Area 5: Responsiveness & Layout
**Overall**: {summary}
...

### Summary
| Area             | New Findings | Verified Fixes | Regressions |
|-----------------|-------------|----------------|-------------|
| Navigation      |           1 |              2 |           0 |
| Feedback        |           2 |              1 |           0 |
| Accessibility   |           0 |              0 |           0 |
| View-Specific   |           3 |              1 |           1 |
| Responsiveness  |           1 |              0 |           0 |
| **Total**       |           7 |              4 |           1 |

New: {count} findings (P1: {n}, P2: {n}, P3: {n})
Verified fixes: {count} from prior audits
Regressions: {count} (previously fixed, now broken again)

### What's the worst thing right now?
{1-2 sentences identifying the single most impactful UX problem. This forces prioritization.}
```

### Finding format

Each finding must include:
- **Severity**: P1/P2/P3 — assigned based on the impact observed, not predetermined.
- **Title**: Short, descriptive.
- **Observed**: What the dashboard actually does. Concrete and evidence-based.
- **Expected**: What good UX looks like for this interaction. Reference common patterns.
- **Evidence**: Screenshot filename, snapshot excerpt, or `browser_evaluate` result.

### Re-evaluated prior findings

For each open issue from a prior audit (Phase 0.5):
- If fixed and the fix is good: mark as `[x] Verified fix` with a note on quality.
- If fixed but the fix is bad: file a new finding about the fix quality.
- If not fixed: leave the existing issue. Do not re-file. Note "Still open: {issue-id}".
- If regressed (was fixed, now broken): file a new finding with `regression` label.

---

## Phase 4 — File Issues

**Present findings to the user first.** Use AskUserQuestion:
- Question: "The audit found {N} new issues. File them as bd issues?"
- Options: "Yes, file all" / "Let me pick which to file" / "No, just the report"

If filing:

```bash
bd create "UX: {short title}" --label ux-audit --label {category} --priority {1|2|3} --type {bug|task}
```

Where:
- `{category}` is one of: `feedback`, `navigation`, `accessibility`, `consistency`, `information`, `responsiveness`, `discoverability`, `error-handling`
- `--type bug` for broken/missing expected behavior
- `--type task` for enhancements and improvements
- `--priority` maps: P1 = `1`, P2 = `2`, P3 = `3`

**Add detail comment** with the full finding (observed, expected, evidence):
```bash
bd comment {issue-id} "{observed behavior}. Expected: {what good UX looks like}. Evidence: {screenshot ref}. Affects: {views/components}."
```

**Close verified fixes:**
```bash
bd close {issue-id} --reason "Verified fixed in UX audit {date}"
```

**Prerequisite**: The `bd` tool must be available. If not found, print findings and instruct the user to file manually.

After filing, print:
```
### Issues Filed
- {issue-id}: UX: {title} (P{n}, {category})
...
Total: {count} filed, {closed} closed as fixed, {skipped} already tracked
```

---

## Phase 7 — Cleanup

**Only if `SKILL_STARTED_SERVER` is true**: Ask the user whether to stop the server.

Use AskUserQuestion:
- Question: "The UX audit started the Langley server. Stop it now?"
- Options: "Yes, stop it" / "No, keep it running"

If yes, kill the server process using the PID from state.json — but **first verify the PID belongs to a `langley` process** (e.g. `tasklist /FI "PID eq {pid}"` on Windows, `ps -p {pid} -o comm=` on Unix). If the PID belongs to a different process, warn the user and skip the kill.

If the skill did NOT start the server, skip this phase entirely.

---

## Grading Rules

- **Grade honestly, not strictly for its own sake.** The goal is accurate evaluation, not maximum findings. If something is genuinely good, say so. If something is bad, say so. Don't manufacture findings to avoid a clean report, but also don't rationalize away real problems.
- **Never rationalize.** If a loading state is missing, it's missing — don't say "the data loads fast enough." Users on slow connections will experience the gap.
- **Findings require evidence.** Every finding must reference a screenshot, a snapshot detail, or a `browser_evaluate` result. "The navigation feels confusing" is not a finding. "The nav has no active state indicator — all buttons look identical when Analytics is selected (see ux-audit-analytics.png)" is.
- **Judge quality, not just presence.** A loading spinner that appears for 50ms and disappears is technically present but functionally useless. An error message that says "Error" with no detail is technically feedback but practically unhelpful. Evaluate whether the implementation actually serves the user.
- **"What's the worst thing?" is mandatory.** The report must answer this. It forces prioritization and prevents a report that lists 12 P3s while burying the one thing that actually matters.
- **SKIP only when genuinely untestable.** "No data exists to test empty states" is a valid skip. "Hard to evaluate" is not.
- **Screenshots are evidence.** Take them on findings and at phase boundaries. They're the audit trail.

## Technical Reference

- **Auth**: Dashboard auto-authenticates from localhost origin via `langley_session` HttpOnly cookie. API calls need `Authorization: Bearer {token}`.
- **State struct**: `{ proxy_addr, api_addr, ca_path, pid, started_at }`
- **Config path**: Same directory as state.json, file `config.yaml`, field `auth.token`.
- **Screenshots**: Saved by Playwright MCP to its configured output directory.
- **WebSocket**: `ws://{api_addr}/ws` — dashboard connects automatically for live flow updates.
- **Rate limit**: API has 20 req/s sustained, 100 burst — not a concern for this audit.
- **Dashboard views**: Flows (1), Analytics (2), Tasks (3), Tools (4), Anomalies (5), Settings (6).
- **Keyboard shortcuts**: `?` help, `j`/`k` nav, `1-6` views, `/` search, `Escape` close, `Enter` select.
- **Settings range**: `idle_gap_minutes` accepts 1-60.
- **Flow list cap**: 50 most recent on initial load, 100 max via WebSocket.
- **`bd` tool**: Local issue tracker. If unavailable, report findings as text and skip auto-filing.
