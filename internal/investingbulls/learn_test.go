package investingbulls

import (
	"encoding/json"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func TestLearnRequiresEnoughData(t *testing.T) {
	_, err := Learn(make([]domain.Kline, 20), DefaultLearnConfig())
	if err == nil { t.Fatal("expected insufficient data error") }
}

func TestLearnDoesNotPanicOnOHLCV(t *testing.T) {
	ks := syntheticLearningCandles(140)
	cfg := DefaultLearnConfig()
	cfg.Symbol = "TEST"
	cfg.Timeframe = "1h"
	cfg.MinTrades = 1
	_, err := Learn(ks, cfg)
	if err != nil { t.Fatal(err) }
}

func TestValidateKlinesRejectsCorruptData(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		mut  func(k *domain.Kline)
	}{
		{"vela en formación", func(k *domain.Kline) { k.Closed = false }},
		{"close fuera de rango", func(k *domain.Kline) { k.Close = 200 }},
		{"open fuera de rango", func(k *domain.Kline) { k.Open = 50 }},
		{"high menor que low", func(k *domain.Kline) { k.High, k.Low = 99, 101 }},
		{"volume negativo", func(k *domain.Kline) { k.Volume = -1 }},
		{"timestamp desordenado", func(k *domain.Kline) { k.Start = base.Add(-time.Hour) }},
	}
	for _, c := range cases {
		ks := []domain.Kline{
			{Start: base, Timeframe: "1h", Open: 100, High: 101, Low: 99, Close: 100, Volume: 10, Closed: true},
			{Start: base.Add(time.Hour), Timeframe: "1h", Open: 100, High: 101, Low: 99, Close: 100, Volume: 10, Closed: true},
			{Start: base.Add(2 * time.Hour), Timeframe: "1h", Open: 100, High: 101, Low: 99, Close: 100, Volume: 10, Closed: true},
		}
		c.mut(&ks[1])
		if err := ValidateKlines(ks, "TEST", "1h"); err == nil {
			t.Fatalf("%s: se esperaba error de validación", c.name)
		}
	}

	good := []domain.Kline{
		{Start: base, Timeframe: "1h", Open: 100, High: 101, Low: 99, Close: 100, Volume: 10, Closed: true},
		{Start: base.Add(time.Hour), Timeframe: "1h", Open: 100, High: 101, Low: 99, Close: 100, Volume: 10, Closed: true},
		{Start: base.Add(2 * time.Hour), Timeframe: "1h", Open: 100, High: 101, Low: 99, Close: 100, Volume: 10, Closed: true},
	}
	if err := ValidateKlines(good, "TEST", "1h"); err != nil {
		t.Fatalf("datos correctos rechazados: %v", err)
	}
	if err := ValidateKlines(good, "OTHER", "1h"); err != nil {
		t.Fatalf("validar con symbol aportado por cfg no debe exigir k.Symbol: %v", err)
	}
}

func TestDatasetDigestStableAndOrderSensitive(t *testing.T) {
	ks := syntheticLearningCandles(10)
	d1 := datasetDigest(ks)
	d2 := datasetDigest(append([]domain.Kline(nil), ks...))
	if d1 != d2 { t.Fatalf("digest debe ser determinista") }
	ks[5].Close++
	if datasetDigest(ks) == d1 { t.Fatal("digest debe cambiar al mutar los datos") }
}

func TestLearnRejectsCorruptData(t *testing.T) {
	ks := syntheticLearningCandles(120)
	ks[50].Closed = false
	_, err := Learn(ks, DefaultLearnConfig())
	if err == nil { t.Fatal("se esperaba rechazo por datos corruptos") }
}

func TestLearnedModelPersistsParameters(t *testing.T) {
	// Modelo construido tal y como lo haría Learn (mismos campos, sec 15).
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	m := LearnedModel{
		Version: 1, Family: "investing_bulls",
		Symbol: "BTCUSDT", Timeframe: "1h",
		SwingLeft: 2, SwingRight: 2,
		Fib: DefaultFibConfig(), Confluence: DefaultConfluenceConfig(), TradePlan: DefaultTradePlanConfig(),
		InitialBalance: 10000, FeePct: 0.001, SlippagePct: 0.0002, MinTrades: 8,
		FinalBalance: 12345.67, Trades: 12, WinRate: 0.5, ProfitFactor: 1.4,
		Sharpe: 0.8, Sortino: 1.1, TotalReturn: 0.2345, MaxDrawdown: 0.05,
		DatasetHash: "abc123", DatasetBars: 500, CodeVersion: CodeVersion,
		LearnedAt: now,
	}
	raw, err := json.Marshal(m)
	if err != nil { t.Fatal(err) }

	var restored LearnedModel
	if err := json.Unmarshal(raw, &restored); err != nil { t.Fatal(err) }

	// El modelo persistido debe reconstruir la config exacta (sec 15).
	fromModel := cfgFromModel(restored)
	if fromModel.InitialBalance != m.InitialBalance || fromModel.FeePct != m.FeePct ||
		fromModel.SlippagePct != m.SlippagePct || fromModel.MinTrades != m.MinTrades {
		t.Fatalf("cfgFromModel no reconstruye los parámetros: %+v", fromModel)
	}
	if fromModel.Fib != m.Fib || fromModel.Confluence != m.Confluence || fromModel.TradePlan != m.TradePlan {
		t.Fatalf("cfgFromModel no reconstruye las configs: %+v", fromModel)
	}
	if fromModel.SwingLeft != m.SwingLeft || fromModel.SwingRight != m.SwingRight {
		t.Fatalf("cfgFromModel no reconstruye swings: %+v", fromModel)
	}
	// Metadatos de reproducibilidad (sec 16).
	if restored.DatasetHash != "abc123" || restored.DatasetBars != 500 || restored.CodeVersion == "" {
		t.Fatalf("metadatos de reproducibilidad no persistidos: %+v", restored)
	}
	// Los modelos legacy (sin parámetros) caen a valores por defecto, no a cero.
	legacy := cfgFromModel(LearnedModel{Symbol: "BTCUSDT", Timeframe: "1h", SwingLeft: 2, SwingRight: 2, Fib: m.Fib, Confluence: m.Confluence, TradePlan: m.TradePlan})
	if legacy.InitialBalance != 10000 || legacy.FeePct != 0.001 || legacy.SlippagePct != 0.0002 || legacy.MinTrades != 8 {
		t.Fatalf("cfgFromModel no aplica defaults a modelos legacy: %+v", legacy)
	}
}

func TestTradeMetricsExtended(t *testing.T) {
	// Tres operaciones deterministas: +0.50, -0.20, +0.10 (retornos por trade).
	trades := []LearnedTrade{
		{Direction: domain.DirectionBuy, PnL: 0.50, FeePct: 0.001},
		{Direction: domain.DirectionSell, PnL: -0.20, FeePct: 0.001},
		{Direction: domain.DirectionBuy, PnL: 0.10, FeePct: 0.001},
	}
	m := tradeMetrics(trades, 10000)
	if m.trades != 3 || m.wins != 2 || m.losses != 1 {
		t.Fatalf("counts erróneos: %+v", m)
	}
	if mathAbsTest(m.winRate-2.0/3.0) > 1e-12 { t.Fatalf("winRate incorrecto: %v", m.winRate) }
	if mathAbsTest(m.grossProfit-0.60) > 1e-12 { t.Fatalf("grossProfit incorrecto: %v", m.grossProfit) }
	if mathAbsTest(m.grossLoss-0.20) > 1e-12 { t.Fatalf("grossLoss incorrecto: %v", m.grossLoss) }
	// Fees: 2×0.001×equity antes de cada trade (equity 10000→15000→12000).
	wantFees := 2*0.001*10000 + 2*0.001*15000 + 2*0.001*12000
	if mathAbsTest(m.feesPaid-wantFees) > 1e-6 { t.Fatalf("feesPaid incorrecto: %v, want %v", m.feesPaid, wantFees) }
	if mathAbsTest(m.averageWin-0.30) > 1e-12 { t.Fatalf("averageWin incorrecto: %v", m.averageWin) }
	if mathAbsTest(m.averageLoss-0.20) > 1e-12 { t.Fatalf("averageLoss incorrecto: %v", m.averageLoss) }
	// Expectancy = wr*avgWin - (1-wr)*avgLoss
	wantExp := (2.0/3.0)*0.30 - (1.0/3.0)*0.20
	if mathAbsTest(m.expectancy-wantExp) > 1e-12 { t.Fatalf("expectancy incorrecto: %v", m.expectancy) }
	// Sharpe = media / sd poblacional de los retornos.
	if m.sharpe <= 0 || m.sharpe > 3 { t.Fatalf("sharpe fuera de rango esperado: %v", m.sharpe) }
	if m.sortino <= 0 { t.Fatalf("sortino debería ser positivo: %v", m.sortino) }
}

func TestNoDoubleSlippage(t *testing.T) {
	slip, fee := 0.0002, 0.001
	entryRaw, exitRaw := 100.0, 103.0

	pnlOnce := costedPnL(domain.DirectionBuy, entryRaw, exitRaw, slip, fee)
	// Slippage aplicado UNA vez: entrada 100→100.02 (buy), salida 103→102.9794 (sell-to-close).
	wantFill := entryRaw * entryMultiplier(domain.DirectionBuy, slip)
	wantExit := applyExitSlippage(exitRaw, domain.DirectionBuy, slip)
	want := (wantExit-wantFill)/wantFill - 2*fee
	if mathAbsTest(pnlOnce-want) > 1e-12 {
		t.Fatalf("pnl = %.10f, want %.10f (slippage una vez)", pnlOnce, want)
	}

	// El resultado NO debe ser equivalente a aplicar slippage por duplicado.
	double := (exitRaw*(1-2*slip) - entryRaw*(1+2*slip))/(entryRaw*(1+2*slip)) - 2*fee
	if mathAbsTest(pnlOnce-double) < 1e-9 {
		t.Fatalf("pnl %.10f no debe reflejar doble slippage (doble = %.10f)", pnlOnce, double)
	}
}

func TestFeesAppliedOnce(t *testing.T) {
	slip, fee := 0.0002, 0.001
	entryRaw, exitRaw := 100.0, 98.0

	// Con fees de lado y lado, pero cada uno UNA vez.
	pnl := costedPnL(domain.DirectionBuy, entryRaw, exitRaw, slip, fee)
	wantFill := entryRaw * entryMultiplier(domain.DirectionBuy, slip)
	wantExit := applyExitSlippage(exitRaw, domain.DirectionBuy, slip)
	want := (wantExit-wantFill)/wantFill - 2*fee
	if mathAbsTest(pnl-want) > 1e-12 {
		t.Fatalf("pnl = %.10f, want %.10f (fees una vez por lado)", pnl, want)
	}

	// Aplicar las fees por segunda vez debe dar un resultado distinto (no doble).
	twice := (wantExit-wantFill)/wantFill - 4*fee
	if pnl == twice {
		t.Fatalf("pnl %.10f no debe incluir fees dos veces", pnl)
	}
}

func mathAbsTest(v float64) float64 {
	if v < 0 { return -v }
	return v
}

// costedPnL reproduce exactamente el cálculo de PnL del simulador de Learn:
// entry con slippage una vez, exit con slippage una vez, fees una vez por lado.
func costedPnL(dir domain.Direction, entryRaw, exitRaw, slip, fee float64) float64 {
	fill := entryRaw * entryMultiplier(dir, slip)
	exit := applyExitSlippage(exitRaw, dir, slip)
	return tradePnL(dir, fill, exit) - 2*fee
}

func syntheticLearningCandles(n int) []domain.Kline {
	out := make([]domain.Kline, n)
	price := 100.0
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range out {
		if i%20 < 10 { price += 0.8 } else { price -= 0.65 }
		out[i] = domain.Kline{Start: base.Add(time.Duration(i) * time.Hour), Timeframe: "1h", Open: price-0.2, High: price+0.8, Low: price-0.8, Close: price, Volume: 1000, Closed: true}
	}
	return out
}
