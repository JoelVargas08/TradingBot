package strategymanager

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"tradingview-bot/internal/ibeval"
	"tradingview-bot/internal/investingbulls"
)

type auditEventRecorder struct {
	mu     sync.Mutex
	events []ibeval.EngineEvent
}

func (r *auditEventRecorder) RecordEngineEvent(_ context.Context, ev ibeval.EngineEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

type failingRecorder struct{ err error }

func (f failingRecorder) RecordEvaluation(context.Context, ibeval.EvaluationRecord, *ibeval.OutboxMessage) (bool, bool, error) {
	return false, false, f.err
}

func traceOf(t *testing.T, rec ibeval.EvaluationRecord) investingbulls.PipelineTrace {
	t.Helper()
	tr, ok := rec.Trace.(investingbulls.PipelineTrace)
	if !ok {
		t.Fatalf("la traza persistida no es PipelineTrace: %T", rec.Trace)
	}
	return tr
}

// La traza persistida lleva los identificadores que la correlacionan con la
// evaluación, la señal y (vía Outbox/recibos) la operación PAPER.
func TestLiveTraceCarriesCorrelationIDsAndFoundations(t *testing.T) {
	f := loadPhase1Fixture(t)
	rec := &phase2Recorder{}
	engine := NewLiveEngine(newPhase1CandleSource(f.main, f.entry, f.confirm), &phase1StrategyStore{st: f.strategy}, &phase1Publisher{})
	engine.SetIBRecorder(rec)
	if err := engine.SetSelection(f.strategy.ID, "TEST", "15m"); err != nil {
		t.Fatal(err)
	}
	if err := engine.OnCandle(context.Background(), f.bar); err != nil {
		t.Fatal(err)
	}
	if len(rec.records) != 1 || rec.records[0].Status != ibeval.EvalAccepted {
		t.Fatalf("se esperaba una evaluación aceptada: %+v", rec.records)
	}
	got := rec.records[0]
	tr := traceOf(t, got)
	if tr.EvaluationKey != got.EvaluationKey || tr.SignalKey != got.SignalKey || tr.SignalKey == "" || tr.ModelVersion != got.ModelVersion {
		t.Fatalf("IDs de correlación ausentes: trace=%q/%q/%q rec=%q/%q/%q",
			tr.EvaluationKey, tr.SignalKey, tr.ModelVersion, got.EvaluationKey, got.SignalKey, got.ModelVersion)
	}
	approved := map[string]bool{}
	for _, s := range tr.Stages {
		if s.Outcome == investingbulls.OutcomeApproved {
			approved[s.Foundation] = true
		}
	}
	for _, f := range []string{investingbulls.FoundationContext1h, investingbulls.FoundationStructure15m,
		investingbulls.FoundationFibonacci, investingbulls.FoundationConfluence, investingbulls.FoundationTradePlan} {
		if !approved[f] {
			t.Errorf("fundamento %s no aprobado en una señal aceptada:\n%s", f, tr.Summary())
		}
	}
	if tr.Confluence == nil || tr.Confluence.Winner == 0 || len(tr.Confluence.Zones) == 0 {
		t.Fatalf("desglose de confluencia ausente: %+v", tr.Confluence)
	}
	st := engine.Status()
	if st.Evaluations != 1 || st.Accepted != 1 || st.LastEvaluationKey != got.EvaluationKey {
		t.Fatalf("estado del motor: %+v", st)
	}
}

// Una vela que llega mucho después de su cierre (recuperada tras una caída)
// se registra como dato atrasado y no genera señal ni Outbox.
func TestStaleBarIsRecordedButNotEvaluated(t *testing.T) {
	f := loadPhase1Fixture(t)
	rec := &phase2Recorder{}
	pub := &phase1Publisher{}
	events := &auditEventRecorder{}
	engine := NewLiveEngine(newPhase1CandleSource(f.main, f.entry, f.confirm), &phase1StrategyStore{st: f.strategy}, pub)
	engine.SetIBRecorder(rec).SetMaxBarLag(15 * time.Minute).SetEngineEventRecorder(events)
	engine.now = func() time.Time { return f.bar.Start.Add(15*time.Minute + 2*time.Hour) }
	if err := engine.SetSelection(f.strategy.ID, "TEST", "15m"); err != nil {
		t.Fatal(err)
	}
	if err := engine.OnCandle(context.Background(), f.bar); err != nil {
		t.Fatal(err)
	}
	if len(rec.records) != 1 || rec.msgs[0] != nil || len(pub.events) != 0 {
		t.Fatalf("la vela atrasada no debe publicar: records=%d msg=%v pub=%d", len(rec.records), rec.msgs, len(pub.events))
	}
	got := rec.records[0]
	tr := traceOf(t, got)
	if got.Status != ibeval.EvalRejected || got.Reason != "vela_atrasada" || tr.FinalCategory != investingbulls.CategoryStaleData {
		t.Fatalf("registro de vela atrasada: %+v / %s", got, tr.FinalCategory)
	}
	if tr.EvaluationKey != got.EvaluationKey {
		t.Fatal("la traza de la vela atrasada debe correlacionarse con su evaluación")
	}
	if st := engine.Status(); st.StaleBars != 1 || st.Rejected != 1 {
		t.Fatalf("contadores del motor: %+v", st)
	}

	// Dentro del margen sí se evalúa normalmente.
	rec2 := &phase2Recorder{}
	engine2 := NewLiveEngine(newPhase1CandleSource(f.main, f.entry, f.confirm), &phase1StrategyStore{st: f.strategy}, &phase1Publisher{})
	engine2.SetIBRecorder(rec2).SetMaxBarLag(15 * time.Minute)
	engine2.now = func() time.Time { return f.bar.Start.Add(15*time.Minute + 20*time.Second) }
	_ = engine2.SetSelection(f.strategy.ID, "TEST", "15m")
	if err := engine2.OnCandle(context.Background(), f.bar); err != nil {
		t.Fatal(err)
	}
	if len(rec2.records) != 1 || rec2.records[0].Status != ibeval.EvalAccepted {
		t.Fatalf("una vela puntual debe evaluarse: %+v", rec2.records)
	}
}

// Si la traza no se puede persistir, el fallo es observable (no se simula que
// la auditoría está completa).
func TestPersistenceFailureIsObservable(t *testing.T) {
	f := loadPhase1Fixture(t)
	events := &auditEventRecorder{}
	engine := NewLiveEngine(newPhase1CandleSource(f.main, f.entry, f.confirm), &phase1StrategyStore{st: f.strategy}, &phase1Publisher{})
	engine.SetIBRecorder(failingRecorder{err: errors.New("database is locked")}).SetEngineEventRecorder(events)
	_ = engine.SetSelection(f.strategy.ID, "TEST", "15m")
	if err := engine.OnCandle(context.Background(), f.bar); err == nil {
		t.Fatal("un fallo de persistencia debe propagarse")
	}
	errs := engine.RecentErrors(5)
	if len(errs) != 1 || errs[0].Kind != ibeval.EngineEventPersistError || errs[0].EvaluationKey == "" {
		t.Fatalf("error del motor no registrado: %+v", errs)
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	if len(events.events) != 1 || events.events[0].Kind != ibeval.EngineEventPersistError {
		t.Fatalf("evento de error no persistido: %+v", events.events)
	}
	if st := engine.Status(); st.PersistErrors != 1 || st.LastError == nil {
		t.Fatalf("estado del motor sin el error: %+v", st)
	}
}
