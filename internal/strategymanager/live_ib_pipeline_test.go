package strategymanager

import (
	"context"
	"sync"
	"testing"
	"time"

	"tradingview-bot/internal/bus"
	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/evaluator"
	"tradingview-bot/internal/ibeval"
	"tradingview-bot/internal/paper"
	"tradingview-bot/internal/processor"
	"tradingview-bot/internal/store"
)

type pipelineNotifier struct {
	mu     sync.Mutex
	events []domain.SignalEvent
}

func (n *pipelineNotifier) Notify(ctx context.Context, ev domain.SignalEvent) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, ev)
	return nil
}

func (n *pipelineNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.events)
}

type pipelineRisk struct{}

func (pipelineRisk) EvaluateSignal(ctx context.Context, ev domain.SignalEvent) (domain.Decision, error) {
	return domain.Decision{
		Allowed: true, Side: ev.Direction, EntryPrice: ev.Price,
		StopLoss: ev.Price * 0.98, TakeProfit: ev.Price * 1.04,
		Quantity: 0.1, RiskAmount: 10,
	}, nil
}

func waitPipeline(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timeout esperando el pipeline IB")
}

func pipelineMetaFloat(meta map[string]any, key string) (float64, bool) {
	v, ok := meta[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// TestIBPipelineFromCandleToPaperPosition cubre vela -> evaluación MTF ->
// evaluación+Outbox -> worker -> processor -> recibo -> PAPER -> enlace.
func TestIBPipelineFromCandleToPaperPosition(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := loadPhase1Fixture(t)
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	b := bus.New(32)
	notifier := &pipelineNotifier{}
	paperEngine := paper.New(st, pipelineRisk{}, paper.Config{}).SetTracer(st)
	proc := processor.New(b, st, evaluator.New(st), notifier, paperEngine, nil, 100)
	proc.SetIBReceipts(st)
	proc.Start(ctx)

	engine := NewLiveEngine(newPhase1CandleSource(f.main, f.entry, f.confirm), &phase1StrategyStore{st: f.strategy}, b)
	engine.SetIBRecorder(st)
	if err := engine.SetSelection(f.strategy.ID, "TEST", "15m"); err != nil {
		t.Fatal(err)
	}
	if err := engine.OnCandle(ctx, f.bar); err != nil {
		t.Fatal(err)
	}
	worker := ibeval.NewOutboxWorker(st, b, ibeval.WorkerConfig{PollInterval: 5 * time.Millisecond})
	n, err := worker.Drain(ctx)
	if err != nil || n != 1 {
		t.Fatalf("Drain = %d, %v", n, err)
	}
	waitPipeline(t, 3*time.Second, func() bool {
		sum, err := st.IBSummary(ctx, f.strategy.ID)
		return err == nil && sum.ReceiptsDone == 1 && sum.OutboxAcked == 1
	})
	if notifier.count() != 1 {
		t.Fatalf("notificaciones = %d, want 1", notifier.count())
	}

	open, err := st.OpenPositions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) == 0 {
		// La señal IB trae zona de entrada: simular una vela que toca solo el
		// primer límite y verificar la apertura PAPER real.
		sig, err := st.LastSignal(ctx, f.strategy.ID, "TEST", "15m")
		if err != nil {
			t.Fatal(err)
		}
		low, hasLow := pipelineMetaFloat(sig.Meta, "entry_zone_low")
		high, hasHigh := pipelineMetaFloat(sig.Meta, "entry_zone_high")
		if !hasLow || !hasHigh || low <= 0 || high <= low {
			t.Fatalf("sin posición ni zona de entrada: %+v", sig.Meta)
		}
		k := domain.Kline{
			Symbol: "TEST", Timeframe: "15m", Start: f.bar.Start.Add(15 * time.Minute),
			Open: high, Close: high, Closed: true, Volume: 100,
		}
		if sig.Direction == domain.DirectionBuy {
			k.Low = (low + high) / 2
			k.High = high + 0.5
		} else {
			k.High = (low + high) / 2
			k.Low = low - 0.5
		}
		paperEngine.OnCandle(ctx, k)
		open, err = st.OpenPositions(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(open) != 1 {
		t.Fatalf("posiciones PAPER abiertas = %d, want 1", len(open))
	}
	events, err := st.RecentIBPaperEventsForStrategy(ctx, f.strategy.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	opened := false
	for _, ev := range events {
		if ev.Kind == "opened" && ev.PositionID == open[0].ID {
			opened = true
		}
	}
	if !opened {
		t.Fatalf("no hay evento opened enlazado a la posición %d: %+v", open[0].ID, events)
	}
	if key, err := st.EvaluationKeyForPosition(ctx, open[0].ID); err != nil || key == "" {
		t.Fatalf("posición sin enlace de evaluación: %q, %v", key, err)
	}
}
