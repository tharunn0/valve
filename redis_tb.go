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

// redisTokenBucket implements the Backend interface using Redis as the backend.
// It utilizes Lua scripts to guarantee atomicity of rate limit evaluations.
type redisTokenBucket struct {
	client         *redis.Client
	rate           int64
	maxTokens      int64
	refillInterval time.Duration
	script         *redis.Script
}

// NewRedisTokenBucket creates a new redisTokenBucket with the given Redis client.
// It initializes the Lua script that will be used for atomic rate limit operations.
func NewRedisTokenBucket(client *redis.Client, rate int64, maxTokens int64, refillInterval time.Duration) Backend {
	return &redisTokenBucket{
		client:         client,
		rate:           rate,
		maxTokens:      maxTokens,
		refillInterval: refillInterval,
		script:         redis.NewScript(rateLimitScript),
	}
}

// AllowN checks if a request is allowed based on the rate limit.
// It executes the rate limit Lua script on the Redis server to atomically deduct the cost
// and return the current state of the bucket.
func (r *redisTokenBucket) AllowN(ctx context.Context, key string, n int64) (Result, error) {
	keys := []string{key}

	args := []any{
		n,
		r.maxTokens,
		r.refillInterval.Milliseconds(),
	}

	res := Result{}

	// Execute the pre-loaded Lua script.
	result, err := r.script.Run(ctx, r.client, keys, args...).Result()
	if err != nil {
		res.Allow = false
		return res, fmt.Errorf("failed to execute rate limit script: %w", err)
	}

	// Parse the results from the Lua script and extract the values.
	resList, ok := result.([]any)
	if !ok || len(resList) < 3 {
		res.Allow = false
		return res, fmt.Errorf("unexpected lua script result format")
	}

	if allowInt, ok := resList[0].(int64); ok && allowInt == 1 {
		res.Allow = true
	}

	if rem, ok := resList[1].(int64); ok {
		res.Remaining = rem
	}

	if retryMs, ok := resList[2].(int64); ok {
		res.RetryAfter = time.Duration(retryMs) * time.Millisecond
	}

	return res, nil
}
