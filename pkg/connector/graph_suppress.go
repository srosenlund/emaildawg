package connector

import (
	"sync"
	"time"
)

// suppressTTL is the duration for which a suppressed message ID is considered active.
const suppressTTL = 45 * time.Second

// suppressCache is a concurrency-safe cache that tracks recently suppressed
// message IDs to prevent read-state feedback loops. An entry is considered
// active for suppressTTL after it was recorded. Expired entries are pruned
// lazily on IsSuppressed access.
type suppressCache struct {
	mu      sync.Mutex
	entries map[string]time.Time
}

// newSuppressCache returns an initialised suppressCache.
func newSuppressCache() *suppressCache {
	return &suppressCache{
		entries: make(map[string]time.Time),
	}
}

// Suppress records id as suppressed at the current time.
func (sc *suppressCache) Suppress(id string) {
	sc.suppressAt(id, time.Now())
}

// suppressAt records id as suppressed at the given time t.
// This unexported helper exists so tests can inject arbitrary timestamps to
// exercise TTL-expiry without relying on real time.Sleep.
func (sc *suppressCache) suppressAt(id string, t time.Time) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.entries[id] = t
}

// IsSuppressed returns true if id was suppressed within the last suppressTTL.
// It also prunes all expired entries from the map on each access.
func (sc *suppressCache) IsSuppressed(id string) bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	now := time.Now()
	// Prune all expired entries.
	for k, t := range sc.entries {
		if now.Sub(t) >= suppressTTL {
			delete(sc.entries, k)
		}
	}
	_, ok := sc.entries[id]
	return ok
}

// Forget removes id from the cache unconditionally.
func (sc *suppressCache) Forget(id string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	delete(sc.entries, id)
}
