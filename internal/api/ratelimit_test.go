package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiter_BurstAllowed(t *testing.T) {
	rl := NewRateLimiter(10, 100) // 10/sec sustained, 100 burst

	// Should allow burst of 100 requests
	for i := 0; i < 100; i++ {
		if !rl.Allow("127.0.0.1") {
			t.Errorf("request %d should be allowed within burst", i+1)
		}
	}

	// 101st request should be denied
	if rl.Allow("127.0.0.1") {
		t.Error("request after burst exhausted should be denied")
	}
}

func TestRateLimiter_RefillOverTime(t *testing.T) {
	rl := NewRateLimiter(100, 10) // 100/sec sustained, 10 burst (fast refill for testing)

	// Exhaust the burst
	for i := 0; i < 10; i++ {
		rl.Allow("127.0.0.1")
	}

	// Should be denied
	if rl.Allow("127.0.0.1") {
		t.Error("should be denied after burst exhausted")
	}

	// Wait for refill (100 tokens/sec = 1 token every 10ms)
	time.Sleep(50 * time.Millisecond)

	// Should now be allowed (at least a few tokens refilled)
	if !rl.Allow("127.0.0.1") {
		t.Error("should be allowed after refill time")
	}
}

func TestRateLimiter_SeparateIPsAreSeparate(t *testing.T) {
	rl := NewRateLimiter(10, 5) // 10/sec sustained, 5 burst

	// Exhaust IP1's burst
	for i := 0; i < 5; i++ {
		rl.Allow("192.168.1.1")
	}

	// IP1 should be denied
	if rl.Allow("192.168.1.1") {
		t.Error("IP1 should be denied after burst")
	}

	// IP2 should still be allowed
	if !rl.Allow("192.168.1.2") {
		t.Error("IP2 should be allowed - separate bucket")
	}
}

func TestRateLimiter_Middleware429(t *testing.T) {
	rl := NewRateLimiter(10, 2) // Low burst for easy testing

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First 2 requests should succeed
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.1:12345"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("request %d: got %d, want %d", i+1, rr.Code, http.StatusOK)
		}
	}

	// 3rd request should be rate limited
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("got %d, want %d (429 Too Many Requests)", rr.Code, http.StatusTooManyRequests)
	}

	// Check Retry-After header
	if rr.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After header")
	}
}

func TestExtractIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		want       string
	}{
		{"IPv4 with port", "192.168.1.1:8080", "192.168.1.1"},
		{"IPv4 without port", "192.168.1.1", "192.168.1.1"},
		{"IPv6 with port", "[::1]:8080", "::1"},
		{"localhost", "127.0.0.1:54321", "127.0.0.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remoteAddr

			got := extractIP(req)
			if got != tt.want {
				t.Errorf("extractIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
