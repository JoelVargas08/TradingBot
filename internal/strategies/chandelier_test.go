package strategies

import (
	"testing"
	"time"

	"tradingview-bot/internal/backtest"
	"tradingview-bot/internal/domain"
)

func chandelierCandles() []domain.Kline {
	// tendencia alcista clara → espera SignalLong en algún cruce
	vals := []float64{100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 112, 113, 114, 115, 116, 117, 118, 119, 120, 121, 122}
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]domain.Kline, len(vals))
	for i, v := range vals {
		out[i] = domain.Kline{
			Symbol: "ETHUSDT",
			Start:  start.Add(time.Duration(i) * time.Hour),
			Open:   v - 0.5,
			High:   v + 1,
			Low:    v - 1,
			Close:  v,
			Volume: 100,
		}
	}
	return out
}

func TestChandelierUptrendEntersLong(t *testing.T) {
	candles := chandelierCandles()
	c := DefaultChandelier()
	var sawLong bool
	for i := range candles {
		if c.Signal(i, candles) == backtest.SignalLong {
			sawLong = true
		}
	}
	if !sawLong {
		t.Fatal("tendencia alcista clara debería generar al menos una señal long")
	}
}

func TestChandelierNeedsWarmup(t *testing.T) {
	candles := chandelierCandles()
	c := DefaultChandelier()
	for i := 0; i < c.ATRPeriod; i++ {
		if c.Signal(i, candles) != backtest.SignalNone {
			t.Fatalf("vela %d (warmup) no debería generar señal", i)
		}
	}
}

func TestChandelierBacktestRuns(t *testing.T) {
	candles := chandelierCandles()
	c := DefaultChandelier()
	r, err := backtest.Backtest(candles, c.Signal, backtest.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Bars != len(candles) {
		t.Fatalf("Bars = %d, want %d", r.Bars, len(candles))
	}
	if r.FinalBalance <= 0 {
		t.Fatalf("FinalBalance = %v, esperado > 0", r.FinalBalance)
	}
}
