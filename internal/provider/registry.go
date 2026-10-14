package provider

// Registry holds registered providers and selects by host.
type Registry struct {
	providers []Provider
}

// NewRegistry creates a registry with all known providers.
func NewRegistry() *Registry {
	return &Registry{
		providers: []Provider{
			&Anthropic{},
			&OpenAI{},
			&Bedrock{},
			&Gemini{},
		},
	}
}

// Detect returns the provider for a given host, or nil if unknown.
func (r *Registry) Detect(host string) Provider {
	for _, p := range r.providers {
		if p.DetectHost(host) {
			return p
		}
	}
	return nil
}

// Get returns a provider by name, or nil if not found.
func (r *Registry) Get(name string) Provider {
	for _, p := range r.providers {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

// ShouldIntercept returns true if any registered provider recognises the host.
// This is the interception gate: it answers "should we MITM this host?" without
// selecting a specific provider (use Detect for that).
func (r *Registry) ShouldIntercept(host string) bool {
	return r.Detect(host) != nil
}
