// Package proxy implements the HTTP/HTTPS intercepting proxy.
package proxy

import (
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

	"github.com/HakAl/langley/internal/config"
)

// Proxy is the main HTTP/HTTPS intercepting proxy.
type Proxy struct {
	cfg      *config.Config
	logger   *slog.Logger
	server   *http.Server
	client   *http.Client
	shutdown sync.WaitGroup
}

// New creates a new Proxy with the given configuration.
func New(cfg *config.Config, logger *slog.Logger) (*Proxy, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if logger == nil {
		logger = slog.Default()
	}

	// HTTP client for forwarding requests
	// Note: For MVP, we use standard TLS. uTLS fingerprinting is deferred.
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
		// Don't follow redirects - let the client handle them
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 0, // No timeout - streaming responses can be long
	}

	p := &Proxy{
		cfg:    cfg,
		logger: logger,
		client: client,
	}

	// Create HTTP server
	p.server = &http.Server{
		Addr:         cfg.Proxy.ListenAddr(),
		Handler:      p,
		ReadTimeout:  0, // No timeout for streaming
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	return p, nil
}

// Serve starts the proxy server and blocks until the context is cancelled.
func (p *Proxy) Serve(ctx context.Context) error {
	// Start listener
	ln, err := net.Listen("tcp", p.server.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	// Shutdown goroutine
	go func() {
		<-ctx.Done()
		p.logger.Info("shutting down proxy")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := p.server.Shutdown(shutdownCtx); err != nil {
			p.logger.Error("shutdown error", "error", err)
		}
	}()

	// Serve
	p.logger.Info("proxy listening", "addr", p.server.Addr)
	if err := p.server.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve: %w", err)
	}

	return nil
}

// ServeHTTP handles incoming HTTP requests.
// This implements the http.Handler interface.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}

	p.handleHTTP(w, r)
}

// handleHTTP handles regular HTTP requests (non-CONNECT).
func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	p.logger.Debug("HTTP request", "method", r.Method, "url", r.URL.String())

	// Create outbound request
	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
	if err != nil {
		p.logger.Error("failed to create request", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Copy headers
	copyHeaders(outReq.Header, r.Header)

	// Remove hop-by-hop headers
	removeHopByHopHeaders(outReq.Header)

	// Forward request
	resp, err := p.client.Do(outReq)
	if err != nil {
		p.logger.Error("failed to forward request", "error", err)
		http.Error(w, "Bad gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	copyHeaders(w.Header(), resp.Header)
	removeHopByHopHeaders(w.Header())

	// Write status
	w.WriteHeader(resp.StatusCode)

	// Stream response body
	if _, err := io.Copy(w, resp.Body); err != nil {
		p.logger.Debug("error copying response", "error", err)
	}
}

// handleConnect handles HTTPS CONNECT requests (tunnel establishment).
// For Phase 0, we just tunnel without interception.
// TLS MITM will be added in Phase 1.
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	p.logger.Debug("CONNECT request", "host", r.Host)

	// Connect to destination
	destConn, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
	if err != nil {
		p.logger.Error("failed to connect to destination", "host", r.Host, "error", err)
		http.Error(w, "Bad gateway", http.StatusBadGateway)
		return
	}

	// Hijack the connection
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		p.logger.Error("hijacking not supported")
		http.Error(w, "Internal error", http.StatusInternalServerError)
		destConn.Close()
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		p.logger.Error("failed to hijack connection", "error", err)
		destConn.Close()
		return
	}

	// Send 200 OK to client
	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		p.logger.Error("failed to write tunnel response", "error", err)
		clientConn.Close()
		destConn.Close()
		return
	}

	// Bidirectional copy
	p.shutdown.Add(2)
	go func() {
		defer p.shutdown.Done()
		defer destConn.Close()
		io.Copy(destConn, clientConn)
	}()
	go func() {
		defer p.shutdown.Done()
		defer clientConn.Close()
		io.Copy(clientConn, destConn)
	}()
}

// copyHeaders copies headers from src to dst.
func copyHeaders(dst, src http.Header) {
	for name, values := range src {
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

// hopByHopHeaders are headers that should not be forwarded.
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
}

// removeHopByHopHeaders removes hop-by-hop headers from the header map.
func removeHopByHopHeaders(h http.Header) {
	// Get Connection header value before we delete it
	conn := h.Get("Connection")

	for _, header := range hopByHopHeaders {
		h.Del(header)
	}

	// Also remove headers listed in Connection header
	if conn != "" {
		for _, f := range strings.Split(conn, ",") {
			if f = strings.TrimSpace(f); f != "" {
				h.Del(f)
			}
		}
	}
}
