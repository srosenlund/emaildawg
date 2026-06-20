package graph

import (
	"context"
	"testing"
	"time"
)

// newTestProvider creates a TokenProvider with an injected fetchFn for testing.
// This avoids any real network calls.
func newTestProvider(tenantID, clientID, clientSecret string, fn fetchFunc) *TokenProvider {
	p := NewTokenProvider(tenantID, clientID, clientSecret)
	p.fetchFn = fn
	return p
}

// stubFetch returns a fixed token and TTL. Call count is tracked via a pointer.
func stubFetch(token string, expiresIn int, calls *int) fetchFunc {
	return func(ctx context.Context, tenantID, clientID, clientSecret, scope string) (string, int, error) {
		*calls++
		return token, expiresIn, nil
	}
}

// TestTokenCache_FreshTokenReturnedWithoutRefetch verifies that a cached token
// that is still valid (well within expiry, beyond the 60s early-refresh window)
// is returned directly without making an HTTP call.
func TestTokenCache_FreshTokenReturnedWithoutRefetch(t *testing.T) {
	calls := 0
	p := newTestProvider("tenant", "client", "secret", stubFetch("tok-A", 3600, &calls))

	// Pre-populate the cache with a token that expires far in the future.
	p.mu.Lock()
	p.token = "cached-token"
	p.expiry = time.Now().Add(10 * time.Minute) // far future — should NOT refetch
	p.mu.Unlock()

	tok, err := p.Token(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "cached-token" {
		t.Errorf("expected cached-token, got %q", tok)
	}
	if calls != 0 {
		t.Errorf("expected 0 HTTP calls for fresh token, got %d", calls)
	}
}

// TestTokenCache_ExpiredTokenTriggersRefetch verifies that when the cached token
// is within the 60s early-refresh window (or already expired), Token() calls the
// fetch function exactly once and returns the new token.
func TestTokenCache_ExpiredTokenTriggersRefetch(t *testing.T) {
	calls := 0
	p := newTestProvider("tenant", "client", "secret", stubFetch("new-token", 3600, &calls))

	// Pre-populate cache with a token that is within the 60s refresh window.
	p.mu.Lock()
	p.token = "old-token"
	p.expiry = time.Now().Add(30 * time.Second) // within 60s window → should refetch
	p.mu.Unlock()

	tok, err := p.Token(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "new-token" {
		t.Errorf("expected new-token, got %q", tok)
	}
	if calls != 1 {
		t.Errorf("expected 1 HTTP call for near-expired token, got %d", calls)
	}
}

// TestTokenCache_AlreadyExpiredTokenTriggersRefetch verifies that a fully expired
// cached token (past expiry) also triggers a refetch.
func TestTokenCache_AlreadyExpiredTokenTriggersRefetch(t *testing.T) {
	calls := 0
	p := newTestProvider("tenant", "client", "secret", stubFetch("fresh-token", 7200, &calls))

	// Token expired in the past.
	p.mu.Lock()
	p.token = "stale-token"
	p.expiry = time.Now().Add(-5 * time.Minute)
	p.mu.Unlock()

	tok, err := p.Token(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "fresh-token" {
		t.Errorf("expected fresh-token, got %q", tok)
	}
	if calls != 1 {
		t.Errorf("expected 1 HTTP call for expired token, got %d", calls)
	}
}

// TestTokenCache_EmptyCacheTriggersInitialFetch verifies that the very first call
// (empty cache) fetches from the endpoint.
func TestTokenCache_EmptyCacheTriggersInitialFetch(t *testing.T) {
	calls := 0
	p := newTestProvider("tenant", "client", "secret", stubFetch("initial-token", 3600, &calls))

	tok, err := p.Token(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "initial-token" {
		t.Errorf("expected initial-token, got %q", tok)
	}
	if calls != 1 {
		t.Errorf("expected 1 HTTP call for empty cache, got %d", calls)
	}
}

// TestGraphScope verifies that TokenProvider uses the Graph scope, not the IMAP scope.
func TestGraphScope(t *testing.T) {
	var capturedScope string
	fetchFn := func(ctx context.Context, tenantID, clientID, clientSecret, scope string) (string, int, error) {
		capturedScope = scope
		return "token", 3600, nil
	}
	p := newTestProvider("tenant", "client", "secret", fetchFn)

	if _, err := p.Token(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	const wantScope = "https://graph.microsoft.com/.default"
	if capturedScope != wantScope {
		t.Errorf("scope = %q, want %q", capturedScope, wantScope)
	}
}
