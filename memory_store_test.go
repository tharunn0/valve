package valve

import (
	"context"
	"sync"
	"testing"
	"time"
)

// --- Basic Allow Behavior ---

func TestAllow_FirstRequestAllowed(t *testing.T) {
	store := NewMemoryStore(5 * time.Minute)

	allow, remaining, retryAfter, err := store.Allow(context.Background(), "key1", 1, 10, time.Second)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allow {
		t.Fatal("expected request to be allowed")
	}
	if remaining != 9 {
		t.Fatalf("expected 9 remaining, got %d", remaining)
	}
	if retryAfter != 0 {
		t.Fatalf("expected zero retryAfter, got %v", retryAfter)
	}
}

func TestAllow_ExhaustAllTokens(t *testing.T) {
	store := NewMemoryStore(5 * time.Minute)
	ctx := context.Background()

	// Consume all 5 tokens one by one.
	for i := range 5 {
		allow, remaining, _, err := store.Allow(ctx, "key1", 1, 5, time.Second)
		if err != nil {
			t.Fatalf("request %d: unexpected error: %v", i, err)
		}
		if !allow {
			t.Fatalf("request %d: expected allowed", i)
		}
		expectedRemaining := 5 - (i + 1)
		if remaining != int64(expectedRemaining) {
			t.Fatalf("request %d: expected %d remaining, got %d", i, expectedRemaining, remaining)
		}
	}

	// The 6th request should be denied.
	allow, remaining, retryAfter, err := store.Allow(ctx, "key1", 1, 5, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allow {
		t.Fatal("expected request to be denied after exhausting tokens")
	}
	if remaining != 0 {
		t.Fatalf("expected 0 remaining, got %d", remaining)
	}
	if retryAfter != time.Second {
		t.Fatalf("expected retryAfter of 1s, got %v", retryAfter)
	}
}

func TestAllow_CostGreaterThanOne(t *testing.T) {
	store := NewMemoryStore(5 * time.Minute)
	ctx := context.Background()

	// Request with cost 3 from a bucket of 10.
	allow, remaining, _, err := store.Allow(ctx, "key1", 3, 10, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allow {
		t.Fatal("expected request to be allowed")
	}
	if remaining != 7 {
		t.Fatalf("expected 7 remaining, got %d", remaining)
	}
}

func TestAllow_CostExceedsAvailableTokens(t *testing.T) {
	store := NewMemoryStore(5 * time.Minute)
	ctx := context.Background()

	// Consume 8 of 10 tokens.
	store.Allow(ctx, "key1", 8, 10, time.Second)

	// Now request cost=5, but only 2 remain.
	allow, remaining, retryAfter, err := store.Allow(ctx, "key1", 5, 10, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allow {
		t.Fatal("expected request to be denied")
	}
	if remaining != 2 {
		t.Fatalf("expected 2 remaining, got %d", remaining)
	}
	// Need 3 more tokens (5 - 2), so retryAfter should be 3 * refillInterval.
	expectedRetry := 3 * time.Second
	if retryAfter != expectedRetry {
		t.Fatalf("expected retryAfter %v, got %v", expectedRetry, retryAfter)
	}
}

func TestAllow_CostEqualToMaxTokens(t *testing.T) {
	store := NewMemoryStore(5 * time.Minute)
	ctx := context.Background()

	// A single request that costs exactly the full bucket.
	allow, remaining, _, err := store.Allow(ctx, "key1", 10, 10, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allow {
		t.Fatal("expected request to be allowed")
	}
	if remaining != 0 {
		t.Fatalf("expected 0 remaining, got %d", remaining)
	}

	// Next request should be denied.
	allow, _, _, _ = store.Allow(ctx, "key1", 1, 10, time.Second)
	if allow {
		t.Fatal("expected request to be denied after full depletion")
	}
}

func TestAllow_CostExceedsMaxTokens(t *testing.T) {
	store := NewMemoryStore(5 * time.Minute)
	ctx := context.Background()

	// A request whose cost is larger than the bucket can ever hold.
	allow, remaining, retryAfter, err := store.Allow(ctx, "key1", 15, 10, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allow {
		t.Fatal("expected request to be denied when cost > maxToken")
	}
	if remaining != 10 {
		t.Fatalf("expected 10 remaining (unchanged), got %d", remaining)
	}
	// Need 5 more tokens (15 - 10), so retryAfter = 5s.
	expectedRetry := 5 * time.Second
	if retryAfter != expectedRetry {
		t.Fatalf("expected retryAfter %v, got %v", expectedRetry, retryAfter)
	}
}

// --- Token Refill Behavior ---

func TestAllow_TokenRefillAfterWait(t *testing.T) {
	store := NewMemoryStore(5 * time.Minute).(*memoryStore)
	ctx := context.Background()

	// Exhaust all tokens.
	store.Allow(ctx, "key1", 5, 5, 100*time.Millisecond)

	// Manually age the lastRefill to simulate time passing (300ms = 3 tokens).
	store.mu.Lock()
	store.buckets["key1"].lastRefill = store.buckets["key1"].lastRefill.Add(-300 * time.Millisecond)
	store.mu.Unlock()

	allow, remaining, _, err := store.Allow(ctx, "key1", 2, 5, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allow {
		t.Fatal("expected request to be allowed after token refill")
	}
	if remaining != 1 {
		t.Fatalf("expected 1 remaining (3 refilled - 2 cost), got %d", remaining)
	}
}

func TestAllow_RefillCapsAtMaxTokens(t *testing.T) {
	store := NewMemoryStore(5 * time.Minute).(*memoryStore)
	ctx := context.Background()

	// Use 2 of 5 tokens.
	store.Allow(ctx, "key1", 2, 5, 100*time.Millisecond)

	// Age by 1 second (10 refill intervals), but bucket should cap at 5.
	store.mu.Lock()
	store.buckets["key1"].lastRefill = store.buckets["key1"].lastRefill.Add(-1 * time.Second)
	store.mu.Unlock()

	allow, remaining, _, _ := store.Allow(ctx, "key1", 1, 5, 100*time.Millisecond)
	if !allow {
		t.Fatal("expected request to be allowed")
	}
	// Should be capped at 5 (max) then minus 1 (cost) = 4.
	if remaining != 4 {
		t.Fatalf("expected 4 remaining (capped at 5, minus 1), got %d", remaining)
	}
}

func TestAllow_PartialRefillNotEnough(t *testing.T) {
	store := NewMemoryStore(5 * time.Minute).(*memoryStore)
	ctx := context.Background()

	// Exhaust all tokens.
	store.Allow(ctx, "key1", 5, 5, time.Second)

	// Age by only 500ms, which is less than one refill interval of 1s.
	store.mu.Lock()
	store.buckets["key1"].lastRefill = store.buckets["key1"].lastRefill.Add(-500 * time.Millisecond)
	store.mu.Unlock()

	allow, remaining, _, _ := store.Allow(ctx, "key1", 1, 5, time.Second)
	if allow {
		t.Fatal("expected request to be denied; not enough time for a refill")
	}
	if remaining != 0 {
		t.Fatalf("expected 0 remaining, got %d", remaining)
	}
}

// --- Key Isolation ---

func TestAllow_DifferentKeysAreIsolated(t *testing.T) {
	store := NewMemoryStore(5 * time.Minute)
	ctx := context.Background()

	// Exhaust key1.
	store.Allow(ctx, "key1", 5, 5, time.Second)

	// key2 should be unaffected.
	allow, remaining, _, err := store.Allow(ctx, "key2", 1, 5, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allow {
		t.Fatal("expected key2 to be allowed; it should be independent of key1")
	}
	if remaining != 4 {
		t.Fatalf("expected 4 remaining for key2, got %d", remaining)
	}
}

// --- Concurrency ---

func TestAllow_ConcurrentAccess(t *testing.T) {
	store := NewMemoryStore(5 * time.Minute)
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
			allow, _, _, err := store.Allow(ctx, "concurrent-key", 1, maxTokens, time.Second)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			mu.Lock()
			if allow {
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
	store := NewMemoryStore(5 * time.Minute)
	ctx := context.Background()

	var wg sync.WaitGroup
	keys := []string{"user:1", "user:2", "user:3", "user:4", "user:5"}
	requestsPerKey := 10
	maxTokens := int64(10)

	wg.Add(len(keys) * requestsPerKey)

	for _, key := range keys {
		for range requestsPerKey {
			go func(k string) {
				defer wg.Done()
				_, _, _, err := store.Allow(ctx, k, 1, maxTokens, time.Second)
				if err != nil {
					t.Errorf("unexpected error for key %s: %v", k, err)
				}
			}(key)
		}
	}

	wg.Wait()

	// All requests should have been allowed since each key has exactly 10 tokens.
	for _, key := range keys {
		allow, remaining, _, _ := store.Allow(ctx, key, 1, maxTokens, time.Second)
		// After consuming 10 tokens, a new request should be denied.
		if allow {
			t.Fatalf("expected key %s to be exhausted, but got allowed with %d remaining", key, remaining)
		}
	}
}

// --- Janitor Cleanup ---

func TestJanitor_CleansUpStaleBuckets(t *testing.T) {
	stalePeriod := 200 * time.Millisecond
	store := NewMemoryStore(stalePeriod).(*memoryStore)
	ctx := context.Background()

	// Create a bucket.
	store.Allow(ctx, "stale-key", 1, 10, time.Second)

	// Verify it exists.
	store.mu.Lock()
	if _, ok := store.buckets["stale-key"]; !ok {
		store.mu.Unlock()
		t.Fatal("expected bucket to exist after Allow")
	}
	store.mu.Unlock()

	// Wait for the janitor to run (stalePeriod + janitor interval of stalePeriod/2 + buffer).
	time.Sleep(stalePeriod + stalePeriod/2 + 50*time.Millisecond)

	// Bucket should have been cleaned up.
	store.mu.Lock()
	_, exists := store.buckets["stale-key"]
	store.mu.Unlock()

	if exists {
		t.Fatal("expected stale bucket to be cleaned up by janitor")
	}
}

func TestJanitor_PreservesActiveBuckets(t *testing.T) {
	stalePeriod := 300 * time.Millisecond
	store := NewMemoryStore(stalePeriod).(*memoryStore)
	ctx := context.Background()

	// Create a bucket and keep it active by making requests.
	store.Allow(ctx, "active-key", 1, 100, time.Second)

	// Sleep less than the stale period.
	time.Sleep(100 * time.Millisecond)

	// Make another request to refresh the bucket's lastRefill indirectly
	// (the refill logic updates lastRefill when tokens are added).
	store.Allow(ctx, "active-key", 1, 100, time.Second)

	// Sleep again, but less than stalePeriod from last activity.
	time.Sleep(100 * time.Millisecond)

	store.mu.Lock()
	_, exists := store.buckets["active-key"]
	store.mu.Unlock()

	if !exists {
		t.Fatal("expected active bucket to be preserved by janitor")
	}
}

func TestJanitor_SelectiveCleanup(t *testing.T) {
	stalePeriod := 200 * time.Millisecond
	store := NewMemoryStore(stalePeriod).(*memoryStore)
	ctx := context.Background()

	// Create two buckets.
	store.Allow(ctx, "stale-key", 1, 10, time.Second)
	store.Allow(ctx, "fresh-key", 1, 10, time.Second)

	// Wait for stale period to pass.
	time.Sleep(stalePeriod + stalePeriod/2 + 50*time.Millisecond)

	// Refresh one of them.
	store.Allow(ctx, "fresh-key", 1, 10, time.Second)

	// Give janitor time to run again.
	time.Sleep(stalePeriod/2 + 50*time.Millisecond)

	store.mu.Lock()
	_, staleExists := store.buckets["stale-key"]
	_, freshExists := store.buckets["fresh-key"]
	store.mu.Unlock()

	if staleExists {
		t.Fatal("expected stale-key to be cleaned up")
	}
	if !freshExists {
		t.Fatal("expected fresh-key to be preserved")
	}
}

// --- Edge Cases ---

func TestAllow_ZeroCost(t *testing.T) {
	store := NewMemoryStore(5 * time.Minute)
	ctx := context.Background()

	allow, remaining, retryAfter, err := store.Allow(ctx, "key1", 0, 10, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allow {
		t.Fatal("expected zero-cost request to be allowed")
	}
	if remaining != 10 {
		t.Fatalf("expected 10 remaining (zero cost should not consume), got %d", remaining)
	}
	if retryAfter != 0 {
		t.Fatalf("expected zero retryAfter, got %v", retryAfter)
	}
}

func TestAllow_MaxTokenOfOne(t *testing.T) {
	store := NewMemoryStore(5 * time.Minute)
	ctx := context.Background()

	// First request allowed.
	allow, remaining, _, _ := store.Allow(ctx, "key1", 1, 1, time.Second)
	if !allow {
		t.Fatal("expected first request to be allowed")
	}
	if remaining != 0 {
		t.Fatalf("expected 0 remaining, got %d", remaining)
	}

	// Second request denied immediately.
	allow, _, retryAfter, _ := store.Allow(ctx, "key1", 1, 1, time.Second)
	if allow {
		t.Fatal("expected second request to be denied")
	}
	if retryAfter != time.Second {
		t.Fatalf("expected retryAfter of 1s, got %v", retryAfter)
	}
}

func TestAllow_RetryAfterAccuracy(t *testing.T) {
	store := NewMemoryStore(5 * time.Minute)
	ctx := context.Background()

	// Use 8 of 10, then request cost=5 (need 3 more).
	store.Allow(ctx, "key1", 8, 10, 200*time.Millisecond)

	_, _, retryAfter, _ := store.Allow(ctx, "key1", 5, 10, 200*time.Millisecond)

	// Need 3 tokens * 200ms = 600ms.
	expected := 600 * time.Millisecond
	if retryAfter != expected {
		t.Fatalf("expected retryAfter %v, got %v", expected, retryAfter)
	}
}

func TestAllow_BucketRecreatedAfterJanitorCleanup(t *testing.T) {
	stalePeriod := 200 * time.Millisecond
	store := NewMemoryStore(stalePeriod)
	ctx := context.Background()

	// Create and exhaust a bucket.
	store.Allow(ctx, "key1", 5, 5, time.Second)

	// Wait for janitor to clean it up.
	time.Sleep(stalePeriod + stalePeriod/2 + 50*time.Millisecond)

	// A new request should create a fresh bucket with full tokens.
	allow, remaining, _, err := store.Allow(ctx, "key1", 1, 5, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allow {
		t.Fatal("expected request to be allowed on a fresh bucket")
	}
	if remaining != 4 {
		t.Fatalf("expected 4 remaining on fresh bucket, got %d", remaining)
	}
}

func TestAllow_MultipleRapidRequests(t *testing.T) {
	store := NewMemoryStore(5 * time.Minute)
	ctx := context.Background()

	var lastRemaining int64
	for i := range 100 {
		allow, remaining, _, _ := store.Allow(ctx, "burst", 1, 100, time.Second)
		if !allow {
			t.Fatalf("request %d: expected allowed", i)
		}
		lastRemaining = remaining
	}

	if lastRemaining != 0 {
		t.Fatalf("expected 0 remaining after 100 requests, got %d", lastRemaining)
	}

	// 101st should be denied.
	allow, _, _, _ := store.Allow(ctx, "burst", 1, 100, time.Second)
	if allow {
		t.Fatal("expected 101st request to be denied")
	}
}

func TestAllow_LargeRefillInterval(t *testing.T) {
	store := NewMemoryStore(5 * time.Minute)
	ctx := context.Background()

	// Use all tokens with a very long refill.
	store.Allow(ctx, "key1", 5, 5, time.Hour)

	allow, _, retryAfter, _ := store.Allow(ctx, "key1", 1, 5, time.Hour)
	if allow {
		t.Fatal("expected denied with hour-long refill")
	}
	if retryAfter != time.Hour {
		t.Fatalf("expected retryAfter of 1h, got %v", retryAfter)
	}
}

func TestAllow_ContextNotUsedButAccepted(t *testing.T) {
	store := NewMemoryStore(5 * time.Minute)

	// Verify that a cancelled context doesn't cause errors
	// (current implementation doesn't use context, but the interface accepts it).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	allow, _, _, err := store.Allow(ctx, "key1", 1, 10, time.Second)
	if err != nil {
		t.Fatalf("unexpected error with cancelled context: %v", err)
	}
	if !allow {
		t.Fatal("expected request to be allowed even with cancelled context")
	}
}
