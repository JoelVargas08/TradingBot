package investingbulls

import (
	"testing"
	"time"
	"tradingview-bot/internal/domain"
)

func candles(highs, lows, closes []float64) []domain.Kline {
	out := make([]domain.Kline, len(closes))
	for i := range closes {
		out[i] = domain.Kline{Symbol:"BTCUSDT", Timeframe:"1h", Start:time.Unix(int64(i),0), Open:closes[i], High:highs[i], Low:lows[i], Close:closes[i], Closed:true}
	}
	return out
}

func TestDetectSwings(t *testing.T) {
	ks := candles(
		[]float64{10,12,15,13,11},
		[]float64{8,9,10,8,7},
		[]float64{9,11,14,10,8},
	)
	got := DetectSwings(ks, 1, 1)
	if len(got) != 2 { t.Fatalf("swings=%d want 2: %+v", len(got), got) }
	if got[0].Index != 2 || !got[0].High { t.Fatalf("high swing=%+v", got[0]) }
	if got[1].Index != 4 || got[1].High { t.Fatalf("low swing=%+v", got[1]) }
}

func TestDetectBreakRequiresClose(t *testing.T) {
	ks := candles(
		[]float64{10,12,15,16,14,13},
		[]float64{8,9,10,11,10,8},
		[]float64{9,11,14,14,13,7},
	)
	swings := []Swing{{Index:2, Price:15, High:true}, {Index:4, Price:10, High:false}}
	breaks := DetectBreaks(ks, swings)
	if len(breaks) != 1 { t.Fatalf("breaks=%d want 1: %+v", len(breaks), breaks) }
	if breaks[0].Type != BreakBOS || breaks[0].Direction != domain.DirectionSell {
		t.Fatalf("break=%+v", breaks[0])
	}
}

func TestCHOCHAfterOppositeStructure(t *testing.T) {
	ks := candles(
		[]float64{10,12,11,9,10,13},
		[]float64{8,9,7,6,8,9},
		[]float64{9,11,8,7,9,12},
	)
	swings := []Swing{
		{Index:1, Price:12, High:true},
		{Index:2, Price:7, High:false},
		{Index:4, Price:10, High:true},
	}
	breaks := DetectBreaks(ks, swings)
	if len(breaks) != 2 { t.Fatalf("breaks=%d want 2: %+v", len(breaks), breaks) }
	if breaks[0].Type != BreakBOS || breaks[0].Direction != domain.DirectionSell { t.Fatalf("first=%+v", breaks[0]) }
	if breaks[1].Type != BreakCHOCH || breaks[1].Direction != domain.DirectionBuy { t.Fatalf("second=%+v", breaks[1]) }
}

func TestAnalyzeTrend(t *testing.T) {
	ks := candles(
		[]float64{10,12,11,13,12,15},
		[]float64{8,9,7,10,9,11},
		[]float64{9,11,8,12,11,14},
	)
	got := Analyze(ks,1,1)
	if got.Trend != TrendBullish { t.Fatalf("trend=%q want bullish", got.Trend) }
}
