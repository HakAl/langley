package provider

import "testing"

func TestMatchDomainSuffix(t *testing.T) {
	tests := []struct {
		host   string
		suffix string
		want   bool
	}{
		// Exact match
		{"anthropic.com", "anthropic.com", true},
		{"openai.com", "openai.com", true},

		// Subdomain match
		{"api.anthropic.com", "anthropic.com", true},
		{"api.claude.ai", "claude.ai", true},
		{"sub.api.openai.com", "openai.com", true},

		// Port stripping
		{"api.anthropic.com:443", "anthropic.com", true},
		{"anthropic.com:8080", "anthropic.com", true},
		{"openai.com:443", "openai.com", true},

		// Case insensitivity
		{"API.Anthropic.COM", "anthropic.com", true},
		{"api.OPENAI.com", "openai.com", true},

		// False positives that MUST NOT match
		{"misanthropic.com", "anthropic.com", false},
		{"misanthropic.io", "anthropic.com", false},
		{"notanthropic.com", "anthropic.com", false},
		{"claudesmith.com", "claude.ai", false},
		{"fakeclaude.ai.evil.com", "claude.ai", false},
		{"notopenai.com", "openai.com", false},
		{"myopenai.com", "openai.com", false},

		// Unrelated hosts
		{"github.com", "anthropic.com", false},
		{"example.com", "openai.com", false},
		{"pypi.org", "claude.ai", false},

		// Empty host
		{"", "anthropic.com", false},
		{"anthropic.com", "", false},
		{"", "", true}, // degenerate but consistent: "" == ""
	}

	for _, tt := range tests {
		t.Run(tt.host+"_"+tt.suffix, func(t *testing.T) {
			if got := MatchDomainSuffix(tt.host, tt.suffix); got != tt.want {
				t.Errorf("MatchDomainSuffix(%q, %q) = %v, want %v", tt.host, tt.suffix, got, tt.want)
			}
		})
	}
}
