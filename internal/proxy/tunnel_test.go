package proxy

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func testTunnelLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestTunnel_BidirectionalCopy(t *testing.T) {
	t.Parallel()

	client, proxyClient := net.Pipe()
	proxyUpstream, upstream := net.Pipe()

	go tunnelWithTimeout(proxyClient, proxyUpstream, testTunnelLogger(), "test.com", 5*time.Second)

	// client -> upstream
	msg := []byte("hello from client")
	if _, err := client.Write(msg); err != nil {
		t.Fatalf("client write: %v", err)
	}

	buf := make([]byte, 64)
	n, err := upstream.Read(buf)
	if err != nil {
		t.Fatalf("upstream read: %v", err)
	}
	if string(buf[:n]) != "hello from client" {
		t.Errorf("upstream got %q, want %q", buf[:n], "hello from client")
	}

	// upstream -> client
	msg = []byte("hello from upstream")
	if _, err := upstream.Write(msg); err != nil {
		t.Fatalf("upstream write: %v", err)
	}

	n, err = client.Read(buf)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(buf[:n]) != "hello from upstream" {
		t.Errorf("client got %q, want %q", buf[:n], "hello from upstream")
	}

	client.Close()
	upstream.Close()
}

func TestTunnel_ClosePropagation(t *testing.T) {
	t.Parallel()

	client, proxyClient := net.Pipe()
	proxyUpstream, upstream := net.Pipe()

	done := make(chan struct{})
	go func() {
		tunnelWithTimeout(proxyClient, proxyUpstream, testTunnelLogger(), "test.com", 5*time.Second)
		close(done)
	}()

	// Close client side — tunnel should tear down both sides
	client.Close()

	// upstream should see EOF
	buf := make([]byte, 1)
	_, err := upstream.Read(buf)
	if err == nil {
		t.Error("upstream Read should have returned an error after client close")
	}

	// tunnel goroutines should complete
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel did not shut down after client close")
	}
}

func TestTunnel_IdleTimeout(t *testing.T) {
	t.Parallel()

	client, proxyClient := net.Pipe()
	proxyUpstream, upstream := net.Pipe()

	done := make(chan struct{})
	go func() {
		tunnelWithTimeout(proxyClient, proxyUpstream, testTunnelLogger(), "test.com", 100*time.Millisecond)
		close(done)
	}()

	// Don't send any data — tunnel should close after idle timeout
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel did not shut down after idle timeout")
	}

	// Both pipes should be closed
	_, err := client.Write([]byte("late"))
	if err == nil {
		t.Error("client write should fail after tunnel timeout")
	}
	_, err = upstream.Write([]byte("late"))
	if err == nil {
		t.Error("upstream write should fail after tunnel timeout")
	}
}
