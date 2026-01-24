package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/langley/internal/config"
	"github.com/anthropics/langley/internal/redact"
	"github.com/anthropics/langley/internal/store"
	"github.com/anthropics/langley/internal/task"
	langleytls "github.com/anthropics/langley/internal/tls"
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
	taskAssigner *task.Assigner
	server       *http.Server
	client       *http.Client
	shutdown     sync.WaitGroup

	// Callbacks for real-time updates
	onFlow   func(*store.Flow)
	onUpdate func(*store.Flow)
}

// MITMProxyConfig holds configuration for creating a MITM proxy.
type MITMProxyConfig struct {
	Config       *config.Config
	Logger       *slog.Logger
	CA           *langleytls.CA
	CertCache    *langleytls.CertCache
	Redactor     *redact.Redactor
	Store        store.Store
	TaskAssigner *task.Assigner
	OnFlow       func(*store.Flow)
	OnUpdate     func(*store.Flow)
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
			// Security: Validate upstream TLS by default (addresses langley-vu5)
			InsecureSkipVerify: false,
		},
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
		cfg:          cfg.Config,
		logger:       cfg.Logger,
		ca:           cfg.CA,
		certCache:    cfg.CertCache,
		redactor:     cfg.Redactor,
		store:        cfg.Store,
		taskAssigner: cfg.TaskAssigner,
		client:       client,
		onFlow:       cfg.OnFlow,
		onUpdate:     cfg.OnUpdate,
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

// Serve starts the proxy server.
func (p *MITMProxy) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", p.server.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	go func() {
		<-ctx.Done()
		p.logger.Info("shutting down MITM proxy")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		p.server.Shutdown(shutdownCtx)
	}()

	p.logger.Info("MITM proxy listening", "addr", p.server.Addr)
	if err := p.server.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve: %w", err)
	}

	return nil
}

// ServeHTTP handles incoming HTTP requests.
func (p *MITMProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	// Read and capture request body (with limit)
	var reqBody []byte
	var reqBodyTruncated bool
	if r.Body != nil {
		maxBody := p.cfg.Persistence.BodyMaxBytes
		limitedReader := io.LimitReader(r.Body, int64(maxBody+1))
		reqBody, _ = io.ReadAll(limitedReader)
		if len(reqBody) > maxBody {
			reqBodyTruncated = true
			reqBody = reqBody[:maxBody]
		}
		r.Body.Close()
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

	// Redact and store request
	if p.redactor != nil {
		flow.RequestHeaders = redact.HeadersToMap(p.redactor.RedactHeaders(r.Header))
		if len(reqBody) > 0 {
			redacted := p.redactor.RedactBody(string(reqBody))
			flow.RequestBody = &redacted
		}
	} else {
		flow.RequestHeaders = redact.HeadersToMap(r.Header)
		if len(reqBody) > 0 {
			s := string(reqBody)
			flow.RequestBody = &s
		}
	}

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
	multiWriter := io.MultiWriter(w, limitedWriter)

	if _, err := io.Copy(multiWriter, resp.Body); err != nil {
		p.logger.Debug("error copying response", "error", err)
	}

	// Finalize flow
	if p.redactor != nil {
		flow.ResponseHeaders = redact.HeadersToMap(p.redactor.RedactHeaders(resp.Header))
		if respBody.Len() > 0 {
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

	// Detect Claude traffic and extract model
	if strings.Contains(r.Host, "anthropic") || strings.Contains(r.Host, "claude") {
		flow.Provider = "anthropic"
	}

	// Save flow
	p.saveFlow(flow)

	// Notify update
	if p.onUpdate != nil {
		p.onUpdate(flow)
	}
}

// handleConnect handles HTTPS CONNECT requests with MITM.
func (p *MITMProxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	p.logger.Debug("CONNECT request", "host", r.Host)

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
	tlsConfig := &tls.Config{
		GetCertificate: p.certCache.GetCertificate,
	}
	tlsConn := tls.Server(clientConn, tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		p.logger.Debug("TLS handshake failed", "error", err)
		clientConn.Close()
		return
	}

	// Parse host for upstream connection
	host := r.Host
	if !strings.Contains(host, ":") {
		host = host + ":443"
	}

	// Connect to upstream
	upstreamConn, err := tls.Dial("tcp", host, &tls.Config{
		InsecureSkipVerify: false, // Validate upstream (addresses langley-vu5)
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
				p.logger.Debug("error reading request", "error", err)
			}
			return
		}

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

	// Read request body
	var reqBody []byte
	var reqBodyTruncated bool
	if r.Body != nil {
		maxBody := p.cfg.Persistence.BodyMaxBytes
		limitedReader := io.LimitReader(r.Body, int64(maxBody+1))
		reqBody, _ = io.ReadAll(limitedReader)
		if len(reqBody) > maxBody {
			reqBodyTruncated = true
			reqBody = reqBody[:maxBody]
		}
		r.Body.Close()
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

	// Redact and store request
	if p.redactor != nil {
		flow.RequestHeaders = redact.HeadersToMap(p.redactor.RedactHeaders(r.Header))
		if len(reqBody) > 0 {
			redacted := p.redactor.RedactBody(string(reqBody))
			flow.RequestBody = &redacted
		}
	} else {
		flow.RequestHeaders = redact.HeadersToMap(r.Header)
		if len(reqBody) > 0 {
			s := string(reqBody)
			flow.RequestBody = &s
		}
	}

	// Detect provider
	if strings.Contains(host, "anthropic") || strings.Contains(host, "claude") {
		flow.Provider = "anthropic"
	}

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

	// Capture response body while streaming to client
	var respBody bytes.Buffer
	maxBody := p.cfg.Persistence.BodyMaxBytes
	limitedWriter := &limitedBuffer{buf: &respBody, max: maxBody}

	// Build response to send to client
	var responseBuf bytes.Buffer
	fmt.Fprintf(&responseBuf, "HTTP/%d.%d %s\r\n", resp.ProtoMajor, resp.ProtoMinor, resp.Status)
	resp.Header.Write(&responseBuf)
	responseBuf.WriteString("\r\n")

	// Write headers to client
	if _, err := clientConn.Write(responseBuf.Bytes()); err != nil {
		p.logger.Debug("error writing response headers", "error", err)
		resp.Body.Close()
		return
	}

	// Stream body
	multiWriter := io.MultiWriter(clientConn, limitedWriter)
	if _, err := io.Copy(multiWriter, resp.Body); err != nil {
		p.logger.Debug("error streaming response", "error", err)
	}
	resp.Body.Close()

	// Finalize flow
	if p.redactor != nil {
		flow.ResponseHeaders = redact.HeadersToMap(p.redactor.RedactHeaders(resp.Header))
		if respBody.Len() > 0 {
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
	conn.Write([]byte(response))
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

	if err := p.store.SaveFlow(ctx, flow); err != nil {
		p.logger.Error("failed to save flow", "flow_id", flow.ID, "error", err)
	}
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
