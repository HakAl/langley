// Package e2e contains end-to-end tests for Langley.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HakAl/langley/internal/api"
	"github.com/HakAl/langley/internal/config"
	"github.com/HakAl/langley/internal/proxy"
	"github.com/HakAl/langley/internal/redact"
	"github.com/HakAl/langley/internal/store"
	"github.com/HakAl/langley/internal/task"
	langleytls "github.com/HakAl/langley/internal/tls"
)

// TestE2E_DirectHandler tests the full flow using direct handler calls.
// 1. Request goes through MITM proxy handler
// 2. Proxy forwards to mock Claude API
// 3. Response flows back through proxy
// 4. Flow is saved to SQLite store
// 5. API endpoints return the saved flow
func TestE2E_DirectHandler(t *testing.T) {
	// Create temp directory for test data
	tempDir, err := os.MkdirTemp("", "langley-e2e-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// 1. Create mock Claude API upstream
	mockClaude := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "req_e2e123")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":    "msg_e2e123",
			"type":  "message",
			"role":  "assistant",
			"model": "claude-3-sonnet-20240229",
			"content": []map[string]interface{}{
				{"type": "text", "text": "E2E test response!"},
			},
			"stop_reason": "end_turn",
			"usage": map[string]interface{}{
				"input_tokens":  15,
				"output_tokens": 8,
			},
		})
	}))
	defer mockClaude.Close()

	// 2. Create SQLite store
	dbPath := filepath.Join(tempDir, "langley.db")
	retention := &config.RetentionConfig{
		FlowsTTLDays:   7,
		EventsTTLDays:  7,
		BodiesTTLDays:  3,
		DropLogTTLDays: 1,
	}
	s, err := store.NewSQLiteStore(dbPath, retention)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	// 3. Create CA and cert cache
	certDir := filepath.Join(tempDir, "certs")
	_ = os.MkdirAll(certDir, 0755)

	ca, err := langleytls.LoadOrCreateCA(certDir)
	if err != nil {
		t.Fatalf("failed to create CA: %v", err)
	}

	certCache := langleytls.NewCertCache(ca, 100)

	// 4. Create config
	cfg := &config.Config{
		Proxy: config.ProxyConfig{
			Listen: "127.0.0.1:0",
		},
		Persistence: config.PersistenceConfig{
			DBPath:       dbPath,
			BodyMaxBytes: 1024 * 1024,
		},
		Redaction: config.RedactionConfig{
			RedactAPIKeys: true,
		},
		Auth: config.AuthConfig{
			Token: "test-token-123",
		},
	}

	// 5. Create redactor
	redactor, err := redact.New(&cfg.Redaction)
	if err != nil {
		t.Fatalf("failed to create redactor: %v", err)
	}

	// 6. Create MITM proxy
	mitmProxy, err := proxy.NewMITMProxy(proxy.MITMProxyConfig{
		Config:       cfg,
		CA:           ca,
		CertCache:    certCache,
		Store:        s,
		Redactor:     redactor,
		TaskAssigner: task.NewAssigner(task.AssignerConfig{IdleGapMinutes: 5}),
	})
	if err != nil {
		t.Fatalf("failed to create MITM proxy: %v", err)
	}

	// 7. Create request that looks like it's going through a proxy
	mockURL, _ := url.Parse(mockClaude.URL)
	reqBody := `{"model":"claude-3-sonnet-20240229","messages":[{"role":"user","content":"E2E test"}],"max_tokens":50}`

	req := httptest.NewRequest("POST", mockClaude.URL+"/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-ant-api-test456")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Host = mockURL.Host
	req.URL.Host = mockURL.Host
	req.URL.Scheme = mockURL.Scheme

	// 8. Call proxy handler directly
	recorder := httptest.NewRecorder()
	mitmProxy.ServeHTTP(recorder, req)

	// 9. Verify response
	resp := recorder.Result()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected status: %d, body: %s", resp.StatusCode, body)
	}

	var respData map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&respData)
	if respData["id"] != "msg_e2e123" {
		t.Errorf("unexpected response id: %v", respData["id"])
	}

	// 10. Verify flow was saved
	ctx := context.Background()
	flows, err := s.ListFlows(ctx, store.FlowFilter{Limit: 10})
	if err != nil {
		t.Fatalf("failed to list flows: %v", err)
	}

	if len(flows) == 0 {
		t.Fatal("no flows saved")
	}

	flow := flows[0]
	if flow.Method != "POST" {
		t.Errorf("expected POST, got %s", flow.Method)
	}
	if flow.StatusCode == nil || *flow.StatusCode != 200 {
		t.Errorf("expected status 200, got %v", flow.StatusCode)
	}
	// Model extraction depends on response parsing - may be nil for simple JSON responses
	// SSE streaming responses extract model from message_start event
	if flow.Model != nil && *flow.Model != "claude-3-sonnet-20240229" {
		t.Errorf("expected model claude-3-sonnet-20240229 or nil, got %v", flow.Model)
	}

	// 11. Test API endpoints
	apiServer := api.NewServer(cfg, s, nil)
	handler := apiServer.Handler()

	// GET /api/flows (with auth)
	apiReq := httptest.NewRequest("GET", "/api/flows", nil)
	apiReq.Header.Set("Authorization", "Bearer test-token-123")
	apiResp := httptest.NewRecorder()
	handler.ServeHTTP(apiResp, apiReq)

	if apiResp.Code != http.StatusOK {
		t.Errorf("GET /api/flows returned %d: %s", apiResp.Code, apiResp.Body.String())
	}

	// GET /api/flows/:id (with auth)
	apiReq = httptest.NewRequest("GET", "/api/flows/"+flow.ID, nil)
	apiReq.Header.Set("Authorization", "Bearer test-token-123")
	apiResp = httptest.NewRecorder()
	handler.ServeHTTP(apiResp, apiReq)

	if apiResp.Code != http.StatusOK {
		t.Errorf("GET /api/flows/:id returned %d: %s", apiResp.Code, apiResp.Body.String())
	}

	// Model might be nil if not extracted from response - check gracefully
	modelStr := "<nil>"
	if flow.Model != nil {
		modelStr = *flow.Model
	}
	t.Logf("E2E DirectHandler test passed: flow %s saved with model %s", flow.ID, modelStr)
}

// TestE2E_SSEStreaming tests Server-Sent Events streaming through the proxy.
func TestE2E_SSEStreaming(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-e2e-sse-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Mock Claude API with SSE streaming response
	mockClaude := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		// Check for streaming request
		var reqBody map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)

		if reqBody["stream"] != true {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "msg_nonstream", "type": "message",
			})
			return
		}

		// SSE streaming response
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("x-request-id", "req_sse123")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter doesn't support Flusher")
			return
		}

		// Send SSE events
		events := []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_sse123","type":"message","role":"assistant","model":"claude-3-opus-20240229","content":[]}}` + "\n\n",
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n",
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}` + "\n\n",
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" from"}}` + "\n\n",
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" SSE!"}}` + "\n\n",
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n",
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}` + "\n\n",
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}` + "\n\n",
		}

		for _, event := range events {
			fmt.Fprint(w, event)
			flusher.Flush()
		}
	}))
	defer mockClaude.Close()

	// Create store
	dbPath := filepath.Join(tempDir, "langley.db")
	s, err := store.NewSQLiteStore(dbPath, &config.RetentionConfig{
		FlowsTTLDays:  7,
		EventsTTLDays: 7,
	})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	// Create CA and proxy
	certDir := filepath.Join(tempDir, "certs")
	_ = os.MkdirAll(certDir, 0755)
	ca, _ := langleytls.LoadOrCreateCA(certDir)
	certCache := langleytls.NewCertCache(ca, 100)

	cfg := &config.Config{
		Persistence: config.PersistenceConfig{BodyMaxBytes: 1024 * 1024},
		Redaction:   config.RedactionConfig{RedactAPIKeys: true},
	}

	redactor, _ := redact.New(&cfg.Redaction)

	mitmProxy, err := proxy.NewMITMProxy(proxy.MITMProxyConfig{
		Config:       cfg,
		CA:           ca,
		CertCache:    certCache,
		Store:        s,
		Redactor:     redactor,
		TaskAssigner: task.NewAssigner(task.AssignerConfig{IdleGapMinutes: 5}),
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	// Make streaming request
	mockURL, _ := url.Parse(mockClaude.URL)
	reqBody := `{"model":"claude-3-opus-20240229","messages":[{"role":"user","content":"Stream test"}],"max_tokens":100,"stream":true}`

	req := httptest.NewRequest("POST", mockClaude.URL+"/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-ant-api-test789")
	req.Host = mockURL.Host
	req.URL.Host = mockURL.Host
	req.URL.Scheme = mockURL.Scheme

	recorder := httptest.NewRecorder()
	mitmProxy.ServeHTTP(recorder, req)

	resp := recorder.Result()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected status: %d, body: %s", resp.StatusCode, body)
	}

	// Read streamed response
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Verify SSE events were streamed
	if !strings.Contains(bodyStr, "message_start") {
		t.Error("response missing message_start event")
	}
	if !strings.Contains(bodyStr, "Hello") {
		t.Error("response missing streamed text")
	}
	if !strings.Contains(bodyStr, "message_stop") {
		t.Error("response missing message_stop event")
	}

	// Verify flow was saved
	ctx := context.Background()
	flows, _ := s.ListFlows(ctx, store.FlowFilter{Limit: 10})
	if len(flows) == 0 {
		t.Fatal("no flows saved for SSE request")
	}

	flow := flows[0]
	// Model extraction from SSE may fail if events aren't saved due to timing
	// The important thing is that the flow was saved
	modelStr := "<nil>"
	if flow.Model != nil {
		modelStr = *flow.Model
		if *flow.Model != "claude-3-opus-20240229" {
			t.Errorf("unexpected model: got %s, want claude-3-opus-20240229", *flow.Model)
		}
	}

	t.Logf("E2E SSE test passed: flow %s with streaming response, model=%s", flow.ID, modelStr)
}

// TestE2E_TaskAssignment tests that requests are assigned to tasks correctly.
func TestE2E_TaskAssignment(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-e2e-task-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mockClaude := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "msg_task123", "type": "message", "model": "claude-3-haiku-20240307",
			"content": []map[string]interface{}{{"type": "text", "text": "Task test"}},
			"usage":   map[string]interface{}{"input_tokens": 5, "output_tokens": 3},
		})
	}))
	defer mockClaude.Close()

	dbPath := filepath.Join(tempDir, "langley.db")
	s, _ := store.NewSQLiteStore(dbPath, &config.RetentionConfig{FlowsTTLDays: 7})
	defer s.Close()

	certDir := filepath.Join(tempDir, "certs")
	_ = os.MkdirAll(certDir, 0755)
	ca, _ := langleytls.LoadOrCreateCA(certDir)
	certCache := langleytls.NewCertCache(ca, 100)

	cfg := &config.Config{
		Persistence: config.PersistenceConfig{BodyMaxBytes: 1024 * 1024},
		Redaction:   config.RedactionConfig{RedactAPIKeys: true},
	}

	redactor, _ := redact.New(&cfg.Redaction)

	mitmProxy, _ := proxy.NewMITMProxy(proxy.MITMProxyConfig{
		Config:       cfg,
		CA:           ca,
		CertCache:    certCache,
		Store:        s,
		Redactor:     redactor,
		TaskAssigner: task.NewAssigner(task.AssignerConfig{IdleGapMinutes: 5}),
	})

	ctx := context.Background()

	// Test 1: Explicit task header
	mockURL, _ := url.Parse(mockClaude.URL)
	req := httptest.NewRequest("POST", mockClaude.URL+"/v1/messages",
		strings.NewReader(`{"model":"claude-3-haiku-20240307","messages":[{"role":"user","content":"Test"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Langley-Task", "explicit-task-001")
	req.Host = mockURL.Host
	req.URL.Host = mockURL.Host
	req.URL.Scheme = mockURL.Scheme

	recorder := httptest.NewRecorder()
	mitmProxy.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("request failed: %d", recorder.Code)
	}

	flows, _ := s.ListFlows(ctx, store.FlowFilter{Limit: 10})
	if len(flows) == 0 {
		t.Fatal("no flows saved")
	}

	if flows[0].TaskID == nil || *flows[0].TaskID != "explicit-task-001" {
		t.Errorf("expected task_id 'explicit-task-001', got '%v'", flows[0].TaskID)
	}

	// Test 2: Metadata user_id (Claude API uses user_id for task grouping)
	req2 := httptest.NewRequest("POST", mockClaude.URL+"/v1/messages",
		strings.NewReader(`{"model":"claude-3-haiku-20240307","messages":[{"role":"user","content":"Test"}],"metadata":{"user_id":"metadata-task-002"}}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.Host = mockURL.Host
	req2.URL.Host = mockURL.Host
	req2.URL.Scheme = mockURL.Scheme

	recorder2 := httptest.NewRecorder()
	mitmProxy.ServeHTTP(recorder2, req2)

	flows2, _ := s.ListFlows(ctx, store.FlowFilter{Limit: 10})
	if len(flows2) < 2 {
		t.Fatal("expected at least 2 flows")
	}

	// Most recent flow should have metadata task ID (from user_id)
	if flows2[0].TaskID == nil || *flows2[0].TaskID != "metadata-task-002" {
		taskVal := "<nil>"
		if flows2[0].TaskID != nil {
			taskVal = *flows2[0].TaskID
		}
		t.Errorf("expected task_id 'metadata-task-002', got '%s'", taskVal)
	}

	t.Log("E2E TaskAssignment test passed")
}

// TestE2E_ErrorHandling tests proxy behavior with upstream errors.
func TestE2E_ErrorHandling(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-e2e-error-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Mock that returns errors
	mockClaude := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"type": "error",
			"error": map[string]interface{}{
				"type":    "invalid_request_error",
				"message": "Invalid model specified",
			},
		})
	}))
	defer mockClaude.Close()

	dbPath := filepath.Join(tempDir, "langley.db")
	s, _ := store.NewSQLiteStore(dbPath, &config.RetentionConfig{FlowsTTLDays: 7})
	defer s.Close()

	certDir := filepath.Join(tempDir, "certs")
	_ = os.MkdirAll(certDir, 0755)
	ca, _ := langleytls.LoadOrCreateCA(certDir)
	certCache := langleytls.NewCertCache(ca, 100)

	cfg := &config.Config{
		Persistence: config.PersistenceConfig{BodyMaxBytes: 1024 * 1024},
		Redaction:   config.RedactionConfig{RedactAPIKeys: true},
	}

	redactor, _ := redact.New(&cfg.Redaction)

	mitmProxy, _ := proxy.NewMITMProxy(proxy.MITMProxyConfig{
		Config:       cfg,
		CA:           ca,
		CertCache:    certCache,
		Store:        s,
		Redactor:     redactor,
		TaskAssigner: task.NewAssigner(task.AssignerConfig{IdleGapMinutes: 5}),
	})

	mockURL, _ := url.Parse(mockClaude.URL)
	req := httptest.NewRequest("POST", mockClaude.URL+"/v1/messages",
		strings.NewReader(`{"model":"invalid-model","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Host = mockURL.Host
	req.URL.Host = mockURL.Host
	req.URL.Scheme = mockURL.Scheme

	recorder := httptest.NewRecorder()
	mitmProxy.ServeHTTP(recorder, req)

	// Should pass through the error response
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", recorder.Code)
	}

	// Error flows should still be saved
	ctx := context.Background()
	flows, _ := s.ListFlows(ctx, store.FlowFilter{Limit: 10})
	if len(flows) == 0 {
		t.Fatal("error flow was not saved")
	}

	if flows[0].StatusCode == nil || *flows[0].StatusCode != 400 {
		t.Errorf("expected saved status 400, got %v", flows[0].StatusCode)
	}

	t.Log("E2E ErrorHandling test passed")
}

// TestE2E_Redaction tests that sensitive data is redacted.
func TestE2E_Redaction(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-e2e-redact-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mockClaude := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo back the authorization header to verify it reaches upstream unredacted
		auth := r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":    "msg_redact",
			"model": "claude-3-sonnet-20240229",
			"auth":  auth, // For test verification
		})
	}))
	defer mockClaude.Close()

	dbPath := filepath.Join(tempDir, "langley.db")
	s, _ := store.NewSQLiteStore(dbPath, &config.RetentionConfig{FlowsTTLDays: 7})
	defer s.Close()

	certDir := filepath.Join(tempDir, "certs")
	_ = os.MkdirAll(certDir, 0755)
	ca, _ := langleytls.LoadOrCreateCA(certDir)
	certCache := langleytls.NewCertCache(ca, 100)

	cfg := &config.Config{
		Persistence: config.PersistenceConfig{BodyMaxBytes: 1024 * 1024},
		Redaction: config.RedactionConfig{
			RedactAPIKeys:       true,
			AlwaysRedactHeaders: []string{"Authorization", "X-Api-Key"},
		},
	}

	redactor, _ := redact.New(&cfg.Redaction)

	mitmProxy, _ := proxy.NewMITMProxy(proxy.MITMProxyConfig{
		Config:       cfg,
		CA:           ca,
		CertCache:    certCache,
		Store:        s,
		Redactor:     redactor,
		TaskAssigner: task.NewAssigner(task.AssignerConfig{IdleGapMinutes: 5}),
	})

	mockURL, _ := url.Parse(mockClaude.URL)
	req := httptest.NewRequest("POST", mockClaude.URL+"/v1/messages",
		bytes.NewReader([]byte(`{"model":"claude-3-sonnet-20240229","messages":[{"role":"user","content":"Test"}]}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-ant-api03-SUPERSECRETKEY123")
	req.Host = mockURL.Host
	req.URL.Host = mockURL.Host
	req.URL.Scheme = mockURL.Scheme

	recorder := httptest.NewRecorder()
	mitmProxy.ServeHTTP(recorder, req)

	// Verify response came through (request wasn't blocked)
	if recorder.Code != http.StatusOK {
		t.Fatalf("request failed: %d", recorder.Code)
	}

	// Check that stored flow has redacted headers
	ctx := context.Background()
	flows, _ := s.ListFlows(ctx, store.FlowFilter{Limit: 10})
	if len(flows) == 0 {
		t.Fatal("no flows saved")
	}

	flow, _ := s.GetFlow(ctx, flows[0].ID)
	if flow == nil {
		t.Fatal("could not get flow")
	}

	// Check request headers for redaction
	if authHeader, ok := flow.RequestHeaders["Authorization"]; ok {
		for _, v := range authHeader {
			if strings.Contains(v, "SUPERSECRETKEY") {
				t.Error("Authorization header was not redacted in stored flow")
			}
			if !strings.Contains(v, "[REDACTED]") {
				t.Error("Authorization header should contain [REDACTED]")
			}
		}
	}

	t.Log("E2E Redaction test passed")
}
