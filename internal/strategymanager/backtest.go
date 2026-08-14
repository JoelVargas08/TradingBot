package strategymanager

import (
	"fmt"
	"math"
	"sort"
	"time"

	"tradingview-bot/internal/domain"
)

// Thresholds define los umbrales que debe superar una estrategia en la
// porción out-of-sample para pasar la validación.
type Thresholds struct {
	MinTrades       int
	MinWinRate      float64
	MinProfitFactor float64
	MinSharpe       float64
	MaxDrawdown     float64
}

func (t Thresholds) withDefaults() Thresholds {
	if t.MinTrades <= 0 {
		t.MinTrades = 8
	}
	if t.MinWinRate <= 0 {
		t.MinWinRate = 0.45
	}
	if t.MinProfitFactor <= 0 {
		t.MinProfitFactor = 1.2
	}
	if t.MinSharpe <= 0 {
		t.MinSharpe = 0.5
	}
	if t.MaxDrawdown <= 0 {
		t.MaxDrawdown = 0.30
	}
	return t
}

// Backtest ejecuta el spec sobre velas y devuelve métricas out-of-sample
// (porción de test tras un warmup y una porción de walk-forward).
func Backtest(spec StrategySpec, ks []domain.Kline, th Thresholds) (domain.BacktestResult, error) {
	th = th.withDefaults()
	if len(ks) < 100 {
		return domain.BacktestResult{}, fmt.Errorf("datos insuficientes: %d velas", len(ks))
	}
	// Índices: [0, trainEnd) entrenamiento, [trainEnd, n) test.
	trainEnd := int(float64(len(ks)) * 0.6)
	if trainEnd < 60 {
		trainEnd = 60
	}

	// Calcular indicadores sobre TODA la serie (los que se usan en test son
	// independientes de parámetros entrenables; validación de forma general).
	ctx := newEvalContext(ks, spec.Indicators)

	metrics, err := runEquity(ctx, spec, ks[trainEnd:], 0.001)
	if err != nil {
		return domain.BacktestResult{}, err
	}

	// Sharpe aproximado con las rentabilidades por barra.
	return domain.BacktestResult{
		StrategyID:   "",
		Trades:       metrics.trades,
		WinRate:      metrics.winRate,
		ProfitFactor: metrics.profitFactor,
		Sharpe:       metrics.sharpe,
		MaxDrawdown:  metrics.maxDrawdown,
		TotalReturn:  metrics.totalReturn,
		TestBars:     len(ks) - trainEnd,
		Passed: metrics.trades >= th.MinTrades &&
			metrics.winRate >= th.MinWinRate &&
			metrics.profitFactor >= th.MinProfitFactor &&
			metrics.sharpe >= th.MinSharpe &&
			metrics.maxDrawdown <= th.MaxDrawdown,
		MetricsAt: time.Now(),
	}, nil
}

type equityMetrics struct {
	trades       int
	wins         int
	losses       int
	grossProfit  float64
	grossLoss    float64
	totalReturn  float64
	maxDrawdown  float64
	sharpe       float64
	winRate      float64
	profitFactor float64
}

func runEquity(ctx *evalContext, spec StrategySpec, ks []domain.Kline, fee float64) (equityMetrics, error) {
	equity := 1.0
	peak := 1.0
	maxDD := 0.0
	var rets []float64
	var trades []tradeResult

	position := -1 // índice de vela de entrada; -1 = flat
	side := ""
	entryPrice := 0.0

	applyExit := func(exitPrice float64, reason string) {
		gross := 0.0
		if side == "buy" {
			gross = (exitPrice - entryPrice) / entryPrice
		} else {
			gross = (entryPrice - exitPrice) / entryPrice
		}
		net := gross - fee
		equity *= 1 + net
		if equity > peak {
			peak = equity
		}
		if dd := (peak - equity) / peak; dd > maxDD {
			maxDD = dd
		}
		trades = append(trades, tradeResult{gross: gross, net: net})
		rets = append(rets, net)
		position = -1
		side = ""
		entryPrice = 0
	}

	for i := range ks {
		k := ks[i]
		if position == -1 {
			// buscar entrada (long y short no simultáneos; se prioriza buy)
			if ruleMatches(ctx, spec.Entries, "buy", i) {
				position = i
				side = "buy"
				entryPrice = k.Close
				continue
			}
			if ruleMatches(ctx, spec.Entries, "sell", i) {
				position = i
				side = "sell"
				entryPrice = k.Close
				continue
			}
			continue
		}
		// en posición: comprobar stop/take
		stop := 0.0
		target := 0.0
		if spec.StopPct > 0 {
			stop = entryPrice * (1 - spec.StopPct/100)
			if side == "sell" {
				stop = entryPrice * (1 + spec.StopPct/100)
			}
		}
		if spec.TakeProfitPct > 0 {
			target = entryPrice * (1 + spec.TakeProfitPct/100)
			if side == "sell" {
				target = entryPrice * (1 - spec.TakeProfitPct/100)
			}
		}
		var hitStop, hitTarget bool
		if side == "buy" {
			hitStop = spec.StopPct > 0 && k.Low <= stop
			hitTarget = spec.TakeProfitPct > 0 && k.High >= target
		} else {
			hitStop = spec.StopPct > 0 && k.High >= stop
			hitTarget = spec.TakeProfitPct > 0 && k.Low <= target
		}
		if hitStop && hitTarget {
			// si ambos se cruzan en la misma vela, asumir el peor caso
			applyExit(stop, "stop")
			continue
		}
		if hitStop {
			applyExit(stop, "stop")
			continue
		}
		if hitTarget {
			applyExit(target, "take-profit")
			continue
		}
		// salida por regla (exits)
		if ruleMatches(ctx, spec.Exits, side, i) {
			applyExit(k.Close, "exit")
		}
	}
	// cerrar en la última vela si sigue abierta
	if position != -1 {
		applyExit(ks[len(ks)-1].Close, "end")
	}

	m := equityMetrics{
		trades:      len(trades),
		totalReturn: (equity - 1) * 100,
		maxDrawdown: maxDD,
	}
	for _, t := range trades {
		if t.net > 0 {
			m.wins++
			m.grossProfit += t.gross
		} else {
			m.losses++
			m.grossLoss += -t.gross
		}
	}
	if m.trades > 0 {
		m.winRate = float64(m.wins) / float64(m.trades)
	}
	if m.grossLoss > 0 {
		m.profitFactor = m.grossProfit / m.grossLoss
	} else if m.grossProfit > 0 {
		m.profitFactor = math.Inf(1)
	}
	m.sharpe = sharpe(rets)
	return m, nil
}

type tradeResult struct {
	gross float64
	net   float64
}

// sharpe calcula el ratio de Sharpe anualizado (proxy) a partir de retornos.
func sharpe(rets []float64) float64 {
	if len(rets) < 2 {
		return 0
	}
	mean, std := meanStd(rets)
	if std == 0 {
		return 0
	}
	perPeriod := mean / std
	// asumimos operaciones en velas 1h → ~8760 al año; usamos sqrt(8760)
	return perPeriod * math.Sqrt(8760)
}

func meanStd(values []float64) (mean, std float64) {
	n := float64(len(values))
	if n == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	mean = sum / n
	var sq float64
	for _, v := range values {
		d := v - mean
		sq += d * d
	}
	std = math.Sqrt(sq / n)
	return mean, std
}

// evalContext calcula los indicadores y resuelve operandos en condiciones.
type evalContext struct {
	series  map[string][]float64
	lengths map[string]int
	lastIdx int
}

func newEvalContext(ks []domain.Kline, inds []Indicator) *evalContext {
	c := &evalContext{
		series:  make(map[string][]float64),
		lengths: make(map[string]int),
		lastIdx: len(ks) - 1,
	}
	c.series["close"] = closeSeries(ks)
	c.series["open"] = openSeries(ks)
	c.series["high"] = highSeries(ks)
	c.series["low"] = lowSeries(ks)
	c.series["volume"] = volumeSeries(ks)
	for _, ind := range inds {
		c.series[ind.ID] = indicatorSeries(ks, ind)
		c.lengths[ind.ID] = ind.Length
	}
	return c
}

// value resuelve un operando en un índice: indicador, serie cruda o número.
func (c *evalContext) value(operand string, i int) (float64, bool) {
	if i < 0 || i >= c.lastIdx+1 {
		return 0, false
	}
	if s, ok := c.series[operand]; ok {
		return s[i], true
	}
	if n, ok := parseNumber(operand); ok {
		return n, true
	}
	return 0, false
}

func ruleMatches(ctx *evalContext, rules []Rule, side string, i int) bool {
	matched := false
	for _, rule := range rules {
		if rule.Side != side {
			continue
		}
		ok := true
		for _, cond := range rule.Conditions {
			if !condMatches(ctx, cond, i) {
				ok = false
				break
			}
		}
		if ok {
			matched = true
		}
	}
	return matched
}

func condMatches(ctx *evalContext, c Condition, i int) bool {
	left, ok := ctx.value(c.Left, i)
	if !ok {
		return false
	}
	right, ok := ctx.value(fmt.Sprintf("%v", c.Right), i)
	if !ok {
		return false
	}
	switch c.Op {
	case ">":
		return left > right
	case "<":
		return left < right
	case ">=":
		return left >= right
	case "<=":
		return left <= right
	case "=":
		return left == right
	case "cross_above":
		prevL, okL := ctx.value(c.Left, i-1)
		prevR, okR := ctx.value(fmt.Sprintf("%v", c.Right), i-1)
		if !okL || !okR {
			return false
		}
		return prevL <= prevR && left > right
	case "cross_below":
		prevL, okL := ctx.value(c.Left, i-1)
		prevR, okR := ctx.value(fmt.Sprintf("%v", c.Right), i-1)
		if !okL || !okR {
			return false
		}
		return prevL >= prevR && left < right
	}
	return false
}

// sortedTradeSeries auxiliar para tests/inspección.
func sortedTradeSeries(trades []tradeResult) []float64 {
	out := make([]float64, len(trades))
	for i, t := range trades {
		out[i] = t.net
	}
	sort.Float64s(out)
	return out
}
