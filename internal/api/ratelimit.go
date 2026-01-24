package api

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// RateLimiter implements a token bucket rate limiter per source IP.
// Provides burst capacity for legitimate traffic while preventing abuse.
type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     float64 // tokens per second (sustained rate)
	burst    int     // max tokens (burst capacity)
	cleanupT time.Duration
}

type bucket struct {
	tokens    float64
	lastCheck time.Time
}

// NewRateLimiter creates a rate limiter with the given sustained rate and burst capacity.
// - rate: sustained requests per second (e.g., 20)
// - burst: maximum burst capacity (e.g., 100)
func NewRateLimiter(rate float64, burst int) *RateLimiter {
	rl := &RateLimiter{
		buckets:  make(map[string]*bucket),
		rate:     rate,
		burst:    burst,
		cleanupT: 5 * time.Minute,
	}

	// Start cleanup goroutine
	go rl.cleanup()

	return rl
}

// Allow checks if a request from the given IP should be allowed.
// Returns true if allowed, false if rate limited.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()

	b, ok := rl.buckets[ip]
	if !ok {
		// New client, start with full bucket
		rl.buckets[ip] = &bucket{
			tokens:    float64(rl.burst) - 1, // -1 for this request
			lastCheck: now,
		}
		return true
	}

	// Refill tokens based on elapsed time
	elapsed := now.Sub(b.lastCheck).Seconds()
	b.tokens += elapsed * rl.rate
	if b.tokens > float64(rl.burst) {
		b.tokens = float64(rl.burst)
	}
	b.lastCheck = now

	// Check if we can allow this request
	if b.tokens >= 1 {
		b.tokens--
		return true
	}

	return false
}

// cleanup periodically removes stale buckets
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(rl.cleanupT)
	defer ticker.Stop()

	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for ip, b := range rl.buckets {
			// Remove buckets that haven't been used for cleanupT
			if now.Sub(b.lastCheck) > rl.cleanupT {
				delete(rl.buckets, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// Middleware returns an HTTP middleware that applies rate limiting.
// Returns 429 Too Many Requests when rate is exceeded.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r)

		if !rl.Allow(ip) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// extractIP extracts the client IP from the request.
// Handles X-Forwarded-For for proxied requests, but prefers direct RemoteAddr
// for security (X-Forwarded-For can be spoofed).
func extractIP(r *http.Request) string {
	// Use RemoteAddr directly - more reliable than headers which can be spoofed
	// For a local-only tool, this is the right choice
	ip := r.RemoteAddr

	// Strip port from IP:port format
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		// Check if this looks like IPv6 [::1]:port
		if strings.HasPrefix(ip, "[") {
			if bracketIdx := strings.Index(ip, "]:"); bracketIdx != -1 {
				ip = ip[1:bracketIdx]
			}
		} else {
			ip = ip[:idx]
		}
	}

	return ip
}
