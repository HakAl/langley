# Langley MVP Implementation Plan

**Status**: Draft - Pending Team Review
**Created**: 2026-01-24
**Tech Stack**: Go + SQLite + React

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                         Langley                                  │
├─────────────────────────────────────────────────────────────────┤
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────────────┐ │
│  │ HTTP Proxy  │───▶│ SSE Parser  │───▶│ Priority Queue      │ │
│  │ (TLS MITM)  │    │             │    │ HIGH/MED/LOW        │ │
│  └─────────────┘    └─────────────┘    └──────────┬──────────┘ │
│                                                    │            │
│  ┌─────────────┐    ┌─────────────┐    ┌──────────▼──────────┐ │
│  │ WebSocket   │◀───│ Memory Mgr  │◀───│ Async Writer        │ │
│  │ Broadcast   │    │ (LRU)       │    │ (Batched)           │ │
│  └─────────────┘    └─────────────┘    └──────────┬──────────┘ │
│                                                    │            │
│  ┌─────────────┐    ┌─────────────┐    ┌──────────▼──────────┐ │
│  │ REST API    │───▶│ Analytics   │◀───│ SQLite              │ │
│  │ (Auth)      │    │ Engine      │    │ (WAL mode)          │ │
│  └─────────────┘    └─────────────┘    └─────────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                    React Frontend                                │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────────────┐ │
│  │ Flow List   │    │ Detail View │    │ Analytics Dashboard │ │
│  └─────────────┘    └─────────────┘    └─────────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
```

## Deferred (Post-MVP)

- uTLS fingerprint spoofing (use standard Go TLS for MVP)
- Bedrock pricing/provider abstraction
- Structural signature hashing for retry detection
- Complex anomaly detection (query ad-hoc first)

## Test Strategy

| Layer | Approach |
|-------|----------|
| Config | Unit tests with temp files |
| Redaction | Unit tests with known patterns |
| SQLite | Integration tests with in-memory DB |
| Queue | Unit tests for priority/backpressure |
| Proxy | Integration tests with httptest server |
| API | Integration tests with httptest |
| WebSocket | Integration tests with gorilla/websocket client |
| E2E | Playwright for frontend |

Focus on behavior, not implementation. Mock external dependencies only.

## Phases

### Phase 0: Scaffolding

**Goal**: Project structure, dependencies, "hello world" proxy

| Task | Description |
|------|-------------|
| 0.1 Go module | `go mod init`, directory structure |
| 0.2 Dependencies | SQLite, config, logging, HTTP |
| 0.3 Hello proxy | Basic HTTP proxy that forwards requests |

**Deliverable**: `go run ./cmd/langley` starts a working (non-intercepting) proxy

### Phase 1: Foundation (Core Proxy + Persistence)

**Goal**: Working proxy that captures and persists Claude traffic

| Task | Description | Security |
|------|-------------|----------|
| 1.1 Project setup | Go module, directory structure, config loading | - |
| 1.2 Config system | YAML + CLI + env layering, secure defaults | CA key protection, token generation |
| 1.3 TLS infrastructure | CA generation, cert cache (LRU), dynamic certs | Random serials, file permissions |
| 1.4 HTTP proxy | CONNECT handler, TLS MITM, request/response capture | Upstream TLS validation ON |
| 1.5 Redaction layer | Headers/bodies redacted before storage | No credential logging |
| 1.6 SQLite persistence | Schema, migrations, WAL mode, async writer | - |
| 1.7 Priority queue | Bounded queue, event prioritization, backpressure | Drop counters |

**Deliverable**: `langley serve` captures Claude traffic to SQLite

### Phase 2: Real-time, API & Basic UI

**Goal**: WebSocket broadcast + REST API + minimal frontend to validate

| Task | Description | Security |
|------|-------------|----------|
| 2.1 Memory manager | LRU for flows/events, persistence ack before evict | - |
| 2.2 WebSocket server | Real-time flow/event broadcast | Origin validation |
| 2.3 REST API | Flows, events endpoints | Bearer token auth |
| 2.4 SSE parser | Claude-specific event parsing, tool extraction | - |
| 2.5 Task assignment | Explicit > metadata > heuristic grouping | - |
| 2.6 Basic frontend | Flow list with WebSocket updates (validates API shape) | - |

**Deliverable**: Working flow list UI with real-time updates

### Phase 3: Analytics Engine

**Goal**: Cost tracking, metrics, anomaly detection

| Task | Description |
|------|-------------|
| 3.1 Token/cost extraction | Parse usage from responses, pricing lookup |
| 3.2 Tool analytics | Extract tool invocations, track success/failure |
| 3.3 Task aggregation | Cost/turns/duration per task |
| 3.4 Anomaly flags | Large context, slow tools, rapid repeats |

**Deliverable**: Analytics queries work, data populated

### Phase 4: Frontend Polish

**Goal**: Complete the React dashboard (basic flow list from Phase 2)

| Task | Description |
|------|-------------|
| 4.1 Detail view | Request/response/events, expandable sections |
| 4.2 Filtering | Filter by host, task, time, status |
| 4.3 Analytics dashboard | Cost charts, tool usage, task breakdown |
| 4.4 Anomaly alerts | Visual indicators for flagged flows |

**Deliverable**: Full dashboard with analytics

### Phase 5: Polish & Ship

**Goal**: Production-ready MVP

| Task | Description |
|------|-------------|
| 5.1 Graceful shutdown | Drain queue, flush batches, clean close |
| 5.2 Retention cleanup | TTL enforcement, VACUUM scheduling |
| 5.3 CLI polish | Help text, version, status commands |
| 5.4 Documentation | README, setup guide, config reference |
| 5.5 Blog post | Document the build, lessons learned |

**Deliverable**: Shippable MVP + blog

## File Structure

```
langley/
├── cmd/
│   └── langley/
│       └── main.go           # CLI entrypoint
├── internal/
│   ├── config/               # Configuration loading
│   ├── proxy/                # HTTP/TLS proxy
│   ├── tls/                  # Certificate management
│   ├── parser/               # SSE/Claude parsing
│   ├── redact/               # Credential redaction
│   ├── queue/                # Priority queue
│   ├── store/                # SQLite persistence
│   ├── memory/               # LRU memory manager
│   ├── api/                  # REST API
│   ├── ws/                   # WebSocket server
│   ├── analytics/            # Analytics engine
│   └── task/                 # Task assignment
├── migrations/               # SQL migrations
├── web/                      # React frontend
│   ├── src/
│   └── ...
├── langley.example.yaml      # Example config
├── go.mod
└── go.sum
```

## Security Checklist (From Matt's Analysis)

Must address from Phase 1:

- [ ] Credential redaction on-write (langley-9qh)
- [ ] CA key file permissions (langley-xpi)
- [ ] Upstream TLS validation ON by default (langley-vu5)
- [ ] Bearer token auth on API (langley-yni)
- [ ] Socket permissions (langley-1ug)
- [ ] Random certificate serials (langley-12r)
- [ ] LRU cert cache with eviction (langley-bma)
- [ ] Streaming request bodies with limits (langley-4vz)
- [ ] Raw body storage default OFF (langley-oy9)

## Progress Tracking

Use `bd` for issue tracking. Each task becomes a bead when started.

### Session 1 (2026-01-24)

**Phase 0: Scaffolding** - COMPLETE
- [x] Go module initialized
- [x] Directory structure created
- [x] Basic HTTP proxy working

**Phase 1: Foundation** - IN PROGRESS
- [x] Config system with YAML/CLI/env layering
- [x] TLS infrastructure (CA generation, cert cache with LRU)
- [x] MITM proxy with TLS interception
- [x] Redaction layer (headers, API keys, base64 images)
- [x] SQLite persistence with migrations
- [x] Priority queue (code complete, not yet integrated)
- [ ] SSE parser for Claude events
- [ ] Task assignment (explicit > metadata > heuristic)
- [ ] Async writer with batching

**Security Addressed:**
- [x] langley-9qh: Credential redaction on-write
- [x] langley-xpi: CA key file permissions (0600)
- [x] langley-vu5: Upstream TLS validation ON by default
- [x] langley-12r: Random certificate serials
- [x] langley-bma: LRU cert cache with max size
- [x] langley-4vz: Request body limits (configurable max)
- [x] langley-oy9: Raw body storage default OFF
- [ ] langley-yni: Bearer token auth on API (Phase 2)
- [ ] langley-1ug: Socket permissions (not applicable on Windows)
- [ ] langley-awl: CA validity reduced to 2 years

## Notes

- Clancy code is reference only - don't copy, learn from
- Schema from SCHEMA.md is the spec
- REVIEW.md clarifications override APP.md
