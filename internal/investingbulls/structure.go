package investingbulls

import (
	"sort"

	"tradingview-bot/internal/domain"
)

// Trend describes the current market structure direction.
type Trend string

const (
	TrendUnknown Trend = "unknown"
	TrendBullish Trend = "bullish"
	TrendBearish Trend = "bearish"
	TrendRange   Trend = "range"
)

// BreakType identifies a confirmed structural break.
type BreakType string

const (
	BreakBOS   BreakType = "bos"
	BreakCHOCH BreakType = "choch"
)

// Swing is a confirmed pivot. Confirmation requires candles on both sides.
type Swing struct {
	Index int
	Price float64
	High  bool
}

// StructureBreak is confirmed only when a candle CLOSES beyond the swing.
type StructureBreak struct {
	Index     int
	Level     float64
	Direction domain.Direction
	Type      BreakType
}

// Structure contains the detected swings, trend and structural breaks.
type Structure struct {
	Swings []Swing
	Trend  Trend
	Breaks []StructureBreak
}

// DetectSwings finds pivot highs/lows using left/right confirmation bars.
// A pivot is never confirmed from a wick breaking a level alone.
// The last candle may qualify as a swing with only left-side confirmation so
// the learner can trade the most recent confirmed level.
func DetectSwings(ks []domain.Kline, left, right int) []Swing {
	if left < 1 || right < 1 || len(ks) < left+1 {
		return nil
	}
	out := make([]Swing, 0)
	for i := left; i < len(ks); i++ {
		isHigh, isLow := true, true
		for j := 1; j <= left; j++ {
			if i-j < 0 {
				isHigh, isLow = false, false
				break
			}
			if ks[i].High <= ks[i-j].High {
				isHigh = false
			}
			if ks[i].Low >= ks[i-j].Low {
				isLow = false
			}
		}
		for j := 1; j <= right; j++ {
			if i+j >= len(ks) {
				break
			}
			if ks[i].High <= ks[i+j].High {
				isHigh = false
			}
			if ks[i].Low >= ks[i+j].Low {
				isLow = false
			}
		}
		if isHigh {
			out = append(out, Swing{Index: i, Price: ks[i].High, High: true})
		}
		if isLow {
			out = append(out, Swing{Index: i, Price: ks[i].Low, High: false})
		}
	}
	return out
}

// DetectBreaks detects BOS/CHOCH from confirmed swings. A wick beyond a
// swing without a closing price beyond it does not create a break.
//
// The scan keeps a forward pointer over the swings instead of rescanning them
// for every candle: the previous version was O(len(ks) * len(swings)) per call
// and, because the learner calls it once per candle, made the whole search
// cubic in the number of candles. The result is identical: for every candle the
// relevant levels are the most recent swing high and swing low confirmed before
// it, and a break consumes (resets) the level it broke.
func DetectBreaks(ks []domain.Kline, swings []Swing) []StructureBreak {
	if len(ks) == 0 || len(swings) == 0 {
		return nil
	}
	swings = sortedByIndex(swings)

	var out []StructureBreak
	trend := TrendUnknown
	// Positions (not prices) of the active swing levels; -1 means "none left".
	lastHigh, lastLow := -1, -1
	next := 0

	for i := range ks {
		for next < len(swings) && swings[next].Index < i {
			if swings[next].High {
				lastHigh = next
			} else {
				lastLow = next
			}
			next++
		}

		if lastHigh >= 0 && ks[i].Close >= swings[lastHigh].Price {
			typ := BreakBOS
			if trend == TrendBearish {
				typ = BreakCHOCH
			}
			out = append(out, StructureBreak{Index: i, Level: swings[lastHigh].Price, Direction: domain.DirectionBuy, Type: typ})
			trend = TrendBullish
			lastHigh = -1
		}
		if lastLow >= 0 && ks[i].Close <= swings[lastLow].Price {
			typ := BreakBOS
			if trend == TrendBullish {
				typ = BreakCHOCH
			}
			out = append(out, StructureBreak{Index: i, Level: swings[lastLow].Price, Direction: domain.DirectionSell, Type: typ})
			trend = TrendBearish
			lastLow = -1
		}
	}
	return out
}

// sortedByIndex returns swings ordered by index without mutating the input.
// DetectSwings already emits them in ascending order, so the common path is a
// no-op copy-free return; hand-built inputs get a defensive sort.
func sortedByIndex(swings []Swing) []Swing {
	for i := 1; i < len(swings); i++ {
		if swings[i].Index < swings[i-1].Index {
			out := make([]Swing, len(swings))
			copy(out, swings)
			sort.SliceStable(out, func(a, b int) bool { return out[a].Index < out[b].Index })
			return out
		}
	}
	return swings
}

// Analyze performs the first market-structure pass used by the learner.
func Analyze(ks []domain.Kline, left, right int) Structure {
	swings := DetectSwings(ks, left, right)
	breaks := DetectBreaks(ks, swings)
	trend := TrendUnknown
	if len(breaks) > 0 {
		switch breaks[len(breaks)-1].Direction {
		case domain.DirectionBuy:
			trend = TrendBullish
		case domain.DirectionSell:
			trend = TrendBearish
		}
	}
	return Structure{Swings: swings, Trend: trend, Breaks: breaks}
}
