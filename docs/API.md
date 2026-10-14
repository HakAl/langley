## API

All endpoints require `Authorization: Bearer <token>`. Rate limited to 20 req/sec sustained, 100 burst.

### Flows

| Endpoint | Description |
|----------|-------------|
| `GET /api/flows` | List flows. Params: `limit`, `host`, `task_id`, `model` |
| `GET /api/flows/{id}` | Single flow with full detail |
| `GET /api/flows/{id}/events` | SSE events for a streaming flow |
| `GET /api/flows/{id}/anomalies` | Anomalies linked to a flow |
| `GET /api/flows/export` | Export. Params: `format` (ndjson/json/csv), `max_rows`, `include_bodies` |
| `GET /api/flows/count` | Count flows matching filters |

### Analytics

| Endpoint | Description |
|----------|-------------|
| `GET /api/stats` | Overall statistics |
| `GET /api/analytics/tasks` | Per-task summaries |
| `GET /api/analytics/tasks/{id}` | Single task detail |
| `GET /api/analytics/tools` | Tool invocation stats |
| `GET /api/analytics/tools/{name}/invocations` | Individual invocations for a tool. Params: `start`, `end`, `limit`, `offset` |
| `GET /api/analytics/tool-invocations/{id}` | Single tool invocation detail (input, result, duration) |
| `GET /api/analytics/cost/daily` | Daily cost breakdown |
| `GET /api/analytics/cost/model` | Cost by model |
| `GET /api/analytics/anomalies` | Recent anomalies |

### System

| Endpoint | Description |
|----------|-------------|
| `GET /api/health` | Health check (no auth required) |
| `GET /api/settings` | Current settings |
| `PUT /api/settings` | Update settings |
| `WS /ws` | Real-time flow updates. Auth via `token` query param. |

Full API spec in `openapi.yaml`.