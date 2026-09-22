package investingbulls

import (
	"context"
	"fmt"
	"time"

	"tradingview-bot/internal/domain"
)

// LearnerStore is the persistence boundary used by the learning workflow.
type LearnerStore interface {
	domain.CandleStore
	domain.StrategyStore
	domain.BacktestStore
}

// LearnAndPersist loads historical candles, learns a candidate, and persists
// it as candidate. It deliberately does not activate the strategy: OOS
// validation/promotion is a separate step.
func LearnAndPersist(ctx context.Context, store LearnerStore, symbol, timeframe string, limit int, cfg LearnConfig) (domain.Strategy, LearnResult, error) {
	if store == nil {
		return domain.Strategy{}, LearnResult{}, fmt.Errorf("learn: store requerido")
	}
	if symbol == "" || timeframe == "" {
		return domain.Strategy{}, LearnResult{}, fmt.Errorf("learn: symbol y timeframe son requeridos")
	}
	if limit <= 0 {
		limit = 5000
	}

	ks, err := store.RecentCandles(ctx, symbol, timeframe, limit)
	if err != nil {
		return domain.Strategy{}, LearnResult{}, fmt.Errorf("learn: cargando histórico: %w", err)
	}
	ks = closedCandles(ks)
	if len(ks) < 100 {
		return domain.Strategy{}, LearnResult{}, fmt.Errorf("learn: histórico cerrado insuficiente: %d velas", len(ks))
	}

	cfg.Symbol = symbol
	cfg.Timeframe = timeframe
	result, err := Learn(ks, cfg)
	if err != nil {
		return domain.Strategy{}, result, err
	}
	if result.SpecJSON == "" {
		return domain.Strategy{}, result, fmt.Errorf("learn: no se generó candidato: %s", result.Reason)
	}

	now := time.Now().UTC()
	id := fmt.Sprintf("investing-bulls-%s-%s-%d", symbol, timeframe, now.Unix())
	strategy := domain.Strategy{
		ID: id,
		Name: fmt.Sprintf("Investing Bulls %s %s", symbol, timeframe),
		Description: "Estrategia aprendida por búsqueda de parámetros sobre estructura, CHOCH/BOS, Fibonacci, imbalance y order block.",
		Status: domain.StrategyCandidate,
		Source: "investing_bulls_learn",
		Spec: result.SpecJSON,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := store.UpsertStrategy(ctx, strategy); err != nil {
		return domain.Strategy{}, result, fmt.Errorf("learn: guardando estrategia: %w", err)
	}

	bt := domain.BacktestResult{
		StrategyID: strategy.ID,
		Trades: result.Model.Trades,
		WinRate: result.Model.WinRate,
		ProfitFactor: result.Model.ProfitFactor,
		MaxDrawdown: result.Model.MaxDrawdown,
		TotalReturn: result.Model.TotalReturn,
		TestBars: len(ks),
		Passed: result.Accepted,
		Folds: 0,
		OOSFolds: nil,
		Status: "in_sample_candidate",
		MetricsAt: now,
	}
	if err := store.SaveBacktest(ctx, bt); err != nil {
		return domain.Strategy{}, result, fmt.Errorf("learn: guardando métricas: %w", err)
	}

	return strategy, result, nil
}

func closedCandles(ks []domain.Kline) []domain.Kline {
	out := make([]domain.Kline, 0, len(ks))
	for _, k := range ks { if k.Closed { out = append(out, k) } }
	return out
}
