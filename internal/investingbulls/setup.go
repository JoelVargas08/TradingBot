package investingbulls

import "tradingview-bot/internal/domain"

type SetupType string

const (
    SetupCHOCHLong SetupType = "choch_long"
    SetupCHOCHShort SetupType = "choch_short"
    SetupContinuationLong SetupType = "continuation_long"
    SetupContinuationShort SetupType = "continuation_short"
)

func (s SetupType) Valid() bool {
    switch s {
    case SetupCHOCHLong, SetupCHOCHShort, SetupContinuationLong, SetupContinuationShort: return true
    }
    return false
}

type ClassifiedSetup struct {
    SetupType SetupType
    Index int
    Direction domain.Direction
    BreakType BreakType
    TrendBefore Trend
    TrendAfter Trend
}

// ClassifyBreak maps a confirmed BOS/CHOCH to the four operational setup families.
func ClassifyBreak(b StructureBreak, trendBefore Trend) (SetupType, bool) {
    if b.Direction == domain.DirectionBuy {
        if b.Type == BreakCHOCH && trendBefore == TrendBearish { return SetupCHOCHLong, true }
        if b.Type == BreakBOS && trendBefore == TrendBullish { return SetupContinuationLong, true }
    }
    if b.Direction == domain.DirectionSell {
        if b.Type == BreakCHOCH && trendBefore == TrendBullish { return SetupCHOCHShort, true }
        if b.Type == BreakBOS && trendBefore == TrendBearish { return SetupContinuationShort, true }
    }
    return "", false
}

func ClassifySetups(s Structure) []ClassifiedSetup {
    var out []ClassifiedSetup
    trend := TrendUnknown
    for _, b := range s.Breaks {
        typ, ok := ClassifyBreak(b, trend)
        if ok {
            after := TrendBullish
            if b.Direction == domain.DirectionSell { after = TrendBearish }
            out = append(out, ClassifiedSetup{SetupType:typ, Index:b.Index, Direction:b.Direction, BreakType:b.Type, TrendBefore:trend, TrendAfter:after})
        }
        if b.Direction == domain.DirectionBuy { trend = TrendBullish } else { trend = TrendBearish }
    }
    return out
}
