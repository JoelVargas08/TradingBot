package ibeval_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/ibeval"
	"tradingview-bot/internal/store"
)

type countingPublisher struct {
	mu    sync.Mutex
	calls int
	fail  bool
}

func (p *countingPublisher) Publish(ctx context.Context, ev domain.SignalEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.fail {
		return errors.New("bus no disponible")
	}
	return nil
}

func (p *countingPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func seedOutbox(t *testing.T, s *store.Store) (string, string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	bar := now.Add(-time.Minute)
	evalKey := ibeval.EvaluationKey("ib_investing", "BTCUSDT", "15m", bar, "fp-worker")
	pubKey := ibeval.PublicationKey("ib_investing", "BTCUSDT", "15m", bar)
	ev := domain.SignalEvent{
		StrategyID: "ib_investing", Symbol: "BTCUSDT", Timeframe: "15m",
		Direction: domain.DirectionBuy, Price: 60000, BarTS: bar,
	}
	msg, err := ibeval.NewOutboxMessage(ev, evalKey, pubKey, now)
	if err != nil {
		t.Fatal(err)
	}
	rec := ibeval.EvaluationRecord{
		EvaluationKey: evalKey, PublicationKey: pubKey,
		StrategyID: "ib_investing", Symbol: "BTCUSDT", Timeframe: "15m", BarTS: bar,
		ModelFingerprint: "fp-worker", Status: ibeval.EvalAccepted, Trace: map[string]any{"ok": true},
	}
	if _, enqueued, err := s.RecordEvaluation(ctx, rec, msg); err != nil || !enqueued {
		t.Fatalf("seed Outbox = %v, %v", enqueued, err)
	}
	return evalKey, pubKey
}

// TestOutboxWorkerCrashAfterPublishRedeliversUntilAck modela una caída después
// de publicar y antes del ack: el mensaje vuelve a entregarse (at-least-once)
// y deja de hacerlo cuando el consumidor confirma.
func TestOutboxWorkerCrashAfterPublishRedeliversUntilAck(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, pubKey := seedOutbox(t, s)

	pub := &countingPublisher{}
	w := ibeval.NewOutboxWorker(s, pub, ibeval.WorkerConfig{
		Visibility:   time.Millisecond,
		PollInterval: time.Millisecond,
		MaxAttempts:  5,
	})
	if n, err := w.Drain(ctx); err != nil || n != 1 {
		t.Fatalf("primer Drain = %d, %v", n, err)
	}
	if pub.count() != 1 {
		t.Fatalf("publicaciones = %d, want 1", pub.count())
	}
	// Simula la caída del consumidor: sin ack, la visibilidad vence.
	time.Sleep(5 * time.Millisecond)
	if n, err := w.Drain(ctx); err != nil || n != 1 {
		t.Fatalf("Drain tras caída = %d, %v", n, err)
	}
	if pub.count() != 2 {
		t.Fatalf("publicaciones tras reintento = %d, want 2", pub.count())
	}
	if err := s.AckOutbox(ctx, pubKey, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if n, err := w.Drain(ctx); err != nil || n != 0 {
		t.Fatalf("Drain tras ack = %d, %v", n, err)
	}
	if pub.count() != 2 {
		t.Fatalf("el ack no detuvo las publicaciones: %d", pub.count())
	}
}

// TestOutboxWorkerFailureRetriesAndDies comprueba backoff y estado dead.
func TestOutboxWorkerFailureRetriesAndDies(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedOutbox(t, s)

	pub := &countingPublisher{fail: true}
	w := ibeval.NewOutboxWorker(s, pub, ibeval.WorkerConfig{
		MaxAttempts: 2,
		BaseBackoff: time.Millisecond,
		MaxBackoff:  time.Millisecond,
	})
	if _, err := w.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if pub.count() != 1 {
		t.Fatalf("publicaciones = %d, want 1", pub.count())
	}
	// El primer fallo deja backoff; esperar a que venza.
	time.Sleep(5 * time.Millisecond)
	if _, err := w.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if pub.count() != 2 {
		t.Fatalf("publicaciones = %d, want 2", pub.count())
	}
	sum, err := s.IBSummary(ctx, "ib_investing")
	if err != nil {
		t.Fatal(err)
	}
	if sum.OutboxDead != 1 {
		t.Fatalf("el Outbox no quedó dead tras agotar intentos: %+v", sum)
	}
	time.Sleep(5 * time.Millisecond)
	if n, err := w.Drain(ctx); err != nil || n != 0 {
		t.Fatalf("un dead no debe reintentarse: %d, %v", n, err)
	}
}

func TestOutboxAckIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, pubKey := seedOutbox(t, s)
	for i := 0; i < 2; i++ {
		if err := s.AckOutbox(ctx, pubKey, time.Now().UTC()); err != nil {
			t.Fatalf("ack %d: %v", i+1, err)
		}
	}
}
