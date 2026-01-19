package limiter

import (
	"sync"
)

// globalManager is the singleton instance
var globalManager = &GlobalLimitManager{
	limiters: make(map[string]*RateLimiter),
}

// GlobalLimitManager coordinates rate limiters across all downloads.
// It maintains a Host -> RateLimiter mapping so that all downloads
// from the same domain share rate limiting state.
type GlobalLimitManager struct {
	mu       sync.RWMutex
	limiters map[string]*RateLimiter
}

// GetLimiter returns the rate limiter for a given host.
// If one doesn't exist, it creates and registers a new one.
// This is the primary API - use this instead of creating RateLimiters directly.
func GetLimiter(host string) *RateLimiter {
	return globalManager.GetLimiter(host)
}

// GetLimiter returns the rate limiter for a given host.
func (g *GlobalLimitManager) GetLimiter(host string) *RateLimiter {
	// Fast path: check if limiter already exists
	g.mu.RLock()
	if limiter, ok := g.limiters[host]; ok {
		g.mu.RUnlock()
		return limiter
	}
	g.mu.RUnlock()

	// Slow path: create new limiter
	g.mu.Lock()
	defer g.mu.Unlock()

	// Double-check after acquiring write lock
	if limiter, ok := g.limiters[host]; ok {
		return limiter
	}

	limiter := NewRateLimiter(host)
	g.limiters[host] = limiter
	return limiter
}

// Reset clears all rate limiters. Intended for testing.
func Reset() {
	globalManager.mu.Lock()
	defer globalManager.mu.Unlock()
	globalManager.limiters = make(map[string]*RateLimiter)
}

// ActiveHosts returns the number of hosts currently being tracked.
// Intended for diagnostics/testing.
func ActiveHosts() int {
	globalManager.mu.RLock()
	defer globalManager.mu.RUnlock()
	return len(globalManager.limiters)
}
