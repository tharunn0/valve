package valve

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// This code uses Token Bucket Algorithm with Redis
// KEYS[1] = bucket key
// ARGV[1] = cost
// ARGV[2] = capacity (max tokens)
// ARGV[3] = refill interval in milliseconds for full refill
const rateLimitScript = `
-- KEYS[1] = bucket key

-- ARGV[1] = cost
-- ARGV[2] = capacity (max tokens)
-- ARGV[3] = refill interval in milliseconds for full refill

local key = KEYS[1]

local cost = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])
local refill_interval = tonumber(ARGV[3])

-- Current Redis server time (milliseconds)
local now_data = redis.call("TIME")
local now_ms = now_data[1] * 1000 + math.floor(now_data[2] / 1000)

-- Token refill rate per millisecond
local refill_rate = capacity / refill_interval

-- Load bucket state
local bucket = redis.call("HMGET", key, "tokens", "timestamp")

local tokens = tonumber(bucket[1])
local last_refill = tonumber(bucket[2])

-- Initialize bucket if it doesn't exist
if tokens == nil then
	tokens = capacity
	last_refill = now_ms
end

-- Refill tokens based on elapsed time
local elapsed = math.max(0, now_ms - last_refill)
local refill = elapsed * refill_rate

tokens = math.min(capacity, tokens + refill)

local allowed = 0
local retry_after = 0

-- Check if enough tokens exist
if tokens >= cost then
	allowed = 1
	tokens = tokens - cost
else
	allowed = 0

	-- Calculate retry time
	local needed = cost - tokens
	retry_after = math.ceil(needed / refill_rate)
end

-- Persist updated state
redis.call("HMSET", key,
	"tokens", tokens,
	"timestamp", now_ms
)

-- Set TTL to auto-clean inactive buckets
local ttl = math.ceil(refill_interval * 2)
redis.call("PEXPIRE", key, ttl)

-- Return:
-- {allowed, remaining_tokens, retry_after_ms}
return {
	allowed,
	math.floor(tokens),
	retry_after
}
`

// redisStore implements the Store interface using Redis as the backend.
// It utilizes Lua scripts to guarantee atomicity of rate limit evaluations.
type redisStore struct {
	client *redis.Client
	script *redis.Script
}

// NewRedisStore creates a new redisStore with the given Redis client.
// It initializes the Lua script that will be used for atomic rate limit operations.
func NewRedisStore(client *redis.Client) Store {
	return &redisStore{
		client: client,
		script: redis.NewScript(rateLimitScript),
	}
}

// Allow checks if a request is allowed based on the rate limit.
// It executes the rate limit Lua script on the Redis server to atomically deduct the cost
// and return the current state of the bucket.
func (r *redisStore) Allow(ctx context.Context, key string, cost, maxToken int64, refillInterval time.Duration) (allow bool, remaining int64, retryAfter time.Duration, err error) {
	keys := []string{key}

	args := []any{
		cost,
		maxToken,
		refillInterval.Milliseconds(),
	}

	// Execute the pre-loaded Lua script.
	result, err := r.script.Run(ctx, r.client, keys, args...).Result()
	if err != nil {
		return false, 0, 0, fmt.Errorf("failed to execute rate limit script: %w", err)
	}

	// Parse the results from the Lua script and extract the values.
	res, ok := result.([]any)
	if !ok || len(res) < 3 {
		return false, 0, 0, fmt.Errorf("unexpected lua script result format")
	}

	if allowInt, ok := res[0].(int64); ok && allowInt == 1 {
		allow = true
	}

	if rem, ok := res[1].(int64); ok {
		remaining = rem
	}

	if retryMs, ok := res[2].(int64); ok {
		retryAfter = time.Duration(retryMs) * time.Millisecond
	}

	return allow, remaining, retryAfter, nil
}
