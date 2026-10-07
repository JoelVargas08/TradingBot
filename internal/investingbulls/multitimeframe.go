package investingbulls

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/observability"
)

type MultiTimeframeConfig struct {
	MainTimeframe     string
	EntryTimeframe    string
	ConfirmTimeframe  string
	RequireConfirm    bool
	MainSwingLeft     int
	MainSwingRight    int
	EntrySwingLeft    int
	EntrySwingRight   int
	ConfirmSwingLeft  int
	ConfirmSwingRight int
}

func DefaultMultiTimeframeConfig() MultiTimeframeConfig {
	return MultiTimeframeConfig{MainTimeframe: "1h", EntryTimeframe: "15m", ConfirmTimeframe: "5m", MainSwingLeft: 2, MainSwingRight: 2, EntrySwingLeft: 2, EntrySwingRight: 2, ConfirmSwingLeft: 2, ConfirmSwingRight: 2}
}

type MultiTimeframeModel struct {
	Version    int
	Family     string
	Symbol     string
	Timeframes MultiTimeframeConfig
	// Search conserva el espacio de búsqueda que se utilizó. El walk-forward
	// vuelve a aprender ese mismo espacio en cada fold; no reutiliza la
	// configuración elegida sobre todo el histórico.
	Search SearchSpace
	// ConfirmSkipReason deja constancia de que el modelo se aprendió sin la
	// confirmación de 5m porque no había histórico para ese timeframe (sec 15).
	ConfirmSkipReason string
	Base              LearnedModel
	AllowedSetups     []SetupType
	SetupStats        map[SetupType]SetupStats
	DatasetHash       string
	DatasetStart      time.Time
	DatasetEnd        time.Time
	DatasetBars       int
	CodeVersion       string
	LearnedAt         time.Time
}

type SetupStats struct {
	Trades       int
	WinRate      float64
	ProfitFactor float64
	TotalReturn  float64
	MaxDrawdown  float64
}

// MultiTimeframeLearnResult expone el diagnóstico del learn base: es la forma de
// explicar por qué el pipeline MTF no produjo candidata (sec 12 del documento).
type MultiTimeframeLearnResult struct {
	Model       MultiTimeframeModel
	Trades      []LearnedTrade
	Accepted    bool
	Reason      string
	SpecJSON    string
	Diagnostics LearnDiagnostics
}

func normalizeMTF(c MultiTimeframeConfig) MultiTimeframeConfig {
	d := DefaultMultiTimeframeConfig()
	if c.MainTimeframe == "" {
		c.MainTimeframe = d.MainTimeframe
	}
	if c.EntryTimeframe == "" {
		c.EntryTimeframe = d.EntryTimeframe
	}
	if c.ConfirmTimeframe == "" {
		c.ConfirmTimeframe = d.ConfirmTimeframe
	}
	if c.MainSwingLeft <= 0 {
		c.MainSwingLeft = 2
	}
	if c.MainSwingRight <= 0 {
		c.MainSwingRight = 2
	}
	if c.EntrySwingLeft <= 0 {
		c.EntrySwingLeft = 2
	}
	if c.EntrySwingRight <= 0 {
		c.EntrySwingRight = 2
	}
	if c.ConfirmSwingLeft <= 0 {
		c.ConfirmSwingLeft = 2
	}
	if c.ConfirmSwingRight <= 0 {
		c.ConfirmSwingRight = 2
	}
	return c
}

// LearnMultiTimeframe aprende el pipeline EXACTO que ejecutará el modelo:
// 1H (dirección) + 15m (setup confirmado) + Fibonacci + imbalance + order block
// + 5m opcional + plan de entrada al siguiente open. La grid solo optimiza
// parámetros permitidos por el perfil Investing Bulls; no elimina reglas del
// setup.
//
// Importante: el learner MTF NO llama a Learn(entry, cfg). Hacerlo primero
// aprendería sobre un espacio de señales más amplio que el que realmente
// ejecuta MTF y luego filtraría esas operaciones, pudiendo producir una
// candidata con 0 trades MTF. Aquí cada configuración se evalúa directamente
// sobre el pipeline MTF ejecutable.
func LearnMultiTimeframe(main, entry, confirm []domain.Kline, cfg LearnConfig, mtf MultiTimeframeConfig) (MultiTimeframeLearnResult, error) {
	mtf = normalizeMTF(mtf)
	if len(main) < 100 || len(entry) < 100 {
		return MultiTimeframeLearnResult{}, fmt.Errorf("learn mtf: se necesitan al menos 100 velas en 1H y 15m")
	}
	if err := ValidateKlines(main, cfg.Symbol, mtf.MainTimeframe); err != nil {
		return MultiTimeframeLearnResult{}, fmt.Errorf("learn mtf 1H: %w", err)
	}
	if err := ValidateKlines(entry, cfg.Symbol, mtf.EntryTimeframe); err != nil {
		return MultiTimeframeLearnResult{}, fmt.Errorf("learn mtf 15m: %w", err)
	}
	if mtf.RequireConfirm && len(confirm) > 0 {
		if err := ValidateKlines(confirm, cfg.Symbol, mtf.ConfirmTimeframe); err != nil {
			return MultiTimeframeLearnResult{}, fmt.Errorf("learn mtf 5m: %w", err)
		}
	}
	confirmSkipReason := ""
	if mtf.RequireConfirm && len(confirm) == 0 {
		confirmSkipReason = fmt.Sprintf("sin velas %s: se aprende con 1H+15m", mtf.ConfirmTimeframe)
		mtf.RequireConfirm = false
		observability.Log(observability.LearnStarted, "mode", "mtf", "symbol", cfg.Symbol, "status", "confirm_skipped", "reason", confirmSkipReason)
	}
	cfg.Timeframe = mtf.EntryTimeframe
	result, err := learnMultiTimeframeWindow(main, entry, confirm, cfg, mtf, 0, confirmSkipReason)
	if err != nil {
		return MultiTimeframeLearnResult{}, err
	}
	if result.SpecJSON == "" {
		return result, fmt.Errorf("learn mtf: el aprendizaje MTF exacto no produjo un candidato\n%s", result.Reason)
	}
	return result, nil
}

// learnMultiTimeframeWindow entrena una candidata MTF sobre UN ÚNICO histórico.
// evaluationStart solo se utiliza cuando el caller necesita generar señales
// dentro de una ventana OOS; durante el entrenamiento permanece en 0.
//
// Todas las configuraciones de SearchSpace pasan por collectMTFDecisionPoints,
// que ya contiene las reglas operativas de 1H/15m/5m. Por eso el learner y el
// simulador MTF comparten exactamente la misma definición de entrada.
func learnMultiTimeframeWindow(main, entry, confirm []domain.Kline, cfg LearnConfig, mtf MultiTimeframeConfig, evaluationStart int, confirmSkipReason string) (MultiTimeframeLearnResult, error) {
	cfg = normalizeLearnConfig(cfg)
	mtf = normalizeMTF(mtf)
	allowed := allMTFSetups()
	digest := datasetDigestMultiTf(main, entry, confirm)
	diag := LearnDiagnostics{Candles: len(entry), MinTrades: cfg.MinTrades}
	space := cfg.Search.normalized()
	cfg.Search = space
	diag.ConfigsEvaluated = space.Size()

	decisions := collectMTFDecisionPoints(main, entry, confirm, cfg, mtf, allowed, evaluationStart, &diag)

	bestScore := math.Inf(-1)
	var bestModel LearnedModel
	var bestTrades []LearnedTrade
	var bestMetrics learnMetrics
	var bestCfg LearnConfig

	for _, testCfg := range space.Configs(cfg) {
		trades := simulateMTFDecisionPoints(entry, decisions, testCfg, evaluationStart)
		metrics := tradeMetrics(trades, cfg.InitialBalance)
		recordBestAttempt(&diag, testCfg, metrics)
		if metrics.trades < cfg.MinTrades {
			continue
		}
		diag.ConfigsWithEnoughTrades++

		score := metrics.totalReturn / (1 + metrics.maxDrawdown)
		if metrics.profitFactor > 0 {
			score *= math.Min(metrics.profitFactor, 5)
		}
		if score > bestScore {
			bestScore = score
			bestTrades = trades
			bestMetrics = metrics
			bestCfg = testCfg
			bestModel = newLearnedModel(cfg, testCfg, digest, entry, metrics)
		}
	}

	if bestScore == math.Inf(-1) {
		return MultiTimeframeLearnResult{
			Accepted: false,
			Reason: noCandidateReason(cfg, diag),
			Diagnostics: diag,
			Model: MultiTimeframeModel{
				Version: 1, Family: "investing_bulls_mtf", Symbol: cfg.Symbol,
				Timeframes: mtf, Search: space, ConfirmSkipReason: confirmSkipReason,
				AllowedSetups: allowed, DatasetHash: digest,
				DatasetStart: entry[0].Start, DatasetEnd: entry[len(entry)-1].Start,
				DatasetBars: len(entry), CodeVersion: CodeVersion, LearnedAt: time.Now().UTC(),
			},
		}, nil
	}

	recordSelectedConfig(&diag, bestCfg, bestMetrics)
	model := MultiTimeframeModel{
		Version: 1, Family: "investing_bulls_mtf", Symbol: cfg.Symbol,
		Timeframes: mtf, Search: space, ConfirmSkipReason: confirmSkipReason,
		Base: bestModel, AllowedSetups: allowed,
		SetupStats: statsBySetup(bestTrades, cfg.InitialBalance),
		DatasetHash: digest,
		DatasetStart: entry[0].Start, DatasetEnd: entry[len(entry)-1].Start,
		DatasetBars: len(entry), CodeVersion: CodeVersion, LearnedAt: time.Now().UTC(),
	}
	raw, err := json.MarshalIndent(model, "", "  ")
	if err != nil {
		return MultiTimeframeLearnResult{}, err
	}

	accepted := bestMetrics.trades >= cfg.MinTrades && bestMetrics.profitFactor > 1
	reason := "candidato MTF generado; requiere validación OOS antes de activarse"
	if !accepted {
		reason = "candidato MTF generado pero no supera el filtro in-sample"
	}
	if confirmSkipReason != "" {
		reason += "; " + confirmSkipReason
	}

	observability.Log(observability.LearnCompleted,
		"scope", "learn_mtf_exact",
		"symbol", cfg.Symbol,
		"candles", len(entry),
		"configs", diag.ConfigsEvaluated,
		"configs_ok", diag.ConfigsWithEnoughTrades,
		"trades", bestMetrics.trades,
		"profit_factor", bestMetrics.profitFactor,
	)

	return MultiTimeframeLearnResult{
		Model: model, Trades: bestTrades, Accepted: accepted,
		Reason: reason, SpecJSON: string(raw), Diagnostics: diag,
	}, nil
}

func allMTFSetups() []SetupType {
	return []SetupType{
		SetupCHOCHLong, SetupCHOCHShort,
		SetupContinuationLong, SetupContinuationShort,
	}
}

type mtfDecisionPoint struct {
	index     int
	entryBar  int
	direction domain.Direction
	setupType SetupType
	evidence  ConfluenceEvidence
	levels    planInputs
}

// collectMTFDecisionPoints construye las oportunidades usando el mismo pipeline
// que MTF LIVE. La estructura de 1H, el setup 15m, Fibonacci, imbalance,
// order block y la confirmación 5m se resuelven antes de que la grid pueda
// decidir distancia/stop. Así el learner no puede optimizar una señal que luego
// será descartada por el ejecutor MTF.
func collectMTFDecisionPoints(
	main, entry, confirm []domain.Kline,
	cfg LearnConfig,
	mtf MultiTimeframeConfig,
	allowed []SetupType,
	evaluationStart int,
	diag *LearnDiagnostics,
) []mtfDecisionPoint {
	if evaluationStart < minLearningBars {
		evaluationStart = minLearningBars
	}
	allow := map[SetupType]bool{}
	for _, s := range allowed {
		allow[s] = true
	}

	ic := DefaultImbalanceConfig()
	bc := DefaultOrderBlockConfig()
	out := make([]mtfDecisionPoint, 0)

	for i := evaluationStart; i < len(entry)-1; i++ {
		mainPrefix := closedCandlesBefore(main, entry[i].Start)
		if len(mainPrefix) < 30 {
			continue
		}
		ms := Analyze(mainPrefix, mtf.MainSwingLeft, mtf.MainSwingRight)
		if ms.Trend != TrendBullish && ms.Trend != TrendBearish {
			continue
		}
		if diag != nil {
			diag.TrendBars++
		}

		ep := entry[:i+1]
		es := Analyze(ep, cfg.SwingLeft, cfg.SwingRight)
		setups := ClassifySetups(es)
		var candidate *ClassifiedSetup
		for j := len(setups) - 1; j >= 0; j-- {
			c := setups[j]
			if c.Index > i || !allow[c.SetupType] {
				continue
			}
			if c.Direction == domain.DirectionBuy && ms.Trend != TrendBullish {
				continue
			}
			if c.Direction == domain.DirectionSell && ms.Trend != TrendBearish {
				continue
			}
			candidate = &c
			break
		}
		if candidate == nil {
			continue
		}

		fib, ok := fibonacciFromLatestImpulse(es, cfg.Fib)
		if !ok {
			continue
		}
		imbs := UpdateImbalances(DetectImbalances(ep, ic), ep, ic)
		blocks := UpdateOrderBlocks(DetectOrderBlocks(ep, es.Breaks, bc), ep, bc)
		evidence, ok := CollectConfluenceEvidence(ep, i, es, fib, imbs, blocks)
		if !ok {
			continue
		}
		if evidence.FibZone == 0 && evidence.ImbalanceIndex < 0 && evidence.OrderBlockIndex < 0 {
			continue
		}

		if mtf.RequireConfirm && !confirmationMatches(confirm, entry[i].Start, candidate.Direction, mtf) {
			continue
		}

		// LIVE recibe la señal al cierre de la vela 15m. El learner MTF usa
		// exactamente ese mismo precio de decisión; la siguiente vela solo
		// resuelve el resultado de la operación.
		entryPrice := entry[i].Close
		if entryPrice <= 0 {
			continue
		}

		levels := resolvePlanInputs(Setup{
			Index: i, Direction: candidate.Direction,
			OrderBlockIndex: evidence.OrderBlockIndex,
		}, fib, entryPrice, es, blocks)

		if diag != nil {
			diag.SetupsDetected++
			if evidence.FibZone > 0 {
				diag.SetupsWithFibonacci++
			}
			if evidence.ImbalanceIndex >= 0 {
				diag.SetupsWithImbalance++
			}
			if evidence.OrderBlockIndex >= 0 {
				diag.SetupsWithOrderBlock++
			}
		}

		out = append(out, mtfDecisionPoint{
			index: i, entryBar: i,
			direction: candidate.Direction, setupType: candidate.SetupType,
			evidence: evidence, levels: levels,
		})
	}
	return out
}

// simulateMTFDecisionPoints ejecuta las oportunidades ya validadas por el
// pipeline MTF. Solo la configuración de confluencia y el stop/plan se aplican
// aquí; no se vuelven a introducir filtros distintos a los usados durante el
// aprendizaje.
func simulateMTFDecisionPoints(ks []domain.Kline, decisions []mtfDecisionPoint, cfg LearnConfig, evaluationStart int) []LearnedTrade {
	if len(decisions) == 0 || len(ks) < 2 {
		return nil
	}
	byBar := make(map[int]int, len(decisions))
	for i := range decisions {
		if decisions[i].index >= evaluationStart {
			byBar[decisions[i].index] = i
		}
	}

	var out []LearnedTrade
	inTrade := false
	var open LearnedTrade
	stop, target := 0.0, 0.0
	start := evaluationStart
	if start < minLearningBars {
		start = minLearningBars
	}

	for i := start; i < len(ks)-1; i++ {
		if inTrade {
			k := ks[i]
			hitStop, hitTarget := false, false
			if open.Direction == domain.DirectionBuy {
				hitStop, hitTarget = k.Low <= stop, k.High >= target
			} else {
				hitStop, hitTarget = k.High >= stop, k.Low <= target
			}
			if hitStop || hitTarget {
				exit, reason := stop, "stop"
				if hitTarget && !hitStop {
					exit, reason = target, "take"
				}
				exit = applyExitSlippage(exit, open.Direction, cfg.SlippagePct)
				open.ExitBar, open.ExitPrice, open.Reason = i, exit, reason
				open.PnL = tradePnL(open.Direction, open.EntryPrice, exit) - 2*cfg.FeePct
				out = append(out, open)
				inTrade = false
			}
			continue
		}

		idx, ok := byBar[i]
		if !ok {
			continue
		}
		d := decisions[idx]
		setup, ok := d.evidence.Setup(cfg.Confluence)
		if !ok || !setup.Valid || setup.Direction != d.direction {
			continue
		}
		plan, ok := buildPlan(d.levels, cfg.TradePlan)
		if !ok {
			continue
		}

		open = LearnedTrade{
			Direction: d.direction,
			EntryBar: d.entryBar,
			EntryPrice: d.levels.Entry * entryMultiplier(d.direction, cfg.SlippagePct),
			FeePct: cfg.FeePct,
			Setup: d.setupType,
			Components: setup.Components,
		}
		stop, target = plan.StopLoss, plan.TakeProfit
		inTrade = true
	}

	if inTrade {
		last := ks[len(ks)-1]
		exit := applyExitSlippage(last.Close, open.Direction, cfg.SlippagePct)
		open.ExitBar, open.ExitPrice, open.Reason = len(ks)-1, exit, "end"
		open.PnL = tradePnL(open.Direction, open.EntryPrice, exit) - 2*cfg.FeePct
		out = append(out, open)
	}
	return out
}

// simulateMultiTimeframe conserva la API usada por LIVE/tests y ahora comparte
// la misma colección de decisiones que el learner MTF.
func simulateMultiTimeframe(main, entry, confirm []domain.Kline, cfg LearnConfig, mtf MultiTimeframeConfig, base LearnedModel, allowed []SetupType) []LearnedTrade {
	cfg.SwingLeft = base.SwingLeft
	cfg.SwingRight = base.SwingRight
	cfg.Fib = base.Fib
	cfg.Confluence = base.Confluence
	cfg.TradePlan = base.TradePlan
	cfg.FeePct = base.FeePct
	cfg.SlippagePct = base.SlippagePct
	if len(allowed) == 0 {
		allowed = allMTFSetups()
	}
	points := collectMTFDecisionPoints(main, entry, confirm, cfg, mtf, allowed, 0, nil)
	return simulateMTFDecisionPoints(entry, points, cfg, 0)
}

func candlesThrough(ks []domain.Kline, ts time.Time) []domain.Kline {
	n := sort.Search(len(ks), func(i int) bool { return !ks[i].Start.Before(ts) })
	if n == 0 {
		return nil
	}
	if n < len(ks) && ks[n].Start.Equal(ts) {
		return ks[:n+1]
	}
	return ks[:n]
}

// closedCandlesBefore devuelve solo las velas cerradas estrictamente antes de ts.
// Para una decisión al cierre de la vela de entrada (ts) la vela del timeframe
// mayor que abre en ts aún NO está cerrada: usarla sería lookahead.
func closedCandlesBefore(ks []domain.Kline, ts time.Time) []domain.Kline {
	n := sort.Search(len(ks), func(i int) bool { return !ks[i].Start.Before(ts) })
	return ks[:n]
}
func confirmationMatches(ks []domain.Kline, ts time.Time, dir domain.Direction, c MultiTimeframeConfig) bool {
	// En la decisión del 15m, una vela 5m que abrió exactamente en ts todavía
	// pertenece al futuro. Solo se usan velas 5m cerradas estrictamente antes.
	p := closedCandlesBefore(ks, ts)
	if len(p) < 10 {
		return false
	}
	s := Analyze(p, c.ConfirmSwingLeft, c.ConfirmSwingRight)
	if len(s.Breaks) == 0 {
		return false
	}
	return s.Breaks[len(s.Breaks)-1].Direction == dir
}

func statsBySetup(trades []LearnedTrade, initial float64) map[SetupType]SetupStats {
	groups := map[SetupType][]LearnedTrade{}
	for _, t := range trades {
		s := t.Setup
		if s.Valid() {
			groups[s] = append(groups[s], t)
		}
	}
	out := map[SetupType]SetupStats{}
	for s, ts := range groups {
		m := tradeMetrics(ts, initial)
		out[s] = SetupStats{Trades: m.trades, WinRate: m.winRate, ProfitFactor: recordedProfitFactor(m.profitFactor), TotalReturn: recordedMetric(m.totalReturn), MaxDrawdown: recordedMetric(m.maxDrawdown)}
	}
	return out
}
func filterTradesBySetup(trades []LearnedTrade, allowed []SetupType) []LearnedTrade {
	set := map[SetupType]bool{}
	for _, s := range allowed {
		set[s] = true
	}
	out := make([]LearnedTrade, 0, len(trades))
	for _, t := range trades {
		if set[t.Setup] {
			out = append(out, t)
		}
	}
	return out
}

func LearnMultiTimeframeAndPersist(ctx context.Context, store LearnerStore, symbol string, limit int, cfg LearnConfig, mtf MultiTimeframeConfig) (domain.Strategy, MultiTimeframeLearnResult, error) {
	observability.Log(observability.LearnStarted, "mode", "mtf", "symbol", symbol, "timeframes", mtf.MainTimeframe+"/"+mtf.EntryTimeframe+"/"+mtf.ConfirmTimeframe)
	mtf = normalizeMTF(mtf)
	if limit <= 0 {
		limit = 5000
	}
	main, err := store.RecentCandles(ctx, symbol, mtf.MainTimeframe, limit)
	if err != nil {
		return domain.Strategy{}, MultiTimeframeLearnResult{}, fmt.Errorf("1H: %w", err)
	}
	entry, err := store.RecentCandles(ctx, symbol, mtf.EntryTimeframe, limit)
	if err != nil {
		return domain.Strategy{}, MultiTimeframeLearnResult{}, fmt.Errorf("15m: %w", err)
	}
	var confirm []domain.Kline
	if mtf.RequireConfirm {
		confirm, err = store.RecentCandles(ctx, symbol, mtf.ConfirmTimeframe, limit)
		if err != nil {
			return domain.Strategy{}, MultiTimeframeLearnResult{}, fmt.Errorf("5m: %w", err)
		}
	}
	main = closedCandles(main)
	entry = closedCandles(entry)
	confirm = closedCandles(confirm)
	cfg.Symbol = symbol
	result, err := LearnMultiTimeframe(main, entry, confirm, cfg, mtf)
	if err != nil {
		return domain.Strategy{}, result, err
	}
	now := time.Now().UTC()
	id := fmt.Sprintf("investing-bulls-mtf-%s-%d", symbol, now.Unix())
	st := domain.Strategy{ID: id, Name: fmt.Sprintf("Investing Bulls MTF %s", symbol), Description: "1H dirección + 15m entrada + 5m confirmación opcional; CHOCH/BOS, Fibonacci, imbalance y order block.", Status: domain.StrategyCandidate, Source: "investing_bulls_learn_mtf", Spec: result.SpecJSON, CreatedAt: now, UpdatedAt: now}
	if err := store.UpsertStrategy(ctx, st); err != nil {
		return domain.Strategy{}, result, err
	}
	m := tradeMetrics(result.Trades, cfg.InitialBalance)
	bt := domain.BacktestResult{StrategyID: id, Trades: m.trades, WinRate: m.winRate, ProfitFactor: m.profitFactor, Sharpe: m.sharpe, Sortino: m.sortino, MaxDrawdown: m.maxDrawdown, TotalReturn: m.totalReturn, AverageWin: m.averageWin, AverageLoss: m.averageLoss, Expectancy: m.expectancy, GrossProfit: m.grossProfit, GrossLoss: m.grossLoss, FeesPaid: m.feesPaid, TestBars: len(entry), Passed: result.Accepted, Status: "in_sample_candidate", MetricsAt: now}
	if err := store.SaveBacktest(ctx, bt); err != nil {
		return domain.Strategy{}, result, err
	}
	observability.Log(observability.LearnCompleted, "mode", "mtf", "strategy", id, "symbol", symbol, "trades", m.trades, "profit_factor", m.profitFactor, "return_pct", m.totalReturn*100, "accepted", result.Accepted)
	return st, result, nil
}

func ValidateMultiTimeframeCandidate(ctx context.Context, store LearnerStore, strategyID, symbol string, limit int, wcfg WalkForwardConfig) (domain.BacktestResult, error) {
	observability.Log(observability.OOSStarted, "mode", "mtf_walk_forward", "strategy", strategyID, "symbol", symbol)
	st, err := store.GetStrategy(ctx, strategyID)
	if err != nil {
		return domain.BacktestResult{}, err
	}
	var model MultiTimeframeModel
	if err = json.Unmarshal([]byte(st.Spec), &model); err != nil {
		return domain.BacktestResult{}, fmt.Errorf("mtf spec: %w", err)
	}
	if model.Symbol != "" && model.Symbol != symbol {
		return domain.BacktestResult{}, fmt.Errorf("mtf symbol no coincide")
	}
	if limit <= 0 {
		limit = 5000
	}

	main, err := store.RecentCandles(ctx, symbol, model.Timeframes.MainTimeframe, limit)
	if err != nil {
		return domain.BacktestResult{}, err
	}
	entry, err := store.RecentCandles(ctx, symbol, model.Timeframes.EntryTimeframe, limit)
	if err != nil {
		return domain.BacktestResult{}, err
	}
	var confirm []domain.Kline
	if model.Timeframes.RequireConfirm {
		confirm, err = store.RecentCandles(ctx, symbol, model.Timeframes.ConfirmTimeframe, limit)
		if err != nil {
			return domain.BacktestResult{}, err
		}
	}
	main = closedCandles(main)
	entry = closedCandles(entry)
	confirm = closedCandles(confirm)

	if err := ValidateKlines(main, model.Symbol, model.Timeframes.MainTimeframe); err != nil {
		return domain.BacktestResult{}, fmt.Errorf("mtf oos 1H: %w", err)
	}
	if err := ValidateKlines(entry, model.Symbol, model.Timeframes.EntryTimeframe); err != nil {
		return domain.BacktestResult{}, fmt.Errorf("mtf oos 15m: %w", err)
	}
	if model.Timeframes.RequireConfirm && len(confirm) > 0 {
		if err := ValidateKlines(confirm, model.Symbol, model.Timeframes.ConfirmTimeframe); err != nil {
			return domain.BacktestResult{}, fmt.Errorf("mtf oos 5m: %w", err)
		}
	}

	wcfg = normalizeWalkForwardConfig(wcfg)
	baseCfg := cfgFromModel(model.Base)
	baseCfg.Timeframe = model.Timeframes.EntryTimeframe
	baseCfg.Search = model.Search.normalized()
	if baseCfg.Search.Size() == 0 {
		baseCfg.Search = DefaultSearchSpace()
	}
	allowed := model.AllowedSetups
	if len(allowed) == 0 {
		allowed = allMTFSetups()
	}

	var all []LearnedTrade
	var folds []domain.OOSFold
	positive := 0
	lastOOS := 0

	for n := 0; n < wcfg.Folds; n++ {
		trainEnd := int(math.Floor(float64(len(entry)) * (wcfg.TrainPct + float64(n)*wcfg.StepPct)))
		oosEnd := int(math.Floor(float64(len(entry)) * (wcfg.TrainPct + float64(n)*wcfg.StepPct + wcfg.OOSPct)))
		if trainEnd < 100 || oosEnd > len(entry) || oosEnd <= trainEnd {
			return domain.BacktestResult{}, fmt.Errorf("walk-forward MTF: fold %d inválido: train=%d oos=%d", n+1, trainEnd, oosEnd)
		}

		// ENTRENAMIENTO: solo datos anteriores al OOS. El timeframe mayor se
		// corta estrictamente antes del inicio de la última vela 15m de train.
		trainEntry := entry[:trainEnd]
		trainCut := trainEntry[len(trainEntry)-1].Start
		trainMain := closedCandlesBefore(main, trainCut)
		trainConfirm := closedCandlesBefore(confirm, trainCut)
		if len(trainMain) < 100 {
			folds = append(folds, domain.OOSFold{Bars: oosEnd - trainEnd})
			lastOOS = oosEnd
			observability.Log(observability.OOSCompleted, "mode", "mtf_walk_forward_fold", "strategy", strategyID, "symbol", symbol, "fold", n+1, "train_bars", trainEnd, "oos_bars", oosEnd-trainEnd, "trades", 0, "win_rate_pct", 0, "profit_factor", 0, "return_pct", 0, "max_drawdown_pct", 0, "positive", false, "reason", "train_main_insufficient")
			continue
		}

		trainCfg := baseCfg
		learned, err := learnMultiTimeframeWindow(trainMain, trainEntry, trainConfirm, trainCfg, model.Timeframes, 0, model.ConfirmSkipReason)
		if err != nil {
			return domain.BacktestResult{}, fmt.Errorf("walk-forward MTF fold %d: %w", n+1, err)
		}
		if learned.SpecJSON == "" || learned.Model.Base.Trades < trainCfg.MinTrades {
			// Un fold sin candidata es un fold fallido, no una razón para relajar
			// MinTrades ni para reciclar la configuración del histórico completo.
			folds = append(folds, domain.OOSFold{Bars: oosEnd - trainEnd})
			lastOOS = oosEnd
			observability.Log(observability.OOSCompleted, "mode", "mtf_walk_forward_fold", "strategy", strategyID, "symbol", symbol, "fold", n+1, "train_bars", trainEnd, "oos_bars", oosEnd-trainEnd, "trades", 0, "win_rate_pct", 0, "profit_factor", 0, "return_pct", 0, "max_drawdown_pct", 0, "positive", false, "reason", "no_train_candidate")
			continue
		}

		// OOS: se congela exclusivamente la configuración elegida en train.
		// Las velas OOS sirven para resolver resultados, nunca para escoger
		// parámetros.
		prefixMain := closedCandlesBefore(main, entry[oosEnd-1].Start)
		prefixConfirm := closedCandlesBefore(confirm, entry[oosEnd-1].Start)
		points := collectMTFDecisionPoints(prefixMain, entry[:oosEnd], prefixConfirm, trainCfg, model.Timeframes, learned.Model.AllowedSetups, trainEnd, nil)
		fixedCfg := cfgFromModel(learned.Model.Base)
		trades := simulateMTFDecisionPoints(entry[:oosEnd], points, fixedCfg, trainEnd)

		m := tradeMetrics(trades, fixedCfg.InitialBalance)
		fold := domain.OOSFold{
			Trades: m.trades, WinRate: m.winRate, ProfitFactor: m.profitFactor,
			Sharpe: m.sharpe, Sortino: m.sortino, MaxDrawdown: m.maxDrawdown,
			TotalReturn: m.totalReturn, Bars: oosEnd - trainEnd,
		}
		folds = append(folds, fold)
		all = append(all, trades...)
		lastOOS = oosEnd

		foldPositive := m.trades >= wcfg.MinOOSTrades && m.profitFactor >= wcfg.MinOOSProfitFactor && m.maxDrawdown <= wcfg.MaxOOSDrawdown
		if foldPositive {
			positive++
		}
		observability.Log(observability.OOSCompleted, "mode", "mtf_walk_forward_fold", "strategy", strategyID, "symbol", symbol, "fold", n+1, "train_bars", trainEnd, "oos_bars", oosEnd-trainEnd, "trades", m.trades, "win_rate_pct", m.winRate*100, "profit_factor", m.profitFactor, "return_pct", m.totalReturn*100, "max_drawdown_pct", m.maxDrawdown*100, "positive", foldPositive)
	}

	if len(folds) == 0 {
		return domain.BacktestResult{}, fmt.Errorf("walk-forward MTF: no hubo folds evaluables")
	}

	m := tradeMetrics(all, baseCfg.InitialBalance)
	// La validación OOS histórica es diagnóstica en esta fase. El criterio
	// definitivo del candidato se toma sobre 100 operaciones PAPER reales del
	// propio motor. Por eso una muestra OOS pequeña (incluso 0-2 trades) no
	// puede rechazar ni impedir la ejecución del candidato.
	historicalPass := len(folds) == wcfg.Folds &&
		positive >= wcfg.MinPositiveFolds &&
		m.trades >= wcfg.MinOOSTrades &&
		m.profitFactor >= wcfg.MinOOSProfitFactor &&
		m.maxDrawdown <= wcfg.MaxOOSDrawdown
	passed := true

	now := time.Now().UTC()
	bt := domain.BacktestResult{
		StrategyID: strategyID, Trades: m.trades, WinRate: m.winRate,
		ProfitFactor: m.profitFactor, Sharpe: m.sharpe, Sortino: m.sortino,
		MaxDrawdown: m.maxDrawdown, TotalReturn: m.totalReturn,
		AverageWin: m.averageWin, AverageLoss: m.averageLoss, Expectancy: m.expectancy,
		GrossProfit: m.grossProfit, GrossLoss: m.grossLoss, FeesPaid: m.feesPaid,
		TestBars: lastOOS - int(math.Floor(float64(len(entry))*wcfg.TrainPct)),
		Passed: passed, Folds: len(folds), OOSFolds: folds,
		Status: "paper_observation_100_trades", MetricsAt: now,
	}
	if err := store.SaveBacktest(ctx, bt); err != nil {
		return domain.BacktestResult{}, err
	}

	st.UpdatedAt = now
	st.Status = domain.StrategyActive
	st.Error = fmt.Sprintf("PAPER en observación: %d operaciones cerradas requeridas; OOS histórico %s (%d/%d folds positivos)", 100, map[bool]string{true:"superado", false:"no superado"}[historicalPass], positive, len(folds))
	if err := store.UpsertStrategy(ctx, st); err != nil {
		return domain.BacktestResult{}, err
	}

	observability.Log(observability.StrategyActivated, "mode", "mtf_walk_forward_paper_observation", "strategy", strategyID, "symbol", symbol, "folds", len(folds), "positive", positive, "profit_factor", m.profitFactor, "return_pct", m.totalReturn*100, "historical_pass", historicalPass)
	observability.Log(observability.OOSCompleted, "mode", "mtf_walk_forward", "strategy", strategyID, "symbol", symbol, "passed", passed, "folds", len(folds), "positive", positive)
	return bt, nil
}
func confirmThrough(ks []domain.Kline, ts time.Time) []domain.Kline { return candlesThrough(ks, ts) }

// cfgFromModel reconstruye LearnConfig exactamente desde el modelo persistido.
// Los valores por defecto solo se aplican a modelos legacy (campos ausentes).
func cfgFromModel(m LearnedModel) LearnConfig {
	cfg := LearnConfig{Symbol: m.Symbol, Timeframe: m.Timeframe, SwingLeft: m.SwingLeft, SwingRight: m.SwingRight, Fib: m.Fib, Confluence: m.Confluence, TradePlan: m.TradePlan, InitialBalance: m.InitialBalance, FeePct: m.FeePct, SlippagePct: m.SlippagePct, MinTrades: m.MinTrades}
	if cfg.InitialBalance <= 0 {
		cfg.InitialBalance = 10000
	}
	if cfg.FeePct <= 0 {
		cfg.FeePct = 0.001
	}
	if cfg.SlippagePct <= 0 {
		cfg.SlippagePct = 0.0002
	}
	if cfg.MinTrades <= 0 {
		cfg.MinTrades = 8
	}
	return cfg
}

// datasetDigestMultiTf combina los históricos 1H/15m/5m en un único hash
// que identifica exactamente el dataset usado para aprender (sec 16).
func datasetDigestMultiTf(arrays ...[]domain.Kline) string {
	h := sha256.New()
	for _, ks := range arrays {
		h.Write([]byte{0})
		h.Write([]byte(datasetDigest(ks)))
	}
	return hex.EncodeToString(h.Sum(nil))
}
