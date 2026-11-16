package investingbulls

import (
	"context"
	"encoding/json"
	"math/rand"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

// oldConfirmationMatches es la implementación anterior, conservada aquí para
// demostrar que la nueva (que además explica el resultado) decide lo mismo.
func oldConfirmationMatches(ks []domain.Kline, ts time.Time, dir domain.Direction, c MultiTimeframeConfig) bool {
	p := closedCandlesBefore(ks, ts)
	if len(p) < 10 {
		return false
	}
	s := Analyze(p, c.ConfirmSwingLeft, c.ConfirmSwingRight)
	if len(s.Breaks) == 0 {
		return false
	}
	return s.Breaks[len(s.Breaks)-1].Direction == dir
}

func TestConfirmationDiagnosticsDecidesExactlyLikeBefore(t *testing.T) {
	cfg := DefaultMultiTimeframeConfig()
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	seen := map[string]bool{}
	for seed := int64(1); seed <= 6; seed++ {
		ks := walkCandles(300, seed, "TEST", "5m", base, 5*time.Minute)
		for i := 0; i < len(ks); i += 7 {
			for _, dir := range []domain.Direction{domain.DirectionBuy, domain.DirectionSell} {
				d := confirmationDiagnostics(ks, ks[i].Start, dir, cfg)
				if d.OK != oldConfirmationMatches(ks, ks[i].Start, dir, cfg) {
					t.Fatalf("seed %d i %d %s: diagnóstico %v difiere de la regla", seed, i, dir, d.OK)
				}
				if !d.OK && (d.Reason == "" || d.Category == "") {
					t.Fatalf("rechazo sin motivo concreto: %+v", d)
				}
				seen[d.Reason] = true
			}
		}
	}
	for _, r := range []string{"confirmacion_5m_datos_insuficientes", "confirmacion_5m_quiebre_contrario"} {
		if !seen[r] {
			t.Errorf("no se ejercitó el motivo %s", r)
		}
	}
}

func TestFibonacciDiagnosticsAgreesWithDecision(t *testing.T) {
	cfg := DefaultFibConfig()
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for seed := int64(1); seed <= 20; seed++ {
		ks := walkCandles(60+int(seed)*5, seed, "TEST", "15m", base, 15*time.Minute)
		for _, n := range []int{3, 10, 25, len(ks)} {
			s := Analyze(ks[:n], 2, 2)
			_, ok := fibonacciFromLatestImpulse(s, cfg)
			d := fibonacciDiagnostics(s, cfg)
			if d.OK != ok {
				t.Fatalf("seed %d n %d: diagnóstico %v != decisión %v (%s)", seed, n, d.OK, ok, d.Reason)
			}
			if !ok && d.Category == "" {
				t.Fatalf("rechazo Fibonacci sin categoría")
			}
		}
	}
	if d := fibonacciDiagnostics(Structure{}, cfg); d.Category != CategoryMissingData {
		t.Fatalf("sin swings debe ser falta de datos, got %s", d.Category)
	}
}

func TestAuditZoneAgreesWithSetupDecision(t *testing.T) {
	rnd := rand.New(rand.NewSource(3))
	fib, ok := NewFibonacci(100, 200, TrendBullish, DefaultFibConfig())
	if !ok {
		t.Fatal("fib")
	}
	modes := []ConfluenceMode{ConfluenceAll, ConfluenceTwoOfThree, ConfluenceFibonacciOrderBlock, ConfluenceFibonacciImbalance, ConfluenceAll4, ConfluenceThreeOfFour, ConfluenceFibonacciSupplyDemand}
	for i := 0; i < 2000; i++ {
		e := ConfluenceEvidence{
			Direction: domain.DirectionBuy, Trend: TrendBullish, FibZone: 1 + rnd.Intn(2),
			FibInZone: rnd.Intn(2) == 0, FibForced: true, DistanceToFibonacci: rnd.Float64() * 0.03,
			ImbalanceIndex: rnd.Intn(3) - 1, DistanceToImbalance: rnd.Float64() * 0.03,
			OrderBlockIndex: rnd.Intn(3) - 1, DistanceToOrderBlock: rnd.Float64() * 0.03, OrderBlockTested: rnd.Intn(2) == 0,
			SupplyDemandIndex: rnd.Intn(3) - 1, DistanceToSupplyDemand: rnd.Float64() * 0.03,
		}
		cfg := ConfluenceConfig{Mode: modes[rnd.Intn(len(modes))], MaxZoneDistancePct: 0.005 + rnd.Float64()*0.015, ExcludeTestedOrderBlock: rnd.Intn(2) == 0}
		setup, sok := e.Setup(cfg)
		za := auditZone(nil, fib, e, cfg, nil, nil, nil)
		if za.Valid != (sok && setup.Valid) {
			t.Fatalf("caso %d: auditoría %v != decisión %v (%+v, %+v)", i, za.Valid, sok && setup.Valid, e, cfg)
		}
		if !za.Valid && za.Reason == "" {
			t.Fatalf("zona inválida sin motivo")
		}
		counted := 0
		for _, c := range za.Components {
			if c.Counted {
				counted++
			}
		}
		if sok && counted != setup.Score {
			t.Fatalf("componentes contados %d != score %d", counted, setup.Score)
		}
	}
}

// La vela 1h que CONTIENE a la vela 15m evaluada no está cerrada en el
// instante de decisión y no debe usarse (antes se usaba: lookahead/parcial).
func TestMainClosedAtExcludesTheHourStillForming(t *testing.T) {
	base := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	main := make([]domain.Kline, 0, 24)
	for i := 0; i < 24; i++ {
		main = append(main, domain.Kline{Start: base.Add(time.Duration(i) * time.Hour), Closed: true})
	}
	entryStart := base.Add(10*time.Hour + 30*time.Minute) // vela 15m 10:30–10:45
	old := closedCandlesBefore(main, entryStart)
	if last := old[len(old)-1].Start; !last.Equal(base.Add(10 * time.Hour)) {
		t.Fatalf("precondición: el criterio antiguo incluía la vela 10:00, got %s", last)
	}
	got := mainClosedAt(main, entryStart.Add(15*time.Minute), time.Hour)
	if last := got[len(got)-1].Start; !last.Equal(base.Add(9 * time.Hour)) {
		t.Fatalf("última 1h usada = %s, want 09:00 (la de 10:00 sigue abierta a las 10:45)", last)
	}
	// En la vela 15m 10:45–11:00 la de 10:00 ya cerró y sí se usa.
	got = mainClosedAt(main, base.Add(11*time.Hour), time.Hour)
	if last := got[len(got)-1].Start; !last.Equal(base.Add(10 * time.Hour)) {
		t.Fatalf("a las 11:00 debe usarse la vela 10:00, got %s", last)
	}
}

func TestTraceCoversEveryFoundationWithCategory(t *testing.T) {
	model := MultiTimeframeModel{Timeframes: DefaultMultiTimeframeConfig()}
	st := strategyWithSpec(t, model)
	main := hourlyCandles(300)
	entry := walkCandles(400, 11, "TEST", "15m", main[0].Start, 15*time.Minute)
	store := traceStore{byTF: map[string][]domain.Kline{"1h": main, "15m": entry}}
	for _, k := range entry[200:260] {
		_, ok, trace, err := EvaluateLiveWithTrace(context.Background(), store, st, k)
		if err != nil {
			t.Fatal(err)
		}
		if trace.TraceVersion != TraceVersion || trace.EngineVersion != EngineVersion || trace.StrategyID != "ib-test" {
			t.Fatalf("cabecera de traza incompleta: %+v", trace)
		}
		if !trace.DecisionTime.Equal(k.Start.Add(15 * time.Minute)) {
			t.Fatalf("decision_time = %s, want cierre de la vela 15m", trace.DecisionTime)
		}
		seen := map[string]bool{}
		for _, s := range trace.Stages {
			if s.Foundation == "" || s.Outcome == "" || s.Category == "" {
				t.Fatalf("etapa sin semántica de auditoría: %+v", s)
			}
			seen[s.Stage] = true
		}
		for _, c := range canonicalStages {
			if !seen[c.stage] {
				t.Fatalf("falta la etapa %s en la traza:\n%s", c.stage, trace.Summary())
			}
		}
		if !ok {
			if trace.FinalReason == "" || trace.FinalCategory == "" {
				t.Fatalf("rechazo sin motivo/categoría final: %+v", trace)
			}
			failDetail := false
			for _, s := range trace.Stages {
				if s.Status == StageFail && s.Detail != "" && s.Rule != "" {
					failDetail = true
				}
			}
			if !failDetail {
				t.Fatalf("el rechazo debe indicar regla y detalle:\n%s", trace.Summary())
			}
		}
		if len(trace.Data) != 2 || trace.Data[0].Status == "" {
			t.Fatalf("calidad de datos por timeframe ausente: %+v", trace.Data)
		}
		// La traza persistida conserva la semántica al releerla.
		b, _ := json.Marshal(trace)
		var back PipelineTrace
		if err := json.Unmarshal(b, &back); err != nil || back.FinalCategory != trace.FinalCategory || len(back.Stages) != len(trace.Stages) {
			t.Fatalf("roundtrip JSON: %v", err)
		}
	}
}

// Datos 1h atrasados son un problema de DATOS, no un rechazo de la estrategia.
func TestStaleMainDataIsReportedAsDataProblem(t *testing.T) {
	model := MultiTimeframeModel{Timeframes: DefaultMultiTimeframeConfig()}
	st := strategyWithSpec(t, model)
	main := hourlyCandles(300)
	entry := walkCandles(400, 11, "TEST", "15m", main[0].Start, 15*time.Minute)
	k := entry[300]
	// Quitar las 1h de las últimas 4 horas antes de la decisión.
	cut := closedCandlesBefore(main, k.Start.Add(-3*time.Hour))
	store := traceStore{byTF: map[string][]domain.Kline{"1h": cut, "15m": entry}}
	_, ok, trace, err := EvaluateLiveWithTrace(context.Background(), store, st, k)
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if trace.FinalReason != "datos_atrasados_principal" || trace.FinalCategory != CategoryStaleData || !trace.FinalCategory.IsDataProblem() {
		t.Fatalf("motivo=%s categoría=%s, want datos atrasados\n%s", trace.FinalReason, trace.FinalCategory, trace.Summary())
	}
	if trace.Data[0].LagBars < 1 || trace.Data[0].Status != "FAIL" {
		t.Fatalf("retraso no cuantificado: %+v", trace.Data[0])
	}
}

func TestStaleBarTraceIsDataProblem(t *testing.T) {
	k := domain.Kline{Symbol: "BTCUSDT", Timeframe: "15m", Start: time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)}
	decision := k.Start.Add(15 * time.Minute)
	tr := StaleBarTrace("ib", k, decision, decision.Add(47*time.Minute), 15*time.Minute)
	if tr.FinalReason != "vela_atrasada" || tr.FinalCategory != CategoryStaleData || tr.Accepted {
		t.Fatalf("traza de vela atrasada: %+v", tr)
	}
	if len(tr.Stages) != len(canonicalStages) {
		t.Fatalf("la traza debe listar todos los fundamentos como no evaluados: %d", len(tr.Stages))
	}
}
