package investingbulls

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"tradingview-bot/internal/domain"
)

// StageStatus resume el resultado de una etapa del pipeline.
type StageStatus string

const (
	StagePass StageStatus = "pass"
	StageFail StageStatus = "fail"
	StageSkip StageStatus = "skip"
)

// StageTrace registra el resultado de una etapa concreta del pipeline.
type StageTrace struct {
	Stage  string         `json:"stage"`
	Status StageStatus    `json:"status"`
	Reason string         `json:"reason,omitempty"`
	Values map[string]any `json:"values,omitempty"`
}

// PipelineTrace es el registro de solo lectura del pipeline completo para una
// vela: cada etapa declara si pasó, el motivo del rechazo y los valores útiles.
// El diagnóstico observa exactamente el mismo pipeline que PAPER/LIVE; no
// calcula una lógica paralela ni crea, modifica o cierra órdenes.
type PipelineTrace struct {
	Symbol      string       `json:"symbol"`
	Timeframe   string       `json:"timeframe"`
	BarTS       time.Time    `json:"bar_ts"`
	Stages      []StageTrace `json:"stages"`
	Accepted    bool         `json:"accepted"`
	FinalReason string       `json:"final_reason,omitempty"`
}

// Nombres canónicos de las etapas, en el orden del pipeline.
const (
	TraceStageData         = "datos"
	TraceStageMainTrend    = "tendencia_principal"
	TraceStageSetup        = "setup"
	TraceStageAlignment    = "alineacion_mtf"
	TraceStageFibonacci    = "fibonacci"
	TraceStageImbalance    = "imbalance"
	TraceStageOrderBlock   = "order_block"
	TraceStageSupplyDemand = "oferta_demanda"
	TraceStageConfluence   = "confluencia"
	TraceStageConfirm      = "confirmacion"
	TraceStageTradePlan    = "plan_trade"
	TraceStageRisk         = "riesgo"
	TraceStageFinal        = "resultado"
)

func (t *PipelineTrace) add(stage string, status StageStatus, reason string, values map[string]any) {
	t.Stages = append(t.Stages, StageTrace{Stage: stage, Status: status, Reason: reason, Values: values})
}

func (t *PipelineTrace) pass(stage string, values map[string]any) {
	t.add(stage, StagePass, "", values)
}

func (t *PipelineTrace) skip(stage, reason string) {
	t.add(stage, StageSkip, reason, nil)
}

func (t *PipelineTrace) fail(stage, reason string, values map[string]any) {
	t.add(stage, StageFail, reason, values)
	t.FinalReason = reason
}

// Summary devuelve una representación legible del trace para Telegram/log.
func (t PipelineTrace) Summary() string {
	var b strings.Builder
	state := "RECHAZADA"
	if t.Accepted {
		state = "ACEPTADA"
	}
	fmt.Fprintf(&b, "Pipeline %s %s %s\n", t.Symbol, t.Timeframe, t.BarTS.UTC().Format(time.RFC3339))
	for _, s := range t.Stages {
		mark := "✓"
		switch s.Status {
		case StageFail:
			mark = "✗"
		case StageSkip:
			mark = "·"
		}
		fmt.Fprintf(&b, "%s %s", mark, s.Stage)
		if s.Reason != "" {
			fmt.Fprintf(&b, " (%s)", s.Reason)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Resultado: %s", state)
	if t.FinalReason != "" {
		fmt.Fprintf(&b, " — %s", t.FinalReason)
	}
	return b.String()
}

// EvaluateLiveWithTrace evalúa el modelo MTF persistido usando solo velas
// cerradas y devuelve, además de la señal, la traza completa del pipeline.
// Comparte la implementación con EvaluateLive: no existe una segunda ruta.
func EvaluateLiveWithTrace(ctx context.Context, store candleReader, strategy domain.Strategy, k domain.Kline) (domain.SignalEvent, bool, PipelineTrace, error) {
	trace := PipelineTrace{Symbol: k.Symbol, Timeframe: k.Timeframe, BarTS: k.Start}
	logReject := func(reason string) {
		log.Printf("IB MTF diagnóstico strategy=%s bar=%s reason=%s", strategy.ID, k.Start.UTC().Format(time.RFC3339), reason)
	}

	model, err := LoadMultiTimeframeModel(strategy.Spec)
	if err != nil {
		trace.fail(TraceStageData, "spec_invalido", map[string]any{"error": err.Error()})
		return domain.SignalEvent{}, false, trace, fmt.Errorf("investing bulls spec: %w", err)
	}
	if model.Timeframes.EntryTimeframe == "" {
		trace.fail(TraceStageData, "entry_timeframe_vacio", nil)
		return domain.SignalEvent{}, false, trace, fmt.Errorf("investing bulls spec: entry timeframe vacío")
	}
	if k.Timeframe != model.Timeframes.EntryTimeframe {
		trace.skip(TraceStageData, "timeframe_no_entrada")
		return domain.SignalEvent{}, false, trace, nil
	}

	main, err := store.RecentCandles(ctx, k.Symbol, model.Timeframes.MainTimeframe, 5000)
	if err != nil {
		trace.fail(TraceStageData, "error_datos", map[string]any{"detalle": err.Error()})
		return domain.SignalEvent{}, false, trace, err
	}
	entry, err := store.RecentCandles(ctx, k.Symbol, model.Timeframes.EntryTimeframe, 5000)
	if err != nil {
		trace.fail(TraceStageData, "error_datos", map[string]any{"detalle": err.Error()})
		return domain.SignalEvent{}, false, trace, err
	}
	var confirm []domain.Kline
	if model.Timeframes.RequireConfirm {
		confirm, err = store.RecentCandles(ctx, k.Symbol, model.Timeframes.ConfirmTimeframe, 5000)
		if err != nil {
			trace.fail(TraceStageData, "error_datos_confirm", map[string]any{"detalle": err.Error()})
			return domain.SignalEvent{}, false, trace, err
		}
	}
	main = closedCandles(main)
	entry = closedCandles(entry)
	confirm = closedCandles(confirm)
	if len(main) < 30 || len(entry) < 30 {
		trace.fail(TraceStageData, "datos_insuficientes", map[string]any{"main": len(main), "entry": len(entry)})
		logReject(fmt.Sprintf("datos_insuficientes main=%d entry=%d", len(main), len(entry)))
		return domain.SignalEvent{}, false, trace, nil
	}
	trace.pass(TraceStageData, map[string]any{"main": len(main), "entry": len(entry), "confirm": len(confirm)})

	ms := Analyze(closedCandlesBefore(main, k.Start), model.Timeframes.MainSwingLeft, model.Timeframes.MainSwingRight)
	trace.pass(TraceStageMainTrend, map[string]any{"trend": string(ms.Trend)})
	ep := candlesThrough(entry, k.Start)
	if len(ep) < 30 {
		trace.fail(TraceStageData, "entrada_insuficiente", map[string]any{"entry": len(ep)})
		logReject(fmt.Sprintf("entrada_insuficiente entry=%d", len(ep)))
		return domain.SignalEvent{}, false, trace, nil
	}
	es := Analyze(ep, model.Base.SwingLeft, model.Base.SwingRight)
	classified := ClassifySetups(es)
	if len(classified) == 0 {
		trace.fail(TraceStageSetup, "sin_setup_15m", map[string]any{"trend1h": string(ms.Trend)})
		logReject(fmt.Sprintf("sin_setup_15m trend1h=%s", ms.Trend))
		return domain.SignalEvent{}, false, trace, nil
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
		trace.fail(TraceStageAlignment, "setup_no_alineado", map[string]any{"trend1h": string(ms.Trend)})
		logReject(fmt.Sprintf("setup_no_alineado trend1h=%s", ms.Trend))
		return domain.SignalEvent{}, false, trace, nil
	}
	trace.pass(TraceStageSetup, map[string]any{"setup": string(candidate.SetupType), "direction": string(candidate.Direction)})
	trace.pass(TraceStageAlignment, map[string]any{"trend1h": string(ms.Trend)})

	if model.Timeframes.RequireConfirm && !confirmationMatches(confirm, k.Start, candidate.Direction, model.Timeframes) {
		trace.fail(TraceStageConfirm, "confirmacion_5m_fail", map[string]any{"direction": string(candidate.Direction), "setup": string(candidate.SetupType)})
		logReject(fmt.Sprintf("confirmacion_5m_fail direction=%s setup=%s trend1h=%s", candidate.Direction, candidate.SetupType, ms.Trend))
		return domain.SignalEvent{}, false, trace, nil
	}
	if model.Timeframes.RequireConfirm {
		trace.pass(TraceStageConfirm, nil)
	} else {
		trace.skip(TraceStageConfirm, "confirmacion_no_requerida")
	}

	fib, ok := fibonacciFromLatestImpulse(es, model.Base.Fib)
	if !ok {
		trace.fail(TraceStageFibonacci, "fib_fail", map[string]any{"direction": string(candidate.Direction)})
		logReject(fmt.Sprintf("fib_fail direction=%s setup=%s trend1h=%s", candidate.Direction, candidate.SetupType, ms.Trend))
		return domain.SignalEvent{}, false, trace, nil
	}
	trace.pass(TraceStageFibonacci, map[string]any{"zone1": fmt.Sprintf("%.4f-%.4f", fib.Zone1Low, fib.Zone1High), "zone2": fmt.Sprintf("%.4f-%.4f", fib.Zone2Low, fib.Zone2High)})

	ic := DefaultImbalanceConfig()
	imbs := UpdateImbalances(DetectImbalances(ep, ic), ep, ic)
	if len(imbs) > 0 {
		trace.pass(TraceStageImbalance, map[string]any{"detectados": len(imbs)})
	} else {
		trace.skip(TraceStageImbalance, "sin_imbalances")
	}
	bc := DefaultOrderBlockConfig()
	blocks := UpdateOrderBlocks(DetectOrderBlocks(ep, es.Breaks, bc), ep, bc)
	if len(blocks) > 0 {
		trace.pass(TraceStageOrderBlock, map[string]any{"detectados": len(blocks)})
	} else {
		trace.skip(TraceStageOrderBlock, "sin_order_blocks")
	}
	sc := DefaultSupplyDemandConfig()
	zones := UpdateSupplyDemandZones(DetectSupplyDemandZones(ep, es.Breaks, sc), ep, sc)
	if len(zones) > 0 {
		trace.pass(TraceStageSupplyDemand, map[string]any{"detectados": len(zones)})
	} else {
		trace.skip(TraceStageSupplyDemand, "sin_zonas")
	}

	// LIVE evalúa AMBAS zonas Fibonacci con la MISMA función que el learner y el
	// backtest MTF (SelectConfluenceZone → SelectSetupFromEvidence): si LIVE
	// eligiera la zona por una ruta distinta, la validación OOS no describiría la
	// operativa real.
	matched, valid := SelectConfluenceZone(ep, len(ep)-1, es, fib, imbs, blocks, zones, model.Base.Confluence)
	if !valid || !matched.Valid || matched.Direction != candidate.Direction {
		trace.fail(TraceStageConfluence, "confluencia_fail", map[string]any{
			"direction": string(candidate.Direction),
			"setup":     string(candidate.SetupType),
			"fib_zone":  matched.FibZone,
			"score":     matched.Score,
			"valid":     valid && matched.Valid,
		})
		logReject(fmt.Sprintf("confluencia_fail direction=%s setup=%s trend1h=%s fib_zone=%d valid=%t", candidate.Direction, candidate.SetupType, ms.Trend, matched.FibZone, valid && matched.Valid))
		return domain.SignalEvent{}, false, trace, nil
	}
	trace.pass(TraceStageConfluence, map[string]any{"mode": string(matched.Mode), "score": matched.Score, "components": matched.Components, "fib_zone": matched.FibZone})

	zoneLow, zoneHigh := 0.0, 0.0
	switch matched.FibZone {
	case 1:
		zoneLow, zoneHigh = fib.Zone1Low, fib.Zone1High
	case 2:
		zoneLow, zoneHigh = fib.Zone2Low, fib.Zone2High
	default:
		trace.fail(TraceStageFibonacci, "fib_zone_invalida", map[string]any{"zone": matched.FibZone})
		logReject(fmt.Sprintf("fib_zone_invalida zone=%d", matched.FibZone))
		return domain.SignalEvent{}, false, trace, nil
	}
	pendingEntry := zoneHigh
	if candidate.Direction == domain.DirectionSell {
		pendingEntry = zoneLow
	}
	if pendingEntry <= 0 {
		trace.fail(TraceStageTradePlan, "pending_entry_invalida", nil)
		logReject("pending_entry_invalida")
		return domain.SignalEvent{}, false, trace, nil
	}
	plan, ok := buildLearningPlan(matched, fib, pendingEntry, es, blocks, model.Base.TradePlan)
	if !ok {
		trace.fail(TraceStageTradePlan, "trade_plan_fail", map[string]any{"direction": string(candidate.Direction), "setup": string(candidate.SetupType)})
		logReject(fmt.Sprintf("trade_plan_fail direction=%s setup=%s trend1h=%s fib_zone=%d", candidate.Direction, candidate.SetupType, ms.Trend, matched.FibZone))
		return domain.SignalEvent{}, false, trace, nil
	}
	trace.pass(TraceStageTradePlan, map[string]any{"stop_loss": plan.StopLoss, "take_profit": plan.TakeProfit, "pending_entry": pendingEntry})
	// El riesgo se aplica en el risk engine, no aquí: se registra como etapa
	// observada para que el trace sea completo sin duplicar la decisión.
	trace.skip(TraceStageRisk, "evaluado_por_risk_engine")

	ev := domain.SignalEvent{
		StrategyID: strategy.ID, Symbol: k.Symbol, Timeframe: k.Timeframe,
		Direction: candidate.Direction, Price: pendingEntry, BarTS: k.Start,
		Meta: map[string]any{
			"source": "investing_bulls", "setup": string(candidate.SetupType),
			"main_trend": string(ms.Trend), "stop_loss": plan.StopLoss,
			"take_profit": plan.TakeProfit, "entry_pending": true,
			"pending_entry": pendingEntry, "signal_market_price": k.Close, "entry_zone_low": zoneLow,
			"entry_zone_high": zoneHigh,
			// El TP parcial se toma en el máximo/mínimo previo (el objetivo del
			// plan): el paper cierra el 50% y protege el resto.
			"partial_tp": plan.TakeProfit,
		},
	}
	trace.Accepted = true
	trace.pass(TraceStageFinal, map[string]any{"direction": string(candidate.Direction), "pending_entry": pendingEntry})
	log.Printf("IB MTF diagnóstico strategy=%s bar=%s SIGNAL direction=%s setup=%s trend1h=%s fib_zone=%d zone=%.8f-%.8f pending=%.8f", strategy.ID, k.Start.UTC().Format(time.RFC3339), candidate.Direction, candidate.SetupType, ms.Trend, matched.FibZone, zoneLow, zoneHigh, pendingEntry)
	return ev, true, trace, nil
}
