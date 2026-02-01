package redact

import (
	"net/http"
	"strings"
	"testing"

	"github.com/HakAl/langley/internal/config"
)

// testConfig returns a RedactionConfig with all redaction enabled.
func testConfig() *config.RedactionConfig {
	return &config.RedactionConfig{
		AlwaysRedactHeaders: []string{
			"authorization",
			"x-api-key",
			"api-key",
			"x-amz-security-token",
		},
		PatternRedactHeaders: []string{
			".*secret.*",
			".*token.*",
		},
		RedactAPIKeys:      true,
		RedactBase64Images: true,
		DisableBodyStorage: false,
	}
}

func TestNew(t *testing.T) {
	cfg := testConfig()
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if r == nil {
		t.Fatal("New() returned nil")
	}
}

// TestRedactHeaders verifies header redaction works for various header names.
func TestRedactHeaders(t *testing.T) {
	r, _ := New(testConfig())

	tests := []struct {
		name       string
		headers    http.Header
		wantRedact []string // headers that should be redacted
		wantKeep   []string // headers that should keep their value
	}{
		{
			name: "authorization header",
			headers: http.Header{
				"Authorization": []string{"Bearer sk-ant-api03-xxx"},
			},
			wantRedact: []string{"Authorization"},
		},
		{
			name: "x-api-key header",
			headers: http.Header{
				"X-Api-Key": []string{"sk-1234567890abcdef"},
			},
			wantRedact: []string{"X-Api-Key"},
		},
		{
			name: "case insensitive",
			headers: http.Header{
				"Authorization": []string{"Bearer token"}, // Go canonicalizes header names
				"X-Api-Key":     []string{"secret"},
			},
			wantRedact: []string{"Authorization", "X-Api-Key"},
		},
		{
			name: "pattern match secret",
			headers: http.Header{
				"X-My-Secret-Key": []string{"sensitive"},
				"Content-Type":    []string{"application/json"},
			},
			wantRedact: []string{"X-My-Secret-Key"},
			wantKeep:   []string{"Content-Type"},
		},
		{
			name: "aws security token",
			headers: http.Header{
				"X-Amz-Security-Token": []string{"FwoGZXIvYXdzEBYaD..."},
			},
			wantRedact: []string{"X-Amz-Security-Token"},
		},
		{
			name: "safe headers preserved",
			headers: http.Header{
				"Content-Type":   []string{"application/json"},
				"Accept":         []string{"*/*"},
				"User-Agent":     []string{"langley/1.0"},
				"Content-Length": []string{"1234"},
			},
			wantKeep: []string{"Content-Type", "Accept", "User-Agent", "Content-Length"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := r.RedactHeaders(tt.headers)

			for _, h := range tt.wantRedact {
				if result.Get(h) != RedactedValue {
					t.Errorf("header %q = %q, want %q", h, result.Get(h), RedactedValue)
				}
			}

			for _, h := range tt.wantKeep {
				orig := tt.headers.Get(h)
				if result.Get(h) != orig {
					t.Errorf("header %q = %q, want original %q", h, result.Get(h), orig)
				}
			}
		})
	}
}

// TestRedactAnthropicKeys verifies Anthropic API key patterns are redacted.
func TestRedactAnthropicKeys(t *testing.T) {
	r, _ := New(testConfig())

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "sk-ant key in plain text",
			input: `{"api_key": "sk-ant-api03-abcdefghijklmnopqrstuvwxyz1234567890"}`,
			want:  `{"api_key": "sk-ant-[REDACTED]"}`,
		},
		{
			name:  "sk-ant key mid-string",
			input: `Authorization: Bearer sk-ant-api03-abcdefghijklmnopqrstuvwxyz`,
			want:  `Authorization: Bearer sk-ant-[REDACTED]`,
		},
		{
			name:  "multiple sk-ant keys",
			input: `key1=sk-ant-api03-aaaaaaaaaaaaaaaaaaaa key2=sk-ant-api03-bbbbbbbbbbbbbbbbbbbb`,
			want:  `key1=sk-ant-[REDACTED] key2=sk-ant-[REDACTED]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.RedactBody(tt.input)
			if got != tt.want {
				t.Errorf("RedactBody() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRedactOpenAIKeys verifies OpenAI API key patterns are redacted.
func TestRedactOpenAIKeys(t *testing.T) {
	r, _ := New(testConfig())

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "sk- key basic",
			input: `{"api_key": "sk-abcdefghijklmnopqrstuvwxyz1234567890"}`,
			want:  `{"api_key": "sk-[REDACTED]"}`,
		},
		{
			name:  "sk-proj key",
			input: `token: sk-proj-abcdefghijklmnopqrstuvwxyz1234`,
			want:  `token: sk-[REDACTED]`,
		},
		{
			name:  "sk-svcacct key",
			input: `export OPENAI_KEY=sk-svcacct-abcdefghijklmnopqrstuvwxyz`,
			want:  `export OPENAI_KEY=sk-[REDACTED]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.RedactBody(tt.input)
			if got != tt.want {
				t.Errorf("RedactBody() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRedactAWSCredentials verifies AWS credential patterns are redacted.
func TestRedactAWSCredentials(t *testing.T) {
	r, _ := New(testConfig())

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "AWS access key ID",
			input: `aws_access_key_id = AKIAIOSFODNN7EXAMPLE`,
			want:  `aws_access_key_id = AKIA[REDACTED]`,
		},
		{
			name:  "AWS key in JSON",
			input: `{"accessKeyId": "AKIAI44QH8DHBEXAMPLE", "region": "us-east-1"}`,
			want:  `{"accessKeyId": "AKIA[REDACTED]", "region": "us-east-1"}`,
		},
		{
			name:  "multiple AWS keys",
			input: `key1=AKIAXXXXXXXXXXXXXXXX key2=AKIAYYYYYYYYYYYYYYYY`,
			want:  `key1=AKIA[REDACTED] key2=AKIA[REDACTED]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.RedactBody(tt.input)
			if got != tt.want {
				t.Errorf("RedactBody() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRedactGeminiKeys verifies Google/Gemini API key patterns are redacted.
func TestRedactGeminiKeys(t *testing.T) {
	r, _ := New(testConfig())

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "AIza key in text",
			input: `gemini_token=AIzaSyA1234567890abcdefghijklmnopqrstuv`,
			want:  `gemini_token=AIza[REDACTED]`,
		},
		{
			name:  "AIza key in JSON",
			input: `{"gemini": "AIzaSyBcdefghijklmnopqrstuvwxyz12345678"}`,
			want:  `{"gemini": "AIza[REDACTED]"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.RedactBody(tt.input)
			if got != tt.want {
				t.Errorf("RedactBody() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRedactJSONCredentialFields verifies credential fields are redacted (2.2.13).
func TestRedactJSONCredentialFields(t *testing.T) {
	r, _ := New(testConfig())

	tests := []struct {
		name       string
		input      string
		wantRedact string // substring that should NOT appear
	}{
		{
			name:       "password field",
			input:      `{"username": "admin", "password": "supersecret123"}`,
			wantRedact: "supersecret123",
		},
		{
			name:       "secret field",
			input:      `{"api_secret": "myverysecretvalue", "id": "123"}`,
			wantRedact: "myverysecretvalue",
		},
		{
			name:       "credential field",
			input:      `{"user_credential": "abc123xyz", "type": "oauth"}`,
			wantRedact: "abc123xyz",
		},
		{
			name:       "db_password variant",
			input:      `{"db_password": "dbpass456", "host": "localhost"}`,
			wantRedact: "dbpass456",
		},
		{
			name:       "client_secret variant",
			input:      `{"client_id": "app1", "client_secret": "clientsecretvalue"}`,
			wantRedact: "clientsecretvalue",
		},
		{
			name:       "multiple credential fields",
			input:      `{"password": "pass1", "api_secret": "secret1", "db_credential": "cred1"}`,
			wantRedact: "pass1",
		},
		{
			name:       "preserves non-credential fields",
			input:      `{"password": "secret", "username": "admin", "server": "localhost"}`,
			wantRedact: "secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.RedactBody(tt.input)
			if strings.Contains(got, tt.wantRedact) {
				t.Errorf("RedactBody() = %q, should not contain %q", got, tt.wantRedact)
			}
			// Verify [REDACTED] appears
			if !strings.Contains(got, RedactedValue) {
				t.Errorf("RedactBody() = %q, should contain %q", got, RedactedValue)
			}
		})
	}

	// Test that non-credential fields are preserved
	t.Run("non-credential fields preserved", func(t *testing.T) {
		input := `{"password": "secret", "username": "admin", "server": "localhost"}`
		got := r.RedactBody(input)
		if !strings.Contains(got, `"username": "admin"`) {
			t.Errorf("username field was incorrectly modified: %s", got)
		}
		if !strings.Contains(got, `"server": "localhost"`) {
			t.Errorf("server field was incorrectly modified: %s", got)
		}
	})
}

// TestRedactBase64Images verifies base64 image data is redacted.
func TestRedactBase64Images(t *testing.T) {
	r, _ := New(testConfig())

	// Generate a fake base64 string (100+ chars)
	fakeBase64 := strings.Repeat("ABCDEFGHabcdefgh12345678", 10)

	tests := []struct {
		name        string
		input       string
		wantContain string
	}{
		{
			name:        "data URL image",
			input:       `<img src="data:image/png;base64,` + fakeBase64 + `">`,
			wantContain: RedactedImageValue,
		},
		{
			name:        "data URL in JSON",
			input:       `{"image": "data:image/jpeg;base64,` + fakeBase64 + `"}`,
			wantContain: RedactedImageValue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.RedactBody(tt.input)
			if !strings.Contains(got, tt.wantContain) {
				t.Errorf("RedactBody() = %q, want to contain %q", got, tt.wantContain)
			}
			// Should not contain the original base64 data
			if strings.Contains(got, fakeBase64) {
				t.Errorf("RedactBody() still contains original base64 data")
			}
		})
	}
}

// TestRedactBodyDisabled verifies redaction can be disabled.
func TestRedactBodyDisabled(t *testing.T) {
	cfg := &config.RedactionConfig{
		RedactAPIKeys:      false,
		RedactBase64Images: false,
	}
	r, _ := New(cfg)

	input := `{"key": "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"}`
	got := r.RedactBody(input)

	if got != input {
		t.Errorf("RedactBody() with disabled redaction = %q, want original %q", got, input)
	}
}

// TestRedactBodyPreservesStructure verifies JSON structure is preserved.
func TestRedactBodyPreservesStructure(t *testing.T) {
	r, _ := New(testConfig())

	input := `{
		"model": "claude-3-opus",
		"api_key": "sk-ant-api03-abcdefghijklmnopqrstuvwxyz",
		"messages": [
			{"role": "user", "content": "Hello"}
		]
	}`

	got := r.RedactBody(input)

	// Should still be valid-ish JSON structure
	if !strings.Contains(got, `"model": "claude-3-opus"`) {
		t.Error("RedactBody() modified non-sensitive field 'model'")
	}
	if !strings.Contains(got, `"messages"`) {
		t.Error("RedactBody() modified non-sensitive field 'messages'")
	}
	if strings.Contains(got, "abcdefghijklmnopqrstuvwxyz") {
		t.Error("RedactBody() did not redact API key")
	}
}

// TestRedactBodyBytes verifies byte slice redaction works.
func TestRedactBodyBytes(t *testing.T) {
	r, _ := New(testConfig())

	input := []byte(`key=sk-ant-api03-abcdefghijklmnopqrstuvwxyz`)
	got := r.RedactBodyBytes(input)

	if strings.Contains(string(got), "abcdefghijklmnopqrstuvwxyz") {
		t.Error("RedactBodyBytes() did not redact API key")
	}
}

// TestShouldStoreBody verifies body storage config.
func TestShouldStoreBody(t *testing.T) {
	tests := []struct {
		name    string
		disable bool
		want    bool
	}{
		{"enabled by default", false, true},
		{"disabled when configured", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.RedactionConfig{DisableBodyStorage: tt.disable}
			r, _ := New(cfg)
			if got := r.ShouldStoreBody(); got != tt.want {
				t.Errorf("ShouldStoreBody() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestHeadersToMap verifies header conversion.
func TestHeadersToMap(t *testing.T) {
	h := http.Header{
		"Content-Type": []string{"application/json"},
		"Accept":       []string{"*/*", "text/plain"},
	}

	m := HeadersToMap(h)

	if len(m) != 2 {
		t.Errorf("HeadersToMap() len = %d, want 2", len(m))
	}
	if m["Content-Type"][0] != "application/json" {
		t.Errorf("HeadersToMap() Content-Type = %v", m["Content-Type"])
	}
	if len(m["Accept"]) != 2 {
		t.Errorf("HeadersToMap() Accept len = %d, want 2", len(m["Accept"]))
	}
}

// TestHeadersFromMap verifies map to header conversion.
func TestHeadersFromMap(t *testing.T) {
	m := map[string][]string{
		"Content-Type": {"application/json"},
		"Accept":       {"*/*"},
	}

	h := HeadersFromMap(m)

	if h.Get("Content-Type") != "application/json" {
		t.Errorf("HeadersFromMap() Content-Type = %q", h.Get("Content-Type"))
	}
	if h.Get("Accept") != "*/*" {
		t.Errorf("HeadersFromMap() Accept = %q", h.Get("Accept"))
	}
}

// TestRedactMixedContent verifies multiple types of sensitive data in one body.
func TestRedactMixedContent(t *testing.T) {
	r, _ := New(testConfig())

	fakeBase64 := strings.Repeat("ABCD1234", 20) // 160 chars

	input := `{
		"anthropic_key": "sk-ant-api03-aaaaaaaaaaaaaaaaaaaaaa",
		"openai_key": "sk-bbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"aws_key": "AKIAIOSFODNN7EXAMPLE",
		"google_key": "AIzaSyA1234567890abcdefghijklmnopqrstuv",
		"image": "data:image/png;base64,` + fakeBase64 + `"
	}`

	got := r.RedactBody(input)

	checks := []struct {
		name      string
		badString string
	}{
		{"anthropic key", "aaaaaaaaaaaaaaaaaaaaaa"},
		{"openai key", "bbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{"aws key", "IOSFODNN7EXAMPLE"},
		{"google key", "1234567890abcdefghijklmnopqrstuv"},
		{"base64 image", fakeBase64},
	}

	for _, c := range checks {
		if strings.Contains(got, c.badString) {
			t.Errorf("RedactBody() did not redact %s", c.name)
		}
	}
}

// TestRedactInputSizeLimit verifies large bodies skip redaction.
func TestRedactInputSizeLimit(t *testing.T) {
	r, _ := New(testConfig())

	// Create body just under the limit - should be redacted
	underLimit := strings.Repeat("x", MaxRedactionInputSize-100) + "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"
	result := r.RedactBody(underLimit)
	if strings.Contains(result, "abcdefghijklmnopqrstuvwxyz") {
		t.Error("body under limit should have keys redacted")
	}

	// Create body over the limit - should skip redaction
	overLimit := strings.Repeat("x", MaxRedactionInputSize+100) + "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"
	result = r.RedactBody(overLimit)
	if result != overLimit {
		t.Error("body over limit should be returned as-is")
	}
}

// Benchmark for performance verification (Phase 2.0.8 requirement)
func BenchmarkRedactBody1MB(b *testing.B) {
	r, _ := New(testConfig())

	// Create ~1MB JSON with some API keys scattered throughout
	chunk := `{"data": "` + strings.Repeat("x", 10000) + `", "key": "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"}`
	body := strings.Repeat(chunk, 100) // ~1MB

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = r.RedactBody(body)
	}
}
