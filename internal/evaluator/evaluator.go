package evaluator

import (
	"context"
	"errors"

	"tradingview-bot/internal/domain"
)

type StateEvaluator struct {
	store domain.SignalStore
}

func New(store domain.SignalStore) *StateEvaluator {
	return &StateEvaluator{store: store}
}

func (e *StateEvaluator) Evaluate(ctx context.Context, ev domain.SignalEvent) (bool, error) {
	last, err := e.store.LastSignal(ctx, ev.StrategyID, ev.Symbol, ev.Timeframe)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return true, nil
		}
		return false, err
	}
	return last.Direction != ev.Direction, nil
}
