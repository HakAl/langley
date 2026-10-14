package proxy

import (
	"net"
	"sync"
	"testing"
	"time"
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
		p.trackConn(cp.pClient)
		p.trackConn(cp.pUp)
		p.tunnelWg.Add(1)
		go func() {
			defer p.tunnelWg.Done()
			defer p.untrackConn(cp.pClient)
			defer p.untrackConn(cp.pUp)
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
			p.trackConn(c1)
			p.trackConn(c2)
			p.untrackConn(c1)
			p.untrackConn(c2)
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
