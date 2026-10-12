package investingbulls

import (
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func fourComponentFixture() ([]domain.Kline, Fibonacci, []Imbalance, []OrderBlock, []SupplyDemandZone) {
	ks := []domain.Kline{{Open: 100, High: 105, Low: 98, Close: 100, Start: time.Unix(1, 0)}}
	fib, _ := NewFibonacci(90, 110, TrendBullish, DefaultFibConfig())
	imbs := []Imbalance{{Index: 0, Low: 99, High: 101, Direction: ImbalanceBullish}}
	blocks := []OrderBlock{{Index: 0, Low: 99, High: 101, Direction: OrderBlockBullish, Valid: true}}
	zones := []SupplyDemandZone{{Index: 0, Low: 99, High: 101, Direction: DemandZone, Valid: true}}
	return ks, fib, imbs, blocks, zones
}

func TestConfluenceAll4RequiresSupplyDemand(t *testing.T) {
	ks, fib, imbs, blocks, zones := fourComponentFixture()
	cfg := ConfluenceConfig{Mode: ConfluenceAll4, MaxZoneDistancePct: 0.02}

	setup, ok := EvaluateConfluenceAtZones(ks, 0, Structure{Trend: TrendBullish}, fib, imbs, blocks, zones, cfg)
	if !ok || !setup.Valid || setup.Score != 4 {
		t.Fatalf("4/4 must accept all four components: %+v ok=%t", setup, ok)
	}
	if setup.Mode != ConfluenceAll4 {
		t.Fatalf("setup must record the mode used, got %q", setup.Mode)
	}
	if setup.SupplyDemandIndex != 0 {
		t.Fatalf("setup must record the supply/demand index, got %d", setup.SupplyDemandIndex)
	}
	if _, ok := EvaluateConfluenceAtZones(ks, 0, Structure{Trend: TrendBullish}, fib, imbs, blocks, nil, cfg); ok {
		t.Fatal("4/4 must reject without a supply/demand zone")
	}
}

func TestConfluenceThreeOfFourRequiresFibonacciAndThree(t *testing.T) {
	ks, fib, imbs, blocks, _ := fourComponentFixture()
	cfg := ConfluenceConfig{Mode: ConfluenceThreeOfFour, MaxZoneDistancePct: 0.02}

	setup, ok := EvaluateConfluenceAtZones(ks, 0, Structure{Trend: TrendBullish}, fib, imbs, blocks, nil, cfg)
	if !ok || !setup.Valid || setup.Score != 3 {
		t.Fatalf("3/4 must accept three components: %+v ok=%t", setup, ok)
	}
	noFib, _ := NewFibonacci(50, 200, TrendBullish, DefaultFibConfig())
	if _, ok := EvaluateConfluenceAtZones(ks, 0, Structure{Trend: TrendBullish}, noFib, imbs, nil, nil, cfg); ok {
		t.Fatal("3/4 must reject without Fibonacci")
	}
}

func TestConfluenceFibonacciSupplyDemandPair(t *testing.T) {
	ks, fib, _, _, zones := fourComponentFixture()
	cfg := ConfluenceConfig{Mode: ConfluenceFibonacciSupplyDemand, MaxZoneDistancePct: 0.02}

	setup, ok := EvaluateConfluenceAtZones(ks, 0, Structure{Trend: TrendBullish}, fib, nil, nil, zones, cfg)
	if !ok || !setup.Valid || setup.Score != 2 {
		t.Fatalf("fib+supply_demand must be a valid pair: %+v ok=%t", setup, ok)
	}
	if _, ok := EvaluateConfluenceAtZones(ks, 0, Structure{Trend: TrendBullish}, fib, nil, nil, nil, cfg); ok {
		t.Fatal("the pair must reject without the supply/demand zone")
	}
}

func TestFaithfulConfluenceModes(t *testing.T) {
	modes := FaithfulConfluenceModes()
	if len(modes) != 6 {
		t.Fatalf("expected 6 faithful modes, got %d: %v", len(modes), modes)
	}
	if modes[0] != ConfluenceAll4 {
		t.Fatalf("the faithful search must start with the strictest 4/4, got %q", modes[0])
	}
	seen := map[ConfluenceMode]bool{}
	for _, m := range modes {
		if !m.Valid() {
			t.Fatalf("invalid mode in faithful space: %q", m)
		}
		seen[m] = true
	}
	for _, want := range []ConfluenceMode{ConfluenceAll4, ConfluenceThreeOfFour, ConfluenceFibonacciSupplyDemand} {
		if !seen[want] {
			t.Fatalf("missing mode %q in faithful space", want)
		}
	}
	if got := ConfluenceAll4.RequiredScore(); got != 4 {
		t.Fatalf("4/4 must require four components, got %d", got)
	}
	if got := ConfluenceThreeOfFour.RequiredScore(); got != 3 {
		t.Fatalf("3/4 must require three components, got %d", got)
	}
	if got := ConfluenceFibonacciSupplyDemand.RequiredScore(); got != 2 {
		t.Fatalf("fib+supply_demand must require two components, got %d", got)
	}
}

// El espacio de búsqueda por defecto debe incluir los modos de cuatro
// componentes: si no, el detector de oferta/demanda quedaría sin usar en el
// learner y la estrategia no podría validarse tal como se describe.
func TestDefaultSearchSpaceSearchesFourComponentModes(t *testing.T) {
	seen := map[ConfluenceMode]bool{}
	for _, m := range DefaultSearchSpace().Modes {
		seen[m] = true
	}
	for _, want := range []ConfluenceMode{ConfluenceAll4, ConfluenceThreeOfFour, ConfluenceFibonacciSupplyDemand} {
		if !seen[want] {
			t.Fatalf("the default search must include the four-component mode %q", want)
		}
	}
}

func TestConfluenceExcludeTestedOrderBlock(t *testing.T) {
	ks := []domain.Kline{{Open: 100, High: 105, Low: 98, Close: 100, Start: time.Unix(1, 0)}}
	fib, _ := NewFibonacci(90, 110, TrendBullish, DefaultFibConfig())
	imbs := []Imbalance{{Index: 0, Low: 99, High: 101, Direction: ImbalanceBullish}}
	blocks := []OrderBlock{{Index: 0, Low: 99, High: 101, Direction: OrderBlockBullish, Valid: true, Tested: true}}

	legacy := ConfluenceConfig{Mode: ConfluenceAll, MaxZoneDistancePct: 0.02}
	if setup, ok := EvaluateConfluenceAt(ks, 0, Structure{Trend: TrendBullish}, fib, imbs, blocks, legacy); !ok || !setup.Valid {
		t.Fatalf("a tested block must still count when exclusion is off: %+v ok=%t", setup, ok)
	}

	strict := ConfluenceConfig{Mode: ConfluenceAll, MaxZoneDistancePct: 0.02, ExcludeTestedOrderBlock: true}
	if _, ok := EvaluateConfluenceAt(ks, 0, Structure{Trend: TrendBullish}, fib, imbs, blocks, strict); ok {
		t.Fatal("a tested block must be excluded when the option is on")
	}
}
