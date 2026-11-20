package evaluator

import (
	"context"
	"errors"

	"tradingview-bot/internal/domain"
)

type Policy string

const (
	PolicyDirection   Policy = "direction"
	PolicyIdempotency Policy = "idempotency"
)

type StateEvaluator struct {
	store  domain.SignalStore
	policy Policy
}

func New(store domain.SignalStore) *StateEvaluator {
	return NewWithPolicy(store, PolicyDirection)
}

func NewWithPolicy(store domain.SignalStore, policy Policy) *StateEvaluator {
	if policy == "" {
		policy = PolicyDirection
	}
	return &StateEvaluator{store: store, policy: policy}
}

func (e *StateEvaluator) Evaluate(ctx context.Context, ev domain.SignalEvent) (bool, error) {
	last, err := e.store.LastSignal(ctx, ev.StrategyID, ev.Symbol, ev.Timeframe)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return true, nil
		}
		return false, err
	}
	if e.policy == PolicyIdempotency {
		return last.IdempotencyKey() != ev.IdempotencyKey(), nil
	}
	return last.Direction != ev.Direction, nil
}
