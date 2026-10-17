package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"encoding/json"

	"github.com/HakAl/langley/internal/analytics"
	"github.com/HakAl/langley/internal/config"
	"github.com/HakAl/langley/internal/parser"
	"github.com/HakAl/langley/internal/pricing"
	"github.com/HakAl/langley/internal/provider"
	"github.com/HakAl/langley/internal/redact"
	"github.com/HakAl/langley/internal/store"
	"github.com/HakAl/langley/internal/task"
	langleytls "github.com/HakAl/langley/internal/tls"
	"github.com/google/uuid"
)

// CaptureController describes the capture lifecycle exposed to control planes.
type CaptureController interface {
	CaptureStatus() CaptureStatus
	SetCapture(enabled bool, force bool) CaptureStatus
}

const (
	CaptureStatePassthrough = "passthrough"
	CaptureStateEnabling    = "enabling"
	CaptureStateCapturing   = "capturing"
	CaptureStateStopping    = "stopping"
)

type CaptureStatus struct {
	State                      string   `json:"state"`
	DrainingPassthroughTunnels int      `json:"draining_passthrough_tunnels"`
	DrainingMITMRequests       int      `json:"draining_mitm_requests"`
	InterceptHosts             []string `json:"intercept_hosts"`
	ProxyAddr                  string   `json:"proxy_addr,omitempty"`
	InstanceID                 string   `json:"instance_id,omitempty"`
	Revision                   uint64   `json:"revision"`
}

// captureSession owns the raw transports; busy spans processing and persistence.
type captureSession struct {
	client, upstream        net.Conn
	busy                    bool
	awaitingFirstByte       bool
	awaitingFirstByteAtomic atomic.Bool
	firstByteRead           atomic.Bool
	pendingReadByte         *byte
	readSeq                 uint64
	readDone                chan struct{}
}

const unreadFirstByteSentinel byte = 0xff

type firstByteSlotConn struct {
	net.Conn
	p       *MITMProxy
	session *captureSession
}

func (c *firstByteSlotConn) Read(b []byte) (int, error) {
	c.p.prepareFirstByteRead(c.session, b)
	n, err := c.Conn.Read(b)
	c.p.finishFirstByteRead(c.session, n)
	return n, err
}

type rawReadEvidenceConn struct {
	net.Conn
	session *captureSession
}

func (c *rawReadEvidenceConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 && c.session.awaitingFirstByteAtomic.Load() {
		c.session.firstByteRead.Store(true)
	}
	return n, err
}

type rawReadEvidenceReader struct {
	r       io.Reader
	p       *MITMProxy
	session *captureSession
}

func (r *rawReadEvidenceReader) Read(b []byte) (int, error) {
	r.p.preparePlaintextFirstByteRead(r.session, b)
	n, err := r.r.Read(b)
	r.p.finishFirstByteRead(r.session, n)
	return n, err
}

type captureAdmissionConn struct {
	net.Conn
	p       *MITMProxy
	session *captureSession
}

func (c *captureAdmissionConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.p.admitMITMFirstByte(c.session)
	}
	return n, err
}

type captureAdmissionReader struct {
	r       io.Reader
	p       *MITMProxy
	session *captureSession
}

func (a *captureAdmissionReader) Read(b []byte) (int, error) {
	n, err := a.r.Read(b)
	if n > 0 {
		a.p.admitMITMFirstByte(a.session)
	}
	return n, err
}

type captureTunnel struct {
	client, upstream net.Conn
	lastActivity     atomic.Int64
	draining         bool // protected by captureMu
}

// MITMProxy is an intercepting proxy that captures TLS traffic.
type MITMProxy struct {
	captureMu                sync.Mutex
	captureWanted            bool
	captureOpSeq             uint64
	captureTunnels           map[*captureTunnel]struct{}
	captureSessions          map[*captureSession]struct{}
	inFlightMITMRequests     int
	passthroughIdleThreshold time.Duration
	proxyAddr                string
	onCaptureStateChange     func(CaptureStatus)
	captureRevision          uint64
	notifyQueue              []CaptureStatus
	notifying                bool

	cfg          *config.Config
	logger       *slog.Logger
	ca           *langleytls.CA
	certCache    *langleytls.CertCache
	redactor     *redact.Redactor
	store        store.Store
	analytics    *analytics.Engine
	taskAssigner *task.Assigner
	providers    *provider.Registry
	server       *http.Server
	client       *http.Client

	// Callbacks for real-time updates
	onFlow   func(*store.Flow)
	onUpdate func(*store.Flow)
	onEvent  func(*store.Event)

	// Graceful shutdown: own hijacked sessions from admission through cleanup.
	tunnelMu     sync.Mutex
	tunnelConns  map[net.Conn]struct{}
	tunnelWg     sync.WaitGroup
	shuttingDown bool
	shutdownCh   chan struct{}
	dialHook     func(ctx context.Context, network, addr string) (net.Conn, error)

	// insecureSkipVerifyUpstream is for testing only
	insecureSkipVerifyUpstream bool
}

// MITMProxyConfig holds configuration for creating a MITM proxy.
type MITMProxyConfig struct {
	StartInPassthrough       bool
	PassthroughIdleThreshold time.Duration
	ProxyAddr                string
	OnCaptureStateChange     func(CaptureStatus)

	Config        *config.Config
	Logger        *slog.Logger
	CA            *langleytls.CA
	CertCache     *langleytls.CertCache
	Redactor      *redact.Redactor
	Store         store.Store
	TaskAssigner  *task.Assigner
	PricingSource *pricing.Source // LiteLLM pricing for cost calculations
	OnFlow        func(*store.Flow)
	OnUpdate      func(*store.Flow)
	OnEvent       func(*store.Event) // Called for each SSE event parsed

	// InsecureSkipVerifyUpstream skips TLS verification for upstream connections.
	// This should ONLY be used for testing. Do not enable in production.
	InsecureSkipVerifyUpstream bool
}

// NewMITMProxy creates a new MITM proxy.
func NewMITMProxy(cfg MITMProxyConfig) (*MITMProxy, error) {
	if cfg.Config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if cfg.CA == nil {
		return nil, fmt.Errorf("CA is required")
	}
	if cfg.CertCache == nil {
		return nil, fmt.Errorf("CertCache is required")
	}
	if cfg.Redactor == nil {
		return nil, fmt.Errorf("Redactor is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	// HTTP client for forwarding requests
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,                // Validate upstream (langley-vu5)
			NextProtos:         []string{"http/1.1"}, // Force HTTP/1.1 (langley-a4m)
		},
		ForceAttemptHTTP2:     false, // Disable HTTP/2 (langley-a4m)
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 0, // No timeout for streaming
	}

	if cfg.PassthroughIdleThreshold <= 0 {
		cfg.PassthroughIdleThreshold = 500 * time.Millisecond
	}
	p := &MITMProxy{
		captureWanted:              !cfg.StartInPassthrough,
		captureTunnels:             make(map[*captureTunnel]struct{}),
		captureSessions:            make(map[*captureSession]struct{}),
		passthroughIdleThreshold:   cfg.PassthroughIdleThreshold,
		proxyAddr:                  cfg.ProxyAddr,
		onCaptureStateChange:       cfg.OnCaptureStateChange,
		cfg:                        cfg.Config,
		logger:                     cfg.Logger,
		ca:                         cfg.CA,
		certCache:                  cfg.CertCache,
		redactor:                   cfg.Redactor,
		store:                      cfg.Store,
		taskAssigner:               cfg.TaskAssigner,
		providers:                  provider.NewRegistry(),
		client:                     client,
		onFlow:                     cfg.OnFlow,
		onUpdate:                   cfg.OnUpdate,
		onEvent:                    cfg.OnEvent,
		tunnelConns:                make(map[net.Conn]struct{}),
		insecureSkipVerifyUpstream: cfg.InsecureSkipVerifyUpstream,
	}

	// Initialize analytics engine if we have a database connection
	if cfg.Store != nil {
		if db, ok := cfg.Store.DB().(*sql.DB); ok {
			p.analytics = analytics.NewEngine(db)
			if cfg.PricingSource != nil {
				p.analytics.SetPricingSource(cfg.PricingSource)
			}
		}
	}

	p.server = &http.Server{
		Addr:         cfg.Config.Proxy.ListenAddr(),
		Handler:      p,
		ReadTimeout:  0,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	return p, nil
}

// Serve starts the proxy server by creating its own listener.
func (p *MITMProxy) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", p.server.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	return p.ServeListener(ctx, ln)
}

// ServeListener starts the proxy server using the provided listener.
// This allows the caller to manage port allocation (e.g., for fallback logic).
func (p *MITMProxy) ServeListener(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		p.logger.Info("shutting down MITM proxy")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = p.server.Shutdown(shutdownCtx)
	}()

	p.logger.Info("MITM proxy listening", "addr", ln.Addr().String())
	if err := p.server.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve: %w", err)
	}

	// Drain hijacked passthrough tunnels (langley-ga3l)
	p.closeTunnels()
	p.tunnelWg.Wait()

	return nil
}

// ServeHTTP handles incoming HTTP requests.
func (p *MITMProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.logger.Debug("incoming request", "method", r.Method, "host", r.Host, "url", r.URL.String())
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleHTTP(w, r)
}

// handleHTTP handles regular HTTP requests.
func (p *MITMProxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if !p.isCaptureWanted() {
		req := r.Clone(r.Context())
		req.RequestURI = ""
		resp, err := p.client.Do(req)
		if err != nil {
			http.Error(w, "Bad gateway", http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		for k, values := range resp.Header {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(newFlushWriter(w), resp.Body); err != nil {
			p.logger.Debug("passthrough HTTP copy failed", "error", err)
		}
		return
	}

	startTime := time.Now()
	flowID := uuid.New().String()

	p.logger.Debug("HTTP request", "flow_id", flowID, "method", r.Method, "url", r.URL.String())

	// Read full request body for forwarding and parsing.
	// Only the stored copy in flow.RequestBody is truncated to BodyMaxBytes.
	var reqBody []byte
	var reqBodyTruncated bool
	if r.Body != nil {
		reqBody, _ = io.ReadAll(r.Body)
		r.Body.Close()
		reqBodyTruncated = len(reqBody) > p.cfg.Persistence.BodyMaxBytes
		r.Body = io.NopCloser(bytes.NewReader(reqBody))
	}

	// Create flow record
	flow := &store.Flow{
		ID:                   flowID,
		Host:                 r.Host,
		Method:               r.Method,
		Path:                 r.URL.Path,
		URL:                  r.URL.String(),
		Timestamp:            startTime,
		TimestampMono:        time.Now().UnixNano(),
		FlowIntegrity:        "complete",
		Provider:             "other",
		RequestBodyTruncated: reqBodyTruncated,
	}

	// Assign task
	if p.taskAssigner != nil {
		assignment := p.taskAssigner.Assign(r.Host, r.Header, reqBody)
		flow.TaskID = &assignment.TaskID
		flow.TaskSource = &assignment.Source
	}

	// Redact and store request (body truncated to BodyMaxBytes for storage only)
	storedBody := reqBody
	if len(storedBody) > p.cfg.Persistence.BodyMaxBytes {
		storedBody = storedBody[:p.cfg.Persistence.BodyMaxBytes]
	}
	if p.redactor != nil {
		flow.RequestHeaders = redact.HeadersToMap(p.redactor.RedactHeaders(r.Header))
		if p.redactor.ShouldStoreBody() && len(storedBody) > 0 {
			redacted := p.redactor.RedactBody(string(storedBody))
			flow.RequestBody = &redacted
		}
	} else {
		flow.RequestHeaders = redact.HeadersToMap(r.Header)
		if len(storedBody) > 0 {
			s := string(storedBody)
			flow.RequestBody = &s
		}
	}

	// Save flow immediately so SSE events can reference it (langley-2fa)
	if p.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := p.store.SaveFlow(ctx, flow); err != nil {
			p.logger.Error("failed to save initial flow", "flow_id", flow.ID, "error", err)
		}
		cancel()
	}

	// Correlate tool_results in request body with prior tool invocations (langley-io4)
	p.correlateToolResults(reqBody)

	// Notify flow started
	if p.onFlow != nil {
		p.onFlow(flow)
	}

	// Forward request
	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), bytes.NewReader(reqBody))
	if err != nil {
		p.logger.Error("failed to create request", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	copyHeaders(outReq.Header, r.Header)
	removeHopByHopHeaders(outReq.Header)
	// Strip Accept-Encoding so upstream sends uncompressed responses.
	// The proxy needs plaintext to store readable bodies and parse usage/SSE.
	outReq.Header.Del("Accept-Encoding")

	resp, err := p.client.Do(outReq)
	if err != nil {
		p.logger.Error("failed to forward request", "error", err)
		http.Error(w, "Bad gateway", http.StatusBadGateway)
		flow.FlowIntegrity = "interrupted"
		p.saveFlow(flow)
		return
	}
	defer resp.Body.Close()

	// Update flow with response info
	duration := time.Since(startTime).Milliseconds()
	flow.DurationMs = &duration
	flow.StatusCode = &resp.StatusCode
	statusText := resp.Status
	flow.StatusText = &statusText

	// Check if SSE
	contentType := resp.Header.Get("Content-Type")
	flow.IsSSE = strings.Contains(contentType, "text/event-stream")

	// Copy response headers
	copyHeaders(w.Header(), resp.Header)
	removeHopByHopHeaders(w.Header())
	w.WriteHeader(resp.StatusCode)

	// Stream response body while capturing
	var respBody bytes.Buffer
	maxBody := p.cfg.Persistence.BodyMaxBytes
	limitedWriter := &limitedBuffer{buf: &respBody, max: maxBody}

	// Use SSE parser for event-stream responses
	if flow.IsSSE {
		// For SSE, wrap ResponseWriter with flusher to ensure immediate delivery
		flushWriter := newFlushWriter(w)
		if err := p.streamSSEWithParser(flowID, flow.TaskID, resp.Body, flushWriter, limitedWriter); err != nil {
			p.logger.Debug("error streaming SSE response", "error", err)
		}
	} else {
		multiWriter := io.MultiWriter(w, limitedWriter)
		if _, err := io.Copy(multiWriter, resp.Body); err != nil {
			p.logger.Debug("error copying response", "error", err)
		}
	}

	// Finalize flow
	if p.redactor != nil {
		flow.ResponseHeaders = redact.HeadersToMap(p.redactor.RedactHeaders(resp.Header))
		if p.redactor.ShouldStoreBody() && respBody.Len() > 0 {
			redacted := p.redactor.RedactBody(respBody.String())
			flow.ResponseBody = &redacted
		}
	} else {
		flow.ResponseHeaders = redact.HeadersToMap(resp.Header)
		if respBody.Len() > 0 {
			s := respBody.String()
			flow.ResponseBody = &s
		}
	}
	flow.ResponseBodyTruncated = limitedWriter.truncated

	// Detect provider and extract usage from captured body
	if prov := p.providers.Detect(r.Host); prov != nil {
		flow.Provider = prov.Name()
		if respBody.Len() > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			p.extractUsageAndCost(ctx, flow, prov, respBody.Bytes())
			cancel()
		}
	}

	// Extract tool invocations from non-streaming JSON responses (langley-ahgo)
	if !flow.IsSSE && respBody.Len() > 0 {
		tools := parser.ExtractToolUsesFromJSON(respBody.Bytes())
		p.saveToolInvocations(flowID, flow.TaskID, tools)
	}

	// Save flow
	p.saveFlow(flow)

	// Notify update
	if p.onUpdate != nil {
		p.onUpdate(flow)
	}
}

// handleConnect routes HTTPS CONNECT requests: MITM for known LLM hosts,
// transparent passthrough for everything else.
func (p *MITMProxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	p.logger.Debug("CONNECT request", "host", r.Host)

	if p.shouldIntercept(r.Host) && p.isCaptureWanted() {
		p.handleConnectMITM(w, r)
		return
	}
	p.handleConnectPassthrough(w, r)
}

// shouldIntercept returns true if the host should be MITM'd — either it's a
// built-in provider host or the user added it to intercept_hosts config.
func (p *MITMProxy) shouldIntercept(host string) bool {
	if p.providers.ShouldIntercept(host) {
		return true
	}
	return matchConfigHosts(host, p.cfg.Proxy.InterceptHosts)
}

// matchConfigHosts checks whether host matches any entry in the user-configured
// intercept_hosts list using domain-suffix matching.
func matchConfigHosts(host string, interceptHosts []string) bool {
	for _, h := range interceptHosts {
		if provider.MatchDomainSuffix(host, h) {
			return true
		}
	}
	return false
}

// beginHijackedSession admits sessions atomically with shutdown.
func (p *MITMProxy) beginHijackedSession(conns ...net.Conn) bool {
	p.tunnelMu.Lock()
	if p.shuttingDown {
		p.tunnelMu.Unlock()
		for _, c := range conns {
			if c != nil {
				c.Close()
			}
		}
		return false
	}
	p.tunnelWg.Add(1)
	for _, c := range conns {
		if c != nil {
			p.tunnelConns[c] = struct{}{}
		}
	}
	p.tunnelMu.Unlock()
	return true
}

func (p *MITMProxy) registerSessionConn(c net.Conn) bool {
	p.tunnelMu.Lock()
	if c == nil {
		accepting := !p.shuttingDown
		p.tunnelMu.Unlock()
		return accepting
	}
	if p.shuttingDown {
		p.tunnelMu.Unlock()
		c.Close()
		return false
	}
	p.tunnelConns[c] = struct{}{}
	p.tunnelMu.Unlock()
	return true
}

// endHijackedSession releases ownership only after transport cleanup finishes.
func (p *MITMProxy) endHijackedSession(closers []io.Closer, tracked ...net.Conn) {
	for _, c := range closers {
		if c != nil {
			c.Close()
		}
	}
	p.tunnelMu.Lock()
	for _, c := range tracked {
		if c != nil {
			delete(p.tunnelConns, c)
		}
	}
	p.tunnelMu.Unlock()
	p.tunnelWg.Done()
}

func (p *MITMProxy) closeTunnels() {
	p.tunnelMu.Lock()
	defer p.tunnelMu.Unlock()
	if p.shutdownCh == nil {
		p.shutdownCh = make(chan struct{})
	}
	if !p.shuttingDown {
		p.shuttingDown = true
		close(p.shutdownCh)
	}
	for c := range p.tunnelConns {
		c.Close()
	}
}

func (p *MITMProxy) dialUpstreamTCP(host string, timeout time.Duration) (net.Conn, error) {
	p.tunnelMu.Lock()
	if p.shutdownCh == nil {
		p.shutdownCh = make(chan struct{})
	}
	ch := p.shutdownCh
	shuttingDown := p.shuttingDown
	dialFn := p.dialHook
	p.tunnelMu.Unlock()

	if shuttingDown {
		return nil, net.ErrClosed
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ch:
			cancel()
		case <-done:
		}
	}()

	if dialFn != nil {
		return dialFn(ctx, "tcp", host)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", host)
}

// handleConnectPassthrough tunnels the connection transparently without MITM.
// The client sees the upstream server's real TLS certificate.
func (p *MITMProxy) handleConnectPassthrough(w http.ResponseWriter, r *http.Request) {
	// Parse host for upstream connection
	host := r.Host
	if !strings.Contains(host, ":") {
		host = host + ":443"
	}

	// Dial upstream BEFORE sending 200 OK — so we can report errors properly
	upstreamConn, err := p.dialUpstreamTCP(host, 10*time.Second)
	if err != nil {
		p.logger.Error("passthrough: failed to connect to upstream", "host", host, "error", err)
		http.Error(w, "Bad gateway", http.StatusBadGateway)
		return
	}

	// Now hijack the client connection
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		p.logger.Error("hijacking not supported")
		http.Error(w, "Internal error", http.StatusInternalServerError)
		upstreamConn.Close()
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		p.logger.Error("failed to hijack connection", "error", err)
		upstreamConn.Close()
		return
	}

	if !p.beginHijackedSession(clientConn, upstreamConn) {
		return
	}

	var target *captureTunnel
	if p.shouldIntercept(r.Host) {
		target = &captureTunnel{client: clientConn, upstream: upstreamConn}
		p.captureMu.Lock()
		p.captureTunnels[target] = struct{}{}
		// Capture may have been enabled while CONNECT was dialing.
		if p.captureWanted {
			target.draining = true
			_ = clientConn.Close()
			_ = upstreamConn.Close()
		}
		p.captureMu.Unlock()
	}
	// Send 200 OK — upstream is reachable
	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		p.logger.Error("failed to write tunnel response", "error", err)
		p.endHijackedSession([]io.Closer{clientConn, upstreamConn}, clientConn, upstreamConn)
		if target != nil {
			p.finishCaptureTunnel(target)
		}
		return
	}

	// Hand off to bidirectional tunnel with idle timeout (langley-ga3l)
	go func() {
		defer p.endHijackedSession([]io.Closer{clientConn, upstreamConn}, clientConn, upstreamConn)
		if target != nil {
			defer p.finishCaptureTunnel(target)
			p.tunnelCapture(target)
		} else {
			tunnel(clientConn, upstreamConn, p.logger, r.Host)
		}
	}()
}

// handleConnectMITM handles HTTPS CONNECT requests with TLS interception.
func (p *MITMProxy) handleConnectMITM(w http.ResponseWriter, r *http.Request) {
	// Hijack the connection
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		p.logger.Error("hijacking not supported")
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		p.logger.Error("failed to hijack connection", "error", err)
		return
	}

	if !p.beginHijackedSession(clientConn) {
		return
	}
	session := &captureSession{client: clientConn}
	p.captureMu.Lock()
	if p.captureSessions == nil {
		p.captureSessions = make(map[*captureSession]struct{})
		p.captureWanted = true
	}
	if !p.captureWanted {
		p.captureMu.Unlock()
		p.endHijackedSession([]io.Closer{clientConn}, clientConn)
		return
	}
	p.captureSessions[session] = struct{}{}
	p.captureMu.Unlock()
	defer func() { p.captureMu.Lock(); delete(p.captureSessions, session); p.captureMu.Unlock() }()
	closers := []io.Closer{clientConn}
	tracked := []net.Conn{clientConn}
	defer func() { p.endHijackedSession(closers, tracked...) }()

	// Send 200 OK
	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		p.logger.Error("failed to write tunnel response", "error", err)
		clientConn.Close()
		return
	}

	// Start TLS handshake with client using generated cert
	// Explicitly negotiate HTTP/1.1 to prevent HTTP/2 issues (langley-a4m)
	tlsConfig := &tls.Config{
		GetCertificate: p.certCache.GetCertificate,
		NextProtos:     []string{"http/1.1"},
	}
	slotConn := &firstByteSlotConn{Conn: clientConn, p: p, session: session}
	evidenceConn := &rawReadEvidenceConn{Conn: slotConn, session: session}
	tlsConn := tls.Server(&captureAdmissionConn{Conn: evidenceConn, p: p, session: session}, tlsConfig)
	closers = append(closers, tlsConn)
	if err := tlsConn.Handshake(); err != nil {
		p.logger.Debug("TLS handshake failed", "host", r.Host, "error", err)
		clientConn.Close()
		return
	}

	// Log negotiated protocol for debugging (langley-a4m)
	p.logger.Debug("TLS handshake complete", "host", r.Host, "negotiated_protocol", tlsConn.ConnectionState().NegotiatedProtocol)

	// Parse host for upstream connection
	host := r.Host
	if !strings.Contains(host, ":") {
		host = host + ":443"
	}

	// Connect to upstream - force HTTP/1.1 to match client negotiation (langley-a4m)
	rawUpstream, err := p.dialUpstreamTCP(host, 10*time.Second)
	if err != nil {
		p.logger.Error("failed to connect to upstream", "host", host, "error", err)
		return
	}
	if !p.registerSessionConn(rawUpstream) {
		return
	}
	closers = append(closers, rawUpstream)
	tracked = append(tracked, rawUpstream)
	p.captureMu.Lock()
	session.upstream = rawUpstream
	wanted := p.captureWanted
	p.captureMu.Unlock()
	if !wanted {
		_ = rawUpstream.Close()
		return
	}
	serverName, _, err := net.SplitHostPort(host)
	if err != nil {
		return
	}
	upstreamConn := tls.Client(rawUpstream, &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: p.insecureSkipVerifyUpstream, // Only skip for testing (langley-vu5)
		NextProtos:         []string{"http/1.1"},
	})
	closers = append(closers, upstreamConn)
	if err := upstreamConn.Handshake(); err != nil {
		p.logger.Error("failed to handshake with upstream", "host", host, "error", err)
		return
	}

	// Handle requests on this connection
	p.serveTLSCaptureSession(tlsConn, upstreamConn, r.Host, session)
}

// underlyingConn returns the raw net.Conn beneath a *tls.Conn so shutdown
// can close the transport directly without blocking on TLS closeNotify writes.
func underlyingConn(c *tls.Conn) net.Conn {
	if nc := c.NetConn(); nc != nil {
		return nc
	}
	return c
}

// handleTLSConnection handles HTTP requests over an established TLS connection.
func (p *MITMProxy) handleTLSConnection(clientConn *tls.Conn, upstreamConn *tls.Conn, host string) {
	rawClient := underlyingConn(clientConn)
	rawUpstream := underlyingConn(upstreamConn)
	if !p.beginHijackedSession(rawClient, rawUpstream) {
		return
	}
	defer p.endHijackedSession([]io.Closer{rawClient, rawUpstream, clientConn, upstreamConn}, rawClient, rawUpstream)
	p.serveTLSConnection(clientConn, upstreamConn, host)
}

func (p *MITMProxy) serveTLSConnection(clientConn *tls.Conn, upstreamConn *tls.Conn, host string) {
	session := &captureSession{client: underlyingConn(clientConn), upstream: underlyingConn(upstreamConn)}
	p.captureMu.Lock()
	if p.captureSessions == nil {
		p.captureSessions = make(map[*captureSession]struct{})
		p.captureWanted = true
	}
	p.captureSessions[session] = struct{}{}
	p.captureMu.Unlock()
	defer func() {
		p.captureMu.Lock()
		delete(p.captureSessions, session)
		p.captureMu.Unlock()
	}()
	p.serveTLSCaptureSession(clientConn, upstreamConn, host, session)
}

func (p *MITMProxy) prepareFirstByteRead(session *captureSession, b []byte) {
	if len(b) == 0 || !session.awaitingFirstByteAtomic.Load() {
		return
	}
	p.captureMu.Lock()
	if session.awaitingFirstByte && !session.busy {
		b[0] = unreadFirstByteSentinel
		session.pendingReadByte = &b[0]
	}
	p.captureMu.Unlock()
}

func (p *MITMProxy) preparePlaintextFirstByteRead(session *captureSession, b []byte) {
	if len(b) == 0 || !session.awaitingFirstByteAtomic.Load() {
		return
	}
	p.captureMu.Lock()
	if session.awaitingFirstByte && !session.busy && session.pendingReadByte == nil {
		b[0] = unreadFirstByteSentinel
		session.pendingReadByte = &b[0]
	}
	p.captureMu.Unlock()
}

func (p *MITMProxy) finishFirstByteRead(session *captureSession, n int) {
	if n > 0 && session.awaitingFirstByteAtomic.Load() {
		session.firstByteRead.Store(true)
	}
	p.captureMu.Lock()
	session.pendingReadByte = nil
	p.captureMu.Unlock()
}

func hasFirstByteEvidenceLocked(session *captureSession) bool {
	if session.firstByteRead.Load() {
		return true
	}
	return session.pendingReadByte != nil && *session.pendingReadByte != unreadFirstByteSentinel
}

func (p *MITMProxy) admitMITMFirstByteLocked(session *captureSession) {
	if session.awaitingFirstByte {
		session.awaitingFirstByte = false
		session.awaitingFirstByteAtomic.Store(false)
		session.firstByteRead.Store(true)
		session.pendingReadByte = nil
		_ = session.client.SetReadDeadline(time.Time{})
		if !session.busy {
			session.busy = true
			p.inFlightMITMRequests++
		}
	}
}

func (p *MITMProxy) admitMITMFirstByte(session *captureSession) {
	p.captureMu.Lock()
	p.admitMITMFirstByteLocked(session)
	p.captureMu.Unlock()
}

func (p *MITMProxy) cancelMITMAdmission(session *captureSession) {
	p.captureMu.Lock()
	session.awaitingFirstByte = false
	session.awaitingFirstByteAtomic.Store(false)
	session.firstByteRead.Store(false)
	session.pendingReadByte = nil
	wasBusy := session.busy
	if wasBusy {
		before := p.captureStatusLocked()
		session.busy = false
		p.inFlightMITMRequests--
		_ = p.recordTransitionLocked(before)
	}
	p.captureMu.Unlock()
	if wasBusy {
		p.flushCaptureNotifications()
	}
}

func (p *MITMProxy) serveTLSCaptureSession(clientConn, upstreamConn *tls.Conn, host string, session *captureSession) {
	evidenceReader := &rawReadEvidenceReader{r: clientConn, p: p, session: session}
	clientReader := bufio.NewReader(&captureAdmissionReader{r: evidenceReader, p: p, session: session})
	upstreamReader := bufio.NewReader(upstreamConn)

	for {
		var readDone chan struct{}
		p.captureMu.Lock()
		if !p.captureWanted {
			p.captureMu.Unlock()
			_ = session.client.Close()
			if session.upstream != nil {
				_ = session.upstream.Close()
			}
			return
		}
		session.readSeq++
		session.pendingReadByte = nil
		if clientReader.Buffered() > 0 {
			session.awaitingFirstByte = false
			session.awaitingFirstByteAtomic.Store(false)
			session.firstByteRead.Store(true)
			session.busy = true
			p.inFlightMITMRequests++
		} else {
			session.firstByteRead.Store(false)
			session.awaitingFirstByte = true
			session.awaitingFirstByteAtomic.Store(true)
			readDone = make(chan struct{})
			session.readDone = readDone
		}
		p.captureMu.Unlock()

		// Wait for the first byte of a request while session.busy is false so
		// genuinely idle keep-alive sessions can be closed immediately on pause.
		_, peekErr := clientReader.Peek(1)
		if readDone != nil {
			close(readDone)
		}
		if peekErr != nil {
			if peekErr != io.EOF {
				p.logger.Debug("error awaiting request from TLS connection", "host", host, "error", peekErr)
			}
			p.cancelMITMAdmission(session)
			_ = session.client.Close()
			if session.upstream != nil {
				_ = session.upstream.Close()
			}
			return
		}
		p.captureMu.Lock()
		admitted := session.busy
		p.captureMu.Unlock()
		if !admitted {
			_ = session.client.Close()
			if session.upstream != nil {
				_ = session.upstream.Close()
			}
			return
		}

		// Read full request headers from client now that the session is marked busy.
		req, err := http.ReadRequest(clientReader)
		if err != nil {
			if err != io.EOF {
				p.logger.Debug("error reading request from TLS connection", "host", host, "error", err)
			}
			p.finishMITMRequest(session, true)
			return
		}
		p.logger.Debug("read request from TLS connection", "host", host, "method", req.Method, "path", req.URL.Path)

		// Fix up the request URL
		req.URL.Scheme = "https"
		req.URL.Host = host

		// Handle this request
		p.handleTLSRequest(req, clientConn, upstreamConn, upstreamReader, host)
		if p.finishMITMRequest(session, false) {
			return
		}
	}
}

// handleTLSRequest handles a single HTTP request over TLS.
func (p *MITMProxy) handleTLSRequest(r *http.Request, clientConn net.Conn, upstreamConn *tls.Conn, upstreamReader *bufio.Reader, host string) {
	startTime := time.Now()
	flowID := uuid.New().String()

	p.logger.Debug("HTTPS request", "flow_id", flowID, "method", r.Method, "host", host, "path", r.URL.Path)

	// Read full request body for forwarding and parsing.
	// Only the stored copy in flow.RequestBody is truncated to BodyMaxBytes.
	var reqBody []byte
	var reqBodyTruncated bool
	if r.Body != nil {
		reqBody, _ = io.ReadAll(r.Body)
		r.Body.Close()
		reqBodyTruncated = len(reqBody) > p.cfg.Persistence.BodyMaxBytes
	}

	// Create flow
	flow := &store.Flow{
		ID:                   flowID,
		Host:                 host,
		Method:               r.Method,
		Path:                 r.URL.Path,
		URL:                  r.URL.String(),
		Timestamp:            startTime,
		TimestampMono:        time.Now().UnixNano(),
		FlowIntegrity:        "complete",
		Provider:             "other",
		RequestBodyTruncated: reqBodyTruncated,
	}

	// Assign task
	if p.taskAssigner != nil {
		assignment := p.taskAssigner.Assign(host, r.Header, reqBody)
		flow.TaskID = &assignment.TaskID
		flow.TaskSource = &assignment.Source
	}

	// Redact and store request (body truncated to BodyMaxBytes for storage only)
	storedBody := reqBody
	if len(storedBody) > p.cfg.Persistence.BodyMaxBytes {
		storedBody = storedBody[:p.cfg.Persistence.BodyMaxBytes]
	}
	if p.redactor != nil {
		flow.RequestHeaders = redact.HeadersToMap(p.redactor.RedactHeaders(r.Header))
		if p.redactor.ShouldStoreBody() && len(storedBody) > 0 {
			redacted := p.redactor.RedactBody(string(storedBody))
			flow.RequestBody = &redacted
		}
	} else {
		flow.RequestHeaders = redact.HeadersToMap(r.Header)
		if len(storedBody) > 0 {
			s := string(storedBody)
			flow.RequestBody = &s
		}
	}

	// Detect provider from host
	if prov := p.providers.Detect(host); prov != nil {
		flow.Provider = prov.Name()
	}

	// Save flow immediately so SSE events can reference it (langley-2fa)
	if p.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := p.store.SaveFlow(ctx, flow); err != nil {
			p.logger.Error("failed to save initial flow", "flow_id", flow.ID, "error", err)
		}
		cancel()
	}

	// Correlate tool_results in request body with prior tool invocations (langley-io4)
	p.correlateToolResults(reqBody)

	// Notify flow started
	if p.onFlow != nil {
		p.onFlow(flow)
	}

	// Forward request to upstream
	outReq, err := http.NewRequest(r.Method, r.URL.String(), bytes.NewReader(reqBody))
	if err != nil {
		p.sendError(clientConn, http.StatusBadRequest, "Bad request")
		return
	}
	copyHeaders(outReq.Header, r.Header)
	removeHopByHopHeaders(outReq.Header)
	// Strip Accept-Encoding so upstream sends uncompressed responses.
	// The proxy needs plaintext to store readable bodies and parse usage/SSE.
	outReq.Header.Del("Accept-Encoding")

	// Write request to upstream
	if err := outReq.Write(upstreamConn); err != nil {
		p.logger.Error("failed to write to upstream", "error", err)
		p.sendError(clientConn, http.StatusBadGateway, "Bad gateway")
		flow.FlowIntegrity = "interrupted"
		p.saveFlow(flow)
		return
	}

	// Read response from upstream
	resp, err := http.ReadResponse(upstreamReader, outReq)
	if err != nil {
		p.logger.Error("failed to read upstream response", "error", err)
		p.sendError(clientConn, http.StatusBadGateway, "Bad gateway")
		flow.FlowIntegrity = "interrupted"
		p.saveFlow(flow)
		return
	}

	// Update flow with response info
	duration := time.Since(startTime).Milliseconds()
	flow.DurationMs = &duration
	flow.StatusCode = &resp.StatusCode
	statusText := resp.Status
	flow.StatusText = &statusText

	// Check if SSE
	contentType := resp.Header.Get("Content-Type")
	flow.IsSSE = strings.Contains(contentType, "text/event-stream")

	// Capture response body
	var respBody bytes.Buffer
	maxBody := p.cfg.Persistence.BodyMaxBytes
	limitedWriter := &limitedBuffer{buf: &respBody, max: maxBody}

	// Build response headers - remove hop-by-hop headers since Go de-chunks automatically
	respHeaders := resp.Header.Clone()
	removeHopByHopHeaders(respHeaders)

	// Handle SSE (streaming) vs regular responses differently (langley-a4m)
	if flow.IsSSE {
		// SSE: Add Transfer-Encoding: chunked since Go de-chunks upstream responses
		// but client needs framing to know when data arrives
		respHeaders.Set("Transfer-Encoding", "chunked")

		// SSE: stream headers immediately, then body
		var responseBuf bytes.Buffer
		fmt.Fprintf(&responseBuf, "HTTP/1.1 %s\r\n", resp.Status)
		_ = respHeaders.Write(&responseBuf)
		responseBuf.WriteString("\r\n")

		p.logger.Debug("sending SSE response headers", "flow_id", flowID, "headers", responseBuf.String())

		if _, err := clientConn.Write(responseBuf.Bytes()); err != nil {
			p.logger.Debug("error writing SSE response headers", "error", err)
			resp.Body.Close()
			return
		}

		// Wrap client connection in chunked writer for proper HTTP/1.1 framing
		chunkedWriter := newChunkedWriter(clientConn)
		if err := p.streamSSEWithParser(flowID, flow.TaskID, resp.Body, chunkedWriter, limitedWriter); err != nil {
			flow.FlowIntegrity = "interrupted"
			p.logger.Debug("error streaming SSE response", "error", err)
		}
		// Write final chunk to signal end of response
		chunkedWriter.Close()
	} else {
		// Non-SSE: buffer body first to set Content-Length (required after removing Transfer-Encoding)
		var bodyBuf bytes.Buffer
		multiWriter := io.MultiWriter(&bodyBuf, limitedWriter)
		if _, err := io.Copy(multiWriter, resp.Body); err != nil {
			p.logger.Debug("error reading response body", "error", err)
		}

		// Set Content-Length based on actual body size
		respHeaders.Set("Content-Length", fmt.Sprintf("%d", bodyBuf.Len()))

		var responseBuf bytes.Buffer
		fmt.Fprintf(&responseBuf, "HTTP/1.1 %s\r\n", resp.Status)
		_ = respHeaders.Write(&responseBuf)
		responseBuf.WriteString("\r\n")

		if _, err := clientConn.Write(responseBuf.Bytes()); err != nil {
			p.logger.Debug("error writing response headers", "error", err)
			resp.Body.Close()
			return
		}

		if _, err := clientConn.Write(bodyBuf.Bytes()); err != nil {
			p.logger.Debug("error writing response body", "error", err)
		}
	}
	resp.Body.Close()

	// Finalize flow
	if p.redactor != nil {
		flow.ResponseHeaders = redact.HeadersToMap(p.redactor.RedactHeaders(resp.Header))
		if p.redactor.ShouldStoreBody() && respBody.Len() > 0 {
			redacted := p.redactor.RedactBody(respBody.String())
			flow.ResponseBody = &redacted
		}
	} else {
		flow.ResponseHeaders = redact.HeadersToMap(resp.Header)
		if respBody.Len() > 0 {
			s := respBody.String()
			flow.ResponseBody = &s
		}
	}
	flow.ResponseBodyTruncated = limitedWriter.truncated

	// Extract usage from captured body (provider was detected earlier at request time)
	if flow.Provider != "" && respBody.Len() > 0 {
		if prov := p.providers.Get(flow.Provider); prov != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			p.extractUsageAndCost(ctx, flow, prov, respBody.Bytes())
			cancel()
		}
	}

	// Extract tool invocations from non-streaming JSON responses (langley-ahgo)
	if !flow.IsSSE && respBody.Len() > 0 {
		tools := parser.ExtractToolUsesFromJSON(respBody.Bytes())
		p.saveToolInvocations(flowID, flow.TaskID, tools)
	}

	// Save flow
	p.saveFlow(flow)

	// Notify update
	if p.onUpdate != nil {
		p.onUpdate(flow)
	}
}

// sendError sends an HTTP error response over a raw connection.
func (p *MITMProxy) sendError(conn net.Conn, status int, message string) {
	response := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s",
		status, http.StatusText(status), len(message), message)
	_, _ = conn.Write([]byte(response))
}

const oversizedRedactedPlaceholder = "[REDACTED: payload exceeds max redaction size]"

// redactOptionalString applies redaction rules to an optional string payload.
// Returns nil if the input is nil or body storage is disabled.
func (p *MITMProxy) redactOptionalString(s *string) *string {
	if s == nil {
		return nil
	}
	if p.redactor == nil {
		return s
	}
	if !p.redactor.ShouldStoreBody() {
		return nil
	}
	if len(*s) > redact.MaxRedactionInputSize {
		placeholder := oversizedRedactedPlaceholder
		return &placeholder
	}
	redacted := p.redactor.RedactBody(*s)
	return &redacted
}

// redactEventData applies redaction rules to parsed SSE event data before storage or broadcast.
func (p *MITMProxy) redactEventData(data map[string]interface{}) map[string]interface{} {
	if data == nil || p.redactor == nil {
		return data
	}
	if !p.redactor.ShouldStoreBody() {
		return nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return map[string]interface{}{"redacted": true, "error": "marshal_failed"}
	}
	if len(raw) > redact.MaxRedactionInputSize {
		return map[string]interface{}{"redacted": true, "truncated": true, "reason": oversizedRedactedPlaceholder}
	}
	redacted := p.redactor.RedactBody(string(raw))
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(redacted), &out); err != nil {
		return map[string]interface{}{"raw": redacted}
	}
	return out
}

// correlateToolResults parses request bodies for tool_result blocks and updates
// the matching tool invocations with success/failure and duration (langley-io4).
func (p *MITMProxy) correlateToolResults(reqBody []byte) {
	if p.store == nil || len(reqBody) == 0 {
		return
	}

	results := parser.ExtractToolResults(reqBody)
	if len(results) == 0 {
		return
	}

	now := time.Now()
	for _, result := range results {
		content := p.redactOptionalString(result.Content)
		// errorMsg is only set when the tool reported an error
		var errorMsg *string
		if result.IsError {
			errorMsg = content
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := p.store.UpdateToolResult(ctx, result.ToolUseID, !result.IsError, errorMsg, content, now); err != nil {
			p.logger.Error("failed to update tool result", "tool_use_id", result.ToolUseID, "error", err)
		} else {
			p.logger.Debug("correlated tool result", "tool_use_id", result.ToolUseID, "success", !result.IsError)
		}
		cancel()
	}
}

// saveFlow persists a flow to the store.
func (p *MITMProxy) saveFlow(flow *store.Flow) {
	if p.store == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Set expiration based on retention config
	expiresAt := time.Now().AddDate(0, 0, p.cfg.Retention.FlowsTTLDays)
	flow.ExpiresAt = &expiresAt

	// Use UpdateFlow since flow was already saved at request start (langley-2fa)
	if err := p.store.UpdateFlow(ctx, flow); err != nil {
		p.logger.Error("failed to update flow", "flow_id", flow.ID, "error", err)
	}
}

// saveToolInvocations persists extracted tool uses to the store.
func (p *MITMProxy) saveToolInvocations(flowID string, taskID *string, tools []*parser.ToolUse) {
	if p.store == nil || len(tools) == 0 {
		return
	}

	for _, tool := range tools {
		toolUseID := tool.ID
		inv := &store.ToolInvocation{
			ID:        uuid.New().String(),
			FlowID:    flowID,
			TaskID:    taskID,
			ToolUseID: &toolUseID,
			ToolName:  tool.Name,
			Timestamp: time.Now(),
		}

		// Serialize tool input to JSON for storage, applying redaction rules (langley-e9ui)
		if len(tool.Input) > 0 {
			if inputJSON, err := json.Marshal(tool.Input); err == nil {
				s := string(inputJSON)
				inv.ToolInput = p.redactOptionalString(&s)
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if saveErr := p.store.SaveToolInvocation(ctx, inv); saveErr != nil {
			p.logger.Error("failed to save tool invocation", "flow_id", flowID, "tool", tool.Name, "error", saveErr)
		} else {
			p.logger.Debug("saved tool invocation", "flow_id", flowID, "tool", tool.Name, "tool_use_id", toolUseID)
		}
		cancel()
	}
}

// extractUsageAndCost parses usage data from the captured response body and calculates cost.
// The body parameter is the raw captured response — independent of whether it's stored on the flow.
func (p *MITMProxy) extractUsageAndCost(ctx context.Context, flow *store.Flow, prov provider.Provider, body []byte) {
	if len(body) == 0 {
		return
	}

	// Use provider to parse usage
	usage, err := prov.ParseUsage(body, flow.IsSSE)
	if err == nil && usage != nil {
		if usage.Model != "" {
			flow.Model = &usage.Model
		}
		if usage.InputTokens > 0 {
			flow.InputTokens = &usage.InputTokens
		}
		if usage.OutputTokens > 0 {
			flow.OutputTokens = &usage.OutputTokens
		}
		if usage.CacheCreationTokens > 0 {
			flow.CacheCreationTokens = &usage.CacheCreationTokens
		}
		if usage.CacheReadTokens > 0 {
			flow.CacheReadTokens = &usage.CacheReadTokens
		}
	}

	// Calculate cost if we have token counts and analytics engine
	if p.analytics != nil && flow.InputTokens != nil {
		inputTokens := 0
		outputTokens := 0
		cacheCreation := 0
		cacheRead := 0

		if flow.InputTokens != nil {
			inputTokens = *flow.InputTokens
		}
		if flow.OutputTokens != nil {
			outputTokens = *flow.OutputTokens
		}
		if flow.CacheCreationTokens != nil {
			cacheCreation = *flow.CacheCreationTokens
		}
		if flow.CacheReadTokens != nil {
			cacheRead = *flow.CacheReadTokens
		}

		model := "unknown"
		if flow.Model != nil {
			model = *flow.Model
		}

		cost, costSource, err := p.analytics.CalculateCost(ctx, flow.Provider, model, inputTokens, outputTokens, cacheCreation, cacheRead)
		if err == nil && costSource != "" {
			flow.TotalCost = &cost
			flow.CostSource = &costSource
		}
	}
}

// streamSSEWithParser streams SSE response body while parsing events.
// It writes to the client, captures to buffer, and emits parsed events.
// After streaming completes, it extracts tool invocations and saves them.
func (p *MITMProxy) streamSSEWithParser(flowID string, taskID *string, reader io.Reader, client io.Writer, capture *limitedBuffer) error {
	// Create a pipe to tee the data
	pr, pw := io.Pipe()

	// Create event channel
	eventsCh := make(chan *store.Event, 100)

	// Start SSE parser in goroutine
	sseParser := parser.NewSSEParserWithLogger(flowID, eventsCh, p.logger)
	var parseErr error
	go func() {
		parseErr = sseParser.Parse(pr)
		close(eventsCh)
	}()

	// Drain events concurrently with io.Copy to prevent deadlock.
	// The channel buffer is finite — if we wait until after io.Copy returns
	// to consume events, the parser blocks on a full channel, which blocks the
	// pipe write, which blocks the multi-writer, which blocks io.Copy. Deadlock.
	var collectedEvents []*store.Event
	var eventWg sync.WaitGroup
	eventWg.Add(1)
	go func() {
		defer eventWg.Done()
		for event := range eventsCh {
			rawEvent := *event
			collectedEvents = append(collectedEvents, &rawEvent)

			event.EventData = p.redactEventData(event.EventData)

			if p.store != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				if saveErr := p.store.SaveEvent(ctx, event); saveErr != nil {
					p.logger.Error("failed to save SSE event", "flow_id", flowID, "error", saveErr)
				}
				cancel()
			}

			if p.onEvent != nil {
				p.onEvent(event)
			}
		}
	}()

	// Create a multi-writer: client + capture + parser pipe
	mw := io.MultiWriter(client, capture, pw)

	// Copy data through the multi-writer
	_, err := io.Copy(mw, reader)
	pw.Close() // Signal parser that we're done

	// Wait for all events to be consumed
	eventWg.Wait()

	// Extract and save tool invocations (io4-1)
	if len(collectedEvents) > 0 {
		tools := parser.ExtractToolUses(collectedEvents)
		p.saveToolInvocations(flowID, taskID, tools)
	}

	if err != nil {
		return err
	}
	return parseErr
}

// limitedBuffer is a writer that stops writing after max bytes.
type limitedBuffer struct {
	buf       *bytes.Buffer
	max       int
	truncated bool
}

func (l *limitedBuffer) Write(p []byte) (n int, err error) {
	if l.buf.Len() >= l.max {
		l.truncated = true
		return len(p), nil // Pretend we wrote it all
	}
	remaining := l.max - l.buf.Len()
	if len(p) > remaining {
		l.truncated = true
		if _, err := l.buf.Write(p[:remaining]); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return l.buf.Write(p)
}

// chunkedWriter implements HTTP/1.1 chunked transfer encoding.
// This is needed for SSE responses where Go's http.ReadResponse de-chunks
// the upstream response but the client needs chunked framing (langley-a4m).
type chunkedWriter struct {
	w io.Writer
}

func newChunkedWriter(w io.Writer) *chunkedWriter {
	return &chunkedWriter{w: w}
}

// Write writes a chunk in HTTP/1.1 chunked transfer encoding format.
func (c *chunkedWriter) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}
	// Write chunk size in hex
	if _, err := fmt.Fprintf(c.w, "%x\r\n", len(p)); err != nil {
		return 0, err
	}
	// Write chunk data
	n, err = c.w.Write(p)
	if err != nil {
		return n, err
	}
	// Write chunk terminator
	if _, err := c.w.Write([]byte("\r\n")); err != nil {
		return n, err
	}
	return n, nil
}

// Close writes the final zero-length chunk to signal end of response.
func (c *chunkedWriter) Close() error {
	_, err := c.w.Write([]byte("0\r\n\r\n"))
	return err
}

// flushWriter wraps an io.Writer and flushes after each write if possible.
// This is needed for SSE responses via http.ResponseWriter (langley-a4m).
type flushWriter struct {
	w       io.Writer
	flusher http.Flusher
}

func newFlushWriter(w io.Writer) *flushWriter {
	fw := &flushWriter{w: w}
	if f, ok := w.(http.Flusher); ok {
		fw.flusher = f
	}
	return fw
}

func (f *flushWriter) Write(p []byte) (n int, err error) {
	n, err = f.w.Write(p)
	if err == nil && f.flusher != nil {
		f.flusher.Flush()
	}
	return n, err
}

func (p *MITMProxy) isCaptureWanted() bool {
	p.captureMu.Lock()
	defer p.captureMu.Unlock()
	return p.captureWanted
}

func (p *MITMProxy) SetProxyAddr(addr string) {
	p.captureMu.Lock()
	defer p.captureMu.Unlock()
	p.proxyAddr = addr
}

func (p *MITMProxy) CaptureStatus() CaptureStatus {
	p.captureMu.Lock()
	defer p.captureMu.Unlock()
	return p.captureStatusLocked()
}

func (p *MITMProxy) captureStatusLocked() CaptureStatus {
	hosts := []string{"api.anthropic.com", "api.openai.com", "generativelanguage.googleapis.com", "bedrock-runtime.*.amazonaws.com"}
	var configured []string
	if p.cfg != nil {
		configured = p.cfg.Proxy.InterceptHosts
	}
	seen := make(map[string]bool)
	result := make([]string, 0, len(hosts)+len(configured))
	for _, h := range append(hosts, configured...) {
		if !seen[h] {
			seen[h] = true
			result = append(result, h)
		}
	}
	status := CaptureStatus{
		State:          CaptureStatePassthrough,
		InterceptHosts: result,
		ProxyAddr:      p.proxyAddr,
		Revision:       p.captureRevision,
	}
	if p.captureWanted {
		status.State = CaptureStateCapturing
		for t := range p.captureTunnels {
			if t.draining {
				status.DrainingPassthroughTunnels++
			}
		}
		if status.DrainingPassthroughTunnels > 0 {
			status.State = CaptureStateEnabling
		}
	} else {
		status.DrainingMITMRequests = p.inFlightMITMRequests
		if status.DrainingMITMRequests > 0 {
			status.State = CaptureStateStopping
		}
	}
	return status
}

func captureStatusChanged(before, after CaptureStatus) bool {
	return before.State != after.State ||
		before.DrainingPassthroughTunnels != after.DrainingPassthroughTunnels ||
		before.DrainingMITMRequests != after.DrainingMITMRequests
}

func (p *MITMProxy) recordTransitionLocked(before CaptureStatus) CaptureStatus {
	after := p.captureStatusLocked()
	if captureStatusChanged(before, after) {
		p.captureRevision++
		after.Revision = p.captureRevision
		p.notifyQueue = append(p.notifyQueue, after)
	}
	return after
}

func (p *MITMProxy) flushCaptureNotifications() {
	p.captureMu.Lock()
	if p.notifying || len(p.notifyQueue) == 0 {
		p.captureMu.Unlock()
		return
	}
	p.notifying = true
	for len(p.notifyQueue) > 0 {
		next := p.notifyQueue[0]
		p.notifyQueue = p.notifyQueue[1:]
		cb := p.onCaptureStateChange
		p.captureMu.Unlock()
		if cb != nil {
			cb(next)
		}
		p.captureMu.Lock()
	}
	p.notifying = false
	p.captureMu.Unlock()
}

type pendingFirstByte struct {
	session  *captureSession
	readDone chan struct{}
	readSeq  uint64
}

func (p *MITMProxy) SetCapture(enabled bool, force bool) CaptureStatus {
	p.captureMu.Lock()
	p.captureOpSeq++
	opSeq := p.captureOpSeq
	before := p.captureStatusLocked()
	var pending []pendingFirstByte
	if !enabled && !force {
		for session := range p.captureSessions {
			if !session.busy && session.awaitingFirstByte {
				if session.firstByteRead.Load() {
					p.admitMITMFirstByteLocked(session)
				} else if session.readDone != nil {
					_ = session.client.SetReadDeadline(time.Now())
					pending = append(pending, pendingFirstByte{
						session:  session,
						readDone: session.readDone,
						readSeq:  session.readSeq,
					})
				}
			}
		}
	}
	if len(pending) > 0 {
		p.captureMu.Unlock()
		for _, item := range pending {
			select {
			case <-item.readDone:
			case <-time.After(10 * time.Millisecond):
			}
		}
		p.captureMu.Lock()
		if p.captureOpSeq != opSeq {
			status := p.captureStatusLocked()
			p.captureMu.Unlock()
			return status
		}
		for _, item := range pending {
			if item.session.readSeq == item.readSeq && item.session.awaitingFirstByte && hasFirstByteEvidenceLocked(item.session) {
				p.admitMITMFirstByteLocked(item.session)
			}
		}
	}
	p.captureWanted = enabled
	for t := range p.captureTunnels {
		if !enabled {
			t.draining = false
			continue
		}
		if force {
			_ = t.client.Close()
			_ = t.upstream.Close()
			delete(p.captureTunnels, t)
		} else {
			t.draining = true
		}
	}
	if enabled {
		for session := range p.captureSessions {
			if session.awaitingFirstByte {
				_ = session.client.SetReadDeadline(time.Time{})
			}
		}
	} else {
		for session := range p.captureSessions {
			if force || !session.busy {
				session.awaitingFirstByte = false
				session.awaitingFirstByteAtomic.Store(false)
				session.firstByteRead.Store(false)
				session.pendingReadByte = nil
				_ = session.client.Close()
				if session.upstream != nil {
					_ = session.upstream.Close()
				}
			}
		}
	}
	status := p.recordTransitionLocked(before)
	p.captureMu.Unlock()
	p.flushCaptureNotifications()
	return status
}

func (p *MITMProxy) finishCaptureTunnel(t *captureTunnel) {
	p.captureMu.Lock()
	before := p.captureStatusLocked()
	delete(p.captureTunnels, t)
	_ = p.recordTransitionLocked(before)
	p.captureMu.Unlock()
	p.flushCaptureNotifications()
}

func (p *MITMProxy) finishMITMRequest(session *captureSession, forceClose bool) bool {
	p.captureMu.Lock()
	before := p.captureStatusLocked()
	p.inFlightMITMRequests--
	session.busy = false
	paused := !p.captureWanted
	_ = p.recordTransitionLocked(before)
	p.captureMu.Unlock()
	if paused || forceClose {
		_ = session.client.Close()
		if session.upstream != nil {
			_ = session.upstream.Close()
		}
	}
	p.flushCaptureNotifications()
	return paused || forceClose
}
