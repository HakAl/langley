// Package redact provides credential redaction for headers and bodies.
// This addresses langley-9qh (API keys stored in plaintext).
package redact

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/anthropics/langley/internal/config"
)

const (
	// RedactedValue is the replacement for redacted content.
	RedactedValue = "[REDACTED]"

	// RedactedImageValue is the replacement for redacted base64 images.
	RedactedImageValue = "[IMAGE base64 redacted]"
)

// Redactor handles credential redaction.
type Redactor struct {
	cfg            *config.RedactionConfig
	headerPatterns []*regexp.Regexp
	apiKeyPattern  *regexp.Regexp
	base64Pattern  *regexp.Regexp
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

	// API key pattern: sk-..., key-..., etc.
	// Matches common API key formats
	r.apiKeyPattern = regexp.MustCompile(`(?i)(sk-[a-zA-Z0-9_-]{20,}|key-[a-zA-Z0-9_-]{20,}|api[_-]?key[=:]["']?[a-zA-Z0-9_-]{20,})`)

	// Base64 image pattern (data URLs and raw base64 in JSON)
	r.base64Pattern = regexp.MustCompile(`(?i)(data:image/[^;]+;base64,)[A-Za-z0-9+/=]{100,}|"(source|data)":\s*\{\s*"type":\s*"base64"[^}]*"data":\s*"[A-Za-z0-9+/=]{100,}"`)

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
func (r *Redactor) RedactBody(body string) string {
	result := body

	// Redact API keys
	if r.cfg.RedactAPIKeys {
		result = r.apiKeyPattern.ReplaceAllStringFunc(result, func(match string) string {
			// Keep the prefix for context, redact the actual key
			if strings.Contains(strings.ToLower(match), "sk-") {
				return "sk-" + RedactedValue
			}
			if strings.Contains(strings.ToLower(match), "key-") {
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
