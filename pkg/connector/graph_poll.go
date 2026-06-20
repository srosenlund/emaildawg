package connector

import (
	"context"
	"time"
)

// pollUntil calls fn immediately and then every interval until it returns true,
// the timeout elapses, or the context is cancelled. It returns true only if fn
// reported success before the deadline. Used to wait (bounded) for asynchronous
// bridgev2 state — room creation, message persistence — to settle before acting
// on it directly.
func pollUntil(ctx context.Context, timeout, interval time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if fn() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(interval):
		}
	}
}
