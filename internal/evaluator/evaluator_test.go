package evaluator

import (
	"context"
	"errors"
	"testing"

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

func TestEvaluatorPropagatesErrors(t *testing.T) {
	boom := errors.New("boom")
	e := New(&fakeStore{err: boom})
	if _, err := e.Evaluate(context.Background(), domain.SignalEvent{Direction: domain.DirectionBuy}); !errors.Is(err, boom) {
		t.Errorf("error no propagado: %v", err)
	}
}
