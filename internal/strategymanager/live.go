package strategymanager

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"tradingview-bot/internal/backtest"
	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/strategies"
)

// SignalPublisher es el bus que recibe las señales generadas por una estrategia.
type SignalPublisher interface {
	Publish(context.Context, domain.SignalEvent) error
}

// LiveEngine evalúa la estrategia activa sobre las velas cerradas recibidas
// desde TradingView y publica las señales en el mismo bus que ya consume el
// processor/paper engine.
type LiveEngine struct {
	candles    CandleSource
	strategies domain.StrategyStore
	publisher  SignalPublisher

	mu         sync.RWMutex
	strategyID string
	symbol     string
	timeframe  string
	lastBar    map[string]int64
}

func NewLiveEngine(candles CandleSource, strategyStore domain.StrategyStore, publisher SignalPublisher) *LiveEngine {
	return &LiveEngine{
		candles:    candles,
		strategies: strategyStore,
		publisher:  publisher,
		strategyID: "chandelier",
		symbol:     "BTCUSDT",
		timeframe:  "1h",
		lastBar:    make(map[string]int64),
	}
}

func (e *LiveEngine) SetSelection(strategyID, symbol, timeframe string) error {
	if strings.TrimSpace(strategyID) == "" || strings.TrimSpace(symbol) == "" || strings.TrimSpace(timeframe) == "" {
		return fmt.Errorf("estrategia, símbolo y temporalidad son requeridos")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.strategyID = strings.TrimSpace(strategyID)
	e.symbol = strings.TrimSpace(symbol)
	e.timeframe = strings.TrimSpace(timeframe)
	return nil
}

func (e *LiveEngine) Selection() (string, string, string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.strategyID, e.symbol, e.timeframe
}

// OnCandle evalúa únicamente velas cerradas del símbolo/temporalidad activos.
// No genera señales duplicadas para la misma barra.
func (e *LiveEngine) OnCandle(ctx context.Context, k domain.Kline) error {
	if !k.Closed {
		return nil
	}
	sid, symbol, timeframe := e.Selection()
	if !strings.EqualFold(k.Symbol, symbol) || !strings.EqualFold(k.Timeframe, timeframe) {
		return nil
	}

	key := sid + "|" + k.Symbol + "|" + k.Timeframe
	ts := k.Start.UnixMilli()
	e.mu.Lock()
	if e.lastBar[key] >= ts {
		e.mu.Unlock()
		return nil
	}
	e.lastBar[key] = ts
	e.mu.Unlock()

	ks, err := e.candles.RecentCandles(ctx, k.Symbol, k.Timeframe, 300)
	if err != nil {
		return fmt.Errorf("cargando velas para estrategia %s: %w", sid, err)
	}
	if len(ks) < 3 {
		return nil
	}
	idx := len(ks) - 1

	var dir domain.Direction
	if sid == "chandelier" {
		d := strategies.DefaultChandelier().Signal(idx, ks)
		switch d {
		case backtest.SignalLong:
			dir = domain.DirectionBuy
		case backtest.SignalShort:
			dir = domain.DirectionSell
		default:
			return nil
		}
	} else {
		if e.strategies == nil {
			return fmt.Errorf("store de estrategias no configurado")
		}
		st, err := e.strategies.GetStrategy(ctx, sid)
		if err != nil {
			return fmt.Errorf("leyendo estrategia %s: %w", sid, err)
		}
		if st.Status != domain.StrategyActive {
			return nil
		}
		spec, err := ParseSpec(st.Spec)
		if err != nil {
			return fmt.Errorf("spec %s: %w", sid, err)
		}
		eval := newEvalContext(ks, spec.Indicators)
		switch {
		case ruleMatches(eval, spec.Entries, "buy", idx):
			dir = domain.DirectionBuy
		case ruleMatches(eval, spec.Entries, "sell", idx):
			dir = domain.DirectionSell
		default:
			return nil
		}
	}

	ev := domain.SignalEvent{
		StrategyID: sid,
		Symbol:     k.Symbol,
		Timeframe:  k.Timeframe,
		Direction:  dir,
		Price:      k.Close,
		BarTS:      k.Start,
		ReceivedAt: time.Now().UTC(),
		Meta: map[string]any{
			"source": "tradingview",
		},
	}
	return e.publisher.Publish(ctx, ev)
}
