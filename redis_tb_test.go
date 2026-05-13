package valve

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestRedisTokenBucket spins up a fresh miniredis instance and returns
// the redisTokenBucket alongside the miniredis server for time manipulation.
func newTestRedisTokenBucket(t *testing.T, rate int64, maxTokens int64, refillInterval time.Duration) (*redisTokenBucket, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t) // auto-closed when t finishes

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	backendI := NewRedisTokenBucket(client, rate, maxTokens, refillInterval)

	backend := backendI.(*redisTokenBucket)

	return backend, mr
}

// --- Constructor ---

func TestNewRedisTokenBucket_ReturnsNonNil(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 10, 10, time.Second)

	if backend == nil {
		t.Fatal("expected non-nil RedisTokenBucket")
	}
	if backend.client == nil {
		t.Fatal("expected non-nil Redis client in backend")
	}
	if backend.script == nil {
		t.Fatal("expected non-nil Lua script in backend")
	}
}

// --- Basic Allow Behavior ---

func TestRedisAllow_FirstRequestAllowed(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 10, 10, time.Second)

	res, err := backend.AllowN(context.Background(), "key1", 1)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allow {
		t.Fatal("expected request to be allowed")
	}
	if res.Remaining != 9 {
		t.Fatalf("expected 9 remaining, got %d", res.Remaining)
	}
	if res.RetryAfter != 0 {
		t.Fatalf("expected zero retryAfter, got %v", res.RetryAfter)
	}
}

func TestRedisAllow_ExhaustAllTokens(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 5, 5, time.Second)
	ctx := context.Background()

	// Consume all 5 tokens one by one.
	for i := range 5 {
		res, err := backend.AllowN(ctx, "key1", 1)
		if err != nil {
			t.Fatalf("request %d: unexpected error: %v", i, err)
		}
		if !res.Allow {
			t.Fatalf("request %d: expected allowed", i)
		}
		expectedRemaining := 5 - (i + 1)
		if res.Remaining != int64(expectedRemaining) {
			t.Fatalf("request %d: expected %d remaining, got %d", i, expectedRemaining, res.Remaining)
		}
	}

	// The 6th request should be denied.
	res, err := backend.AllowN(ctx, "key1", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allow {
		t.Fatal("expected request to be denied after exhausting tokens")
	}
	if res.Remaining != 0 {
		t.Fatalf("expected 0 remaining, got %d", res.Remaining)
	}
	if res.RetryAfter <= 0 {
		t.Fatalf("expected positive retryAfter, got %v", res.RetryAfter)
	}
}

func TestRedisAllow_CostGreaterThanOne(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 10, 10, time.Second)
	ctx := context.Background()

	// Request with cost 3 from a bucket of 10.
	res, err := backend.AllowN(ctx, "key1", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allow {
		t.Fatal("expected request to be allowed")
	}
	if res.Remaining != 7 {
		t.Fatalf("expected 7 remaining, got %d", res.Remaining)
	}
}

func TestRedisAllow_CostExceedsAvailableTokens(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 10, 10, time.Second)
	ctx := context.Background()

	// Consume 8 of 10 tokens.
	backend.AllowN(ctx, "key1", 8)

	// Now request cost=5, but only 2 remain.
	res, err := backend.AllowN(ctx, "key1", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allow {
		t.Fatal("expected request to be denied")
	}
	if res.Remaining != 2 {
		t.Fatalf("expected 2 remaining, got %d", res.Remaining)
	}
	if res.RetryAfter <= 0 {
		t.Fatal("expected positive retryAfter when denied")
	}
}

func TestRedisAllow_CostEqualToMaxTokens(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 10, 10, time.Second)
	ctx := context.Background()

	// A single request that costs exactly the full bucket.
	res, err := backend.AllowN(ctx, "key1", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allow {
		t.Fatal("expected request to be allowed")
	}
	if res.Remaining != 0 {
		t.Fatalf("expected 0 remaining, got %d", res.Remaining)
	}

	// Next request should be denied.
	res, _ = backend.AllowN(ctx, "key1", 1)
	if res.Allow {
		t.Fatal("expected request to be denied after full depletion")
	}
}

func TestRedisAllow_CostExceedsMaxTokens(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 10, 10, time.Second)
	ctx := context.Background()

	// A request whose cost is larger than the bucket can ever hold.
	res, err := backend.AllowN(ctx, "key1", 15)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allow {
		t.Fatal("expected request to be denied when cost > maxToken")
	}
	if res.Remaining != 10 {
		t.Fatalf("expected 10 remaining (unchanged), got %d", res.Remaining)
	}
	if res.RetryAfter <= 0 {
		t.Fatal("expected positive retryAfter for impossible cost")
	}
}

// --- Token Refill Behavior ---

func TestRedisAllow_TokenRefillAfterWait(t *testing.T) {
	backend, mr := newTestRedisTokenBucket(t, 5, 5, time.Second)
	ctx := context.Background()

	// Pin the start time so TIME calls in the Lua script are deterministic.
	now := time.Now()
	mr.SetTime(now)

	// Exhaust all tokens.
	backend.AllowN(ctx, "key1", 5)

	// Advance the mocked TIME by 600ms to simulate partial refill.
	// With capacity=5 and refillInterval=1s, rate = 5 tokens/s.
	// After 600ms: 0.6s * 5 = 3 tokens refilled.
	mr.SetTime(now.Add(600 * time.Millisecond))

	res, err := backend.AllowN(ctx, "key1", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allow {
		t.Fatal("expected request to be allowed after token refill")
	}
	if res.Remaining != 1 {
		t.Fatalf("expected 1 remaining (3 refilled - 2 cost), got %d", res.Remaining)
	}
}

func TestRedisAllow_RefillCapsAtMaxTokens(t *testing.T) {
	backend, mr := newTestRedisTokenBucket(t, 5, 5, time.Second)
	ctx := context.Background()

	now := time.Now()
	mr.SetTime(now)

	// Use 2 of 5 tokens.
	backend.AllowN(ctx, "key1", 2)

	// Advance TIME by 5 seconds, far longer than the full refill interval.
	// Tokens should cap at 5, not exceed it.
	mr.SetTime(now.Add(5 * time.Second))

	res, err := backend.AllowN(ctx, "key1", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allow {
		t.Fatal("expected request to be allowed")
	}
	// Should be capped at 5 (max) then minus 1 (cost) = 4.
	if res.Remaining != 4 {
		t.Fatalf("expected 4 remaining (capped at 5, minus 1), got %d", res.Remaining)
	}
}

func TestRedisAllow_PartialRefillNotEnough(t *testing.T) {
	backend, mr := newTestRedisTokenBucket(t, 5, 5, 10*time.Second)
	ctx := context.Background()

	now := time.Now()
	mr.SetTime(now)

	// Exhaust all tokens (capacity=5, refillInterval=10s).
	// rate = 5/10s = 0.5 tokens/s.
	backend.AllowN(ctx, "key1", 5)

	// Advance TIME by 1s => only 0.5 tokens refilled, which is < 1 cost.
	mr.SetTime(now.Add(1 * time.Second))

	res, err := backend.AllowN(ctx, "key1", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allow {
		t.Fatal("expected request to be denied; not enough time for a full token refill")
	}
	if res.Remaining != 0 {
		t.Fatalf("expected 0 remaining, got %d", res.Remaining)
	}
}

// --- Key Isolation ---

func TestRedisAllow_DifferentKeysAreIsolated(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 5, 5, time.Second)
	ctx := context.Background()

	// Exhaust key1.
	backend.AllowN(ctx, "key1", 5)

	// key2 should be unaffected.
	res, err := backend.AllowN(ctx, "key2", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allow {
		t.Fatal("expected key2 to be allowed; it should be independent of key1")
	}
	if res.Remaining != 4 {
		t.Fatalf("expected 4 remaining for key2, got %d", res.Remaining)
	}
}

func TestRedisAllow_ManyKeysIndependent(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 3, 3, time.Second)
	ctx := context.Background()

	keys := []string{"user:alpha", "user:bravo", "user:charlie"}

	for _, key := range keys {
		res, err := backend.AllowN(ctx, key, 1)
		if err != nil {
			t.Fatalf("key %s: unexpected error: %v", key, err)
		}
		if !res.Allow {
			t.Fatalf("key %s: expected allowed", key)
		}
		if res.Remaining != 2 {
			t.Fatalf("key %s: expected 2 remaining, got %d", key, res.Remaining)
		}
	}
}

// --- Concurrency ---

func TestRedisAllow_ConcurrentAccess(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 50, 50, time.Hour)
	ctx := context.Background()

	var (
		wg           sync.WaitGroup
		mu           sync.Mutex
		allowedCount int64
		deniedCount  int64
	)

	goroutines := 100
	maxTokens := int64(50)
	wg.Add(goroutines)

	// Use a very large refillInterval so that the fractional token refill
	// from miniredis TIME drift between calls is negligible.
	for range goroutines {
		go func() {
			defer wg.Done()
			res, err := backend.AllowN(ctx, "concurrent-key", 1)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			mu.Lock()
			if res.Allow {
				allowedCount++
			} else {
				deniedCount++
			}
			mu.Unlock()
		}()
	}

	wg.Wait()

	// Exactly maxTokens requests should be allowed.
	if allowedCount != maxTokens {
		t.Fatalf("expected %d allowed, got %d", maxTokens, allowedCount)
	}
	expectedDenied := int64(goroutines) - maxTokens
	if deniedCount != expectedDenied {
		t.Fatalf("expected %d denied, got %d", expectedDenied, deniedCount)
	}
}

func TestRedisAllow_ConcurrentDifferentKeys(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 10, 10, time.Hour)
	ctx := context.Background()

	var wg sync.WaitGroup
	keys := []string{"user:1", "user:2", "user:3", "user:4", "user:5"}
	requestsPerKey := 10

	wg.Add(len(keys) * requestsPerKey)

	// Use a very large refillInterval to prevent fractional token refills from TIME drift.
	for _, key := range keys {
		for range requestsPerKey {
			go func(k string) {
				defer wg.Done()
				_, err := backend.AllowN(ctx, k, 1)
				if err != nil {
					t.Errorf("unexpected error for key %s: %v", k, err)
				}
			}(key)
		}
	}

	wg.Wait()

	// All requests should have been allowed since each key has exactly 10 tokens.
	for _, key := range keys {
		res, _ := backend.AllowN(ctx, key, 1)
		// After consuming 10 tokens, a new request should be denied.
		if res.Allow {
			t.Fatalf("expected key %s to be exhausted, but got allowed with %d remaining", key, res.Remaining)
		}
	}
}

// --- Edge Cases ---

func TestRedisAllow_ZeroCost(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 10, 10, time.Second)
	ctx := context.Background()

	res, err := backend.AllowN(ctx, "key1", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allow {
		t.Fatal("expected zero-cost request to be allowed")
	}
	if res.Remaining != 10 {
		t.Fatalf("expected 10 remaining (zero cost should not consume), got %d", res.Remaining)
	}
	if res.RetryAfter != 0 {
		t.Fatalf("expected zero retryAfter, got %v", res.RetryAfter)
	}
}

func TestRedisAllow_MaxTokenOfOne(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 1, 1, time.Second)
	ctx := context.Background()

	// First request allowed.
	res, _ := backend.AllowN(ctx, "key1", 1)
	if !res.Allow {
		t.Fatal("expected first request to be allowed")
	}
	if res.Remaining != 0 {
		t.Fatalf("expected 0 remaining, got %d", res.Remaining)
	}

	// Second request denied immediately.
	res, _ = backend.AllowN(ctx, "key1", 1)
	if res.Allow {
		t.Fatal("expected second request to be denied")
	}
	if res.RetryAfter <= 0 {
		t.Fatal("expected positive retryAfter")
	}
}

func TestRedisAllow_MultipleRapidRequests(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 100, 100, time.Hour)
	ctx := context.Background()

	// Use a very large refillInterval so miniredis TIME differences between
	// sequential calls don't cause fractional token refills.
	var lastRemaining int64
	for i := range 100 {
		res, err := backend.AllowN(ctx, "burst", 1)
		if err != nil {
			t.Fatalf("request %d: unexpected error: %v", i, err)
		}
		if !res.Allow {
			t.Fatalf("request %d: expected allowed", i)
		}
		lastRemaining = res.Remaining
	}

	if lastRemaining != 0 {
		t.Fatalf("expected 0 remaining after 100 requests, got %d", lastRemaining)
	}

	// 101st should be denied.
	res, _ := backend.AllowN(ctx, "burst", 1)
	if res.Allow {
		t.Fatal("expected 101st request to be denied")
	}
}

func TestRedisAllow_LargeRefillInterval(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 5, 5, time.Hour)
	ctx := context.Background()

	// Use all tokens with a very long refill.
	backend.AllowN(ctx, "key1", 5)

	res, _ := backend.AllowN(ctx, "key1", 1)
	if res.Allow {
		t.Fatal("expected denied with hour-long refill")
	}
	if res.RetryAfter <= 0 {
		t.Fatal("expected positive retryAfter for hour-long refill")
	}
}

// --- Redis Connection Error ---

func TestRedisAllow_FailsOnClosedConnection(t *testing.T) {
	mr := miniredis.RunT(t)

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	backend := NewRedisTokenBucket(client, 10, 100, time.Second)

	// Close the miniredis server to simulate a connection failure.
	mr.Close()

	_, err := backend.AllowN(context.Background(), "key1", 1)
	if err == nil {
		t.Fatal("expected error when Redis connection is closed")
	}
}

// --- Cancelled Context ---

func TestRedisAllow_CancelledContext(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 10, 10, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := backend.AllowN(ctx, "key1", 1)

	// With a cancelled context, the Redis call should return an error.
	if err == nil {
		t.Fatal("expected error with cancelled context")
	}
}

// --- Backend Interface Compliance ---

func TestRedisTokenBucket_ImplementsBackendInterface(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 10, 10, time.Second)

	// Compile-time check: redisTokenBucket must satisfy the Backend interface.
	var _ Backend = backend
}

// --- Refill Restores Full Bucket ---

func TestRedisAllow_FullRefillRestoresBucket(t *testing.T) {
	backend, mr := newTestRedisTokenBucket(t, 10, 10, time.Second)
	ctx := context.Background()

	now := time.Now()
	mr.SetTime(now)

	// Exhaust all tokens.
	backend.AllowN(ctx, "key1", 10)

	// Advance TIME by the full refill interval.
	mr.SetTime(now.Add(1 * time.Second))

	// Bucket should be fully restored (capped at capacity).
	res, err := backend.AllowN(ctx, "key1", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allow {
		t.Fatal("expected request to be allowed after full refill")
	}
	if res.Remaining != 9 {
		t.Fatalf("expected 9 remaining (10 refilled - 1 cost), got %d", res.Remaining)
	}
}

// --- Retry After Accuracy ---

func TestRedisAllow_RetryAfterAccuracy(t *testing.T) {
	backend, _ := newTestRedisTokenBucket(t, 10, 10, time.Second)
	ctx := context.Background()

	// Use 8 of 10 tokens, then request cost=5 (need 3 more).
	backend.AllowN(ctx, "key1", 8)

	res, err := backend.AllowN(ctx, "key1", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Need ~3 more tokens at rate 10/s = 0.01 tokens/ms.
	// retryAfter = ceil(needed / rate) ≈ 300ms.
	// Allow a small tolerance for floating point arithmetic in Lua and
	// minor miniredis TIME drift between the two Allow calls.
	expectedMin := 290 * time.Millisecond
	expectedMax := 310 * time.Millisecond
	if res.RetryAfter < expectedMin || res.RetryAfter > expectedMax {
		t.Fatalf("expected retryAfter between %v and %v, got %v", expectedMin, expectedMax, res.RetryAfter)
	}
}

// --- Bucket Recreation After Expiry ---

func TestRedisAllow_BucketRecreatedAfterTTLExpiry(t *testing.T) {
	backend, mr := newTestRedisTokenBucket(t, 5, 5, time.Second)
	ctx := context.Background()

	now := time.Now()
	mr.SetTime(now)

	// Create and exhaust a bucket.
	backend.AllowN(ctx, "key1", 5)

	// Advance TIME and TTL past the key expiry (TTL = 2 * refillInterval = 2s).
	mr.SetTime(now.Add(3 * time.Second))
	mr.FastForward(3 * time.Second)

	// A new request should create a fresh bucket with full tokens.
	res, err := backend.AllowN(ctx, "key1", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allow {
		t.Fatal("expected request to be allowed on a fresh bucket")
	}
	if res.Remaining != 4 {
		t.Fatalf("expected 4 remaining on fresh bucket, got %d", res.Remaining)
	}
}
