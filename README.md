# Langley

An LLM traffic proxy with persistence and analytics. Intercept, capture, and analyze Claude API traffic.

## Features

- **TLS Interception** - MITM proxy with dynamic certificate generation
- **Credential Redaction** - API keys and sensitive headers redacted before storage
- **SQLite Persistence** - WAL mode for performance, with TTL-based retention
- **Priority Queue** - Backpressure handling for high-volume traffic
- **Security First** - Upstream TLS validation, secure file permissions, auth tokens

## Quick Start

```bash
# Build
go build ./cmd/langley

# Run
./langley

# Show CA certificate path
./langley --show-ca
```

## Configuration

Langley looks for configuration at:
- **Linux/macOS**: `~/.config/langley/config.yaml`
- **Windows**: `%APPDATA%\langley\config.yaml`

See `langley.example.yaml` for all options.

## Trusting the CA

To intercept HTTPS traffic, you need to trust Langley's CA certificate:

```bash
# macOS
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain ~/.config/langley/certs/ca.crt

# Linux
sudo cp ~/.config/langley/certs/ca.crt /usr/local/share/ca-certificates/langley.crt
sudo update-ca-certificates

# Windows (run as Administrator)
certutil -addstore -f "ROOT" %APPDATA%\langley\certs\ca.crt
```

## Using with Claude

Configure Claude Code to use the proxy:

```bash
export HTTPS_PROXY=http://localhost:9090
export HTTP_PROXY=http://localhost:9090
```

Or configure globally in your shell profile.

## Security

- **Credential Redaction**: API keys, auth headers, and sensitive patterns are redacted before storage
- **TLS Validation**: Upstream TLS certificates are validated by default
- **File Permissions**: CA private key and config files use restrictive permissions
- **Auth Tokens**: API endpoints require bearer token authentication

## Status

MVP in development. See `PLAN.md` for roadmap.

## License

MIT
