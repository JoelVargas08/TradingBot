package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/ibeval"
)

func TestIBMigrationIsIdempotentAndPreservesLegacyTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE legacy_marker (id INTEGER PRIMARY KEY, note TEXT NOT NULL)`,
		`INSERT INTO legacy_marker (id, note) VALUES (7, 'conservar')`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("preparando base antigua: %v", err)
		}
	}
	raw.Close()

	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open iteración %d: %v", i, err)
		}
		var note string
		if err := s.db.QueryRow(`SELECT note FROM legacy_marker WHERE id = 7`).Scan(&note); err != nil {
			s.Close()
			t.Fatalf("tabla previa perdida: %v", err)
		}
		if note != "conservar" {
			s.Close()
			t.Fatalf("dato previo alterado: %q", note)
		}
		for _, table := range []string{
			"ib_evaluations", "ib_outbox", "ib_signal_receipts",
			"ib_order_links", "ib_position_links", "ib_paper_events",
		} {
			var name string
			if err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
				s.Close()
				t.Fatalf("tabla %s ausente: %v", table, err)
			}
		}
		s.Close()
	}
}

func TestIBEvaluationAndOutboxTransaction(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Add(time.Second)
	bar := now
	evalKey := ibeval.EvaluationKey("ib_investing", "BTCUSDT", "15m", bar, "fp-1")
	pubKey := ibeval.PublicationKey("ib_investing", "BTCUSDT", "15m", bar)
	ev := domain.SignalEvent{
		StrategyID: "ib_investing", Symbol: "BTCUSDT", Timeframe: "15m",
		Direction: domain.DirectionBuy, Price: 60000, BarTS: bar,
	}
	msg, err := ibeval.NewOutboxMessage(ev, evalKey, pubKey, bar)
	if err != nil {
		t.Fatal(err)
	}
	rec := ibeval.EvaluationRecord{
		EvaluationKey: evalKey, PublicationKey: pubKey,
		StrategyID: "ib_investing", Symbol: "BTCUSDT", Timeframe: "15m", BarTS: bar,
		ModelFingerprint: "fp-1", Status: ibeval.EvalAccepted, Trace: map[string]any{"ok": true},
	}
	created, enqueued, err := s.RecordEvaluation(ctx, rec, msg)
	if err != nil {
		t.Fatal(err)
	}
	if !created || !enqueued {
		t.Fatalf("primera evaluación created=%v enqueued=%v", created, enqueued)
	}
	created, enqueued, err = s.RecordEvaluation(ctx, rec, msg)
	if err != nil {
		t.Fatal(err)
	}
	if created || enqueued {
		t.Fatalf("reevaluación duplicó registros: created=%v enqueued=%v", created, enqueued)
	}

	sum, err := s.IBSummary(ctx, "ib_investing")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Evaluations != 1 || sum.Accepted != 1 || sum.OutboxPending != 1 {
		t.Fatalf("resumen inesperado: %+v", sum)
	}

	items, err := s.ClaimOutbox(ctx, now, time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Attempts != 1 || items[0].Key != pubKey {
		t.Fatalf("reclamo inesperado: %+v", items)
	}
	if got, _ := s.ClaimOutbox(ctx, now, time.Minute, 10); len(got) != 0 {
		t.Fatalf("se reclamó dos veces con lease vivo: %+v", got)
	}
	if err := s.UpdateOutbox(ctx, ibeval.OutboxUpdate{
		Key: pubKey, Status: ibeval.OutboxDispatched, Attempts: 1,
		AvailableAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ClaimOutbox(ctx, now.Add(30*time.Second), time.Minute, 10); len(got) != 0 {
		t.Fatalf("se reclamó antes de la visibilidad: %+v", got)
	}
	items, err = s.ClaimOutbox(ctx, now.Add(2*time.Minute), time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Attempts != 2 {
		t.Fatalf("no se recuperó tras la visibilidad: %+v", items)
	}
	if err := s.AckOutbox(ctx, pubKey, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ClaimOutbox(ctx, now.Add(time.Hour), time.Minute, 10); len(got) != 0 {
		t.Fatalf("se reclamó un mensaje acked: %+v", got)
	}
	sum, err = s.IBSummary(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if sum.OutboxAcked != 1 || sum.OutboxPending != 0 {
		t.Fatalf("estado outbox inesperado: %+v", sum)
	}
}

// TestIBReservationInterruptionNeverCreatesOutbox simula una caída entre la
// reserva y el cierre: la recuperación marca `interrupted` y un cierre tardío
// no puede crear señal, Outbox ni efecto PAPER.
func TestIBReservationInterruptionNeverCreatesOutbox(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	bar := now.Add(-time.Minute)
	evalKey := ibeval.EvaluationKey("ib_investing", "BTCUSDT", "15m", bar, "fp-1")
	pubKey := ibeval.PublicationKey("ib_investing", "BTCUSDT", "15m", bar)
	rec := ibeval.EvaluationRecord{
		EvaluationKey: evalKey, PublicationKey: pubKey,
		StrategyID: "ib_investing", Symbol: "BTCUSDT", Timeframe: "15m", BarTS: bar,
		ModelFingerprint: "fp-1", Status: ibeval.EvalRunning, CreatedAt: now,
	}
	started, err := s.BeginEvaluation(ctx, rec)
	if err != nil || !started {
		t.Fatalf("BeginEvaluation = %v, %v", started, err)
	}
	if started, err := s.BeginEvaluation(ctx, rec); err != nil || started {
		t.Fatalf("segunda reserva = %v, %v", started, err)
	}
	if n, err := s.MarkInterrupted(ctx, now.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("MarkInterrupted = %d, %v", n, err)
	}
	ev := domain.SignalEvent{
		StrategyID: "ib_investing", Symbol: "BTCUSDT", Timeframe: "15m",
		Direction: domain.DirectionBuy, Price: 60000, BarTS: bar,
	}
	msg, err := ibeval.NewOutboxMessage(ev, evalKey, pubKey, now)
	if err != nil {
		t.Fatal(err)
	}
	rec.Status = ibeval.EvalAccepted
	rec.Trace = map[string]any{"ok": true}
	if enqueued, err := s.FinishEvaluation(ctx, rec, msg); err != nil || enqueued {
		t.Fatalf("Finish tras interrupted = %v, %v", enqueued, err)
	}
	sum, err := s.IBSummary(ctx, "ib_investing")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Evaluations != 1 || sum.Accepted != 0 || sum.OutboxPending != 0 {
		t.Fatalf("una evaluación interrumpida no debe producir efectos: %+v", sum)
	}
}

// TestIBReservationAcceptedIsAtomic comprueba que una reserva cerrada como
// aceptada y su Outbox se confirman juntos.
func TestIBReservationAcceptedIsAtomic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	bar := now.Add(-time.Minute)
	evalKey := ibeval.EvaluationKey("ib_investing", "BTCUSDT", "15m", bar, "fp-2")
	pubKey := ibeval.PublicationKey("ib_investing", "BTCUSDT", "15m", bar)
	rec := ibeval.EvaluationRecord{
		EvaluationKey: evalKey, PublicationKey: pubKey,
		StrategyID: "ib_investing", Symbol: "BTCUSDT", Timeframe: "15m", BarTS: bar,
		ModelFingerprint: "fp-2", Status: ibeval.EvalRunning, CreatedAt: now,
	}
	if started, err := s.BeginEvaluation(ctx, rec); err != nil || !started {
		t.Fatalf("BeginEvaluation = %v, %v", started, err)
	}
	ev := domain.SignalEvent{
		StrategyID: "ib_investing", Symbol: "BTCUSDT", Timeframe: "15m",
		Direction: domain.DirectionBuy, Price: 60000, BarTS: bar,
	}
	msg, err := ibeval.NewOutboxMessage(ev, evalKey, pubKey, now)
	if err != nil {
		t.Fatal(err)
	}
	rec.Status = ibeval.EvalAccepted
	rec.SignalKey = ev.IdempotencyKey()
	rec.Trace = map[string]any{"ok": true}
	if enqueued, err := s.FinishEvaluation(ctx, rec, msg); err != nil || !enqueued {
		t.Fatalf("FinishEvaluation = %v, %v", enqueued, err)
	}
	sum, err := s.IBSummary(ctx, "ib_investing")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Accepted != 1 || sum.OutboxPending != 1 {
		t.Fatalf("cierre aceptado incompleto: %+v", sum)
	}
}

// TestIBConcurrentClaimHasSingleOwner garantiza que dos workers no publican el
// mismo mensaje a la vez: solo un claim incrementa intentos y toma el lease.
func TestIBConcurrentClaimHasSingleOwner(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	bar := now.Add(-time.Minute)
	evalKey := ibeval.EvaluationKey("ib_investing", "BTCUSDT", "15m", bar, "fp-conc")
	pubKey := ibeval.PublicationKey("ib_investing", "BTCUSDT", "15m", bar)
	ev := domain.SignalEvent{
		StrategyID: "ib_investing", Symbol: "BTCUSDT", Timeframe: "15m",
		Direction: domain.DirectionBuy, Price: 60000, BarTS: bar,
	}
	msg, err := ibeval.NewOutboxMessage(ev, evalKey, pubKey, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, enqueued, err := s.RecordEvaluation(ctx, ibeval.EvaluationRecord{
		EvaluationKey: evalKey, PublicationKey: pubKey,
		StrategyID: "ib_investing", Symbol: "BTCUSDT", Timeframe: "15m", BarTS: bar,
		ModelFingerprint: "fp-conc", Status: ibeval.EvalAccepted, Trace: map[string]any{"ok": true},
	}, msg); err != nil || !enqueued {
		t.Fatalf("seed = %v, %v", enqueued, err)
	}

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		total int
	)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := s.ClaimOutbox(ctx, now, time.Minute, 10)
			if err != nil {
				t.Errorf("claim concurrente: %v", err)
				return
			}
			mu.Lock()
			total += len(items)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if total != 1 {
		t.Fatalf("claims concurrentes = %d, want 1", total)
	}
	var attempts int
	if err := s.db.QueryRow(`SELECT attempts FROM ib_outbox WHERE outbox_key = ?`, pubKey).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

// TestIBConcurrentSaveSignalSingleReceipt comprueba que entregas simultáneas de
// la misma señal producen una sola señal y un solo recibo.
func TestIBConcurrentSaveSignalSingleReceipt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ev := domain.SignalEvent{
		StrategyID: "ib_investing", Symbol: "BTCUSDT", Timeframe: "15m",
		Direction: domain.DirectionBuy, Price: 60000, BarTS: now,
		Meta: map[string]any{
			ibeval.MetaEvaluationKey: "eval-conc",
			ibeval.MetaOutboxKey:     "pub-conc",
		},
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
	)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.SaveSignalWithReceipt(ctx, ev, true, true, now)
			if err != nil {
				t.Errorf("recibo concurrente: %v", err)
				return
			}
			if r.Created {
				mu.Lock()
				created++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if created != 1 {
		t.Fatalf("recibos creados = %d, want 1", created)
	}
	var signals, receipts int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM signals WHERE idem_key = ?`, ev.IdempotencyKey()).Scan(&signals); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ib_signal_receipts WHERE signal_key = ?`, ev.IdempotencyKey()).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if signals != 1 || receipts != 1 {
		t.Fatalf("señales=%d recibos=%d, want 1/1", signals, receipts)
	}
}

func TestIBReceiptIdempotenceAndPaperLeaseRecovery(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC)
	ev := domain.SignalEvent{
		StrategyID: "ib_investing", Symbol: "BTCUSDT", Timeframe: "15m",
		Direction: domain.DirectionBuy, Price: 60000, BarTS: now,
		Meta: map[string]any{
			ibeval.MetaEvaluationKey: "eval-1",
			ibeval.MetaOutboxKey:     "pub-1",
		},
	}
	key := ev.IdempotencyKey()
	r, err := s.SaveSignalWithReceipt(ctx, ev, true, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Created || !r.ShouldNotify || r.PaperStatus != ibeval.PaperPending {
		t.Fatalf("recibo inicial inesperado: %+v", r)
	}
	again, err := s.SaveSignalWithReceipt(ctx, ev, true, true, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if again.Created || again.SignalKey != key {
		t.Fatalf("entrega repetida creó otro recibo: %+v", again)
	}

	claimed, err := s.ClaimReceiptPaper(ctx, key, now, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("primer claim = %v, %v", claimed, err)
	}
	if claimed, _ := s.ClaimReceiptPaper(ctx, key, now, time.Minute); claimed {
		t.Fatal("se reclamó PAPER con lease vivo")
	}
	if claimed, err := s.ClaimReceiptPaper(ctx, key, now.Add(2*time.Minute), time.Minute); err != nil || !claimed {
		t.Fatalf("no se recuperó PAPER con lease vencido: %v, %v", claimed, err)
	}
	if err := s.MarkReceiptNotified(ctx, key, now); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishReceiptPaper(ctx, key, ibeval.PaperDone, "", now); err != nil {
		t.Fatal(err)
	}
	if claimed, _ := s.ClaimReceiptPaper(ctx, key, now.Add(time.Hour), time.Minute); claimed {
		t.Fatal("se reclamó PAPER en estado terminal")
	}
	sum, err := s.IBSummary(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if sum.ReceiptsDone != 1 || sum.ReceiptsProcess != 0 {
		t.Fatalf("resumen de recibos inesperado: %+v", sum)
	}
}

func TestIBPaperEventsLinkOrdersAndPositions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 2, 5, 0, 0, 0, time.UTC)
	if err := s.RecordPaperEvent(ctx, ibeval.PaperEvent{
		EvaluationKey: "eval-1", SignalKey: "signal-1", OutboxKey: "pub-1",
		Kind: "order_armed", OrderClientID: "client-1", Price: 60000, Quantity: 0.1,
		Detail: map[string]any{"stop": 59000}, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPaperEvent(ctx, ibeval.PaperEvent{
		EvaluationKey: "eval-1", SignalKey: "signal-1", OutboxKey: "pub-1",
		Kind: "position_opened", PositionID: 9, Price: 60001, Quantity: 0.1, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.EvaluationKeyForOrder(ctx, "client-1"); err != nil || got != "eval-1" {
		t.Fatalf("enlace de orden = %q, %v", got, err)
	}
	if got, err := s.EvaluationKeyForPosition(ctx, 9); err != nil || got != "eval-1" {
		t.Fatalf("enlace de posición = %q, %v", got, err)
	}
	if _, err := s.EvaluationKeyForOrder(ctx, "ausente"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("orden ausente err = %v, want ErrNotFound", err)
	}
	events, err := s.RecentIBPaperEvents(ctx, "eval-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("eventos PAPER = %d, want 2", len(events))
	}
}
