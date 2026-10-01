package investingbulls

import (
	"math"

	"tradingview-bot/internal/domain"
)

// MaxStopPctLimit es el tope de stop del modelo Investing Bulls: la estrategia
// fuente limita la distancia del stop aproximadamente al 2%. El learner no puede
// seleccionar stops más anchos, de modo que un plan más ajustado es el único
// resultado admisible para este perfil.
const MaxStopPctLimit = 0.02

// TradePlanConfig turns the stop/target rules from the source material into
// tunable parameters for backtesting. The source repeatedly uses the previous
// high/low as the target and limits the stop distance to roughly 2%.
type TradePlanConfig struct {
	MaxStopPct        float64
	StopBufferPct     float64
	UseOrderBlockStop bool
}

// DefaultTradePlanConfig caps the stop distance at the source limit while
// leaving room for structure-based stops that sit beyond an order block.
func DefaultTradePlanConfig() TradePlanConfig {
	return TradePlanConfig{
		MaxStopPct:        MaxStopPctLimit,
		StopBufferPct:     0.001,
		UseOrderBlockStop: true,
	}
}

// Valid rejects configurations outside the Investing Bulls profile: a stop wider
// than MaxStopPctLimit does not belong to the strategy being learned.
func (c TradePlanConfig) Valid() bool {
	return c.MaxStopPct > 0 && c.MaxStopPct <= MaxStopPctLimit && c.StopBufferPct >= 0
}

// Normalize completes missing values and clamps MaxStopPct to the profile limit.
func (c TradePlanConfig) Normalize() TradePlanConfig {
	if c.MaxStopPct <= 0 {
		c.MaxStopPct = MaxStopPctLimit
	}
	if c.MaxStopPct > MaxStopPctLimit {
		c.MaxStopPct = MaxStopPctLimit
	}
	if c.StopBufferPct < 0 {
		c.StopBufferPct = 0
	}
	return c
}

type TradePlan struct {
	SetupIndex int
	Direction  domain.Direction
	Entry      float64
	StopLoss   float64
	TakeProfit float64
	RiskPct    float64
	RewardPct  float64
	Valid      bool
	Reason     string
}

// BuildTradePlans creates executable SL/TP candidates from valid setups.
// Long targets use the most recent confirmed swing high; short targets use
// the most recent confirmed swing low. Stops use the relevant structure/
// order-block boundary and are rejected when they exceed MaxStopPct.
func BuildTradePlans(
	ks []domain.Kline,
	structure Structure,
	blocks []OrderBlock,
	setups []Setup,
	cfg TradePlanConfig,
) []TradePlan {
	if len(ks) == 0 || !cfg.Valid() {
		return nil
	}

	plans := make([]TradePlan, 0, len(setups))
	for _, setup := range setups {
		if !setup.Valid || setup.Index < 0 || setup.Index >= len(ks) {
			continue
		}

		entry := ks[setup.Index].Close
		if entry <= 0 {
			continue
		}

		if plan, ok := BuildTradePlanAt(setup, entry, structure, blocks, cfg); ok {
			plans = append(plans, plan)
		}
	}
	return plans
}

// BuildTradePlanAt builds the plan of a single setup with an explicit entry
// price. The learner decides the entry on the following open, so it cannot be
// derived from the setup candle close the way BuildTradePlans does.
func BuildTradePlanAt(setup Setup, entry float64, structure Structure, blocks []OrderBlock, cfg TradePlanConfig) (TradePlan, bool) {
	if !cfg.Valid() || entry <= 0 || setup.Index < 0 {
		return TradePlan{}, false
	}

	switch setup.Direction {
	case domain.DirectionBuy:
		if target, stop, ok := longLevels(entry, setup, structure, blocks, cfg); ok {
			return makePlan(setup, entry, stop, target), true
		}
	case domain.DirectionSell:
		if target, stop, ok := shortLevels(entry, setup, structure, blocks, cfg); ok {
			return makePlan(setup, entry, stop, target), true
		}
	}
	return TradePlan{}, false
}

func longLevels(entry float64, setup Setup, structure Structure, blocks []OrderBlock, cfg TradePlanConfig) (float64, float64, bool) {
	target, ok := previousSwingTarget(structure.Swings, setup.Index, true)
	if !ok || target <= entry {
		return 0, 0, false
	}

	stop := math.Inf(1)
	if cfg.UseOrderBlockStop {
		if ob, ok := setupBlock(setup, blocks, OrderBlockBullish); ok {
			stop = ob.Low * (1 - cfg.StopBufferPct)
		}
	}
	if !isFinitePositive(stop) {
		if swing, ok := previousSwingLow(structure.Swings, setup.Index); ok {
			stop = swing * (1 - cfg.StopBufferPct)
		}
	}
	if !isFinitePositive(stop) || stop >= entry {
		return 0, 0, false
	}
	if (entry-stop)/entry > cfg.MaxStopPct {
		return 0, 0, false
	}
	return target, stop, true
}

func shortLevels(entry float64, setup Setup, structure Structure, blocks []OrderBlock, cfg TradePlanConfig) (float64, float64, bool) {
	target, ok := previousSwingTarget(structure.Swings, setup.Index, false)
	if !ok || target >= entry {
		return 0, 0, false
	}

	stop := 0.0
	if cfg.UseOrderBlockStop {
		if ob, ok := setupBlock(setup, blocks, OrderBlockBearish); ok {
			stop = ob.High * (1 + cfg.StopBufferPct)
		}
	}
	if stop <= 0 {
		if swing, ok := previousSwingHigh(structure.Swings, setup.Index); ok {
			stop = swing * (1 + cfg.StopBufferPct)
		}
	}
	if stop <= entry {
		return 0, 0, false
	}
	if (stop-entry)/entry > cfg.MaxStopPct {
		return 0, 0, false
	}
	return target, stop, true
}

func makePlan(setup Setup, entry, stop, target float64) TradePlan {
	return makePlanFor(setup.Index, setup.Direction, entry, stop, target)
}

func makePlanFor(index int, direction domain.Direction, entry, stop, target float64) TradePlan {
	var risk, reward float64
	if direction == domain.DirectionBuy {
		risk = (entry - stop) / entry
		reward = (target - entry) / entry
	} else {
		risk = (stop - entry) / entry
		reward = (entry - target) / entry
	}
	return TradePlan{
		SetupIndex: index,
		Direction:  direction,
		Entry:      entry,
		StopLoss:   stop,
		TakeProfit: target,
		RiskPct:    risk,
		RewardPct:  reward,
		Valid:      reward > 0 && risk > 0,
	}
}

func setupBlock(setup Setup, blocks []OrderBlock, want OrderBlockDirection) (OrderBlock, bool) {
	if setup.OrderBlockIndex < 0 || setup.OrderBlockIndex >= len(blocks) {
		return OrderBlock{}, false
	}
	ob := blocks[setup.OrderBlockIndex]
	return ob, ob.Valid && ob.Direction == want
}

func previousSwingTarget(swings []Swing, index int, high bool) (float64, bool) {
	var best Swing
	found := false
	for _, s := range swings {
		if s.Index >= index || s.High != high {
			continue
		}
		if !found || s.Index > best.Index {
			best, found = s, true
		}
	}
	if !found {
		return 0, false
	}
	return best.Price, true
}

func previousSwingLow(swings []Swing, index int) (float64, bool) {
	return previousSwingTarget(swings, index, false)
}

func previousSwingHigh(swings []Swing, index int) (float64, bool) {
	return previousSwingTarget(swings, index, true)
}

func isFinitePositive(v float64) bool {
	return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

// planInputs son los niveles de un setup YA resueltos con la información
// disponible hasta su vela. No dependen de la configuración de riesgo, solo de
// estructura, order block y Fibonacci: por eso el learner puede resolverlos una
// vez por vela y limitarse a aplicar el stop de cada configuración.
type planInputs struct {
	SetupIndex     int
	Direction      domain.Direction
	Entry          float64
	SwingTarget    float64
	HasSwingTarget bool
	OBStopAnchor   float64
	HasOBStop      bool
	FibStopAnchor  float64
	FibTarget      float64
	HasFibTarget   bool
}

// resolvePlanInputs calcula los niveles de entrada, objetivo y stop del setup.
//
// El objetivo sigue la estrategia fuente: el swing estructural más reciente al
// otro lado de la entrada y, si no existe, el nivel Fibonacci más cercano al otro
// lado (extensión si está en la dirección del impulso, borde de zona o extremo
// del propio impulso). El stop se ancla tras el order block y, en su defecto,
// tras la zona Fibonacci; ambos son anteriori a la vela del setup.
func resolvePlanInputs(setup Setup, fib Fibonacci, entry float64, structure Structure, blocks []OrderBlock) planInputs {
	in := planInputs{
		SetupIndex:    setup.Index,
		Direction:     setup.Direction,
		Entry:         entry,
		FibStopAnchor: fibStopAnchor(setup.Direction, fib),
	}
	if target, ok := previousSwingTarget(structure.Swings, setup.Index, setup.Direction == domain.DirectionBuy); ok {
		in.SwingTarget, in.HasSwingTarget = target, true
	}
	if target, ok := fibTarget(setup.Direction, fib, entry); ok {
		in.FibTarget, in.HasFibTarget = target, true
	}
	if cfg := DefaultTradePlanConfig(); cfg.UseOrderBlockStop {
		if ob, ok := setupBlock(setup, blocks, orderBlockSide(setup.Direction)); ok {
			in.OBStopAnchor, in.HasOBStop = ob.Low, true
		}
	}
	return in
}

// buildPlan aplica la configuración de riesgo a los niveles del setup.
func buildPlan(in planInputs, cfg TradePlanConfig) (TradePlan, bool) {
	if !cfg.Valid() || in.Entry <= 0 {
		return TradePlan{}, false
	}

	target, ok := in.SwingTarget, in.HasSwingTarget
	if !ok || !targetBeyondEntry(in.Direction, in.Entry, target) {
		target, ok = in.FibTarget, in.HasFibTarget
	}
	if !ok || !targetBeyondEntry(in.Direction, in.Entry, target) {
		return TradePlan{}, false
	}

	stop, ok := in.structuralStop(cfg)
	if !ok {
		return TradePlan{}, false
	}
	plan := makePlanFor(in.SetupIndex, in.Direction, in.Entry, stop, target)
	if !plan.Valid {
		return TradePlan{}, false
	}
	return plan, true
}

// structuralStop coloca el stop tras el order block o, en su defecto, tras la
// zona Fibonacci, siempre del lado contrario a la entrada. Si no hay ninguno de
// los dos anclajes utilizable, usa el propio límite de riesgo como stop.
//
// El stop nunca se "coloca" en un nivel que contradiga la estructura: si el
// ancla queda al lado equivocado o más lejos que MaxStopPct, el setup se
// descarta. Es la misma regla que aplica BuildTradePlanAt, de modo que el
// learner y la ejecución en vivo no pueden discrepar sobre una misma entrada.
func (in planInputs) structuralStop(cfg TradePlanConfig) (float64, bool) {
	anchor := in.OBStopAnchor
	if !cfg.UseOrderBlockStop || !in.HasOBStop || !isFinitePositive(anchor) {
		anchor = in.FibStopAnchor
	}
	if in.Direction == domain.DirectionBuy {
		if !isFinitePositive(anchor) || anchor >= in.Entry {
			anchor = in.Entry * (1 - cfg.MaxStopPct)
		}
		anchor *= 1 - cfg.StopBufferPct
		if !isFinitePositive(anchor) || anchor >= in.Entry {
			return 0, false
		}
		if (in.Entry-anchor)/in.Entry > cfg.MaxStopPct {
			return 0, false
		}
		return anchor, true
	}

	if !isFinitePositive(anchor) || anchor <= in.Entry {
		anchor = in.Entry * (1 + cfg.MaxStopPct)
	}
	anchor *= 1 + cfg.StopBufferPct
	if !isFinitePositive(anchor) || anchor <= in.Entry {
		return 0, false
	}
	if (anchor-in.Entry)/in.Entry > cfg.MaxStopPct {
		return 0, false
	}
	return anchor, true
}

// fibStopAnchor es el borde de la zona Fibonacci que protege la entrada: el
// extremo profundo de la zona 0.72–0.85.
func fibStopAnchor(direction domain.Direction, fib Fibonacci) float64 {
	if direction == domain.DirectionBuy {
		return fib.Zone2Low
	}
	return fib.Zone2High
}

// fibTarget devuelve el nivel Fibonacci más cercano al otro lado de la entrada.
// La escalera es simétrica: bordes de zona, extensiones e extremo del impulso.
func fibTarget(direction domain.Direction, fib Fibonacci, entry float64) (float64, bool) {
	if entry <= 0 {
		return 0, false
	}
	candidates := []float64{fib.Zone1Low, fib.Zone1High, fib.Zone2Low, fib.Zone2High, fib.Target1, fib.Target2, fib.Low, fib.High}
	best := 0.0
	found := false
	for _, c := range candidates {
		if !isFinitePositive(c) || !targetBeyondEntry(direction, entry, c) {
			continue
		}
		if !found {
			best, found = c, true
			continue
		}
		if direction == domain.DirectionBuy {
			if c < best {
				best = c
			}
			continue
		}
		if c > best {
			best = c
		}
	}
	return best, found
}

func targetBeyondEntry(direction domain.Direction, entry, target float64) bool {
	if !isFinitePositive(target) {
		return false
	}
	if direction == domain.DirectionBuy {
		return target > entry
	}
	return target < entry
}

func orderBlockSide(direction domain.Direction) OrderBlockDirection {
	if direction == domain.DirectionBuy {
		return OrderBlockBullish
	}
	return OrderBlockBearish
}
