## ARCHITECTURAL PRINCIPLES (READ THIS FIRST)

**Boy scout rule: always leave code better than you found it.**

### Project Stack
- **Backend:** Go
- **Frontend:** React (Vite dev server)
- **E2E Testing:** Playwright (via MCP server)
- **Issue Tracking:** bd (beads) - lightweight git-integrated issue tracker

### You Are an Architect, Not a Code Monkey

Before writing ANY code, you must:
1. **Design the abstraction** - What protocol/interface is needed?
2. **Consider SOLID principles** - Is this following Single Responsibility, Open/Closed, Liskov Substitution, Interface Segregation, Dependency Inversion?
3. **Plan dependency injection** - How will this be tested? What needs to be injected?
4. **Think about edge cases** - What can go wrong? What are the boundaries?
5. **Design before coding** - No coding until the design is clear

### MANDATORY: Protocol-First Design

**NEVER write a concrete class without defining its protocol first.**

**Why?** Protocols enable:
- Testing with test doubles
- Swapping implementations
- Dependency inversion
- Clear contracts

### Protocol Rule (Go Backend)

In Go, define a minimal `type X interface { ... }` in the package before any concrete implementation.
Interfaces must be small and specific (ISP); prefer multiple tiny interfaces over one big one.

---

## SOLID PRINCIPLES (NON-NEGOTIABLE)

### Single Responsibility Principle
**Each class should have ONE reason to change.**

### Open/Closed Principle
**Open for extension, closed for modification.**

Use strategy pattern, not if/else chains.

### Liskov Substitution Principle
**Subtypes must be substitutable for their base types.**

If you inherit from a class or implement a protocol, you must honor the contract completely.

### Interface Segregation Principle
**Don't force clients to depend on interfaces they don't use.**

### Dependency Inversion Principle
**Depend on abstractions, not concretions.**

---

## DEPENDENCY INJECTION (MANDATORY)

### The Rule: ALL Dependencies MUST Be Injected

**NO direct instantiation of dependencies in class bodies.**

### Constructor Rules

1. **NO side effects** - Constructors assign dependencies only
2. **NO business logic** - Move logic to explicit methods
3. **NO I/O operations** - No file reads, no network calls
4. **NO auto-registration** - Explicit is better than implicit
5. **Provide defaults with factory functions** - `NewFoo()` creates with defaults, `NewFooWith(deps)` for injection

### Dependency Injection: Required Abstractions (Go)

These core components MUST be injected via interfaces, not concrete types:
- `Proxy` - request forwarding
- `Store` - data persistence
- `Redactor` - sensitive data masking
- `AnalyticsEngine` - metrics/telemetry
- `TaskAssigner` - work distribution
- `Provider` - external service adapters
- `CertSource` - TLS certificate management
- `EventQueue` - async event handling

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

**Am I mocking appropriately?**
- Only external dependencies (APIs, file system, network)?
- Using real objects for business logic?
- Using interface-based test doubles?

### TEST ISOLATION (CRITICAL)

**NEVER MAKE REAL API CALLS IN TESTS. EVER.**
**TEST BEHAVIOR THROUGH MOCKS NOT REAL APIS**

### Backend Tests (Go)

For any change that touches concurrency or shared state, run:
```bash
go test -race ./...
```

### Frontend Tests (React)

Use Playwright MCP server for E2E verification of the dashboard UI.
Unit tests use Vitest for component logic.

# Agent Instructions

## Building & Running

Use `make` (via Git Bash on Windows). Install make if needed: `choco install make` or use MinGW.

```bash
make help          # Show all targets
make install-deps  # Install Go + npm dependencies
make dev           # Run dev servers (backend + frontend hot reload)
make build         # Build production binary
make test          # Run all tests (with -race flag)
make check         # Lint + test (quality gate)
make clean         # Remove build artifacts
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
make check         # Must pass
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
