package paper

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sync"
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
// SL/TP con velas cerradas, actualiza equity intratrade y calcula métricas.
type pendingEntry struct {
	Key string
	StrategyID string
	Symbol string
	Timeframe string
	Side domain.Direction
	Price float64
	StopLoss float64
	TakeProfit float64
	Quantity float64
	RiskAmount float64
	BarTS time.Time
}

type Engine struct {
	store    domain.PositionStore
	candles  domain.CandleStore
	riskCtrl domain.RiskDecider
	cfg      Config
	marks    domain.MarkStore
	mu       sync.Mutex
	pending  map[string][]pendingEntry
	entryGate func() bool
}

// New crea un Engine que persiste en store y delega la decisión en riskCtrl.
func New(store domain.PositionStore, riskCtrl domain.RiskDecider, cfg Config) *Engine {
	var candles domain.CandleStore
	if cs, ok := store.(domain.CandleStore); ok {
		candles = cs
	}
	return &Engine{
		store:    store,
		candles:  candles,
		riskCtrl: riskCtrl,
		cfg:      cfg.withDefaults(),
		pending:  make(map[string][]pendingEntry),
	}
}

// SetMarkStore enlaza la persistencia de últimos precios (marks) para poder
// valorar el PnL no realizado de todas las posiciones abiertas.
func (e *Engine) SetMarkStore(ms domain.MarkStore) *Engine {
	e.marks = ms
	return e
}

func (e *Engine) SetSessionGate(g interface{ IsActive() bool }) *Engine {
	if g == nil { e.entryGate = nil } else { e.entryGate = g.IsActive }
	return e
}

func (e *Engine) SetEntryGate(g func() bool) *Engine {
	e.entryGate = g
	return e
}

func (e *Engine) OnCandle(ctx context.Context, k domain.Kline) {
	if !k.Closed { return }
	if e.entryGate != nil && !e.entryGate() { return }
	e.mu.Lock()
	var fills []pendingEntry
	for key, entries := range e.pending {
		if len(entries) == 0 { delete(e.pending, key); continue }
		remaining := entries[:0]
		for _, p := range entries {
			if p.Symbol != k.Symbol || p.Timeframe != k.Timeframe || !k.Start.After(p.BarTS) { remaining = append(remaining, p); continue }
			touched := (p.Side == domain.DirectionBuy && k.Low <= p.Price) || (p.Side == domain.DirectionSell && k.High >= p.Price)
			if touched { fills = append(fills, p) } else { remaining = append(remaining, p) }
		}
		if len(remaining) == 0 { delete(e.pending, key) } else { e.pending[key] = remaining }
	}
	e.mu.Unlock()
	for _, p := range fills {
		ev := domain.SignalEvent{StrategyID:p.StrategyID, Symbol:p.Symbol, Timeframe:p.Timeframe, Direction:p.Side, Price:p.Price, BarTS:k.Start, Meta:map[string]any{domain.MetaKeyStopLoss:p.StopLoss, domain.MetaKeyTakeProfit:p.TakeProfit, domain.MetaKeySetup:"investing_bulls_limit_fill"}}
		if err := e.openPending(ctx, ev, p.Quantity, p.RiskAmount); err != nil { log.Printf("paper: limit IB %.8f no ejecutada: %v", p.Price, err); continue }
		e.mu.Lock()
		for key, entries := range e.pending {
			filtered := entries[:0]
			for _, other := range entries { if other.StrategyID != p.StrategyID || other.Symbol != p.Symbol || other.Timeframe != p.Timeframe || other.Side != p.Side { filtered = append(filtered, other) } }
			if len(filtered)==0 { delete(e.pending,key) } else { e.pending[key]=filtered }
		}
		e.mu.Unlock()
		log.Printf("paper: limit IB ejecutada %s %s %.8f", p.Symbol, p.Side, p.Price)
		break
	}
}

func (e *Engine) openPending(ctx context.Context, ev domain.SignalEvent, quantity, riskAmount float64) error {
	if e.riskCtrl == nil { return fmt.Errorf("controlador de riesgo no configurado") }
	decision, err := e.riskCtrl.EvaluateSignal(ctx, ev)
	if err != nil { return err }
	if !decision.Allowed { return fmt.Errorf("señal rechazada: %s", decision.Reason) }
	if quantity > 0 { decision.Quantity = quantity }
	if riskAmount > 0 { decision.RiskAmount = riskAmount }
	_, err = e.store.OpenPosition(ctx, domain.Position{StrategyID:ev.StrategyID,Symbol:ev.Symbol,Timeframe:ev.Timeframe,Side:decision.Side,EntryTS:time.Now(),EntryPrice:decision.EntryPrice,StopLoss:decision.StopLoss,TakeProfit:decision.TakeProfit,Quantity:decision.Quantity,RiskAmount:decision.RiskAmount,Status:domain.PositionOpen})
	if err != nil { return fmt.Errorf("abriendo posición límite: %w",err) }
	return nil
}

// OnSignal aplica el flujo de ejecución paper sobre una señal:
// cierra posiciones contrarias con costes, ignora mismas direcciones,
// evalúa riesgo y abre posición si está permitido.
func (e *Engine) OnSignal(ctx context.Context, ev domain.SignalEvent) error {
	if e.riskCtrl == nil { return fmt.Errorf("controlador de riesgo no configurado") }
	if e.entryGate != nil && !e.entryGate() { return nil }

	zoneLow, hasLow := paperMetaFloat(ev.Meta, "entry_zone_low")
	zoneHigh, hasHigh := paperMetaFloat(ev.Meta, "entry_zone_high")
	twoLimits := hasLow && hasHigh && zoneLow > 0 && zoneHigh > zoneLow

	open, err := e.store.OpenPositions(ctx)
	if err != nil { return fmt.Errorf("listando posiciones abiertas: %w", err) }
	for _, p := range open {
		if p.StrategyID != ev.StrategyID || p.Symbol != ev.Symbol || p.Timeframe != ev.Timeframe { continue }
		if p.Side == ev.Direction { return nil }
		exitPrice := ev.Price
		if market, ok := paperMetaFloat(ev.Meta, "signal_market_price"); ok && market > 0 { exitPrice = market }
		if err := e.closePosition(ctx, p.ID, exitPrice, time.Now(), domain.CloseOptions{Reason:"signal-contrary"}); err != nil {
			return fmt.Errorf("cerrando posición %d (señal contraria): %w", p.ID, err)
		}
	}

	key := ev.IdempotencyKey()
	e.mu.Lock()
	if len(e.pending[key]) > 0 { e.mu.Unlock(); return nil }
	for k, entries := range e.pending {
		if len(entries)>0 && entries[0].StrategyID==ev.StrategyID && entries[0].Symbol==ev.Symbol && entries[0].Timeframe==ev.Timeframe && entries[0].Side!=ev.Direction {
			delete(e.pending,k)
		}
	}
	e.mu.Unlock()

	decision, err := e.riskCtrl.EvaluateSignal(ctx, ev)
	if err != nil { return err }
	if !decision.Allowed { return fmt.Errorf("señal rechazada: %s", decision.Reason) }

	if !twoLimits {
		_, err = e.store.OpenPosition(ctx, domain.Position{StrategyID:ev.StrategyID,Symbol:ev.Symbol,Timeframe:ev.Timeframe,Side:decision.Side,EntryTS:time.Now(),EntryPrice:decision.EntryPrice,StopLoss:decision.StopLoss,TakeProfit:decision.TakeProfit,Quantity:decision.Quantity,RiskAmount:decision.RiskAmount,Status:domain.PositionOpen})
		if err != nil { return fmt.Errorf("abriendo posición: %w",err) }
		return nil
	}

	first, second := zoneHigh, zoneLow
	if ev.Direction == domain.DirectionSell { first, second = zoneLow, zoneHigh }
	halfQty, halfRisk := decision.Quantity/2, decision.RiskAmount/2
	entries := []pendingEntry{
		{Key:key+"|1",StrategyID:ev.StrategyID,Symbol:ev.Symbol,Timeframe:ev.Timeframe,Side:ev.Direction,Price:first,StopLoss:decision.StopLoss,TakeProfit:decision.TakeProfit,Quantity:halfQty,RiskAmount:halfRisk,BarTS:ev.BarTS},
		{Key:key+"|2",StrategyID:ev.StrategyID,Symbol:ev.Symbol,Timeframe:ev.Timeframe,Side:ev.Direction,Price:second,StopLoss:decision.StopLoss,TakeProfit:decision.TakeProfit,Quantity:halfQty,RiskAmount:halfRisk,BarTS:ev.BarTS},
	}
	// Both entries remain armed and are filled independently. The first level
	// follows the natural approach into the zone: high boundary for longs,
	// low boundary for shorts.
	e.mu.Lock()
	e.pending[key] = entries
	e.mu.Unlock()
	log.Printf("paper: zona armada %s %s %.8f-%.8f con 2 límites",ev.Symbol,ev.Direction,zoneLow,zoneHigh)
	return nil
}
// Recover reconstruye el equity al arrancar a partir del estado persistido.
func (e *Engine) Recover(ctx context.Context) error {
	acc, err := e.store.GetAccount(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("recover: leyendo cuenta: %w", err)
	}
	open, err := e.store.OpenPositions(ctx)
	if err != nil {
		return fmt.Errorf("recover: leyendo posiciones abiertas: %w", err)
	}
	prices := make(map[string]float64)
	if e.marks != nil {
		marks, err := e.marks.Marks(ctx)
		if err != nil {
			return fmt.Errorf("recover: leyendo marks: %w", err)
		}
		for _, mk := range marks {
			if mk.Price > 0 {
				prices[markKey(mk.Symbol, mk.Timeframe)] = mk.Price
			}
		}
	}
	unrealized := 0.0
	for _, p := range open {
		price, ok := prices[markKey(p.Symbol, p.Timeframe)]
		if !ok {
			continue
		}
		if p.Side == domain.DirectionBuy {
			unrealized += (price - p.EntryPrice) * p.Quantity
		} else {
			unrealized += (p.EntryPrice - price) * p.Quantity
		}
	}
	acc.UnrealizedPnL = unrealized
	acc.Equity = acc.Balance + unrealized
	if acc.PeakEquity < acc.Equity {
		acc.PeakEquity = acc.Equity
	}
	if acc.PeakEquity > 0 {
		dd := (acc.PeakEquity - acc.Equity) / acc.PeakEquity
		if dd > acc.MaxDrawdown {
			acc.MaxDrawdown = dd
		}
	}
	acc.UpdatedAt = time.Now()
	if err := e.store.UpdateAccount(ctx, acc); err != nil {
		return fmt.Errorf("recover: actualizando cuenta: %w", err)
	}
	log.Printf("paper recovery: %d posiciones abiertas, balance=%.2f equity=%.2f unrealized=%.2f", len(open), acc.Balance, acc.Equity, acc.UnrealizedPnL)
	return nil
}

// ReconcileOpenPositions reevalúa las posiciones que quedaron abiertas
// durante una interrupción usando las velas ya persistidas. Esto evita perder
// un SL/TP ocurrido mientras un runner efímero estaba apagado.
func (e *Engine) ReconcileOpenPositions(ctx context.Context) error {
	open, err := e.store.OpenPositions(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: leyendo posiciones: %w", err)
	}
	for _, p := range open {
		if e.candles == nil {
			return fmt.Errorf("reconcile %d: store no implementa CandleStore", p.ID)
		}
		candles, err := e.candles.RecentCandles(ctx, p.Symbol, p.Timeframe, 5000)
		if err != nil {
			return fmt.Errorf("reconcile %d: leyendo velas: %w", p.ID, err)
		}
		for _, k := range candles {
			if k.Start.Before(p.EntryTS) {
				continue
			}
			// CheckStops vuelve a consultar las posiciones abiertas, por lo que
			// después de cerrar esta posición no se vuelve a procesar.
			e.checkPosition(ctx, p, k)
			stillOpen, err := e.store.OpenPositions(ctx)
			if err != nil {
				return fmt.Errorf("reconcile %d: verificando posición: %w", p.ID, err)
			}
			found := false
			for _, current := range stillOpen {
				if current.ID == p.ID {
					found = true
					break
				}
			}
			if !found {
				break
			}
		}
	}
	if len(open) > 0 {
		log.Printf("paper recovery: reconciliadas %d posiciones abiertas contra velas persistidas", len(open))
	}
	return nil
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
	if !k.Closed { return }
	if e.entryGate == nil || e.entryGate() { e.fillPending(ctx,k) }
	open, err := e.store.OpenPositions(ctx)
	if err != nil { log.Printf("paper: leyendo posiciones abiertas: %v",err); return }
	for _, p := range open { if p.Symbol==k.Symbol && p.Timeframe==k.Timeframe { e.checkPosition(ctx,p,k) } }
}

func (e *Engine) fillPending(ctx context.Context,k domain.Kline) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for key, entries := range e.pending {
		remaining := entries[:0]
		for _, pe := range entries {
			if pe.Symbol!=k.Symbol || pe.Timeframe!=k.Timeframe || k.Low>pe.Price || pe.Price>k.High {
				remaining=append(remaining,pe); continue
			}
			_, err := e.store.OpenPosition(ctx,domain.Position{StrategyID:pe.StrategyID,Symbol:pe.Symbol,Timeframe:pe.Timeframe,Side:pe.Side,EntryTS:k.Start,EntryPrice:pe.Price,StopLoss:pe.StopLoss,TakeProfit:pe.TakeProfit,Quantity:pe.Quantity,RiskAmount:pe.RiskAmount,Status:domain.PositionOpen})
			if err!=nil { log.Printf("paper: ejecutando límite %.8f: %v",pe.Price,err); remaining=append(remaining,pe); continue }
			log.Printf("paper: límite ejecutado %s %s price=%.8f qty=%.8f",pe.Symbol,pe.Side,pe.Price,pe.Quantity)
		}
		if len(remaining)==0 { delete(e.pending,key) } else { e.pending[key]=remaining }
	}
}

func (e *Engine) CancelPending() {
	e.mu.Lock()
	defer e.mu.Unlock()
	clear(e.pending)
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

// MarkPrice actualiza el equity de la cuenta con el PnL no realizado de TODAS
// las posiciones abiertas. Mantiene los últimos precios por símbolo+timeframe
// (persistiéndolos en MarkStore si está disponible) para que al llegar una
// vela de un símbolo no desaparezca el PnL de otros.
func (e *Engine) MarkPrice(ctx context.Context, k domain.Kline) error {
	open, err := e.store.OpenPositions(ctx)
	if err != nil {
		return fmt.Errorf("mark-price: listando posiciones: %w", err)
	}
	acc, err := e.store.GetAccount(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("mark-price: leyendo cuenta: %w", err)
	}

	// Guardar el mark del símbolo/timeframe actual (para sobrevivir reinicios).
	if e.marks != nil {
		if err := e.marks.SaveMark(ctx, domain.Mark{Symbol: k.Symbol, Timeframe: k.Timeframe, Price: k.Close, TS: k.Start}); err != nil {
			return fmt.Errorf("mark-price: guardando mark: %w", err)
		}
	}

	// Construir el mapa de últimos precios por símbolo+timeframe: primero los
	// persistidos y luego el precio de la vela actual (más reciente).
	prices := make(map[string]float64)
	if e.marks != nil {
		marks, err := e.marks.Marks(ctx)
		if err != nil {
			return fmt.Errorf("mark-price: leyendo marks: %w", err)
		}
		for _, mk := range marks {
			prices[markKey(mk.Symbol, mk.Timeframe)] = mk.Price
		}
	}
	prices[markKey(k.Symbol, k.Timeframe)] = k.Close

	unrealized := 0.0
	for _, p := range open {
		price, ok := prices[markKey(p.Symbol, p.Timeframe)]
		if !ok {
			continue // sin mark aún, se valorará cuando llegue la primera vela
		}
		if p.Side == domain.DirectionBuy {
			unrealized += (price - p.EntryPrice) * p.Quantity
		} else {
			unrealized += (p.EntryPrice - price) * p.Quantity
		}
	}
	acc.UnrealizedPnL = unrealized
	acc.Equity = acc.Balance + unrealized
	if acc.Equity > acc.PeakEquity {
		acc.PeakEquity = acc.Equity
	}
	if acc.PeakEquity > 0 {
		dd := (acc.PeakEquity - acc.Equity) / acc.PeakEquity
		if dd > acc.MaxDrawdown {
			acc.MaxDrawdown = dd
		}
	}
	acc.UpdatedAt = time.Now()
	return e.store.UpdateAccount(ctx, acc)
}

func markKey(symbol, timeframe string) string {
	return symbol + "|" + timeframe
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
	var winRS, lossRS []float64

	for _, t := range trades {
		netSeries = append(netSeries, t.NetPnL)
		if t.NetPnL > 0 {
			perf.Wins++
			totalWins += t.NetPnL
			grossProfit += t.NetPnL
			winRS = append(winRS, t.RMultiple)
			winStreak++
			lossStreak = 0
			if winStreak > bestWinStreak {
				bestWinStreak = winStreak
			}
		} else {
			perf.Losses++
			totalLosses += -t.NetPnL
			grossLoss += -t.NetPnL
			lossRS = append(lossRS, t.RMultiple)
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
	// Profit Factor y PnL sobre valores netos (tras comisiones y slippage).
	if grossLoss > 0 {
		perf.ProfitFactor = grossProfit / grossLoss
	} else if grossProfit > 0 {
		perf.ProfitFactor = math.Inf(1)
	}
	if perf.Wins > 0 {
		perf.AverageWin = totalWins / float64(perf.Wins)
		perf.AverageWinR = mean(winRS)
	}
	if perf.Losses > 0 {
		perf.AverageLoss = totalLosses / float64(perf.Losses)
		perf.AverageLossR = mean(lossRS)
	}
	perf.TotalPnL = sum(netSeries)
	if perf.Trades > 0 {
		perf.ExpectancyPnL = perf.TotalPnL / float64(perf.Trades)
	}
	winRate := 0.0
	lossRate := 0.0
	if perf.Trades > 0 {
		winRate = float64(perf.Wins) / float64(perf.Trades)
		lossRate = float64(perf.Losses) / float64(perf.Trades)
	}
	perf.ExpectancyR = winRate*perf.AverageWinR - lossRate*perf.AverageLossR
	initial := acc.InitialBalance
	if initial <= 0 {
		initial = acc.PeakEquity
	}
	if initial > 0 {
		perf.ReturnPct = perf.TotalPnL / initial * 100
	}
	perf.MaxDrawdown = acc.MaxDrawdown * 100
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


func paperMetaFloat(meta map[string]any, key string) (float64, bool) {
	v, ok := meta[key]
	if !ok { return 0, false }
	switch n := v.(type) {
	case float64: return n, true
	case int: return float64(n), true
	case int64: return float64(n), true
	default: return 0, false
	}
}


func metaFloat(meta map[string]any, key string) (float64, bool) {
	v, ok := meta[key]
	if !ok { return 0, false }
	switch n := v.(type) {
	case float64: return n, true
	case int: return float64(n), true
	case int64: return float64(n), true
	default: return 0, false
	}
}
