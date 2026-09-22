package investingbulls

import "tradingview-bot/internal/domain"

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
func DetectBreaks(ks []domain.Kline, swings []Swing) []StructureBreak {
	if len(ks) == 0 || len(swings) == 0 {
		return nil
	}
	var out []StructureBreak
	trend := TrendUnknown
	lastHigh, lastLow := -1, -1
	for i := range ks {
		for _, s := range swings {
			if s.Index >= i {
				continue
			}
			if s.High {
				lastHigh = s.Index
			} else {
				lastLow = s.Index
			}
		}
		if lastHigh >= 0 && ks[i].Close >= ks[lastHigh].High {
			typ := BreakBOS
			if trend == TrendBearish {
				typ = BreakCHOCH
			}
			out = append(out, StructureBreak{Index: i, Level: ks[lastHigh].High, Direction: domain.DirectionBuy, Type: typ})
			trend = TrendBullish
			lastHigh = -1
		}
		if lastLow >= 0 && ks[i].Close <= ks[lastLow].Low {
			typ := BreakBOS
			if trend == TrendBullish {
				typ = BreakCHOCH
			}
			out = append(out, StructureBreak{Index: i, Level: ks[lastLow].Low, Direction: domain.DirectionSell, Type: typ})
			trend = TrendBearish
			lastLow = -1
		}
	}
	return out
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
