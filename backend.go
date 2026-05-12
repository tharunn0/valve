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
	// - n: The number of tokens the current request consumes.

	//
	// Returns:
	// - Result: Contains Allow (bool), Remaining (int64), and RetryAfter (time.Duration).
	// - error: Any error that occurred during the check.
	AllowN(ctx context.Context, key string, n int64) (Result, error)
}

// Result represents the result of a rate limit check.
type Result struct {
	Allow      bool
	Remaining  int64
	RetryAfter time.Duration
}
