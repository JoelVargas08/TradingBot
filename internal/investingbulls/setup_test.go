package investingbulls

import (
 "testing"
 "tradingview-bot/internal/domain"
)

func TestClassifyBreakFamilies(t *testing.T) {
 cases:=[]struct{name string; b StructureBreak; trend Trend; want SetupType}{
  {"choch long",StructureBreak{Direction:domain.DirectionBuy,Type:BreakCHOCH},TrendBearish,SetupCHOCHLong},
  {"choch short",StructureBreak{Direction:domain.DirectionSell,Type:BreakCHOCH},TrendBullish,SetupCHOCHShort},
  {"continuation long",StructureBreak{Direction:domain.DirectionBuy,Type:BreakBOS},TrendBullish,SetupContinuationLong},
  {"continuation short",StructureBreak{Direction:domain.DirectionSell,Type:BreakBOS},TrendBearish,SetupContinuationShort},
 }
 for _,tc:=range cases {got,ok:=ClassifyBreak(tc.b,tc.trend);if !ok||got!=tc.want{t.Fatalf("%s: got %q ok=%v want %q",tc.name,got,ok,tc.want)}}
}

func TestClassifyBreakRejectsCounterTrendBOS(t *testing.T) {
 if _,ok:=ClassifyBreak(StructureBreak{Direction:domain.DirectionBuy,Type:BreakBOS},TrendBearish);ok{t.Fatal("bearish BOS must not be classified as bullish continuation")}
}