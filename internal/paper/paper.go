package paper

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"tradingview-bot/internal/domain"
)

// Config define los costes simulados del paper trading.
type Config struct {
	// FeeRate es la comisión por operación (proporción del nocional).
	FeeRate float64
	// SlippageRate es el deslizamiento estimado (proporción del nocional).
	SlippageRate float64
}

func (c Config) withDefaults() Config {
	if c.FeeRate <= 0 {
		c.FeeRate = 0.001
	}
	if c.SlippageRate <= 0 {
		c.SlippageRate = 0.0002
	}
	return c
}

// Engine es el motor de paper trading: abre/cierra posiciones, vigila
// SL/TP con velas cerradas y calcula métricas.
type Engine struct {
	store    domain.PositionStore
	riskCtrl domain.PositionController
	cfg      Config
}

// New crea un Engine que persiste en store y delega las entradas en riskCtrl.
func New(store domain.PositionStore, riskCtrl domain.PositionController, cfg Config) *Engine {
	return &Engine{store: store, riskCtrl: riskCtrl, cfg: cfg.withDefaults()}
}

// OnSignal delega el flujo de riesgo para abrir/cerrar posiciones.
func (e *Engine) OnSignal(ctx context.Context, ev domain.SignalEvent) error {
	if e.riskCtrl == nil {
		return fmt.Errorf("controlador de riesgo no configurado")
	}
	return e.riskCtrl.OnSignal(ctx, ev)
}

// Close cierra una posición abierta aplicando fees, slippage, PnL bruto/neto,
// R múltiple y duración. El balance solo se actualiza al cerrar.
func (e *Engine) Close(ctx context.Context, id int64, exitPrice float64, exitTS time.Time, reason string) error {
	return e.closePosition(ctx, id, exitPrice, exitTS, domain.CloseOptions{Reason: reason})
}

// closePosition aplica fees/slippage estimados y delega el cierre en el store.
func (e *Engine) closePosition(ctx context.Context, id int64, exitPrice float64, exitTS time.Time, opts domain.CloseOptions) error {
	p, err := e.openPositionByID(ctx, id)
	if err != nil {
		return err
	}
	opts.EntryFee = p.EntryPrice * p.Quantity * e.cfg.FeeRate
	opts.ExitFee = exitPrice * p.Quantity * e.cfg.FeeRate
	opts.SlippageEntry = p.EntryPrice * p.Quantity * e.cfg.SlippageRate
	opts.SlippageExit = exitPrice * p.Quantity * e.cfg.SlippageRate
	_, err = e.store.ClosePosition(ctx, id, exitPrice, exitTS, opts)
	return err
}

// CheckStops revisa una vela cerrada y cierra posiciones cuyo SL/TP se tocó.
func (e *Engine) CheckStops(ctx context.Context, k domain.Kline) {
	if !k.Closed {
		return
	}
	open, err := e.store.OpenPositions(ctx)
	if err != nil {
		log.Printf("paper: leyendo posiciones abiertas: %v", err)
		return
	}
	for _, p := range open {
		if p.Symbol != k.Symbol || p.Timeframe != k.Timeframe {
			continue
		}
		e.checkPosition(ctx, p, k)
	}
}

func (e *Engine) checkPosition(ctx context.Context, p domain.Position, k domain.Kline) {
	var hitStop, hitTarget bool
	var stopPrice, targetPrice float64
	stopPrice = p.StopLoss
	targetPrice = p.TakeProfit

	switch p.Side {
	case domain.DirectionBuy:
		hitStop = p.StopLoss > 0 && k.Low <= p.StopLoss
		hitTarget = p.TakeProfit > 0 && k.High >= p.TakeProfit
	case domain.DirectionSell:
		hitStop = p.StopLoss > 0 && k.High >= p.StopLoss
		hitTarget = p.TakeProfit > 0 && k.Low <= p.TakeProfit
	}

	ambiguous := hitStop && hitTarget
	switch {
	case ambiguous:
		if err := e.closePosition(ctx, p.ID, stopPrice, k.Start, domain.CloseOptions{Reason: "stop", AmbiguousBar: true}); err != nil {
			log.Printf("paper: cerrando %d por stop (ambiguo): %v", p.ID, err)
			return
		}
		log.Printf("paper: posición %d cerrada (vino ambiguo → stop; SL=%v TP=%v vela L/H=%v/%v)", p.ID, p.StopLoss, p.TakeProfit, k.Low, k.High)
	case hitStop:
		if err := e.Close(ctx, p.ID, stopPrice, k.Start, "stop"); err != nil {
			log.Printf("paper: cerrando %d por stop: %v", p.ID, err)
		}
	case hitTarget:
		if err := e.Close(ctx, p.ID, targetPrice, k.Start, "take-profit"); err != nil {
			log.Printf("paper: cerrando %d por take-profit: %v", p.ID, err)
		}
	}
}

func (e *Engine) openPositionByID(ctx context.Context, id int64) (domain.Position, error) {
	open, err := e.store.OpenPositions(ctx)
	if err != nil {
		return domain.Position{}, err
	}
	for _, p := range open {
		if p.ID == id {
			return p, nil
		}
	}
	return domain.Position{}, fmt.Errorf("posición abierta %d no encontrada", id)
}

// Performance calcula las métricas de paper trading sobre trades cerrados.
func (e *Engine) Performance(ctx context.Context) (domain.Performance, error) {
	trades, err := e.store.ClosedPositions(ctx)
	if err != nil {
		return domain.Performance{}, err
	}
	acc, err := e.store.GetAccount(ctx)
	if err != nil {
		return domain.Performance{}, err
	}

	var perf domain.Performance
	perf.Trades = len(trades)

	var grossProfit, grossLoss float64
	var totalWins, totalLosses float64
	var winStreak, lossStreak, bestWinStreak, bestLossStreak int
	var netSeries []float64
	var rSeries []float64

	for _, t := range trades {
		netSeries = append(netSeries, t.NetPnL)
		rSeries = append(rSeries, t.RMultiple)
		if t.NetPnL > 0 {
			perf.Wins++
			totalWins += t.NetPnL
			grossProfit += t.GrossPnL
			winStreak++
			lossStreak = 0
			if winStreak > bestWinStreak {
				bestWinStreak = winStreak
			}
		} else {
			perf.Losses++
			totalLosses += -t.NetPnL
			grossLoss += -t.GrossPnL
			lossStreak++
			winStreak = 0
			if lossStreak > bestLossStreak {
				bestLossStreak = lossStreak
			}
		}
	}
	perf.ConsecutiveWins = bestWinStreak
	perf.ConsecutiveLosses = bestLossStreak

	if perf.Trades > 0 {
		perf.WinRate = float64(perf.Wins) / float64(perf.Trades) * 100
	}
	if grossLoss > 0 {
		perf.ProfitFactor = grossProfit / grossLoss
	} else if grossProfit > 0 {
		perf.ProfitFactor = math.Inf(1)
	}
	if perf.Wins > 0 {
		perf.AverageWin = totalWins / float64(perf.Wins)
	}
	if perf.Losses > 0 {
		perf.AverageLoss = totalLosses / float64(perf.Losses)
	}
	perf.TotalPnL = sum(netSeries)
	initial := acc.InitialBalance
	if initial <= 0 {
		initial = acc.PeakEquity
	}
	if initial > 0 {
		perf.ReturnPct = perf.TotalPnL / initial * 100
	}
	perf.MaxDrawdown = acc.MaxDrawdown * 100
	perf.Expectancy = mean(rSeries)
	perf.Sharpe = ratioWithDownside(netSeries, false) * math.Sqrt(float64(len(netSeries)))
	perf.Sortino = ratioWithDownside(netSeries, true) * math.Sqrt(float64(len(netSeries)))
	return perf, nil
}

func sum(vals []float64) float64 {
	var s float64
	for _, v := range vals {
		s += v
	}
	return s
}

func mean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	return sum(vals) / float64(len(vals))
}

// ratioWithDownside calcula mean/std; si downside es true usa solo negativos.
func ratioWithDownside(vals []float64, downside bool) float64 {
	data := vals
	if downside {
		var neg []float64
		for _, v := range vals {
			if v < 0 {
				neg = append(neg, v)
			}
		}
		data = neg
	}
	if len(data) == 0 {
		return 0
	}
	m := mean(data)
	var sq float64
	for _, v := range data {
		d := v - m
		sq += d * d
	}
	std := math.Sqrt(sq / float64(len(data)))
	if std == 0 {
		return 0
	}
	return m / std
}

var _ domain.PositionController = (*Engine)(nil)
var _ domain.PositionStore = (*Engine)(nil)

// OpenPosition implementa domain.PositionStore delegando en el store.
func (e *Engine) OpenPosition(ctx context.Context, p domain.Position) (int64, error) {
	return e.store.OpenPosition(ctx, p)
}

// ClosePosition implementa domain.PositionStore delegando en el store.
func (e *Engine) ClosePosition(ctx context.Context, id int64, exitPrice float64, exitTS time.Time, opts domain.CloseOptions) (domain.Position, error) {
	return e.store.ClosePosition(ctx, id, exitPrice, exitTS, opts)
}

// OpenPositions implementa domain.PositionStore.
func (e *Engine) OpenPositions(ctx context.Context) ([]domain.Position, error) {
	return e.store.OpenPositions(ctx)
}

// ClosedPositions implementa domain.PositionStore.
func (e *Engine) ClosedPositions(ctx context.Context) ([]domain.Position, error) {
	return e.store.ClosedPositions(ctx)
}

// GetAccount implementa domain.PositionStore.
func (e *Engine) GetAccount(ctx context.Context) (domain.Account, error) {
	return e.store.GetAccount(ctx)
}

// UpdateAccount implementa domain.PositionStore.
func (e *Engine) UpdateAccount(ctx context.Context, a domain.Account) error {
	return e.store.UpdateAccount(ctx, a)
}

var _ = time.Now
