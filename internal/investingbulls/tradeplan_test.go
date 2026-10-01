package investingbulls

import (
	"math"
	"testing"

	"tradingview-bot/internal/domain"
)

func TestBuildLongTradePlan(t *testing.T) {
	ks := []domain.Kline{
		{Close: 100},
		{Close: 99.5},
		{Close: 100.5},
		{Close: 100},
	}
	structure := Structure{
		Trend: TrendBullish,
		Swings: []Swing{
			{Index: 1, Price: 95, High: false},
			{Index: 2, Price: 110, High: true},
		},
	}
	blocks := []OrderBlock{
		{Index: 1, Low: 99, High: 100.2, Direction: OrderBlockBullish, Valid: true},
	}
	setup := Setup{Index: 3, Direction: domain.DirectionBuy, OrderBlockIndex: 0, Valid: true}

	plans := BuildTradePlans(ks, structure, blocks, []Setup{setup}, DefaultTradePlanConfig())
	if len(plans) != 1 {
		t.Fatalf("got %d plans, want 1", len(plans))
	}
	p := plans[0]
	if math.Abs(p.StopLoss-99*0.999) > 1e-9 {
		t.Fatalf("unexpected stop: %.6f", p.StopLoss)
	}
	if p.TakeProfit != 110 {
		t.Fatalf("unexpected target: %.2f", p.TakeProfit)
	}
	if !p.Valid {
		t.Fatal("expected valid trade plan")
	}
}

func TestBuildShortTradePlan(t *testing.T) {
	ks := []domain.Kline{
		{Close: 110},
		{Close: 109.5},
		{Close: 110.5},
		{Close: 109},
	}
	structure := Structure{
		Trend: TrendBearish,
		Swings: []Swing{
			{Index: 1, Price: 115, High: true},
			{Index: 2, Price: 100, High: false},
		},
	}
	blocks := []OrderBlock{
		{Index: 1, Low: 108.5, High: 109.5, Direction: OrderBlockBearish, Valid: true},
	}
	setup := Setup{Index: 3, Direction: domain.DirectionSell, OrderBlockIndex: 0, Valid: true}

	plans := BuildTradePlans(ks, structure, blocks, []Setup{setup}, DefaultTradePlanConfig())
	if len(plans) != 1 {
		t.Fatalf("got %d plans, want 1", len(plans))
	}
	p := plans[0]
	if math.Abs(p.StopLoss-109.5*1.001) > 1e-9 {
		t.Fatalf("unexpected stop: %.6f", p.StopLoss)
	}
	if p.TakeProfit != 100 {
		t.Fatalf("unexpected target: %.2f", p.TakeProfit)
	}
	if !p.Valid {
		t.Fatal("expected valid trade plan")
	}
}

func TestTradePlanRejectsStopAboveMaximum(t *testing.T) {
	ks := []domain.Kline{{Close: 100}}
	structure := Structure{Swings: []Swing{{Index: 0, Price: 90, High: false}, {Index: -1, Price: 120, High: true}}}
	blocks := []OrderBlock{{Index: 0, Low: 90, High: 91, Direction: OrderBlockBullish, Valid: true}}
	setup := Setup{Index: 0, Direction: domain.DirectionBuy, OrderBlockIndex: 0, Valid: true}

	plans := BuildTradePlans(ks, structure, blocks, []Setup{setup}, DefaultTradePlanConfig())
	if len(plans) != 0 {
		t.Fatalf("expected oversized stop to be rejected, got %+v", plans)
	}
}

func TestTradePlanRequiresTargetBeyondEntry(t *testing.T) {
	ks := []domain.Kline{{Close: 100}}
	structure := Structure{Swings: []Swing{{Index: -1, Price: 95, High: false}, {Index: -1, Price: 99, High: true}}}
	setup := Setup{Index: 0, Direction: domain.DirectionBuy, Valid: true}

	if plans := BuildTradePlans(ks, structure, nil, []Setup{setup}, DefaultTradePlanConfig()); len(plans) != 0 {
		t.Fatalf("expected no plan without target above entry")
	}
}

// El límite del 2% es una restricción del PERFIL de la estrategia, no un valor
// que el learner pueda ampliar para conseguir más operaciones.
func TestTradePlanConfigClampsStopToProfileLimit(t *testing.T) {
	cfg := TradePlanConfig{MaxStopPct: 0.15, StopBufferPct: 0.001}.Normalize()
	if cfg.MaxStopPct != MaxStopPctLimit {
		t.Fatalf("expected stop clamped to %.2f%%, got %.4f", MaxStopPctLimit*100, cfg.MaxStopPct*100)
	}
	if (TradePlanConfig{MaxStopPct: 0.15}).Valid() {
		t.Fatal("a 15% stop is outside the Investing Bulls profile and must be invalid")
	}
	if zero := (TradePlanConfig{MaxStopPct: 0}); zero.Valid() {
		t.Fatal("a zero stop must be invalid")
	}
	fallback := TradePlanConfig{MaxStopPct: 0}.Normalize()
	if fallback.MaxStopPct != MaxStopPctLimit {
		t.Fatalf("expected default stop %.2f%%, got %.4f", MaxStopPctLimit*100, fallback.MaxStopPct)
	}
	if DefaultTradePlanConfig().MaxStopPct != MaxStopPctLimit {
		t.Fatalf("default stop must be the profile limit, got %.4f", DefaultTradePlanConfig().MaxStopPct)
	}
}

// Un ancla estructural más lejos que el stop máximo descarta el setup, tanto en
// la ejecución estricta como en el learner: inventar un stop que la estructura
// no respalda inflaría las métricas de cualquier configuración.
func TestTradePlanDropsSetupWhenStructuralStopTooWide(t *testing.T) {
	structure := Structure{Swings: []Swing{
		{Index: 0, Price: 99, High: false},
		{Index: 1, Price: 110, High: true},
	}}
	blocks := []OrderBlock{{Index: 0, Low: 95, High: 99.5, Direction: OrderBlockBullish, Valid: true}}
	setup := Setup{Index: 2, Direction: domain.DirectionBuy, OrderBlockIndex: 0, Valid: true}

	if _, ok := BuildTradePlanAt(setup, 100, structure, blocks, DefaultTradePlanConfig()); ok {
		t.Fatal("an order block 5% away must invalidate the strict plan")
	}

	in := planInputs{SetupIndex: 2, Direction: domain.DirectionBuy, Entry: 100, SwingTarget: 110, HasSwingTarget: true, OBStopAnchor: 95, HasOBStop: true}
	if _, ok := buildPlan(in, DefaultTradePlanConfig()); ok {
		t.Fatal("the learner must apply the same stop rule as the strict plan")
	}

	// Un ancla Fibonacci utilizable, en cambio, produce un stop estructural válido.
	withFib := planInputs{SetupIndex: 2, Direction: domain.DirectionBuy, Entry: 100, SwingTarget: 110, HasSwingTarget: true, FibStopAnchor: 99.5}
	plan, ok := buildPlan(withFib, DefaultTradePlanConfig())
	if !ok {
		t.Fatal("expected a plan anchored on the fib zone")
	}
	if math.Abs(plan.StopLoss-99.5*0.999) > 1e-9 {
		t.Fatalf("expected fib-anchored stop, got %.6f", plan.StopLoss)
	}

	if _, ok := buildPlan(planInputs{SetupIndex: 2, Direction: domain.DirectionBuy, Entry: 100}, DefaultTradePlanConfig()); ok {
		t.Fatal("a setup without target must not produce a plan")
	}
}

// BuildTradePlanAt es la vía del learner: la entrada es la apertura siguiente y
// no el cierre de la vela del setup.
func TestBuildTradePlanAtUsesExplicitEntry(t *testing.T) {
	structure := Structure{Swings: []Swing{
		{Index: 1, Price: 99, High: false},
		{Index: 2, Price: 110, High: true},
	}}
	blocks := []OrderBlock{{Index: 1, Low: 99.2, High: 100.4, Direction: OrderBlockBullish, Valid: true}}
	setup := Setup{Index: 3, Direction: domain.DirectionBuy, OrderBlockIndex: 0, Valid: true}

	plan, ok := BuildTradePlanAt(setup, 100.5, structure, blocks, DefaultTradePlanConfig())
	if !ok {
		t.Fatal("expected a plan with an explicit entry")
	}
	if plan.Entry != 100.5 || plan.SetupIndex != 3 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if math.Abs(plan.StopLoss-99.2*0.999) > 1e-9 || plan.TakeProfit != 110 {
		t.Fatalf("unexpected levels: %+v", plan)
	}

	if _, ok := BuildTradePlanAt(setup, 0, structure, blocks, DefaultTradePlanConfig()); ok {
		t.Fatal("a non-positive entry must not produce a plan")
	}
	if _, ok := BuildTradePlanAt(setup, 100.5, structure, blocks, TradePlanConfig{MaxStopPct: 0.15}); ok {
		t.Fatal("an out-of-profile stop must not produce a plan")
	}
}

// El objetivo Fibonacci sustituye al swing cuando no hay uno previo utilizable,
// y la escalera es simétrica para largos y cortos.
func TestFibTargetSymmetry(t *testing.T) {
	bull := Fibonacci{Low: 100, High: 120, Zone1Low: 117.8, Zone1High: 118.4, Zone2Low: 112.6, Zone2High: 113.4, Target1: 130, Target2: 140}
	bear := Fibonacci{Low: 120, High: 140, Zone1Low: 121.6, Zone1High: 122.2, Zone2Low: 126.6, Zone2High: 127.4, Target1: 110, Target2: 100}

	// El objetivo es el nivel MÁS CERCANO al otro lado de la entrada, de modo que
	// un largo y su espejo corto eligen niveles equivalentes.
	up, ok := fibTarget(domain.DirectionBuy, bull, 116)
	if !ok || up != 117.8 {
		t.Fatalf("expected nearest fib target above entry (117.8), got %.2f ok=%v", up, ok)
	}
	down, ok := fibTarget(domain.DirectionSell, bear, 124)
	if !ok || down != 122.2 {
		t.Fatalf("expected nearest fib target below entry (122.2), got %.2f ok=%v", down, ok)
	}
	// Superado el extremo del impulso, la siguiente referencia es la extensión.
	if ext, ok := fibTarget(domain.DirectionBuy, bull, 135); !ok || ext != 140 {
		t.Fatalf("expected extension 140 above the impulse, got %.2f ok=%v", ext, ok)
	}
	if _, ok := fibTarget(domain.DirectionBuy, bull, 141); ok {
		t.Fatal("no fib target exists above the highest extension")
	}
}
