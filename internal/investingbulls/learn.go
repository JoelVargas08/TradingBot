package investingbulls

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/observability"
)

// CodeVersion identifica la versión del código que produjo un modelo.
// Se persiste junto al modelo para reproducibilidad (sec 18).
const CodeVersion = "investing-bulls/1.2.0"

// minLearningBars es la primera vela evaluable: por debajo no hay estructura
// confirmada (los swings exigen barras a izquierda y derecha).
const minLearningBars = 20

type LearnConfig struct {
	Symbol         string
	Timeframe      string
	SwingLeft      int
	SwingRight     int
	Fib            FibConfig
	Confluence     ConfluenceConfig
	TradePlan      TradePlanConfig
	Search         SearchSpace
	InitialBalance float64
	FeePct         float64
	SlippagePct    float64
	MinTrades      int
}

func DefaultLearnConfig() LearnConfig {
	return LearnConfig{
		SwingLeft: 2, SwingRight: 2,
		Fib:            DefaultFibConfig(),
		Confluence:     DefaultConfluenceConfig(),
		TradePlan:      DefaultTradePlanConfig(),
		Search:         DefaultSearchSpace(),
		InitialBalance: 10000, FeePct: 0.001, SlippagePct: 0.0002,
		MinTrades: 8,
	}
}

// SearchSpace acota el espacio de búsqueda del learner. El valor por defecto
// recorre la confluencia de tres factores y sus tres variantes 2/3, tolerancias
// de zona del 0.5% al 2% y stops del 1% al 2% (tope del perfil Investing Bulls).
// Las reglas de la estrategia NO se eliminan: lo que se abre es el grado de
// confluencia y la tolerancia geométrica, ambas parametrizadas y auditables.
type SearchSpace struct {
	Modes     []ConfluenceMode
	Distances []float64
	Stops     []float64
}

func DefaultSearchSpace() SearchSpace {
	return SearchSpace{
		Modes:     SearchConfluenceModes(),
		Distances: []float64{0.005, 0.010, 0.015, MaxConfluenceDistanceLimit},
		Stops:     []float64{0.010, 0.015, MaxStopPctLimit},
	}
}

// Size es el número de configuraciones del espacio tal y como se recibe.
func (s SearchSpace) Size() int {
	return len(s.Modes) * len(s.Distances) * len(s.Stops)
}

// normalized deduplica y descarta valores fuera de los límites del perfil,
// de modo que una configuración externa nunca amplíe el espacio sin control.
func (s SearchSpace) normalized() SearchSpace {
	def := DefaultSearchSpace()
	out := SearchSpace{}
	for _, m := range s.Modes {
		if m.Valid() && !containsMode(out.Modes, m) {
			out.Modes = append(out.Modes, m)
		}
	}
	for _, d := range s.Distances {
		if d > 0 && d <= MaxConfluenceDistanceLimit && !containsFloat(out.Distances, d) {
			out.Distances = append(out.Distances, d)
		}
	}
	for _, st := range s.Stops {
		if st > 0 && st <= MaxStopPctLimit && !containsFloat(out.Stops, st) {
			out.Stops = append(out.Stops, st)
		}
	}
	if len(out.Modes) == 0 {
		out.Modes = def.Modes
	}
	if len(out.Distances) == 0 {
		out.Distances = def.Distances
	}
	if len(out.Stops) == 0 {
		out.Stops = def.Stops
	}
	return out
}

// Configs expande el espacio en configuraciones concretas. El orden es
// determinista y, ante empate de puntuación, favorece la variante más fiel a la
// estrategia fuente: primero 3/3, después distancias y stops más ajustados.
func (s SearchSpace) Configs(base LearnConfig) []LearnConfig {
	space := s.normalized()
	out := make([]LearnConfig, 0, space.Size())
	for _, mode := range space.Modes {
		for _, distance := range space.Distances {
			for _, stop := range space.Stops {
				cfg := base
				cfg.Search = SearchSpace{Modes: []ConfluenceMode{mode}, Distances: []float64{distance}, Stops: []float64{stop}}
				cfg.Confluence.Mode = mode
				cfg.Confluence.MaxZoneDistancePct = distance
				cfg.TradePlan.MaxStopPct = stop
				out = append(out, cfg)
			}
		}
	}
	return out
}

func containsMode(list []ConfluenceMode, m ConfluenceMode) bool {
	for _, v := range list {
		if v == m {
			return true
		}
	}
	return false
}

func containsFloat(list []float64, v float64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

type LearnedModel struct {
	Version    int              `json:"version"`
	Family     string           `json:"family"`
	Symbol     string           `json:"symbol"`
	Timeframe  string           `json:"timeframe"`
	SwingLeft  int              `json:"swing_left"`
	SwingRight int              `json:"swing_right"`
	Fib        FibConfig        `json:"fib"`
	Confluence ConfluenceConfig `json:"confluence"`
	// ConfluenceComponents registra qué componentes confluyeron en la candidata
	// (por ejemplo "fibonacci,order_block"): sin esto el modelo no explica por
	// qué fue elegida esa configuración.
	ConfluenceComponents string          `json:"confluence_components"`
	TradePlan            TradePlanConfig `json:"trade_plan"`
	InitialBalance       float64         `json:"initial_balance"`
	FeePct               float64         `json:"fee_pct"`
	SlippagePct          float64         `json:"slippage_pct"`
	MinTrades            int             `json:"min_trades"`
	ConfigsEvaluated     int             `json:"configs_evaluated"`
	FinalBalance         float64         `json:"final_balance"`
	Trades               int             `json:"trades"`
	WinRate              float64         `json:"win_rate"`
	ProfitFactor         float64         `json:"profit_factor"`
	// ZeroLosses explica un ProfitFactor igual al tope registrado: la
	// configuración candidata no cerró ninguna operación perdedora.
	ZeroLosses   bool      `json:"zero_losses"`
	Sharpe       float64   `json:"sharpe"`
	Sortino      float64   `json:"sortino"`
	TotalReturn  float64   `json:"total_return"`
	MaxDrawdown  float64   `json:"max_drawdown"`
	AverageWin   float64   `json:"average_win"`
	AverageLoss  float64   `json:"average_loss"`
	Expectancy   float64   `json:"expectancy"`
	FeesPaid     float64   `json:"fees_paid"`
	DatasetHash  string    `json:"dataset_hash"`
	DatasetStart time.Time `json:"dataset_start"`
	DatasetEnd   time.Time `json:"dataset_end"`
	DatasetBars  int       `json:"dataset_bars"`
	CodeVersion  string    `json:"code_version"`
	LearnedAt    time.Time `json:"learned_at"`
}

// LearnDiagnostics explica la búsqueda: cuántas velas y setups entraron, cuántas
// configuraciones se evaluaron y cuál fue la mejor. Sin esto, un fallo del
// comando /learn* solo informaba de que no hubo candidata.
type LearnDiagnostics struct {
	Candles                 int
	MinTrades               int
	TrendBars               int
	SetupsDetected          int
	SetupsWithFibonacci     int
	SetupsWithOrderBlock    int
	SetupsWithImbalance     int
	ConfigsEvaluated        int
	ConfigsWithEnoughTrades int
	BestTrades              int
	BestProfitFactor        float64
	BestTotalReturn         float64
	BestMaxDrawdown         float64
	// Selected indica que Best* describe la configuración elegida y no el
	// mejor intento visto durante la búsqueda.
	Selected           bool
	BestConfluenceMode string
	BestComponents     string
	BestDistancePct    float64
	BestStopPct        float64
}

// Summary es el informe legible que se devuelve cuando no hay candidata.
func (d LearnDiagnostics) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Learn base:\n")
	fmt.Fprintf(&b, "  velas evaluadas: %d\n", d.Candles)
	fmt.Fprintf(&b, "  velas con tendencia definida: %d\n", d.TrendBars)
	fmt.Fprintf(&b, "  setups detectados: %d\n", d.SetupsDetected)
	fmt.Fprintf(&b, "  setups con Fibonacci: %d\n", d.SetupsWithFibonacci)
	fmt.Fprintf(&b, "  setups con Order Block: %d\n", d.SetupsWithOrderBlock)
	fmt.Fprintf(&b, "  setups con Imbalance: %d\n", d.SetupsWithImbalance)
	fmt.Fprintf(&b, "  configuraciones evaluadas: %d\n", d.ConfigsEvaluated)
	fmt.Fprintf(&b, "  configuraciones con suficientes trades (>= %d): %d\n", d.MinTrades, d.ConfigsWithEnoughTrades)
	label := "Mejor configuración:"
	if d.Selected {
		label = "Configuración seleccionada:"
	}
	fmt.Fprintf(&b, "  mejor número de trades: %d\n", d.BestTrades)
	fmt.Fprintf(&b, "  mejor PF: %s\n", formatMetric(d.BestProfitFactor))
	fmt.Fprintf(&b, "  mejor return: %s\n", formatPct(d.BestTotalReturn))
	fmt.Fprintf(&b, "  mejor DD: %s\n", formatPct(d.BestMaxDrawdown))
	if d.BestTrades > 0 {
		fmt.Fprintf(&b, "%s\n", label)
		fmt.Fprintf(&b, "  confluence_mode=%s\n", d.BestConfluenceMode)
		fmt.Fprintf(&b, "  components=%s\n", d.BestComponents)
		fmt.Fprintf(&b, "  distance=%s\n", formatPct(d.BestDistancePct))
		fmt.Fprintf(&b, "  stop=%s\n", formatPct(d.BestStopPct))
	}
	return b.String()
}

func formatMetric(v float64) string {
	if math.IsInf(v, 1) {
		return "inf"
	}
	if math.IsNaN(v) || math.IsInf(v, -1) {
		return "n/d"
	}
	return fmt.Sprintf("%.2f", v)
}

func formatPct(v float64) string {
	return fmt.Sprintf("%.2f%%", v*100)
}

type LearnResult struct {
	Model        LearnedModel
	FinalBalance float64
	Trades       []LearnedTrade
	Accepted     bool
	Reason       string
	SpecJSON     string
	Diagnostics  LearnDiagnostics
}

type LearnedTrade struct {
	Direction  domain.Direction
	EntryBar   int
	EntryPrice float64
	ExitBar    int
	ExitPrice  float64
	PnL        float64
	FeePct     float64
	Reason     string
	Setup      SetupType
	// Components registra qué evidencia de confluencia produjo la entrada; sin
	// ella una candidata 2/3 es indistinguible de una 3/3 en el histórico.
	Components string
}

// Learn performs a deterministic parameter search over the source strategy.
// It is parameter optimization/backtesting, not a machine-learning model.
// Each decision at bar i uses only candles [0..i]; later candles are only used
// to resolve the historical outcome (TP/SL/END) of an already taken decision.
func Learn(ks []domain.Kline, cfg LearnConfig) (LearnResult, error) {
	cfg = normalizeLearnConfig(cfg)
	if len(ks) < 100 {
		return LearnResult{}, fmt.Errorf("learn: se necesitan al menos 100 velas, hay %d", len(ks))
	}
	if err := ValidateKlines(ks, cfg.Symbol, cfg.Timeframe); err != nil {
		return LearnResult{}, fmt.Errorf("learn: datos inválidos: %w", err)
	}
	digest := datasetDigest(ks)

	diag := LearnDiagnostics{Candles: len(ks), MinTrades: cfg.MinTrades}
	// La estructura, el Fibonacci, los imbalances y los order blocks NO dependen
	// del modo, la distancia ni el stop: se miden una sola vez y cada
	// configuración reinterpreta los mismos puntos de decisión. Sin esta
	// separación, recorrer toda la grid exigiría un pass completo por cada
	// configuración sobre el histórico.
	decisions := collectDecisionPoints(ks, cfg, 0, &diag)

	space := cfg.Search.normalized()
	diag.ConfigsEvaluated = space.Size()

	bestScore := math.Inf(-1)
	var best LearnedModel
	var bestTrades []LearnedTrade
	var bestBalance float64
	var bestCfg LearnConfig
	var bestMetrics learnMetrics

	for _, testCfg := range space.Configs(cfg) {
		trades := simulateDecisions(ks, decisions, testCfg)
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
			bestBalance = metrics.finalBalance
			bestCfg = testCfg
			bestMetrics = metrics
			best = newLearnedModel(cfg, testCfg, digest, ks, metrics)
		}
	}

	// Con candidata, el informe describe la configuración ELEGIDA (no la que más
	// operaciones tuvo): es la que el usuario debe inspeccionar.
	if bestScore != math.Inf(-1) {
		recordSelectedConfig(&diag, bestCfg, bestMetrics)
	}

	observability.Log(observability.LearnCompleted,
		"scope", "learn_base",
		"symbol", cfg.Symbol,
		"timeframe", cfg.Timeframe,
		"candles", diag.Candles,
		"setups", diag.SetupsDetected,
		"configs", diag.ConfigsEvaluated,
		"configs_ok", diag.ConfigsWithEnoughTrades,
		"best_trades", diag.BestTrades,
	)

	if bestScore == math.Inf(-1) {
		return LearnResult{
			Accepted:    false,
			Reason:      noCandidateReason(cfg, diag),
			Diagnostics: diag,
		}, nil
	}

	raw, err := json.MarshalIndent(best, "", "  ")
	if err != nil {
		return LearnResult{}, fmt.Errorf("learn: serializando modelo: %w", err)
	}

	accepted := best.Trades >= cfg.MinTrades && best.ProfitFactor > 1
	reason := "candidato generado; requiere validación OOS antes de activarse"
	if !accepted {
		reason = "candidato generado pero no supera el filtro de investigación"
	}

	return LearnResult{
		Model: best, FinalBalance: bestBalance, Trades: bestTrades,
		Accepted: accepted, Reason: reason, SpecJSON: string(raw), Diagnostics: diag,
	}, nil
}

// noCandidateReason explica por qué la búsqueda no produjo candidata.
func noCandidateReason(cfg LearnConfig, diag LearnDiagnostics) string {
	var b strings.Builder
	if diag.SetupsDetected == 0 {
		b.WriteString("el learner no detectó ningún setup con confluencia válida (MinTrades se mantiene: ")
		b.WriteString(fmt.Sprintf("%d)\n", cfg.MinTrades))
	} else if diag.ConfigsWithEnoughTrades == 0 {
		b.WriteString(fmt.Sprintf("ninguna configuración alcanzó el mínimo de operaciones (%d)\n", cfg.MinTrades))
	} else {
		b.WriteString("la mejor configuración no supera el filtro de investigación\n")
	}
	b.WriteString(diag.Summary())
	return b.String()
}

func newLearnedModel(cfg, testCfg LearnConfig, digest string, ks []domain.Kline, metrics learnMetrics) LearnedModel {
	return LearnedModel{
		Version: 1, Family: "investing_bulls",
		Symbol: cfg.Symbol, Timeframe: cfg.Timeframe,
		SwingLeft: cfg.SwingLeft, SwingRight: cfg.SwingRight,
		Fib:                  cfg.Fib,
		Confluence:           testCfg.Confluence,
		ConfluenceComponents: metrics.components,
		TradePlan:            testCfg.TradePlan,
		InitialBalance:       cfg.InitialBalance, FeePct: cfg.FeePct,
		SlippagePct: cfg.SlippagePct, MinTrades: cfg.MinTrades,
		ConfigsEvaluated: cfg.Search.Size(),
		FinalBalance:     metrics.finalBalance,
		Trades:           metrics.trades, WinRate: metrics.winRate,
		ProfitFactor: recordedProfitFactor(metrics.profitFactor),
		ZeroLosses:   math.IsInf(metrics.profitFactor, 1),
		Sharpe:       recordedMetric(metrics.sharpe),
		Sortino:      recordedMetric(metrics.sortino),
		TotalReturn:  recordedMetric(metrics.totalReturn),
		MaxDrawdown:  recordedMetric(metrics.maxDrawdown),
		AverageWin:   recordedMetric(metrics.averageWin),
		AverageLoss:  recordedMetric(metrics.averageLoss),
		Expectancy:   recordedMetric(metrics.expectancy),
		FeesPaid:     metrics.feesPaid,
		DatasetHash:  digest,
		DatasetStart: ks[0].Start, DatasetEnd: ks[len(ks)-1].Start,
		DatasetBars: len(ks), CodeVersion: CodeVersion,
		LearnedAt: time.Now().UTC(),
	}
}

// MaxProfitFactorRecorded acota el profit factor persistible: por encima de 99
// la métrica deja de ser informativa y un profit factor infinito (ninguna
// operación perdedora) no es serializable en JSON, lo que hacía fallar el
// comando Learn con un candidato perfectamente válido.
const MaxProfitFactorRecorded = 99

func recordedProfitFactor(pf float64) float64 {
	if math.IsNaN(pf) || pf < 0 {
		return 0
	}
	if math.IsInf(pf, 1) || pf > MaxProfitFactorRecorded {
		return MaxProfitFactorRecorded
	}
	return pf
}

func recordedMetric(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// recordBestAttempt conserva la mejor configuración vista, incluso si no alcanza
// MinTrades: es lo que permite diagnosticar un fallo de la búsqueda.
func recordBestAttempt(diag *LearnDiagnostics, cfg LearnConfig, metrics learnMetrics) {
	better := metrics.trades > diag.BestTrades
	if !better && metrics.trades == diag.BestTrades && metrics.trades > 0 {
		better = betterProfitFactor(metrics.profitFactor, diag.BestProfitFactor)
	}
	if !better {
		return
	}
	recordConfig(diag, cfg, metrics)
}

// recordSelectedConfig describe la configuración ELEGIDA, que no siempre es la que
// más operaciones produjo: el criterio de selección es retorno ajustado por
// drawdown, no el número de operaciones.
func recordSelectedConfig(diag *LearnDiagnostics, cfg LearnConfig, metrics learnMetrics) {
	diag.Selected = true
	recordConfig(diag, cfg, metrics)
}

func recordConfig(diag *LearnDiagnostics, cfg LearnConfig, metrics learnMetrics) {
	diag.BestTrades = metrics.trades
	diag.BestProfitFactor = metrics.profitFactor
	diag.BestTotalReturn = metrics.totalReturn
	diag.BestMaxDrawdown = metrics.maxDrawdown
	diag.BestConfluenceMode = string(cfg.Confluence.Mode)
	diag.BestComponents = metrics.components
	diag.BestDistancePct = cfg.Confluence.MaxZoneDistancePct
	diag.BestStopPct = cfg.TradePlan.MaxStopPct
}

func betterProfitFactor(a, b float64) bool {
	if math.IsInf(a, 1) {
		return !math.IsInf(b, 1)
	}
	return a > b
}

func normalizeLearnConfig(cfg LearnConfig) LearnConfig {
	if cfg.SwingLeft <= 0 {
		cfg.SwingLeft = 2
	}
	if cfg.SwingRight <= 0 {
		cfg.SwingRight = 2
	}
	if cfg.Fib.Target1 <= 1 {
		cfg.Fib = DefaultFibConfig()
	}
	cfg.Confluence = cfg.Confluence.Normalize()
	cfg.TradePlan = cfg.TradePlan.Normalize()
	cfg.Search = cfg.Search.normalized()
	if cfg.InitialBalance <= 0 {
		cfg.InitialBalance = 10000
	}
	if cfg.FeePct < 0 {
		cfg.FeePct = 0
	}
	if cfg.SlippagePct < 0 {
		cfg.SlippagePct = 0
	}
	if cfg.MinTrades <= 0 {
		cfg.MinTrades = 8
	}
	return cfg
}

// decisionPoint es una oportunidad de entrada evaluada SIN lookahead sobre el
// prefijo cerrado hasta su vela. Guarda los niveles ya resueltos (target
// estructural, stop del order block, anclas de Fibonacci) para que cada
// configuración del espacio de búsqueda solo tenga que aplicar su stop y su
// distancia, sin volver a analizar el histórico.
type decisionPoint struct {
	index     int
	entryBar  int
	entry     float64
	direction domain.Direction
	setupType SetupType
	evidence  ConfluenceEvidence
	levels    planInputs
}

// collectDecisionPoints recorre las velas midiendo la confluencia disponible en
// cada una. evaluationStart filtra la primera vela de decisión (warm-forward),
// pero el prefijo de indicadores siempre empieza en cero.
func collectDecisionPoints(ks []domain.Kline, cfg LearnConfig, evaluationStart int, diag *LearnDiagnostics) []decisionPoint {
	if evaluationStart < minLearningBars {
		evaluationStart = minLearningBars
	}
	imbalanceCfg := DefaultImbalanceConfig()
	orderBlockCfg := DefaultOrderBlockConfig()

	var out []decisionPoint
	for i := evaluationStart; i < len(ks)-1; i++ {
		prefix := ks[:i+1]
		structure := Analyze(prefix, cfg.SwingLeft, cfg.SwingRight)
		if structure.Trend != TrendBullish && structure.Trend != TrendBearish {
			continue
		}
		if diag != nil {
			diag.TrendBars++
		}

		fib, ok := fibonacciFromLatestImpulse(structure, cfg.Fib)
		if !ok {
			continue
		}

		imbs := UpdateImbalances(DetectImbalances(prefix, imbalanceCfg), prefix, imbalanceCfg)
		blocks := UpdateOrderBlocks(DetectOrderBlocks(prefix, structure.Breaks, orderBlockCfg), prefix, orderBlockCfg)
		evidence, ok := CollectConfluenceEvidence(prefix, i, structure, fib, imbs, blocks)
		if !ok {
			continue
		}

		entryBar := i + 1
		entry := ks[entryBar].Open
		if entry <= 0 {
			entry = ks[entryBar].Close
		}
		if entry <= 0 {
			continue
		}

		// Solo se guarda la vela si hay al menos una evidencia de confluencia:
		// sin Fibonacci, imbalance ni order block ningún modo puede aceptarla.
		if evidence.FibZone == 0 && evidence.ImbalanceIndex < 0 && evidence.OrderBlockIndex < 0 {
			continue
		}

		// Los contadores se refieren al punto de decisión guardado, no a toda
		// vela con tendencia: si no, el diagnóstico insinuaría una densidad de
		// setups que el learner nunca puede usar.
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

		out = append(out, decisionPoint{
			index:     i,
			entryBar:  entryBar,
			entry:     entry,
			direction: evidence.Direction,
			setupType: inferSetupType(structure, i, evidence.Direction),
			evidence:  evidence,
			levels: resolvePlanInputs(Setup{
				Index:           i,
				Direction:       evidence.Direction,
				OrderBlockIndex: evidence.OrderBlockIndex,
			}, fib, entry, structure, blocks),
		})
	}
	return out
}

// simulateDecisions reproduce la operativa barra a barra con los puntos de
// decisión precalculados: una sola posición abierta a la vez y resolución del
// resultado con las velas posteriores (TP/SL/END).
func simulateDecisions(ks []domain.Kline, decisions []decisionPoint, cfg LearnConfig) []LearnedTrade {
	if len(decisions) == 0 || len(ks) < 2 {
		return nil
	}
	byBar := make(map[int]int, len(decisions))
	for i := range decisions {
		byBar[decisions[i].index] = i
	}

	var out []LearnedTrade
	inTrade := false
	var open LearnedTrade
	stop, target := 0.0, 0.0

	start := decisions[0].index
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
			Direction:  d.direction,
			EntryBar:   d.entryBar,
			EntryPrice: d.entry * entryMultiplier(d.direction, cfg.SlippagePct),
			FeePct:     cfg.FeePct,
			Setup:      d.setupType,
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

func generateAndSimulate(ks []domain.Kline, cfg LearnConfig) []LearnedTrade {
	return generateAndSimulateFrom(ks, cfg, 0)
}

func generateAndSimulateFrom(ks []domain.Kline, cfg LearnConfig, evaluationStart int) []LearnedTrade {
	return simulateDecisions(ks, collectDecisionPoints(ks, cfg, evaluationStart, nil), cfg)
}

func fibonacciFromLatestImpulse(s Structure, cfg FibConfig) (Fibonacci, bool) {
	if len(s.Swings) < 2 {
		return Fibonacci{}, false
	}
	for i := len(s.Swings) - 1; i > 0; i-- {
		origin, destination := s.Swings[i-1], s.Swings[i]
		if s.Trend == TrendBullish && !origin.High && destination.High {
			return NewFibonacciFromSwings(origin, destination, cfg)
		}
		if s.Trend == TrendBearish && origin.High && !destination.High {
			return NewFibonacciFromSwings(origin, destination, cfg)
		}
	}
	return Fibonacci{}, false
}

// buildLearningPlan construye el plan operativo de un setup a partir de la
// estructura disponible. LIVE y el simulador MTF la usan directamente; el
// learner usa planInputs, que ya viene resuelto.
func buildLearningPlan(setup Setup, fib Fibonacci, entry float64, structure Structure, blocks []OrderBlock, cfg TradePlanConfig) (TradePlan, bool) {
	return buildPlan(resolvePlanInputs(setup, fib, entry, structure, blocks), cfg)
}

// ValidateKlines valida la integridad del histórico antes de aprender (sec 13).
// Datos corruptos provocan error (rechazo), no aprendizaje silencioso.
func ValidateKlines(ks []domain.Kline, symbol, timeframe string) error {
	for i, k := range ks {
		if symbol != "" && k.Symbol != "" && k.Symbol != symbol {
			return fmt.Errorf("vela %d con symbol %q, esperado %q", i, k.Symbol, symbol)
		}
		if timeframe != "" && k.Timeframe != "" && k.Timeframe != timeframe {
			return fmt.Errorf("vela %d con timeframe %q, esperado %q", i, k.Timeframe, timeframe)
		}
		if !k.Closed {
			return fmt.Errorf("vela %d (%s) en formación: el aprendizaje usa solo velas cerradas", i, k.Start.Format(time.RFC3339))
		}
		if k.High < k.Low {
			return fmt.Errorf("vela %d con high < low (high=%v low=%v)", i, k.High, k.Low)
		}
		if k.Open < k.Low || k.Open > k.High {
			return fmt.Errorf("vela %d con open fuera de rango [low,high]: open=%v", i, k.Open)
		}
		if k.Close < k.Low || k.Close > k.High {
			return fmt.Errorf("vela %d con close fuera de rango [low,high]: close=%v", i, k.Close)
		}
		if k.Volume < 0 {
			return fmt.Errorf("vela %d con volume negativo: %v", i, k.Volume)
		}
		if i > 0 && !ks[i-1].Start.Before(k.Start) {
			return fmt.Errorf("timestamps desordenados o duplicados en la vela %d (%v)", i, k.Start.Format(time.RFC3339))
		}
	}
	return nil
}

// datasetDigest crea un hash determinista del histórico exacto utilizado,
// de modo que un modelo quede ligado a los datos con los que fue entrenado.
func datasetDigest(ks []domain.Kline) string {
	h := sha256.New()
	for _, k := range ks {
		fmt.Fprintf(h, "%d|%s|%s|%.12f|%.12f|%.12f|%.12f|%.12f|%v\n",
			k.Start.UnixNano(), k.Timeframe, k.Symbol, k.Open, k.High, k.Low, k.Close, k.Volume, k.Closed)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// entryMultiplier ajusta el precio de entrada por slippage UNA sola vez:
// los compradores pagan más caro, los vendedores reciben menos.
func entryMultiplier(direction domain.Direction, slippage float64) float64 {
	if direction == domain.DirectionBuy {
		return 1 + slippage
	}
	return 1 - slippage
}

// applyExitSlippage ajusta el precio de salida por slippage UNA sola vez:
// salir de un long es vender (menos), salir de un short es comprar (más).
func applyExitSlippage(exit float64, direction domain.Direction, slippage float64) float64 {
	if direction == domain.DirectionBuy {
		return exit * (1 - slippage)
	}
	return exit * (1 + slippage)
}

func tradePnL(direction domain.Direction, entry, exit float64) float64 {
	if entry <= 0 {
		return 0
	}
	if direction == domain.DirectionBuy {
		return (exit - entry) / entry
	}
	return (entry - exit) / entry
}

type learnMetrics struct {
	trades       int
	winRate      float64
	profitFactor float64
	sharpe       float64
	sortino      float64
	totalReturn  float64
	maxDrawdown  float64
	finalBalance float64
	averageWin   float64
	averageLoss  float64
	expectancy   float64
	grossProfit  float64
	grossLoss    float64
	feesPaid     float64
	// components registra la confluencia que produjo el mejor conjunto de
	// operaciones; se propaga al modelo para que la candidata sea auditable.
	components string
	wins       int
	losses     int
}

func tradeMetrics(trades []LearnedTrade, initial float64) learnMetrics {
	equity, peak := initial, initial
	maxDD := 0.0
	wins, losses := 0, 0
	var gains, lossSum float64
	pnls := make([]float64, 0, len(trades))
	feesPaid := 0.0
	for _, t := range trades {
		pnls = append(pnls, t.PnL)
		if t.FeePct > 0 {
			feesPaid += 2 * t.FeePct * equity
		}
		equity *= 1 + t.PnL
		if t.PnL > 0 {
			wins++
			gains += t.PnL
		} else {
			losses++
			lossSum -= t.PnL
		}
		if equity > peak {
			peak = equity
		}
		if dd := (peak - equity) / peak; dd > maxDD {
			maxDD = dd
		}
	}
	wr := 0.0
	if len(trades) > 0 {
		wr = float64(wins) / float64(len(trades))
	}
	pf := 0.0
	if lossSum > 0 {
		pf = gains / lossSum
	} else if gains > 0 {
		pf = math.Inf(1)
	}

	metrics := learnMetrics{
		trades: len(trades), winRate: wr, profitFactor: pf,
		totalReturn: equity/initial - 1, maxDrawdown: maxDD,
		finalBalance: equity, wins: wins, losses: losses,
		grossProfit: gains, grossLoss: lossSum, feesPaid: feesPaid,
	}
	if wins > 0 {
		metrics.averageWin = gains / float64(wins)
	}
	if losses > 0 {
		metrics.averageLoss = lossSum / float64(losses)
	}
	metrics.expectancy = wr*metrics.averageWin - (1-wr)*metrics.averageLoss
	metrics.sharpe, metrics.sortino = returnsStats(pnls)
	metrics.components = dominantComponents(trades)
	return metrics
}

// dominantComponents devuelve el conjunto de componentes de confluencia que
// produjo más operaciones. Ante empate gana el primero en orden de aparición:
// determinista y auditable.
func dominantComponents(trades []LearnedTrade) string {
	counts := map[string]int{}
	best, bestCount := "", 0
	for _, t := range trades {
		if t.Components == "" {
			continue
		}
		counts[t.Components]++
		if counts[t.Components] > bestCount {
			best, bestCount = t.Components, counts[t.Components]
		}
	}
	return best
}

// returnsStats calcula Sharpe y Sortino por operación (media / desviación,
// y media / desviación de las pérdidas respectivamente), sin anualizar.
func returnsStats(pnls []float64) (sharpe, sortino float64) {
	n := len(pnls)
	if n == 0 {
		return 0, 0
	}
	mean, sd := 0.0, 0.0
	for _, p := range pnls {
		mean += p
	}
	mean /= float64(n)
	for _, p := range pnls {
		d := p - mean
		sd += d * d
	}
	sd = math.Sqrt(sd / float64(n))
	if sd > 0 {
		sharpe = mean / sd
	}

	down, count := 0.0, 0
	for _, p := range pnls {
		if p < 0 {
			down += p * p
			count++
		}
	}
	if count > 0 {
		down = math.Sqrt(down / float64(count))
		if down > 0 {
			sortino = mean / down
		}
	}
	return sharpe, sortino
}

func inferSetupType(s Structure, index int, dir domain.Direction) SetupType {
	classified := ClassifySetups(s)
	for i := len(classified) - 1; i >= 0; i-- {
		if classified[i].Index <= index && classified[i].Direction == dir {
			return classified[i].SetupType
		}
	}
	if dir == domain.DirectionBuy {
		return SetupContinuationLong
	}
	return SetupContinuationShort
}
