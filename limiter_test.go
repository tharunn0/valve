package valve

import (
	"context"
	"testing"
	"time"
)

func TestNewLimiter(t *testing.T) {
	backend := NewMemoryTokenBucket()

	tests := []struct {
		name    string
		opts    []Option
		wantErr error
	}{
		{
			name: "valid options",
			opts: []Option{
				WithBackend(backend),
				WithRate(5),
				WithMaxTokens(10),
				WithRefillInterval(time.Second),
			},
			wantErr: nil,
		},
		{
			name:    "missing backend",
			opts:    []Option{WithRate(5)},
			wantErr: ErrBackendRequired,
		},
		{
			name:    "invalid rate",
			opts:    []Option{WithBackend(backend), WithRate(0)},
			wantErr: ErrInvalidRate,
		},
		{
			name:    "invalid max tokens",
			opts:    []Option{WithBackend(backend), WithMaxTokens(-1)},
			wantErr: ErrInvalidMaxTokens,
		},
		{
			name:    "invalid refill interval",
			opts:    []Option{WithBackend(backend), WithRefillInterval(0)},
			wantErr: ErrInvalidRefillInterval,
		},
		{
			name: "rate too high",
			opts: []Option{
				WithBackend(backend),
				WithRate(2),
				WithRefillInterval(1 * time.Nanosecond),
			},
			wantErr: ErrRateTooHigh,
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
	backend := NewMemoryTokenBucket()
	limiter, _ := NewLimiter(
		WithBackend(backend),
		WithRate(5), // 5 tokens per second => 200ms per token
		WithMaxTokens(10),
		WithRefillInterval(time.Second),
	)

	ctx := context.Background()
	key := "test-key"

	// Initial burst
	for i := 0; i < 10; i++ {
		allow, remaining, retryAfter, err := limiter.Allow(key)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !allow {
			t.Fatalf("request %d should be allowed", i)
		}
		if remaining != int64(9-i) {
			t.Fatalf("expected remaining %d, got %d", 9-i, remaining)
		}
		if retryAfter != 0 {
			t.Fatalf("expected retryAfter 0, got %v", retryAfter)
		}
	}

	// 11th request should be denied
	res, err := limiter.AllowContext(ctx, key, 1)
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
