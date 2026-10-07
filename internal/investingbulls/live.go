package investingbulls

import (
	"context"
	"encoding/json"
	"fmt"

	"tradingview-bot/internal/domain"
)

// candleReader es la única capacidad de lectura de velas que EvaluateLive
// necesita: el bus de velas en vivo no implementa domain.CandleStore completo.
type candleReader interface {
	RecentCandles(ctx context.Context, symbol, timeframe string, limit int) ([]domain.Kline, error)
}

// EvaluateLive evaluates a persisted MTF model using only closed candles up to now.
func EvaluateLive(ctx context.Context, store candleReader, strategy domain.Strategy, k domain.Kline) (domain.SignalEvent, bool, error) {
	var model MultiTimeframeModel
	if err := json.Unmarshal([]byte(strategy.Spec), &model); err != nil {
		return domain.SignalEvent{}, false, fmt.Errorf("investing bulls spec: %w", err)
	}
	if model.Timeframes.EntryTimeframe == "" {
		return domain.SignalEvent{}, false, fmt.Errorf("investing bulls spec: entry timeframe vacío")
	}
	if k.Timeframe != model.Timeframes.EntryTimeframe {
		return domain.SignalEvent{}, false, nil
	}
	main, err := store.RecentCandles(ctx, k.Symbol, model.Timeframes.MainTimeframe, 5000)
	if err != nil {
		return domain.SignalEvent{}, false, err
	}
	entry, err := store.RecentCandles(ctx, k.Symbol, model.Timeframes.EntryTimeframe, 5000)
	if err != nil {
		return domain.SignalEvent{}, false, err
	}
	var confirm []domain.Kline
	if model.Timeframes.RequireConfirm {
		confirm, err = store.RecentCandles(ctx, k.Symbol, model.Timeframes.ConfirmTimeframe, 5000)
		if err != nil {
			return domain.SignalEvent{}, false, err
		}
	}
	main = closedCandles(main)
	entry = closedCandles(entry)
	confirm = closedCandles(confirm)
	if len(main) < 30 || len(entry) < 30 {
		return domain.SignalEvent{}, false, nil
	}
	ms := Analyze(closedCandlesBefore(main, k.Start), model.Timeframes.MainSwingLeft, model.Timeframes.MainSwingRight)
	ep := candlesThrough(entry, k.Start)
	if len(ep) < 30 {
		return domain.SignalEvent{}, false, nil
	}
	es := Analyze(ep, model.Base.SwingLeft, model.Base.SwingRight)
	classified := ClassifySetups(es)
	if len(classified) == 0 {
		return domain.SignalEvent{}, false, nil
	}
	var candidate *ClassifiedSetup
	allowed := map[SetupType]bool{}
	for _, s := range model.AllowedSetups {
		allowed[s] = true
	}
	for i := len(classified) - 1; i >= 0; i-- {
		c := classified[i]
		if c.Index > len(ep)-1 {
			continue
		}
		if !allowed[c.SetupType] {
			continue
		}
		if c.Direction == domain.DirectionBuy && ms.Trend != TrendBullish {
			continue
		}
		if c.Direction == domain.DirectionSell && ms.Trend != TrendBearish {
			continue
		}
		candidate = &c
		break
	}
	if candidate == nil {
		return domain.SignalEvent{}, false, nil
	}
	if model.Timeframes.RequireConfirm && !confirmationMatches(confirm, k.Start, candidate.Direction, model.Timeframes) {
		return domain.SignalEvent{}, false, nil
	}
	fib, ok := fibonacciFromLatestImpulse(es, model.Base.Fib)
	if !ok {
		return domain.SignalEvent{}, false, nil
	}
	ic := DefaultImbalanceConfig()
	imbs := UpdateImbalances(DetectImbalances(ep, ic), ep, ic)
	bc := DefaultOrderBlockConfig()
	blocks := UpdateOrderBlocks(DetectOrderBlocks(ep, es.Breaks, bc), ep, bc)
	matched, valid := EvaluateConfluenceAt(ep, len(ep)-1, es, fib, imbs, blocks, model.Base.Confluence)
	if !valid || !matched.Valid || matched.Direction != candidate.Direction {
		return domain.SignalEvent{}, false, nil
	}
	// La estrategia arma la zona y espera que el precio vuelva a tocarla.
	zoneLow, zoneHigh := 0.0, 0.0
	switch matched.FibZone {
	case 1: zoneLow, zoneHigh = fib.Zone1Low, fib.Zone1High
	case 2: zoneLow, zoneHigh = fib.Zone2Low, fib.Zone2High
	default: return domain.SignalEvent{}, false, nil
	}
	pendingEntry := zoneHigh
	if candidate.Direction == domain.DirectionSell { pendingEntry = zoneLow }
	if pendingEntry <= 0 { return domain.SignalEvent{}, false, nil }
	plan, ok := buildLearningPlan(matched, fib, pendingEntry, es, blocks, model.Base.TradePlan)
	if !ok { return domain.SignalEvent{}, false, nil }

	ev := domain.SignalEvent{
		StrategyID: strategy.ID, Symbol: k.Symbol, Timeframe: k.Timeframe,
		Direction: candidate.Direction, Price: pendingEntry, BarTS: k.Start,
		Meta: map[string]any{
			"source":"investing_bulls", "setup":string(candidate.SetupType),
			"main_trend":string(ms.Trend), "stop_loss":plan.StopLoss,
			"take_profit":plan.TakeProfit, "entry_pending":true,
			"pending_entry":pendingEntry, "entry_zone_low":zoneLow,
			"entry_zone_high":zoneHigh,
		},
	}
	return ev, true, nil
}
