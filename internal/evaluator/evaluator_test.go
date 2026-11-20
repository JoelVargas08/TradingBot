package evaluator

import (
	"context"
	"errors"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

type fakeStore struct {
	last domain.SignalEvent
	err  error
}

func (f *fakeStore) SaveSignal(ctx context.Context, ev domain.SignalEvent) error { return nil }

func (f *fakeStore) LastSignal(ctx context.Context, s, sym, tf string) (domain.SignalEvent, error) {
	return f.last, f.err
}

func TestEvaluatorEmitsOnFirstSignal(t *testing.T) {
	e := New(&fakeStore{err: domain.ErrNotFound})
	emit, err := e.Evaluate(context.Background(), domain.SignalEvent{Direction: domain.DirectionBuy})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !emit {
		t.Error("primera señal debería emitirse")
	}
}

func TestEvaluatorEmitsOnDirectionChange(t *testing.T) {
	e := New(&fakeStore{last: domain.SignalEvent{Direction: domain.DirectionBuy}})
	emit, err := e.Evaluate(context.Background(), domain.SignalEvent{Direction: domain.DirectionSell})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !emit {
		t.Error("cambio buy→sell debería emitirse")
	}
}

func TestEvaluatorSkipsSameDirection(t *testing.T) {
	e := New(&fakeStore{last: domain.SignalEvent{Direction: domain.DirectionBuy}})
	emit, err := e.Evaluate(context.Background(), domain.SignalEvent{Direction: domain.DirectionBuy})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if emit {
		t.Error("misma dirección no debería emitirse")
	}
}

func TestEvaluatorIdempotencyEmitsOnNewBarSameDirection(t *testing.T) {
	last := domain.SignalEvent{
		StrategyID: "s", Symbol: "BTCUSDT", Timeframe: "15m",
		Direction: domain.DirectionBuy, BarTS: time.UnixMilli(1000),
	}
	e := NewWithPolicy(&fakeStore{last: last}, PolicyIdempotency)
	next := last
	next.BarTS = time.UnixMilli(2000)
	emit, err := e.Evaluate(context.Background(), next)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !emit {
		t.Error("nueva vela en la misma dirección debería emitirse")
	}
}

func TestEvaluatorIdempotencySkipsSameSignal(t *testing.T) {
	last := domain.SignalEvent{
		StrategyID: "s", Symbol: "BTCUSDT", Timeframe: "15m",
		Direction: domain.DirectionBuy, BarTS: time.UnixMilli(1000),
		Meta: map[string]any{domain.MetaKeySetup: "choch_long"},
	}
	e := NewWithPolicy(&fakeStore{last: last}, PolicyIdempotency)
	next := last
	emit, err := e.Evaluate(context.Background(), next)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if emit {
		t.Error("misma idempotency key no debería emitirse")
	}
}

func TestEvaluatorPropagatesErrors(t *testing.T) {
	boom := errors.New("boom")
	e := New(&fakeStore{err: boom})
	if _, err := e.Evaluate(context.Background(), domain.SignalEvent{Direction: domain.DirectionBuy}); !errors.Is(err, boom) {
		t.Errorf("error no propagado: %v", err)
	}
}
