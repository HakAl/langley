package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HakAl/langley/internal/config"
	"github.com/HakAl/langley/internal/redact"
	"github.com/HakAl/langley/internal/store"
	"github.com/HakAl/langley/internal/task"
	langleytls "github.com/HakAl/langley/internal/tls"
)

// flowCapture provides thread-safe capture of flow data for tests.
type flowCapture struct {
	mu     sync.Mutex
	flow   *store.Flow
	events []*store.Event
}

func (c *flowCapture) OnFlow(flow *store.Flow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flow = flow
}

func (c *flowCapture) OnUpdate(flow *store.Flow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flow = flow
}

func (c *flowCapture) OnEvent(event *store.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

func (c *flowCapture) Flow() *store.Flow {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.flow
}

func (c *flowCapture) Events() []*store.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.events
}

// WaitForFlow waits for a flow to be captured with timeout.
func (c *flowCapture) WaitForFlow(timeout time.Duration) *store.Flow {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f := c.Flow(); f != nil {
			return f
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// testConfig returns a minimal config for testing
func testConfig() *config.Config {
	return &config.Config{
		Proxy: config.ProxyConfig{
			Listen: "127.0.0.1:0", // Random port
		},
		Persistence: config.PersistenceConfig{
			BodyMaxBytes: 1024 * 1024,
		},
		Retention: config.RetentionConfig{
			FlowsTTLDays: 7,
		},
	}
}

// testLogger returns a silent logger for tests
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNew(t *testing.T) {
	t.Parallel()

	t.Run("valid config", func(t *testing.T) {
		cfg := testConfig()
		p, err := New(cfg, testLogger())
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if p == nil {
			t.Fatal("New() returned nil")
		}
	})

	t.Run("nil config", func(t *testing.T) {
		_, err := New(nil, testLogger())
		if err == nil {
			t.Error("New() expected error for nil config")
		}
	})

	t.Run("nil logger uses default", func(t *testing.T) {
		cfg := testConfig()
		p, err := New(cfg, nil)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if p.logger == nil {
			t.Error("logger should not be nil")
		}
	})
}

func TestCopyHeaders(t *testing.T) {
	t.Parallel()

	src := http.Header{}
	src.Set("Content-Type", "application/json")
	src.Set("X-Custom", "value1")
	src.Add("X-Custom", "value2")

	dst := http.Header{}
	copyHeaders(dst, src)

	if dst.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want %q", dst.Get("Content-Type"), "application/json")
	}

	values := dst.Values("X-Custom")
	if len(values) != 2 {
		t.Errorf("X-Custom values = %d, want 2", len(values))
	}
}

func TestRemoveHopByHopHeaders(t *testing.T) {
	t.Parallel()

	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Connection", "keep-alive")
	h.Set("Keep-Alive", "timeout=5")
	h.Set("Transfer-Encoding", "chunked")
	h.Set("X-Custom", "value")

	removeHopByHopHeaders(h)

	// Should be removed
	if h.Get("Connection") != "" {
		t.Error("Connection header should be removed")
	}
	if h.Get("Keep-Alive") != "" {
		t.Error("Keep-Alive header should be removed")
	}
	if h.Get("Transfer-Encoding") != "" {
		t.Error("Transfer-Encoding header should be removed")
	}

	// Should remain
	if h.Get("Content-Type") != "application/json" {
		t.Error("Content-Type should remain")
	}
	if h.Get("X-Custom") != "value" {
		t.Error("X-Custom should remain")
	}
}

func TestRemoveHopByHopHeaders_ConnectionValues(t *testing.T) {
	t.Parallel()

	h := http.Header{}
	h.Set("Connection", "X-Foo, X-Bar")
	h.Set("X-Foo", "foo")
	h.Set("X-Bar", "bar")
	h.Set("X-Keep", "keep")

	removeHopByHopHeaders(h)

	// Headers listed in Connection should be removed
	if h.Get("X-Foo") != "" {
		t.Error("X-Foo should be removed (listed in Connection)")
	}
	if h.Get("X-Bar") != "" {
		t.Error("X-Bar should be removed (listed in Connection)")
	}

	// X-Keep should remain
	if h.Get("X-Keep") != "keep" {
		t.Error("X-Keep should remain")
	}
}

// mockStore implements store.Store for testing
type mockStore struct {
	flows  map[string]*store.Flow
	events map[string][]*store.Event
}

func newMockStore() *mockStore {
	return &mockStore{
		flows:  make(map[string]*store.Flow),
		events: make(map[string][]*store.Event),
	}
}

func (m *mockStore) SaveFlow(ctx context.Context, flow *store.Flow) error {
	m.flows[flow.ID] = flow
	return nil
}

func (m *mockStore) UpdateFlow(ctx context.Context, flow *store.Flow) error {
	m.flows[flow.ID] = flow
	return nil
}

func (m *mockStore) GetFlow(ctx context.Context, id string) (*store.Flow, error) {
	return m.flows[id], nil
}

func (m *mockStore) ListFlows(ctx context.Context, filter store.FlowFilter) ([]*store.Flow, error) {
	var result []*store.Flow
	for _, f := range m.flows {
		result = append(result, f)
	}
	return result, nil
}

func (m *mockStore) DeleteFlow(ctx context.Context, id string) error {
	delete(m.flows, id)
	return nil
}

func (m *mockStore) CountFlows(ctx context.Context, filter store.FlowFilter) (int, error) {
	return len(m.flows), nil
}

func (m *mockStore) SaveEvent(ctx context.Context, event *store.Event) error {
	// Simulate foreign key constraint - flow must exist (langley-2fa)
	if _, exists := m.flows[event.FlowID]; !exists {
		return fmt.Errorf("FOREIGN KEY constraint failed: flow %s does not exist", event.FlowID)
	}
	m.events[event.FlowID] = append(m.events[event.FlowID], event)
	return nil
}

func (m *mockStore) SaveEvents(ctx context.Context, events []*store.Event) error {
	for _, e := range events {
		m.events[e.FlowID] = append(m.events[e.FlowID], e)
	}
	return nil
}

func (m *mockStore) GetEventsByFlow(ctx context.Context, flowID string) ([]*store.Event, error) {
	return m.events[flowID], nil
}

func (m *mockStore) SaveToolInvocation(ctx context.Context, inv *store.ToolInvocation) error {
	return nil
}

func (m *mockStore) GetToolInvocationsByFlow(ctx context.Context, flowID string) ([]*store.ToolInvocation, error) {
	return nil, nil
}

func (m *mockStore) LogDrop(ctx context.Context, entry *store.DropLogEntry) error {
	return nil
}

func (m *mockStore) RunRetention(ctx context.Context) (int64, error) {
	return 0, nil
}

func (m *mockStore) Close() error {
	return nil
}

func (m *mockStore) DB() interface{} {
	return nil
}

func TestMITMProxy_HTTPForwarding(t *testing.T) {
	t.Parallel()

	// Create mock upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo back some request info
		w.Header().Set("X-Echo-Method", r.Method)
		w.Header().Set("X-Echo-Path", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, r.Body)
	}))
	defer upstream.Close()

	// Create proxy components
	cfg := testConfig()
	logger := testLogger()

	// Create CA for MITM
	tmpDir := t.TempDir()
	ca, err := langleytls.LoadOrCreateCA(tmpDir)
	if err != nil {
		t.Fatalf("failed to create CA: %v", err)
	}

	certCache := langleytls.NewCertCache(ca, 100)

	redactor, err := redact.New(&config.RedactionConfig{})
	if err != nil {
		t.Fatalf("failed to create redactor: %v", err)
	}

	mockStore := newMockStore()
	taskAssigner := task.NewAssigner(task.AssignerConfig{})

	capture := &flowCapture{}
	proxy, err := NewMITMProxy(MITMProxyConfig{
		Config:       cfg,
		Logger:       logger,
		CA:           ca,
		CertCache:    certCache,
		Redactor:     redactor,
		Store:        mockStore,
		TaskAssigner: taskAssigner,
		OnFlow:       capture.OnFlow,
		OnUpdate:     capture.OnUpdate,
	})
	if err != nil {
		t.Fatalf("NewMITMProxy failed: %v", err)
	}

	// Start proxy
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	// Make request through proxy to upstream
	// Note: This tests the HTTP (non-CONNECT) path
	reqBody := `{"message": "hello"}`
	req, err := http.NewRequest("POST", upstream.URL+"/test/path", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Use proxy as forward proxy
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(mustParseURL(t, proxyServer.URL)),
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Verify response
	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if resp.Header.Get("X-Echo-Method") != "POST" {
		t.Errorf("X-Echo-Method = %q, want %q", resp.Header.Get("X-Echo-Method"), "POST")
	}
	if resp.Header.Get("X-Echo-Path") != "/test/path" {
		t.Errorf("X-Echo-Path = %q, want %q", resp.Header.Get("X-Echo-Path"), "/test/path")
	}

	// Verify flow was captured
	capturedFlow := capture.WaitForFlow(2 * time.Second)
	if capturedFlow == nil {
		t.Fatal("flow was not captured")
	}
	if capturedFlow.Method != "POST" {
		t.Errorf("captured Method = %q, want %q", capturedFlow.Method, "POST")
	}
	if capturedFlow.Path != "/test/path" {
		t.Errorf("captured Path = %q, want %q", capturedFlow.Path, "/test/path")
	}
}

func TestMITMProxy_SSEResponse(t *testing.T) {
	t.Parallel()

	// Create mock SSE upstream
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter doesn't support flushing")
			return
		}

		// Send SSE events
		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\"}\n\n",
			"event: content_block_delta\ndata: {\"delta\":\"hello\"}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}

		for _, event := range events {
			w.Write([]byte(event))
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	// Create proxy
	cfg := testConfig()
	logger := testLogger()

	tmpDir := t.TempDir()
	ca, _ := langleytls.LoadOrCreateCA(tmpDir)
	certCache := langleytls.NewCertCache(ca, 100)
	redactor, _ := redact.New(&config.RedactionConfig{})
	mockStore := newMockStore()

	capture := &flowCapture{}

	proxy, _ := NewMITMProxy(MITMProxyConfig{
		Config:    cfg,
		Logger:    logger,
		CA:        ca,
		CertCache: certCache,
		Redactor:  redactor,
		Store:     mockStore,
		OnFlow:    capture.OnFlow,
		OnUpdate:  capture.OnUpdate,
		OnEvent:   capture.OnEvent,
	})

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	// Make request
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(mustParseURL(t, proxyServer.URL)),
		},
	}

	resp, err := client.Get(upstream.URL + "/messages")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Read entire response
	body, _ := io.ReadAll(resp.Body)

	// Verify SSE content
	if !strings.Contains(string(body), "message_start") {
		t.Error("response should contain message_start event")
	}

	// Verify flow marked as SSE
	capturedFlow := capture.WaitForFlow(2 * time.Second)
	if capturedFlow == nil {
		t.Fatal("flow not captured")
	}
	if !capturedFlow.IsSSE {
		t.Error("flow should be marked as SSE")
	}

	// Verify events captured
	capturedEvents := capture.Events()
	if len(capturedEvents) < 3 {
		t.Errorf("expected at least 3 events, got %d", len(capturedEvents))
	}
}

func TestMITMProxy_ErrorResponse(t *testing.T) {
	t.Parallel()

	// Create upstream that returns errors
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "bad request"}`))
	}))
	defer upstream.Close()

	cfg := testConfig()
	logger := testLogger()

	tmpDir := t.TempDir()
	ca, _ := langleytls.LoadOrCreateCA(tmpDir)
	certCache := langleytls.NewCertCache(ca, 100)
	redactor, _ := redact.New(&config.RedactionConfig{})
	mockStore := newMockStore()

	capture := &flowCapture{}
	proxy, _ := NewMITMProxy(MITMProxyConfig{
		Config:    cfg,
		Logger:    logger,
		CA:        ca,
		CertCache: certCache,
		Redactor:  redactor,
		Store:     mockStore,
		OnUpdate:  capture.OnUpdate,
	})

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(mustParseURL(t, proxyServer.URL)),
		},
	}

	resp, err := client.Get(upstream.URL + "/error")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Verify error status passed through
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	// Verify flow captures error status
	capturedFlow := capture.WaitForFlow(2 * time.Second)
	if capturedFlow != nil && capturedFlow.StatusCode != nil {
		if *capturedFlow.StatusCode != 400 {
			t.Errorf("captured StatusCode = %d, want 400", *capturedFlow.StatusCode)
		}
	}
}

func TestMITMProxy_TaskAssignment(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := testConfig()
	logger := testLogger()

	tmpDir := t.TempDir()
	ca, _ := langleytls.LoadOrCreateCA(tmpDir)
	certCache := langleytls.NewCertCache(ca, 100)
	redactor, _ := redact.New(&config.RedactionConfig{})
	mockStore := newMockStore()
	taskAssigner := task.NewAssigner(task.AssignerConfig{IdleGapMinutes: 5})

	capture := &flowCapture{}
	proxy, _ := NewMITMProxy(MITMProxyConfig{
		Config:       cfg,
		Logger:       logger,
		CA:           ca,
		CertCache:    certCache,
		Redactor:     redactor,
		Store:        mockStore,
		TaskAssigner: taskAssigner,
		OnFlow:       capture.OnFlow,
	})

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(mustParseURL(t, proxyServer.URL)),
		},
	}

	t.Run("explicit task header", func(t *testing.T) {
		req, _ := http.NewRequest("GET", upstream.URL+"/test", nil)
		req.Header.Set("X-Langley-Task", "my-explicit-task")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		resp.Body.Close()

		capturedFlow := capture.WaitForFlow(2 * time.Second)
		if capturedFlow == nil || capturedFlow.TaskID == nil {
			t.Fatal("task not assigned")
		}
		if *capturedFlow.TaskID != "my-explicit-task" {
			t.Errorf("TaskID = %q, want %q", *capturedFlow.TaskID, "my-explicit-task")
		}
		if capturedFlow.TaskSource == nil || *capturedFlow.TaskSource != "explicit" {
			t.Errorf("TaskSource = %v, want explicit", capturedFlow.TaskSource)
		}
	})

	t.Run("metadata task", func(t *testing.T) {
		body := `{"metadata": {"user_id": "metadata-task-123"}}`
		req, _ := http.NewRequest("POST", upstream.URL+"/test", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		resp.Body.Close()

		capturedFlow := capture.WaitForFlow(2 * time.Second)
		if capturedFlow == nil || capturedFlow.TaskID == nil {
			t.Fatal("task not assigned")
		}
		if *capturedFlow.TaskID != "metadata-task-123" {
			t.Errorf("TaskID = %q, want %q", *capturedFlow.TaskID, "metadata-task-123")
		}
	})
}

func TestMITMProxy_BodyTruncation(t *testing.T) {
	t.Parallel()

	// Create large response
	largeBody := bytes.Repeat([]byte("x"), 2*1024*1024) // 2MB

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write(largeBody)
	}))
	defer upstream.Close()

	cfg := testConfig()
	cfg.Persistence.BodyMaxBytes = 1024 * 1024 // 1MB limit
	logger := testLogger()

	tmpDir := t.TempDir()
	ca, _ := langleytls.LoadOrCreateCA(tmpDir)
	certCache := langleytls.NewCertCache(ca, 100)
	redactor, _ := redact.New(&config.RedactionConfig{})
	mockStore := newMockStore()

	capture := &flowCapture{}
	proxy, _ := NewMITMProxy(MITMProxyConfig{
		Config:    cfg,
		Logger:    logger,
		CA:        ca,
		CertCache: certCache,
		Redactor:  redactor,
		Store:     mockStore,
		OnUpdate:  capture.OnUpdate,
	})

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(mustParseURL(t, proxyServer.URL)),
		},
	}

	resp, err := client.Get(upstream.URL + "/large")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	// Read full response (should get all data)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if len(body) != len(largeBody) {
		t.Errorf("response body length = %d, want %d", len(body), len(largeBody))
	}

	// But captured body should be truncated
	capturedFlow := capture.WaitForFlow(2 * time.Second)
	if capturedFlow != nil && capturedFlow.ResponseBody != nil {
		if len(*capturedFlow.ResponseBody) > cfg.Persistence.BodyMaxBytes {
			t.Errorf("captured body should be truncated to %d, got %d",
				cfg.Persistence.BodyMaxBytes, len(*capturedFlow.ResponseBody))
		}
		if !capturedFlow.ResponseBodyTruncated {
			t.Error("ResponseBodyTruncated should be true")
		}
	}
}

// TestMITMProxy_RawBodyStorageOff verifies that RawBodyStorage=false prevents body storage.
func TestMITMProxy_RawBodyStorageOff(t *testing.T) {
	t.Parallel()

	testBody := "test request body"
	responseBody := "test response body"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(responseBody))
	}))
	defer upstream.Close()

	cfg := testConfig()
	logger := testLogger()

	tmpDir := t.TempDir()
	ca, _ := langleytls.LoadOrCreateCA(tmpDir)
	certCache := langleytls.NewCertCache(ca, 100)

	// Create redactor with RawBodyStorage=false (the default)
	redactor, _ := redact.New(&config.RedactionConfig{
		RawBodyStorage: false, // Explicitly OFF
	})
	mockStore := newMockStore()

	capture := &flowCapture{}
	proxy, _ := NewMITMProxy(MITMProxyConfig{
		Config:    cfg,
		Logger:    logger,
		CA:        ca,
		CertCache: certCache,
		Redactor:  redactor,
		Store:     mockStore,
		OnUpdate:  capture.OnUpdate,
	})

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(mustParseURL(t, proxyServer.URL)),
		},
	}

	req, _ := http.NewRequest("POST", upstream.URL+"/test", strings.NewReader(testBody))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	// With RawBodyStorage=false, bodies should NOT be stored
	capturedFlow := capture.WaitForFlow(2 * time.Second)
	if capturedFlow == nil {
		t.Fatal("expected captured flow")
	}
	if capturedFlow.RequestBody != nil {
		t.Errorf("RequestBody should be nil when RawBodyStorage=false, got %q", *capturedFlow.RequestBody)
	}
	if capturedFlow.ResponseBody != nil {
		t.Errorf("ResponseBody should be nil when RawBodyStorage=false, got %q", *capturedFlow.ResponseBody)
	}

	// Verify that metadata is still captured
	if capturedFlow.Method != "POST" {
		t.Errorf("Method = %q, want POST", capturedFlow.Method)
	}
	if capturedFlow.StatusCode == nil || *capturedFlow.StatusCode != 200 {
		t.Error("StatusCode should be 200")
	}
}

func TestNewMITMProxy_Validation(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ca, _ := langleytls.LoadOrCreateCA(tmpDir)
	certCache := langleytls.NewCertCache(ca, 100)

	t.Run("nil config", func(t *testing.T) {
		_, err := NewMITMProxy(MITMProxyConfig{
			CA:        ca,
			CertCache: certCache,
		})
		if err == nil {
			t.Error("expected error for nil config")
		}
	})

	t.Run("nil CA", func(t *testing.T) {
		_, err := NewMITMProxy(MITMProxyConfig{
			Config:    testConfig(),
			CertCache: certCache,
		})
		if err == nil {
			t.Error("expected error for nil CA")
		}
	})

	t.Run("nil CertCache", func(t *testing.T) {
		_, err := NewMITMProxy(MITMProxyConfig{
			Config: testConfig(),
			CA:     ca,
		})
		if err == nil {
			t.Error("expected error for nil CertCache")
		}
	})
}

// Helper to parse URL for tests
func mustParseURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("failed to parse URL %q: %v", rawURL, err)
	}
	return u
}

// TestMITMProxy_CONNECT_SSE tests SSE streaming through HTTPS CONNECT tunnel.
// This is the critical path used by Claude CLI and other HTTPS clients.
func TestMITMProxy_CONNECT_SSE(t *testing.T) {
	t.Parallel()

	// Create mock HTTPS upstream with SSE
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter doesn't support flushing")
			return
		}

		// Send SSE events
		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\"}\n\n",
			"event: content_block_delta\ndata: {\"delta\":\"Hello from CONNECT tunnel!\"}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}

		for _, event := range events {
			w.Write([]byte(event))
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	// Create proxy components
	cfg := testConfig()
	logger := testLogger()

	tmpDir := t.TempDir()
	ca, err := langleytls.LoadOrCreateCA(tmpDir)
	if err != nil {
		t.Fatalf("failed to create CA: %v", err)
	}

	certCache := langleytls.NewCertCache(ca, 100)
	redactor, _ := redact.New(&config.RedactionConfig{})
	mockStore := newMockStore()

	capture := &flowCapture{}

	proxy, err := NewMITMProxy(MITMProxyConfig{
		Config:                     cfg,
		Logger:                     logger,
		CA:                         ca,
		CertCache:                  certCache,
		Redactor:                   redactor,
		Store:                      mockStore,
		OnFlow:                     capture.OnFlow,
		OnUpdate:                   capture.OnUpdate,
		OnEvent:                    capture.OnEvent,
		InsecureSkipVerifyUpstream: true, // Test only - skip upstream cert validation
	})
	if err != nil {
		t.Fatalf("NewMITMProxy failed: %v", err)
	}

	// Start actual proxy server (not httptest.NewServer which doesn't support CONNECT properly)
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start proxy listener: %v", err)
	}
	defer proxyListener.Close()

	proxyAddr := proxyListener.Addr().String()
	go func() { _ = http.Serve(proxyListener, proxy) }()

	// Parse upstream URL
	upstreamURL, _ := url.Parse(upstream.URL)

	// Create HTTP client that uses CONNECT proxy and trusts our CA
	proxyURL, _ := url.Parse("http://" + proxyAddr)
	certPool := x509.NewCertPool()
	certPool.AppendCertsFromPEM(ca.CertPEM())
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs: certPool,
			},
		},
	}

	// Make HTTPS request through CONNECT tunnel
	resp, err := client.Get(upstream.URL + "/messages")
	if err != nil {
		t.Fatalf("CONNECT request failed: %v", err)
	}
	defer resp.Body.Close()

	// Read entire response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	// Verify SSE content was received
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "message_start") {
		t.Errorf("response missing message_start event, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "Hello from CONNECT tunnel!") {
		t.Errorf("response missing delta content, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "message_stop") {
		t.Errorf("response missing message_stop event, got: %s", bodyStr)
	}

	// Verify flow was captured (wait for async callback)
	capturedFlow := capture.WaitForFlow(2 * time.Second)
	if capturedFlow == nil {
		t.Fatal("flow was not captured")
	}
	if !capturedFlow.IsSSE {
		t.Error("flow should be marked as SSE")
	}
	if capturedFlow.Host != upstreamURL.Host {
		t.Errorf("captured Host = %q, want %q", capturedFlow.Host, upstreamURL.Host)
	}

	// Verify events were captured
	capturedEvents := capture.Events()
	if len(capturedEvents) < 3 {
		t.Errorf("expected at least 3 SSE events, got %d", len(capturedEvents))
	}
}

func TestLimitedBuffer(t *testing.T) {
	t.Parallel()

	t.Run("within limit", func(t *testing.T) {
		var buf bytes.Buffer
		lb := &limitedBuffer{buf: &buf, max: 100}

		n, err := lb.Write([]byte("hello"))
		if err != nil {
			t.Errorf("Write error: %v", err)
		}
		if n != 5 {
			t.Errorf("n = %d, want 5", n)
		}
		if lb.truncated {
			t.Error("should not be truncated")
		}
		if buf.String() != "hello" {
			t.Errorf("buf = %q, want %q", buf.String(), "hello")
		}
	})

	t.Run("exceeds limit", func(t *testing.T) {
		var buf bytes.Buffer
		lb := &limitedBuffer{buf: &buf, max: 5}

		// First write within limit
		_, _ = lb.Write([]byte("hel"))

		// Second write exceeds limit
		n, err := lb.Write([]byte("lo world"))
		if err != nil {
			t.Errorf("Write error: %v", err)
		}

		// Should pretend to write all but only store up to limit
		if !lb.truncated {
			t.Error("should be truncated")
		}
		if buf.Len() > 5 {
			t.Errorf("buf len = %d, should be <= 5", buf.Len())
		}
		// n should be length of what we tried to write
		if n < 2 {
			t.Errorf("n = %d, should be at least 2", n)
		}
	})

	t.Run("already at limit", func(t *testing.T) {
		var buf bytes.Buffer
		lb := &limitedBuffer{buf: &buf, max: 5}

		_, _ = lb.Write([]byte("12345"))

		// Now at limit, further writes should be ignored
		n, err := lb.Write([]byte("more"))
		if err != nil {
			t.Errorf("Write error: %v", err)
		}
		if n != 4 {
			t.Errorf("n = %d, want 4 (pretend success)", n)
		}
		if !lb.truncated {
			t.Error("should be truncated")
		}
		if buf.Len() != 5 {
			t.Errorf("buf len = %d, want 5", buf.Len())
		}
	})
}
