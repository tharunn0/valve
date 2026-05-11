package valve

import (
	"context"
	"testing"
	"time"
)

func TestNewLimiter(t *testing.T) {
	store := NewMemoryStore(0)

	tests := []struct {
		name    string
		opts    []Option
		wantErr error
	}{
		{
			name: "valid options",
			opts: []Option{
				WithStore(store),
				WithRate(5),
				WithMaxTokens(10),
				WithRefillInterval(time.Second),
			},
			wantErr: nil,
		},
		{
			name:    "missing store",
			opts:    []Option{WithRate(5)},
			wantErr: ErrStoreRequired,
		},
		{
			name:    "invalid rate",
			opts:    []Option{WithStore(store), WithRate(0)},
			wantErr: ErrInvalidRate,
		},
		{
			name:    "invalid max tokens",
			opts:    []Option{WithStore(store), WithMaxTokens(-1)},
			wantErr: ErrInvalidMaxTokens,
		},
		{
			name:    "invalid refill interval",
			opts:    []Option{WithStore(store), WithRefillInterval(0)},
			wantErr: ErrInvalidRefillInterval,
		},
		{
			name: "rate too high",
			opts: []Option{
				WithStore(store),
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
	store := NewMemoryStore(0)
	limiter, _ := NewLimiter(
		WithStore(store),
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
	allow, remaining, retryAfter, err := limiter.AllowContext(ctx, key, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allow {
		t.Fatal("expected request to be denied")
	}
	if remaining != 0 {
		t.Fatalf("expected remaining 0, got %d", remaining)
	}
	// Since 1 token refilled every 200ms, retryAfter should be 200ms
	if retryAfter != 200*time.Millisecond {
		t.Fatalf("expected retryAfter 200ms, got %v", retryAfter)
	}
}
