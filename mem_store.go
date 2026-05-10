package valve

import (
	"context"
	"sync"
	"time"
)

// bucket holds the state for a single rate-limited key.
type bucket struct {
	tokens     int64
	lastRefill time.Time
}

// memoryStore is an in-memory implementation of the Store interface.
type memoryStore struct {
	mu          sync.Mutex
	buckets     map[string]*bucket
	stalePeriod time.Duration
}

// NewMemoryStore creates a new instance of memoryStore and starts a janitor goroutine.
func NewMemoryStore(stalePeriod time.Duration) Store {
	m := &memoryStore{
		buckets:     make(map[string]*bucket),
		stalePeriod: stalePeriod,
	}

	// Start the background goroutine to clean up unused buckets
	go m.janitor()

	return m
}

// Allow checks if a request is permitted for the given key using a token bucket algorithm.
func (m *memoryStore) Allow(ctx context.Context, key string, cost, maxToken int64, refillInterval time.Duration) (allow bool, remaining int64, retryAfter time.Duration, err error) {
	// Lock the store to ensure thread-safety for map access and bucket updates.
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()

	// Retrieve the bucket for the given key; create and store a new one if it doesn't exist.
	b, ok := m.buckets[key]
	if !ok {
		b = &bucket{
			tokens:     maxToken,
			lastRefill: now,
		}
		m.buckets[key] = b
	}

	// Calculate tokens to add based on the time elapsed since the last refill.
	elapsed := now.Sub(b.lastRefill)
	tokensToAdd := int64(elapsed / refillInterval)

	if tokensToAdd > 0 {
		b.tokens += tokensToAdd

		// Cap the tokens at maxToken.
		if b.tokens > maxToken {
			b.tokens = maxToken
		}

		// Update the lastRefill time by adding the exact duration of added tokens.
		// This prevents "drift" and ensures precise refill timing.
		b.lastRefill = b.lastRefill.Add(time.Duration(tokensToAdd) * refillInterval)
	}

	// Check if the bucket has enough tokens for the requested cost.
	if b.tokens >= cost {
		b.tokens -= cost
		return true, b.tokens, 0, nil
	}

	// If not enough tokens, calculate the duration until enough tokens are refilled.
	needed := cost - b.tokens
	retryAfter = time.Duration(needed) * refillInterval

	return false, b.tokens, retryAfter, nil
}

// janitor runs in a background goroutine to periodically clean up stale buckets.
func (m *memoryStore) janitor() {
	// The janitor runs every (half the stalePeriod)
	ticker := time.NewTicker(m.stalePeriod / 2)

	for range ticker.C {
		m.mu.Lock()
		now := time.Now()
		for key, b := range m.buckets {
			if now.Sub(b.lastRefill) >= m.stalePeriod {
				delete(m.buckets, key)
			}
		}
		m.mu.Unlock()
	}
}