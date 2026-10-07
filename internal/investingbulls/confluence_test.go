package investingbulls

import (
	"math"
	"strings"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

// Las tres fuentes de evidencia y su HistoricalZones de Fibonacci.
//
// Un imbalance no rellenado es una zona futura: si contiene el precio ya no
// puede validar la confluencia.
func TestEvaluateConfluenceRequiresThreeFactors(t *testing.T) {
	ks := []domain.Kline{{Open: 100, High: 105, Low: 98, Close: 100, Start: time.Unix(1, 0)}}
	fib, _ := NewFibonacci(90, 110, TrendBullish, DefaultFibConfig())
	imbs := []Imbalance{{Index: 0, Low: 99, High: 101, Direction: ImbalanceBullish}}
	blocks := []OrderBlock{{Index: 0, Low: 99, High: 101, Direction: OrderBlockBullish, Valid: true}}

	got := EvaluateConfluence(ks, Structure{Trend: TrendBullish}, fib, imbs, blocks, DefaultConfluenceConfig())
	if len(got) != 1 || got[0].Score != 3 || !got[0].Valid {
		t.Fatalf("unexpected setups: %+v", got)
	}
}

func TestEvaluateConfluenceRejectsFilledImbalance(t *testing.T) {
	ks := []domain.Kline{{Open: 100, High: 105, Low: 98, Close: 100, Start: time.Unix(1, 0)}}
	fib, _ := NewFibonacci(90, 110, TrendBullish, DefaultFibConfig())
	filled := []Imbalance{{Index: 0, Low: 99, High: 101, Direction: ImbalanceBullish, IsFilled: true}}
	blocks := []OrderBlock{{Index: 0, Low: 99, High: 101, Direction: OrderBlockBullish, Valid: true}}

	got := EvaluateConfluence(ks, Structure{Trend: TrendBullish}, fib, filled, blocks, DefaultConfluenceConfig())
	if len(got) != 0 {
		t.Fatalf("filled imbalance must not confirm: %+v", got)
	}
}

func TestEvaluateConfluenceBearish(t *testing.T) {
	// Impulso bajista: el precio retrocede hacia arriba dentro de las zonas, y el
	// imbalance y el order block se esperarían por ENCIMA del precio.
	ks := []domain.Kline{{Open: 100, High: 100.5, Low: 99.5, Close: 100, Start: time.Unix(1, 0)}}
	fib, ok := NewFibonacci(90, 110, TrendBearish, DefaultFibConfig())
	if !ok || fib.Zone(100) == 0 {
		t.Fatalf("expected the price inside a bearish fib zone, got %+v", fib)
	}
	imbs := []Imbalance{{Index: 0, Low: 100.5, High: 102, Direction: ImbalanceBearish}}
	blocks := []OrderBlock{{Index: 0, Low: 100.5, High: 102, Direction: OrderBlockBearish, Valid: true}}

	got := EvaluateConfluence(ks, Structure{Trend: TrendBearish}, fib, imbs, blocks, ConfluenceConfig{Mode: ConfluenceAll, MaxZoneDistancePct: 0.02})
	if len(got) != 1 || got[0].Direction != domain.DirectionSell || !got[0].Valid {
		t.Fatalf("unexpected bearish setups: %+v", got)
	}
}

func TestEvaluateConfluenceOptionalFactors(t *testing.T) {
	ks := []domain.Kline{{Open: 100, High: 105, Low: 98, Close: 100, Start: time.Unix(1, 0)}}
	fib, _ := NewFibonacci(90, 110, TrendBullish, DefaultFibConfig())

	cfg := ConfluenceConfig{Mode: ConfluenceFibonacciOrderBlock, MaxZoneDistancePct: 0.01}

	got := EvaluateConfluence(ks, Structure{Trend: TrendBullish}, fib, nil, []OrderBlock{{Index: 0, Low: 99, High: 101, Direction: OrderBlockBullish, Valid: true}}, cfg)
	if len(got) != 1 || got[0].Score != 2 || !got[0].Valid {
		t.Fatalf("unexpected optional-factor setup: %+v", got)
	}
}

func TestConfluenceModesCoverThreeOfThreeAndPairs(t *testing.T) {
	modes := SearchConfluenceModes()
	if len(modes) != 3 {
		t.Fatalf("expected 3/3 plus two Fibonacci-based pair variants, got %d: %v", len(modes), modes)
	}
	seen := map[ConfluenceMode]bool{}
	for _, m := range modes {
		if !m.Valid() {
			t.Fatalf("invalid mode in search space: %q", m)
		}
		seen[m] = true
	}
	for _, want := range []ConfluenceMode{ConfluenceAll, ConfluenceFibonacciOrderBlock, ConfluenceFibonacciImbalance} {
		if !seen[want] {
			t.Fatalf("missing mode %q in search space", want)
		}
	}
	if modes[0] != ConfluenceAll {
		t.Fatalf("the search must start with the strictest mode 3/3, got %q", modes[0])
	}
	if got := ConfluenceAll.RequiredScore(); got != 3 {
		t.Fatalf("3/3 must require three components, got %d", got)
	}
	if got := ConfluenceTwoOfThree.RequiredScore(); got != 2 {
		t.Fatalf("2/3 must require two components, got %d", got)
	}
	for _, m := range []ConfluenceMode{ConfluenceFibonacciOrderBlock, ConfluenceFibonacciImbalance} {
		if got := m.RequiredScore(); got != 2 {
			t.Fatalf("%s must require two components, got %d", m, got)
		}
	}
}

func TestConfluenceTwoOfThreeRequiresFibonacci(t *testing.T) {
	ks := []domain.Kline{{Open: 100, High: 100.5, Low: 99.5, Close: 100, Start: time.Unix(1, 0)}}
	fib, _ := NewFibonacci(90, 110, TrendBullish, DefaultFibConfig())
	cfg := ConfluenceConfig{Mode: ConfluenceTwoOfThree, MaxZoneDistancePct: 0.01}

	pairs := []struct {
		name   string
		imbs   []Imbalance
		blocks []OrderBlock
	}{
		{"fib+ob", nil, []OrderBlock{{Index: 0, Low: 99.5, High: 100.5, Direction: OrderBlockBullish, Valid: true}}},
		{"fib+imbalance", []Imbalance{{Index: 0, Low: 99.5, High: 100.5, Direction: ImbalanceBullish, IsFilled: false}}, nil},
	}
	// La puntuación cuenta TODA la evidencia confluente, no solo la exigida: un
	// setup con los tres componentes vale más que uno que cumple por la vía larga.
	for _, p := range pairs {
		setup, ok := EvaluateConfluenceAt(ks, 0, Structure{Trend: TrendBullish}, fib, p.imbs, p.blocks, cfg)
		if !ok || !setup.Valid {
			t.Fatalf("%s must satisfy 2/3: %+v", p.name, setup)
		}
		if setup.Score < 2 {
			t.Fatalf("%s must score at least 2, got %d", p.name, setup.Score)
		}
	}

	// Un único componente nunca basta en 2/3. ok=true solo indica que se pudo
	// MEDIR la evidencia; la suficiencia la decide Valid.
	setup, _ := EvaluateConfluenceAt(ks, 0, Structure{Trend: TrendBullish}, fib, nil, nil, cfg)
	if setup.Valid || setup.Score != 1 {
		t.Fatalf("a single component must not satisfy 2/3: %+v", setup)
	}
}

func TestConfluenceAllStillRequiresEveryComponent(t *testing.T) {
	ks := []domain.Kline{{Open: 100, High: 100.5, Low: 99.5, Close: 100, Start: time.Unix(1, 0)}}
	fib, _ := NewFibonacci(90, 110, TrendBullish, DefaultFibConfig())
	blocks := []OrderBlock{{Index: 0, Low: 99.5, High: 100.5, Direction: OrderBlockBullish, Valid: true}}
	imbs := []Imbalance{{Index: 0, Low: 99.5, High: 100.5, Direction: ImbalanceBullish}}
	cfg := ConfluenceConfig{Mode: ConfluenceAll, MaxZoneDistancePct: 0.01}

	setup, ok := EvaluateConfluenceAt(ks, 0, Structure{Trend: TrendBullish}, fib, imbs, blocks, cfg)
	if !ok || !setup.Valid || setup.Score != 3 {
		t.Fatalf("3/3 must accept all components: %+v", setup)
	}
	if _, ok := EvaluateConfluenceAt(ks, 0, Structure{Trend: TrendBullish}, fib, imbs, nil, cfg); ok {
		t.Fatal("3/3 must reject without order block")
	}
	if _, ok := EvaluateConfluenceAt(ks, 0, Structure{Trend: TrendBullish}, fib, nil, blocks, cfg); ok {
		t.Fatal("3/3 must reject without imbalance")
	}
}

func TestConfluenceDistanceToleranceIsClamped(t *testing.T) {
	if got := (ConfluenceConfig{Mode: ConfluenceAll, MaxZoneDistancePct: 0.5}).Normalize().MaxZoneDistancePct; got != MaxConfluenceDistanceLimit {
		t.Fatalf("expected distance clamped to %.2f%%, got %.4f", MaxConfluenceDistanceLimit*100, got)
	}
	if got := (ConfluenceConfig{Mode: "nonsense"}).Normalize().Mode; got != ConfluenceAll {
		t.Fatalf("an unknown mode must fall back to 3/3, got %q", got)
	}
	if got := (ConfluenceConfig{Mode: ConfluenceAll}).Normalize().MaxZoneDistancePct; got != DefaultConfluenceConfig().MaxZoneDistancePct {
		t.Fatalf("expected default distance %.4f, got %.4f", DefaultConfluenceConfig().MaxZoneDistancePct, got)
	}
}

// La tolerancia acota la DISTANCIA a la zona, no el contenido de la vela: una
// zona lejana se rechaza por config, aunque la vela la atraviese.
func TestConfluenceToleranceMeasuresZoneDistance(t *testing.T) {
	ks := []domain.Kline{{Open: 100, High: 100.5, Low: 99.5, Close: 100, Start: time.Unix(1, 0)}}
	fib, _ := NewFibonacci(90, 110, TrendBullish, DefaultFibConfig())
	blocks := []OrderBlock{{Index: 0, Low: 98.5, High: 99, Direction: OrderBlockBullish, Valid: true}}

	ev, ok := CollectConfluenceEvidence(ks, 0, Structure{Trend: TrendBullish}, fib, nil, blocks)
	if !ok {
		t.Fatal("expected evidence for the order block component")
	}
	if ev.OrderBlockIndex != 0 {
		t.Fatalf("expected the order block to be measured, got %+v", ev)
	}
	if ev.DistanceToOrderBlock > 0.02 || ev.DistanceToOrderBlock <= 0.01 {
		t.Fatalf("a block at ~98.75 must be ~1.25%% away, got %.4f", ev.DistanceToOrderBlock)
	}
	if _, valid := ev.Setup(ConfluenceConfig{Mode: ConfluenceFibonacciOrderBlock, MaxZoneDistancePct: 0.01}); valid {
		t.Fatal("a block 1.25% away must not satisfy a 1% tolerance")
	}
	if _, valid := ev.Setup(ConfluenceConfig{Mode: ConfluenceFibonacciOrderBlock, MaxZoneDistancePct: 0.02}); !valid {
		t.Fatal("a block 1.25% away must satisfy a 2% tolerance")
	}
	// El mismo order block medido con la zona a 10% decide distinto según la
	// tolerancia: la evidencia se mide una vez, la decisión es por configuración.
	far := []OrderBlock{{Index: 0, Low: 90, High: 90.5, Direction: OrderBlockBullish, Valid: true}}
	farEv, ok := CollectConfluenceEvidence(ks, 0, Structure{Trend: TrendBullish}, fib, nil, far)
	if !ok || farEv.OrderBlockIndex != 0 {
		t.Fatalf("expected the distant block to be measured, got %+v", farEv)
	}
	if _, valid := farEv.Setup(ConfluenceConfig{Mode: ConfluenceFibonacciOrderBlock, MaxZoneDistancePct: 0.02}); valid {
		t.Fatal("a block 10% away must not satisfy the 2% profile tolerance")
	}
}

// Un imbalance que contenga el precio sigue siendo válido como SOPORTE: exigir
// price ∈ [Low, High] era lo que hacía la confluencia con imbalance imposible.
// Lo que sí lo invalida es que el precio lo haya perforado por debajo, porque
// entonces el hueco quedaría llenado.
func TestConfluenceTreatsUnfilledImbalanceAsSupport(t *testing.T) {
	ks := []domain.Kline{{Open: 100, High: 100.5, Low: 99.5, Close: 100, Start: time.Unix(1, 0)}}
	fib, _ := NewFibonacci(90, 110, TrendBullish, DefaultFibConfig())
	cfg := ConfluenceConfig{Mode: ConfluenceFibonacciImbalance, MaxZoneDistancePct: 0.02}
	around := []Imbalance{{Index: 0, Low: 99, High: 101, Direction: ImbalanceBullish}}
	under := []Imbalance{{Index: 0, Low: 98, High: 99, Direction: ImbalanceBullish}}
	broken := []Imbalance{{Index: 0, Low: 100.5, High: 102, Direction: ImbalanceBullish}}
	filled := []Imbalance{{Index: 0, Low: 99, High: 101, Direction: ImbalanceBullish, IsFilled: true}}

	if _, ok := EvaluateConfluenceAt(ks, 0, Structure{Trend: TrendBullish}, fib, around, nil, cfg); !ok {
		t.Fatal("an unfilled imbalance around the price must support the setup")
	}
	if _, ok := EvaluateConfluenceAt(ks, 0, Structure{Trend: TrendBullish}, fib, under, nil, cfg); !ok {
		t.Fatal("an unfilled imbalance below the price must support the setup")
	}
	if _, ok := EvaluateConfluenceAt(ks, 0, Structure{Trend: TrendBullish}, fib, broken, nil, cfg); ok {
		t.Fatal("an imbalance the price has already broken through must not support the setup")
	}
	if _, ok := EvaluateConfluenceAt(ks, 0, Structure{Trend: TrendBullish}, fib, filled, nil, cfg); ok {
		t.Fatal("a filled imbalance must not support the setup")
	}
}

func TestConfluenceEvidenceSeparatesCurrentPriceFromFibPrice(t *testing.T) {
	ks := []domain.Kline{{Open: 100, High: 100.5, Low: 99.5, Close: 100, Start: time.Unix(1, 0)}}
	fib, _ := NewFibonacci(90, 110, TrendBullish, DefaultFibConfig())
	ev, ok := CollectConfluenceEvidence(ks, 0, Structure{Trend: TrendBullish}, fib, nil, nil)
	if !ok {
		t.Fatal("expected evidence from the fibonacci component")
	}
	if ev.CurrentPrice != 100 {
		t.Fatalf("expected current price 100, got %.4f", ev.CurrentPrice)
	}
	// El precio Fibonacci es el punto medio de la zona que contiene al precio: son
	// magnitudes distintas y por eso se persisten por separado.
	level, dist := fibonacciReference(fib, ev.FibZone, 100)
	if ev.FibonacciPrice != level || ev.DistanceToFibonacci != dist {
		t.Fatalf("expected fib level %.4f at %.4f distance, got %.4f/%.4f", level, dist, ev.FibonacciPrice, ev.DistanceToFibonacci)
	}
	if ev.FibonacciPrice == ev.CurrentPrice && ev.FibZone == 0 {
		t.Fatal("a price outside every fib zone must not report a fib level")
	}
}

func TestParseConfluenceMode(t *testing.T) {
	for _, in := range []string{"all", "ALL", " order_block_imbalance "} {
		if _, ok := ParseConfluenceMode(in); !ok {
			t.Fatalf("expected %q to parse", in)
		}
	}
	if _, ok := ParseConfluenceMode("three_of_five"); ok {
		t.Fatal("unknown modes must not parse")
	}
}

func TestSearchSpaceBuildsExpectedConfigurations(t *testing.T) {
	cfg := DefaultLearnConfig()
	space := cfg.Search
	all := space.Configs(cfg)
	if len(all) != 48 {
		t.Fatalf("expected 4 modes x 4 distances x 3 stops = 48 configurations, got %d", len(all))
	}
	if space.Size() != 48 {
		t.Fatalf("Size must report 48, got %d", space.Size())
	}

	distances := map[float64]bool{}
	stops := map[float64]bool{}
	for _, c := range all {
		distances[c.Confluence.MaxZoneDistancePct] = true
		stops[c.TradePlan.MaxStopPct] = true
		if !c.TradePlan.Valid() {
			t.Fatalf("configuration with an out-of-profile stop: %+v", c)
		}
		if c.Confluence.MaxZoneDistancePct > MaxConfluenceDistanceLimit {
			t.Fatalf("configuration exceeds the distance limit: %+v", c)
		}
	}
	for _, d := range []float64{0.005, 0.010, 0.015, 0.020} {
		if !distances[d] {
			t.Fatalf("missing distance %.3f in the grid", d)
		}
	}
	for _, s := range []float64{0.010, 0.015, 0.020} {
		if !stops[s] {
			t.Fatalf("missing stop %.3f in the grid", s)
		}
	}

	// La grid descarta los valores fuera del perfil en lugar de aceptarlos: un 5%
	// de stop o un 8% de distancia no pertenecen a esta estrategia.
	wide := SearchSpace{
		Distances: []float64{0.01, 0.05, 0.08},
		Stops:     []float64{0.05, 0.10},
		Modes:     []ConfluenceMode{ConfluenceAll, ConfluenceOrderBlockImbalance, ConfluenceFibonacciOrderBlock},
	}
	clamped := wide.normalized()
	if len(clamped.Modes) != 3 {
		t.Fatalf("expected 3 valid modes, got %d", len(clamped.Modes))
	}
	if len(clamped.Distances) != 1 || clamped.Distances[0] != 0.01 {
		t.Fatalf("expected only the in-profile distance 0.01, got %v", clamped.Distances)
	}
	if len(clamped.Stops) != len(DefaultSearchSpace().Stops) {
		t.Fatalf("with no in-profile stop the default grid must apply, got %v", clamped.Stops)
	}
	for _, c := range clamped.Configs(cfg) {
		if !c.TradePlan.Valid() || c.Confluence.MaxZoneDistancePct > MaxConfluenceDistanceLimit {
			t.Fatalf("normalized grid kept an out-of-profile value: %+v", c)
		}
	}

	// Un espacio vacío no puede dejar al learner sin nada que probar.
	empty := SearchSpace{}.normalized()
	if len(empty.Configs(cfg)) != len(DefaultSearchSpace().Configs(cfg)) {
		t.Fatalf("an empty space must fall back to the default grid, got %d", len(empty.Configs(cfg)))
	}
}

// El orden de la grid es determinista: primero el criterio más estricto.
func TestSearchSpaceOrderIsDeterministic(t *testing.T) {
	cfg := DefaultLearnConfig()
	first := cfg.Search.Configs(cfg)
	second := cfg.Search.Configs(cfg)
	if len(first) != len(second) {
		t.Fatalf("grid length changed between calls: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Confluence != second[i].Confluence ||
			first[i].Confluence.MaxZoneDistancePct != second[i].Confluence.MaxZoneDistancePct ||
			first[i].TradePlan.MaxStopPct != second[i].TradePlan.MaxStopPct {
			t.Fatalf("grid order changed at %d", i)
		}
	}
	if first[0].Confluence.Mode != ConfluenceAll {
		t.Fatalf("expected 3/3 first, got %q", first[0].Confluence.Mode)
	}
}

func TestLearnDiagnosticsReportSelectedConfiguration(t *testing.T) {
	metrics := learnMetrics{trades: 12, profitFactor: 1.8}
	diag := LearnDiagnostics{Candles: 500, MinTrades: 8}
	recordSelectedConfig(&diag, DefaultLearnConfig(), metrics)

	summary := diag.Summary()
	for _, want := range []string{"velas evaluadas: 500", "setups detectados", "Configuración seleccionada:"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("missing %q in summary:\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "Mejor configuración:") {
		t.Fatalf("a selected configuration must not be labelled as best attempt:\n%s", summary)
	}

	// Sin candidata, el informe describe el mejor intento visto.
	attempt := LearnDiagnostics{Candles: 100, MinTrades: 8}
	recordBestAttempt(&attempt, DefaultLearnConfig(), learnMetrics{trades: 3})
	if !strings.Contains(attempt.Summary(), "Mejor configuración:") {
		t.Fatalf("expected the best-attempt label:\n%s", attempt.Summary())
	}
	if attempt.Selected {
		t.Fatal("a best attempt is not a selected configuration")
	}

	// Con menos operaciones, el mejor intento no reemplaza al anterior.
	recordBestAttempt(&attempt, DefaultLearnConfig(), learnMetrics{trades: 1, profitFactor: 9})
	if attempt.BestTrades != 3 {
		t.Fatalf("a worse attempt must not overwrite the best one, got %d", attempt.BestTrades)
	}
}

func TestRecordedProfitFactorIsSerializable(t *testing.T) {
	if got := recordedProfitFactor(math.Inf(1)); got != MaxProfitFactorRecorded {
		t.Fatalf("an infinite profit factor must be capped, got %.2f", got)
	}
	if got := recordedProfitFactor(math.NaN()); got != 0 {
		t.Fatalf("NaN must become 0, got %.2f", got)
	}
	if got := recordedProfitFactor(1.9); got != 1.9 {
		t.Fatalf("a finite profit factor must be preserved, got %.2f", got)
	}
	if got := recordedMetric(math.Inf(-1)); got != 0 {
		t.Fatalf("a non-finite metric must become 0, got %.2f", got)
	}
}

func TestConfluenceTwoOfThreeRejectsOrderBlockImbalanceWithoutFibonacci(t *testing.T) {
	ks := []domain.Kline{{Open: 100, High: 100.5, Low: 99.5, Close: 100, Start: time.Unix(1, 0)}}
	fib, _ := NewFibonacci(50, 200, TrendBullish, DefaultFibConfig())
	cfg := ConfluenceConfig{Mode: ConfluenceTwoOfThree, MaxZoneDistancePct: 0.01}
	imbs := []Imbalance{{Index: 0, Low: 99.5, High: 100.5, Direction: ImbalanceBullish}}
	blocks := []OrderBlock{{Index: 0, Low: 99.5, High: 100.5, Direction: OrderBlockBullish, Valid: true}}
	setup, ok := EvaluateConfluenceAt(ks, 0, Structure{Trend: TrendBullish}, fib, imbs, blocks, cfg)
	if ok || setup.Valid {
		t.Fatalf("OB+Imbalance without Fibonacci must be rejected: %+v", setup)
	}
}
