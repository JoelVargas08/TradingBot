package investingbulls

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"tradingview-bot/internal/domain"
)

// ValidateAndPromote performs walk-forward validation for a persisted candidate.
// A strategy becomes active only when the OOS criteria pass.
func ValidateAndPromote(ctx context.Context, store LearnerStore, strategyID, symbol, timeframe string, limit int, wcfg WalkForwardConfig) (domain.BacktestResult, error) {
	if store == nil {
		return domain.BacktestResult{}, fmt.Errorf("oos: store requerido")
	}
	if strategyID == "" || symbol == "" || timeframe == "" {
		return domain.BacktestResult{}, fmt.Errorf("oos: strategyID, symbol y timeframe son requeridos")
	}
	if limit <= 0 {
		limit = 5000
	}

	strategy, err := store.GetStrategy(ctx, strategyID)
	if err != nil {
		return domain.BacktestResult{}, fmt.Errorf("oos: cargando estrategia: %w", err)
	}
	if strategy.Spec == "" {
		return domain.BacktestResult{}, fmt.Errorf("oos: la estrategia no contiene Spec")
	}

	var model LearnedModel
	if err := json.Unmarshal([]byte(strategy.Spec), &model); err != nil {
		return domain.BacktestResult{}, fmt.Errorf("oos: decodificando modelo: %w", err)
	}
	if model.Symbol != "" && model.Symbol != symbol {
		return domain.BacktestResult{}, fmt.Errorf("oos: symbol no coincide: modelo=%s solicitado=%s", model.Symbol, symbol)
	}
	if model.Timeframe != "" && model.Timeframe != timeframe {
		return domain.BacktestResult{}, fmt.Errorf("oos: timeframe no coincide: modelo=%s solicitado=%s", model.Timeframe, timeframe)
	}

	ks, err := store.RecentCandles(ctx, symbol, timeframe, limit)
	if err != nil {
		return domain.BacktestResult{}, fmt.Errorf("oos: cargando histórico: %w", err)
	}

	cfg := DefaultLearnConfig()
	cfg.Symbol = symbol
	cfg.Timeframe = timeframe
	cfg.SwingLeft = model.SwingLeft
	cfg.SwingRight = model.SwingRight
	cfg.Fib = model.Fib
	cfg.Confluence = model.Confluence
	cfg.TradePlan = model.TradePlan

	wf, err := WalkForward(ks, cfg, wcfg)
	if err != nil {
		return domain.BacktestResult{}, err
	}

	now := time.Now().UTC()
	bt := domain.BacktestResult{
		StrategyID: strategyID,
		Trades: wf.TotalTrades,
		WinRate: wf.WinRate,
		ProfitFactor: wf.ProfitFactor,
		MaxDrawdown: wf.MaxDrawdown,
		TotalReturn: wf.TotalReturn,
		TestBars: wf.EvaluatedBars,
		Passed: wf.Passed,
		Folds: len(wf.Folds),
		OOSFolds: wf.Folds,
		Status: "oos_validated",
		MetricsAt: now,
	}
	if err := store.SaveBacktest(ctx, bt); err != nil {
		return domain.BacktestResult{}, fmt.Errorf("oos: guardando métricas: %w", err)
	}

	strategy.UpdatedAt = now
	strategy.Error = ""
	if wf.Passed {
		strategy.Status = domain.StrategyActive
	} else {
		strategy.Status = domain.StrategyRejected
		strategy.Error = wf.Reason
	}
	if err := store.UpsertStrategy(ctx, strategy); err != nil {
		return domain.BacktestResult{}, fmt.Errorf("oos: actualizando estado de estrategia: %w", err)
	}

	return bt, nil
}
