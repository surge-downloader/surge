package concurrent

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/surge-downloader/surge/internal/utils"
)

// RateLimitError is returned when a 429 response is received
// It contains the recommended wait duration based on Retry-After header or backoff
type RateLimitError struct {
	WaitDuration time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited (429), retry after %v", e.WaitDuration)
}

// RateLimiter tracks rate limiting state across all workers for a download
// It provides intelligent backoff and concurrency scaling based on 429 responses
type RateLimiter struct {
	// blockedUntil is a Unix nanosecond timestamp when rate limiting expires
	// If current time < blockedUntil, workers should wait
	blockedUntil atomic.Int64

	// consecutiveHits tracks how many 429s we've received in a row
	// Used for exponential backoff when no Retry-After is provided
	consecutiveHits atomic.Int32

	// mu protects backoff calculation
	mu sync.Mutex
}

// NewRateLimiter creates a new rate limiter instance
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{}
}

// Handle429 processes a 429 response and updates the rate limiter state
// It returns the duration that workers should wait before retrying
func (rl *RateLimiter) Handle429(resp *http.Response) time.Duration {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	hits := rl.consecutiveHits.Add(1)

	// Try to parse Retry-After header
	if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
		// Retry-After can be either seconds or an HTTP-date
		if seconds, err := strconv.Atoi(retryAfter); err == nil {
			// It's a number of seconds
			waitDuration := time.Duration(seconds) * time.Second
			rl.setBlockedUntil(waitDuration)
			utils.Debug("RateLimiter: got 429 with Retry-After=%d seconds (hit #%d)", seconds, hits)
			return waitDuration
		}

		// Try parsing as HTTP-date (e.g., "Fri, 31 Dec 2021 23:59:59 GMT")
		if t, err := http.ParseTime(retryAfter); err == nil {
			waitDuration := time.Until(t)
			if waitDuration < 0 {
				waitDuration = time.Second // Minimum wait
			}
			rl.setBlockedUntil(waitDuration)
			utils.Debug("RateLimiter: got 429 with Retry-After date (wait %v, hit #%d)", waitDuration, hits)
			return waitDuration
		}
	}

	// No Retry-After header - use exponential backoff
	// Base: 1s, then 2s, 4s, 8s, 16s, 32s, capped at 60s
	baseDelay := time.Second
	backoffMultiplier := int64(1) << min(int(hits-1), 5) // Cap at 2^5 = 32
	waitDuration := time.Duration(backoffMultiplier) * baseDelay

	maxWait := 60 * time.Second
	if waitDuration > maxWait {
		waitDuration = maxWait
	}

	rl.setBlockedUntil(waitDuration)
	utils.Debug("RateLimiter: got 429 without Retry-After, backing off %v (hit #%d)", waitDuration, hits)

	return waitDuration
}

// setBlockedUntil sets the global blocked timestamp
func (rl *RateLimiter) setBlockedUntil(duration time.Duration) {
	newBlockedUntil := time.Now().Add(duration).UnixNano()

	// Only update if this extends the block period
	for {
		current := rl.blockedUntil.Load()
		if newBlockedUntil <= current {
			return // Already blocked for longer
		}
		if rl.blockedUntil.CompareAndSwap(current, newBlockedUntil) {
			return
		}
	}
}

// WaitIfBlocked checks if we're currently rate limited and waits if so
// Returns true if we waited, false if we were not blocked
func (rl *RateLimiter) WaitIfBlocked() bool {
	blockedUntil := rl.blockedUntil.Load()
	if blockedUntil == 0 {
		return false
	}

	waitDuration := time.Until(time.Unix(0, blockedUntil))
	if waitDuration <= 0 {
		return false
	}

	utils.Debug("RateLimiter: worker waiting %v due to rate limit", waitDuration)
	time.Sleep(waitDuration)
	return true
}

// ReportSuccess resets the consecutive hit counter on successful request
func (rl *RateLimiter) ReportSuccess() {
	if rl.consecutiveHits.Load() > 0 {
		rl.consecutiveHits.Store(0)
		utils.Debug("RateLimiter: success reported, reset consecutive hits counter")
	}
}

// IsBlocked returns true if we're currently rate limited
func (rl *RateLimiter) IsBlocked() bool {
	blockedUntil := rl.blockedUntil.Load()
	if blockedUntil == 0 {
		return false
	}
	return time.Now().UnixNano() < blockedUntil
}

// BlockDuration returns how long until the rate limit expires
// Returns 0 if not currently blocked
func (rl *RateLimiter) BlockDuration() time.Duration {
	blockedUntil := rl.blockedUntil.Load()
	if blockedUntil == 0 {
		return 0
	}
	duration := time.Until(time.Unix(0, blockedUntil))
	if duration < 0 {
		return 0
	}
	return duration
}
