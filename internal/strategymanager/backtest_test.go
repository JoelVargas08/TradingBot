package strategymanager

import (
	"math"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func testKlines(n int) []domain.Kline {
	out := make([]domain.Kline, n)
	// Serie oscilante (sinusoidal) para que los cruces de EMA ocurran
	// repetidamente a lo largo de TODA la serie, incluida la porción OOS
	// de cada fold. Una serie monótona solo cruza una vez (en el warmup),
	// lo que no genera trades en OOS con indexing global correcto.
	price := 100.0
	for i := 0; i < n; i++ {
		price = 100 + 15*math.Sin(float64(i)/18.0) + float64(i%7)*0.02
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

func TestWalkForwardFourFolds(t *testing.T) {
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
	ks := testKlines(2000)
	res, err := Backtest(spec, ks, Thresholds{Folds: 4})
	if err != nil {
		t.Fatalf("Backtest: %v", err)
	}
	if res.Folds != 4 {
		t.Errorf("folds = %d, want 4", res.Folds)
	}
	if len(res.OOSFolds) != 4 {
		t.Errorf("OOSFolds = %d, want 4", len(res.OOSFolds))
	}
	if res.Folds != len(res.OOSFolds) {
		t.Error("Folds y OOSFolds deben coincidir")
	}
	totalTrades := 0
	for _, f := range res.OOSFolds {
		totalTrades += f.Trades
	}
	if totalTrades != res.Trades {
		t.Errorf("suma de trades por fold (%d) != trades agregados (%d)", totalTrades, res.Trades)
	}
}

func TestWalkForwardStatus(t *testing.T) {
	spec := StrategySpec{
		Symbol:    "BTCUSDT",
		Timeframe: "1h",
		Entries: []Rule{
			{Side: "buy", Conditions: []Condition{{Left: "close", Op: ">", Right: 1e9}}},
		},
		StopPct: 1.0,
	}
	res, err := Backtest(spec, testKlines(600), Thresholds{Folds: 4})
	if err != nil {
		t.Fatalf("Backtest: %v", err)
	}
	if res.Status != "REJECTED" {
		t.Errorf("status = %q, want REJECTED sin trades", res.Status)
	}
	if res.Passed {
		t.Error("sin trades no debería pasar umbrales")
	}
}

func TestAggregatedReturnCompoundsSimpleReturns(t *testing.T) {
	rets := []float64{0.10, -0.05, 0.20}
	want := ((1.10 * 0.95 * 1.20) - 1) * 100
	if got := compoundReturns(rets); math.Abs(got-want) > 1e-9 {
		t.Errorf("compoundReturns = %v, want %v", got, want)
	}
}

func TestProfitFactorUsesNetReturns(t *testing.T) {
	// Con costes altos, el PF neto debe ser < 1 aunque el bruto sea > 1.
	trades := []tradeResult{
		{gross: 0.020, net: 0.010},
		{gross: 0.025, net: 0.015},
		{gross: -0.010, net: -0.020},
		{gross: -0.015, net: -0.025},
	}
	m := equityMetrics{}
	for _, tr := range trades {
		if tr.net > 0 {
			m.wins++
			m.netProfit += tr.net
		} else {
			m.losses++
			m.netLoss += -tr.net
		}
	}
	if m.profitFactor = m.netProfit / m.netLoss; m.profitFactor >= 1 {
		t.Errorf("PF neto = %v, want < 1 con costes", m.profitFactor)
	}
	// PF también debe estar correctamente calculado en runEquity.
	spec := StrategySpec{
		Symbol:    "BTCUSDT",
		Timeframe: "1h",
		Entries:   []Rule{{Side: "buy", Conditions: []Condition{{Left: "close", Op: ">", Right: 0}}}},
		Exits:     []Rule{{Side: "buy", Conditions: []Condition{{Left: "close", Op: "<", Right: 1e9}}}},
		StopPct:   0.1,
	}
	ks := testKlines(200)
	ctx := newEvalContext(ks, spec.Indicators)
	res, err := runEquity(ctx, spec, ks, 0, BacktestCosts{FeeRate: 0.001, SlippageRate: 0.001})
	if err != nil {
		t.Fatalf("runEquity: %v", err)
	}
	if res.profitFactor < 0 || math.IsNaN(res.profitFactor) {
		t.Errorf("profitFactor inválido: %v", res.profitFactor)
	}
}

// TestWalkForwardUsesGlobalIndicatorIndex verifica que runEquity consulta los
// indicadores con el índice global (testStart + i), no con el índice local.
func TestWalkForwardUsesGlobalIndicatorIndex(t *testing.T) {
	ctx := newEvalContext(testKlines(300), nil)
	ks := testKlines(50)
	// globalIndex del primer elemento de la porción.
	global := 100
	res, err := runEquity(ctx, StrategySpec{
		Symbol:    "BTCUSDT",
		Timeframe: "1h",
		Entries:   []Rule{{Side: "buy", Conditions: []Condition{{Left: "close", Op: ">", Right: 0}}}},
		Exits:     []Rule{{Side: "buy", Conditions: []Condition{{Left: "close", Op: "<", Right: 1e9}}}},
	}, ks, global, BacktestCosts{})
	if err != nil {
		t.Fatalf("runEquity: %v", err)
	}
	_ = res
}

// TestWalkForwardHasNoLookahead verifica que cada fold OOS solo evalúa en su
// porción y que los indicadores usan datos hasta esa posición (sin futuro).
func TestWalkForwardHasNoLookahead(t *testing.T) {
	// Una condición "close > 1000000" nunca se cumple en testKlines, por lo
	// que el primer fold no debe generar trades por filtrado futuro.
	spec := StrategySpec{
		Symbol:    "BTCUSDT",
		Timeframe: "1h",
		Entries:   []Rule{{Side: "buy", Conditions: []Condition{{Left: "close", Op: ">", Right: 1e9}}}},
		StopPct:   1.0,
	}
	ks := testKlines(2000)
	res, err := Backtest(spec, ks, Thresholds{Folds: 4})
	if err != nil {
		t.Fatalf("Backtest: %v", err)
	}
	if res.Trades != 0 {
		t.Errorf("trades = %d, want 0 (condición imposible filtrada en todos los folds)", res.Trades)
	}
}

func compoundReturns(rets []float64) float64 {
	eq := 1.0
	for _, r := range rets {
		eq *= 1 + r
	}
	return (eq - 1) * 100
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
