package investingbulls

import "tradingview-bot/internal/domain"

// SupplyDemandDirection identifies the side represented by a supply/demand zone.
type SupplyDemandDirection string

const (
	DemandZone SupplyDemandDirection = "demand"
	SupplyZone SupplyDemandDirection = "supply"
)

// SupplyDemandZone is the base (consolidation) that a directional expansion
// displaced. It is modelled INDEPENDENTLY from OrderBlock: an order block is
// the single opposite candle before the break, while a supply/demand zone is
// the whole contiguous base of candles the expansion left behind, which is how
// the source material describes "zona de oferta/demanda".
type SupplyDemandZone struct {
	Index       int
	High        float64
	Low         float64
	Direction   SupplyDemandDirection
	CreatedAt   int64
	Tested      bool
	Valid       bool
	Invalidated bool
}

// SupplyDemandConfig makes the subjective parts of the concept tunable.
type SupplyDemandConfig struct {
	Lookback         int
	MaxBaseRangePct  float64
	MinImpulsePct    float64
	InvalidateByWick bool
}

func DefaultSupplyDemandConfig() SupplyDemandConfig {
	return SupplyDemandConfig{
		Lookback:         5,
		MaxBaseRangePct:  0.5,
		MinImpulsePct:    0,
		InvalidateByWick: true,
	}
}

// DetectSupplyDemandZones searches backwards from each confirmed structure break
// for the contiguous base of candles preceding the expansion candle. A candle
// belongs to the base while its range does not exceed MaxBaseRangePct of the
// expansion candle range; the first candle that breaks the condition stops the
// run. The zone is the envelope of that base.
//
// The impulse filter is measured from the base extreme to the break close and
// must be DIRECTIONAL: a demand zone needs a bullish impulse (break close above
// the base high) and a supply zone a bearish one.
func DetectSupplyDemandZones(ks []domain.Kline, breaks []StructureBreak, cfg SupplyDemandConfig) []SupplyDemandZone {
	if len(ks) == 0 || len(breaks) == 0 || cfg.Lookback < 1 || cfg.MaxBaseRangePct <= 0 || cfg.MinImpulsePct < 0 {
		return nil
	}

	out := make([]SupplyDemandZone, 0, len(breaks))
	for _, br := range breaks {
		if br.Index <= 0 || br.Index >= len(ks) {
			continue
		}

		dir, _ := supplyDemandDirection(br.Direction)
		expansion := ks[br.Index]
		expansionRange := expansion.High - expansion.Low
		if expansionRange <= 0 {
			continue
		}
		limit := cfg.MaxBaseRangePct * expansionRange

		start := br.Index - cfg.Lookback
		if start < 0 {
			start = 0
		}

		baseHigh, baseLow := 0.0, 0.0
		baseStart := -1
		for i := br.Index - 1; i >= start; i-- {
			c := ks[i]
			if c.High-c.Low > limit {
				break
			}
			if baseStart < 0 {
				baseHigh, baseLow = c.High, c.Low
			} else {
				if c.High > baseHigh {
					baseHigh = c.High
				}
				if c.Low < baseLow {
					baseLow = c.Low
				}
			}
			baseStart = i
		}
		if baseStart < 0 || baseHigh <= baseLow {
			continue
		}
		if !meetsSupplyDemandImpulse(baseLow, baseHigh, expansion.Close, cfg.MinImpulsePct, dir == DemandZone) {
			continue
		}

		out = append(out, SupplyDemandZone{
			Index:     baseStart,
			High:      baseHigh,
			Low:       baseLow,
			Direction: dir,
			CreatedAt: ks[baseStart].Start.UnixMilli(),
			Valid:     true,
		})
	}
	return out
}

// UpdateSupplyDemandZones marks a zone as tested when a later candle trades into
// its range. A demand zone becomes invalid when price closes below its low, a
// supply zone when price closes above its high. When InvalidateByWick is true, a
// wick through the boundary invalidates it.
func UpdateSupplyDemandZones(zones []SupplyDemandZone, ks []domain.Kline, cfg SupplyDemandConfig) []SupplyDemandZone {
	out := make([]SupplyDemandZone, len(zones))
	copy(out, zones)

	for i := range out {
		z := &out[i]
		if !z.Valid || z.Index < 0 || z.Index >= len(ks) {
			continue
		}

		for j := z.Index + 1; j < len(ks); j++ {
			c := ks[j]

			if c.High >= z.Low && c.Low <= z.High {
				z.Tested = true
			}
			if z.Direction == DemandZone {
				if c.Close < z.Low || (cfg.InvalidateByWick && c.Low < z.Low) {
					z.Valid = false
					z.Invalidated = true
					break
				}
			} else {
				if c.Close > z.High || (cfg.InvalidateByWick && c.High > z.High) {
					z.Valid = false
					z.Invalidated = true
					break
				}
			}
		}
	}
	return out
}

func supplyDemandDirection(d domain.Direction) (SupplyDemandDirection, bool) {
	if d == domain.DirectionBuy {
		return DemandZone, true
	}
	return SupplyZone, false
}

// meetsSupplyDemandImpulse verifies a directional expansion between the base
// envelope and the break candle close.
// Demand: the close must sit above the base high.
// Supply: the close must sit below the base low.
func meetsSupplyDemandImpulse(baseLow, baseHigh, breakClose, minPct float64, demand bool) bool {
	if baseLow <= 0 || baseHigh <= baseLow {
		return false
	}
	if demand {
		if breakClose <= baseHigh {
			return false
		}
		return (breakClose-baseLow)/baseLow >= minPct
	}
	if breakClose >= baseLow {
		return false
	}
	return (baseHigh-breakClose)/baseHigh >= minPct
}
