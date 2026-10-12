package investingbulls

import (
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func zoneKline(close float64) []domain.Kline {
	return []domain.Kline{{Open: close, High: close + 0.5, Low: close - 0.5, Close: close, Start: time.Unix(1, 0)}}
}

// El precio en la zona 1 elige la zona 1: la zona 2 queda demasiado lejos de la
// tolerancia y no se fuerza una confluencia que la estrategia no respalda.
func TestSelectConfluenceZonePicksContainingZone(t *testing.T) {
	ks := zoneKline(100)
	fib, _ := NewFibonacci(90, 110, TrendBullish, DefaultFibConfig())
	imbs := []Imbalance{{Index: 0, Low: 99.5, High: 100.5, Direction: ImbalanceBullish}}
	blocks := []OrderBlock{{Index: 0, Low: 99.5, High: 100.5, Direction: OrderBlockBullish, Valid: true}}
	cfg := ConfluenceConfig{Mode: ConfluenceFibonacciOrderBlock, MaxZoneDistancePct: 0.02}

	setup, ok := SelectConfluenceZone(ks, 0, Structure{Trend: TrendBullish}, fib, imbs, blocks, nil, cfg)
	if !ok || setup.FibZone != 1 {
		t.Fatalf("expected zone 1 to win, got zone=%d ok=%t: %+v", setup.FibZone, ok, setup)
	}
}

// El precio en la zona 2 (retroceso profundo) elige la zona 2 aunque la zona 1
// exista: evaluar ambas zonas es justamente el objetivo.
func TestSelectConfluenceZonePicksDeepZone(t *testing.T) {
	ks := zoneKline(94.3)
	fib, _ := NewFibonacci(90, 110, TrendBullish, DefaultFibConfig())
	if fib.Zone(94.3) != 2 {
		t.Fatalf("fixture error: expected 94.3 in zone 2, got %d", fib.Zone(94.3))
	}
	imbs := []Imbalance{{Index: 0, Low: 93.5, High: 95, Direction: ImbalanceBullish}}
	blocks := []OrderBlock{{Index: 0, Low: 93.5, High: 95, Direction: OrderBlockBullish, Valid: true}}
	cfg := ConfluenceConfig{Mode: ConfluenceFibonacciOrderBlock, MaxZoneDistancePct: 0.02}

	setup, ok := SelectConfluenceZone(ks, 0, Structure{Trend: TrendBullish}, fib, imbs, blocks, nil, cfg)
	if !ok || setup.FibZone != 2 {
		t.Fatalf("expected zone 2 to win, got zone=%d ok=%t: %+v", setup.FibZone, ok, setup)
	}
}

// Un precio en tierra de nadie no activa ninguna zona: no se relaja la regla
// para producir una señal.
func TestSelectConfluenceZoneRejectsNoZone(t *testing.T) {
	ks := zoneKline(105)
	fib, _ := NewFibonacci(90, 110, TrendBullish, DefaultFibConfig())
	imbs := []Imbalance{{Index: 0, Low: 104.5, High: 105.5, Direction: ImbalanceBullish}}
	blocks := []OrderBlock{{Index: 0, Low: 104.5, High: 105.5, Direction: OrderBlockBullish, Valid: true}}
	cfg := ConfluenceConfig{Mode: ConfluenceFibonacciOrderBlock, MaxZoneDistancePct: 0.02}

	if setup, ok := SelectConfluenceZone(ks, 0, Structure{Trend: TrendBullish}, fib, imbs, blocks, nil, cfg); ok {
		t.Fatalf("no zone is within tolerance, expected rejection, got %+v", setup)
	}
}

// bullishEvidence construye la evidencia medida para una zona concreta con los
// componentes indicados: sirve para probar la regla de selección sin depender
// de una serie sintética.
func bullishEvidence(zone int, dist float64, imb, ob, sd bool) ConfluenceEvidence {
	ev := ConfluenceEvidence{
		Index: 0, Direction: domain.DirectionBuy, Trend: TrendBullish,
		CurrentPrice: 100, FibZone: zone, FibInZone: true, FibForced: true,
		FibonacciPrice: 100, DistanceToFibonacci: dist,
		ImbalanceIndex: -1, OrderBlockIndex: -1, SupplyDemandIndex: -1,
	}
	if imb {
		ev.ImbalanceIndex, ev.DistanceToImbalance = 0, 0.001
	}
	if ob {
		ev.OrderBlockIndex, ev.DistanceToOrderBlock = 0, 0.001
	}
	if sd {
		ev.SupplyDemandIndex, ev.DistanceToSupplyDemand = 0, 0.001
	}
	return ev
}

// Ante dos zonas válidas gana la de mayor puntuación de confluencia, no la que
// contiene el precio.
func TestSelectSetupFromEvidencePrefersHigherScore(t *testing.T) {
	cfg := ConfluenceConfig{Mode: ConfluenceThreeOfFour, MaxZoneDistancePct: 0.02}
	shallow := bullishEvidence(1, 0.015, true, true, false)
	deep := bullishEvidence(2, 0.015, true, true, true)

	sel, ok := SelectSetupFromEvidence([]ConfluenceEvidence{shallow, deep}, cfg)
	if !ok || sel.FibZone != 2 || sel.Score != 4 {
		t.Fatalf("the higher-score zone must win, got zone=%d score=%d ok=%t", sel.FibZone, sel.Score, ok)
	}
}

// A igualdad de puntuación gana la zona más cercana al cierre, con independencia
// del orden en que se midieron las evidencias.
func TestSelectSetupFromEvidenceBreaksTiesByDistance(t *testing.T) {
	cfg := ConfluenceConfig{Mode: ConfluenceThreeOfFour, MaxZoneDistancePct: 0.02}
	shallow := bullishEvidence(1, 0.015, true, true, false)
	deep := bullishEvidence(2, 0.001, true, true, false)

	for _, evs := range [][]ConfluenceEvidence{{shallow, deep}, {deep, shallow}} {
		sel, ok := SelectSetupFromEvidence(evs, cfg)
		if !ok || sel.FibZone != 2 {
			t.Fatalf("on a score tie the nearer zone must win regardless of order, got zone=%d ok=%t", sel.FibZone, ok)
		}
	}
}

// collectEvidenceForZones mide SIEMPRE las dos zonas en la misma vela: forzar
// una zona no obliga al precio a estar dentro de ella.
func TestCollectEvidenceForZonesMeasuresBothZones(t *testing.T) {
	ks := zoneKline(100)
	fib, _ := NewFibonacci(90, 110, TrendBullish, DefaultFibConfig())

	evs, ok := collectEvidenceForZones(ks, 0, Structure{Trend: TrendBullish}, fib, nil, nil, nil)
	if !ok {
		t.Fatal("expected both zones to be measurable")
	}
	if evs[0].FibZone != 1 || evs[1].FibZone != 2 {
		t.Fatalf("both Fibonacci zones must be measured, got %d and %d", evs[0].FibZone, evs[1].FibZone)
	}
	if !evs[0].FibInZone || evs[1].FibInZone {
		t.Fatalf("a close of 100 sits in zone 1 only: %+v", evs)
	}
	if !evs[0].FibForced || !evs[1].FibForced {
		t.Fatal("both zones must be flagged as forced to document the measurement")
	}
}
