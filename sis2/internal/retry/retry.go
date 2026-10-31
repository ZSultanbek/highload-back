// Package retry provides a small, protocol-agnostic retry policy with
// exponential backoff + jitter and a per-attempt timeout. It is used by both
// the gRPC and REST clients in the benchmark so the reliability behaviour is
// identical across protocols.
package retry

import (
	"context"
	"math/rand"
	"time"
)

// Policy configures retries.
type Policy struct {
	MaxAttempts int           // total attempts, including the first
	PerAttempt  time.Duration // timeout applied to each attempt
	BaseBackoff time.Duration // backoff before the 2nd attempt
	MaxBackoff  time.Duration
}

// Default is a sane policy for idempotent calls.
func Default() Policy {
	return Policy{
		MaxAttempts: 3,
		PerAttempt:  100 * time.Millisecond,
		BaseBackoff: 10 * time.Millisecond,
		MaxBackoff:  200 * time.Millisecond,
	}
}

// Do runs fn with the policy. fn receives a per-attempt context carrying the
// deadline. retryable decides whether a given error is worth another attempt
// (e.g. gRPC UNAVAILABLE / HTTP 503 / DeadlineExceeded). Only call Do for
// idempotent operations (GET always; CREATE is safe thanks to the idempotency
// key, so retries never duplicate).
func Do(ctx context.Context, p Policy, retryable func(error) bool, fn func(ctx context.Context) error) error {
	var lastErr error
	backoff := p.BaseBackoff
	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, p.PerAttempt)
		err := fn(attemptCtx)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt == p.MaxAttempts || !retryable(err) {
			break
		}
		// Full jitter: sleep in [0, backoff].
		sleep := time.Duration(rand.Int63n(int64(backoff) + 1))
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff *= 2
		if backoff > p.MaxBackoff {
			backoff = p.MaxBackoff
		}
	}
	return lastErr
}
