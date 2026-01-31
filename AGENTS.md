## ARCHITECTURAL PRINCIPLES (READ THIS FIRST)

**Boy scout rule: always leave code better than you found it.**

> For general coding principles (SOLID, DI philosophy, testing mindset), see `~/.claude/CLAUDE.md`.
> This document covers **project-specific** conventions for langley.

### Project Stack
- **Backend:** Go
- **Frontend:** React (Vite dev server)
- **E2E Testing:** Playwright (via MCP server)
- **Issue Tracking:** bd (beads) - lightweight git-integrated issue tracker

### You Are an Architect, Not a Code Monkey

Before writing ANY code, you must:
1. **Design the interface** - What contract is needed?
2. **Consider SOLID principles** - Single Responsibility, Open/Closed, Liskov Substitution, Interface Segregation, Dependency Inversion
3. **Plan dependency injection** - How will this be tested? What needs to be injected?
4. **Think about edge cases** - What can go wrong? What are the boundaries?
5. **Design before coding** - No coding until the design is clear

### MANDATORY: Interface-First Design

**NEVER write a concrete type without defining its interface first.**

**Why?** Interfaces enable:
- Testing with test doubles
- Swapping implementations
- Dependency inversion
- Clear contracts

### Interface Design (Go)

Define minimal interfaces in the package that *uses* them (not the package that implements them).
Interfaces must be small and specific (ISP); prefer multiple tiny interfaces over one big one.

```go
// GOOD: Small, focused interface defined by consumer
type FlowReader interface {
    GetFlow(ctx context.Context, id string) (*Flow, error)
}

// GOOD: Compose interfaces when needed
type FlowStore interface {
    FlowReader
    FlowWriter
}

// BAD: Kitchen-sink interface
type Store interface {
    GetFlow(...)
    SaveFlow(...)
    DeleteFlow(...)
    GetEvents(...)
    SaveEvent(...)
    GetStats(...)
    // 20 more methods...
}
```

---

## GO IDIOMS (PROJECT-SPECIFIC)

### Context Propagation

**Every function that does I/O or may block MUST accept `context.Context` as its first parameter.**

```go
// GOOD: Context first, enables cancellation and timeouts
func (s *SQLiteStore) GetFlow(ctx context.Context, id string) (*Flow, error) {
    row := s.db.QueryRowContext(ctx, "SELECT ...", id)
    // ...
}

// BAD: No context - can't cancel, can't timeout
func (s *SQLiteStore) GetFlow(id string) (*Flow, error) {
    row := s.db.QueryRow("SELECT ...", id)  // Blocks forever if DB hangs
}
```

### Defer for Cleanup

Use `defer` immediately after acquiring a resource:

```go
// GOOD: Defer right after acquire
func processFile(path string) error {
    f, err := os.Open(path)
    if err != nil {
        return err
    }
    defer f.Close()  // Immediately after successful open

    // ... use f ...
}

// GOOD: Mutex pattern
func (s *Server) updateState() {
    s.mu.Lock()
    defer s.mu.Unlock()

    // ... modify state ...
}
```

### Code Formatting

**All Go code MUST be formatted.** `make check` enforces this.

```bash
gofmt -s -w .      # Format with simplification
goimports -w .     # Format + fix imports
```

---

## CONCURRENCY (Go)

### Goroutine Ownership

**Whoever starts a goroutine is responsible for ensuring it stops.**

```go
// GOOD: Clear ownership with done channel
type Server struct {
    done chan struct{}
    wg   sync.WaitGroup
}

func (s *Server) Start() {
    s.wg.Add(1)
    go func() {
        defer s.wg.Done()
        for {
            select {
            case <-s.done:
                return
            case work := <-s.workChan:
                s.process(work)
            }
        }
    }()
}

func (s *Server) Stop() {
    close(s.done)
    s.wg.Wait()  // Wait for goroutine to finish
}

// BAD: Orphaned goroutine - no way to stop it
func (s *Server) Start() {
    go func() {
        for work := range s.workChan {  // Blocks forever if channel never closes
            s.process(work)
        }
    }()
}
```

### Channel vs Mutex

- **Channel**: For transferring ownership of data between goroutines
- **Mutex**: For protecting shared state accessed by multiple goroutines

```go
// Channel: Passing work to a worker
workChan <- job  // Ownership transfers to receiver

// Mutex: Multiple goroutines reading/writing same map
s.mu.Lock()
s.cache[key] = value
s.mu.Unlock()
```

### Race Detector

**ALWAYS run tests with race detector for concurrent code:**

```bash
go test -race ./...
```

---

## DEPENDENCY INJECTION (MANDATORY)

### The Rule: ALL Dependencies MUST Be Injected

**NO direct instantiation of dependencies in type constructors.**

### Constructor Rules

1. **NO side effects** - Constructors assign dependencies only
2. **NO business logic** - Move logic to explicit methods
3. **NO I/O operations** - No file reads, no network calls
4. **NO auto-registration** - Explicit is better than implicit
5. **Provide defaults with factory functions** - `NewFoo()` creates with defaults, `NewFooWith(deps)` for injection

### Required Abstractions

These core components MUST be injected via interfaces, not concrete types:
- `Store` - data persistence
- `Redactor` - sensitive data masking
- `CertSource` - TLS certificate management

---

## ERROR HANDLING (Go)

### The Rule: Errors Are Values, Handle Them Explicitly

```go
// GOOD: Handle errors at the appropriate level
result, err := doThing()
if err != nil {
    return fmt.Errorf("doThing failed: %w", err)  // Wrap with context
}

// BAD: Swallowing errors
result, _ := doThing()  // Never do this
```

### Error Wrapping
- Use `fmt.Errorf("context: %w", err)` to add context while preserving the original
- Check specific errors with `errors.Is()` and `errors.As()`
- Return errors to callers; let them decide how to handle

### Panic Policy
- **NEVER** panic in library code
- Panics are only acceptable for truly unrecoverable states (programmer errors, not runtime errors)

---

## SECURITY

### Input Validation

**Validate at system boundaries, trust internal code.**

```go
// GOOD: Validate at the API boundary
func (h *Handler) GetFlow(w http.ResponseWriter, r *http.Request) {
    id := chi.URLParam(r, "id")
    if !isValidFlowID(id) {  // Validate here
        http.Error(w, "invalid flow ID", http.StatusBadRequest)
        return
    }

    flow, err := h.store.GetFlow(r.Context(), id)  // Store trusts valid input
    // ...
}

// BAD: Validating deep in the stack (defensive programming spiral)
func (s *SQLiteStore) GetFlow(ctx context.Context, id string) (*Flow, error) {
    if id == "" {  // Why would this happen if API validated?
        return nil, errors.New("empty id")
    }
    // ...
}
```

### Sensitive Data

- **NEVER** log API keys, tokens, or credentials
- Use the `Redactor` interface to mask sensitive data before storage/display
- Environment variables for secrets, not config files

---

## LOGGING

Use structured logging with consistent levels:
- **ERROR**: Something failed that affects functionality
- **WARN**: Something unexpected that the system handled
- **INFO**: Significant state changes (startup, shutdown, connections)
- **DEBUG**: Detailed diagnostic info (disabled in production)

Include context in log messages: request IDs, user identifiers, operation names.

---

## TESTS

### Test Quality Checklist

Before writing ANY test, answer these questions:

**Does this test prove a feature works?**
- If NO -> Don't write it

**Would this test fail if the feature breaks?**
- If NO -> Don't write it

**Can I refactor internals without breaking this test?**
- If NO -> You're testing implementation, not behavior

**Does this test cover edge cases?**
- Empty inputs?
- Boundary values?
- Error conditions?
- Invalid data?

### TEST ISOLATION (CRITICAL)

**NEVER MAKE REAL API CALLS IN TESTS. EVER.**
**TEST BEHAVIOR THROUGH INTERFACE-BASED TEST DOUBLES, NOT REAL APIS**

### Backend Tests (Go)

```bash
go test -race ./...              # Always use race detector
go test -v -run TestName ./...   # Run specific test
```

### Frontend Tests (React)

Use Playwright MCP server for E2E verification of the dashboard UI.
Unit tests use Vitest for component logic.

### Frontend CSS Rules

**All UI changes MUST follow `docs/style-guide.md`.**

- Use design tokens (`--bg-primary`, `--accent`, etc.) — never hardcode colors
- Every interactive element needs `:hover`, `:active`, `:focus-visible`, and `:disabled` states
- `prefers-reduced-motion` is enforced globally — no opt-out
- Contrast: normal text >= 4.5:1, non-text UI >= 3:1
- Border radius is 0 except documented exceptions (`.status-dot`, `.bar`)

---

# Agent Instructions

## Project Structure

```
langley/
├── cmd/langley/         # Main entry point
├── internal/
│   ├── api/             # REST API handlers, export logic
│   ├── config/          # Configuration loading
│   ├── parser/          # SSE/JSON response parsing
│   ├── provider/        # LLM provider detection (anthropic, openai, etc.)
│   ├── proxy/           # HTTPS CONNECT proxy, request interception
│   ├── redact/          # Sensitive data masking
│   ├── store/           # SQLite persistence (flows, events, tools)
│   ├── task/            # Task ID extraction
│   ├── tls/             # Certificate generation
│   └── ws/              # WebSocket for live updates
├── web/                 # React frontend (Vite)
│   ├── src/App.tsx      # Main dashboard component
│   └── tests/           # Playwright E2E tests
├── test/e2e/            # Go E2E tests
└── Makefile             # Build/dev commands
```

## Key Files

| File | Purpose |
|------|---------|
| `internal/store/store.go` | Store interface (add methods here first) |
| `internal/store/sqlite.go` | SQLite implementation + schema |
| `internal/api/api.go` | API route registration + handlers |
| `internal/proxy/proxy.go` | HTTPS proxy core logic |
| `web/src/App.tsx` | React dashboard (single-file app) |
| `web/src/index.css` | All CSS — uses design tokens, no hardcoded colors |
| `docs/style-guide.md` | **UI style guide** — source of truth for tokens, a11y, components |
| `config.yaml` | Runtime configuration |

## Building & Running

Use `make` (via Git Bash on Windows). Install make if needed: `choco install make` or use MinGW.

```bash
# Core commands
make help          # Show all targets
make install-deps  # Install Go + npm dependencies
make dev           # Run dev servers (backend + frontend hot reload)
make build         # Build production binary
make test          # Run all tests (with -race flag)
make check         # Lint + format + test (quality gate)
make clean         # Remove build artifacts

# Development helpers
make dev-debug     # Backend with debug logging (run dev-frontend separately)
make stop          # Kill orphaned dev processes (Windows Ctrl+C workaround)
make stop-frontend # Kill vite only
make stop-backend  # Kill langley only
make test-cover    # Generate coverage report
```

**Development workflow:**
```bash
make install-deps  # First time only
make dev           # Starts backend + Vite dev server
# Dashboard: http://localhost:5173
# Proxy: localhost:9090
```

**Before committing:**
```bash
make check         # Must pass (includes lint + format + test)
```

**After tests pass, verify against the ticket:**
```bash
/e2e-verify <bead-id>   # Reads AC from the bead, runs executable assertions
```
Run this when finishing work on a feature, bug fix, or any bead with acceptance criteria. The skill extracts AC from the issue, classifies each into an executable check (API, UI, CLI, unit-test), runs them against a live instance, and reports pass/fail with observed values. Do not close a bead until `e2e-verify` passes or remaining failures are acknowledged by the user.

## API Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /api/flows` | List flows (paginated, filterable) |
| `GET /api/flows/count` | Count flows matching filters |
| `GET /api/flows/export` | Export flows (ndjson/json/csv) |
| `GET /api/flows/{id}` | Get single flow detail |
| `GET /api/flows/{id}/events` | Get SSE events for flow |
| `GET /api/stats` | Dashboard stats |
| `GET /api/analytics/*` | Task/tool/cost analytics |
| `GET /api/health` | Health check |
| `WS /ws` | Live flow updates |

## Configuration

Environment variables override `config.yaml`:
- `LANGLEY_AUTH_TOKEN` - API auth token
- `LANGLEY_PROXY_PORT` - Proxy listen port (default: 9090)
- `LANGLEY_API_PORT` - Dashboard API port (default: 8080)
- `LANGLEY_DB_PATH` - SQLite database path

## Debugging Tips

```bash
# Run backend with verbose logging
make dev-debug

# Check if ports are in use
netstat -an | grep 9090
netstat -an | grep 5173

# Kill stuck processes on Windows
make stop

# Run specific Go test
go test -v -run TestName ./internal/store/...

# Check test coverage
make test-cover && open coverage.html
```

## Branch Strategy

**Trunk-based development on `main`.**
- Small, frequent commits directly to main
- Feature flags for incomplete work (not long-lived branches)
- `make check` must pass before any push

## Issue Tracking

**bd (beads)** is a lightweight, git-integrated issue tracker. Run `bd onboard` to get started.

## Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --status in_progress  # Claim work
bd close <id>         # Complete work
bd sync               # Sync with git
```

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - `make check` must pass
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd sync
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds

## Doc Hygiene

Keep README, CLI help, and config schema in sync. If a setting name or env var changes, update all three.
