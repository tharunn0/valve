package valve

import (
	"context"
	"sync"
	"time"
)

const defaultStalePeriod = 1 * time.Hour

// bucket holds the state for a single rate-limited key.
type bucket struct {
	tokens     int64
	lastRefill time.Time
}

// memoryTokenBucket is an in-memory implementation of the Backend interface.
type memoryTokenBucket struct {
	mu             sync.Mutex
	rate           int64
	maxToken       int64
	refillInterval time.Duration

	tokenInterval time.Duration

	buckets     map[string]*bucket
	stalePeriod time.Duration
}

// NewMemoryTokenBucket creates a new instance of memoryTokenBucket and starts a janitor goroutine.
func NewMemoryTokenBucket(rate int64, maxTokens int64, refillInterval time.Duration) Backend {

	m := &memoryTokenBucket{
		rate:           rate,
		maxToken:       maxTokens,
		refillInterval: refillInterval,
		buckets:        make(map[string]*bucket),
		stalePeriod:    defaultStalePeriod,
	}

	m.tokenInterval = refillInterval / time.Duration(rate)

	// Start the background goroutine to clean up unused buckets
	go m.janitor()

	return m
}

// Allow checks if a request is permitted for the given key using a token bucket algorithm.
func (m *memoryTokenBucket) AllowN(ctx context.Context, key string, n int64) (Result, error) {
	// Lock the store to ensure thread-safety for map access and bucket updates.
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()

	// Retrieve the bucket for the given key; create and store a new one if it doesn't exist.
	b, ok := m.buckets[key]
	if !ok {
		b = &bucket{
			tokens:     m.maxToken,
			lastRefill: now,
		}
		m.buckets[key] = b
	}

	// Calculate tokens to add based on the time elapsed since the last refill.
	elapsed := now.Sub(b.lastRefill)
	tokensToAdd := int64(elapsed / m.tokenInterval)

	if tokensToAdd > 0 {
		b.tokens += tokensToAdd

		// Cap the tokens at maxToken.
		if b.tokens > m.maxToken {
			b.tokens = m.maxToken
		}

		// Update the lastRefill time by adding the exact duration of added tokens.
		// This prevents "drift" and ensures precise refill timing.
		b.lastRefill = b.lastRefill.Add(time.Duration(tokensToAdd) * m.tokenInterval)
	}

	var res Result

	// Check if the bucket has enough tokens for the requested cost.
	if b.tokens >= n {
		b.tokens -= n

		res.Allow = true
		res.Remaining = b.tokens
		res.RetryAfter = 0
		return res, nil
	}

	needed := n - b.tokens
	retryAfter := time.Duration(needed) * m.tokenInterval

	res.Allow = false
	res.Remaining = b.tokens
	res.RetryAfter = retryAfter

	return res, nil
}

// janitor runs in a background goroutine to periodically clean up stale buckets.
func (m *memoryTokenBucket) janitor() {
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
