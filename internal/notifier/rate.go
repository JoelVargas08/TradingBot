package notifier

import (
	"context"
	"sync"
	"time"
)

type tokenBucket struct {
	mu     sync.Mutex
	tokens float64
	rate   float64
	burst  float64
	last   time.Time
}

func newTokenBucket(rate, burst float64) *tokenBucket {
	return &tokenBucket{
		tokens: burst,
		rate:   rate,
		burst:  burst,
		last:   time.Now(),
	}
}

func (t *tokenBucket) wait(ctx context.Context) {
	for {
		t.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(t.last).Seconds()
		t.last = now
		t.tokens += elapsed * t.rate
		if t.tokens > t.burst {
			t.tokens = t.burst
		}
		if t.tokens >= 1 {
			t.tokens--
			t.mu.Unlock()
			return
		}
		wait := time.Duration((1 - t.tokens) / t.rate * float64(time.Second))
		t.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
