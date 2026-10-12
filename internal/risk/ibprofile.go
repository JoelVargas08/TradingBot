package risk

import (
	"fmt"
	"math"

	"tradingview-bot/internal/domain"
)

// IBRiskProfile es el perfil de riesgo auditable de la estrategia Investing
// Bulls. Está desacoplado de los detectores: no conoce velas, zonas ni
// confluencias, solo límites y cálculos de riesgo/sizing.
//
// Decisiones de ingeniería (NO texto fuente del PDF; el PDF no resuelve los
// porcentajes de forma inequívoca):
//
//   - El "3-4% por operación" se interpreta como distancia proyectada del
//     objetivo (reward), no como riesgo de capital. El capital arriesgado por
//     operación se mantiene conservador (RiskPct, por defecto 1%).
//   - "Pérdida diaria 1%" es el límite de pérdida realizada del día medido
//     sobre el balance inicial (definición inequívoca, ver DailyLossLimit).
//   - "SL máximo 1.5%" es el stop por defecto (IBStopDefaultPct); el histórico
//     2% (IBStopMaxPct) queda como tope absoluto.
//   - "Apalancamiento máximo 20x" se valida pero NUNCA se habilita: el trading
//     real y el apalancamiento real permanecen desactivados.
//
// Estas decisiones se documentan en MEMORIA_FASE_IB_ASIMILACION.md.
const (
	// IBStopDefaultPct es la distancia de stop por defecto del perfil IB (1.5%).
	IBStopDefaultPct = 0.015
	// IBStopMaxPct es el tope histórico absoluto del stop IB (2%).
	IBStopMaxPct = 0.02
	// IBMaxLeverage es el apalancamiento máximo permitido por el perfil (20x).
	IBMaxLeverage = 20.0
	// IBRiskDefaultPct es el riesgo de capital por operación por defecto (1%).
	IBRiskDefaultPct = 0.01
	// IBDailyLossDefaultPct es el límite de pérdida realizada diaria (1%).
	IBDailyLossDefaultPct = 0.01
)

// IBRiskProfile agrupa los límites y el cálculo de tamaño/exposición del perfil
// Investing Bulls.
type IBRiskProfile struct {
	// RiskPct es la fracción del balance arriesgada por operación.
	RiskPct float64
	// StopDefaultPct es la distancia de stop por defecto (1.5%).
	StopDefaultPct float64
	// StopMaxPct es el tope absoluto de distancia de stop (2%).
	StopMaxPct float64
	// MaxDailyLossPct es el límite de pérdida realizada diaria sobre el balance inicial.
	MaxDailyLossPct float64
	// MaxOpenPositions es el máximo de posiciones simultáneas.
	MaxOpenPositions int
	// MinRR es el ratio riesgo/beneficio mínimo exigido.
	MinRR float64
	// MaxLeverage es el apalancamiento máximo validado (no habilitado).
	MaxLeverage float64
}

// DefaultIBRiskProfile devuelve el perfil con las decisiones de ingeniería
// documentadas arriba.
func DefaultIBRiskProfile() IBRiskProfile {
	return IBRiskProfile{
		RiskPct:          IBRiskDefaultPct,
		StopDefaultPct:   IBStopDefaultPct,
		StopMaxPct:       IBStopMaxPct,
		MaxDailyLossPct:  IBDailyLossDefaultPct,
		MaxOpenPositions: 3,
		MinRR:            2.0,
		MaxLeverage:      IBMaxLeverage,
	}
}

// Validate comprueba que la configuración es coherente. Debe invocarse al
// iniciar; si falla, el arranque debe abortar (fallar de forma segura).
func (p IBRiskProfile) Validate() error {
	if p.RiskPct <= 0 || p.RiskPct > 1 {
		return fmt.Errorf("IBRiskProfile: RiskPct %.4f fuera de (0,1]", p.RiskPct)
	}
	if p.StopDefaultPct <= 0 {
		return fmt.Errorf("IBRiskProfile: StopDefaultPct %.4f debe ser > 0", p.StopDefaultPct)
	}
	if p.StopMaxPct <= 0 {
		return fmt.Errorf("IBRiskProfile: StopMaxPct %.4f debe ser > 0", p.StopMaxPct)
	}
	if p.StopDefaultPct > p.StopMaxPct {
		return fmt.Errorf("IBRiskProfile: StopDefaultPct %.4f > StopMaxPct %.4f", p.StopDefaultPct, p.StopMaxPct)
	}
	if p.StopMaxPct > IBStopMaxPct {
		return fmt.Errorf("IBRiskProfile: StopMaxPct %.4f excede el tope del perfil %.4f", p.StopMaxPct, IBStopMaxPct)
	}
	if p.MaxDailyLossPct <= 0 || p.MaxDailyLossPct > 1 {
		return fmt.Errorf("IBRiskProfile: MaxDailyLossPct %.4f fuera de (0,1]", p.MaxDailyLossPct)
	}
	if p.MaxOpenPositions <= 0 {
		return fmt.Errorf("IBRiskProfile: MaxOpenPositions %d debe ser > 0", p.MaxOpenPositions)
	}
	if p.MinRR <= 0 {
		return fmt.Errorf("IBRiskProfile: MinRR %.2f debe ser > 0", p.MinRR)
	}
	if p.MaxLeverage <= 0 || p.MaxLeverage > IBMaxLeverage {
		return fmt.Errorf("IBRiskProfile: MaxLeverage %.2f fuera de (0,%.0f]", p.MaxLeverage, IBMaxLeverage)
	}
	return nil
}

// ClampStopPct normaliza una distancia de stop al rango del perfil: valores no
// positivos toman el default y valores por encima del tope se recortan.
func (p IBRiskProfile) ClampStopPct(pct float64) float64 {
	if pct <= 0 {
		return p.StopDefaultPct
	}
	if p.StopMaxPct > 0 && pct > p.StopMaxPct {
		return p.StopMaxPct
	}
	return pct
}

// RiskAmount es el capital arriesgado por operación para un balance dado.
func (p IBRiskProfile) RiskAmount(balance float64) float64 {
	if balance <= 0 || p.RiskPct <= 0 {
		return 0
	}
	return balance * p.RiskPct
}

// StopDistancePct es la distancia relativa del stop respecto a la entrada.
func (p IBRiskProfile) StopDistancePct(entry, stop float64) float64 {
	if entry <= 0 {
		return 0
	}
	return math.Abs(entry-stop) / entry
}

// PositionSize dimensiona la cantidad según el capital arriesgado y la
// distancia absoluta del stop. Devuelve 0 si los datos son inválidos.
func (p IBRiskProfile) PositionSize(balance, entry, stop float64) float64 {
	if entry <= 0 || stop <= 0 {
		return 0
	}
	riskPerUnit := math.Abs(entry - stop)
	if riskPerUnit <= 0 {
		return 0
	}
	return p.RiskAmount(balance) / riskPerUnit
}

// Exposure es el notional de una posición (cantidad x entrada).
func (p IBRiskProfile) Exposure(quantity, entry float64) float64 {
	if quantity <= 0 || entry <= 0 {
		return 0
	}
	return quantity * entry
}

// Leverage es la exposición relativa al balance.
func (p IBRiskProfile) Leverage(exposure, balance float64) float64 {
	if balance <= 0 {
		return 0
	}
	return exposure / balance
}

// WithinLeverage indica si la exposición respeta el apalancamiento máximo.
func (p IBRiskProfile) WithinLeverage(exposure, balance float64) bool {
	l := p.Leverage(exposure, balance)
	return l > 0 && l <= p.MaxLeverage
}

// DailyLossLimit es el límite monetario de pérdida realizada del día, medido
// sobre el balance inicial (definición inequívoca). Si no hay balance inicial
// utilizable, no se puede aplicar el límite (devuelve 0).
func (p IBRiskProfile) DailyLossLimit(acc domain.Account) float64 {
	base := accountInitial(&acc)
	if base <= 0 {
		return 0
	}
	return p.MaxDailyLossPct * base
}

// DailyLossExceeded indica si la pérdida realizada del día alcanzó el límite.
// Si no hay base de balance, falla de forma segura bloqueando nuevas entradas.
func (p IBRiskProfile) DailyLossExceeded(realizedPnL float64, acc domain.Account) bool {
	limit := p.DailyLossLimit(acc)
	if limit <= 0 {
		return true
	}
	return realizedPnL <= -limit
}
