package valve

import (
	"context"
	"sync"
	"time"
)

// memorySlidingWindowCounter is an in-memory implementation of the Backend interface
// using the sliding window counter algorithm.
type memorySlidingWindowCounter struct {
	mu sync.Mutex

	windows     map[string]*window
	stalePeriod time.Duration
}

// window holds the state for a single rate-limited key across two adjacent windows.
type window struct {
	prevCount int64
	currCount int64
	currStart time.Time
}

// NewMemorySlidingWindowCounter creates a new instance of memorySlidingWindowCounter
// and starts a janitor goroutine for cleaning up stale windows.
func NewMemorySlidingWindowCounter() Backend {
	m := &memorySlidingWindowCounter{
		windows:     make(map[string]*window),
		stalePeriod: defaultStalePeriod,
	}

	go m.janitor()

	return m
}

// AllowN checks if a request is permitted for the given key using a sliding window counter algorithm.
func (m *memorySlidingWindowCounter) AllowN(ctx context.Context, key string, n int64) (Result, error) {
	// TODO: implement sliding window counter logic
	return Result{}, nil
}

// janitor runs in a background goroutine to periodically clean up stale windows.
func (m *memorySlidingWindowCounter) janitor() {
	// TODO: implement stale window cleanup
}
