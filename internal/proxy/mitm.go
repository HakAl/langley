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
	"time"

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

// MITMProxy is an intercepting proxy that captures TLS traffic.
type MITMProxy struct {
	cfg          *config.Config
	logger       *slog.Logger
	ca           *langleytls.CA
	certCache    *langleytls.CertCache
	redactor     *redact.Redactor
	store        store.Store
	analytics    *analytics.Engine
	taskAssigner *task.Assigner
	providers    *provider.Registry
	server *http.Server
	client *http.Client

	// Callbacks for real-time updates
	onFlow   func(*store.Flow)
	onUpdate func(*store.Flow)
	onEvent  func(*store.Event)

	// insecureSkipVerifyUpstream is for testing only
	insecureSkipVerifyUpstream bool
}

// MITMProxyConfig holds configuration for creating a MITM proxy.
type MITMProxyConfig struct {
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
			InsecureSkipVerify: false,              // Validate upstream (langley-vu5)
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

	p := &MITMProxy{
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
		Addr:        cfg.Config.Proxy.ListenAddr(),
		Handler:     p,
		ReadTimeout: 0,
		WriteTimeout: 0,
		IdleTimeout: 120 * time.Second,
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

	if p.shouldIntercept(r.Host) {
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

// handleConnectPassthrough tunnels the connection transparently without MITM.
// The client sees the upstream server's real TLS certificate.
func (p *MITMProxy) handleConnectPassthrough(w http.ResponseWriter, r *http.Request) {
	// Parse host for upstream connection
	host := r.Host
	if !strings.Contains(host, ":") {
		host = host + ":443"
	}

	// Dial upstream BEFORE sending 200 OK — so we can report errors properly
	upstreamConn, err := net.DialTimeout("tcp", host, 10*time.Second)
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

	// Send 200 OK — upstream is reachable
	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		p.logger.Error("failed to write tunnel response", "error", err)
		clientConn.Close()
		upstreamConn.Close()
		return
	}

	// Hand off to bidirectional tunnel with idle timeout
	go tunnel(clientConn, upstreamConn, p.logger, r.Host)
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
	tlsConn := tls.Server(clientConn, tlsConfig)
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
	upstreamConn, err := tls.Dial("tcp", host, &tls.Config{
		InsecureSkipVerify: p.insecureSkipVerifyUpstream, // Only skip for testing (langley-vu5)
		NextProtos:         []string{"http/1.1"},
	})
	if err != nil {
		p.logger.Error("failed to connect to upstream", "host", host, "error", err)
		tlsConn.Close()
		return
	}

	// Handle requests on this connection
	p.handleTLSConnection(tlsConn, upstreamConn, r.Host)
}

// handleTLSConnection handles HTTP requests over an established TLS connection.
func (p *MITMProxy) handleTLSConnection(clientConn *tls.Conn, upstreamConn *tls.Conn, host string) {
	defer clientConn.Close()
	defer upstreamConn.Close()

	clientReader := bufio.NewReader(clientConn)

	for {
		// Read request from client
		req, err := http.ReadRequest(clientReader)
		if err != nil {
			if err != io.EOF {
				p.logger.Debug("error reading request from TLS connection", "host", host, "error", err)
			}
			return
		}
		p.logger.Debug("read request from TLS connection", "host", host, "method", req.Method, "path", req.URL.Path)

		// Fix up the request URL
		req.URL.Scheme = "https"
		req.URL.Host = host

		// Handle this request
		p.handleTLSRequest(req, clientConn, upstreamConn, host)
	}
}

// handleTLSRequest handles a single HTTP request over TLS.
func (p *MITMProxy) handleTLSRequest(r *http.Request, clientConn net.Conn, upstreamConn *tls.Conn, host string) {
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
	upstreamReader := bufio.NewReader(upstreamConn)
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
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := p.store.UpdateToolResult(ctx, result.ToolUseID, !result.IsError, result.Content, now); err != nil {
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
			collectedEvents = append(collectedEvents, event)

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
	if p.store != nil && len(collectedEvents) > 0 {
		tools := parser.ExtractToolUses(collectedEvents)
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

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if saveErr := p.store.SaveToolInvocation(ctx, inv); saveErr != nil {
				p.logger.Error("failed to save tool invocation", "flow_id", flowID, "tool", tool.Name, "error", saveErr)
			} else {
				p.logger.Debug("saved tool invocation", "flow_id", flowID, "tool", tool.Name, "tool_use_id", toolUseID)
			}
			cancel()
		}
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
		return l.buf.Write(p[:remaining])
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
