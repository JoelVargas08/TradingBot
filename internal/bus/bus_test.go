package bus

import (
	"context"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func TestPublishSubscribe(t *testing.T) {
	b := New(2)
	defer b.Close()
	ctx := context.Background()

	ev := domain.SignalEvent{Symbol: "BTCUSDT", Direction: domain.DirectionBuy, Price: 60000}
	if err := b.Publish(ctx, ev); err != nil {
		t.Fatalf("Publish error: %v", err)
	}
	select {
	case got := <-b.Subscribe():
		if got.Symbol != "BTCUSDT" {
			t.Errorf("got %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout esperando evento")
	}
}

func TestPublishHonorsContext(t *testing.T) {
	b := New(1)
	defer b.Close()
	ev := domain.SignalEvent{Symbol: "BTCUSDT"}

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := b.Publish(ctx, ev); err != nil {
		t.Fatalf("primer publish no debería fallar: %v", err)
	}
	if err := b.Publish(ctx, ev); err == nil {
		t.Error("publish con canal lleno y contexto expirado debería fallar")
	}
}
