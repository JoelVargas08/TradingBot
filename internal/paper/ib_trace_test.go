package paper

import (
	"context"
	"sync"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/ibeval"
)

type fakeIBTracer struct {
	mu        sync.Mutex
	events    []ibeval.PaperEvent
	orderEval map[string]string
	posEval   map[int64]string
}

func newFakeIBTracer() *fakeIBTracer {
	return &fakeIBTracer{orderEval: map[string]string{}, posEval: map[int64]string{}}
}

func (f *fakeIBTracer) RecordPaperEvent(ctx context.Context, ev ibeval.PaperEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	if ev.OrderClientID != "" {
		f.orderEval[ev.OrderClientID] = ev.EvaluationKey
	}
	if ev.PositionID > 0 {
		f.posEval[ev.PositionID] = ev.EvaluationKey
	}
	return nil
}

func (f *fakeIBTracer) EvaluationKeyForOrder(ctx context.Context, clientID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if k, ok := f.orderEval[clientID]; ok {
		return k, nil
	}
	return "", domain.ErrNotFound
}

func (f *fakeIBTracer) EvaluationKeyForPosition(ctx context.Context, positionID int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if k, ok := f.posEval[positionID]; ok {
		return k, nil
	}
	return "", domain.ErrNotFound
}

func (f *fakeIBTracer) byKind(kind string) []ibeval.PaperEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ibeval.PaperEvent
	for _, ev := range f.events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func ibSignal(dir domain.Direction, price float64, evalKey, pubKey string) domain.SignalEvent {
	ev := domain.SignalEvent{
		StrategyID: "ib_investing", Symbol: "BTCUSDT", Timeframe: "15m",
		Direction: dir, Price: price, BarTS: time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC),
		Meta: map[string]any{},
	}
	ibeval.SetMetaString(ev.Meta, ibeval.MetaEvaluationKey, evalKey)
	ibeval.SetMetaString(ev.Meta, ibeval.MetaOutboxKey, pubKey)
	return ev
}

func TestIBPaperTwoLimitsLinkAllOrders(t *testing.T) {
	e, _ := newTestEngine()
	tracer := newFakeIBTracer()
	e.SetTracer(tracer)
	ctx := context.Background()
	ev := ibSignal(domain.DirectionBuy, 100, "eval-two", "pub-two")
	ev.Meta["entry_zone_low"] = 95.0
	ev.Meta["entry_zone_high"] = 105.0
	if err := e.OnSignal(ctx, ev); err != nil {
		t.Fatal(err)
	}
	armed := tracer.byKind("armed")
	if len(armed) != 2 {
		t.Fatalf("órdenes armadas = %d, want 2", len(armed))
	}
	for _, a := range armed {
		if a.EvaluationKey != "eval-two" || a.OrderClientID == "" {
			t.Fatalf("armado sin enlace: %+v", a)
		}
	}
	if len(tracer.orderEval) != 2 {
		t.Fatalf("enlaces de orden = %d, want 2", len(tracer.orderEval))
	}

	// Una vela posterior toca solo el primer límite (105) del LONG.
	k := domain.Kline{
		Symbol: "BTCUSDT", Timeframe: "15m", Start: ev.BarTS.Add(15 * time.Minute),
		Open: 104, High: 106, Low: 100, Close: 103, Volume: 10, Closed: true,
	}
	e.OnCandle(ctx, k)
	opened := tracer.byKind("opened")
	if len(opened) != 1 || opened[0].PositionID == 0 || opened[0].EvaluationKey != "eval-two" {
		t.Fatalf("apertura desde límite mal enlazada: %+v", opened)
	}
	if got := tracer.byKind("filled"); len(got) != 1 || got[0].OrderClientID == "" {
		t.Fatalf("ejecución de límite sin orden: %+v", got)
	}
}

func TestIBPaperImmediateLinksPosition(t *testing.T) {
	e, _ := newTestEngine()
	tracer := newFakeIBTracer()
	e.SetTracer(tracer)
	ctx := context.Background()
	if err := e.OnSignal(ctx, ibSignal(domain.DirectionBuy, 100, "eval-market", "pub-market")); err != nil {
		t.Fatal(err)
	}
	opened := tracer.byKind("opened")
	if len(opened) != 1 || opened[0].PositionID == 0 || opened[0].EvaluationKey != "eval-market" {
		t.Fatalf("apertura de mercado mal enlazada: %+v", opened)
	}
}

func TestIBPaperCloseLinksOpeningEvaluation(t *testing.T) {
	e, _ := newTestEngine()
	tracer := newFakeIBTracer()
	e.SetTracer(tracer)
	ctx := context.Background()
	if err := e.OnSignal(ctx, ibSignal(domain.DirectionBuy, 100, "eval-open", "pub-open")); err != nil {
		t.Fatal(err)
	}
	// La señal contraria trae otra evaluación; el cierre debe quedar ligado a
	// la evaluación de apertura, no a la que provoca el cierre.
	closing := ibSignal(domain.DirectionSell, 103, "eval-close", "pub-close")
	closing.BarTS = closing.BarTS.Add(15 * time.Minute)
	if err := e.OnSignal(ctx, closing); err != nil {
		t.Fatal(err)
	}
	closed := tracer.byKind("closed")
	if len(closed) != 1 {
		t.Fatalf("cierres registrados = %d, want 1", len(closed))
	}
	if closed[0].EvaluationKey != "eval-open" {
		t.Fatalf("cierre ligado a %q, want eval-open", closed[0].EvaluationKey)
	}
	if closed[0].PositionID == 0 {
		t.Fatalf("cierre sin posición: %+v", closed[0])
	}
}
