package investingbulls

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

// walkCandles genera un histórico determinista con impulses y retrocesos: la
// serie sintética sirve para ejercitar el learner de extremo a extremo, pero NO
// demuestra rentabilidad. La validación real ocurre sobre datos de mercado vía
// walk-forward OOS.
func walkCandles(n int, seed int64, symbol, timeframe string, start time.Time, step time.Duration) []domain.Kline {
	r := rand.New(rand.NewSource(seed))
	out := make([]domain.Kline, n)
	price := 100.0
	for i := range out {
		var drift float64
		switch (i / 12) % 4 {
		case 0, 2:
			drift = 0.9
		default:
			drift = -0.4
		}
		open := price
		price = math1Max(5, price+drift+(r.Float64()-0.5)*0.8)
		high := math1Max(open, price) + r.Float64()*0.4
		low := math1Min(open, price) - r.Float64()*0.4
		out[i] = domain.Kline{
			Start:     start.Add(time.Duration(i) * step),
			Timeframe: timeframe,
			Symbol:    symbol,
			Closed:    true,
			Open:      open,
			High:      high,
			Low:       low,
			Close:     price,
			Volume:    1000 + r.Float64()*500,
		}
	}
	return out
}

func math1Max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func math1Min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func learnTestConfig() LearnConfig {
	cfg := DefaultLearnConfig()
	cfg.Symbol = "TEST"
	cfg.Timeframe = "15m"
	cfg.MinTrades = 1
	return cfg
}

func TestLearnProducesCandidateWithSelectedConfiguration(t *testing.T) {
	ks := walkCandles(1500, 7, "TEST", "15m", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 15*time.Minute)

	res, err := Learn(ks, learnTestConfig())
	if err != nil {
		t.Fatalf("learn must not fail on valid candles: %v", err)
	}
	if res.SpecJSON == "" {
		t.Fatalf("expected a candidate, got reason:\n%s", res.Reason)
	}
	if res.Model.Trades < res.Model.MinTrades {
		t.Fatalf("a candidate below MinTrades must not be produced: %d", res.Model.Trades)
	}
	if res.Model.ConfigsEvaluated != DefaultSearchSpace().Size() {
		t.Fatalf("expected the whole grid to be evaluated, got %d", res.Model.ConfigsEvaluated)
	}

	// El modelo persistido debe describir la configuración que realmente produjo
	// sus operaciones, no una por defecto.
	if !res.Model.Confluence.Mode.Valid() {
		t.Fatalf("persisted an invalid confluence mode: %q", res.Model.Confluence.Mode)
	}
	if res.Model.Confluence.MaxZoneDistancePct > MaxConfluenceDistanceLimit {
		t.Fatalf("persisted distance out of profile: %.4f", res.Model.Confluence.MaxZoneDistancePct)
	}
	if !res.Model.TradePlan.Valid() {
		t.Fatalf("persisted an out-of-profile stop: %.4f", res.Model.TradePlan.MaxStopPct)
	}
	if res.Model.ConfluenceComponents == "" {
		t.Fatal("the candidate must record which components produced its trades")
	}
	if res.Model.DatasetHash != datasetDigest(ks) {
		t.Fatal("the candidate must record the hash of the candles it was measured on")
	}
	if res.Model.DatasetBars != len(ks) || !res.Model.DatasetStart.Equal(ks[0].Start) {
		t.Fatalf("unexpected dataset metadata: %+v", res.Model)
	}

	var traded int
	for _, tr := range res.Trades {
		if tr.Components == "" {
			t.Fatalf("trade without confluence evidence: %+v", tr)
		}
		traded++
	}
	if traded != res.Model.Trades {
		t.Fatalf("expected %d trades, got %d", res.Model.Trades, traded)
	}

	// El diagnóstico debe describir la configuración ELEGIDA, que es la que el
	// usuario inspecciona, no necesariamente la que más operaciones tuvo.
	if !res.Diagnostics.Selected {
		t.Fatal("a produced candidate must report a selected configuration")
	}
	if res.Diagnostics.BestConfluenceMode != string(res.Model.Confluence.Mode) {
		t.Fatalf("diagnostic mode %q does not match the model %q",
			res.Diagnostics.BestConfluenceMode, res.Model.Confluence.Mode)
	}
	if res.Diagnostics.BestDistancePct != res.Model.Confluence.MaxZoneDistancePct ||
		res.Diagnostics.BestStopPct != res.Model.TradePlan.MaxStopPct {
		t.Fatalf("diagnostic parameters do not match the model: %+v", res.Diagnostics)
	}
}

func TestLearnIsDeterministic(t *testing.T) {
	ks := walkCandles(800, 21, "TEST", "15m", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), 15*time.Minute)

	first, err := Learn(ks, learnTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Learn(ks, learnTestConfig())
	if err != nil {
		t.Fatal(err)
	}

	if first.Model.Confluence != second.Model.Confluence ||
		first.Model.TradePlan != second.Model.TradePlan ||
		first.Model.DatasetHash != second.Model.DatasetHash ||
		first.Model.Trades != second.Model.Trades {
		t.Fatal("two runs over the same candles produced different models")
	}
	if first.SpecJSON == "" {
		return
	}
	// El JSON no puede contener Instantes learned_at distintos entre corridas
	// comparables: el contenido de la estrategia debe ser idéntico.
	a := strings.Replace(first.SpecJSON, first.Model.LearnedAt.Format(time.RFC3339Nano), "", -1)
	b := strings.Replace(second.SpecJSON, second.Model.LearnedAt.Format(time.RFC3339Nano), "", -1)
	if a != b {
		t.Fatal("learned specs differ beyond the learning timestamp")
	}
}

// El datasetHash identifica el histórico con el que se midió una candidata: es
// lo que permite comprobar que un modelo se aplica al dataset correcto.
func TestDatasetHashIdentifiesCandles(t *testing.T) {
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	a := walkCandles(500, 3, "TEST", "15m", start, 15*time.Minute)
	b := walkCandles(500, 4, "TEST", "15m", start, 15*time.Minute)

	if datasetDigest(a) == datasetDigest(b) {
		t.Fatal("different candles must produce different dataset hashes")
	}
	if datasetDigest(a) != datasetDigest(append([]domain.Kline(nil), a...)) {
		t.Fatal("the same candles must hash the same")
	}

	tweaked := append([]domain.Kline(nil), a...)
	tweaked[250].Close += 1e-7
	if datasetDigest(tweaked) == datasetDigest(a) {
		t.Fatal("a price change must change the hash")
	}

	if datasetDigestMultiTf(a, nil) == datasetDigestMultiTf(a, a) {
		t.Fatal("the multi-timeframe hash must separate its timeframes")
	}
}

// El learner NO puede fabricar una candidata: sin evidencia de confluencia en el
// histórico la respuesta es un diagnóstico, no un modelo con números bajos.
func TestLearnRefusesCandidateWithoutEvidence(t *testing.T) {
	// Serie perfectamente plana: hay velas, pero ni tendencia ni zonas.
	ks := make([]domain.Kline, 400)
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	for i := range ks {
		ks[i] = domain.Kline{
			Start: base.Add(time.Duration(i) * 15 * time.Minute), Timeframe: "15m",
			Symbol: "TEST", Closed: true, Open: 100, High: 100.1, Low: 99.9, Close: 100, Volume: 10,
		}
	}

	res, err := Learn(ks, learnTestConfig())
	if err != nil {
		t.Fatalf("no evidence is a legitimate outcome, not an error: %v", err)
	}
	if res.SpecJSON != "" {
		t.Fatal("a flat dataset must not produce a candidate")
	}
	if res.Accepted {
		t.Fatal("a flat dataset cannot be accepted")
	}
	for _, want := range []string{"Learn base", "setups detectados: 0", "configuraciones evaluadas"} {
		if !strings.Contains(res.Reason, want) {
			t.Fatalf("missing %q in reason:\n%s", want, res.Reason)
		}
	}
}

// Las 48 configuraciones deben evaluarse aunque casi ninguna tenga suficientes
// operaciones: el diagnóstico explica si el fallo está en la generación de
// setups o en el filtro de riesgo.
func TestLearnEvaluatesWholeGridAndExplainsShortfall(t *testing.T) {
	// Pocas velas con tendencia: hay setups, pero no los suficientes.
	ks := walkCandles(120, 11, "TEST", "15m", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), 15*time.Minute)

	res, err := Learn(ks, learnTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if res.Diagnostics.ConfigsEvaluated != DefaultSearchSpace().Size() {
		t.Fatalf("expected the whole grid to be evaluated, got %d", res.Diagnostics.ConfigsEvaluated)
	}
	if res.SpecJSON == "" && res.Diagnostics.BestTrades > 0 && !strings.Contains(res.Reason, "Mejor configuración") {
		t.Fatalf("a shortfall must name the best attempt:\n%s", res.Reason)
	}
}

// MinTrades es una restricción de calidad: se respeta tal cual, nunca se relaja
// para poder devolver una candidata.
func TestLearnHonoursMinTrades(t *testing.T) {
	ks := walkCandles(1500, 7, "TEST", "15m", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 15*time.Minute)

	cfg := learnTestConfig()
	cfg.MinTrades = 400
	res, err := Learn(ks, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.SpecJSON != "" {
		t.Fatalf("MinTrades=400 must not yield a candidate, got %d trades", res.Model.Trades)
	}
	if res.Diagnostics.MinTrades != 400 {
		t.Fatalf("the diagnostic must report the requested MinTrades, got %d", res.Diagnostics.MinTrades)
	}
	if res.Diagnostics.ConfigsWithEnoughTrades != 0 {
		t.Fatalf("no configuration can reach 400 trades here, got %d", res.Diagnostics.ConfigsWithEnoughTrades)
	}

	cfg.MinTrades = 1
	if res, err = Learn(ks, cfg); err != nil {
		t.Fatal(err)
	}
	if res.SpecJSON == "" {
		t.Fatalf("MinTrades=1 should find a candidate, reason:\n%s", res.Reason)
	}
	if res.Model.MinTrades != 5 {
		t.Fatalf("the model must record the MinTrades used, got %d", res.Model.MinTrades)
	}
}

func TestLearnRejectsInvalidCandles(t *testing.T) {
	if _, err := Learn(make([]domain.Kline, 99), learnTestConfig()); err == nil {
		t.Fatal("fewer than 100 candles must be rejected")
	}

	ks := walkCandles(200, 5, "TEST", "15m", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), 15*time.Minute)
	ks[120].Close = 0
	if _, err := Learn(ks, learnTestConfig()); err == nil {
		t.Fatal("a candle with a non-positive close must be rejected")
	}

	ks = walkCandles(200, 5, "TEST", "15m", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), 15*time.Minute)
	ks[120].Symbol = "OTHER"
	if _, err := Learn(ks, learnTestConfig()); err == nil {
		t.Fatal("a candle from another symbol must be rejected")
	}

	ks = walkCandles(200, 5, "TEST", "15m", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), 15*time.Minute)
	ks[120].Start = ks[119].Start
	if _, err := Learn(ks, learnTestConfig()); err == nil {
		t.Fatal("out-of-order candles must be rejected")
	}
}

// Una decisión en la vela i no puede usar el resultado de la vela i+1: el
// proveedor del setup conoce su propio SL/TP, pero el learner solo debe poder
// tomar la entrada si el plan era ejecutable con la información de la vela i.
func TestDecisionPointsDoNotLookAhead(t *testing.T) {
	ks := walkCandles(600, 13, "TEST", "15m", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), 15*time.Minute)
	cfg := normalizeLearnConfig(learnTestConfig())

	points := collectDecisionPoints(ks, cfg, 0, nil)
	if len(points) == 0 {
		t.Skip("this synthetic walk produced no decision points")
	}
	last := len(ks) - 2
	for _, p := range points {
		if p.index < minLearningBars || p.index > last {
			t.Fatalf("decision at bar %d is outside the learnable range [%d, %d]", p.index, minLearningBars, last)
		}
		if p.entryBar != p.index+1 {
			t.Fatalf("entry must be the following open, got bar %d after %d", p.entryBar, p.index)
		}
		if p.evidence.Index != p.index || p.levels.SetupIndex != p.index {
			t.Fatalf("decision levels were resolved for another bar: %+v", p)
		}
	}

	// Alterar velas FUTURAS no puede cambiar ninguna decisión tomada antes de
	// ellas: si lo hiciera, el learner estaría usando información que el mercado
	// aún no tenía.
	cut := 400
	mutated := append([]domain.Kline(nil), ks...)
	for i := cut; i < len(mutated); i++ {
		mutated[i].Close *= 1.2
		mutated[i].High *= 1.2
		mutated[i].Low *= 1.2
	}

	beforeCut := decisionsBefore(collectDecisionPoints(ks, cfg, 0, nil), cut)
	afterCut := decisionsBefore(collectDecisionPoints(mutated, cfg, 0, nil), cut)

	if len(beforeCut) != len(afterCut) {
		t.Fatalf("mutating future candles changed past decisions: %d vs %d", len(beforeCut), len(afterCut))
	}
	for i := range beforeCut {
		if beforeCut[i] != afterCut[i] {
			t.Fatalf("decision at bar %d changed after mutating candles from %d on", beforeCut[i].index, cut)
		}
	}
}

func decisionsBefore(points []decisionPoint, cut int) []decisionPoint {
	var out []decisionPoint
	for _, p := range points {
		if p.index < cut {
			out = append(out, p)
		}
	}
	return out
}

// Lagrid de stops es una variable real: un stop más amplio admits entradas que
// uno más ajustado rechaza por distancia, sin que ninguna de las dos invente
// niveles que la estructura no respalda.
func TestTradePlanGridTradeoffIsMeaningful(t *testing.T) {
	in := planInputs{
		SetupIndex: 30, Direction: domain.DirectionBuy, Entry: 100,
		SwingTarget: 103, HasSwingTarget: true,
		OBStopAnchor: 98.6, HasOBStop: true,
	}
	tight := TradePlanConfig{MaxStopPct: 0.01, StopBufferPct: 0, UseOrderBlockStop: true}
	wide := TradePlanConfig{MaxStopPct: 0.02, StopBufferPct: 0, UseOrderBlockStop: true}

	// Stop estructural ≈1.40%: dentro del perfil de 2%, fuera del de 1%.
	if _, ok := buildPlan(in, wide); !ok {
		t.Fatal("a 1.40% structural stop must be admitted by the 2% profile")
	}
	if _, ok := buildPlan(in, tight); ok {
		t.Fatal("a 1.40% structural stop must be rejected by the 1% profile")
	}

	far := in
	far.OBStopAnchor = 97
	if _, ok := buildPlan(far, wide); ok {
		t.Fatal("a 3% structural stop must be rejected by a 2% profile")
	}

	// El buffer solo ensancha el stop: no puede hacer caber en el perfil un ancla
	// que ya lo excede.
	farWithBuffer := TradePlanConfig{MaxStopPct: 0.02, StopBufferPct: 0.005, UseOrderBlockStop: true}
	if _, ok := buildPlan(far, farWithBuffer); ok {
		t.Fatal("a buffer must never rescue an out-of-profile stop")
	}

	// Sin order block stop el ancla es la zona Fibonacci; sin ninguna de las dos,
	// el stop es el propio límite de riesgo.
	noOB := in
	noOB.OBStopAnchor, noOB.HasOBStop = 0, false
	if _, ok := buildPlan(noOB, tight); !ok {
		t.Fatal("without a structural anchor the profile stop must apply")
	}
}
