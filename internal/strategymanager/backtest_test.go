package strategymanager

import (
	"math"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func testKlines(n int) []domain.Kline {
	out := make([]domain.Kline, n)
	price := 100.0
	for i := 0; i < n; i++ {
		price += 0.5 + float64(i%7)*0.1
		out[i] = domain.Kline{
			Symbol:    "BTCUSDT",
			Timeframe: "1h",
			Start:     time.UnixMilli(int64(i) * 3600_000),
			Open:      price,
			High:      price + 1,
			Low:       price - 1,
			Close:     price,
			Volume:    1000 + float64(i%5)*10,
			Closed:    true,
		}
	}
	return out
}

func TestBacktestTrendFollowing(t *testing.T) {
	spec := StrategySpec{
		Symbol:    "BTCUSDT",
		Timeframe: "1h",
		Indicators: []Indicator{
			{ID: "ema_fast", Type: "ema", Length: 5},
			{ID: "ema_slow", Type: "ema", Length: 20},
		},
		Entries: []Rule{
			{Side: "buy", Conditions: []Condition{
				{Left: "ema_fast", Op: "cross_above", Right: "ema_slow"},
			}},
		},
		Exits: []Rule{
			{Side: "buy", Conditions: []Condition{
				{Left: "ema_fast", Op: "cross_below", Right: "ema_slow"},
			}},
		},
		StopPct:       5.0,
		TakeProfitPct: 10.0,
	}
	ks := testKlines(600)
	res, err := Backtest(spec, ks, Thresholds{})
	if err != nil {
		t.Fatalf("Backtest: %v", err)
	}
	if res.Trades <= 0 {
		t.Error("no se generaron trades en una serie con cruces")
	}
	if res.TestBars <= 0 {
		t.Error("porción de test vacía")
	}
	if res.WinRate < 0 || res.WinRate > 1 {
		t.Errorf("win rate fuera de rango: %v", res.WinRate)
	}
	if res.MaxDrawdown < 0 {
		t.Errorf("drawdown negativo: %v", res.MaxDrawdown)
	}
}

func TestBacktestInsufficientData(t *testing.T) {
	spec := StrategySpec{
		Symbol: "BTCUSDT", Timeframe: "1h",
		Entries: []Rule{{Side: "buy", Conditions: []Condition{{Left: "close", Op: ">", Right: 0}}}},
	}
	ks := testKlines(10)
	if _, err := Backtest(spec, ks, Thresholds{}); err == nil {
		t.Fatal("debería fallar con datos insuficientes")
	}
}

func TestBacktestNoTradesFails(t *testing.T) {
	// condición imposible → 0 trades → no supera MinTrades
	spec := StrategySpec{
		Symbol:    "BTCUSDT",
		Timeframe: "1h",
		Entries: []Rule{
			{Side: "buy", Conditions: []Condition{{Left: "close", Op: ">", Right: 1e9}}},
		},
		Exits: []Rule{
			{Side: "buy", Conditions: []Condition{{Left: "close", Op: "<", Right: 1e9}}},
		},
		StopPct:       1.0,
		TakeProfitPct: 0.5,
	}
	ks := testKlines(600)
	res, err := Backtest(spec, ks, Thresholds{})
	if err != nil {
		t.Fatalf("Backtest: %v", err)
	}
	if res.Trades != 0 {
		t.Errorf("trades = %d, want 0", res.Trades)
	}
	if res.Passed {
		t.Error("estrategia sin trades no debería pasar los umbrales")
	}
}

func TestSharpeCalculation(t *testing.T) {
	if got := sharpe(nil); got != 0 {
		t.Errorf("sharpe(nil) = %v, want 0", got)
	}
	if got := sharpe([]float64{1, 1, 1}); got != 0 {
		t.Errorf("sharpe de retornos constantes debería ser 0, got %v", got)
	}
	if got := sharpe([]float64{0.01, -0.005, 0.02, 0.01}); math.IsNaN(got) {
		t.Errorf("sharpe no debería ser NaN: %v", got)
	}
}

func TestIndicatorEMA(t *testing.T) {
	values := make([]float64, 30)
	for i := range values {
		values[i] = float64(i + 1)
	}
	// EMA sobre secuencia creciente debe ser creciente y cercana al último valor
	ema := ema(values, 10)
	if ema[29] <= 0 {
		t.Error("EMA no calculada")
	}
	if ema[29] < ema[20] {
		t.Error("EMA debería crecer en serie creciente")
	}
}
