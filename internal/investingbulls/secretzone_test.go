package investingbulls

import (
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func zoneCandle(high, low, close float64) domain.Kline {
	return domain.Kline{
		Symbol: "TEST", Timeframe: "15m",
		Start: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		Open:  close, High: high, Low: low, Close: close, Closed: true,
	}
}

// Banda del nivel secreto a partir de un Fibonacci bajista (impulso de máximo a
// mínimo, como cuando se buscan ventas): la zona queda por ENCIMA del máximo.
func TestNewSecretZoneFromFibonacci(t *testing.T) {
	bear, ok := NewFibonacci(100, 200, TrendBearish, DefaultFibConfig())
	if !ok {
		t.Fatal("bearish fibonacci must be valid")
	}
	z, ok := NewSecretZone(bear)
	if !ok {
		t.Fatal("secret zone must be usable")
	}
	if z.Direction != TrendBearish {
		t.Fatalf("unexpected direction: %v", z.Direction)
	}
	// Price(1.34)=100+1.34*100=234; Price(1.53)=100+1.53*100=253.
	if z.Low != 234 || z.High != 253 {
		t.Fatalf("unexpected band: [%.2f, %.2f]", z.Low, z.High)
	}
	if z.Contains(240) == false || z.Contains(230) == true {
		t.Fatal("Contains must respect the ordered band")
	}
}

func TestNewSecretZoneRejectsUnusableFibonacci(t *testing.T) {
	if _, ok := NewSecretZone(Fibonacci{}); ok {
		t.Fatal("a zero-value fibonacci must not yield a secret zone")
	}
}

// Toque por mecha sin confirmación: la fuente exige más que el toque.
func TestSecretZoneTouchWithoutConfirmation(t *testing.T) {
	z := SecretZone{Direction: TrendBearish, Low: 234, High: 253}
	k := zoneCandle(240, 220, 228) // mecha entra en la banda, cierra por debajo
	if got := EvaluateSecretZone(z, k, false); got != SecretZoneTouch {
		t.Fatalf("expected touch, got %v", got)
	}
}

func TestSecretZoneConfirmedWithConfluence(t *testing.T) {
	z := SecretZone{Direction: TrendBearish, Low: 234, High: 253}
	k := zoneCandle(240, 220, 228)
	if got := EvaluateSecretZone(z, k, true); got != SecretZoneConfirmed {
		t.Fatalf("expected confirmed, got %v", got)
	}
}

func TestSecretZoneNotTouched(t *testing.T) {
	z := SecretZone{Direction: TrendBearish, Low: 234, High: 253}
	k := zoneCandle(220, 200, 210) // ni la mecha llega a 234
	if got := EvaluateSecretZone(z, k, true); got != SecretZoneNone {
		t.Fatalf("expected none, got %v", got)
	}
}

// Cierre que cruza toda la banda: la reacción no se sostuvo. La invalidación
// tiene prioridad sobre la confirmación.
func TestSecretZoneInvalidatedWhenClosedThroughBand(t *testing.T) {
	z := SecretZone{Direction: TrendBearish, Low: 234, High: 253}
	k := zoneCandle(260, 230, 256) // cierra por encima del borde superior
	if got := EvaluateSecretZone(z, k, true); got != SecretZoneInvalidated {
		t.Fatalf("expected invalidated, got %v", got)
	}
}

// Banda por debajo (Fibonacci alcista): se invalida si el cierre queda por
// debajo del borde inferior.
func TestSecretZoneBullishInvalidationBelowBand(t *testing.T) {
	z := SecretZone{Direction: TrendBullish, Low: 47, High: 66}
	brokenDown := zoneCandle(60, 40, 45)
	if got := EvaluateSecretZone(z, brokenDown, false); got != SecretZoneInvalidated {
		t.Fatalf("expected invalidated below band, got %v", got)
	}
	held := zoneCandle(70, 60, 64) // entra en la banda y no la cruza
	if got := EvaluateSecretZone(z, held, false); got != SecretZoneTouch {
		t.Fatalf("expected touch, got %v", got)
	}
	if got := EvaluateSecretZone(z, held, true); got != SecretZoneConfirmed {
		t.Fatalf("expected confirmed, got %v", got)
	}
}

func TestSecretZoneReactionString(t *testing.T) {
	cases := map[SecretZoneReaction]string{
		SecretZoneNone:        "none",
		SecretZoneTouch:       "touch",
		SecretZoneConfirmed:   "confirmed",
		SecretZoneInvalidated: "invalidated",
		SecretZoneReaction(9): "unknown",
	}
	for r, want := range cases {
		if got := r.String(); got != want {
			t.Fatalf("reaction %d: got %q want %q", r, got, want)
		}
	}
}
