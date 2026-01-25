package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anthropics/langley/internal/config"
	"github.com/anthropics/langley/internal/store"
)

// mockStore implements store.Store for testing.
type mockStore struct {
	flows []*store.Flow
}

func (m *mockStore) SaveFlow(ctx context.Context, flow *store.Flow) error      { return nil }
func (m *mockStore) UpdateFlow(ctx context.Context, flow *store.Flow) error    { return nil }
func (m *mockStore) GetFlow(ctx context.Context, id string) (*store.Flow, error) {
	return &store.Flow{ID: id, Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages"}, nil
}
func (m *mockStore) ListFlows(ctx context.Context, filter store.FlowFilter) ([]*store.Flow, error) {
	if m.flows == nil {
		return []*store.Flow{}, nil
	}
	// Simple pagination
	start := filter.Offset
	if start >= len(m.flows) {
		return []*store.Flow{}, nil
	}
	end := start + filter.Limit
	if end > len(m.flows) {
		end = len(m.flows)
	}
	return m.flows[start:end], nil
}
func (m *mockStore) DeleteFlow(ctx context.Context, id string) error { return nil }
func (m *mockStore) CountFlows(ctx context.Context, filter store.FlowFilter) (int, error) {
	if m.flows == nil {
		return 0, nil
	}
	return len(m.flows), nil
}
func (m *mockStore) SaveEvent(ctx context.Context, event *store.Event) error                   { return nil }
func (m *mockStore) SaveEvents(ctx context.Context, events []*store.Event) error               { return nil }
func (m *mockStore) GetEventsByFlow(ctx context.Context, flowID string) ([]*store.Event, error) {
	return []*store.Event{}, nil
}
func (m *mockStore) SaveToolInvocation(ctx context.Context, inv *store.ToolInvocation) error { return nil }
func (m *mockStore) GetToolInvocationsByFlow(ctx context.Context, flowID string) ([]*store.ToolInvocation, error) {
	return []*store.ToolInvocation{}, nil
}
func (m *mockStore) LogDrop(ctx context.Context, entry *store.DropLogEntry) error { return nil }
func (m *mockStore) RunRetention(ctx context.Context) (int64, error)              { return 0, nil }
func (m *mockStore) Close() error                                                 { return nil }
func (m *mockStore) DB() interface{}                                              { return nil }

func TestAuthMiddleware_RejectsTokenInURL(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Auth.Token = "test-token-12345"

	server := NewServer(cfg, &mockStore{}, nil)
	handler := server.Handler()

	tests := []struct {
		name           string
		path           string
		authHeader     string
		wantStatus     int
		wantBodySubstr string
	}{
		{
			name:           "token in URL rejected with 400",
			path:           "/api/flows?token=test-token-12345",
			authHeader:     "",
			wantStatus:     http.StatusBadRequest,
			wantBodySubstr: "Token in URL is not allowed",
		},
		{
			name:           "token in URL rejected even if valid",
			path:           "/api/flows?token=test-token-12345",
			authHeader:     "",
			wantStatus:     http.StatusBadRequest,
			wantBodySubstr: "Token in URL is not allowed",
		},
		{
			name:           "token in URL rejected even with header also present",
			path:           "/api/flows?token=test-token-12345",
			authHeader:     "Bearer test-token-12345",
			wantStatus:     http.StatusBadRequest,
			wantBodySubstr: "Token in URL is not allowed",
		},
		{
			name:           "valid header auth succeeds",
			path:           "/api/flows",
			authHeader:     "Bearer test-token-12345",
			wantStatus:     http.StatusOK,
			wantBodySubstr: "",
		},
		{
			name:           "missing auth returns 401",
			path:           "/api/flows",
			authHeader:     "",
			wantStatus:     http.StatusUnauthorized,
			wantBodySubstr: "Unauthorized",
		},
		{
			name:           "invalid token returns 401",
			path:           "/api/flows",
			authHeader:     "Bearer wrong-token",
			wantStatus:     http.StatusUnauthorized,
			wantBodySubstr: "Unauthorized",
		},
		{
			name:           "empty token param is allowed (no param value)",
			path:           "/api/flows?other=param",
			authHeader:     "Bearer test-token-12345",
			wantStatus:     http.StatusOK,
			wantBodySubstr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", rr.Code, tt.wantStatus)
			}

			if tt.wantBodySubstr != "" {
				body := rr.Body.String()
				if !containsSubstring(body, tt.wantBodySubstr) {
					t.Errorf("body %q does not contain %q", body, tt.wantBodySubstr)
				}
			}
		})
	}
}

func TestAuthMiddleware_ConstantTimeComparison(t *testing.T) {
	// This test verifies that the auth middleware is using constant-time comparison
	// by checking that subtle.ConstantTimeCompare is used in the code.
	// (Actual timing attack tests would be flaky and not suitable for unit tests)
	//
	// The implementation should use:
	//   subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) != 1
	//
	// This test just verifies the auth works correctly with various inputs.

	cfg := config.DefaultConfig()
	cfg.Auth.Token = "secure-token-abc123"

	server := NewServer(cfg, &mockStore{}, nil)
	handler := server.Handler()

	// Test that similar-length wrong tokens are rejected
	wrongTokens := []string{
		"secure-token-abc124", // Off by one char
		"secure-token-abc12",  // Missing char
		"secure-token-abc1234", // Extra char
		"SECURE-TOKEN-ABC123", // Wrong case
	}

	for _, wrongToken := range wrongTokens {
		req := httptest.NewRequest("GET", "/api/flows", nil)
		req.Header.Set("Authorization", "Bearer "+wrongToken)

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("wrong token %q: got status %d, want 401", wrongToken, rr.Code)
		}
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && containsSubstringHelper(s, substr)))
}

func containsSubstringHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestAdminReload_LocalhostOnly(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Auth.Token = "test-token"

	server := NewServer(cfg, &mockStore{}, nil)
	handler := server.Handler()

	tests := []struct {
		name       string
		remoteAddr string
		wantStatus int
	}{
		{
			name:       "localhost IPv4 allowed",
			remoteAddr: "127.0.0.1:12345",
			wantStatus: http.StatusServiceUnavailable, // No config path set, but passes auth
		},
		{
			name:       "localhost IPv6 allowed",
			remoteAddr: "[::1]:12345",
			wantStatus: http.StatusServiceUnavailable, // No config path set, but passes auth
		},
		// Note: Testing non-localhost rejection is difficult in unit tests
		// because httptest uses localhost by default
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/admin/reload", nil)
			req.Header.Set("Authorization", "Bearer test-token")
			req.RemoteAddr = tt.remoteAddr

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d, body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
		})
	}
}

func TestIsLocalhost(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"127.0.0.1", true},
		{"localhost:8080", true},
		{"localhost", true},
		{"[::1]:8080", true},
		{"::1", true},
		{"192.168.1.1:8080", false},
		{"10.0.0.1:8080", false},
		{"8.8.8.8:8080", false},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got := isLocalhost(tt.addr)
			if got != tt.want {
				t.Errorf("isLocalhost(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestExportFlows_NDJSON(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Auth.Token = "test-token"

	mockFlows := createTestFlows(3)
	ms := &mockStore{flows: mockFlows}

	server := NewServer(cfg, ms, nil)
	handler := server.Handler()

	req := httptest.NewRequest("GET", "/api/flows/export?format=ndjson", nil)
	req.Header.Set("Authorization", "Bearer test-token")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200, body: %s", rr.Code, rr.Body.String())
	}

	if ct := rr.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", ct)
	}

	// Should have 3 lines (one per flow)
	lines := splitNonEmpty(rr.Body.String(), "\n")
	if len(lines) != 3 {
		t.Errorf("got %d lines, want 3", len(lines))
	}
}

func TestExportFlows_JSON(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Auth.Token = "test-token"

	mockFlows := createTestFlows(2)
	ms := &mockStore{flows: mockFlows}

	server := NewServer(cfg, ms, nil)
	handler := server.Handler()

	req := httptest.NewRequest("GET", "/api/flows/export?format=json", nil)
	req.Header.Set("Authorization", "Bearer test-token")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rr.Code)
	}

	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	// Parse JSON response
	var result map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	flows, ok := result["flows"].([]interface{})
	if !ok {
		t.Fatal("missing 'flows' array")
	}
	if len(flows) != 2 {
		t.Errorf("got %d flows, want 2", len(flows))
	}

	meta, ok := result["meta"].(map[string]interface{})
	if !ok {
		t.Fatal("missing 'meta' object")
	}
	if meta["row_count"].(float64) != 2 {
		t.Errorf("row_count = %v, want 2", meta["row_count"])
	}
}

func TestExportFlows_CSV(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Auth.Token = "test-token"

	mockFlows := createTestFlows(2)
	ms := &mockStore{flows: mockFlows}

	server := NewServer(cfg, ms, nil)
	handler := server.Handler()

	req := httptest.NewRequest("GET", "/api/flows/export?format=csv", nil)
	req.Header.Set("Authorization", "Bearer test-token")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rr.Code)
	}

	if ct := rr.Header().Get("Content-Type"); ct != "text/csv" {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}

	// Should have header + 2 data rows
	lines := splitNonEmpty(rr.Body.String(), "\n")
	if len(lines) != 3 {
		t.Errorf("got %d lines, want 3 (header + 2 rows)", len(lines))
	}
}

func TestExportFlows_MaxRows(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Auth.Token = "test-token"

	mockFlows := createTestFlows(10)
	ms := &mockStore{flows: mockFlows}

	server := NewServer(cfg, ms, nil)
	handler := server.Handler()

	req := httptest.NewRequest("GET", "/api/flows/export?format=ndjson&max_rows=3", nil)
	req.Header.Set("Authorization", "Bearer test-token")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rr.Code)
	}

	lines := splitNonEmpty(rr.Body.String(), "\n")
	if len(lines) != 3 {
		t.Errorf("got %d lines, want 3 (max_rows limit)", len(lines))
	}
}

// Helper to create test flows
func createTestFlows(n int) []*store.Flow {
	flows := make([]*store.Flow, n)
	for i := 0; i < n; i++ {
		status := 200
		flows[i] = &store.Flow{
			ID:            "flow-" + string(rune('a'+i)),
			Host:          "api.anthropic.com",
			Method:        "POST",
			Path:          "/v1/messages",
			StatusCode:    &status,
			Provider:      "anthropic",
			FlowIntegrity: "complete",
		}
	}
	return flows
}

// Helper to split string and remove empty lines
func splitNonEmpty(s, sep string) []string {
	parts := make([]string, 0)
	for _, p := range split(s, sep) {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

func split(s, sep string) []string {
	var result []string
	for len(s) > 0 {
		i := indexOf(s, sep)
		if i < 0 {
			result = append(result, s)
			break
		}
		result = append(result, s[:i])
		s = s[i+len(sep):]
	}
	return result
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
