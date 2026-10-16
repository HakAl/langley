package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	langleytls "github.com/HakAl/langley/internal/tls"
)

// TestShutdownDrainsTunnels verifies that closeTunnels + tunnelWg.Wait drains
// tracked passthrough tunnels promptly instead of waiting for the 5-minute
// idle timeout (langley-ga3l).
func TestShutdownDrainsTunnels(t *testing.T) {
	t.Parallel()

	p := &MITMProxy{
		logger:      testTunnelLogger(),
		tunnelConns: make(map[net.Conn]struct{}),
	}

	const numTunnels = 3
	type connPair struct {
		client   net.Conn // external end (simulates real client)
		pClient  net.Conn // proxy-side client conn given to tunnel
		pUp      net.Conn // proxy-side upstream conn given to tunnel
		upstream net.Conn // external end (simulates real upstream)
	}

	pairs := make([]connPair, numTunnels)
	for i := range pairs {
		c, pc := net.Pipe()
		pu, u := net.Pipe()
		pairs[i] = connPair{client: c, pClient: pc, pUp: pu, upstream: u}
	}

	// Simulate what handleConnectPassthrough does: track and launch tunnels.
	for i := range pairs {
		cp := pairs[i]
		if !p.beginHijackedSession(cp.pClient, cp.pUp) { t.Fatal("session rejected") }
		go func() {
			defer p.endHijackedSession([]io.Closer{cp.pClient, cp.pUp}, cp.pClient, cp.pUp)
			tunnel(cp.pClient, cp.pUp, p.logger, "test.com")
		}()
	}

	// closeTunnels + Wait should finish quickly — not stuck for 5 min.
	done := make(chan struct{})
	go func() {
		p.closeTunnels()
		p.tunnelWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("closeTunnels + tunnelWg.Wait did not return promptly")
	}

	// All proxy-side connections should be closed.
	for i, cp := range pairs {
		if _, err := cp.pClient.Write([]byte("x")); err == nil {
			t.Errorf("tunnel %d: proxy-client conn still writable", i)
		}
		if _, err := cp.pUp.Write([]byte("x")); err == nil {
			t.Errorf("tunnel %d: proxy-upstream conn still writable", i)
		}
	}

	// Tracking map should be empty.
	p.tunnelMu.Lock()
	remaining := len(p.tunnelConns)
	p.tunnelMu.Unlock()
	if remaining != 0 {
		t.Errorf("tunnelConns map has %d entries, want 0", remaining)
	}

	// Clean up external ends.
	for _, cp := range pairs {
		cp.client.Close()
		cp.upstream.Close()
	}
}

// TestShutdownNoTunnels verifies closeTunnels is safe when there are no tunnels.
func TestShutdownNoTunnels(t *testing.T) {
	t.Parallel()

	p := &MITMProxy{
		logger:      testTunnelLogger(),
		tunnelConns: make(map[net.Conn]struct{}),
	}

	// Should not panic or block.
	p.closeTunnels()

	done := make(chan struct{})
	go func() {
		p.tunnelWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("tunnelWg.Wait blocked with no tunnels")
	}
}

// TestShutdownConcurrentTrackClose verifies no races between tracking and closing.
func TestShutdownConcurrentTrackClose(t *testing.T) {
	t.Parallel()

	p := &MITMProxy{
		logger:      testTunnelLogger(),
		tunnelConns: make(map[net.Conn]struct{}),
	}

	var wg sync.WaitGroup

	// Spawn goroutines that track/untrack connections concurrently with close.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c1, c2 := net.Pipe()
			if p.beginHijackedSession(c1, c2) {
				p.endHijackedSession([]io.Closer{c1, c2}, c1, c2)
			}
			c1.Close()
			c2.Close()
		}()
	}

	// Concurrently close all tunnels.
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.closeTunnels()
	}()

	wg.Wait()
}

// TestShutdownDrainsMITMTLSConnections verifies that active MITM TLS sessions
// in handleTLSConnection are tracked and drained promptly on shutdown (langley-dui2).
func TestShutdownDrainsMITMTLSConnections(t *testing.T) {
	t.Parallel()

	p := &MITMProxy{
		logger:      testTunnelLogger(),
		tunnelConns: make(map[net.Conn]struct{}),
	}

	c, pc := net.Pipe()
	pu, u := net.Pipe()
	defer c.Close()
	defer u.Close()

	// Use an httptest certificate locally; no upstream API calls are made.
	certServer := httptest.NewTLSServer(http.NotFoundHandler())
	defer certServer.Close()
	serverConfig := &tls.Config{Certificates: certServer.TLS.Certificates}
	tlsClient := tls.Server(pc, serverConfig)
	tlsUpstream := tls.Client(pu, &tls.Config{InsecureSkipVerify: true})
	c.SetDeadline(time.Now().Add(2 * time.Second))
	u.SetDeadline(time.Now().Add(2 * time.Second))
	handshakes := make(chan error, 2)
	go func() { handshakes <- tls.Client(c, &tls.Config{InsecureSkipVerify: true}).Handshake() }()
	go func() { handshakes <- tls.Server(u, serverConfig).Handshake() }()
	if err := tlsClient.Handshake(); err != nil {
		t.Fatal(err)
	}
	if err := tlsUpstream.Handshake(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := <-handshakes; err != nil {
			t.Fatal(err)
		}
	}
	c.SetDeadline(time.Time{})
	u.SetDeadline(time.Time{})

	started := make(chan struct{})
	go func() {
		close(started)
		p.handleTLSConnection(tlsClient, tlsUpstream, "api.anthropic.com")
	}()
	<-started

	// Wait briefly for handleTLSConnection to register both connections
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.tunnelMu.Lock()
		n := len(p.tunnelConns)
		p.tunnelMu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	p.tunnelMu.Lock()
	registered := len(p.tunnelConns)
	p.tunnelMu.Unlock()
	if registered != 2 {
		p.closeTunnels()
		t.Fatalf("active TLS transports tracked = %d, want 2", registered)
	}

	done := make(chan struct{})
	go func() {
		p.closeTunnels()
		p.tunnelWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleTLSConnection did not drain promptly on closeTunnels")
	}

	p.tunnelMu.Lock()
	remaining := len(p.tunnelConns)
	p.tunnelMu.Unlock()
	if remaining != 0 {
		t.Errorf("tunnelConns map has %d entries, want 0", remaining)
	}
}

// TestShutdownRejectsSessions covers both admission paths after the shutdown sweep.
func TestShutdownRejectsSessions(t *testing.T) {
	p := &MITMProxy{tunnelConns: make(map[net.Conn]struct{})}
	p.closeTunnels()
	for _, admit := range []func(net.Conn) bool{
		func(c net.Conn) bool { return p.beginHijackedSession(c) },
		p.registerSessionConn,
	} {
		c, peer := net.Pipe()
		if admit(c) {
			t.Fatal("admitted a connection after shutdown")
		}
		if _, err := peer.Write([]byte("x")); err == nil {
			t.Error("rejected connection was not closed")
		}
		peer.Close()
	}
	if p.registerSessionConn(nil) {
		t.Error("nil registration accepted after shutdown")
	}
	p.tunnelWg.Wait()
}

// A controlled closer proves Wait includes cleanup, rather than just request work.
type shutdownBlockingConn interface {
	net.Conn
}

type shutdownCloseConn struct {
	shutdownBlockingConn
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *shutdownCloseConn) Close() error {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return c.shutdownBlockingConn.Close()
}

func TestShutdownWaitIncludesCleanup(t *testing.T) {
	p := &MITMProxy{tunnelConns: make(map[net.Conn]struct{})}
	c, peer := net.Pipe()
	defer peer.Close()
	blocked := &shutdownCloseConn{shutdownBlockingConn: c, entered: make(chan struct{}), release: make(chan struct{})}
	if !p.beginHijackedSession(c) {
		t.Fatal("session rejected")
	}
	go p.endHijackedSession([]io.Closer{blocked}, c)
	<-blocked.entered
	p.closeTunnels()
	done := make(chan struct{})
	go func() {
		p.tunnelWg.Wait()
		close(done)
	}()
	select {
	case <-done:
		t.Error("Wait returned before cleanup finished")
	case <-time.After(25 * time.Millisecond):
	}
	close(blocked.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Wait did not finish after cleanup")
	}
}

// This exercises production acquisition: CONNECT is hijacked, but the client
// never sends a TLS ClientHello. Shutdown must own and interrupt that handshake.
func TestShutdownInterruptsHijackedHandshake(t *testing.T) {
	p := &MITMProxy{
		logger:      testTunnelLogger(),
		certCache:   langleytls.NewCertCache(nil, 1),
		tunnelConns: make(map[net.Conn]struct{}),
	}
	server := httptest.NewServer(http.HandlerFunc(p.handleConnectMITM))
	defer server.Close()
	client, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(client, "CONNECT api.example.test:443 HTTP/1.1\r\nHost: api.example.test:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d", response.StatusCode)
	}
	p.tunnelMu.Lock()
	tracked := len(p.tunnelConns)
	p.tunnelMu.Unlock()
	if tracked != 1 {
		t.Fatalf("connections tracked before ClientHello = %d, want 1", tracked)
	}
	p.closeTunnels()
	done := make(chan struct{})
	go func() {
		p.tunnelWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stalled hijacked handshake survived shutdown")
	}
}

func TestShutdownCancelsUpstreamDial(t *testing.T) {
	dialEntered := make(chan struct{})
	p := &MITMProxy{
		logger:      testTunnelLogger(),
		tunnelConns: make(map[net.Conn]struct{}),
		dialHook: func(ctx context.Context, network, addr string) (net.Conn, error) {
			close(dialEntered)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	dialDone := make(chan error, 1)
	go func() {
		_, err := p.dialUpstreamTCP("api.example.test:443", 10*time.Second)
		dialDone <- err
	}()

	<-dialEntered
	p.closeTunnels()

	select {
	case err := <-dialDone:
		if err == nil {
			t.Fatal("expected canceled dial error, got nil")
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight upstream dial was not canceled by closeTunnels")
	}
}

