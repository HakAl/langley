// Package redact provides credential redaction for headers and bodies.
// This addresses langley-9qh (API keys stored in plaintext).
package redact

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/HakAl/langley/internal/config"
)

const (
	// RedactedValue is the replacement for redacted content.
	RedactedValue = "[REDACTED]"

	// RedactedImageValue is the replacement for redacted base64 images.
	RedactedImageValue = "[IMAGE base64 redacted]"

	// MaxRedactionInputSize is the maximum body size to attempt redaction on.
	// Bodies larger than this are returned as-is to avoid regex performance issues.
	// Security note: Very large bodies may contain secrets but regex on them is expensive.
	MaxRedactionInputSize = 1024 * 1024 // 1MB
)

// Redactor handles credential redaction.
type Redactor struct {
	cfg                   *config.RedactionConfig
	headerPatterns        []*regexp.Regexp
	apiKeyPattern         *regexp.Regexp
	base64Pattern         *regexp.Regexp
	jsonCredentialPattern *regexp.Regexp
}

// New creates a new Redactor with the given configuration.
func New(cfg *config.RedactionConfig) (*Redactor, error) {
	r := &Redactor{
		cfg: cfg,
	}

	// Compile header patterns
	for _, pattern := range cfg.PatternRedactHeaders {
		re, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			// Fallback to literal match if regex fails
			continue
		}
		r.headerPatterns = append(r.headerPatterns, re)
	}

	// API key patterns for multiple providers
	// Handles both plain and JSON-escaped strings (\" instead of ")
	// Patterns:
	// - Anthropic: sk-ant-... (Claude API)
	// - OpenAI: sk-... (OpenAI API)
	// - AWS: AKIA... (AWS access key)
	// - Google: AIza... (Google AI/Firebase)
	// - Generic: key-..., api_key=...
	r.apiKeyPattern = regexp.MustCompile(`(?i)(` +
		// Anthropic keys - sk-ant-api03-...
		`sk-ant-[a-zA-Z0-9_-]{20,}|` +
		// OpenAI keys - sk-...
		`sk-[a-zA-Z0-9_-]{20,}|` +
		// AWS access keys - AKIA followed by 16 alphanumeric
		`AKIA[0-9A-Z]{16}|` +
		// Google API keys - AIza followed by 35+ chars
		`AIza[0-9A-Za-z_-]{35,}|` +
		// Generic key patterns (with optional JSON escaping)
		`key-[a-zA-Z0-9_-]{20,}|` +
		// api_key/api-key in JSON with optional escaped quotes
		`api[_-]?key[=:]\\?"?[a-zA-Z0-9_-]{20,}` +
		`)`)

	// Base64 image pattern (data URLs and raw base64 in JSON)
	r.base64Pattern = regexp.MustCompile(`(?i)(data:image/[^;]+;base64,)[A-Za-z0-9+/=]{100,}|"(source|data)":\s*\{\s*"type":\s*"base64"[^}]*"data":\s*"[A-Za-z0-9+/=]{100,}"`)

	// JSON credential field patterns (2.2.13)
	// Matches: "password": "...", "secret": "...", "credential": "...", etc.
	// Also catches variations like "api_secret", "db_password", "user_credential"
	// Handles both regular and JSON-escaped quotes
	r.jsonCredentialPattern = regexp.MustCompile(`(?i)"([^"]*(?:password|secret|credential)[^"]*)":\s*"([^"\\]*(?:\\.[^"\\]*)*)"`)

	return r, nil
}

// RedactHeaders redacts sensitive headers in place.
// Returns a new header map with redacted values.
func (r *Redactor) RedactHeaders(h http.Header) http.Header {
	result := make(http.Header)

	for name, values := range h {
		if r.shouldRedactHeader(name) {
			result[name] = []string{RedactedValue}
		} else {
			result[name] = values
		}
	}

	return result
}

// shouldRedactHeader checks if a header name should be redacted.
func (r *Redactor) shouldRedactHeader(name string) bool {
	nameLower := strings.ToLower(name)

	// Check always-redact list
	for _, h := range r.cfg.AlwaysRedactHeaders {
		if strings.ToLower(h) == nameLower {
			return true
		}
	}

	// Check compiled patterns
	for _, pattern := range r.headerPatterns {
		if pattern.MatchString(name) {
			return true
		}
	}

	// Use config method for simple patterns
	if r.cfg.HeaderShouldRedact(name) {
		return true
	}

	return false
}

// RedactBody redacts sensitive content in a body string.
// Returns the redacted body.
// Bodies larger than MaxRedactionInputSize (1MB) are returned as-is
// to avoid regex performance issues on very large payloads.
func (r *Redactor) RedactBody(body string) string {
	// Skip redaction for very large bodies to avoid performance issues
	if len(body) > MaxRedactionInputSize {
		return body
	}

	result := body

	// Redact API keys
	if r.cfg.RedactAPIKeys {
		result = r.apiKeyPattern.ReplaceAllStringFunc(result, func(match string) string {
			matchLower := strings.ToLower(match)

			// Keep provider prefix for debugging context
			switch {
			case strings.HasPrefix(matchLower, "sk-ant-"):
				return "sk-ant-" + RedactedValue
			case strings.HasPrefix(matchLower, "sk-"):
				return "sk-" + RedactedValue
			case strings.HasPrefix(match, "AKIA"):
				return "AKIA" + RedactedValue
			case strings.HasPrefix(match, "AIza"):
				return "AIza" + RedactedValue
			case strings.HasPrefix(matchLower, "key-"):
				return "key-" + RedactedValue
			}

			// For api_key=... patterns, keep the structure
			parts := strings.SplitN(match, "=", 2)
			if len(parts) == 2 {
				return parts[0] + "=" + RedactedValue
			}
			parts = strings.SplitN(match, ":", 2)
			if len(parts) == 2 {
				return parts[0] + ":" + RedactedValue
			}
			return RedactedValue
		})
	}

	// Redact base64 images
	if r.cfg.RedactBase64Images {
		result = r.base64Pattern.ReplaceAllStringFunc(result, func(match string) string {
			// Preserve data URL prefix if present
			if strings.HasPrefix(strings.ToLower(match), "data:image") {
				idx := strings.Index(match, ",")
				if idx > 0 {
					return match[:idx+1] + RedactedImageValue
				}
			}
			// For JSON structures, indicate image was redacted
			if strings.Contains(match, `"base64"`) {
				return RedactedImageValue
			}
			return RedactedImageValue
		})
	}

	// Redact JSON credential fields (2.2.13)
	// Matches "password", "secret", "credential" keys and redacts their values
	if r.cfg.RedactAPIKeys { // Use same config flag as API keys
		result = r.jsonCredentialPattern.ReplaceAllStringFunc(result, func(match string) string {
			// Find the colon and value portion
			colonIdx := strings.Index(match, ":")
			if colonIdx > 0 {
				keyPart := match[:colonIdx+1] // Keep the key name and colon
				return keyPart + ` "` + RedactedValue + `"`
			}
			return match
		})
	}

	return result
}

// RedactBodyBytes redacts sensitive content in a body.
// Returns the redacted body as bytes.
func (r *Redactor) RedactBodyBytes(body []byte) []byte {
	return []byte(r.RedactBody(string(body)))
}

// ShouldStoreRawBody returns whether raw body storage is enabled.
// This is OFF by default for security (addresses langley-oy9).
func (r *Redactor) ShouldStoreRawBody() bool {
	return r.cfg.RawBodyStorage
}

// HeadersToMap converts http.Header to a map for JSON serialization.
func HeadersToMap(h http.Header) map[string][]string {
	result := make(map[string][]string, len(h))
	for k, v := range h {
		result[k] = v
	}
	return result
}

// HeadersFromMap converts a map back to http.Header.
func HeadersFromMap(m map[string][]string) http.Header {
	result := make(http.Header, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}
