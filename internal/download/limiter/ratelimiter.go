// Package limiter provides global rate limiting coordination across downloads.
// When one download from a specific host gets rate-limited (HTTP 429),
// all downloads from that host will pause to avoid IP bans.
package limiter

import (
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/surge-downloader/surge/internal/utils"
)

// RateLimitError is returned when a 429 response is received.
// It contains the recommended wait duration based on Retry-After header or backoff.
type RateLimitError struct {
	WaitDuration time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited (429), retry after %v", e.WaitDuration)
}

// RateLimiter tracks rate limiting state for a specific host.
// It provides intelligent backoff based on 429 responses with jitter.
type RateLimiter struct {
	Host string // The hostname this limiter is for

	// blockedUntil is a Unix nanosecond timestamp when rate limiting expires
	blockedUntil atomic.Int64

	// consecutiveHits tracks how many 429s we've received in a row
	consecutiveHits atomic.Int32

	// mu protects backoff calculation
	mu sync.Mutex
}

// NewRateLimiter creates a new rate limiter for a specific host
func NewRateLimiter(host string) *RateLimiter {
	return &RateLimiter{
		Host: host,
	}
}

// Handle429 processes a 429 response and updates the rate limiter state.
// It returns the duration that workers should wait before retrying.
// Includes jitter (±10%) to prevent thundering herd.
func (rl *RateLimiter) Handle429(resp *http.Response) time.Duration {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	hits := rl.consecutiveHits.Add(1)

	var waitDuration time.Duration

	// Try to parse Retry-After header
	if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
		// Retry-After can be either seconds or an HTTP-date
		if seconds, err := strconv.Atoi(retryAfter); err == nil {
			waitDuration = time.Duration(seconds) * time.Second
			utils.Debug("RateLimiter [%s]: got 429 with Retry-After=%d seconds (hit #%d)", rl.Host, seconds, hits)
		} else if t, err := http.ParseTime(retryAfter); err == nil {
			// Try parsing as HTTP-date
			waitDuration = time.Until(t)
			if waitDuration < 0 {
				waitDuration = time.Second // Minimum wait
			}
			utils.Debug("RateLimiter [%s]: got 429 with Retry-After date (wait %v, hit #%d)", rl.Host, waitDuration, hits)
		}
	}

	// No Retry-After header or failed to parse - use exponential backoff
	if waitDuration == 0 {
		// Base: 1s, then 2s, 4s, 8s, 16s, 32s, capped at 60s
		baseDelay := time.Second
		backoffMultiplier := int64(1) << min(int(hits-1), 5) // Cap at 2^5 = 32
		waitDuration = time.Duration(backoffMultiplier) * baseDelay

		maxWait := 60 * time.Second
		if waitDuration > maxWait {
			waitDuration = maxWait
		}

		utils.Debug("RateLimiter [%s]: got 429 without Retry-After, backing off %v (hit #%d)", rl.Host, waitDuration, hits)
	}

	// Add jitter (±10%) to prevent thundering herd
	waitDuration = addJitter(waitDuration, 0.10)

	rl.setBlockedUntil(waitDuration)
	return waitDuration
}

// addJitter adds random variation to a duration
// jitterFactor of 0.10 means ±10%
func addJitter(d time.Duration, jitterFactor float64) time.Duration {
	if d <= 0 {
		return d
	}
	// Generate a random value between -jitterFactor and +jitterFactor
	jitter := (rand.Float64()*2 - 1) * jitterFactor
	return time.Duration(float64(d) * (1 + jitter))
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

// WaitIfBlocked checks if we're currently rate limited and waits if so.
// Returns true if we waited, false if we were not blocked.
func (rl *RateLimiter) WaitIfBlocked() bool {
	blockedUntil := rl.blockedUntil.Load()
	if blockedUntil == 0 {
		return false
	}

	waitDuration := time.Until(time.Unix(0, blockedUntil))
	if waitDuration <= 0 {
		return false
	}

	utils.Debug("RateLimiter [%s]: worker waiting %v due to rate limit", rl.Host, waitDuration)
	time.Sleep(waitDuration)
	return true
}

// ReportSuccess resets the consecutive hit counter on successful request
func (rl *RateLimiter) ReportSuccess() {
	if rl.consecutiveHits.Load() > 0 {
		rl.consecutiveHits.Store(0)
		utils.Debug("RateLimiter [%s]: success reported, reset consecutive hits", rl.Host)
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

// BlockedUntil returns the time when rate limiting expires.
// Returns zero time if not blocked.
func (rl *RateLimiter) BlockedUntil() time.Time {
	blockedUntil := rl.blockedUntil.Load()
	if blockedUntil == 0 {
		return time.Time{}
	}
	return time.Unix(0, blockedUntil)
}

// BlockDuration returns how long until the rate limit expires.
// Returns 0 if not currently blocked.
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
