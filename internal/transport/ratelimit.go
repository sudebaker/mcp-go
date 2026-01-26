package transport

import (
	"errors"
	"net/http"
	"sync"
	"time"
)

type RateLimiter struct {
	limiters      map[string]*tokenBucket
	mu            sync.RWMutex
	rps           float64
	burst         int
	cleanupTicker *time.Ticker
	cleanupStop   chan struct{}
	maxIdleTime   time.Duration
}

type tokenBucket struct {
	tokens     float64
	lastUpdate time.Time
	capacity   float64
	rate       float64
}

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

func (rl *RateLimiter) getLimiter(clientID string) *tokenBucket {
	rl.mu.RLock()
	limiter, exists := rl.limiters[clientID]
	rl.mu.RUnlock()

	if exists {
		return limiter
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

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

func (rl *RateLimiter) Allow(clientID string) bool {
	return rl.allowN(clientID, 1) == nil
}

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

func getClientID(r *http.Request) string {
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		return xff
	}
	return r.RemoteAddr
}

type RateLimitExceededError struct {
	RetryAfter time.Duration
}

func (e *RateLimitExceededError) Error() string {
	return "rate limit exceeded"
}

func (rl *RateLimiter) Reset(clientID string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.limiters, clientID)
}

func (rl *RateLimiter) ResetAll() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.limiters = make(map[string]*tokenBucket)
}

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

func (rl *RateLimiter) Stop() {
	close(rl.cleanupStop)
}
