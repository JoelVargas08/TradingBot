package ml

import (
	"context"
	"sync"

	"tradingview-bot/internal/domain"
)

// Publisher abstraction so the sink does not depend on the concrete bus.
type Publisher interface {
	Publish(ctx context.Context, ev domain.SignalEvent) error
}

// SignalSink connects the streaming market feed to the ML engine without
// mixing timeframes: it only forwards the configured symbol/timeframe and
// seeds the engine from persisted candles on the first closed candle.
type SignalSink struct {
	engine    *Engine
	publisher Publisher
	store     domain.CandleStore
	symbol    string
	timeframe string

	mu     sync.Mutex
	seeded bool
}

func NewSignalSink(engine *Engine, publisher Publisher, store domain.CandleStore, symbol, timeframe string) *SignalSink {
	return &SignalSink{
		engine:    engine,
		publisher: publisher,
		store:     store,
		symbol:    symbol,
		timeframe: timeframe,
	}
}

func (s *SignalSink) OnCandle(ctx context.Context, k domain.Kline) error {
	if s.engine == nil || k.Symbol != s.symbol || k.Timeframe != s.timeframe {
		return nil
	}
	s.seedOnce(ctx)

	events, err := s.engine.OnCandle(ctx, k)
	if err != nil {
		return err
	}
	for _, ev := range events {
		if s.publisher != nil {
			if err := s.publisher.Publish(ctx, ev); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SignalSink) SetSelection(strategyID, symbol, timeframe string) error { return nil }

func (s *SignalSink) seedOnce(ctx context.Context) {
	s.mu.Lock()
	if s.seeded {
		s.mu.Unlock()
		return
	}
	s.seeded = true
	s.mu.Unlock()

	if s.store == nil || s.engine == nil {
		return
	}
	history, err := s.store.RecentCandles(ctx, s.symbol, s.timeframe, s.engine.Window())
	if err != nil || len(history) == 0 {
		return
	}
	s.engine.Seed(s.symbol, s.timeframe, history)
}
