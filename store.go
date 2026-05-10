package valve

import (
	"context"
	"time"
)

// Store defines the interface for a rate limiter's storage backend.
type Store interface {
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
	// - allow: True if the request is permitted, false otherwise.
	// - remaining: The number of tokens remaining in the bucket.
	// - retryAfter: The time duration to wait before a new request is allowed.
	// - err: Any error that occurred during the check.
	Allow(ctx context.Context, key string, cost, maxToken int64, refillInterval time.Duration) (allow bool, remaining int64, retryAfter time.Duration, err error)
}
