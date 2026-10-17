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
	if s := p.SetCapture(true, false); s.State != CaptureStateEnabling || s.DrainingPassthroughTunnels != 1 {
		t.Fatalf("enable with open passthrough tunnel: %+v", s)
	}
	client.CloseIdleConnections()
	waitCapture(t, p, CaptureStateCapturing)
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
	foundCapturing := false
	for !foundCapturing {
		select {
		case s := <-changes:
			foundCapturing = s.State == CaptureStateCapturing
		case <-time.After(time.Second):
			t.Fatal("missing enable notification")
		}
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
					_ = resp.Body.Close()
					client.CloseIdleConnections()
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

func TestMITMKeepAliveReadRequestCountsAsInFlight(t *testing.T) {
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
	p, err := NewMITMProxy(MITMProxyConfig{
		Config:                     cfg,
		Logger:                     testLogger(),
		CA:                         ca,
		CertCache:                  langleytls.NewCertCache(ca, 10),
		Redactor:                   redactor,
		Store:                      data,
		StartInPassthrough:         false,
		InsecureSkipVerifyUpstream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	upstreamListener := newPipeListener()
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})}
	go func() {
		_ = upstream.Serve(tls.NewListener(upstreamListener, &tls.Config{Certificates: []tls.Certificate{*cert}}))
	}()
	t.Cleanup(func() { _ = upstreamListener.Close(); _ = upstream.Close() })
	p.dialHook = upstreamListener.dial
	proxyListener := newPipeListener()
	proxyServer := &http.Server{Handler: p}
	go func() { _ = proxyServer.Serve(proxyListener) }()
	t.Cleanup(func() { _ = proxyListener.Close(); _ = proxyServer.Close() })

	rawConn, err := proxyListener.dial(context.Background(), "tcp", "127.0.0.1:9090")
	if err != nil {
		t.Fatal(err)
	}
	defer rawConn.Close()
	if _, err := io.WriteString(rawConn, "CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(rawConn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT failed: %v %+v", err, resp)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.CertPEM())
	tlsConn := tls.Client(rawConn, &tls.Config{ServerName: "api.openai.com", RootCAs: roots})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatal(err)
	}

	// Send first byte of HTTP request line ("P") so Peek(1) unblocks and ReadRequest is in progress.
	if _, err := tlsConn.Write([]byte("P")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.captureMu.Lock()
		inFlight := p.inFlightMITMRequests
		p.captureMu.Unlock()
		if inFlight == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	status := p.SetCapture(false, false)
	if status.State != CaptureStateStopping || status.DrainingMITMRequests != 1 {
		t.Fatalf("SetCapture(false, false) while request headers are mid-send = %+v, want State=stopping DrainingMITMRequests=1", status)
	}

	// Complete the request and verify it finishes and drains to passthrough.
	if _, err := tlsConn.Write([]byte("OST /v1/messages HTTP/1.1\r\nHost: api.openai.com\r\nContent-Length: 2\r\n\r\n{}")); err != nil {
		t.Fatal(err)
	}
	tlsReader := bufio.NewReader(tlsConn)
	httpResp, err := http.ReadResponse(tlsReader, nil)
	if err != nil {
		t.Fatalf("in-flight request was killed during header send: %v", err)
	}
	_, _ = io.Copy(io.Discard, httpResp.Body)
	_ = httpResp.Body.Close()

	waitCapture(t, p, CaptureStatePassthrough)
	if n := flowCount(t, data); n != 1 {
		t.Fatalf("expected 1 persisted flow, got %d", n)
	}
}

func TestCaptureStateNotificationsSerializedAndMonotonic(t *testing.T) {
	var (
		mu       sync.Mutex
		observed []CaptureStatus
		inCall   bool
		overlap  bool
	)
	cfg := config.DefaultConfig()
	ca, err := langleytls.LoadOrCreateCA(filepath.Join(t.TempDir(), "proxy"))
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
	p, err := NewMITMProxy(MITMProxyConfig{
		Config:             cfg,
		Logger:             testLogger(),
		CA:                 ca,
		CertCache:          langleytls.NewCertCache(ca, 10),
		Redactor:           redactor,
		Store:              data,
		StartInPassthrough: true,
		OnCaptureStateChange: func(s CaptureStatus) {
			mu.Lock()
			if inCall {
				overlap = true
			}
			inCall = true
			mu.Unlock()
			time.Sleep(2 * time.Millisecond)
			mu.Lock()
			observed = append(observed, s)
			inCall = false
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		enabled := i%2 == 0
		go func(en bool) {
			defer wg.Done()
			p.SetCapture(en, false)
		}(enabled)
	}
	wg.Wait()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.captureMu.Lock()
		busy := p.notifying || len(p.notifyQueue) > 0
		p.captureMu.Unlock()
		if !busy {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if overlap {
		t.Fatal("OnCaptureStateChange callbacks executed concurrently")
	}
	if len(observed) == 0 {
		t.Fatal("expected state change notifications")
	}
	for i := 1; i < len(observed); i++ {
		if observed[i].Revision <= observed[i-1].Revision {
			t.Fatalf("non-monotonic revision order at %d: %d -> %d", i, observed[i-1].Revision, observed[i].Revision)
		}
	}
	final := p.CaptureStatus()
	if observed[len(observed)-1].State != final.State || observed[len(observed)-1].Revision != final.Revision {
		t.Fatalf("last notification %+v != final status %+v", observed[len(observed)-1], final)
	}
}

func TestEnableDuringThinkPausePreservesStream(t *testing.T) {
	release := make(chan struct{})
	time.AfterFunc(900*time.Millisecond, func() { close(release) })
	p, c, _, _, _ := captureFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: last\n\n")
	}), true)
	p.passthroughIdleThreshold = 500 * time.Millisecond
	resp, err := c.Get("https://api.openai.com/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	time.Sleep(600 * time.Millisecond)
	s := p.SetCapture(true, false)
	if s.State != CaptureStateEnabling || s.DrainingPassthroughTunnels != 1 {
		t.Fatalf("SetCapture(true, false) during think pause = %+v, want enabling/1", s)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "data: first\n\ndata: last\n\n" {
		t.Fatalf("non-force enable aborted streaming response: body=%q err=%v", string(body), err)
	}
}

type shortDeadlineConn struct{ net.Conn }

func (c shortDeadlineConn) SetReadDeadline(_ time.Time) error {
	return c.Conn.SetReadDeadline(time.Now().Add(120 * time.Millisecond))
}

func TestOneWayStreamUsesSharedActivity(t *testing.T) {
	clientPeer, client := net.Pipe()
	upstream, upstreamPeer := net.Pipe()
	defer clientPeer.Close()
	defer client.Close()
	defer upstream.Close()
	defer upstreamPeer.Close()

	p := &MITMProxy{passthroughIdleThreshold: 500 * time.Millisecond}
	tunnel := &captureTunnel{client: shortDeadlineConn{client}, upstream: upstream, draining: true}
	tunnel.lastActivity.Store(time.Now().UnixNano())
	done := make(chan struct{})
	go func() {
		p.tunnelCapture(tunnel)
		close(done)
	}()
	go func() { _, _ = io.Copy(io.Discard, clientPeer) }()

	start := time.Now()
	for time.Since(start) < 300*time.Millisecond {
		if _, err := upstreamPeer.Write([]byte("x")); err != nil {
			t.Fatalf("active downstream stream closed early after %v: %v", time.Since(start), err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = upstreamPeer.Close()
	<-done
}

type firstByteHookConn struct {
	net.Conn
	mu                 sync.Mutex
	trackReads         bool
	firstByteReads     int
	enteredSecondRead  chan struct{}
	enteredOnce        sync.Once
	holdAfterBytesRead bool
	bytesReadCh        chan struct{}
	bytesReadOnce      sync.Once
	releaseReadCh      chan struct{}
	ignoreDeadline     bool
	deadlineSetCh      chan struct{}
	deadlineOnce       sync.Once
}

func newFirstByteHookConn(inner net.Conn) *firstByteHookConn {
	return &firstByteHookConn{
		Conn:              inner,
		enteredSecondRead: make(chan struct{}),
		bytesReadCh:       make(chan struct{}),
		releaseReadCh:     make(chan struct{}),
		deadlineSetCh:     make(chan struct{}),
	}
}

func (c *firstByteHookConn) Read(b []byte) (int, error) {
	c.mu.Lock()
	tracked := c.trackReads
	if tracked {
		c.firstByteReads++
	}
	isSecond := tracked && c.firstByteReads >= 2
	c.mu.Unlock()
	if isSecond {
		c.enteredOnce.Do(func() { close(c.enteredSecondRead) })
	}
	n, err := c.Conn.Read(b)
	if isSecond && n > 0 {
		c.mu.Lock()
		hold := c.holdAfterBytesRead
		c.mu.Unlock()
		if hold {
			c.bytesReadOnce.Do(func() { close(c.bytesReadCh) })
			<-c.releaseReadCh
		}
	}
	return n, err
}

func (c *firstByteHookConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	ignore := c.ignoreDeadline
	c.mu.Unlock()
	if !t.IsZero() && ignore {
		c.deadlineOnce.Do(func() { close(c.deadlineSetCh) })
		return nil
	}
	return c.Conn.SetReadDeadline(t)
}

func dialHookedMITMKeepAlive(t *testing.T) (*MITMProxy, store.Store, *tls.Conn, *bufio.Reader, *firstByteHookConn) {
	t.Helper()
	p, _, data, _, _ := captureFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "2")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}), false)

	proxyListener := newPipeListener()
	proxyServer := &http.Server{Handler: p}
	proxyDone := make(chan struct{})
	go func() {
		defer close(proxyDone)
		_ = proxyServer.Serve(proxyListener)
	}()

	clientRaw, serverRaw := net.Pipe()
	hookConn := newFirstByteHookConn(serverRaw)
	proxyListener.conns <- hookConn

	t.Cleanup(func() {
		_ = clientRaw.Close()
		_ = serverRaw.Close()
		_ = proxyServer.Close()
		_ = proxyListener.Close()
		<-proxyDone
	})

	if _, err := io.WriteString(clientRaw, "CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	rawReader := bufio.NewReader(clientRaw)
	connectResp, err := http.ReadResponse(rawReader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	_ = connectResp.Body.Close()

	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(p.ca.CertPEM())
	tlsClient := tls.Client(clientRaw, &tls.Config{
		ServerName: "api.openai.com",
		RootCAs:    roots,
		NextProtos: []string{"http/1.1"},
	})
	if err := tlsClient.Handshake(); err != nil {
		t.Fatal(err)
	}
	hookConn.mu.Lock()
	hookConn.trackReads = true
	hookConn.mu.Unlock()

	if _, err := io.WriteString(tlsClient, "GET /v1/models HTTP/1.1\r\nHost: api.openai.com\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	tlsReader := bufio.NewReader(tlsClient)
	resp1, err := http.ReadResponse(tlsReader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp1.Body)
	_ = resp1.Body.Close()

	// Wait until the first request has completed and the session is parked in
	// its keep-alive first-byte read for the second request.
	<-hookConn.enteredSecondRead

	return p, data, tlsClient, tlsReader, hookConn
}

func TestPauseDoesNotAdmitUnscheduledIdleSession(t *testing.T) {
	t.Run("descheduled before Peek enters raw read", func(t *testing.T) {
		p, _, _, _, _ := captureFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), false)
		clientPeer, clientConn := net.Pipe()
		upstreamPeer, upstreamConn := net.Pipe()
		defer clientPeer.Close()
		defer upstreamPeer.Close()

		session := &captureSession{
			client:            clientConn,
			upstream:          upstreamConn,
			awaitingFirstByte: true,
			readSeq:           1,
		}
		session.awaitingFirstByteAtomic.Store(true)

		p.captureMu.Lock()
		if p.captureSessions == nil {
			p.captureSessions = make(map[*captureSession]struct{})
		}
		p.captureSessions[session] = struct{}{}
		p.captureMu.Unlock()
		defer func() {
			p.captureMu.Lock()
			delete(p.captureSessions, session)
			p.captureMu.Unlock()
		}()

		status := p.SetCapture(false, false)
		if status.State != CaptureStatePassthrough || status.DrainingMITMRequests != 0 {
			t.Fatalf("SetCapture(false, false) on pre-Peek idle session = %+v, want passthrough/0", status)
		}
		p.captureMu.Lock()
		busy := session.busy
		p.captureMu.Unlock()
		if busy {
			t.Fatal("pre-Peek idle session was marked busy")
		}
		if _, err := clientPeer.Write([]byte("x")); err == nil {
			t.Fatal("expected pre-Peek idle session client transport to be closed on pause")
		}
	})

	t.Run("parked in keep-alive read with no client write", func(t *testing.T) {
		p, _, tlsClient, _, _ := dialHookedMITMKeepAlive(t)

		status := p.SetCapture(false, false)
		if status.State != CaptureStatePassthrough || status.DrainingMITMRequests != 0 {
			t.Fatalf("SetCapture(false, false) on keep-alive idle session = %+v, want passthrough/0", status)
		}
		if _, err := io.WriteString(tlsClient, "GET /v1/models HTTP/1.1\r\nHost: api.openai.com\r\n\r\n"); err == nil {
			var one [1]byte
			if _, rerr := tlsClient.Read(one[:]); rerr == nil {
				t.Fatal("expected idle session client transport to be closed on pause")
			}
		}
	})
}

type directDeadlineConn struct {
	net.Conn
	raw net.Conn
}

func (c *directDeadlineConn) SetReadDeadline(d time.Time) error {
	return c.raw.SetReadDeadline(d)
}

func TestPauseAdmitsSessionWithPositiveFirstByteEvidence(t *testing.T) {
	t.Run("synchronized post-read hold", func(t *testing.T) {
		p, data, tlsClient, tlsReader, hookConn := dialHookedMITMKeepAlive(t)
		hookConn.mu.Lock()
		hookConn.holdAfterBytesRead = true
		hookConn.ignoreDeadline = true
		hookConn.mu.Unlock()

		writeDone := make(chan error, 1)
		go func() {
			_, err := io.WriteString(tlsClient, "GET /v1/models HTTP/1.1\r\nHost: api.openai.com\r\n\r\n")
			writeDone <- err
		}()

		<-hookConn.bytesReadCh
		status := p.SetCapture(false, false)
		if status.State != CaptureStateStopping || status.DrainingMITMRequests != 1 {
			t.Fatalf("SetCapture(false, false) while first-byte read is held = %+v, want stopping/1", status)
		}
		close(hookConn.releaseReadCh)
		if err := <-writeDone; err != nil {
			t.Fatalf("writing second request failed: %v", err)
		}
		resp2, err := http.ReadResponse(tlsReader, nil)
		if err != nil {
			t.Fatalf("reading second response failed: %v", err)
		}
		_, _ = io.ReadAll(resp2.Body)
		_ = resp2.Body.Close()

		after := waitCapture(t, p, CaptureStatePassthrough)
		if after.DrainingMITMRequests != 0 {
			t.Fatalf("after finishing admitted request = %+v, want passthrough/0", after)
		}
		if got := flowCount(t, data); got != 2 {
			t.Fatalf("persisted flow count = %d, want 2", got)
		}
	})

	t.Run("concurrent pending read without channel handoff", func(t *testing.T) {
		p, data, tlsClient, tlsReader, hookConn := dialHookedMITMKeepAlive(t)
		hookConn.mu.Lock()
		hookConn.holdAfterBytesRead = true
		hookConn.mu.Unlock()
		p.captureMu.Lock()
		for s := range p.captureSessions {
			s.client = &directDeadlineConn{Conn: s.client, raw: hookConn.Conn}
		}
		p.captureMu.Unlock()

		writeDone := make(chan error, 1)
		go func() {
			_, err := io.WriteString(tlsClient, "GET /v1/models HTTP/1.1\r\nHost: api.openai.com\r\n\r\n")
			writeDone <- err
		}()

		time.Sleep(20 * time.Millisecond)
		status := p.SetCapture(false, false)
		close(hookConn.releaseReadCh)
		if err := <-writeDone; err != nil {
			t.Fatalf("writing second request failed: %v", err)
		}
		if status.State != CaptureStateStopping || status.DrainingMITMRequests != 1 {
			t.Fatalf("SetCapture(false, false) at held-read boundary = %+v, want stopping/1", status)
		}
		resp2, err := http.ReadResponse(tlsReader, nil)
		if err != nil {
			t.Fatalf("reading second response failed: %v", err)
		}
		_, _ = io.ReadAll(resp2.Body)
		_ = resp2.Body.Close()

		after := waitCapture(t, p, CaptureStatePassthrough)
		if after.DrainingMITMRequests != 0 {
			t.Fatalf("after finishing admitted request = %+v, want passthrough/0", after)
		}
		if got := flowCount(t, data); got != 2 {
			t.Fatalf("persisted flow count = %d, want 2", got)
		}
	})

	t.Run("buffered TLS plaintext read without new raw read", func(t *testing.T) {
		p, _, _, _, _ := captureFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), false)
		clientPeer, clientConn := net.Pipe()
		upstreamPeer, upstreamConn := net.Pipe()
		defer clientPeer.Close()
		defer upstreamPeer.Close()

		session := &captureSession{
			client:            clientConn,
			upstream:          upstreamConn,
			awaitingFirstByte: true,
			readSeq:           1,
		}
		session.awaitingFirstByteAtomic.Store(true)

		p.captureMu.Lock()
		if p.captureSessions == nil {
			p.captureSessions = make(map[*captureSession]struct{})
		}
		p.captureSessions[session] = struct{}{}
		p.captureMu.Unlock()
		defer func() {
			p.captureMu.Lock()
			delete(p.captureSessions, session)
			p.captureMu.Unlock()
		}()

		enteredPlaintextRead := make(chan struct{})
		releasePlaintextRead := make(chan struct{})
		bufferedTLS := readerFunc(func(b []byte) (int, error) {
			n := copy(b, "GET /v1/messages HTTP/1.1\r\nHost: api.openai.com\r\nX-Partial: ")
			close(enteredPlaintextRead)
			<-releasePlaintextRead
			return n, nil
		})
		evidenceReader := &rawReadEvidenceReader{r: bufferedTLS, p: p, session: session}
		admReader := &captureAdmissionReader{r: evidenceReader, p: p, session: session}

		readDone := make(chan error, 1)
		go func() {
			buf := make([]byte, 128)
			_, err := admReader.Read(buf)
			readDone <- err
		}()

		<-enteredPlaintextRead
		status := p.SetCapture(false, false)
		if status.State != CaptureStateStopping || status.DrainingMITMRequests != 1 {
			close(releasePlaintextRead)
			t.Fatalf("SetCapture(false, false) during buffered TLS plaintext read = %+v, want stopping/1", status)
		}
		close(releasePlaintextRead)
		if err := <-readDone; err != nil {
			t.Fatalf("buffered TLS plaintext read failed: %v", err)
		}

		// Verify the read deadline on the underlying client transport was cleared
		// so the remainder of the partial request can be read from the socket.
		writeErr := make(chan error, 1)
		go func() {
			_, err := io.WriteString(clientPeer, "finished\r\n\r\n")
			writeErr <- err
		}()
		remBuf := make([]byte, 32)
		if _, err := clientConn.Read(remBuf); err != nil {
			t.Fatalf("reading remainder of partial request after buffered TLS admission failed: %v", err)
		}
		if err := <-writeErr; err != nil {
			t.Fatalf("client write of request remainder failed: %v", err)
		}
		p.finishMITMRequest(session, true)
		if after := p.CaptureStatus(); after.State != CaptureStatePassthrough || after.DrainingMITMRequests != 0 {
			t.Fatalf("after finishing buffered TLS request = %+v, want passthrough/0", after)
		}
	})
}

type readerFunc func(b []byte) (int, error)

func (f readerFunc) Read(b []byte) (int, error) {
	return f(b)
}

func TestSupersededPauseDoesNotMutateOrLeaveDeadlineOnIdleSession(t *testing.T) {
	p, data, tlsClient, tlsReader, hookConn := dialHookedMITMKeepAlive(t)
	hookConn.mu.Lock()
	hookConn.ignoreDeadline = true
	hookConn.mu.Unlock()

	pauseDone := make(chan CaptureStatus, 1)
	go func() {
		pauseDone <- p.SetCapture(false, false)
	}()

	<-hookConn.deadlineSetCh
	hookConn.mu.Lock()
	hookConn.ignoreDeadline = false
	hookConn.mu.Unlock()

	enableStatus := p.SetCapture(true, false)
	if enableStatus.State != CaptureStateCapturing {
		t.Fatalf("SetCapture(true, false) = %+v, want capturing", enableStatus)
	}
	<-pauseDone

	if cur := p.CaptureStatus(); cur.State != CaptureStateCapturing || cur.DrainingMITMRequests != 0 {
		t.Fatalf("final status after superseded pause = %+v, want capturing/0", cur)
	}
	if _, err := io.WriteString(tlsClient, "GET /v1/models HTTP/1.1\r\nHost: api.openai.com\r\n\r\n"); err != nil {
		t.Fatalf("writing second request after superseded pause failed: %v", err)
	}
	resp2, err := http.ReadResponse(tlsReader, nil)
	if err != nil {
		t.Fatalf("expected cleared read deadline on idle session after enable, got err=%v", err)
	}
	_, _ = io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if got := flowCount(t, data); got != 2 {
		t.Fatalf("persisted flow count = %d, want 2", got)
	}
}
