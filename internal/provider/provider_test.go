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

func TestRegistry_Get(t *testing.T) {
	r := NewRegistry()

	tests := []struct {
		name    string
		wantNil bool
	}{
		{"anthropic", false},
		{"openai", false},
		{"bedrock", false},
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
