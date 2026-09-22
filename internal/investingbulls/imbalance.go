package investingbulls

import "tradingview-bot/internal/domain"

// ImbalanceDirection identifies the direction of a three-candle imbalance.
type ImbalanceDirection string

const (
	ImbalanceBullish ImbalanceDirection = "bullish"
	ImbalanceBearish ImbalanceDirection = "bearish"
)

// Imbalance is a price gap between the first and third candle of a
// three-candle sequence. It is intentionally modeled independently from
// Order Blocks so the learner can test their confluence later.
type Imbalance struct {
	Index     int
	High      float64
	Low       float64
	Direction ImbalanceDirection
	CreatedAt int64
	FilledPct float64
	IsFilled  bool
}

// ImbalanceConfig controls the minimum size and fill tolerance.
// MinGapPct is measured against the middle candle close.
type ImbalanceConfig struct {
	MinGapPct       float64
	FullFillEpsilon float64
}

func DefaultImbalanceConfig() ImbalanceConfig {
	return ImbalanceConfig{
		MinGapPct:       0,
		FullFillEpsilon: 0,
	}
}

// DetectImbalances finds three-candle fair-value gaps.
//
// Bullish: current candle low > candle two-bars-back high.
// Bearish: current candle high < candle two-bars-back low.
//
// The middle candle is deliberately not used to define the gap itself.
func DetectImbalances(ks []domain.Kline, cfg ImbalanceConfig) []Imbalance {
	if len(ks) < 3 || cfg.MinGapPct < 0 || cfg.FullFillEpsilon < 0 {
		return nil
	}

	out := make([]Imbalance, 0)
	for i := 2; i < len(ks); i++ {
		older := ks[i-2]
		current := ks[i]

		if current.Low > older.High {
			gap := current.Low - older.High
			if qualifiesGap(gap, older.High, cfg.MinGapPct) {
				out = append(out, Imbalance{
					Index:     i,
					High:      current.Low,
					Low:       older.High,
					Direction: ImbalanceBullish,
					CreatedAt: current.Start.UnixMilli(),
				})
			}
		}

		if current.High < older.Low {
			gap := older.Low - current.High
			if qualifiesGap(gap, older.Low, cfg.MinGapPct) {
				out = append(out, Imbalance{
					Index:     i,
					High:      older.Low,
					Low:       current.High,
					Direction: ImbalanceBearish,
					CreatedAt: current.Start.UnixMilli(),
				})
			}
		}
	}
	return out
}

// UpdateImbalances updates fill state using subsequent candle ranges.
// A bullish gap fills as price trades down to its low boundary; a bearish
// gap fills as price trades up to its high boundary. FilledPct is capped
// between 0 and 1.
func UpdateImbalances(imbs []Imbalance, ks []domain.Kline, cfg ImbalanceConfig) []Imbalance {
	out := make([]Imbalance, len(imbs))
	copy(out, imbs)

	for i := range out {
		imb := &out[i]
		rangeSize := imb.High - imb.Low
		if rangeSize <= 0 {
			continue
		}

		for j := imb.Index + 1; j < len(ks); j++ {
			c := ks[j]
			if imb.Direction == ImbalanceBullish {
				if c.Low <= imb.Low+cfg.FullFillEpsilon {
					imb.FilledPct = 1
					imb.IsFilled = true
					break
				}
				if c.Low < imb.High {
					pct := (imb.High - c.Low) / rangeSize
					if pct > imb.FilledPct {
						imb.FilledPct = clamp01(pct)
					}
				}
			} else {
				if c.High >= imb.High-cfg.FullFillEpsilon {
					imb.FilledPct = 1
					imb.IsFilled = true
					break
				}
				if c.High > imb.Low {
					pct := (c.High - imb.Low) / rangeSize
					if pct > imb.FilledPct {
						imb.FilledPct = clamp01(pct)
					}
				}
			}
		}
	}
	return out
}

func qualifiesGap(gap, reference, minGapPct float64) bool {
	if reference <= 0 {
		return false
	}
	return gap/reference >= minGapPct
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
