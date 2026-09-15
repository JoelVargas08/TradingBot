package risk

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"tradingview-bot/internal/domain"
)

type Config struct {
	RiskPct          float64
	MinRR            float64
	MaxOpenPositions int
	KillSwitchPct    float64
	StartingBalance  float64
	DefaultStopPct   float64
}

func (c Config) withDefaults() Config {
	if c.RiskPct <= 0 {
		c.RiskPct = 0.01
	}
	if c.MinRR <= 0 {
		c.MinRR = 2.0
	}
	if c.MaxOpenPositions <= 0 {
		c.MaxOpenPositions = 3
	}
	if c.KillSwitchPct <= 0 {
		c.KillSwitchPct = 0.15
	}
	if c.StartingBalance <= 0 {
		c.StartingBalance = 10000
	}
	if c.DefaultStopPct <= 0 {
		c.DefaultStopPct = 0.03
	}
	return c
}

// Decision es la salida de la evaluación de riesgo (definida en domain).
type Decision = domain.Decision

type Manager struct {
	cfg   Config
	store domain.PositionStore
}

func New(store domain.PositionStore, cfg Config) *Manager {
	return &Manager{cfg: cfg.withDefaults(), store: store}
}

var ErrClosed = errors.New("posiciones deshabilitadas por kill-switch")

func (m *Manager) EvaluateSignal(ctx context.Context, ev domain.SignalEvent) (Decision, error) {
	return m.Evaluate(ctx, ev)
}

func (m *Manager) Evaluate(ctx context.Context, ev domain.SignalEvent) (Decision, error) {
	acc, err := m.store.GetAccount(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		acc = domain.Account{Balance: m.cfg.StartingBalance, PeakEquity: m.cfg.StartingBalance}
	} else if err != nil {
		return Decision{}, fmt.Errorf("obteniendo cuenta: %w", err)
	}
	if acc.PeakEquity <= 0 {
		acc.PeakEquity = acc.Balance
	}

	d := Decision{
		Side:       ev.Direction,
		EntryPrice: ev.Price,
		Drawdown:   drawdown(acc.Balance, acc.PeakEquity),
	}

	if math.Abs(d.Drawdown) >= m.cfg.KillSwitchPct {
		d.Allowed = false
		d.Reason = fmt.Sprintf("kill-switch: drawdown %.1f%% ≥ %.1f%%", math.Abs(d.Drawdown)*100, m.cfg.KillSwitchPct*100)
		return d, ErrClosed
	}

	open, err := m.store.OpenPositions(ctx)
	if err != nil {
		return Decision{}, fmt.Errorf("listando posiciones abiertas: %w", err)
	}
	d.OpenCount = len(open)
	if d.OpenCount >= m.cfg.MaxOpenPositions {
		d.Allowed = false
		d.Reason = fmt.Sprintf("límite alcanzado: %d/%d posiciones abiertas", d.OpenCount, m.cfg.MaxOpenPositions)
		return d, nil
	}

	d.StopLoss = metaStop(ev, d.EntryPrice, m.cfg.DefaultStopPct)
	d.TakeProfit = metaTarget(ev, d.EntryPrice, d.StopLoss, m.cfg.MinRR)

	riskPerUnit := math.Abs(d.EntryPrice - d.StopLoss)
	if riskPerUnit <= 0 {
		d.Allowed = false
		d.Reason = "stop inválido (distancia cero)"
		return d, nil
	}

	rr := ratioRR(d.EntryPrice, d.StopLoss, d.TakeProfit)
	if rr < m.cfg.MinRR {
		d.Allowed = false
		d.Reason = fmt.Sprintf("R/R %.1f:1 menor que mínimo %.1f:1", rr, m.cfg.MinRR)
		return d, nil
	}

	d.RiskAmount = acc.Balance * m.cfg.RiskPct
	d.Quantity = d.RiskAmount / riskPerUnit
	d.Allowed = true
	d.Reason = fmt.Sprintf("riesgo $%.2f (%.1f%%), R/R %.1f:1", d.RiskAmount, m.cfg.RiskPct*100, rr)
	return d, nil
}

func (m *Manager) OnSignal(ctx context.Context, ev domain.SignalEvent) error {
	open, err := m.store.OpenPositions(ctx)
	if err != nil {
		return fmt.Errorf("listando posiciones abiertas: %w", err)
	}
	for _, p := range open {
		if p.StrategyID != ev.StrategyID || p.Symbol != ev.Symbol || p.Timeframe != ev.Timeframe {
			continue
		}
		if p.Side == ev.Direction {
			return nil
		}
		if _, err := m.store.ClosePosition(ctx, p.ID, ev.Price, time.Now(), domain.CloseOptions{
			Reason: "signal-contrary",
		}); err != nil {
			return fmt.Errorf("cerrando posición %d: %w", p.ID, err)
		}
	}

	d, err := m.Evaluate(ctx, ev)
	if err != nil {
		return err
	}
	if !d.Allowed {
		return fmt.Errorf("señal rechazada: %s", d.Reason)
	}

	_, err = m.store.OpenPosition(ctx, domain.Position{
		StrategyID: ev.StrategyID,
		Symbol:     ev.Symbol,
		Timeframe:  ev.Timeframe,
		Side:       d.Side,
		EntryTS:    time.Now(),
		EntryPrice: d.EntryPrice,
		StopLoss:   d.StopLoss,
		TakeProfit: d.TakeProfit,
		Quantity:   d.Quantity,
		RiskAmount: d.RiskAmount,
		Status:     domain.PositionOpen,
	})
	if err != nil {
		return fmt.Errorf("abriendo posición: %w", err)
	}
	return nil
}

func metaStop(ev domain.SignalEvent, entry, defaultPct float64) float64 {
	if sl, ok := metaFloat(ev.Meta, domain.MetaKeyStopLoss); ok && sl > 0 {
		return sl
	}
	if ev.Direction == domain.DirectionBuy {
		return entry * (1 - defaultPct)
	}
	return entry * (1 + defaultPct)
}

func metaTarget(ev domain.SignalEvent, entry, stop, minRR float64) float64 {
	if tp, ok := metaFloat(ev.Meta, domain.MetaKeyTakeProfit); ok && tp > 0 {
		return tp
	}
	risk := math.Abs(entry - stop)
	if ev.Direction == domain.DirectionBuy {
		return entry + risk*minRR
	}
	return entry - risk*minRR
}

func ratioRR(entry, stop, target float64) float64 {
	risk := math.Abs(entry - stop)
	if risk <= 0 {
		return 0
	}
	reward := math.Abs(target - entry)
	return reward / risk
}

func drawdown(balance, peak float64) float64 {
	if peak <= 0 {
		return 0
	}
	return (balance - peak) / peak
}

func metaFloat(meta map[string]any, key string) (float64, bool) {
	v, ok := meta[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}
