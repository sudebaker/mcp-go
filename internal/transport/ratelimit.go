package transport

import (
	"errors"
	"net/http"
	"sync"
	"time"
)

// RateLimiter implements a per-client token bucket rate limiting algorithm.
// It tracks request rates per client IP address (or X-Forwarded-For header)
// and enforces configurable requests-per-second (RPS) limits with burst capacity.
//
// Security features:
//   - Per-client isolation prevents one client affecting others
//   - Background cleanup goroutine prevents memory leaks from idle clients
//   - Thread-safe with RWMutex for concurrent access
//
// Example configuration:
//   limiter := NewRateLimiter(10.0, 20) // 10 RPS with burst of 20
type RateLimiter struct {
	limiters      map[string]*tokenBucket // Per-client bucket state
	mu            sync.RWMutex            // Protects limiters map
	rps           float64                // Refill rate (tokens per second)
	burst         int                    // Maximum bucket capacity
	cleanupTicker *time.Ticker          // Periodic cleanup trigger
	cleanupStop   chan struct{}          // Cleanup goroutine shutdown signal
	maxIdleTime   time.Duration          // Inactive client TTL before cleanup
	stopOnce      sync.Once             // Ensures cleanup goroutine stops only once
}

// tokenBucket represents a client's rate limiting state using the token bucket algorithm.
// Tokens accumulate at 'rate' per second up to 'capacity', consumed by each request.
type tokenBucket struct {
	tokens     float64 // Current available tokens (fractional allowed)
	lastUpdate time.Time // Last token refill timestamp
	capacity   float64 // Maximum token storage
	rate       float64 // Token generation rate per second
}

// NewRateLimiter creates a rate limiter with specified RPS and burst parameters.
//
// Args:
//   rps: Requests per second allowed (refill rate). Must be > 0.
//   burst: Maximum burst size (initial/full capacity). Must be > 0.
//
// Returns:
//   Configured RateLimiter with background cleanup goroutine running.
//
// Example:
//   limiter := NewRateLimiter(10.0, 20) // 10 req/s sustained, burst up to 20
func NewRateLimiter(rps float64, burst int) *RateLimiter {
	rl := &RateLimiter{
		limiters:    make(map[string]*tokenBucket),
		rps:         rps,
		burst:       burst,
		cleanupStop: make(chan struct{}),
		maxIdleTime: 10 * time.Minute,
	}
	rl.startCleanup()
	return rl
}

// getLimiter retrieves or creates a token bucket for a specific client.
// Uses double-checked locking pattern to minimize contention.
//
// Thread-safe: Yes (uses RWMutex)
//
// Args:
//   clientID: Unique client identifier (typically IP address)
//
// Returns:
//   Pointer to the client's tokenBucket
func (rl *RateLimiter) getLimiter(clientID string) *tokenBucket {
	rl.mu.RLock()
	limiter, exists := rl.limiters[clientID]
	rl.mu.RUnlock()

	if exists {
		return limiter
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Double-check after acquiring write lock
	if limiter, exists = rl.limiters[clientID]; exists {
		return limiter
	}

	limiter = &tokenBucket{
		tokens:     float64(rl.burst),
		lastUpdate: time.Now(),
		capacity:   float64(rl.burst),
		rate:       rl.rps,
	}
	rl.limiters[clientID] = limiter
	return limiter
}

// Allow checks if a single request from the client should be allowed.
// Convenience method wrapping allowN(clientID, 1).
//
// Args:
//   clientID: Unique client identifier
//
// Returns:
//   true if the request is allowed, false if rate limited
func (rl *RateLimiter) Allow(clientID string) bool {
	return rl.allowN(clientID, 1) == nil
}

// allowN checks if n requests from the client should be allowed.
//
// Uses the token bucket algorithm:
//   1. Calculate elapsed time since last update
//   2. Add tokens based on elapsed time and rate
//   3. Cap tokens at capacity (no overflow)
//   4. If n <= tokens, consume tokens and allow
//   5. Otherwise, reject with retry suggestion
//
// Thread-safe: Yes
//
// Args:
//   clientID: Unique client identifier
//   n: Number of tokens to consume (usually 1)
//
// Returns:
//   nil if allowed, or RateLimitExceededError with RetryAfter duration
func (rl *RateLimiter) allowN(clientID string, n int) error {
	limiter := rl.getLimiter(clientID)

	now := time.Now()
	elapsed := now.Sub(limiter.lastUpdate).Seconds()
	limiter.lastUpdate = now

	limiter.tokens += elapsed * limiter.rate
	if limiter.tokens > limiter.capacity {
		limiter.tokens = limiter.capacity
	}

	if float64(n) <= limiter.tokens {
		limiter.tokens -= float64(n)
		return nil
	}

	return &RateLimitExceededError{
		RetryAfter: time.Duration(float64(time.Second) * ((float64(n) - limiter.tokens) / limiter.rate)),
	}
}

// Middleware returns an http.Handler that enforces rate limiting.
// Applied as middleware around the MCP HTTP handlers.
//
// Args:
//   next: The downstream HTTP handler to wrap
//
// Returns:
//   HTTP middleware that checks rate limits before passing requests
//
// Behavior:
//   - Extracts client ID from X-Forwarded-For header or RemoteAddr
//   - If rate limited, responds with 429 Too Many Requests
//   - Retry-After header set with suggested wait time
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientID := getClientID(r)

		err := rl.allowN(clientID, 1)
		if err != nil {
			var rateErr *RateLimitExceededError
			if errors.As(err, &rateErr) {
				w.Header().Set("Retry-After", rateErr.RetryAfter.String())
				http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			} else {
				http.Error(w, "Internal server error", http.StatusInternalServerError)
			}
			return
		}

		next.ServeHTTP(w, r)
	})
}

// getClientID extracts client identifier from the HTTP request.
// Uses X-Forwarded-For header if present (for proxied requests),
// otherwise falls back to RemoteAddr.
func getClientID(r *http.Request) string {
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		return xff
	}
	return r.RemoteAddr
}

// RateLimitExceededError is returned when a client exceeds their rate limit.
type RateLimitExceededError struct {
	RetryAfter time.Duration // Suggested wait time before retry
}

func (e *RateLimitExceededError) Error() string {
	return "rate limit exceeded"
}

// Reset clears the rate limit state for a specific client.
// Useful when a client's session ends and you want fresh rate limits.
//
// Thread-safe: Yes
//
// Args:
//   clientID: The client whose state should be cleared
func (rl *RateLimiter) Reset(clientID string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.limiters, clientID)
}

// ResetAll clears all client rate limit states.
// Use with caution - this affects all clients simultaneously.
//
// Thread-safe: Yes
func (rl *RateLimiter) ResetAll() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.limiters = make(map[string]*tokenBucket)
}

// startCleanup launches a background goroutine that periodically
// removes idle client buckets to prevent memory growth.
// Runs every 5 minutes and cleans up clients idle for > 10 minutes.
func (rl *RateLimiter) startCleanup() {
	rl.cleanupTicker = time.NewTicker(5 * time.Minute)
	go func() {
		for {
			select {
			case <-rl.cleanupTicker.C:
				rl.cleanup()
			case <-rl.cleanupStop:
				rl.cleanupTicker.Stop()
				return
			}
		}
	}()
}

// cleanup removes token buckets for clients that have been idle
// beyond maxIdleTime (10 minutes by default).
//
// Thread-safe: Yes (acquires write lock)
func (rl *RateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	for clientID, limiter := range rl.limiters {
		if now.Sub(limiter.lastUpdate) > rl.maxIdleTime {
			delete(rl.limiters, clientID)
		}
	}
}

// Stop terminates the background cleanup goroutine.
// Safe to call multiple times (uses sync.Once).
func (rl *RateLimiter) Stop() {
	rl.stopOnce.Do(func() {
		close(rl.cleanupStop)
	})
}
