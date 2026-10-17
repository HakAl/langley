package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/HakAl/langley/internal/config"
	"github.com/HakAl/langley/internal/redact"
	"github.com/HakAl/langley/internal/store"
	langleytls "github.com/HakAl/langley/internal/tls"
)

// pipeListener implements net.Listener with in-memory transports. These tests
// exercise real HTTP, CONNECT and TLS without binding ports or contacting APIs.
type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
}
func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9090} }
func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-ctx.Done():
		_ = client.Close()
		_ = server.Close()
		return nil, ctx.Err()
	case <-l.done:
		_ = client.Close()
		_ = server.Close()
		return nil, net.ErrClosed
	}
}

func captureFixture(t *testing.T, handler http.Handler, paused bool, wrap ...func(store.Store) store.Store) (*MITMProxy, *http.Client, store.Store, []byte, <-chan CaptureStatus) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Persistence.BodyMaxBytes = 1 << 20
	ca, err := langleytls.LoadOrCreateCA(filepath.Join(t.TempDir(), "proxy"))
	if err != nil {
		t.Fatal(err)
	}
	upstreamCA, err := langleytls.LoadOrCreateCA(filepath.Join(t.TempDir(), "upstream"))
	if err != nil {
		t.Fatal(err)
	}
	cert, err := langleytls.NewCertCache(upstreamCA, 10).GetCertificate(&tls.ClientHelloInfo{ServerName: "api.openai.com"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "capture.db"), &cfg.Retention)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	redactor, err := redact.New(&cfg.Redaction)
	if err != nil {
		t.Fatal(err)
	}
	persistence := store.Store(data)
	for _, decorator := range wrap {
		persistence = decorator(persistence)
	}
	changes := make(chan CaptureStatus, 100)
	p, err := NewMITMProxy(MITMProxyConfig{Config: cfg, Logger: testLogger(), CA: ca, CertCache: langleytls.NewCertCache(ca, 10), Redactor: redactor, Store: persistence, StartInPassthrough: paused, PassthroughIdleThreshold: 100 * time.Millisecond, InsecureSkipVerifyUpstream: true, OnCaptureStateChange: func(s CaptureStatus) { changes <- s }})
	if err != nil {
		t.Fatal(err)
	}
	upstreamListener := newPipeListener()
	upstream := &http.Server{Handler: handler}
	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
		_ = upstream.Serve(tls.NewListener(upstreamListener, &tls.Config{Certificates: []tls.Certificate{*cert}}))
	}()
	p.dialHook = upstreamListener.dial
	proxyListener := newPipeListener()
	proxyServer := &http.Server{Handler: p}
	proxyDone := make(chan struct{})
	go func() { defer close(proxyDone); _ = proxyServer.Serve(proxyListener) }()
	proxyURL, _ := url.Parse("http://localhost:9090")
	// Both independent CAs are trusted; certificate assertions distinguish routing.
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.CertPEM())
	roots.AppendCertsFromPEM(upstreamCA.CertPEM())
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DialContext: proxyListener.dial, TLSClientConfig: &tls.Config{RootCAs: roots}, ForceAttemptHTTP2: false}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		p.closeTunnels()
		p.tunnelWg.Wait()
		_ = proxyServer.Close()
		_ = upstream.Close()
		<-proxyDone
		<-upstreamDone
	})
	return p, client, data, cert.Certificate[0], changes
}

func waitCapture(t *testing.T, p *MITMProxy, state string) CaptureStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s := p.CaptureStatus()
		if s.State == state {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("capture status: %+v; want %s", p.CaptureStatus(), state)
	return CaptureStatus{}
}
func flowCount(t *testing.T, data store.Store) int {
	t.Helper()
	n, err := data.CountFlows(context.Background(), store.FlowFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCaptureKeepAliveSwitch(t *testing.T) {
	p, client, data, upstreamCert, changes := captureFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"ok":true}`) }), true)
	fetch := func() []byte {
		t.Helper()
		resp, err := client.Get("https://api.openai.com/v1/messages")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if _, err = io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatal(err)
		}
		return resp.TLS.PeerCertificates[0].Raw
	}
	if cert := fetch(); !bytes.Equal(cert, upstreamCert) {
		t.Fatal("passthrough substituted upstream certificate")
	}
	if n := flowCount(t, data); n != 0 {
		t.Fatalf("passthrough saved %d flows", n)
	}
	// Allow the completed response's TLS bytes to become idle before enabling.
	time.Sleep(150 * time.Millisecond)
	if s := p.SetCapture(true, false); s.State != CaptureStateCapturing {
		t.Fatalf("enable: %+v", s)
	}
	if cert := fetch(); bytes.Equal(cert, upstreamCert) {
		t.Fatal("capture retained upstream certificate")
	}
	deadline := time.Now().Add(time.Second)
	for flowCount(t, data) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := flowCount(t, data); n != 1 {
		t.Fatalf("capture saved %d flows", n)
	}
	select {
	case s := <-changes:
		if s.State != CaptureStateCapturing {
			t.Fatalf("callback: %+v", s)
		}
	case <-time.After(time.Second):
		t.Fatal("missing enable notification")
	}
	if s := p.SetCapture(false, false); s.State != CaptureStatePassthrough && s.State != CaptureStateStopping {
		t.Fatalf("pause: %+v", s)
	}
	waitCapture(t, p, CaptureStatePassthrough)
	if cert := fetch(); !bytes.Equal(cert, upstreamCert) {
		t.Fatal("idle MITM reused after pause")
	}
}

func TestCaptureStreamDraining(t *testing.T) {
	for _, paused := range []bool{true, false} {
		for _, force := range []bool{false, true} {
			name := CaptureStateStopping
			if paused {
				name = CaptureStateEnabling
			}
			if force {
				name += "/force"
			}
			t.Run(name, func(t *testing.T) {
				release := make(chan struct{})
				var once sync.Once
				finish := func() { once.Do(func() { close(release) }) }
				defer finish()
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"part\":1}\n\n")
					w.(http.Flusher).Flush()
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-release:
							_, _ = io.WriteString(w, "data: [DONE]\n\n")
							return
						case <-r.Context().Done():
							return
						case <-ticker.C:
							if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
								return
							}
							w.(http.Flusher).Flush()
						}
					}
				})
				p, client, data, upstreamCert, changes := captureFixture(t, handler, paused)
				resp, err := client.Get("https://api.openai.com/v1/messages")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = resp.Body.Close() }()
				reader := bufio.NewReader(resp.Body)
				if _, err := reader.ReadString('\n'); err != nil {
					t.Fatal(err)
				}
				status := p.SetCapture(paused, force)
				if !force {
					if paused && (status.State != CaptureStateEnabling || status.DrainingPassthroughTunnels != 1) {
						t.Fatalf("enable drain: %+v", status)
					}
					if !paused && (status.State != CaptureStateStopping || status.DrainingMITMRequests != 1) {
						t.Fatalf("pause drain: %+v", status)
					}
					if !paused {
						// New CONNECTs bypass MITM immediately while the old stream drains.
						fresh, err := client.Get("https://api.openai.com/v1/messages")
						if err != nil {
							t.Fatal(err)
						}
						if !bytes.Equal(fresh.TLS.PeerCertificates[0].Raw, upstreamCert) {
							t.Fatal("new connection decrypted while stopping")
						}
						_ = fresh.Body.Close()
					}
					finish()
					if _, err := io.Copy(io.Discard, reader); err != nil {
						t.Fatalf("graceful drain interrupted stream: %v", err)
					}
				} else {
					if _, err := io.Copy(io.Discard, reader); err == nil {
						t.Fatal("force did not interrupt stream")
					}
					finish()
				}
				final := CaptureStatePassthrough
				if paused {
					final = CaptureStateCapturing
				}
				waitCapture(t, p, final)
				if paused && flowCount(t, data) != 0 {
					t.Fatal("passthrough stream was stored")
				}
				if !paused {
					flows, err := data.ListFlows(context.Background(), store.FlowFilter{Limit: 10})
					if err != nil {
						t.Fatal(err)
					}
					if len(flows) != 1 || flows[0].StatusCode == nil {
						t.Fatalf("pause completed before flow persisted: %+v", flows)
					}
				}
				found := false
				for !found {
					select {
					case change := <-changes:
						found = change.State == final
					case <-time.After(time.Second):
						t.Fatalf("missing async callback for %s", final)
					}
				}
			})
		}
	}
}

// captureFlushStore intercepts final persistence through the store interface.
type captureFlushStore struct {
	store.Store
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (s *captureFlushStore) UpdateFlow(ctx context.Context, flow *store.Flow) error {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
		return s.Store.UpdateFlow(ctx, flow)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestCapturePauseWaitsForPersistence(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	p, client, data, _, changes := captureFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"ok":true}`) }), false, func(s store.Store) store.Store {
		return &captureFlushStore{Store: s, entered: entered, release: release}
	})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	resp, err := client.Get("https://api.openai.com/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("final persistence never started")
	}
	for _, force := range []bool{false, true} {
		status := p.SetCapture(false, force)
		if status.State != CaptureStateStopping || status.DrainingMITMRequests != 1 {
			t.Fatalf("reported passthrough before persistence: %+v", status)
		}
	}
	finish()
	waitCapture(t, p, CaptureStatePassthrough)
	flows, err := data.ListFlows(context.Background(), store.FlowFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 1 || flows[0].StatusCode == nil {
		t.Fatalf("final flow missing after drain: %+v", flows)
	}
	found := false
	for !found {
		select {
		case s := <-changes:
			found = s.State == CaptureStatePassthrough
		case <-time.After(time.Second):
			t.Fatal("missing flush-completion callback")
		}
	}
}

// captureTransportFunc implements the HTTP client's RoundTripper dependency.
type captureTransportFunc func(*http.Request) (*http.Response, error)

func (f captureTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCaptureHTTPPassthroughDoesNotPersist(t *testing.T) {
	p, _, data, _, _ := captureFixture(t, http.NotFoundHandler(), true)
	p.onFlow = func(*store.Flow) { t.Error("passthrough invoked onFlow") }
	p.onUpdate = func(*store.Flow) { t.Error("passthrough invoked onUpdate") }
	p.client = &http.Client{Transport: captureTransportFunc(func(r *http.Request) (*http.Response, error) {
		if r.RequestURI != "" {
			t.Error("RequestURI was not cleared for forwarding")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if string(body) != "request payload" || r.Header.Get("X-Test") != "forwarded" {
			t.Error("request was not forwarded intact")
		}
		return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"X-Upstream": []string{"yes"}}, Body: io.NopCloser(bytes.NewBufferString("upstream payload"))}, nil
	})}
	req := httptest.NewRequest(http.MethodPost, "http://api.openai.com/v1/messages", bytes.NewBufferString("request payload"))
	req.Header.Set("X-Test", "forwarded")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || rec.Header().Get("X-Upstream") != "yes" || rec.Body.String() != "upstream payload" {
		t.Fatalf("response was not forwarded intact: %+v", rec)
	}
	if n := flowCount(t, data); n != 0 {
		t.Fatalf("passthrough stored %d flows", n)
	}
}

func TestCaptureStatusScopeAndAddress(t *testing.T) {
	p, _, _, _, _ := captureFixture(t, http.NotFoundHandler(), true)
	p.cfg.Proxy.InterceptHosts = []string{"api.openai.com", "custom.test", "custom.test"}
	p.SetProxyAddr("127.0.0.1:12345")
	status := p.CaptureStatus()
	if status.State != CaptureStatePassthrough || status.ProxyAddr != "127.0.0.1:12345" || len(status.InterceptHosts) != 5 {
		t.Fatalf("status: %+v", status)
	}
	status.InterceptHosts[0] = "mutated.test"
	if p.CaptureStatus().InterceptHosts[0] != "api.anthropic.com" {
		t.Fatal("status exposed mutable shared host list")
	}
}
