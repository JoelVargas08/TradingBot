package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func event(symbol string, dir domain.Direction, barTS int64) domain.SignalEvent {
	return domain.SignalEvent{
		StrategyID: "chandelier",
		Symbol:     symbol,
		Timeframe:  "1h",
		Direction:  dir,
		Price:      60000,
		BarTS:      time.UnixMilli(barTS),
		ReceivedAt: time.Now(),
	}
}

func TestSaveAndLastSignal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SaveSignal(ctx, event("BTCUSDT", domain.DirectionBuy, 1)); err != nil {
		t.Fatalf("SaveSignal error: %v", err)
	}
	if err := s.SaveSignal(ctx, event("BTCUSDT", domain.DirectionSell, 2)); err != nil {
		t.Fatalf("SaveSignal error: %v", err)
	}
	last, err := s.LastSignal(ctx, "chandelier", "BTCUSDT", "1h")
	if err != nil {
		t.Fatalf("LastSignal error: %v", err)
	}
	if last.Direction != domain.DirectionSell || last.BarTS.UnixMilli() != 2 {
		t.Errorf("last inesperada: %+v", last)
	}
}

func TestLastSignalNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.LastSignal(ctx, "chandelier", "ETHUSDT", "1h"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestSaveDuplicateReturnsErrDuplicate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ev := event("BTCUSDT", domain.DirectionBuy, 1)
	if err := s.SaveSignal(ctx, ev); err != nil {
		t.Fatalf("primer guardado: %v", err)
	}
	if err := s.SaveSignal(ctx, ev); !errors.Is(err, domain.ErrDuplicate) {
		t.Errorf("duplicado debería devolver ErrDuplicate, got %v", err)
	}
}

func TestStrategyRoundtrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	st := domain.Strategy{
		ID:          "chandelier",
		Name:        "Chandelier Exit",
		Description: "22 ATR x3",
		Status:      domain.StrategyActive,
		CreatedAt:   time.Now(),
	}
	if err := s.UpsertStrategy(ctx, st); err != nil {
		t.Fatalf("UpsertStrategy error: %v", err)
	}
	st.Description = "actualizada"
	if err := s.UpsertStrategy(ctx, st); err != nil {
		t.Fatalf("UpsertStrategy error: %v", err)
	}
	got, err := s.GetStrategy(ctx, "chandelier")
	if err != nil {
		t.Fatalf("GetStrategy error: %v", err)
	}
	if got.Description != "actualizada" || got.Status != domain.StrategyActive {
		t.Errorf("estrategia inesperada: %+v", got)
	}
}

func TestStrategyNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetStrategy(context.Background(), "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func candle(symbol, tf string, ts int64, close, volume float64) domain.Kline {
	return domain.Kline{
		Symbol:    symbol,
		Timeframe: tf,
		Start:     time.UnixMilli(ts),
		Open:      close,
		High:      close + 1,
		Low:       close - 1,
		Close:     close,
		Volume:    volume,
		Closed:    true,
	}
}

func TestSaveAndRecentCandles(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SaveCandle(ctx, candle("BTCUSDT", "1h", 3, 100, 10)); err != nil {
		t.Fatalf("SaveCandle: %v", err)
	}
	if err := s.SaveCandle(ctx, candle("BTCUSDT", "1h", 1, 90, 10)); err != nil {
		t.Fatalf("SaveCandle: %v", err)
	}
	if err := s.SaveCandle(ctx, candle("BTCUSDT", "1h", 2, 95, 10)); err != nil {
		t.Fatalf("SaveCandle: %v", err)
	}
	if err := s.SaveCandle(ctx, candle("ETHUSDT", "1h", 2, 3000, 10)); err != nil {
		t.Fatalf("SaveCandle: %v", err)
	}

	got, err := s.RecentCandles(ctx, "BTCUSDT", "1h", 2)
	if err != nil {
		t.Fatalf("RecentCandles: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("velas = %d, want 2", len(got))
	}
	if got[0].Start.UnixMilli() != 2 || got[1].Start.UnixMilli() != 3 {
		t.Errorf("orden ascendente esperado (más reciente al final): %+v", got)
	}
	if !got[0].Closed {
		t.Error("velas históricas deberían marcarse como cerradas")
	}
}

func TestSaveCandleDeduplicates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	k := candle("BTCUSDT", "1h", 1, 100, 10)
	if err := s.SaveCandle(ctx, k); err != nil {
		t.Fatalf("primer guardado: %v", err)
	}
	if err := s.SaveCandle(ctx, k); err != nil {
		t.Fatalf("guardo duplicado de vela no debería fallar (INSERT OR IGNORE): %v", err)
	}
	got, err := s.RecentCandles(ctx, "BTCUSDT", "1h", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("velas = %d, want 1 (sin duplicados)", len(got))
	}
}
