package main

import (
	"net"
	"strconv"
	"testing"
)

func TestListenWithFallback_FirstPortAvailable(t *testing.T) {
	// Find a free port
	freePort := findFreePort(t)

	// Try to listen with fallback - should get the first port
	ln, actualAddr, err := listenWithFallback("localhost:"+strconv.Itoa(freePort), 5)
	if err != nil {
		t.Fatalf("listenWithFallback failed: %v", err)
	}
	defer ln.Close()

	// Should get the requested port
	expectedAddr := net.JoinHostPort("localhost", strconv.Itoa(freePort))
	if actualAddr != expectedAddr {
		t.Errorf("expected addr %s, got %s", expectedAddr, actualAddr)
	}
}

func TestListenWithFallback_PortInUse(t *testing.T) {
	// Occupy a port
	firstPort := findFreePort(t)
	blocker, err := net.Listen("tcp", "localhost:"+strconv.Itoa(firstPort))
	if err != nil {
		t.Fatalf("failed to occupy port: %v", err)
	}
	defer blocker.Close()

	// Try to listen with fallback - should fall back to next port
	ln, actualAddr, err := listenWithFallback("localhost:"+strconv.Itoa(firstPort), 5)
	if err != nil {
		t.Fatalf("listenWithFallback failed: %v", err)
	}
	defer ln.Close()

	// Should NOT get the first port (it's blocked)
	blockedAddr := net.JoinHostPort("localhost", strconv.Itoa(firstPort))
	if actualAddr == blockedAddr {
		t.Errorf("should have fallen back from blocked port %s", blockedAddr)
	}

	// Should get a higher port
	_, portStr, _ := net.SplitHostPort(actualAddr)
	actualPort, _ := strconv.Atoi(portStr)
	if actualPort <= firstPort {
		t.Errorf("expected port > %d, got %d", firstPort, actualPort)
	}
}

func TestListenWithFallback_AllPortsInUse(t *testing.T) {
	// Occupy multiple consecutive ports
	basePort := findFreePort(t)
	blockers := make([]net.Listener, 3)
	for i := 0; i < 3; i++ {
		ln, err := net.Listen("tcp", "localhost:"+strconv.Itoa(basePort+i))
		if err != nil {
			// Clean up and skip if we can't occupy all ports
			for j := 0; j < i; j++ {
				blockers[j].Close()
			}
			t.Skipf("could not occupy port %d: %v", basePort+i, err)
		}
		blockers[i] = ln
	}
	defer func() {
		for _, ln := range blockers {
			if ln != nil {
				ln.Close()
			}
		}
	}()

	// Try with only 3 attempts - should fail since all 3 ports are blocked
	_, _, err := listenWithFallback("localhost:"+strconv.Itoa(basePort), 3)
	if err == nil {
		t.Error("expected error when all ports are in use, got nil")
	}
}

func TestIsAddrInUse(t *testing.T) {
	tests := []struct {
		name     string
		errMsg   string
		expected bool
	}{
		{"unix error", "listen tcp :9090: bind: address already in use", true},
		{"windows error", "listen tcp :9090: bind: Only one usage of each socket address", true},
		{"generic EADDRINUSE", "EADDRINUSE", true},
		{"permission denied", "listen tcp :80: bind: permission denied", false},
		{"nil error", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.errMsg != "" {
				err = &testError{msg: tt.errMsg}
			}
			if got := isAddrInUse(err); got != tt.expected {
				t.Errorf("isAddrInUse() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// testError is a simple error type for testing
type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

// findFreePort finds an available TCP port
func findFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	defer ln.Close()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return port
}
