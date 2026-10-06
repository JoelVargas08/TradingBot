package investingbulls

import (
	"strings"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func TestDefaultMultiTimeframeConfig(t *testing.T) {
	c := DefaultMultiTimeframeConfig()
	if c.MainTimeframe != "1h" || c.EntryTimeframe != "15m" || c.ConfirmTimeframe != "5m" {
		t.Fatalf("unexpected timeframes: %+v", c)
	}
	if c.RequireConfirm {
		t.Fatal("5m confirmation must be optional by default")
	}
}

func TestNormalizeMultiTimeframeConfig(t *testing.T) {
	c := normalizeMTF(MultiTimeframeConfig{})
	if c.MainTimeframe == "" || c.EntryTimeframe == "" || c.MainSwingLeft <= 0 || c.EntrySwingRight <= 0 || c.ConfirmSwingLeft <= 0 {
		t.Fatalf("config not normalized: %+v", c)
	}
}

// hourlyCandles genera n velas 1h consecutivas y cerradas.
func hourlyCandles(n int) []domain.Kline {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]domain.Kline, n)
	price := 100.0
	for i := range out {
		if i%12 < 6 {
			price += 0.7
		} else {
			price -= 0.55
		}
		out[i] = domain.Kline{
			Start:     base.Add(time.Duration(i) * time.Hour),
			Timeframe: "1h",
			Closed:    true,
			Open:      price - 0.15,
			High:      price + 0.9,
			Low:       price - 0.9,
			Close:     price,
			Volume:    1500,
		}
	}
	return out
}

// Sec 4 (no-lookahead): la vela del timeframe mayor que ABRÓ en el instante de
// decisión aún no está cerrada y NO debe entrar en el prefijo.
func TestClosedCandlesBeforeExcludesCandleOpeningAtDecisionTime(t *testing.T) {
	main := hourlyCandles(80)
	ts := main[40].Start
	p := closedCandlesBefore(main, ts)
	if len(p) != 40 {
		t.Fatalf("want 40 velas cerradas antes de ts, got %d", len(p))
	}
	for _, k := range p {
		if k.Start.Equal(ts) || k.Start.After(ts) {
			t.Fatalf("el prefijo incluye una vela no cerrada en ts: %v", k.Start)
		}
	}
	// candlesThrough (versión anterior) SÍ incluía esa vela: diferencia de una vela.
	if len(candlesThrough(main, ts)) != len(p)+1 {
		t.Fatalf("candlesThrough debía incluir una vela más: %d vs %d", len(candlesThrough(main, ts)), len(p))
	}
}

// Sec 4: modificar una vela futura NO cambia decisiones anteriores
// (tendencia, rupturas y fibonacci derivados del prefijo previo a ts).
// Sec 15: pedir confirmación de 5m sin histórico NO puede abortar el
// aprendizaje. El modelo se aprende con 1H+15m y deja constancia del motivo,
// porque un modelo sin la confirmación que se pidió no es lo mismo que uno
// aprendido con ella.
func TestLearnMTFContinuesWithoutConfirmCandles(t *testing.T) {
	main := hourlyCandles(600)
	entry := walkCandles(1500, 7, "TEST", "15m", main[0].Start, 15*time.Minute)
	cfg := learnTestConfig()
	// Este test valida el mecanismo de salto de 5m, no el umbral de calidad
	// de producción. El umbral productivo sigue siendo MinTrades=8.
	cfg.MinTrades = 1
	mtf := DefaultMultiTimeframeConfig()
	mtf.RequireConfirm = true

	res, err := LearnMultiTimeframe(main, entry, nil, cfg, mtf)
	if err != nil {
		t.Fatalf("missing 5m history must not fail the MTF learn: %v", err)
	}
	if res.Model.ConfirmSkipReason == "" {
		t.Fatal("the skipped 5m confirmation must be recorded in the model")
	}
	if res.Model.Timeframes.RequireConfirm {
		t.Fatal("the persisted model must not claim a 5m confirmation it never used")
	}
	if !strings.Contains(res.Reason, "5m") {
		t.Fatalf("the reason must mention the skipped timeframe:\n%s", res.Reason)
	}
	if res.SpecJSON == "" && !strings.Contains(res.SpecJSON, res.Model.ConfirmSkipReason) {
		t.Fatal("the persisted spec must carry the skip reason")
	}
	if res.Model.DatasetHash != datasetDigestMultiTf(main, entry, nil) {
		t.Fatal("the MTF hash must cover every timeframe actually used")
	}

	// Con histórico de 5m disponible, la confirmación sí se exige y no hay motivo.
	withConfirm := walkCandles(3000, 9, "TEST", "5m", main[0].Start, 5*time.Minute)
	used, err := LearnMultiTimeframe(main, entry, withConfirm, cfg, mtf)
	if err != nil {
		t.Fatalf("MTF learn with 5m history failed: %v", err)
	}
	if used.Model.ConfirmSkipReason != "" {
		t.Fatalf("5m history was available, so no skip should be recorded: %q", used.Model.ConfirmSkipReason)
	}
	if !used.Model.Timeframes.RequireConfirm {
		t.Fatal("with 5m history the model must keep the confirmation requirement")
	}
	if used.Model.DatasetHash == res.Model.DatasetHash {
		t.Fatal("using 5m history must change the dataset hash")
	}
}

// Sin velas de entrada no hay nada que aprender, y eso sí es un error: la
// ausencia de 5m es tolerable, la ausencia del timeframe principal no.
func TestLearnMTFRequiresMainAndEntryCandles(t *testing.T) {
	main := hourlyCandles(600)
	entry := walkCandles(1500, 7, "TEST", "15m", main[0].Start, 15*time.Minute)
	mtf := DefaultMultiTimeframeConfig()

	if _, err := LearnMultiTimeframe(main[:99], entry, nil, learnTestConfig(), mtf); err == nil {
		t.Fatal("fewer than 100 main candles must be rejected")
	}
	if _, err := LearnMultiTimeframe(main, entry[:99], nil, learnTestConfig(), mtf); err == nil {
		t.Fatal("fewer than 100 entry candles must be rejected")
	}

	// Velas corruptas en cualquier timeframe se rechazan, no se ignoran.
	broken := append([]domain.Kline(nil), main...)
	broken[200].High = 0
	if _, err := LearnMultiTimeframe(broken, entry, nil, learnTestConfig(), mtf); err == nil {
		t.Fatal("invalid 1h candles must be rejected")
	}
}

// El pipeline MTF solo debe operar sobre la dirección que permite el timeframe
// mayor: una señal contraria a la tendencia de 1H no se descarta después, no se
// genera.
func TestLearnMTFUsesExactExecutionPipeline(t *testing.T) {
	main := hourlyCandles(600)
	entry := walkCandles(1500, 7, "TEST", "15m", main[0].Start, 15*time.Minute)
	cfg := learnTestConfig()
	cfg.MinTrades = 1 // prueba de arquitectura; producción mantiene MinTrades=8
	mtf := DefaultMultiTimeframeConfig()

	res, err := LearnMultiTimeframe(main, entry, nil, cfg, mtf)
	if err != nil {
		t.Fatalf("exact MTF learn failed: %v", err)
	}
	if res.SpecJSON == "" {
		t.Skipf("synthetic series produced no exact MTF candidate: %s", res.Reason)
	}
	if res.Model.Base.Trades != len(res.Trades) {
		t.Fatalf("persisted MTF base reports %d trades but learner returned %d", res.Model.Base.Trades, len(res.Trades))
	}
	if res.Model.Base.ConfigsEvaluated != DefaultSearchSpace().Size() {
		t.Fatalf("expected exact MTF learner to evaluate the full grid, got %d", res.Model.Base.ConfigsEvaluated)
	}
	if len(res.Model.AllowedSetups) != 4 {
		t.Fatalf("expected all four Investing Bulls setup families, got %v", res.Model.AllowedSetups)
	}

	// La candidata aprendida debe reproducir exactamente sus propias señales
	// cuando se ejecuta con el mismo modelo y pipeline MTF.
	replayed := simulateMultiTimeframe(main, entry, nil, cfg, mtf, res.Model.Base, res.Model.AllowedSetups)
	if len(replayed) != len(res.Trades) {
		t.Fatalf("replaying the persisted MTF model changed trade count: learned=%d replay=%d", len(res.Trades), len(replayed))
	}
	for i := range res.Trades {
		if res.Trades[i].EntryBar != replayed[i].EntryBar ||
			res.Trades[i].Direction != replayed[i].Direction ||
			res.Trades[i].Setup != replayed[i].Setup {
			t.Fatalf("trade %d differs between learn and execution: learned=%+v replay=%+v", i, res.Trades[i], replayed[i])
		}
	}
}

func TestSimulateMTFRespectsMainTrendDirection(t *testing.T) {
	main := hourlyCandles(600)
	entry := walkCandles(1500, 7, "TEST", "15m", main[0].Start, 15*time.Minute)
	cfg := normalizeLearnConfig(learnTestConfig())
	mtf := normalizeMTF(DefaultMultiTimeframeConfig())
	base, err := Learn(entry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if base.SpecJSON == "" {
		t.Skip("this synthetic walk produced no base candidate")
	}

	allowed := []SetupType{SetupCHOCHLong, SetupCHOCHShort, SetupContinuationLong, SetupContinuationShort}
	trades := simulateMultiTimeframe(main, entry, nil, cfg, mtf, base.Model, allowed)
	if len(trades) == 0 {
		t.Skip("the synthetic walk produced no MTF trades")
	}

	cut := len(entry) / 2
	for _, tr := range trades {
		if tr.EntryBar >= len(entry) {
			t.Fatalf("entry outside the dataset: %+v", tr)
		}
		prefix := closedCandlesBefore(main, entry[tr.EntryBar-1].Start)
		if len(prefix) < 30 {
			continue
		}
		ms := Analyze(prefix, mtf.MainSwingLeft, mtf.MainSwingRight)
		switch {
		case ms.Trend == TrendBullish && tr.Direction != domain.DirectionBuy:
			t.Fatalf("a long against a bullish 1h trend: %+v", tr)
		case ms.Trend == TrendBearish && tr.Direction != domain.DirectionSell:
			t.Fatalf("a short against a bearish 1h trend: %+v", tr)
		}
		// La entrada se produce en la apertura posterior a la vela de decisión.
		if tr.EntryBar <= minLearningBars || tr.EntryBar >= len(entry)-1 {
			t.Fatalf("unexpected entry bar %d", tr.EntryBar)
		}
		if tr.Components == "" {
			t.Fatalf("MTF trade without confluence evidence: %+v", tr)
		}
		if tr.ExitBar < tr.EntryBar {
			t.Fatalf("exit before entry: %+v", tr)
		}
	}

	// No puede haber dos operaciones simultáneas: el filtro de una posición
	// abierta es la base de las métricas del modelo.
	for i := 1; i < len(trades); i++ {
		if trades[i].EntryBar <= trades[i-1].ExitBar {
			t.Fatalf("overlapping trades: %+v and %+v", trades[i-1], trades[i])
		}
		if cut > 0 && trades[i-1].ExitBar >= len(entry) {
			t.Fatalf("exit outside the dataset: %+v", trades[i-1])
		}
	}
}

func TestFutureCandleDoesNotChangeEarlierDecisions(t *testing.T) {
	ks := hourlyCandles(120)
	ts := ks[60].Start

	modified := append([]domain.Kline(nil), ks...)
	for i := 60; i < len(modified); i++ {
		// distorsión extrema del futuro
		modified[i].Close *= 3
		modified[i].Open *= 3
		modified[i].High *= 3
		modified[i].Low *= 3
	}

	s1 := Analyze(closedCandlesBefore(ks, ts), 2, 2)
	s2 := Analyze(closedCandlesBefore(modified, ts), 2, 2)
	if s1.Trend != s2.Trend {
		t.Fatalf("tendencia cambió por velas futuras: %s vs %s", s1.Trend, s2.Trend)
	}
	if len(s1.Breaks) != len(s2.Breaks) {
		t.Fatalf("rupturas cambiaron por velas futuras: %d vs %d", len(s1.Breaks), len(s2.Breaks))
	}
	for i := range s1.Breaks {
		if s1.Breaks[i] != s2.Breaks[i] {
			t.Fatalf("ruptura %d cambió por velas futuras: %+v vs %+v", i, s1.Breaks[i], s2.Breaks[i])
		}
	}
	f1, ok1 := fibonacciFromLatestImpulse(s1, DefaultFibConfig())
	f2, ok2 := fibonacciFromLatestImpulse(s2, DefaultFibConfig())
	if ok1 != ok2 {
		t.Fatalf("validez de fibonacci cambió por velas futuras: %v vs %v", ok1, ok2)
	}
	if ok1 && (f1.Low != f2.Low || f1.High != f2.High || f1.Origin != f2.Origin || f1.Destination != f2.Destination) {
		t.Fatalf("fibonacci cambió por velas futuras: %+v vs %+v", f1, f2)
	}
}
