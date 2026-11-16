package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"tradingview-bot/internal/ibeval"
	"tradingview-bot/internal/ingest"
)

// La migración de ops_events se aplica sobre una base existente (como
// data/bot.db) sin tocar tablas ni datos previos, y es idempotente.
func TestOpsEventsMigrationPreservesExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, _, err := s.RecordEvaluation(ctx, ibeval.EvaluationRecord{EvaluationKey: "ev-1", PublicationKey: "pub-1",
		StrategyID: "ib", Symbol: "BTCUSDT", Timeframe: "15m", BarTS: time.UnixMilli(1000), Status: ibeval.EvalRejected,
		Reason: "confluencia_insuficiente", Trace: map[string]any{"x": 1}}, nil); err != nil {
		t.Fatal(err)
	}
	// Simular una base anterior a ops_events.
	if _, err := s.db.Exec(`DROP TABLE ops_events`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ib_evaluations WHERE evaluation_key='ev-1'`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("evaluación previa perdida: n=%d err=%v", n, err)
		}
		var name string
		if err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='ops_events'`).Scan(&name); err != nil {
			t.Fatalf("ops_events no creada: %v", err)
		}
		s.Close()
	}
}

func TestOpsEventsDedupeFilterAndPrune(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	ev := ingest.IngestEvent{Key: "BTCUSDT|1h|gap", Symbol: "BTCUSDT", Timeframe: "1h", Kind: ingest.EventBackfillFailed,
		Severity: ingest.SeverityFail, HTTPStatus: 400, Code: -1142, Message: "Parameter 'startTime' is invalid.",
		From: now.Add(-2 * time.Hour), To: now.Add(-time.Hour), Expected: 2, At: now}
	for i := 0; i < 3; i++ { // reintentos del mismo suceso
		if err := s.RecordIngestEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordIngestEvent(ctx, ingest.IngestEvent{Key: "conn", Kind: ingest.EventWSConnected, Severity: ingest.SeverityInfo,
		Symbol: "BTCUSDT", Timeframe: "1h", At: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordEngineEvent(ctx, ibeval.EngineEvent{Key: "ev-9", Kind: ibeval.EngineEventPersistError, Severity: "FAIL",
		StrategyID: "ib", EvaluationKey: "ev-9", Message: "database is locked", At: now.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}

	all, err := s.RecentOpsEvents(ctx, OpsFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("eventos = %d err=%v, want 3 (sin duplicados por reintento)", len(all), err)
	}
	if all[0].Component != OpsComponentIBEngine || all[0].EvaluationKey != "ev-9" {
		t.Fatalf("orden/correlación incorrectos: %+v", all[0])
	}
	warn, _ := s.RecentOpsEvents(ctx, OpsFilter{MinSeverity: "WARN"})
	if len(warn) != 2 {
		t.Fatalf("WARN+FAIL = %d, want 2", len(warn))
	}
	weex, _ := s.RecentOpsEvents(ctx, OpsFilter{Component: OpsComponentWeex, Kinds: []string{ingest.EventBackfillFailed}})
	if len(weex) != 1 || weex[0].Code != -1142 || weex[0].HTTPStatus != 400 || weex[0].Expected != 2 || !weex[0].From.Equal(ev.From) {
		t.Fatalf("evento WEEX mal persistido: %+v", weex)
	}

	n, err := s.PruneOps(ctx, now.Add(31*24*time.Hour), 30*24*time.Hour, 0)
	if err != nil || n != 3 {
		t.Fatalf("poda = %d err=%v, want 3 (todo anterior a la retención)", n, err)
	}
	for i := 0; i < 5; i++ {
		_ = s.RecordOpsEvent(ctx, OpsEvent{Key: fmt.Sprintf("k%d", i), Component: "x", Kind: "y", CreatedAt: now.Add(40 * 24 * time.Hour).Add(time.Duration(i) * time.Second)})
	}
	if _, err := s.PruneOps(ctx, now.Add(40*24*time.Hour), 30*24*time.Hour, 3); err != nil {
		t.Fatal(err)
	}
	left, _ := s.RecentOpsEvents(ctx, OpsFilter{Limit: 100})
	if len(left) != 3 {
		t.Fatalf("tope de filas: quedan %d, want 3", len(left))
	}
}

func TestOpsEventRequiresKey(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RecordOpsEvent(context.Background(), OpsEvent{Component: "weex", Kind: "x"}); err == nil {
		t.Fatal("un evento sin clave estable debe rechazarse")
	}
}

func TestIBEvaluationLookupAndReasonCounts(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	base := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	for i, reason := range []string{"confluencia_insuficiente", "confluencia_insuficiente", "sin_setup_15m"} {
		key := fmt.Sprintf("ib-1|BTCUSDT|15m|%d|abcdef%d", base.Add(time.Duration(i)*15*time.Minute).UnixMilli(), i)
		if _, _, err := s.RecordEvaluation(ctx, ibeval.EvaluationRecord{EvaluationKey: key, PublicationKey: key,
			StrategyID: "ib-1", Symbol: "BTCUSDT", Timeframe: "15m", BarTS: base.Add(time.Duration(i) * 15 * time.Minute),
			Status: ibeval.EvalRejected, Reason: reason, Trace: map[string]any{"final_reason": reason}, CreatedAt: base.Add(time.Duration(i) * time.Minute)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	r, ok, err := s.IBEvaluationByPrefix(ctx, "abcdef2")
	if err != nil || !ok || r.Reason != "sin_setup_15m" {
		t.Fatalf("búsqueda por fragmento: ok=%v err=%v r=%+v", ok, err, r)
	}
	if _, ok, _ := s.IBEvaluationByPrefix(ctx, "no-existe"); ok {
		t.Fatal("un fragmento inexistente no debe resolver")
	}
	counts, err := s.IBReasonCounts(ctx, "ib-1", time.Time{})
	if err != nil || len(counts) != 2 || counts[0].Reason != "confluencia_insuficiente" || counts[0].Count != 2 {
		t.Fatalf("agrupación por motivo: %+v err=%v", counts, err)
	}
}

var _ = sql.ErrNoRows
