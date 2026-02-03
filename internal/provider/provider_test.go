package provider

import (
	"testing"
)

func TestRegistry_Detect(t *testing.T) {
	r := NewRegistry()

	tests := []struct {
		host     string
		wantName string
	}{
		{"api.anthropic.com", "anthropic"},
		{"claude.ai", "anthropic"},
		{"api.openai.com", "openai"},
		{"bedrock-runtime.us-east-1.amazonaws.com", "bedrock"},
		{"generativelanguage.googleapis.com", "gemini"},
		{"example.com", ""},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			p := r.Detect(tt.host)
			if tt.wantName == "" {
				if p != nil {
					t.Errorf("Detect(%q) = %q, want nil", tt.host, p.Name())
				}
			} else {
				if p == nil {
					t.Errorf("Detect(%q) = nil, want %q", tt.host, tt.wantName)
				} else if p.Name() != tt.wantName {
					t.Errorf("Detect(%q).Name() = %q, want %q", tt.host, p.Name(), tt.wantName)
				}
			}
		})
	}
}

func TestRegistry_ShouldIntercept(t *testing.T) {
	r := NewRegistry()

	tests := []struct {
		host string
		want bool
	}{
		// Known LLM hosts
		{"api.anthropic.com", true},
		{"claude.ai", true},
		{"api.openai.com", true},
		{"bedrock-runtime.us-east-1.amazonaws.com", true},
		{"generativelanguage.googleapis.com", true},
		{"api.anthropic.com:443", true},

		// Non-LLM hosts — must NOT intercept
		{"github.com", false},
		{"pypi.org", false},
		{"registry.npmjs.org", false},
		{"misanthropic.io", false},
		{"example.com", false},

		// Crafted hosts that MUST NOT match (domain boundary safety)
		{"generativelanguage.googleapis.com.evil.com", false},
		{"bedrock-runtime.evil-amazonaws.com", false},
		{"bedrock-runtime.us-east-1.amazonaws.com.evil.com", false},
		{"fakegenerativelanguage.googleapis.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := r.ShouldIntercept(tt.host); got != tt.want {
				t.Errorf("ShouldIntercept(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestRegistry_Get(t *testing.T) {
	r := NewRegistry()

	tests := []struct {
		name    string
		wantNil bool
	}{
		{"anthropic", false},
		{"openai", false},
		{"bedrock", false},
		{"gemini", false},
		{"unknown", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := r.Get(tt.name)
			if tt.wantNil && p != nil {
				t.Errorf("Get(%q) = %v, want nil", tt.name, p)
			}
			if !tt.wantNil && p == nil {
				t.Errorf("Get(%q) = nil, want non-nil", tt.name)
			}
		})
	}
}
