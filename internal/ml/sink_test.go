package ml

import (
	"context"
	"sync"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

type stubStore struct {
	history []domain.Kline
	err     error
}

func (s *stubStore) SaveCandle(context.Context, domain.Kline) error { return nil }
func (s *stubStore) RecentCandles(context.Context, string, string, int) ([]domain.Kline, error) {
	return s.history, s.err
}
func (s *stubStore) CandlesBetween(context.Context, string, string, time.Time, time.Time) ([]domain.Kline, error) {
	return nil, nil
}

type stubPublisher struct {
	mu     sync.Mutex
	events []domain.SignalEvent
}

func (p *stubPublisher) Publish(_ context.Context, ev domain.SignalEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, ev)
	return nil
}

func TestSignalSinkSeedsAndPublishes(t *testing.T) {
	pred := &stubPredictor{p: Prediction{Signal: "buy", Confidence: 0.8, Price: 150, ModelVersion: "v1"}}
	eng := New(Config{Window: 3, Confidence: 0.6, Cooldown: time.Minute}, pred)
	now := time.Now().Truncate(time.Hour)
	store := &stubStore{history: klines(3, now.Add(-2*time.Hour))}
	pub := &stubPublisher{}
	sink := NewSignalSink(eng, pub, store, "BTCUSDT", "1h")

	k := klines(1, now)[0]
	if err := sink.OnCandle(context.Background(), k); err != nil {
		t.Fatalf("OnCandle: %v", err)
	}
	if len(pub.events) != 1 {
		t.Fatalf("eventos = %d, want 1", len(pub.events))
	}
	if pub.events[0].Meta[domain.MetaKeyModelVersion] != "v1" {
		t.Errorf("model_version no propagada: %v", pub.events[0].Meta)
	}
	if got := eng.Buffered("BTCUSDT", "1h"); got != 3 {
		t.Errorf("buffer = %d, want 3 (sin duplicar la vela ya persistida)", got)
	}
	seen := map[int64]bool{}
	for _, row := range pred.last {
		if seen[row.Start.UnixMilli()] {
			t.Errorf("vela duplicada en la ventana del predictor: %v", row.Start)
		}
		seen[row.Start.UnixMilli()] = true
	}
}

func TestSignalSinkFiltersOtherTimeframes(t *testing.T) {
	pred := &stubPredictor{p: Prediction{Signal: "buy", Confidence: 0.9, Price: 150}}
	eng := New(Config{Window: 1, Confidence: 0.5, Cooldown: time.Second}, pred)
	sink := NewSignalSink(eng, &stubPublisher{}, nil, "BTCUSDT", "1h")
	k := klines(1, time.Now())[0]
	k.Timeframe = "15m"
	if err := sink.OnCandle(context.Background(), k); err != nil {
		t.Fatalf("OnCandle: %v", err)
	}
	if pred.Calls() != 0 {
		t.Errorf("el sink no debe predecir para otro timeframe")
	}
}
