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

func position(symbol string, side domain.Direction, entryPrice, quantity float64) domain.Position {
	return domain.Position{
		StrategyID: "chandelier",
		Symbol:     symbol,
		Timeframe:  "1h",
		Side:       side,
		EntryTS:    time.UnixMilli(1),
		EntryPrice: entryPrice,
		StopLoss:   entryPrice * 0.97,
		TakeProfit: entryPrice * 1.06,
		Quantity:   quantity,
		RiskAmount: 100,
		Status:     domain.PositionOpen,
	}
}

func TestPositionRoundtrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	id, err := s.OpenPosition(ctx, position("BTCUSDT", domain.DirectionBuy, 60000, 0.1))
	if err != nil {
		t.Fatalf("OpenPosition: %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d, want > 0", id)
	}
	open, err := s.OpenPositions(ctx)
	if err != nil {
		t.Fatalf("OpenPositions: %v", err)
	}
	if len(open) != 1 || open[0].Symbol != "BTCUSDT" || open[0].EntryPrice != 60000 {
		t.Errorf("posiciones abiertas inesperadas: %+v", open)
	}
}

func TestPositionUniquePerOpenStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.OpenPosition(ctx, position("BTCUSDT", domain.DirectionBuy, 60000, 0.1)); err != nil {
		t.Fatalf("primera apertura: %v", err)
	}
	if _, err := s.OpenPosition(ctx, position("BTCUSDT", domain.DirectionBuy, 61000, 0.1)); !errors.Is(err, domain.ErrDuplicate) {
		t.Errorf("segunda apertura abierta debería devolver ErrDuplicate, got %v", err)
	}
}

func TestClosePositionUpdatesPnLAndAccount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.UpdateAccount(ctx, domain.Account{Balance: 10000, PeakEquity: 10000, UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("UpdateAccount inicial: %v", err)
	}
	id, err := s.OpenPosition(ctx, position("BTCUSDT", domain.DirectionBuy, 60000, 0.1))
	if err != nil {
		t.Fatalf("OpenPosition: %v", err)
	}
	p, err := s.ClosePosition(ctx, id, 61000, time.UnixMilli(2), domain.CloseOptions{})
	if err != nil {
		t.Fatalf("ClosePosition: %v", err)
	}
	if p.Status != domain.PositionClosed || p.PnL != 100 {
		t.Errorf("posición cerrada inesperada: %+v", p)
	}
	open, err := s.OpenPositions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Errorf("no debería haber posiciones abiertas: %+v", open)
	}
	acc, err := s.GetAccount(ctx)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if acc.Balance != 10100 {
		t.Errorf("balance = %v, want 10100", acc.Balance)
	}
}

func TestAccountNotFoundAndRoundtrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.GetAccount(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("cuenta inicial debería devolver ErrNotFound, got %v", err)
	}
	if err := s.UpdateAccount(ctx, domain.Account{Balance: 5000, PeakEquity: 5500, UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	acc, err := s.GetAccount(ctx)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if acc.Balance != 5000 || acc.PeakEquity != 5500 {
		t.Errorf("cuenta inesperada: %+v", acc)
	}
}
