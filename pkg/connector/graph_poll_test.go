package connector

import (
	"context"
	"testing"
	"time"
)

func TestPollUntil_TrueImmediately(t *testing.T) {
	calls := 0
	ok := pollUntil(context.Background(), time.Second, 10*time.Millisecond, func() bool {
		calls++
		return true
	})
	if !ok {
		t.Fatal("expected true when predicate is satisfied immediately")
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 call, got %d", calls)
	}
}

func TestPollUntil_TrueAfterRetries(t *testing.T) {
	calls := 0
	ok := pollUntil(context.Background(), time.Second, 5*time.Millisecond, func() bool {
		calls++
		return calls >= 3
	})
	if !ok {
		t.Fatal("expected true once predicate becomes satisfied")
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestPollUntil_TimeoutReturnsFalse(t *testing.T) {
	start := time.Now()
	ok := pollUntil(context.Background(), 40*time.Millisecond, 10*time.Millisecond, func() bool {
		return false
	})
	if ok {
		t.Fatal("expected false when predicate never satisfied")
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("returned before timeout elapsed: %v", elapsed)
	}
}

func TestPollUntil_ContextCancelReturnsFalse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	ok := pollUntil(ctx, time.Second, 10*time.Millisecond, func() bool {
		return false
	})
	if ok {
		t.Fatal("expected false when context is cancelled")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("did not return promptly on context cancel: %v", elapsed)
	}
}
