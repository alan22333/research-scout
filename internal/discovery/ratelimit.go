package discovery

import (
	"context"
	"sync"
	"time"
)

// rateLimiter enforces a minimum spacing between outbound provider requests.
// A requests-per-second value <= 0 disables throttling.
type rateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	last     time.Time
}

func newRateLimiter(requestsPerSecond int) *rateLimiter {
	if requestsPerSecond <= 0 {
		return &rateLimiter{}
	}
	return &rateLimiter{interval: time.Second / time.Duration(requestsPerSecond)}
}

func (r *rateLimiter) Wait(ctx context.Context) error {
	if r == nil || r.interval <= 0 {
		return nil
	}
	r.mu.Lock()
	now := time.Now()
	wait := time.Duration(0)
	if !r.last.IsZero() {
		if elapsed := now.Sub(r.last); elapsed < r.interval {
			wait = r.interval - elapsed
		}
	}
	r.last = now.Add(wait)
	r.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
