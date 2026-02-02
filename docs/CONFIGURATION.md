## Configuration

YAML config with environment variable overrides. Config file locations:

- **Unix**: `~/.config/langley/langley.yaml`
- **Windows**: `%APPDATA%\langley\langley.yaml`

```yaml
proxy:
  listen: "localhost:9090"

auth:
  token: "your-secret-token"  # Auto-generated if not set

persistence:
  body_max_bytes: 1048576     # 1MB max body storage per flow

redaction:
  always_redact_headers:
    - authorization
    - x-api-key
    - cookie
    - set-cookie
  pattern_redact_headers:
    - "^x-.*-token$"
    - "^x-.*-key$"
  redact_api_keys: true       # Masks sk-*, AKIA*, AIza* patterns
  redact_base64_images: true  # Replaces images with placeholders
  disable_body_storage: false  # Set to true to stop storing bodies

retention:
  flows_ttl_days: 30
  events_ttl_days: 7
  drop_log_ttl_days: 1

analytics:
  anomaly_context_tokens: 100000
  anomaly_tool_delay_ms: 30000
  anomaly_rapid_calls_window_s: 10
  anomaly_rapid_calls_threshold: 5
```

See `langley.example.yaml` for the full annotated config.

### Environment Variables

| Variable | Overrides |
|----------|-----------|
| `LANGLEY_LISTEN` | `proxy.listen` |
| `LANGLEY_AUTH_TOKEN` | `auth.token` |
| `LANGLEY_DB_PATH` | `persistence.db_path` |

Relative paths in `LANGLEY_DB_PATH` resolve from the working directory. Use absolute paths when running as a service.