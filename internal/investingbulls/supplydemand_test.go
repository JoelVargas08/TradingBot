package investingbulls

import (
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func TestDetectDemandZone(t *testing.T) {
	ks := []domain.Kline{
		{Open: 100, High: 105, Low: 98, Close: 103, Start: time.Unix(0, 0)},
		{Open: 103, High: 104, Low: 102, Close: 103.5, Start: time.Unix(1, 0)}, // base
		{Open: 103.5, High: 104, Low: 103, Close: 103.5, Start: time.Unix(2, 0)},
		{Open: 103.5, High: 115, Low: 103, Close: 114, Start: time.Unix(3, 0)}, // expansion
	}
	breaks := []StructureBreak{{Index: 3, Direction: domain.DirectionBuy, Type: BreakBOS, Level: 105}}

	got := DetectSupplyDemandZones(ks, breaks, DefaultSupplyDemandConfig())
	if len(got) != 1 {
		t.Fatalf("got %d demand zones, want 1: %+v", len(got), got)
	}
	z := got[0]
	if z.Direction != DemandZone || z.Index != 1 || z.High != 104 || z.Low != 102 || !z.Valid {
		t.Fatalf("unexpected demand zone: %+v", z)
	}
}

func TestDetectSupplyZone(t *testing.T) {
	ks := []domain.Kline{
		{Open: 100, High: 102, Low: 95, Close: 97, Start: time.Unix(0, 0)},
		{Open: 97, High: 98, Low: 96, Close: 97.5, Start: time.Unix(1, 0)}, // base
		{Open: 97.5, High: 98, Low: 97, Close: 97.5, Start: time.Unix(2, 0)},
		{Open: 97.5, High: 98, Low: 90, Close: 91, Start: time.Unix(3, 0)}, // expansion
	}
	breaks := []StructureBreak{{Index: 3, Direction: domain.DirectionSell, Type: BreakCHOCH, Level: 95}}

	got := DetectSupplyDemandZones(ks, breaks, DefaultSupplyDemandConfig())
	if len(got) != 1 {
		t.Fatalf("got %d supply zones, want 1: %+v", len(got), got)
	}
	z := got[0]
	if z.Direction != SupplyZone || z.Index != 1 || z.High != 98 || z.Low != 96 || !z.Valid {
		t.Fatalf("unexpected supply zone: %+v", z)
	}
}

func TestSupplyDemandImpulseFilter(t *testing.T) {
	cfg := DefaultSupplyDemandConfig()
	cfg.MinImpulsePct = 0.60

	ks := []domain.Kline{
		{Open: 100, High: 105, Low: 98, Close: 103, Start: time.Unix(0, 0)},
		{Open: 103, High: 104, Low: 102, Close: 103.5, Start: time.Unix(1, 0)},
		{Open: 103.5, High: 104, Low: 103, Close: 103.5, Start: time.Unix(2, 0)},
		{Open: 103.5, High: 115, Low: 103, Close: 114, Start: time.Unix(3, 0)},
	}
	breaks := []StructureBreak{{Index: 3, Direction: domain.DirectionBuy, Type: BreakBOS, Level: 105}}

	if got := DetectSupplyDemandZones(ks, breaks, cfg); len(got) != 0 {
		t.Fatalf("expected impulse filter to reject the base, got %d: %+v", len(got), got)
	}
}

func TestSupplyDemandBaseStopsAtFirstWideCandle(t *testing.T) {
	ks := []domain.Kline{
		{Open: 100, High: 106, Low: 99, Close: 100, Start: time.Unix(0, 0)}, // wide: stops the run
		{Open: 100, High: 101, Low: 100, Close: 100.5, Start: time.Unix(1, 0)},
		{Open: 100.5, High: 101, Low: 100, Close: 100.5, Start: time.Unix(2, 0)},
		{Open: 100.5, High: 112, Low: 100, Close: 111, Start: time.Unix(3, 0)},
	}
	breaks := []StructureBreak{{Index: 3, Direction: domain.DirectionBuy, Type: BreakBOS, Level: 101}}

	got := DetectSupplyDemandZones(ks, breaks, DefaultSupplyDemandConfig())
	if len(got) != 1 {
		t.Fatalf("got %d zones, want 1", len(got))
	}
	if got[0].Index != 1 || got[0].Low != 100 {
		t.Fatalf("base must stop before the wide candle, got %+v", got[0])
	}
}

func TestUpdateDemandZoneTestedAndInvalidated(t *testing.T) {
	zones := []SupplyDemandZone{{Index: 1, High: 104, Low: 102, Direction: DemandZone, Valid: true}}

	tested := []domain.Kline{
		{Open: 100, High: 104, Low: 102, Close: 103, Start: time.Unix(0, 0)},
		{Open: 103, High: 104, Low: 102, Close: 103, Start: time.Unix(1, 0)},
		{Open: 102.5, High: 103, Low: 102.5, Close: 102.8, Start: time.Unix(2, 0)},
	}
	got := UpdateSupplyDemandZones(zones, tested, DefaultSupplyDemandConfig())
	if len(got) != 1 || !got[0].Tested || !got[0].Valid {
		t.Fatalf("expected tested valid zone, got %+v", got)
	}

	invalid := []domain.Kline{
		{Open: 100, High: 104, Low: 102, Close: 103, Start: time.Unix(0, 0)},
		{Open: 103, High: 104, Low: 102, Close: 103, Start: time.Unix(1, 0)},
		{Open: 102, High: 102.5, Low: 99, Close: 99.5, Start: time.Unix(2, 0)},
	}
	got = UpdateSupplyDemandZones(zones, invalid, DefaultSupplyDemandConfig())
	if len(got) != 1 || !got[0].Invalidated || got[0].Valid {
		t.Fatalf("expected invalidated zone, got %+v", got)
	}
}
