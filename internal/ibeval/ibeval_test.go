package ibeval

import (
	"context"
	"errors"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func TestFingerprintIgnoresVolatileFieldsAndOrder(t *testing.T) {
	a := `{"name":"ib","threshold":3,"nested":{"updated_at":"2026-01-01","x":1}}`
	b := `{"nested":{"x":1,"updated_at":"2026-09-09"},"threshold":3,"name":"ib"}`
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("la huella cambió por orden de claves o campos volátiles")
	}
	c := `{"name":"ib","threshold":4,"nested":{"x":1}}`
	if Fingerprint(a) == Fingerprint(c) {
		t.Fatal("la huella no detectó un cambio semántico")
	}
}

func TestPublicationKeyStableAndEvaluationIncludesModel(t *testing.T) {
	bar := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	pub := PublicationKey("ib", "BTCUSDT", "15m", bar)
	if pub != PublicationKey("ib", "BTCUSDT", "15m", bar) {
		t.Fatal("PublicationKey inestable")
	}
	if pub == EvaluationKey("ib", "BTCUSDT", "15m", bar, "fp1") {
		t.Fatal("EvaluationKey debe incluir el modelo")
	}
	if EvaluationKey("ib", "BTCUSDT", "15m", bar, "fp1") == EvaluationKey("ib", "BTCUSDT", "15m", bar, "fp2") {
		t.Fatal("modelos distintos comparten EvaluationKey")
	}
}

type fakeOutbox struct {
	items   []OutboxItem
	updates []OutboxUpdate
}

func (f *fakeOutbox) ClaimOutbox(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]OutboxItem, error) {
	return f.items, nil
}

func (f *fakeOutbox) UpdateOutbox(ctx context.Context, up OutboxUpdate) error {
	f.updates = append(f.updates, up)
	return nil
}

type fakePublisher struct {
	err error
	got []domain.SignalEvent
}

func (f *fakePublisher) Publish(ctx context.Context, ev domain.SignalEvent) error {
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, ev)
	return nil
}

func TestOutboxWorkerDispatchesAndNegotiatesRetry(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	ev := domain.SignalEvent{StrategyID: "ib", Symbol: "BTCUSDT", Timeframe: "15m", BarTS: now}
	msg, err := NewOutboxMessage(ev, "eval", "pub", now)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeOutbox{items: []OutboxItem{{Key: "pub", Payload: msg.Payload, Attempts: 1}}}
	pub := &fakePublisher{}
	w := NewOutboxWorker(store, pub, WorkerConfig{MaxAttempts: 3})
	w.now = func() time.Time { return now }

	n, err := w.Drain(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("Drain = %d, %v", n, err)
	}
	if len(pub.got) != 1 || pub.got[0].StrategyID != "ib" {
		t.Fatalf("señal publicada = %+v", pub.got)
	}
	if len(store.updates) != 1 || store.updates[0].Status != OutboxDispatched {
		t.Fatalf("transición tras publicar = %+v", store.updates)
	}
	if !store.updates[0].AvailableAt.After(now) {
		t.Fatalf("visibilidad no futura: %+v", store.updates[0])
	}
}

func TestOutboxWorkerMarksDeadAfterMaxAttempts(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	ev := domain.SignalEvent{StrategyID: "ib", Symbol: "BTCUSDT", Timeframe: "15m", BarTS: now}
	msg, err := NewOutboxMessage(ev, "eval", "pub", now)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeOutbox{items: []OutboxItem{{Key: "pub", Payload: msg.Payload, Attempts: 3}}}
	w := NewOutboxWorker(store, &fakePublisher{err: errors.New("bus caído")}, WorkerConfig{MaxAttempts: 3})
	w.now = func() time.Time { return now }

	if _, err := w.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.updates) != 1 || store.updates[0].Status != OutboxDead {
		t.Fatalf("transición = %+v", store.updates)
	}
}

func TestOutboxWorkerDeadLettersCorruptPayload(t *testing.T) {
	store := &fakeOutbox{items: []OutboxItem{{Key: "bad", Payload: []byte("{"), Attempts: 1}}}
	w := NewOutboxWorker(store, &fakePublisher{}, WorkerConfig{})
	if _, err := w.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.updates) != 1 || store.updates[0].Status != OutboxDead {
		t.Fatalf("transición = %+v", store.updates)
	}
}
