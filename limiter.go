// Package valve provides a flexible rate-limiting library with support for multiple storage backends.
package valve

import (
	"context"
	"time"
)

// Limiter represents a rate limiter that uses a token bucket algorithm.
// It delegates the actual storage and token logic to a Store implementation.
type Limiter struct {
	// Rate is the number of tokens refilled per RefillInterval.
	Rate int64

	// MaxTokens is the maximum capacity of the bucket.
	MaxTokens int64

	// RefillInterval is the duration over which Rate tokens are refilled.
	RefillInterval time.Duration

	// Store is the storage backend for the rate limiter.
	Store Store
}

// Option defines a functional option for configuring a Limiter.
type Option func(*Limiter)

// WithRate sets the number of tokens refilled per RefillInterval.
func WithRate(rate int64) Option {
	return func(l *Limiter) {
		l.Rate = rate
	}
}

// WithMaxTokens sets the maximum capacity of the bucket.
func WithMaxTokens(max int64) Option {
	return func(l *Limiter) {
		l.MaxTokens = max
	}
}

// WithRefillInterval sets the duration over which tokens are refilled.
func WithRefillInterval(interval time.Duration) Option {
	return func(l *Limiter) {
		l.RefillInterval = interval
	}
}

// WithStore sets the storage backend for the Limiter.
func WithStore(store Store) Option {
	return func(l *Limiter) {
		l.Store = store
	}
}

// NewLimiter creates a new Limiter with the provided options.
// It returns an error if any of the configuration parameters are invalid.
func NewLimiter(opts ...Option) (*Limiter, error) {
	limiter := &Limiter{
		Rate:           1,
		MaxTokens:      20,
		RefillInterval: time.Second,
	}

	for _, opt := range opts {
		opt(limiter)
	}

	if limiter.Store == nil {
		return nil, ErrStoreRequired
	}
	if limiter.Rate <= 0 {
		return nil, ErrInvalidRate
	}
	if limiter.MaxTokens <= 0 {
		return nil, ErrInvalidMaxTokens
	}
	if limiter.RefillInterval <= 0 {
		return nil, ErrInvalidRefillInterval
	}

	// Ensure the token refill interval is at least 1 nanosecond.
	if limiter.RefillInterval/time.Duration(limiter.Rate) <= 0 {
		return nil, ErrRateTooHigh
	}

	return limiter, nil
}

// Allow is a shorthand for AllowContext(context.Background(), key, 1).
func (l *Limiter) Allow(key string) (bool, int64, time.Duration, error) {
	return l.AllowContext(context.Background(), key, 1)
}

// AllowContext checks if a request with the given cost is allowed for the specified key.
// It returns whether the request is allowed, the remaining tokens, the duration to wait
// before retrying, and any error encountered.
func (l *Limiter) AllowContext(ctx context.Context, key string, cost int64) (bool, int64, time.Duration, error) {
	// Calculate the interval for a single token refill.
	// If Rate is tokens per RefillInterval, then 1 token is refilled every RefillInterval/Rate.
	tokenInterval := l.RefillInterval / time.Duration(l.Rate)

	return l.Store.Allow(ctx, key, cost, l.MaxTokens, tokenInterval)
}
