// Package valve provides a flexible rate-limiting library with support for multiple storage backends.
package valve

import (
	"context"
)

// Limiter represents a rate limiter that uses a token bucket algorithm.
// It delegates the actual storage and token logic to a Backend implementation.
type Limiter struct {
	// Backend is the storage backend for the rate limiter.
	Backend Backend
}

// Option defines a functional option for configuring a Limiter.
type Option func(*Limiter)

// WithBackend sets the storage backend for the Limiter.
func WithBackend(backend Backend) Option {
	return func(l *Limiter) {
		l.Backend = backend
	}
}

// NewLimiter creates a new Limiter with the provided options.
// It returns an error if any of the configuration parameters are invalid.
func NewLimiter(opts ...Option) (*Limiter, error) {
	limiter := &Limiter{}

	for _, opt := range opts {
		opt(limiter)
	}

	if limiter.Backend == nil {
		return nil, ErrBackendRequired
	}

	return limiter, nil
}

// Allow is a shorthand for AllowN(ctx, key, 1).
func (l *Limiter) Allow(ctx context.Context, key string) (Result, error) {
	return l.AllowN(ctx, key, 1)
}

// AllowN checks if a request with the given value of n is allowed for the specified key.
// It returns whether the request is allowed, the remaining tokens, the duration to wait
// before retrying, and any error encountered.
func (l *Limiter) AllowN(ctx context.Context, key string, n int64) (Result, error) {
	return l.Backend.AllowN(ctx, key, n)
}
