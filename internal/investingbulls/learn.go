package investingbulls

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"tradingview-bot/internal/domain"
)

// CodeVersion identifica la versión del código que produjo un modelo.
// Se persiste junto al modelo para reproducibilidad (sec 16).
const CodeVersion = "investing-bulls/1.0.0"

type LearnConfig struct {
	Symbol string
	Timeframe string
	SwingLeft int
	SwingRight int
	Fib FibConfig
	Confluence ConfluenceConfig
	TradePlan TradePlanConfig
	InitialBalance float64
	FeePct float64
	SlippagePct float64
	MinTrades int
}

func DefaultLearnConfig() LearnConfig {
	return LearnConfig{
		SwingLeft: 2, SwingRight: 2,
		Fib: DefaultFibConfig(),
		Confluence: DefaultConfluenceConfig(),
		TradePlan: DefaultTradePlanConfig(),
		InitialBalance: 10000, FeePct: 0.001, SlippagePct: 0.0002,
		MinTrades: 8,
	}
}

type LearnedModel struct {
	Version int `json:"version"`
	Family string `json:"family"`
	Symbol string `json:"symbol"`
	Timeframe string `json:"timeframe"`
	SwingLeft int `json:"swing_left"`
	SwingRight int `json:"swing_right"`
	Fib FibConfig `json:"fib"`
	Confluence ConfluenceConfig `json:"confluence"`
	TradePlan TradePlanConfig `json:"trade_plan"`
	InitialBalance float64 `json:"initial_balance"`
	FeePct float64 `json:"fee_pct"`
	SlippagePct float64 `json:"slippage_pct"`
	MinTrades int `json:"min_trades"`
	FinalBalance float64 `json:"final_balance"`
	Trades int `json:"trades"`
	WinRate float64 `json:"win_rate"`
	ProfitFactor float64 `json:"profit_factor"`
	Sharpe float64 `json:"sharpe"`
	Sortino float64 `json:"sortino"`
	TotalReturn float64 `json:"total_return"`
	MaxDrawdown float64 `json:"max_drawdown"`
	AverageWin float64 `json:"average_win"`
	AverageLoss float64 `json:"average_loss"`
	Expectancy float64 `json:"expectancy"`
	FeesPaid float64 `json:"fees_paid"`
	DatasetHash string `json:"dataset_hash"`
	DatasetStart time.Time `json:"dataset_start"`
	DatasetEnd time.Time `json:"dataset_end"`
	DatasetBars int `json:"dataset_bars"`
	CodeVersion string `json:"code_version"`
	LearnedAt time.Time `json:"learned_at"`
}

type LearnResult struct {
	Model LearnedModel
	FinalBalance float64
	Trades []LearnedTrade
	Accepted bool
	Reason string
	SpecJSON string
}

type LearnedTrade struct {
	Direction domain.Direction
	EntryBar int
	EntryPrice float64
	ExitBar int
	ExitPrice float64
	PnL float64
	FeePct float64
	Reason string
	Setup SetupType
}

// Learn performs a deterministic parameter search over the source strategy.
// It is parameter optimization/backtesting, not a machine-learning model.
// Each decision at bar i uses only candles [0..i].
func Learn(ks []domain.Kline, cfg LearnConfig) (LearnResult, error) {
	cfg = normalizeLearnConfig(cfg)
	if len(ks) < 100 {
		return LearnResult{}, fmt.Errorf("learn: se necesitan al menos 100 velas, hay %d", len(ks))
	}
	if err := ValidateKlines(ks, cfg.Symbol, cfg.Timeframe); err != nil {
		return LearnResult{}, fmt.Errorf("learn: datos inválidos: %w", err)
	}
	digest := datasetDigest(ks)

	distances := []float64{cfg.Confluence.MaxZoneDistancePct * 0.5, cfg.Confluence.MaxZoneDistancePct, cfg.Confluence.MaxZoneDistancePct * 1.5}
	stops := []float64{0.015, cfg.TradePlan.MaxStopPct}
	bestScore := math.Inf(-1)
	var best LearnedModel
	var bestTrades []LearnedTrade
	var bestBalance float64

	for _, distance := range distances {
		if distance <= 0 { continue }
		for _, stop := range stops {
			if stop <= 0 { continue }
			testCfg := cfg
			testCfg.Confluence.MaxZoneDistancePct = distance
			testCfg.TradePlan.MaxStopPct = stop

			trades := generateAndSimulate(ks, testCfg)
			metrics := tradeMetrics(trades, cfg.InitialBalance)
			if metrics.trades < cfg.MinTrades { continue }

			score := metrics.totalReturn / (1 + metrics.maxDrawdown)
			if metrics.profitFactor > 0 {
				score *= math.Min(metrics.profitFactor, 5)
			}
			if score > bestScore {
				bestScore = score
				bestTrades = trades
				bestBalance = metrics.finalBalance
				best = LearnedModel{
					Version: 1, Family: "investing_bulls",
					Symbol: cfg.Symbol, Timeframe: cfg.Timeframe,
					SwingLeft: cfg.SwingLeft, SwingRight: cfg.SwingRight,
					Fib: cfg.Fib, Confluence: testCfg.Confluence,
					TradePlan: testCfg.TradePlan,
					InitialBalance: cfg.InitialBalance, FeePct: cfg.FeePct,
					SlippagePct: cfg.SlippagePct, MinTrades: cfg.MinTrades,
					FinalBalance: metrics.finalBalance,
					Trades: metrics.trades, WinRate: metrics.winRate,
					ProfitFactor: metrics.profitFactor,
					Sharpe: metrics.sharpe, Sortino: metrics.sortino,
					TotalReturn: metrics.totalReturn,
					MaxDrawdown: metrics.maxDrawdown,
					AverageWin: metrics.averageWin, AverageLoss: metrics.averageLoss,
					Expectancy: metrics.expectancy, FeesPaid: metrics.feesPaid,
					DatasetHash: digest,
					DatasetStart: ks[0].Start, DatasetEnd: ks[len(ks)-1].Start,
					DatasetBars: len(ks), CodeVersion: CodeVersion,
					LearnedAt: time.Now().UTC(),
				}
			}
		}
	}

	if bestScore == math.Inf(-1) {
		return LearnResult{Accepted: false, Reason: "ninguna configuración alcanzó el mínimo de operaciones"}, nil
	}

	raw, err := json.MarshalIndent(best, "", "  ")
	if err != nil { return LearnResult{}, fmt.Errorf("learn: serializando modelo: %w", err) }

	accepted := best.Trades >= cfg.MinTrades && best.ProfitFactor > 1
	reason := "candidato generado; requiere validación OOS antes de activarse"
	if !accepted { reason = "candidato generado pero no supera el filtro de investigación" }

	return LearnResult{
		Model: best, FinalBalance: bestBalance, Trades: bestTrades,
		Accepted: accepted, Reason: reason, SpecJSON: string(raw),
	}, nil
}

func normalizeLearnConfig(cfg LearnConfig) LearnConfig {
	if cfg.SwingLeft <= 0 { cfg.SwingLeft = 2 }
	if cfg.SwingRight <= 0 { cfg.SwingRight = 2 }
	if cfg.Fib.Target1 <= 1 { cfg.Fib = DefaultFibConfig() }
	if cfg.Confluence.MaxZoneDistancePct <= 0 { cfg.Confluence = DefaultConfluenceConfig() }
	if cfg.TradePlan.MaxStopPct <= 0 { cfg.TradePlan = DefaultTradePlanConfig() }
	if cfg.InitialBalance <= 0 { cfg.InitialBalance = 10000 }
	if cfg.FeePct < 0 { cfg.FeePct = 0 }
	if cfg.SlippagePct < 0 { cfg.SlippagePct = 0 }
	if cfg.MinTrades <= 0 { cfg.MinTrades = 8 }
	return cfg
}

func generateAndSimulate(ks []domain.Kline, cfg LearnConfig) []LearnedTrade {
	return generateAndSimulateFrom(ks, cfg, 0)
}

func generateAndSimulateFrom(ks []domain.Kline, cfg LearnConfig, evaluationStart int) []LearnedTrade {
	if evaluationStart < 0 {
		evaluationStart = 0
	}
	if evaluationStart >= len(ks) {
		return nil
	}
	var out []LearnedTrade
	inTrade := false
	var open LearnedTrade
	stop, target := 0.0, 0.0

	start := 20
	if evaluationStart > start { start = evaluationStart }
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
				if hitTarget && !hitStop { exit, reason = target, "take" }
				exit = applyExitSlippage(exit, open.Direction, cfg.SlippagePct)
				open.ExitBar, open.ExitPrice, open.Reason = i, exit, reason
				open.PnL = tradePnL(open.Direction, open.EntryPrice, exit) - 2*cfg.FeePct
				out = append(out, open)
				inTrade = false
			}
			continue
		}

		prefix := ks[:i+1]
		structure := Analyze(prefix, cfg.SwingLeft, cfg.SwingRight)
		if structure.Trend != TrendBullish && structure.Trend != TrendBearish { continue }

		fib, ok := fibonacciFromLatestImpulse(structure, cfg.Fib)
		if !ok { continue }

		icfg := DefaultImbalanceConfig()
		imbs := UpdateImbalances(DetectImbalances(prefix, icfg), prefix, icfg)
		bcfg := DefaultOrderBlockConfig()
		blocks := UpdateOrderBlocks(DetectOrderBlocks(prefix, structure.Breaks, bcfg), prefix, bcfg)

		setups := EvaluateConfluence(prefix, structure, fib, imbs, blocks, cfg.Confluence)
		if len(setups) == 0 { continue }
		setup := setups[len(setups)-1]
		if !setup.Valid { continue }

		entryBar := i + 1
		entry := ks[entryBar].Open
		if entry <= 0 { entry = ks[entryBar].Close }

		plan, ok := buildLearningPlan(setup, fib, entry, structure, blocks, cfg.TradePlan)
		if !ok { continue }

		open = LearnedTrade{
			Direction: setup.Direction,
			EntryBar: entryBar,
			EntryPrice: entry * entryMultiplier(setup.Direction, cfg.SlippagePct),
			FeePct: cfg.FeePct,
			Setup: inferSetupType(structure, i, setup.Direction),
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

func fibonacciFromLatestImpulse(s Structure, cfg FibConfig) (Fibonacci, bool) {
	if len(s.Swings) < 2 { return Fibonacci{}, false }
	for i := len(s.Swings)-1; i > 0; i-- {
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

func buildLearningPlan(setup Setup, fib Fibonacci, entry float64, structure Structure, blocks []OrderBlock, cfg TradePlanConfig) (TradePlan, bool) {
	plans := BuildTradePlans([]domain.Kline{{Close: entry}}, Structure{Swings: structure.Swings}, blocks,
		[]Setup{{Index: 0, Direction: setup.Direction, OrderBlockIndex: setup.OrderBlockIndex, Valid: true}}, cfg)
	if len(plans) > 0 { return plans[0], true }

	if setup.Direction == domain.DirectionBuy {
		stop := fib.Zone2Low * (1 - cfg.StopBufferPct)
		if stop >= entry { stop = entry * (1 - cfg.MaxStopPct) }
		target := fib.Target1
		if target <= entry { target = fib.Target2 }
		if target > entry && entry > stop && (entry-stop)/entry <= cfg.MaxStopPct {
			return TradePlan{SetupIndex: setup.Index, Direction: setup.Direction, Entry: entry, StopLoss: stop, TakeProfit: target, Valid: true}, true
		}
	} else {
		stop := fib.Zone2High * (1 + cfg.StopBufferPct)
		if stop <= entry { stop = entry * (1 + cfg.MaxStopPct) }
		target := fib.Target1
		if target >= entry { target = fib.Target2 }
		if target < entry && stop > entry && (stop-entry)/entry <= cfg.MaxStopPct {
			return TradePlan{SetupIndex: setup.Index, Direction: setup.Direction, Entry: entry, StopLoss: stop, TakeProfit: target, Valid: true}, true
		}
	}
	return TradePlan{}, false
}

// ValidateKlines valida la integridad del histórico antes de aprender (sec 43).
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
	if direction == domain.DirectionBuy { return 1 + slippage }
	return 1 - slippage
}

// applyExitSlippage ajusta el precio de salida por slippage UNA sola vez:
// salir de un long es vender (menos), salir de un short es comprar (más).
func applyExitSlippage(exit float64, direction domain.Direction, slippage float64) float64 {
	if direction == domain.DirectionBuy { return exit * (1 - slippage) }
	return exit * (1 + slippage)
}

func tradePnL(direction domain.Direction, entry, exit float64) float64 {
	if entry <= 0 { return 0 }
	if direction == domain.DirectionBuy { return (exit-entry)/entry }
	return (entry-exit)/entry
}

type learnMetrics struct {
	trades int
	winRate float64
	profitFactor float64
	sharpe float64
	sortino float64
	totalReturn float64
	maxDrawdown float64
	finalBalance float64
	averageWin float64
	averageLoss float64
	expectancy float64
	grossProfit float64
	grossLoss float64
	feesPaid float64
	wins int
	losses int
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
		if t.FeePct > 0 { feesPaid += 2 * t.FeePct * equity }
		equity *= 1 + t.PnL
		if t.PnL > 0 { wins++; gains += t.PnL } else { losses++; lossSum -= t.PnL }
		if equity > peak { peak = equity }
		if dd := (peak-equity)/peak; dd > maxDD { maxDD = dd }
	}
	wr := 0.0
	if len(trades) > 0 { wr = float64(wins)/float64(len(trades)) }
	pf := 0.0
	if lossSum > 0 { pf = gains/lossSum } else if gains > 0 { pf = math.Inf(1) }

	metrics := learnMetrics{
		trades: len(trades), winRate: wr, profitFactor: pf,
		totalReturn: equity/initial-1, maxDrawdown: maxDD,
		finalBalance: equity, wins: wins, losses: losses,
		grossProfit: gains, grossLoss: lossSum, feesPaid: feesPaid,
	}
	if wins > 0 { metrics.averageWin = gains / float64(wins) }
	if losses > 0 { metrics.averageLoss = lossSum / float64(losses) }
	metrics.expectancy = wr*metrics.averageWin - (1-wr)*metrics.averageLoss
	metrics.sharpe, metrics.sortino = returnsStats(pnls)
	return metrics
}

// returnsStats calcula Sharpe y Sortino por operación (media / desviación,
// y media / desviación de las pérdidas respectivamente), sin anualizar.
func returnsStats(pnls []float64) (sharpe, sortino float64) {
	n := len(pnls)
	if n == 0 { return 0, 0 }
	mean, sd := 0.0, 0.0
	for _, p := range pnls { mean += p }
	mean /= float64(n)
	for _, p := range pnls { d := p - mean; sd += d * d }
	sd = math.Sqrt(sd / float64(n))
	if sd > 0 { sharpe = mean / sd }

	down, count := 0.0, 0
	for _, p := range pnls {
		if p < 0 { down += p * p; count++ }
	}
	if count > 0 {
		down = math.Sqrt(down / float64(count))
		if down > 0 { sortino = mean / down }
	}
	return sharpe, sortino
}

func inferSetupType(s Structure, index int, dir domain.Direction) SetupType {
	classified := ClassifySetups(s)
	for i := len(classified)-1; i >= 0; i-- {
		if classified[i].Index <= index && classified[i].Direction == dir { return classified[i].SetupType }
	}
	if dir == domain.DirectionBuy { return SetupContinuationLong }
	return SetupContinuationShort
}
