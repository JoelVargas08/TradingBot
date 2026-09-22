package investingbulls

import "tradingview-bot/internal/domain"

// OrderBlockDirection identifies the side represented by an order block.
type OrderBlockDirection string

const (
	OrderBlockBullish OrderBlockDirection = "bullish"
	OrderBlockBearish OrderBlockDirection = "bearish"
)

// OrderBlock is a deterministic representation of the last opposite candle
// before a structural expansion. This is an operational definition used by
// the learner; the source material itself describes the concept qualitatively.
type OrderBlock struct {
	Index     int
	High      float64
	Low       float64
	Direction OrderBlockDirection
	CreatedAt int64
	Tested    bool
	Valid     bool
}

// OrderBlockConfig makes the subjective parts of the concept tunable.
type OrderBlockConfig struct {
	Lookback       int
	MinImpulsePct  float64
	InvalidateByWick bool
}

func DefaultOrderBlockConfig() OrderBlockConfig {
	return OrderBlockConfig{
		Lookback:         5,
		MinImpulsePct:   0,
		InvalidateByWick: true,
	}
}

// DetectOrderBlocks searches backwards from each confirmed structure break
// for the nearest candle opposite to the break direction.
//
// Bullish break -> nearest bearish candle.
// Bearish break -> nearest bullish candle.
//
// The impulse filter is measured from the candidate candle close to the
// break candle close. A later learner can optimize this definition.
func DetectOrderBlocks(ks []domain.Kline, breaks []StructureBreak, cfg OrderBlockConfig) []OrderBlock {
	if len(ks) == 0 || len(breaks) == 0 || cfg.Lookback < 1 || cfg.MinImpulsePct < 0 {
		return nil
	}

	out := make([]OrderBlock, 0, len(breaks))
	for _, br := range breaks {
		if br.Index < 0 || br.Index >= len(ks) {
			continue
		}

		dir, opposite := orderBlockDirection(br.Direction)
		start := br.Index - cfg.Lookback
		if start < 0 {
			start = 0
		}

		for i := br.Index - 1; i >= start; i-- {
			c := ks[i]
			if !isOppositeCandle(c, opposite) {
				continue
			}
			if !meetsImpulse(c.Close, ks[br.Index].Close, cfg.MinImpulsePct) {
				continue
			}

			out = append(out, OrderBlock{
				Index:     i,
				High:      c.High,
				Low:       c.Low,
				Direction: dir,
				CreatedAt: c.Start,
				Valid:     true,
			})
			break
		}
	}
	return out
}

// UpdateOrderBlocks marks an order block as tested when a later candle trades
// into its range. It becomes invalid if price closes through the far boundary.
// When InvalidateByWick is true, a wick through the boundary invalidates it.
func UpdateOrderBlocks(obs []OrderBlock, ks []domain.Kline, cfg OrderBlockConfig) []OrderBlock {
	out := make([]OrderBlock, len(obs))
	copy(out, obs)

	for i := range out {
		ob := &out[i]
		if !ob.Valid || ob.Index < 0 || ob.Index >= len(ks) {
			continue
		}

		for j := ob.Index + 1; j < len(ks); j++ {
			c := ks[j]

			if ob.Direction == OrderBlockBullish {
				if c.High >= ob.Low && c.Low <= ob.High {
					ob.Tested = true
				}
				if c.Close < ob.Low || (cfg.InvalidateByWick && c.Low < ob.Low) {
					ob.Valid = false
					break
				}
			} else {
				if c.High >= ob.Low && c.Low <= ob.High {
					ob.Tested = true
				}
				if c.Close > ob.High || (cfg.InvalidateByWick && c.High > ob.High) {
					ob.Valid = false
					break
				}
			}
		}
	}
	return out
}

func orderBlockDirection(d domain.Direction) (OrderBlockDirection, bool) {
	if d == domain.DirectionBuy {
		return OrderBlockBullish, true
	}
	return OrderBlockBearish, false
}

func isOppositeCandle(c domain.Kline, opposite bool) bool {
	if opposite {
		return c.Close < c.Open
	}
	return c.Close > c.Open
}

func meetsImpulse(candidateClose, breakClose, minPct float64) bool {
	if candidateClose <= 0 {
		return false
	}
	diff := breakClose - candidateClose
	if diff < 0 {
		diff = -diff
	}
	return diff/candidateClose >= minPct
}
