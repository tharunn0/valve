package valve

import (
	"context"
	"sync"
	"testing"
	"time"
)

// --- Basic Allow Behavior ---

func TestAllow_FirstRequestAllowed(t *testing.T) {
	backend := NewMemoryTokenBucket(10, 10, time.Second)

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

func TestAllow_ExhaustAllTokens(t *testing.T) {
	backend := NewMemoryTokenBucket(5, 5, time.Second)
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
	// rate=5, refillInterval=1s => tokenInterval = 1s/5 = 200ms per token, need 1 => 200ms
	if res.RetryAfter != 200*time.Millisecond {
		t.Fatalf("expected retryAfter of 200ms, got %v", res.RetryAfter)
	}
}

func TestAllow_CostGreaterThanOne(t *testing.T) {
	backend := NewMemoryTokenBucket(10, 10, time.Second)
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

func TestAllow_CostExceedsAvailableTokens(t *testing.T) {
	backend := NewMemoryTokenBucket(10, 10, time.Second)
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
	// Need 3 more tokens (5 - 2), tokenInterval = 1s/10 = 100ms, so retryAfter = 3 * 100ms = 300ms.
	expectedRetry := 300 * time.Millisecond
	if res.RetryAfter != expectedRetry {
		t.Fatalf("expected retryAfter %v, got %v", expectedRetry, res.RetryAfter)
	}
}

func TestAllow_CostEqualToMaxTokens(t *testing.T) {
	backend := NewMemoryTokenBucket(10, 10, time.Second)
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

func TestAllow_CostExceedsMaxTokens(t *testing.T) {
	backend := NewMemoryTokenBucket(10, 10, time.Second)
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
	// Need 5 more tokens (15 - 10), tokenInterval = 1s/10 = 100ms, so retryAfter = 5 * 100ms = 500ms.
	expectedRetry := 500 * time.Millisecond
	if res.RetryAfter != expectedRetry {
		t.Fatalf("expected retryAfter %v, got %v", expectedRetry, res.RetryAfter)
	}
}

// --- Token Refill Behavior ---

func TestAllow_TokenRefillAfterWait(t *testing.T) {
	// rate=5, maxTokens=5, refillInterval=100ms => tokenInterval = 100ms/5 = 20ms per token
	backend := NewMemoryTokenBucket(5, 5, 100*time.Millisecond).(*memoryTokenBucket)
	ctx := context.Background()

	// Exhaust all tokens.
	backend.AllowN(ctx, "key1", 5)

	// Manually age the lastRefill to simulate time passing (60ms = 3 tokens at 20ms/token).
	backend.mu.Lock()
	backend.buckets["key1"].lastRefill = backend.buckets["key1"].lastRefill.Add(-60 * time.Millisecond)
	backend.mu.Unlock()

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

func TestAllow_RefillCapsAtMaxTokens(t *testing.T) {
	// rate=5, maxTokens=5, refillInterval=100ms => tokenInterval = 20ms per token
	backend := NewMemoryTokenBucket(5, 5, 100*time.Millisecond).(*memoryTokenBucket)
	ctx := context.Background()

	// Use 2 of 5 tokens.
	backend.AllowN(ctx, "key1", 2)

	// Age by 1 second (far beyond refill), but bucket should cap at 5.
	backend.mu.Lock()
	backend.buckets["key1"].lastRefill = backend.buckets["key1"].lastRefill.Add(-1 * time.Second)
	backend.mu.Unlock()

	res, _ := backend.AllowN(ctx, "key1", 1)
	if !res.Allow {
		t.Fatal("expected request to be allowed")
	}
	// Should be capped at 5 (max) then minus 1 (cost) = 4.
	if res.Remaining != 4 {
		t.Fatalf("expected 4 remaining (capped at 5, minus 1), got %d", res.Remaining)
	}
}

func TestAllow_PartialRefillNotEnough(t *testing.T) {
	// rate=5, maxTokens=5, refillInterval=1s => tokenInterval = 1s/5 = 200ms per token
	backend := NewMemoryTokenBucket(5, 5, time.Second).(*memoryTokenBucket)
	ctx := context.Background()

	// Exhaust all tokens.
	backend.AllowN(ctx, "key1", 5)

	// Age by only 100ms, which is less than one tokenInterval of 200ms.
	backend.mu.Lock()
	backend.buckets["key1"].lastRefill = backend.buckets["key1"].lastRefill.Add(-100 * time.Millisecond)
	backend.mu.Unlock()

	res, _ := backend.AllowN(ctx, "key1", 1)
	if res.Allow {
		t.Fatal("expected request to be denied; not enough time for a refill")
	}
	if res.Remaining != 0 {
		t.Fatalf("expected 0 remaining, got %d", res.Remaining)
	}
}

// --- Key Isolation ---

func TestAllow_DifferentKeysAreIsolated(t *testing.T) {
	backend := NewMemoryTokenBucket(5, 5, time.Second)
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

// --- Concurrency ---

func TestAllow_ConcurrentAccess(t *testing.T) {
	backend := NewMemoryTokenBucket(50, 50, time.Second)
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

func TestAllow_ConcurrentDifferentKeys(t *testing.T) {
	backend := NewMemoryTokenBucket(10, 10, time.Second)
	ctx := context.Background()

	var wg sync.WaitGroup
	keys := []string{"user:1", "user:2", "user:3", "user:4", "user:5"}
	requestsPerKey := 10

	wg.Add(len(keys) * requestsPerKey)

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

// --- Janitor Cleanup ---

func TestJanitor_CleansUpStaleBuckets(t *testing.T) {
	backend := NewMemoryTokenBucket(10, 10, time.Second).(*memoryTokenBucket)
	backend.stalePeriod = 200 * time.Millisecond
	ctx := context.Background()

	// Create a bucket.
	backend.AllowN(ctx, "stale-key", 1)

	// Verify it exists.
	backend.mu.Lock()
	if _, ok := backend.buckets["stale-key"]; !ok {
		backend.mu.Unlock()
		t.Fatal("expected bucket to exist after Allow")
	}
	backend.mu.Unlock()

	// Wait for the janitor to run (stalePeriod + janitor interval of stalePeriod/2 + buffer).
	time.Sleep(backend.stalePeriod + backend.stalePeriod/2 + 50*time.Millisecond)

	// Bucket should have been cleaned up.
	backend.mu.Lock()
	_, exists := backend.buckets["stale-key"]
	backend.mu.Unlock()

	if exists {
		t.Fatal("expected stale bucket to be cleaned up by janitor")
	}
}

func TestJanitor_PreservesActiveBuckets(t *testing.T) {
	backend := NewMemoryTokenBucket(100, 100, time.Second).(*memoryTokenBucket)
	backend.stalePeriod = 300 * time.Millisecond
	ctx := context.Background()

	// Create a bucket and keep it active by making requests.
	backend.AllowN(ctx, "active-key", 1)

	// Sleep less than the stale period.
	time.Sleep(100 * time.Millisecond)

	// Make another request to refresh the bucket's lastRefill indirectly
	// (the refill logic updates lastRefill when tokens are added).
	backend.AllowN(ctx, "active-key", 1)

	// Sleep again, but less than stalePeriod from last activity.
	time.Sleep(100 * time.Millisecond)

	backend.mu.Lock()
	_, exists := backend.buckets["active-key"]
	backend.mu.Unlock()

	if !exists {
		t.Fatal("expected active bucket to be preserved by janitor")
	}
}

func TestJanitor_SelectiveCleanup(t *testing.T) {
	backend := NewMemoryTokenBucket(10, 10, time.Second).(*memoryTokenBucket)
	backend.stalePeriod = 200 * time.Millisecond
	ctx := context.Background()

	// Create two buckets.
	backend.AllowN(ctx, "stale-key", 1)
	backend.AllowN(ctx, "fresh-key", 1)

	// Wait for stale period to pass.
	time.Sleep(backend.stalePeriod + backend.stalePeriod/2 + 50*time.Millisecond)

	// Refresh one of them.
	backend.AllowN(ctx, "fresh-key", 1)

	// Give janitor time to run again.
	time.Sleep(backend.stalePeriod/2 + 50*time.Millisecond)

	backend.mu.Lock()
	_, staleExists := backend.buckets["stale-key"]
	_, freshExists := backend.buckets["fresh-key"]
	backend.mu.Unlock()

	if staleExists {
		t.Fatal("expected stale-key to be cleaned up")
	}
	if !freshExists {
		t.Fatal("expected fresh-key to be preserved")
	}
}

// --- Edge Cases ---

func TestAllow_ZeroCost(t *testing.T) {
	backend := NewMemoryTokenBucket(10, 10, time.Second)
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

func TestAllow_MaxTokenOfOne(t *testing.T) {
	// rate=1, maxTokens=1, refillInterval=1s => tokenInterval = 1s per token
	backend := NewMemoryTokenBucket(1, 1, time.Second)
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
	if res.RetryAfter != time.Second {
		t.Fatalf("expected retryAfter of 1s, got %v", res.RetryAfter)
	}
}

func TestAllow_RetryAfterAccuracy(t *testing.T) {
	// rate=10, maxTokens=10, refillInterval=200ms => tokenInterval = 200ms/10 = 20ms per token
	backend := NewMemoryTokenBucket(10, 10, 200*time.Millisecond)
	ctx := context.Background()

	// Use 8 of 10, then request cost=5 (need 3 more).
	backend.AllowN(ctx, "key1", 8)

	res, _ := backend.AllowN(ctx, "key1", 5)

	// Need 3 tokens * 20ms = 60ms.
	expected := 60 * time.Millisecond
	if res.RetryAfter != expected {
		t.Fatalf("expected retryAfter %v, got %v", expected, res.RetryAfter)
	}
}

func TestAllow_BucketRecreatedAfterJanitorCleanup(t *testing.T) {
	backend := NewMemoryTokenBucket(5, 5, time.Second).(*memoryTokenBucket)
	backend.stalePeriod = 200 * time.Millisecond
	ctx := context.Background()

	// Create and exhaust a bucket.
	backend.AllowN(ctx, "key1", 5)

	// Wait for janitor to clean it up.
	time.Sleep(backend.stalePeriod + backend.stalePeriod/2 + 50*time.Millisecond)

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

func TestAllow_MultipleRapidRequests(t *testing.T) {
	backend := NewMemoryTokenBucket(100, 100, time.Second)
	ctx := context.Background()

	var lastRemaining int64
	for i := range 100 {
		res, _ := backend.AllowN(ctx, "burst", 1)
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

func TestAllow_LargeRefillInterval(t *testing.T) {
	// rate=5, maxTokens=5, refillInterval=1h => tokenInterval = 1h/5 = 12min per token
	backend := NewMemoryTokenBucket(5, 5, time.Hour)
	ctx := context.Background()

	// Use all tokens with a very long refill.
	backend.AllowN(ctx, "key1", 5)

	res, _ := backend.AllowN(ctx, "key1", 1)
	if res.Allow {
		t.Fatal("expected denied with hour-long refill")
	}
	// tokenInterval = 1h/5 = 12min, need 1 token => 12min
	if res.RetryAfter != 12*time.Minute {
		t.Fatalf("expected retryAfter of 12m, got %v", res.RetryAfter)
	}
}

func TestAllow_ContextNotUsedButAccepted(t *testing.T) {
	backend := NewMemoryTokenBucket(10, 10, time.Second)

	// Verify that a cancelled context doesn't cause errors
	// (current implementation doesn't use context, but the interface accepts it).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := backend.AllowN(ctx, "key1", 1)
	if err != nil {
		t.Fatalf("unexpected error with cancelled context: %v", err)
	}
	if !res.Allow {
		t.Fatal("expected request to be allowed even with cancelled context")
	}
}
