package connector

import (
	"testing"
	"time"
)

func TestSuppressCache_Suppress_then_IsSuppressed(t *testing.T) {
	sc := newSuppressCache()
	sc.Suppress("msg-1")
	if !sc.IsSuppressed("msg-1") {
		t.Fatal("expected IsSuppressed to return true immediately after Suppress")
	}
}

func TestSuppressCache_NeverSuppressed_isFalse(t *testing.T) {
	sc := newSuppressCache()
	if sc.IsSuppressed("unknown-id") {
		t.Fatal("expected IsSuppressed to return false for a never-suppressed id")
	}
}

func TestSuppressCache_ExpiredEntry_isFalse(t *testing.T) {
	sc := newSuppressCache()
	// Inject a timestamp old enough to be past the TTL.
	old := time.Now().Add(-(suppressTTL + time.Second))
	sc.suppressAt("msg-old", old)
	if sc.IsSuppressed("msg-old") {
		t.Fatal("expected IsSuppressed to return false for an expired entry")
	}
}

func TestSuppressCache_Forget_isFalse(t *testing.T) {
	sc := newSuppressCache()
	sc.Suppress("msg-2")
	sc.Forget("msg-2")
	if sc.IsSuppressed("msg-2") {
		t.Fatal("expected IsSuppressed to return false after Forget")
	}
}
