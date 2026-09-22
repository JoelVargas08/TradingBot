package investingbulls

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"tradingview-bot/internal/domain"
)

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
	Version int
	Family string
	Symbol string
	Timeframe string
	SwingLeft int
	SwingRight int
	Fib FibConfig
	Confluence ConfluenceConfig
	TradePlan TradePlanConfig
	Trades int
	WinRate float64
	ProfitFactor float64
	TotalReturn float64
	MaxDrawdown float64
	LearnedAt time.Time
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
	Reason string
}

// Learn performs a deterministic parameter search over the source strategy.
// It is parameter optimization/backtesting, not a machine-learning model.
// Each decision at bar i uses only candles [0..i].
func Learn(ks []domain.Kline, cfg LearnConfig) (LearnResult, error) {
	cfg = normalizeLearnConfig(cfg)
	if len(ks) < 100 {
		return LearnResult{}, fmt.Errorf("learn: se necesitan al menos 100 velas, hay %d", len(ks))
	}

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
					Trades: metrics.trades, WinRate: metrics.winRate,
					ProfitFactor: metrics.profitFactor,
					TotalReturn: metrics.totalReturn,
					MaxDrawdown: metrics.maxDrawdown,
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
	var out []LearnedTrade
	inTrade := false
	var open LearnedTrade
	stop, target := 0.0, 0.0

	for i := 20; i < len(ks)-1; i++ {
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
				open.ExitBar, open.ExitPrice, open.Reason = i, exit, reason
				open.PnL = tradePnL(open.Direction, open.EntryPrice, exit) - 2*cfg.FeePct - 2*cfg.SlippagePct
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
		}
		stop, target = plan.StopLoss, plan.TakeProfit
		inTrade = true
	}

	if inTrade {
		last := ks[len(ks)-1]
		open.ExitBar, open.ExitPrice, open.Reason = len(ks)-1, last.Close, "end"
		open.PnL = tradePnL(open.Direction, open.EntryPrice, last.Close) - 2*cfg.FeePct - 2*cfg.SlippagePct
		out = append(out, open)
	}
	return out
}

func fibonacciFromLatestImpulse(s Structure, cfg FibConfig) (Fibonacci, bool) {
	if len(s.Swings) < 2 { return Fibonacci{}, false }
	for i := len(s.Swings)-1; i > 0; i-- {
		a, b := s.Swings[i-1], s.Swings[i]
		if s.Trend == TrendBullish && !a.High && b.High && a.Price < b.Price {
			return NewFibonacci(a.Price, b.Price, TrendBullish, cfg)
		}
		if s.Trend == TrendBearish && a.High && !b.High && a.Price > b.Price {
			return NewFibonacci(a.Price, b.Price, TrendBearish, cfg)
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

func entryMultiplier(direction domain.Direction, slippage float64) float64 {
	if direction == domain.DirectionBuy { return 1 + slippage }
	return 1 - slippage
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
	totalReturn float64
	maxDrawdown float64
	finalBalance float64
}

func tradeMetrics(trades []LearnedTrade, initial float64) learnMetrics {
	equity, peak := initial, initial
	maxDD := 0.0
	wins := 0
	var gains, losses float64
	for _, t := range trades {
		equity *= 1 + t.PnL
		if t.PnL > 0 { wins++; gains += t.PnL } else { losses -= t.PnL }
		if equity > peak { peak = equity }
		if dd := (peak-equity)/peak; dd > maxDD { maxDD = dd }
	}
	wr := 0.0
	if len(trades) > 0 { wr = float64(wins)/float64(len(trades)) }
	pf := 0.0
	if losses > 0 { pf = gains/losses } else if gains > 0 { pf = math.Inf(1) }
	return learnMetrics{trades: len(trades), winRate: wr, profitFactor: pf, totalReturn: equity/initial-1, maxDrawdown: maxDD, finalBalance: equity}
}
