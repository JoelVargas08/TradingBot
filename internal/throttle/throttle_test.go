package throttle

import (
	"context"
	"testing"
	"time"
)

func TestLimiterEnforcesRate(t *testing.T) {
	l := New(10, 1)
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := l.Wait(ctx); err != nil {
			t.Fatalf("Wait error: %v", err)
		}
	}
	elapsed := time.Since(start)
	if elapsed < 200*time.Millisecond {
		t.Errorf("3 tokens a 10/s deberían tardar ≥200ms, tardó %v", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("el limiter se quedó bloqueado: %v", elapsed)
	}
}

func TestLimiterAllowsBurst(t *testing.T) {
	l := New(1, 5)
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 5; i++ {
		if err := l.Wait(ctx); err != nil {
			t.Fatalf("Wait error: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("el burst de 5 debería pasar de inmediato, tardó %v", elapsed)
	}
}

func TestLimiterHonorsContext(t *testing.T) {
	l := New(1, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := l.Wait(ctx); err == nil {
		t.Error("con burst 0 y contexto expirado, Wait debería fallar")
	}
}
