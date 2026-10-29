package strategymanager

import (
	"context"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

type liveCandleSource struct {
	candles []domain.Kline
}

func (s *liveCandleSource) RecentCandles(ctx context.Context, symbol, timeframe string, limit int) ([]domain.Kline, error) {
	if limit > len(s.candles) {
		limit = len(s.candles)
	}
	out := append([]domain.Kline(nil), s.candles[len(s.candles)-limit:]...)
	return out, nil
}

func (s *liveCandleSource) CandlesBetween(ctx context.Context, symbol, timeframe string, start, end time.Time) ([]domain.Kline, error) {
	var out []domain.Kline
	for _, k := range s.candles {
		if k.Symbol == symbol && k.Timeframe == timeframe && !k.Start.Before(start) && k.Start.Before(end) {
			out = append(out, k)
		}
	}
	return out, nil
}

type livePublisher struct {
	events []domain.SignalEvent
}

func (p *livePublisher) Publish(ctx context.Context, ev domain.SignalEvent) error {
	p.events = append(p.events, ev)
	return nil
}

func TestLiveEngineIgnoresOtherSelection(t *testing.T) {
	src := &liveCandleSource{}
	pub := &livePublisher{}
	e := NewLiveEngine(src, nil, pub)

	k := domain.Kline{
		Symbol: "ETHUSDT", Timeframe: "1h", Start: time.Unix(1000, 0).UTC(),
		Open: 100, High: 101, Low: 99, Close: 100.5, Volume: 10, Closed: true,
	}
	if err := e.OnCandle(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	if len(pub.events) != 0 {
		t.Fatalf("events = %d, want 0", len(pub.events))
	}
}

func TestLiveEngineIgnoresOpenCandle(t *testing.T) {
	src := &liveCandleSource{}
	pub := &livePublisher{}
	e := NewLiveEngine(src, nil, pub)

	k := domain.Kline{
		Symbol: "BTCUSDT", Timeframe: "1h", Start: time.Unix(1000, 0).UTC(),
		Open: 100, High: 101, Low: 99, Close: 100.5, Volume: 10, Closed: false,
	}
	if err := e.OnCandle(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	if len(pub.events) != 0 {
		t.Fatalf("events = %d, want 0", len(pub.events))
	}
}

func TestLiveEngineSetSelection(t *testing.T) {
	e := NewLiveEngine(&liveCandleSource{}, nil, &livePublisher{})
	if err := e.SetSelection("chandelier", "ETHUSDT", "15m"); err != nil {
		t.Fatal(err)
	}
	s, sym, tf := e.Selection()
	if s != "chandelier" || sym != "ETHUSDT" || tf != "15m" {
		t.Fatalf("selection = %s %s %s", s, sym, tf)
	}
}

type countingSource struct {
	liveCandleSource
	calls int
}

func (s *countingSource) RecentCandles(ctx context.Context, symbol, timeframe string, limit int) ([]domain.Kline, error) {
	s.calls++
	return s.liveCandleSource.RecentCandles(ctx, symbol, timeframe, limit)
}

// Con la selección explícita "none" el motor compartido no evalúa nada: una
// sesión solo-Investing-Bulls nunca opera Chandelier como sustituto.
func TestLiveEngineStrategyNoneNeverEvaluates(t *testing.T) {
	src := &countingSource{}
	pub := &livePublisher{}
	e := NewLiveEngine(src, nil, pub)
	if err := e.SetSelection(StrategyNone, "BTCUSDT", "1h"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		k := domain.Kline{
			Symbol: "BTCUSDT", Timeframe: "1h", Start: time.Unix(int64(3600*(i+1)), 0).UTC(),
			Open: 100, High: 120, Low: 80, Close: 100 + float64(i*5), Volume: 10, Closed: true,
		}
		src.candles = append(src.candles, k)
		if err := e.OnCandle(context.Background(), k); err != nil {
			t.Fatal(err)
		}
	}
	if len(pub.events) != 0 || src.calls != 0 {
		t.Fatalf("StrategyNone publicó %d señales y leyó velas %d veces", len(pub.events), src.calls)
	}
}
