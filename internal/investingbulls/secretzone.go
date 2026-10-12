package investingbulls

import "tradingview-bot/internal/domain"

// SecretZone es la banda de proyección Fibonacci definida por los niveles 1.34
// y 1.53. La fuente (docs/source/estrategia_investing_bulls_p34.txt, L255-380)
// la describe como "zona de reacción para cambio de tendencia" y, además, como
// objetivo donde cerrar posiciones. Ver docs/INVESTING_BULLS_STRATEGY.md (R17).
//
// La banda es [min(Target1,Target2), max(Target1,Target2)] y queda siempre
// FUERA del impulso: por debajo del mínimo si el Fibonacci es alcista y por
// encima del máximo si es bajista.
type SecretZone struct {
	// Direction es la dirección del impulso que proyecta la banda.
	Direction Trend
	// Low y High son los bordes inferior y superior de la banda, ordenados.
	Low  float64
	High float64
}

// NewSecretZone construye la banda a partir de los niveles de proyección de un
// Fibonacci ya trazado. Devuelve false si los niveles no son utilizables.
func NewSecretZone(f Fibonacci) (SecretZone, bool) {
	if !validPrice(f.Target1) || !validPrice(f.Target2) {
		return SecretZone{}, false
	}
	low, high := ordered(f.Target1, f.Target2)
	if low == high {
		return SecretZone{}, false
	}
	return SecretZone{Direction: f.Direction, Low: low, High: high}, true
}

// Contains indica si un precio está dentro de la banda (bordes incluidos).
func (z SecretZone) Contains(price float64) bool {
	return price >= z.Low && price <= z.High
}

// Touch indica si la vela intersecta la banda, incluyendo mechas: la fuente
// habla de "tocar la zona", por lo que un toque por mecha cuenta.
func (z SecretZone) Touch(k domain.Kline) bool {
	return k.High >= z.Low && k.Low <= z.High
}

// SecretZoneReaction es el estado de reacción del precio en la banda secreta.
type SecretZoneReaction int

const (
	// SecretZoneNone: el precio no tocó la banda.
	SecretZoneNone SecretZoneReaction = iota
	// SecretZoneTouch: tocó la banda pero sin confirmación de confluencia. La
	// fuente es explícita en que tocar la zona NO es suficiente para operar.
	SecretZoneTouch
	// SecretZoneConfirmed: tocó la banda, no la rompió y hay confirmación de
	// confluencia (Order Block / imbalance). Es una reacción válida.
	SecretZoneConfirmed
	// SecretZoneInvalidated: el precio cerró cruzando toda la banda, por lo que
	// la reacción no se sostuvo.
	SecretZoneInvalidated
)

func (r SecretZoneReaction) String() string {
	switch r {
	case SecretZoneNone:
		return "none"
	case SecretZoneTouch:
		return "touch"
	case SecretZoneConfirmed:
		return "confirmed"
	case SecretZoneInvalidated:
		return "invalidated"
	default:
		return "unknown"
	}
}

// EvaluateSecretZone evalúa la reacción de una vela cerrada en la banda secreta.
//
// Reglas respaldadas por la fuente (L255-380):
//   - Tocar la zona no basta: para considerar una operación de reversión se
//     requieren confirmaciones adicionales (Order Block / imbalance).
//   - La zona también sirve como objetivo de cierre de una posición existente.
//
// Decisiones de ingeniería (documentadas, no texto fuente):
//   - "Invalidación" se define como un cierre que cruza toda la banda (el
//     precio pasó de largo y la reacción no se sostuvo). La invalidación tiene
//     prioridad sobre el toque, de forma determinista.
//   - Qué confluencia concreta cuenta como confirmación es política del llamante
//     y sigue siendo una pregunta abierta (docs/INVESTING_BULLS_STRATEGY.md §5.Q3).
func EvaluateSecretZone(z SecretZone, k domain.Kline, confluence bool) SecretZoneReaction {
	if !z.Touch(k) {
		return SecretZoneNone
	}
	if z.closedBeyond(k.Close) {
		return SecretZoneInvalidated
	}
	if confluence {
		return SecretZoneConfirmed
	}
	return SecretZoneTouch
}

// closedBeyond informa si el cierre dejó atrás toda la banda, en el sentido en
// que el impulso proyectaba (equivale a que la reacción no se sostuvo).
func (z SecretZone) closedBeyond(close float64) bool {
	if z.Direction == TrendBullish {
		return close < z.Low
	}
	return close > z.High
}
