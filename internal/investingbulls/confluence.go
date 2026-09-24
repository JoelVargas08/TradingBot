package investingbulls

import (
	"math"

	"tradingview-bot/internal/domain"
)

// ConfluenceConfig defines the minimum evidence required for a setup.
type ConfluenceConfig struct {
	RequireFibonacci   bool
	RequireImbalance   bool
	RequireOrderBlock  bool
	MaxZoneDistancePct float64
}

// DefaultConfluenceConfig reflects the three-factor confluence described in
// the source material: Fibonacci + Order Block + Imbalance.
func DefaultConfluenceConfig() ConfluenceConfig {
	return ConfluenceConfig{
		RequireFibonacci:   true,
		RequireImbalance:   true,
		RequireOrderBlock:  true,
		MaxZoneDistancePct: 0.01,
	}
}

// Setup is a candidate LONG/SHORT generated from confluence rather than from
// a single indicator.
type Setup struct {
	Index           int
	Direction       domain.Direction
	Trend           Trend
	FibZone         int
	ReferencePrice  float64
	ImbalanceIndex  int
	OrderBlockIndex int
	Score           int
	Valid           bool
}

// EvaluateConfluence evaluates the latest candle against already detected
// market structure, Fibonacci zones, imbalances and order blocks.
func EvaluateConfluence(
	ks []domain.Kline,
	structure Structure,
	fib Fibonacci,
	imbalances []Imbalance,
	blocks []OrderBlock,
	cfg ConfluenceConfig,
) []Setup {
	if len(ks) == 0 || cfg.MaxZoneDistancePct < 0 {
		return nil
	}

	out := make([]Setup, 0)
	for i := range ks {
		c := ks[i]
		direction := domain.DirectionBuy
		if structure.Trend == TrendBearish {
			direction = domain.DirectionSell
		} else if structure.Trend != TrendBullish {
			continue
		}

		zone := fib.Zone(c.Close)
		if cfg.RequireFibonacci && zone == 0 {
			continue
		}

		imbIdx := -1
		if cfg.RequireImbalance {
			imbIdx = nearestCompatibleImbalance(i, direction, c.Close, imbalances, cfg.MaxZoneDistancePct)
			if imbIdx < 0 {
				continue
			}
		}

		obIdx := -1
		if cfg.RequireOrderBlock {
			obIdx = nearestCompatibleOrderBlock(i, direction, c.Close, blocks, cfg.MaxZoneDistancePct)
			if obIdx < 0 {
				continue
			}
		}

		score := 0
		if zone > 0 {
			score++
		}
		if imbIdx >= 0 {
			score++
		}
		if obIdx >= 0 {
			score++
		}

		out = append(out, Setup{
			Index:           i,
			Direction:       direction,
			Trend:           structure.Trend,
			FibZone:         zone,
			ReferencePrice:  c.Close,
			ImbalanceIndex:  imbIdx,
			OrderBlockIndex: obIdx,
			Score:           score,
			Valid:           score >= requiredScore(cfg),
		})
	}
	return out
}

func requiredScore(cfg ConfluenceConfig) int {
	n := 0
	if cfg.RequireFibonacci {
		n++
	}
	if cfg.RequireImbalance {
		n++
	}
	if cfg.RequireOrderBlock {
		n++
	}
	return n
}

func nearestCompatibleImbalance(index int, direction domain.Direction, price float64, imbs []Imbalance, maxDistancePct float64) int {
	want := ImbalanceBullish
	if direction == domain.DirectionSell {
		want = ImbalanceBearish
	}

	best, bestDist := -1, math.MaxFloat64
	for i := range imbs {
		imb := imbs[i]
		if imb.Direction != want || imb.IsFilled || imb.Index > index {
			continue
		}
		if price < imb.Low || price > imb.High {
			continue
		}
		dist := 0.0
		if price != 0 {
			dist = math.Abs(price-((imb.Low+imb.High)/2)) / price
		}
		if dist <= maxDistancePct && dist < bestDist {
			best, bestDist = i, dist
		}
	}
	return best
}

func nearestCompatibleOrderBlock(index int, direction domain.Direction, price float64, blocks []OrderBlock, maxDistancePct float64) int {
	want := OrderBlockBullish
	if direction == domain.DirectionSell {
		want = OrderBlockBearish
	}

	best, bestDist := -1, math.MaxFloat64
	for i := range blocks {
		ob := blocks[i]
		if ob.Direction != want || !ob.Valid || ob.Index > index {
			continue
		}
		if price < ob.Low || price > ob.High {
			continue
		}
		dist := 0.0
		if price != 0 {
			dist = math.Abs(price-((ob.Low+ob.High)/2)) / price
		}
		if dist <= maxDistancePct && dist < bestDist {
			best, bestDist = i, dist
		}
	}
	return best
}
