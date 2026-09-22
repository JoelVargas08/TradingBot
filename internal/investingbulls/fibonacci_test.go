package investingbulls

import (
	"math"
	"testing"
)

func TestNewFibonacciBullish(t *testing.T) {
	f, ok := NewFibonacci(100, 200, TrendBullish, DefaultFibConfig())
	if !ok {
		t.Fatal("expected valid fibonacci")
	}

	tests := []struct {
		ratio float64
		want  float64
	}{
		{0, 200},
		{0.45, 155},
		{0.50, 150},
		{0.72, 128},
		{0.85, 115},
		{1, 100},
	}

	for _, tt := range tests {
		if got := f.Price(tt.ratio); math.Abs(got-tt.want) > 1e-9 {
			t.Fatalf("ratio %.2f: got %.8f want %.8f", tt.ratio, got, tt.want)
		}
	}

	if f.Zone(152) != 1 {
		t.Fatalf("expected price 152 in zone 1")
	}
	if f.Zone(120) != 2 {
		t.Fatalf("expected price 120 in zone 2")
	}
	if f.Zone(170) != 0 {
		t.Fatalf("expected price 170 outside zones")
	}

	if math.Abs(f.Target1-66) > 1e-9 || math.Abs(f.Target2-47) > 1e-9 {
		t.Fatalf("unexpected bullish projection prices: target1=%.2f target2=%.2f", f.Target1, f.Target2)
	}
}

func TestNewFibonacciBearish(t *testing.T) {
	f, ok := NewFibonacci(100, 200, TrendBearish, DefaultFibConfig())
	if !ok {
		t.Fatal("expected valid fibonacci")
	}

	tests := []struct {
		ratio float64
		want  float64
	}{
		{0, 100},
		{0.45, 145},
		{0.50, 150},
		{0.72, 172},
		{0.85, 185},
		{1, 200},
	}

	for _, tt := range tests {
		if got := f.Price(tt.ratio); math.Abs(got-tt.want) > 1e-9 {
			t.Fatalf("ratio %.2f: got %.8f want %.8f", tt.ratio, got, tt.want)
		}
	}

	if f.Zone(148) != 1 {
		t.Fatalf("expected price 148 in zone 1")
	}
	if f.Zone(180) != 2 {
		t.Fatalf("expected price 180 in zone 2")
	}
}

func TestFibonacciCustomConfig(t *testing.T) {
	cfg := DefaultFibConfig()
	cfg.Zone1Min = 0.40
	cfg.Zone1Max = 0.55
	cfg.Zone2Min = 0.70
	cfg.Zone2Max = 0.90
	cfg.Target1 = 1.25
	cfg.Target2 = 1.60

	f, ok := NewFibonacci(100, 200, TrendBullish, cfg)
	if !ok {
		t.Fatal("expected valid fibonacci")
	}
	if !f.InZone(150, 1) {
		t.Fatal("expected custom zone 1 to include 150")
	}
	if math.Abs(f.Target1-75) > 1e-9 || math.Abs(f.Target2-40) > 1e-9 {
		t.Fatalf("unexpected custom targets: %.2f %.2f", f.Target1, f.Target2)
	}
}

func TestNewFibonacciRejectsInvalidInput(t *testing.T) {
	if _, ok := NewFibonacci(200, 100, TrendBullish, DefaultFibConfig()); ok {
		t.Fatal("expected high <= low to be rejected")
	}
	if _, ok := NewFibonacci(100, 200, TrendUnknown, DefaultFibConfig()); ok {
		t.Fatal("expected unknown direction to be rejected")
	}
	if _, ok := NewFibonacci(0, 200, TrendBullish, DefaultFibConfig()); ok {
		t.Fatal("expected invalid price to be rejected")
	}
}
