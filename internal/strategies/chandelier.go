// Package strategies contiene implementaciones en Go de estrategias
// backtesteables directamente (sin sidecar Python).
package strategies

import (
	"math"

	"tradingview-bot/internal/backtest"
	"tradingview-bot/internal/domain"
)

// Chandelier implementa el Chandelier Exit (bandas basadas en máximos/mínimos
// y ATR) como generador de señales de estado objetivo para el backtest.
//   - Long: se entra cuando el cierre cruza por encima de
//     max(high, n) - mult*ATR; se sale cuando el cierre cae por debajo.
//   - Short: se entra cuando el cierre cruza por debajo de
//     min(low, n) + mult*ATR; se sale cuando el cierre sube por encima.
type Chandelier struct {
	Period     int     // ventana de máximos/mínimos (típico 22)
	Multiplier float64 // multiplicador del ATR (típico 3.0)
	ATRPeriod  int     // periodo del ATR de Wilder (típico 22)
}

// DefaultChandelier devuelve la configuración clásica de Chandelier.
func DefaultChandelier() Chandelier {
	return Chandelier{Period: 22, Multiplier: 3.0, ATRPeriod: 22}
}

// Signal devuelve la señal al cierre de la vela i sin lookahead.
func (c Chandelier) Signal(i int, candles []domain.Kline) backtest.Direction {
	if c.Period < 2 || c.ATRPeriod < 2 || len(candles) == 0 {
		return backtest.SignalNone
	}
	if i < c.ATRPeriod {
		return backtest.SignalNone
	}

	atrVal := wilderATR(candles, c.ATRPeriod)[i]
	hi, lo := c.highestHigh(candles, i), c.lowestLow(candles, i)
	bandLong := hi - c.Multiplier*atrVal
	bandShort := lo + c.Multiplier*atrVal

	close := candles[i].Close
	closePrev := candles[i-1].Close
	atrPrev := wilderATR(candles, c.ATRPeriod)[i-1]
	hiPrev := c.highestHigh(candles, i-1)
	loPrev := c.lowestLow(candles, i-1)
	bandLongPrev := hiPrev - c.Multiplier*atrPrev
	bandShortPrev := loPrev + c.Multiplier*atrPrev

	// primera barra válida: inicializar estado según la posición actual
	if i == c.ATRPeriod {
		if close > bandLong {
			return backtest.SignalLong
		}
		if close < bandShort {
			return backtest.SignalShort
		}
		return backtest.SignalNone
	}

	// cruz arriba de la banda long → long
	if close > bandLong && closePrev <= bandLongPrev {
		return backtest.SignalLong
	}
	// cruz abajo de la banda short → short
	if close < bandShort && closePrev >= bandShortPrev {
		return backtest.SignalShort
	}
	return backtest.SignalNone
}

func (c Chandelier) highestHigh(candles []domain.Kline, i int) float64 {
	start := 0
	if i-c.Period+1 > start {
		start = i - c.Period + 1
	}
	hi := candles[start].High
	for j := start + 1; j <= i; j++ {
		if candles[j].High > hi {
			hi = candles[j].High
		}
	}
	return hi
}

func (c Chandelier) lowestLow(candles []domain.Kline, i int) float64 {
	start := 0
	if i-c.Period+1 > start {
		start = i - c.Period + 1
	}
	lo := candles[start].Low
	for j := start + 1; j <= i; j++ {
		if candles[j].Low < lo {
			lo = candles[j].Low
		}
	}
	return lo
}

// wilderATR devuelve el ATR de Wilder sobre las velas.
func wilderATR(candles []domain.Kline, length int) []float64 {
	out := make([]float64, len(candles))
	if length <= 0 || len(candles) == 0 {
		return out
	}
	tr := make([]float64, len(candles))
	for i, k := range candles {
		tr[i] = k.High - k.Low
		if i > 0 {
			tr[i] = math.Max(tr[i], math.Abs(k.High-candles[i-1].Close))
			tr[i] = math.Max(tr[i], math.Abs(k.Low-candles[i-1].Close))
		}
	}
	prev := 0.0
	started := false
	for i := 0; i < len(candles); i++ {
		if !started {
			if i < length-1 {
				continue
			}
			var sum float64
			for j := 0; j < length; j++ {
				sum += tr[i-j]
			}
			prev = sum / float64(length)
			out[i] = prev
			started = true
			continue
		}
		prev = (prev*(float64(length)-1) + tr[i]) / float64(length)
		out[i] = prev
	}
	return out
}
