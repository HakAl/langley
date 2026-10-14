// Package provider defines the interface for LLM API providers.
package provider

// Usage contains token counts extracted from a provider response.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
	Model               string
}

// Provider defines the interface for parsing LLM API responses.
type Provider interface {
	// Name returns the provider identifier (e.g., "anthropic", "openai").
	Name() string

	// DetectHost returns true if this provider handles the given host.
	DetectHost(host string) bool

	// ParseUsage extracts token usage from a response body.
	// For SSE responses, pass the complete accumulated body.
	ParseUsage(body []byte, isSSE bool) (*Usage, error)
}
