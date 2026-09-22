package investingbulls

import (
	"math"
	"testing"

	"tradingview-bot/internal/domain"
)

func TestDetectBullishImbalance(t *testing.T) {
	ks := []domain.Kline{
		{High: 100, Low: 95, Close: 98, Start: 1},
		{High: 108, Low: 101, Close: 106, Start: 2},
		{High: 115, Low: 105, Close: 112, Start: 3},
	}
	got := DetectImbalances(ks, DefaultImbalanceConfig())
	if len(got) != 1 {
		t.Fatalf("got %d imbalances, want 1", len(got))
	}
	if got[0].Direction != ImbalanceBullish || got[0].Low != 100 || got[0].High != 105 {
		t.Fatalf("unexpected bullish imbalance: %+v", got[0])
	}
}

func TestDetectBearishImbalance(t *testing.T) {
	ks := []domain.Kline{
		{High: 105, Low: 100, Close: 102, Start: 1},
		{High: 99, Low: 92, Close: 94, Start: 2},
		{High: 95, Low: 88, Close: 90, Start: 3},
	}
	got := DetectImbalances(ks, DefaultImbalanceConfig())
	if len(got) != 1 {
		t.Fatalf("got %d imbalances, want 1", len(got))
	}
	if got[0].Direction != ImbalanceBearish || got[0].Low != 95 || got[0].High != 100 {
		t.Fatalf("unexpected bearish imbalance: %+v", got[0])
	}
}

func TestDetectImbalanceMinGap(t *testing.T) {
	cfg := DefaultImbalanceConfig()
	cfg.MinGapPct = 0.05

	ks := []domain.Kline{
		{High: 100, Low: 95, Close: 98, Start: 1},
		{High: 104, Low: 101, Close: 103, Start: 2},
		{High: 103, Low: 100.5, Close: 102, Start: 3},
	}
	if got := DetectImbalances(ks, cfg); len(got) != 0 {
		t.Fatalf("expected small gap to be filtered, got %d", len(got))
	}
}

func TestUpdateBullishImbalanceFill(t *testing.T) {
	ks := []domain.Kline{
		{High: 100, Low: 95, Close: 98, Start: 1},
		{High: 108, Low: 101, Close: 106, Start: 2},
		{High: 115, Low: 105, Close: 112, Start: 3},
		{High: 110, Low: 103, Close: 107, Start: 4},
		{High: 108, Low: 100, Close: 102, Start: 5},
	}
	imbs := DetectImbalances(ks, DefaultImbalanceConfig())
	got := UpdateImbalances(imbs, ks, DefaultImbalanceConfig())
	if len(got) != 1 || !got[0].IsFilled || math.Abs(got[0].FilledPct-1) > 1e-9 {
		t.Fatalf("expected full fill, got %+v", got)
	}
}

func TestUpdateBearishImbalancePartialFill(t *testing.T) {
	ks := []domain.Kline{
		{High: 105, Low: 100, Close: 102, Start: 1},
		{High: 99, Low: 92, Close: 94, Start: 2},
		{High: 95, Low: 88, Close: 90, Start: 3},
		{High: 97, Low: 90, Close: 92, Start: 4},
	}
	imbs := DetectImbalances(ks, DefaultImbalanceConfig())
	got := UpdateImbalances(imbs, ks, DefaultImbalanceConfig())
	if len(got) != 1 {
		t.Fatalf("expected one imbalance")
	}
	if got[0].IsFilled {
		t.Fatalf("expected partial, not full, fill")
	}
	if math.Abs(got[0].FilledPct-0.4) > 1e-9 {
		t.Fatalf("got fill %.4f, want 0.4", got[0].FilledPct)
	}
}

func TestDetectImbalancesRejectsInvalidConfig(t *testing.T) {
	cfg := DefaultImbalanceConfig()
	cfg.MinGapPct = -0.1
	if got := DetectImbalances(make([]domain.Kline, 3), cfg); got != nil {
		t.Fatal("expected nil for invalid config")
	}
}
