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

## Doc Hygiene

Keep README, CLI help, and config schema in sync. If a setting name or env var changes, update all three.

### Project Stack
- **Backend:** Go
- **Frontend:** React (Vite dev server)
- **E2E Testing:** Playwright (via MCP server)
- **Issue Tracking:** bd (beads) - lightweight git-integrated issue tracker

---

## Code Style

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

## Philosophy

## Rule #1: Work Is Not Done Until User Confirms

**NEVER claim work is complete, fixed, or working until the user has tested and confirmed.**

- Don't say "Fixed!" or "Done!" after making changes
- Don't assume code works because it compiles/passes tests
- Always ask the user to verify before moving on
- If the user reports a problem, the fix isn't done yet

## Boy Scout Rule

Always leave code better than you found it. Every touch should improve:
- Clarity
- Simplicity
- Correctness

If you can't make it better, at least don't make it worse.

## Think Like an Architect

Before writing ANY code:
1. Design interfaces first - what's the contract?
2. Consider dependencies - what does this need? What needs this?
3. Plan for testing - how will this be verified?
4. Follow SOLID - single responsibility, open/closed, etc.
5. Write tests that prove it works - behavior, not implementation
6. THEN implement

## Never Be a Lazy Coder

- Don't mock everything - test real behavior
- Don't write tests that prove nothing - "it returns something" is useless
- Don't create god classes - split responsibilities
- Don't hard-code dependencies - inject them
- Don't skip edge cases - they're where bugs live
- Don't write code without designing first - thinking is not optional

## SOLID Principles

**Single Responsibility**: Each class/function does ONE thing. If you're writing "and" in the description, split it.

**Open/Closed**: Open for extension, closed for modification. Use strategies/plugins, not if/else chains.

**Liskov Substitution**: Subtypes must honor the base contract completely. No surprises.

**Interface Segregation**: Don't force clients to depend on methods they don't use. Small, focused interfaces.

**Dependency Inversion**: Depend on abstractions, not concretions. Inject dependencies, don't instantiate them.

## Core Principle: Delete More Than You Add

Before writing ANY fix, ask:
1. Why does this problem exist?
2. Is there code that should be deleted instead of code to add?
3. Am I fixing a symptom or the root cause?