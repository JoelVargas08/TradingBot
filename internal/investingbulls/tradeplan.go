package investingbulls

import (
	"math"

	"tradingview-bot/internal/domain"
)

// TradePlanConfig turns the stop/target rules from the source material into
// tunable parameters for backtesting. The source repeatedly uses the previous
// high/low as the target and limits the stop distance to roughly 2%.
type TradePlanConfig struct {
	MaxStopPct    float64
	StopBufferPct float64
	UseOrderBlockStop bool
}

// DefaultTradePlanConfig uses the source's approximately 2% maximum stop.
func DefaultTradePlanConfig() TradePlanConfig {
	return TradePlanConfig{
		MaxStopPct:       0.02,
		StopBufferPct:   0.001,
		UseOrderBlockStop: true,
	}
}

type TradePlan struct {
	SetupIndex int
	Direction domain.Direction
	Entry     float64
	StopLoss  float64
	TakeProfit float64
	RiskPct   float64
	RewardPct float64
	Valid     bool
	Reason    string
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
	if len(ks) == 0 || cfg.MaxStopPct <= 0 || cfg.StopBufferPct < 0 {
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

		switch setup.Direction {
		case domain.DirectionBuy:
			if target, stop, ok := longLevels(entry, setup, structure, blocks, cfg); ok {
				plans = append(plans, makePlan(setup, entry, stop, target))
			}
		case domain.DirectionSell:
			if target, stop, ok := shortLevels(entry, setup, structure, blocks, cfg); ok {
				plans = append(plans, makePlan(setup, entry, stop, target))
			}
		}
	}
	return plans
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
	var risk, reward float64
	if setup.Direction == domain.DirectionBuy {
		risk = (entry - stop) / entry
		reward = (target - entry) / entry
	} else {
		risk = (stop - entry) / entry
		reward = (entry - target) / entry
	}
	return TradePlan{
		SetupIndex: setup.Index,
		Direction: setup.Direction,
		Entry: entry,
		StopLoss: stop,
		TakeProfit: target,
		RiskPct: risk,
		RewardPct: reward,
		Valid: reward > 0 && risk > 0,
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
