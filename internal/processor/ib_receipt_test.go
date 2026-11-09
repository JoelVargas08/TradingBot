package processor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"tradingview-bot/internal/bus"
	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/evaluator"
	"tradingview-bot/internal/ibeval"
	"tradingview-bot/internal/store"
)

type flakyPosition struct {
	mu    sync.Mutex
	fails int
	calls int
}

func (f *flakyPosition) OnSignal(ctx context.Context, ev domain.SignalEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.fails {
		return errors.New("fallo temporal PAPER")
	}
	return nil
}

func (f *flakyPosition) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.fails
}

func newIBPipeline(t *testing.T, pos domain.PositionController) (*Processor, *recordingNotifier, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	rec := &recordingNotifier{}
	p := New(bus.New(8), st, evaluator.New(st), rec, pos, &testGate{active: true}, 100)
	p.SetIBReceipts(st)
	return p, rec, st
}

func ibEvent(bar time.Time) domain.SignalEvent {
	return domain.SignalEvent{
		StrategyID: "ib_investing", Symbol: "BTCUSDT", Timeframe: "15m",
		Direction: domain.DirectionBuy, Price: 60000, BarTS: bar, ReceivedAt: bar,
	}
}

func queueIBEvaluation(t *testing.T, st *store.Store, ev domain.SignalEvent, status string, finger string) (string, string) {
	t.Helper()
	evalKey := ibeval.EvaluationKey(ev.StrategyID, ev.Symbol, ev.Timeframe, ev.BarTS, finger)
	pubKey := ibeval.PublicationKey(ev.StrategyID, ev.Symbol, ev.Timeframe, ev.BarTS)
	var msg *ibeval.OutboxMessage
	if status == ibeval.EvalAccepted {
		var err error
		msg, err = ibeval.NewOutboxMessage(ev, evalKey, pubKey, ev.BarTS)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := st.RecordEvaluation(context.Background(), ibeval.EvaluationRecord{
		EvaluationKey: evalKey, PublicationKey: pubKey, StrategyID: ev.StrategyID,
		Symbol: ev.Symbol, Timeframe: ev.Timeframe, BarTS: ev.BarTS,
		ModelFingerprint: finger, Status: status, Trace: map[string]any{"status": status},
	}, msg)
	if err != nil {
		t.Fatal(err)
	}
	return evalKey, pubKey
}

func TestIBReceiptPreventsDuplicateDeliveryEffects(t *testing.T) {
	ctx := context.Background()
	pos := &recordingPosition{}
	p, rec, st := newIBPipeline(t, pos)
	ev := ibEvent(time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC))
	evalKey, pubKey := queueIBEvaluation(t, st, ev, ibeval.EvalAccepted, "fp")
	ev.Meta = map[string]any{
		ibeval.MetaEvaluationKey: evalKey,
		ibeval.MetaOutboxKey:     pubKey,
	}

	p.handle(ctx, ev)
	p.handle(ctx, ev)

	if rec.count() != 1 {
		t.Fatalf("notificaciones = %d, want 1", rec.count())
	}
	if pos.count() != 1 {
		t.Fatalf("PAPER = %d, want 1", pos.count())
	}
	sum, err := st.IBSummary(ctx, ev.StrategyID)
	if err != nil {
		t.Fatal(err)
	}
	if sum.ReceiptsDone != 1 || sum.OutboxAcked != 1 {
		t.Fatalf("estado persistente inesperado: %+v", sum)
	}
}

func TestIBReceiptRecoversPaperAfterExpiredLease(t *testing.T) {
	ctx := context.Background()
	pos := &recordingPosition{}
	p, rec, st := newIBPipeline(t, pos)
	ev := ibEvent(time.Now().UTC().Add(-10 * time.Minute))
	key := ev.IdempotencyKey()
	ev.Meta = map[string]any{
		ibeval.MetaEvaluationKey: "eval-crash",
		ibeval.MetaOutboxKey:     "pub-crash",
	}
	if _, err := st.SaveSignalWithReceipt(ctx, ev, true, true, time.Now().UTC().Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimReceiptPaper(ctx, key, time.Now().UTC().Add(-10*time.Minute), time.Minute)
	if err != nil || !claimed {
		t.Fatalf("preparando lease vencido: %v, %v", claimed, err)
	}

	p.handle(ctx, ev)
	p.handle(ctx, ev)

	if rec.count() != 1 {
		t.Fatalf("notificaciones = %d, want 1", rec.count())
	}
	if pos.count() != 1 {
		t.Fatalf("PAPER = %d, want 1", pos.count())
	}
	sum, err := st.IBSummary(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if sum.ReceiptsDone != 1 || sum.ReceiptsProcess != 0 {
		t.Fatalf("recibo no terminó: %+v", sum)
	}
}

func TestIBPaperFailureRetriesWithoutRenotifying(t *testing.T) {
	ctx := context.Background()
	pos := &flakyPosition{fails: 1}
	p, rec, st := newIBPipeline(t, pos)
	ev := ibEvent(time.Date(2026, 1, 2, 6, 0, 0, 0, time.UTC))
	evalKey, pubKey := queueIBEvaluation(t, st, ev, ibeval.EvalAccepted, "fp")
	ev.Meta = map[string]any{
		ibeval.MetaEvaluationKey: evalKey,
		ibeval.MetaOutboxKey:     pubKey,
	}

	p.handle(ctx, ev)
	p.handle(ctx, ev)

	calls, _ := pos.counts()
	if calls != 2 {
		t.Fatalf("intentos PAPER = %d, want 2", calls)
	}
	if rec.count() != 1 {
		t.Fatalf("notificaciones = %d, want 1", rec.count())
	}
	sum, err := st.IBSummary(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if sum.ReceiptsDone != 1 || sum.OutboxAcked != 1 {
		t.Fatalf("no se completó el reintento: %+v", sum)
	}
}

func TestIBRejectedSignalDoesNotNotifyOrOpenPaper(t *testing.T) {
	ctx := context.Background()
	pos := &recordingPosition{}
	p, rec, st := newIBPipeline(t, pos)
	ev := ibEvent(time.Date(2026, 1, 2, 7, 0, 0, 0, time.UTC))

	prev := ev
	prev.BarTS = ev.BarTS.Add(-15 * time.Minute)
	if err := st.SaveSignal(ctx, prev); err != nil {
		t.Fatal(err)
	}
	evalKey, pubKey := queueIBEvaluation(t, st, ev, ibeval.EvalRejected, "fp")
	ev.Meta = map[string]any{
		ibeval.MetaEvaluationKey: evalKey,
		ibeval.MetaOutboxKey:     pubKey,
	}

	p.handle(ctx, ev)

	if rec.count() != 0 || pos.count() != 0 {
		t.Fatalf("señal rechazada notificó=%d PAPER=%d", rec.count(), pos.count())
	}
}
