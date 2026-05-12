package valve

import (
	"context"
	"time"
)

// Backend defines the interface for a rate limiter's storage backend.
type Backend interface {
	// Allow checks if a request is allowed based on the rate limit.
	// It deducts the specified cost from the bucket associated with the key.
	//
	// Parameters:
	// - ctx: The context for the request.
	// - key: A unique identifier for the rate limit bucket.
	// - cost: The number of tokens the current request consumes.
	// - maxToken: The maximum number of tokens the bucket can hold.
	// - refillInterval: The time duration required to refill tokens.
	//
	// Returns:
	// - *Result: Contains Allow (bool), Remaining (int64), and RetryAfter (time.Duration).
	// - error: Any error that occurred during the check.
	Allow(ctx context.Context, key string, cost, maxToken int64, refillInterval time.Duration) (*Result, error)
}

// Result represents the result of a rate limit check.
type Result struct {
	Allow      bool
	Remaining  int64
	RetryAfter time.Duration
}
