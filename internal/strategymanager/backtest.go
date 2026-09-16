package strategymanager

import (
	"fmt"
	"math"
	"sort"
	"time"

	"tradingview-bot/internal/domain"
)

// BacktestCosts modela los costes de operación del backtest (entrada + salida).
type BacktestCosts struct {
	FeeRate      float64 // comisión por lado (proporción del nocional)
	SlippageRate float64 // deslizamiento por lado (proporción del nocional)
}

func (c BacktestCosts) withDefaults() BacktestCosts {
	if c.FeeRate <= 0 {
		c.FeeRate = 0.001
	}
	if c.SlippageRate <= 0 {
		c.SlippageRate = 0.0002
	}
	return c
}

// CostPerTrade devuelve el coste total por trade round-trip (entrada + salida).
func (c BacktestCosts) CostPerTrade() float64 {
	return 2 * (c.FeeRate + c.SlippageRate)
}

// Thresholds define los umbrales que debe superar una estrategia en la
// porción out-of-sample para pasar la validación.
type Thresholds struct {
	MinTrades       int
	MinWinRate      float64
	MinProfitFactor float64
	MinSharpe       float64
	MaxDrawdown     float64
	Folds           int
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
	if t.Folds <= 0 {
		t.Folds = 4
	}
	return t
}

// Backtest ejecuta walk-forward OOS con folds ventanas expansivas.
//
// Cada fold sigue la separación TRAIN/OOS:
//
//	Sección 0          = TRAIN 1 (calentamiento; no se evalúa)
//	Sección 1          = TEST 1 (OOS fold 1)
//	Secciones 0..1     = TRAIN 2 (TRAIN acumulada)
//	Sección 2          = TEST 2 (OOS fold 2)
//	Secciones 0..2     = TRAIN 3
//	Sección 3          = TEST 3 (OOS fold 3)
//	...
//
// Los indicadores se calculan sobre TODA la serie para que las reglas
// tengan acceso a los valores correctos en cada posición, pero el
// backtest solo EVALúa en la porción OOS de cada fold. No se permite
// que parámetros futuros entren en el OOS.
func Backtest(spec StrategySpec, ks []domain.Kline, th Thresholds, costs ...BacktestCosts) (domain.BacktestResult, error) {
	th = th.withDefaults()
	c := BacktestCosts{}.withDefaults()
	if len(costs) > 0 {
		c = costs[0].withDefaults()
	}
	if len(ks) < 100 {
		return domain.BacktestResult{}, fmt.Errorf("datos insuficientes: %d velas", len(ks))
	}
	folds := th.Folds
	if folds <= 1 {
		folds = 1
	}
	segSize := len(ks) / (folds + 1)
	if segSize < 30 {
		segSize = 30
		folds = len(ks)/segSize - 1
		if folds < 1 {
			folds = 1
		}
	}

	// Calcular indicadores sobre TODA la serie para poder evaluar reglas.
	ctx := newEvalContext(ks, spec.Indicators)

	var foldsMetrics []domain.OOSFold
	var allRets []float64
	totalTrades, totalWins, totalLosses := 0, 0, 0
	var aggNetProfit, aggNetLoss float64
	aggMaxDD := 0.0
	totalBars := 0

	for i := 0; i < folds; i++ {
		testStart := (i + 1) * segSize
		testEnd := (i + 2) * segSize
		if i == folds-1 {
			testEnd = len(ks)
		}
		if testStart >= len(ks) {
			break
		}
		testKS := ks[testStart:testEnd]
		// testStart es el offset global para que runEquity use los
		// indicadores calculados sobre la serie completa.
		m, err := runEquity(ctx, spec, testKS, testStart, c)
		if err != nil {
			return domain.BacktestResult{}, err
		}
		fold := domain.OOSFold{
			Trades:       m.trades,
			WinRate:      m.winRate,
			ProfitFactor: m.profitFactor,
			Sharpe:       m.sharpe,
			Sortino:      m.sortino,
			MaxDrawdown:  m.maxDrawdown,
			TotalReturn:  m.totalReturn,
			Bars:         len(testKS),
		}
		foldsMetrics = append(foldsMetrics, fold)
		totalTrades += m.trades
		totalWins += m.wins
		totalLosses += m.losses
		aggNetProfit += m.netProfit
		aggNetLoss += m.netLoss
		allRets = append(allRets, m.rets...)
		if m.maxDrawdown > aggMaxDD {
			aggMaxDD = m.maxDrawdown
		}
		totalBars += len(testKS)
	}

	aggWR := 0.0
	if totalTrades > 0 {
		aggWR = float64(totalWins) / float64(totalTrades)
	}
	// Profit Factor neto: net profit / net loss.
	aggPF := 0.0
	if aggNetLoss > 0 {
		aggPF = aggNetProfit / aggNetLoss
	} else if aggNetProfit > 0 {
		aggPF = math.Inf(1)
	}
	aggSharpe := sharpe(allRets)
	aggSortino := sortino(allRets)
	// TotalReturn por composición (no log-return exponentiation).
	aggReturn := 0.0
	if len(allRets) > 0 {
		equity := 1.0
		for _, r := range allRets {
			equity *= 1 + r
		}
		aggReturn = (equity - 1) * 100
	}

	passed := totalTrades >= th.MinTrades &&
		aggWR >= th.MinWinRate &&
		aggPF >= th.MinProfitFactor &&
		aggSharpe >= th.MinSharpe &&
		aggMaxDD <= th.MaxDrawdown

	status := "RESEARCH_PASS"
	if !passed {
		status = "REJECTED"
	}

	return domain.BacktestResult{
		StrategyID:   "",
		Trades:       totalTrades,
		WinRate:      aggWR,
		ProfitFactor: aggPF,
		Sharpe:       aggSharpe,
		Sortino:      aggSortino,
		MaxDrawdown:  aggMaxDD,
		TotalReturn:  aggReturn,
		TestBars:     totalBars,
		Passed:       passed,
		MetricsAt:    time.Now(),
		Folds:        folds,
		OOSFolds:     foldsMetrics,
		Status:       status,
	}, nil
}

type equityMetrics struct {
	trades       int
	wins         int
	losses       int
	netProfit    float64 // suma de retornos netos positivos
	netLoss      float64 // suma de retornos netos negativos (valor absoluto)
	totalReturn  float64
	maxDrawdown  float64
	sharpe       float64
	sortino      float64
	winRate      float64
	profitFactor float64
	rets         []float64
}

// runEquity ejecuta el backtest sobre ks con un offset global para
// resolver indicadores calculados sobre la serie completa.
func runEquity(ctx *evalContext, spec StrategySpec, ks []domain.Kline, globalStart int, costs BacktestCosts) (equityMetrics, error) {
	equity := 1.0
	peak := 1.0
	maxDD := 0.0
	var rets []float64
	var trades []tradeResult

	position := -1 // índice local de vela de entrada; -1 = flat
	side := ""
	entryPrice := 0.0

	// Coste round-trip por trade (entrada + salida).
	tradeCost := costs.CostPerTrade()

	applyExit := func(exitPrice float64, reason string) {
		gross := 0.0
		if side == "buy" {
			gross = (exitPrice - entryPrice) / entryPrice
		} else {
			gross = (entryPrice - exitPrice) / entryPrice
		}
		net := gross - tradeCost
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
		// globalIndex es el índice absoluto en la serie completa
		// para consultar los indicadores correctamente.
		globalIndex := globalStart + i
		k := ks[i]
		if position == -1 {
			// buscar entrada (long y short no simultáneos; se prioriza buy)
			if ruleMatches(ctx, spec.Entries, "buy", globalIndex) {
				position = i
				side = "buy"
				entryPrice = k.Close
				continue
			}
			if ruleMatches(ctx, spec.Entries, "sell", globalIndex) {
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
		if ruleMatches(ctx, spec.Exits, side, globalIndex) {
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
			m.netProfit += t.net
		} else {
			m.losses++
			m.netLoss += -t.net
		}
	}
	if m.trades > 0 {
		m.winRate = float64(m.wins) / float64(m.trades)
	}
	if m.netLoss > 0 {
		m.profitFactor = m.netProfit / m.netLoss
	} else if m.netProfit > 0 {
		m.profitFactor = math.Inf(1)
	}
	m.sharpe = sharpe(rets)
	m.sortino = sortino(rets)
	m.rets = rets
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

// sortino calcula el ratio de Sortino anualizado usando solo volatilidad negativa.
func sortino(rets []float64) float64 {
	if len(rets) < 2 {
		return 0
	}
	var sum, negSq float64
	negN := 0
	for _, r := range rets {
		sum += r
		if r < 0 {
			negSq += r * r
			negN++
		}
	}
	mean := sum / float64(len(rets))
	if negN == 0 {
		if mean > 0 {
			return math.Inf(1)
		}
		return 0
	}
	downStd := math.Sqrt(negSq / float64(negN))
	if downStd == 0 {
		return 0
	}
	return (mean / downStd) * math.Sqrt(8760)
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
