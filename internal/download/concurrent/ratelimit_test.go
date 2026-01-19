package concurrent

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiter_Handle429_WithRetryAfterSeconds(t *testing.T) {
	rl := NewRateLimiter()

	// Create a response with Retry-After in seconds
	resp := &http.Response{
		Header: http.Header{"Retry-After": []string{"5"}},
	}

	waitDuration := rl.Handle429(resp)

	if waitDuration != 5*time.Second {
		t.Errorf("expected 5s wait, got %v", waitDuration)
	}

	// Verify blocked state
	if !rl.IsBlocked() {
		t.Error("expected to be blocked after 429")
	}

	blockDuration := rl.BlockDuration()
	if blockDuration < 4*time.Second || blockDuration > 6*time.Second {
		t.Errorf("expected ~5s block duration, got %v", blockDuration)
	}
}

func TestRateLimiter_Handle429_WithRetryAfterDate(t *testing.T) {
	rl := NewRateLimiter()

	// Create a response with Retry-After as HTTP-date (3 seconds from now)
	// Note: http.TimeFormat expects UTC time
	futureTime := time.Now().UTC().Add(3 * time.Second)
	resp := &http.Response{
		Header: http.Header{"Retry-After": []string{futureTime.Format(http.TimeFormat)}},
	}

	waitDuration := rl.Handle429(resp)

	// Should be approximately 3 seconds
	if waitDuration < 2*time.Second || waitDuration > 4*time.Second {
		t.Errorf("expected ~3s wait, got %v", waitDuration)
	}
}

func TestRateLimiter_Handle429_WithoutRetryAfter_ExponentialBackoff(t *testing.T) {
	rl := NewRateLimiter()

	// Response without Retry-After header
	resp := &http.Response{
		Header: http.Header{},
	}

	// First 429: should be 1s
	wait1 := rl.Handle429(resp)
	if wait1 != 1*time.Second {
		t.Errorf("first 429: expected 1s, got %v", wait1)
	}

	// Simulate quick succession - second 429: should be 2s
	wait2 := rl.Handle429(resp)
	if wait2 != 2*time.Second {
		t.Errorf("second 429: expected 2s, got %v", wait2)
	}

	// Third 429: should be 4s
	wait3 := rl.Handle429(resp)
	if wait3 != 4*time.Second {
		t.Errorf("third 429: expected 4s, got %v", wait3)
	}
}

func TestRateLimiter_ReportSuccess_ResetsCounter(t *testing.T) {
	rl := NewRateLimiter()

	// Trigger a few 429s to build up counter
	resp := &http.Response{Header: http.Header{}}
	rl.Handle429(resp)
	rl.Handle429(resp)

	// Report success
	rl.ReportSuccess()

	// Next 429 should start fresh at 1s
	wait := rl.Handle429(resp)
	if wait != 1*time.Second {
		t.Errorf("after success: expected 1s, got %v", wait)
	}
}

func TestRateLimiter_WaitIfBlocked_NotBlocked(t *testing.T) {
	rl := NewRateLimiter()

	start := time.Now()
	waited := rl.WaitIfBlocked()
	elapsed := time.Since(start)

	if waited {
		t.Error("expected not to wait when not blocked")
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("should return immediately, took %v", elapsed)
	}
}

func TestRateLimiter_WaitIfBlocked_Blocked(t *testing.T) {
	rl := NewRateLimiter()

	// Set a short block
	resp := &http.Response{
		Header: http.Header{"Retry-After": []string{"1"}},
	}
	rl.Handle429(resp)

	start := time.Now()
	waited := rl.WaitIfBlocked()
	elapsed := time.Since(start)

	if !waited {
		t.Error("expected to wait when blocked")
	}
	if elapsed < 900*time.Millisecond {
		t.Errorf("should wait ~1s, waited only %v", elapsed)
	}
}

func TestRateLimiter_ExponentialBackoff_CappedAt60s(t *testing.T) {
	rl := NewRateLimiter()
	resp := &http.Response{Header: http.Header{}}

	// Hit many times to reach cap
	var lastWait time.Duration
	for i := 0; i < 10; i++ {
		lastWait = rl.Handle429(resp)
	}

	// Should cap at 60s (2^5 = 32s, then it caps)
	// After 6 hits: 1, 2, 4, 8, 16, 32, then capped at 32s for remaining
	// Actually the cap is at 60s
	if lastWait > 60*time.Second {
		t.Errorf("backoff should cap at 60s, got %v", lastWait)
	}
}

func TestRateLimitError_Error(t *testing.T) {
	err := &RateLimitError{WaitDuration: 5 * time.Second}
	expected := "rate limited (429), retry after 5s"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestRateLimiter_Integration(t *testing.T) {
	// Create a test server that returns 429 with Retry-After
	retryCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		retryCount++
		if retryCount <= 2 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))
	defer server.Close()

	rl := NewRateLimiter()
	client := server.Client()

	// Make request
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		waitDuration := rl.Handle429(resp)
		if waitDuration != 1*time.Second {
			t.Errorf("expected 1s wait, got %v", waitDuration)
		}
	}
	resp.Body.Close()
}
