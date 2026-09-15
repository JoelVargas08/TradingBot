package processor

import (
	"context"
	"sync"
	"testing"
	"time"

	"tradingview-bot/internal/bus"
	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/evaluator"
	"tradingview-bot/internal/session"
	"tradingview-bot/internal/store"
)

type recordingNotifier struct {
	mu    sync.Mutex
	calls []domain.SignalEvent
}

func (r *recordingNotifier) Notify(ctx context.Context, ev domain.SignalEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, ev)
	return nil
}

func (r *recordingNotifier) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

type recordingPosition struct {
	mu    sync.Mutex
	calls []domain.SignalEvent
}

func (r *recordingPosition) OnSignal(ctx context.Context, ev domain.SignalEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, ev)
	return nil
}

func (r *recordingPosition) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func newTestPipeline(t *testing.T) (*Processor, *bus.Bus, *recordingNotifier, *store.Store) {
	return newTestPipelineWith(t, nil)
}

func newTestPipelineWith(t *testing.T, position domain.PositionController) (*Processor, *bus.Bus, *recordingNotifier, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	b := bus.New(32)
	eval := evaluator.New(st)
	rec := &recordingNotifier{}
	sess := session.New()
	sess.Start()
	p := New(b, st, eval, rec, position, sess, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	t.Cleanup(cancel)
	return p, b, rec, st
}

func ev(symbol string, dir domain.Direction, barTS int64) domain.SignalEvent {
	return domain.SignalEvent{
		StrategyID: "chandelier",
		Symbol:     symbol,
		Timeframe:  "1h",
		Direction:  dir,
		Price:      60000,
		BarTS:      time.UnixMilli(barTS),
		ReceivedAt: time.Now(),
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timeout esperando condición")
}

func TestProcessorNotifiesOnDirectionChange(t *testing.T) {
	_, b, rec, _ := newTestPipeline(t)
	ctx := context.Background()

	if err := b.Publish(ctx, ev("BTCUSDT", domain.DirectionBuy, 1)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return rec.count() == 1 })

	if err := b.Publish(ctx, ev("BTCUSDT", domain.DirectionSell, 2)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return rec.count() == 2 })
}

func TestProcessorSkipsSameDirection(t *testing.T) {
	_, b, rec, _ := newTestPipeline(t)
	ctx := context.Background()

	if err := b.Publish(ctx, ev("BTCUSDT", domain.DirectionBuy, 1)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return rec.count() == 1 })

	if err := b.Publish(ctx, ev("BTCUSDT", domain.DirectionBuy, 2)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if rec.count() != 1 {
		t.Errorf("misma dirección notificó %d veces, want 1", rec.count())
	}
}

func TestProcessorDedupesSameBar(t *testing.T) {
	_, b, rec, _ := newTestPipeline(t)
	ctx := context.Background()

	sig := ev("BTCUSDT", domain.DirectionBuy, 42)
	if err := b.Publish(ctx, sig); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return rec.count() == 1 })

	if err := b.Publish(ctx, sig); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if rec.count() != 1 {
		t.Errorf("misma barra notificó %d veces, want 1", rec.count())
	}
}

func TestProcessorSeparatesStrategiesOnSameSymbol(t *testing.T) {
	_, b, rec, _ := newTestPipeline(t)
	ctx := context.Background()

	beta := ev("BTCUSDT", domain.DirectionBuy, 1)
	beta.StrategyID = "beta"
	if err := b.Publish(ctx, ev("BTCUSDT", domain.DirectionBuy, 1)); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(ctx, beta); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return rec.count() == 2 })
}

func TestProcessorSeparatesTimeframesOnSameSymbol(t *testing.T) {
	_, b, rec, _ := newTestPipeline(t)
	ctx := context.Background()

	h4 := ev("BTCUSDT", domain.DirectionBuy, 1)
	h4.Timeframe = "4h"
	if err := b.Publish(ctx, ev("BTCUSDT", domain.DirectionBuy, 1)); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(ctx, h4); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return rec.count() == 2 })
}

func TestProcessorPersistsSignals(t *testing.T) {
	_, b, rec, st := newTestPipeline(t)
	ctx := context.Background()

	if err := b.Publish(ctx, ev("BTCUSDT", domain.DirectionBuy, 7)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return rec.count() == 1 })

	last, err := st.LastSignal(ctx, "chandelier", "BTCUSDT", "1h")
	if err != nil {
		t.Fatalf("LastSignal: %v", err)
	}
	if last.Direction != domain.DirectionBuy {
		t.Errorf("señal persistida incorrecta: %+v", last)
	}
}

func TestProcessorCallsPositionControllerOnDirectionChange(t *testing.T) {
	pos := &recordingPosition{}
	_, b, rec, _ := newTestPipelineWith(t, pos)
	ctx := context.Background()

	if err := b.Publish(ctx, ev("BTCUSDT", domain.DirectionBuy, 1)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return pos.count() == 1 })
	waitFor(t, 2*time.Second, func() bool { return rec.count() == 1 })

	if err := b.Publish(ctx, ev("BTCUSDT", domain.DirectionSell, 2)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return pos.count() == 2 })
}

func TestProcessorBlocksPositionWhenSessionStopped(t *testing.T) {
	pos := &recordingPosition{}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	b := bus.New(32)
	eval := evaluator.New(st)
	rec := &recordingNotifier{}
	sess := session.New()
	p := New(b, st, eval, rec, pos, sess, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	t.Cleanup(cancel)

	if err := b.Publish(ctx, ev("BTCUSDT", domain.DirectionBuy, 1)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return rec.count() == 1 })
	time.Sleep(150 * time.Millisecond)
	if pos.count() != 0 {
		t.Errorf("sesión detenida abrió %d posiciones, want 0", pos.count())
	}
	if sess.Signals() != 1 {
		t.Errorf("señales registradas %d, want 1", sess.Signals())
	}

	// Iniciar sesión: ahora la señal sí debe llegar al controlador de posición.
	sess.Start()
	if err := b.Publish(ctx, ev("BTCUSDT", domain.DirectionSell, 2)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return pos.count() == 1 })
}
