package valve

import (
	"context"
	"testing"
	"time"
)

func TestNewLimiter(t *testing.T) {
	backend := NewMemoryTokenBucket(5, 10, time.Second)

	tests := []struct {
		name    string
		opts    []Option
		wantErr error
	}{
		{
			name: "valid options",
			opts: []Option{
				WithBackend(backend),
			},
			wantErr: nil,
		},
		{
			name:    "missing backend",
			opts:    []Option{},
			wantErr: ErrBackendRequired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewLimiter(tt.opts...)
			if err != tt.wantErr {
				t.Errorf("NewLimiter() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLimiter_Allow(t *testing.T) {
	// 5 tokens per second => 200ms per token, max 10 tokens
	backend := NewMemoryTokenBucket(5, 10, time.Second)
	limiter, err := NewLimiter(
		WithBackend(backend),
	)
	if err != nil {
		t.Fatalf("unexpected error creating limiter: %v", err)
	}

	ctx := context.Background()
	key := "test-key"

	// Initial burst — consume all 10 tokens
	for i := 0; i < 10; i++ {
		res, err := limiter.Allow(ctx, key)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.Allow {
			t.Fatalf("request %d should be allowed", i)
		}
		if res.Remaining != int64(9-i) {
			t.Fatalf("expected remaining %d, got %d", 9-i, res.Remaining)
		}
		if res.RetryAfter != 0 {
			t.Fatalf("expected retryAfter 0, got %v", res.RetryAfter)
		}
	}

	// 11th request should be denied
	res, err := limiter.AllowN(ctx, key, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allow {
		t.Fatal("expected request to be denied")
	}
	if res.Remaining != 0 {
		t.Fatalf("expected remaining 0, got %d", res.Remaining)
	}
	// Since 1 token refilled every 200ms, retryAfter should be 200ms
	if res.RetryAfter != 200*time.Millisecond {
		t.Fatalf("expected retryAfter 200ms, got %v", res.RetryAfter)
	}
}
