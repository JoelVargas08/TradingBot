package investingbulls

import "math"

// FibConfig contains the Fibonacci levels used by the Investing Bulls setup.
// The values are configurable so the Learn engine can optimize them later.
type FibConfig struct {
	Zone1Min float64
	Zone1Max float64
	Zone2Min float64
	Zone2Max float64
	Target1  float64
	Target2  float64
}

func DefaultFibConfig() FibConfig {
	return FibConfig{
		Zone1Min: 0.45,
		Zone1Max: 0.50,
		Zone2Min: 0.72,
		Zone2Max: 0.85,
		Target1:  1.34,
		Target2:  1.53,
	}
}

// Fibonacci describes a directional swing and its retracement/projection prices.
// Low and High are the endpoints of the impulse leg, independent of direction.
type Fibonacci struct {
	Low         float64
	High        float64
	Direction   Trend
	Range       float64
	Levels      map[float64]float64
	Zone1Low    float64
	Zone1High   float64
	Zone2Low    float64
	Zone2High   float64
	Target1     float64
	Target2     float64
}

func NewFibonacci(low, high float64, direction Trend, cfg FibConfig) (Fibonacci, bool) {
	if !validPrice(low) || !validPrice(high) || high <= low || cfg.Zone1Min < 0 || cfg.Zone1Max < cfg.Zone1Min ||
		cfg.Zone2Min < 0 || cfg.Zone2Max < cfg.Zone2Min || cfg.Target1 <= 1 || cfg.Target2 <= 1 {
		return Fibonacci{}, false
	}
	if direction != TrendBullish && direction != TrendBearish {
		return Fibonacci{}, false
	}

	f := Fibonacci{
		Low:       low,
		High:      high,
		Direction: direction,
		Range:     high - low,
		Levels:    make(map[float64]float64),
	}

	ratios := []float64{0, 0.45, 0.50, 0.72, 0.85, 1, cfg.Target1, cfg.Target2}
	for _, ratio := range ratios {
		f.Levels[ratio] = f.Price(ratio)
	}

	f.Zone1Low, f.Zone1High = ordered(f.Price(cfg.Zone1Min), f.Price(cfg.Zone1Max))
	f.Zone2Low, f.Zone2High = ordered(f.Price(cfg.Zone2Min), f.Price(cfg.Zone2Max))
	f.Target1 = f.Price(cfg.Target1)
	f.Target2 = f.Price(cfg.Target2)

	return f, true
}

// Price converts a Fibonacci ratio into a price.
// Bullish impulse: retracements move down from High.
// Bearish impulse: retracements move up from Low.
func (f Fibonacci) Price(ratio float64) float64 {
	if f.Direction == TrendBullish {
		return f.High - f.Range*ratio
	}
	return f.Low + f.Range*ratio
}

func (f Fibonacci) InZone(price float64, zone int) bool {
	switch zone {
	case 1:
		return price >= f.Zone1Low && price <= f.Zone1High
	case 2:
		return price >= f.Zone2Low && price <= f.Zone2High
	default:
		return false
	}
}

func (f Fibonacci) Zone(price float64) int {
	if f.InZone(price, 1) {
		return 1
	}
	if f.InZone(price, 2) {
		return 2
	}
	return 0
}

func validPrice(v float64) bool {
	return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

func ordered(a, b float64) (float64, float64) {
	if a <= b {
		return a, b
	}
	return b, a
}
