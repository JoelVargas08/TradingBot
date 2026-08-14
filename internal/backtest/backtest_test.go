package backtest

import (
	"math"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func mkCandles(closes []float64) []domain.Kline {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]domain.Kline, len(closes))
	for i, c := range closes {
		out[i] = domain.Kline{
			Symbol: "ETHUSDT",
			Start:  start.Add(time.Duration(i) * time.Hour),
			Open:   c,
			High:   c,
			Low:    c,
			Close:  c,
		}
	}
	return out
}

func TestBacktestBuyHoldEquity(t *testing.T) {
	candles := mkCandles([]float64{100, 110, 121})
	r, err := Backtest(candles, func(int, []domain.Kline) Direction { return SignalNone }, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := r.BuyHoldReturn, 0.21; math.Abs(got-want) > 1e-9 {
		t.Fatalf("BuyHoldReturn = %v, want %v", got, want)
	}
	if got, want := r.TotalReturn, 0.0; math.Abs(got-want) > 1e-9 {
		t.Fatalf("TotalReturn = %v, want %v", got, want)
	}
	if r.Trades != 0 {
		t.Fatalf("Trades = %d, want 0", r.Trades)
	}
}

func TestBacktestAlwaysLong(t *testing.T) {
	candles := mkCandles([]float64{100, 110, 99, 90})
	r, err := Backtest(candles, func(int, []domain.Kline) Direction { return SignalLong }, Config{})
	if err != nil {
		t.Fatal(err)
	}
	// sin costos: 1.1 * 0.9 * (90/99) = 0.9 → -10%
	if got, want := r.TotalReturn, -0.1; math.Abs(got-want) > 1e-9 {
		t.Fatalf("TotalReturn = %v, want %v", got, want)
	}
	// equity final igual a balance sin posiciones
	if r.Trades != 1 {
		t.Fatalf("Trades = %d, want 1", r.Trades)
	}
	if got, want := r.FinalBalance, 9000.0; math.Abs(got-want) > 1e-9 {
		t.Fatalf("FinalBalance = %v, want %v", got, want)
	}
}

func TestBacktestShort(t *testing.T) {
	candles := mkCandles([]float64{100, 90, 81})
	r, err := Backtest(candles, func(int, []domain.Kline) Direction { return SignalShort }, Config{})
	if err != nil {
		t.Fatal(err)
	}
	// corto sostenido 100→81 = +19% (un único trade, no re-entra)
	if got, want := r.TotalReturn, 0.19; math.Abs(got-want) > 1e-9 {
		t.Fatalf("TotalReturn = %v, want %v", got, want)
	}
}

func TestBacktestFeesReduceReturn(t *testing.T) {
	candles := mkCandles([]float64{100, 110})
	long := func(int, []domain.Kline) Direction { return SignalLong }
	noFees, _ := Backtest(candles, long, Config{})
	feeCfg := Config{FeePct: 0.001, SlippagePct: 0.0005}
	withFees, _ := Backtest(candles, long, feeCfg)
	if withFees.TotalReturn >= noFees.TotalReturn {
		t.Fatalf("con costos el retorno debería ser menor: noFees=%v withFees=%v", noFees.TotalReturn, withFees.TotalReturn)
	}
	if withFees.TotalFees <= 0 {
		t.Fatalf("TotalFees = %v, esperado > 0", withFees.TotalFees)
	}
}

func TestBacktestStopHit(t *testing.T) {
	// cierra con stop cuando el mínimo cruza el stop
	candles := mkCandles([]float64{100, 105, 94})
	cfg := Config{StopPct: 0.05}
	r, err := Backtest(candles, func(int, []domain.Kline) Direction { return SignalLong }, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// stop = 100 * (1-0.05) = 95; en la 3ª vela Low=94 → se activa
	if len(r.TradeLog) == 0 {
		t.Fatalf("sin trades, TradeLog=%v", r.TradeLog)
	}
	if r.TradeLog[0].Reason != "stop" {
		t.Fatalf("Reason = %q, want stop", r.TradeLog[0].Reason)
	}
	if r.TradeLog[0].ExitPrice >= 100 {
		t.Fatalf("ExitPrice = %v, esperado por debajo de 100", r.TradeLog[0].ExitPrice)
	}
}

func TestBacktestOppositeSignalCloses(t *testing.T) {
	candles := mkCandles([]float64{100, 110, 99})
	sig := func(i int, _ []domain.Kline) Direction {
		if i < 2 {
			return SignalLong
		}
		return SignalShort
	}
	r, err := Backtest(candles, sig, Config{})
	if err != nil {
		t.Fatal(err)
	}
	// el 3er cierre: cierra el long por señal y abre un short
	if len(r.TradeLog) < 1 {
		t.Fatalf("sin trades, TradeLog=%v", r.TradeLog)
	}
	if r.TradeLog[0].Reason != "signal" {
		t.Fatalf("Reason = %q, want signal", r.TradeLog[0].Reason)
	}
	if len(r.TradeLog) != 2 {
		t.Fatalf("Trades = %d, want 2 (cierre por señal + cierre fin de serie)", len(r.TradeLog))
	}
}

func TestBacktestNoneClosesToFlat(t *testing.T) {
	candles := mkCandles([]float64{100, 110, 109})
	sig := func(i int, _ []domain.Kline) Direction {
		if i == 0 {
			return SignalLong
		}
		return SignalNone
	}
	r, err := Backtest(candles, sig, Config{})
	if err != nil {
		t.Fatal(err)
	}
	// se abre long en 100, se cierra a flat en 110 (ganancia +10%), luego flat
	if got, want := r.TotalReturn, 0.1; math.Abs(got-want) > 1e-9 {
		t.Fatalf("TotalReturn = %v, want %v", got, want)
	}
	if r.Trades != 1 {
		t.Fatalf("Trades = %d, want 1", r.Trades)
	}
}

func TestBacktestSharpeZeroWhenFlat(t *testing.T) {
	candles := mkCandles([]float64{100, 100, 100, 100})
	r, _ := Backtest(candles, func(int, []domain.Kline) Direction { return SignalNone }, Config{})
	if r.Sharpe != 0 || r.MaxDrawdown != 0 {
		t.Fatalf("flat: Sharpe=%v MaxDD=%v, esperado 0", r.Sharpe, r.MaxDrawdown)
	}
}

func TestBacktestNeedsTwoBars(t *testing.T) {
	candles := mkCandles([]float64{100})
	if _, err := Backtest(candles, func(int, []domain.Kline) Direction { return SignalNone }, Config{}); err == nil {
		t.Fatal("esperaba error con 1 vela")
	}
}

func TestBacktestNoLookaheadOnFirstBar(t *testing.T) {
	// la primera barra solo puede emitir SignalNone si el generador
	// necesita historia previa (como en estrategias con indicadores)
	candles := mkCandles([]float64{100, 101, 102, 103, 104, 105})
	calls := 0
	r, err := Backtest(candles, func(i int, c []domain.Kline) Direction {
		calls++
		if calls > 1 && i == 0 {
			t.Fatal("SignalFunc llamada para i=0 con más de una invocación (puede indicar lookahead)")
		}
		return SignalNone
	}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Bars != len(candles) {
		t.Fatalf("Bars = %d, want %d", r.Bars, len(candles))
	}
}
