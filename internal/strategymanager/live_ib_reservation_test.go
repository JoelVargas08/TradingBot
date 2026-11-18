package strategymanager

import (
	"context"
	"testing"
	"time"

	"tradingview-bot/internal/ibeval"
	"tradingview-bot/internal/store"
)

// TestLiveEngineSkipsReservedBar demuestra que SQLite es la autoridad: una
// barra ya reservada (por ejemplo, en curso o interrumpida tras un reinicio)
// no se reevalúa ni produce una segunda publicación.
func TestLiveEngineSkipsReservedBar(t *testing.T) {
	f := loadPhase1Fixture(t)
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	evalKey := ibeval.EvaluationKey(f.strategy.ID, "TEST", "15m", f.bar.Start, ibeval.Fingerprint(f.strategy.Spec))
	if started, err := st.BeginEvaluation(ctx, ibeval.EvaluationRecord{
		EvaluationKey: evalKey, PublicationKey: ibeval.PublicationKey(f.strategy.ID, "TEST", "15m", f.bar.Start),
		StrategyID: f.strategy.ID, Symbol: "TEST", Timeframe: "15m", BarTS: f.bar.Start,
		ModelFingerprint: ibeval.Fingerprint(f.strategy.Spec), Status: ibeval.EvalRunning, CreatedAt: now,
	}); err != nil || !started {
		t.Fatalf("reserva previa = %v, %v", started, err)
	}
	if n, err := st.MarkInterrupted(ctx, now.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("MarkInterrupted = %d, %v", n, err)
	}

	pub := &phase1Publisher{}
	engine := NewLiveEngine(newPhase1CandleSource(f.main, f.entry, f.confirm), &phase1StrategyStore{st: f.strategy}, pub)
	engine.SetIBRecorder(st)
	if err := engine.SetSelection(f.strategy.ID, "TEST", "15m"); err != nil {
		t.Fatal(err)
	}
	if err := engine.OnCandle(ctx, f.bar); err != nil {
		t.Fatal(err)
	}
	if len(pub.events) != 0 {
		t.Fatalf("una barra interrumpida no debe publicar: %d eventos", len(pub.events))
	}
	sum, err := st.IBSummary(ctx, f.strategy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Evaluations != 1 || sum.Accepted != 0 || sum.OutboxPending != 0 {
		t.Fatalf("la reserva interrumpida produjo efectos: %+v", sum)
	}
}
