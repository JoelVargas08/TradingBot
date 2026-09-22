package investingbulls

import (
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func TestDetectBullishOrderBlock(t *testing.T) {
	ks := []domain.Kline{
		{Open: 100, High: 105, Low: 98, Close: 103, Start: time.Unix(1, 0)},
		{Open: 103, High: 106, Low: 101, Close: 102, Start: time.Unix(2, 0)}, // last bearish candle
		{Open: 102, High: 115, Low: 101, Close: 113, Start: time.Unix(3, 0)},
	}
	breaks := []StructureBreak{
		{Index: 2, Direction: domain.DirectionBuy, Type: BreakBOS, Level: 106},
	}

	got := DetectOrderBlocks(ks, breaks, DefaultOrderBlockConfig())
	if len(got) != 1 {
		t.Fatalf("got %d order blocks, want 1", len(got))
	}
	if got[0].Direction != OrderBlockBullish || got[0].Index != 1 ||
		got[0].High != 106 || got[0].Low != 101 || !got[0].Valid {
		t.Fatalf("unexpected bullish order block: %+v", got[0])
	}
}

func TestDetectBearishOrderBlock(t *testing.T) {
	ks := []domain.Kline{
		{Open: 100, High: 105, Low: 98, Close: 103, Start: time.Unix(1, 0)},
		{Open: 103, High: 110, Low: 102, Close: 108, Start: time.Unix(2, 0)}, // last bullish candle
		{Open: 108, High: 109, Low: 95, Close: 97, Start: time.Unix(3, 0)},
	}
	breaks := []StructureBreak{
		{Index: 2, Direction: domain.DirectionSell, Type: BreakCHOCH, Level: 102},
	}

	got := DetectOrderBlocks(ks, breaks, DefaultOrderBlockConfig())
	if len(got) != 1 {
		t.Fatalf("got %d order blocks, want 1", len(got))
	}
	if got[0].Direction != OrderBlockBearish || got[0].Index != 1 ||
		got[0].High != 110 || got[0].Low != 102 || !got[0].Valid {
		t.Fatalf("unexpected bearish order block: %+v", got[0])
	}
}

func TestOrderBlockImpulseFilter(t *testing.T) {
	cfg := DefaultOrderBlockConfig()
	cfg.MinImpulsePct = 0.10

	ks := []domain.Kline{
		{Open: 100, High: 105, Low: 98, Close: 103, Start: time.Unix(1, 0)},
		{Open: 103, High: 106, Low: 101, Close: 102, Start: time.Unix(2, 0)},
		{Open: 102, High: 107, Low: 101, Close: 107, Start: time.Unix(3, 0)},
	}
	breaks := []StructureBreak{
		{Index: 2, Direction: domain.DirectionBuy, Type: BreakBOS, Level: 106},
	}
	if got := DetectOrderBlocks(ks, breaks, cfg); len(got) != 0 {
		t.Fatalf("expected impulse filter to reject block, got %d", len(got))
	}
}

func TestUpdateBullishOrderBlock(t *testing.T) {
	ks := []domain.Kline{
		{Open: 100, High: 105, Low: 98, Close: 103, Start: time.Unix(1, 0)},
		{Open: 103, High: 106, Low: 101, Close: 102, Start: time.Unix(2, 0)},
		{Open: 102, High: 115, Low: 101, Close: 113, Start: time.Unix(3, 0)},
		{Open: 112, High: 114, Low: 103, Close: 108, Start: time.Unix(4, 0)}, // tests
	}
	obs := []OrderBlock{{Index: 1, High: 106, Low: 101, Direction: OrderBlockBullish, Valid: true}}
	got := UpdateOrderBlocks(obs, ks, DefaultOrderBlockConfig())
	if len(got) != 1 || !got[0].Tested || !got[0].Valid {
		t.Fatalf("expected tested valid block, got %+v", got)
	}
}

func TestUpdateBearishOrderBlockInvalidation(t *testing.T) {
	ks := []domain.Kline{
		{Open: 100, High: 105, Low: 98, Close: 103, Start: time.Unix(1, 0)},
		{Open: 103, High: 110, Low: 102, Close: 108, Start: time.Unix(2, 0)},
		{Open: 108, High: 109, Low: 95, Close: 97, Start: time.Unix(3, 0)},
		{Open: 97, High: 106, Low: 96, Close: 105, Start: time.Unix(4, 0)},
	}
	obs := []OrderBlock{{Index: 1, High: 110, Low: 102, Direction: OrderBlockBearish, Valid: true}}
	got := UpdateOrderBlocks(obs, ks, DefaultOrderBlockConfig())
	if len(got) != 1 || got[0].Valid {
		t.Fatalf("expected invalidated bearish block, got %+v", got)
	}
}

func TestOrderBlockLookback(t *testing.T) {
	cfg := DefaultOrderBlockConfig()
	cfg.Lookback = 1

	ks := []domain.Kline{
		{Open: 100, High: 105, Low: 98, Close: 103, Start: time.Unix(1, 0)},
		{Open: 103, High: 106, Low: 101, Close: 102, Start: time.Unix(2, 0)},
		{Open: 102, High: 115, Low: 101, Close: 113, Start: time.Unix(3, 0)},
	}
	breaks := []StructureBreak{
		{Index: 2, Direction: domain.DirectionBuy, Type: BreakBOS, Level: 106},
	}
	if got := DetectOrderBlocks(ks, breaks, cfg); len(got) != 1 {
		t.Fatalf("expected nearest candle within lookback, got %d", len(got))
	}
}
