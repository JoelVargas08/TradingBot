package investingbulls

import (
	"fmt"
	"math"
	"time"

	"tradingview-bot/internal/domain"
)

// WalkForwardConfig controls chronological train/OOS validation.
// These are validation-engineering choices, not claims from the source PDF.
type WalkForwardConfig struct {
	Folds           int
	TrainPct        float64
	OOSPct          float64
	StepPct         float64
	MinOOSTrades    int
	MinOOSProfitFactor float64
	MaxOOSDrawdown  float64
	MinPositiveFolds int
}

// DefaultWalkForwardConfig uses an expanding-window walk-forward:
// 60% initial training, then four 10% OOS windows advancing by 10%.
func DefaultWalkForwardConfig() WalkForwardConfig {
	return WalkForwardConfig{
		Folds: 4,
		TrainPct: 0.60,
		OOSPct: 0.10,
		StepPct: 0.10,
		MinOOSTrades: 3,
		MinOOSProfitFactor: 1.0,
		MaxOOSDrawdown: 0.08,
		MinPositiveFolds: 3,
	}
}

type WalkForwardResult struct {
	Folds  []domain.OOSFold
	Passed bool
	Reason string
	EvaluatedBars int
	TotalTrades int
	WinRate float64
	ProfitFactor float64
	TotalReturn float64
	MaxDrawdown float64
	ValidatedAt time.Time
}

// WalkForward learns parameters only from each training window, then freezes
// them and evaluates them on the following OOS window. OOS candles are never
// used to optimize the parameters for that fold.
func WalkForward(ks []domain.Kline, cfg LearnConfig, wcfg WalkForwardConfig) (WalkForwardResult, error) {
	wcfg = normalizeWalkForwardConfig(wcfg)
	if len(ks) < 100 {
		return WalkForwardResult{}, fmt.Errorf("walk-forward: se necesitan al menos 100 velas, hay %d", len(ks))
	}
	if wcfg.TrainPct+wcfg.OOSPct+(float64(wcfg.Folds-1)*wcfg.StepPct) > 1.000001 {
		return WalkForwardResult{}, fmt.Errorf("walk-forward: ventanas exceden el histórico disponible")
	}

	var folds []domain.OOSFold
	var allTrades []LearnedTrade
	positive := 0
	lastOOS := 0

	for n := 0; n < wcfg.Folds; n++ {
		trainEnd := int(math.Floor(float64(len(ks)) * (wcfg.TrainPct + float64(n)*wcfg.StepPct)))
		oosEnd := int(math.Floor(float64(len(ks)) * (wcfg.TrainPct + float64(n)*wcfg.StepPct + wcfg.OOSPct)))
		if trainEnd < 100 || oosEnd > len(ks) || oosEnd <= trainEnd {
			return WalkForwardResult{}, fmt.Errorf("walk-forward: fold %d inválido: train=%d oos=%d", n+1, trainEnd, oosEnd)
		}

		trainCfg := cfg
		trainCfg.Symbol = cfg.Symbol
		trainCfg.Timeframe = cfg.Timeframe
		learned, err := Learn(ks[:trainEnd], trainCfg)
		if err != nil {
			return WalkForwardResult{}, fmt.Errorf("walk-forward: fold %d: %w", n+1, err)
		}
		if learned.Model.Trades == 0 {
			return WalkForwardResult{}, fmt.Errorf("walk-forward: fold %d no produjo modelo entrenable", n+1)
		}

		fixedCfg := trainCfg
		fixedCfg.SwingLeft = learned.Model.SwingLeft
		fixedCfg.SwingRight = learned.Model.SwingRight
		fixedCfg.Fib = learned.Model.Fib
		fixedCfg.Confluence = learned.Model.Confluence
		fixedCfg.TradePlan = learned.Model.TradePlan
		fixedCfg.InitialBalance = cfg.InitialBalance
		fixedCfg.FeePct = cfg.FeePct
		fixedCfg.SlippagePct = cfg.SlippagePct

		// Keep the complete prefix available for indicator warm-up, but count
		// only trades whose decision/entry occurs inside this OOS window.
		trades := generateAndSimulateFrom(ks[:oosEnd], fixedCfg, trainEnd)
		m := tradeMetrics(trades, fixedCfg.InitialBalance)
		fold := domain.OOSFold{
			Trades: m.trades,
			WinRate: m.winRate,
			ProfitFactor: m.profitFactor,
			MaxDrawdown: m.maxDrawdown,
			TotalReturn: m.totalReturn,
			Bars: oosEnd-trainEnd,
		}
		folds = append(folds, fold)
		allTrades = append(allTrades, trades...)
		lastOOS = oosEnd

		if m.trades >= wcfg.MinOOSTrades && m.profitFactor >= wcfg.MinOOSProfitFactor && m.maxDrawdown <= wcfg.MaxOOSDrawdown {
			positive++
		}
	}

	if len(folds) == 0 {
		return WalkForwardResult{}, fmt.Errorf("walk-forward: no hubo folds evaluables")
	}

	agg := tradeMetrics(allTrades, cfg.InitialBalance)
	passed := positive >= wcfg.MinPositiveFolds && agg.trades >= wcfg.MinOOSTrades && agg.profitFactor >= wcfg.MinOOSProfitFactor && agg.maxDrawdown <= wcfg.MaxOOSDrawdown
	reason := fmt.Sprintf("%d/%d folds cumplen los criterios OOS; trades=%d PF=%.2f DD=%.2f%%", positive, len(folds), agg.trades, agg.profitFactor, agg.maxDrawdown*100)
	if !passed {
		reason = "OOS rechazado: " + reason
	} else {
		reason = "OOS aprobado: " + reason
	}

	return WalkForwardResult{
		Folds: folds,
		Passed: passed,
		Reason: reason,
		EvaluatedBars: lastOOS-int(math.Floor(float64(len(ks))*wcfg.TrainPct)),
		TotalTrades: agg.trades,
		WinRate: agg.winRate,
		ProfitFactor: agg.profitFactor,
		TotalReturn: agg.totalReturn,
		MaxDrawdown: agg.maxDrawdown,
		ValidatedAt: time.Now().UTC(),
	}, nil
}

func normalizeWalkForwardConfig(cfg WalkForwardConfig) WalkForwardConfig {
	defaults := DefaultWalkForwardConfig()
	if cfg.Folds <= 0 { cfg.Folds = defaults.Folds }
	if cfg.TrainPct <= 0 { cfg.TrainPct = defaults.TrainPct }
	if cfg.OOSPct <= 0 { cfg.OOSPct = defaults.OOSPct }
	if cfg.StepPct <= 0 { cfg.StepPct = defaults.StepPct }
	if cfg.MinOOSTrades <= 0 { cfg.MinOOSTrades = defaults.MinOOSTrades }
	if cfg.MinOOSProfitFactor <= 0 { cfg.MinOOSProfitFactor = defaults.MinOOSProfitFactor }
	if cfg.MaxOOSDrawdown <= 0 { cfg.MaxOOSDrawdown = defaults.MaxOOSDrawdown }
	if cfg.MinPositiveFolds <= 0 { cfg.MinPositiveFolds = defaults.MinPositiveFolds }
	if cfg.MinPositiveFolds > cfg.Folds { cfg.MinPositiveFolds = cfg.Folds }
	return cfg
}
