package risk

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/observability"
)

type Config struct {
	RiskPct          float64
	MinRR            float64
	MaxOpenPositions int
	KillSwitchPct    float64
	StartingBalance  float64
	DefaultStopPct   float64

	// MaxDailyLossPct bloquea nuevas entradas el resto del día cuando las
	// pérdidas realizadas superan ese % del balance inicial.
	MaxDailyLossPct float64

	// MaxDrawdownPct es un gate SUAVE: bloquea nuevas entradas por drawdown
	// sin cerrar posiciones (el kill-switch hard es KillSwitchPct).
	MaxDrawdownPct float64

	// KillSwitchPolicy decide qué hacer con las posiciones abiertas cuando el
	// kill-switch se dispara: "close_all" las cierra, "keep" las conserva
	// (el SL/TP del broker sigue protegiéndolas).
	KillSwitchPolicy string
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
	if c.MaxDailyLossPct <= 0 {
		c.MaxDailyLossPct = 0.05
	}
	if c.MaxDrawdownPct <= 0 {
		c.MaxDrawdownPct = 0.10
	}
	if c.KillSwitchPolicy == "" {
		c.KillSwitchPolicy = "close_all"
	}
	return c
}

// Decision es la salida de la evaluación de riesgo (definida en domain).
type Decision = domain.Decision

type Manager struct {
	cfg   Config
	store domain.PositionStore
	marks domain.MarkStore

	mu       sync.Mutex
	killed   bool
	killWhen time.Time
	events   []KillSwitchEvent
	now      func() time.Time
}

func New(store domain.PositionStore, cfg Config) *Manager {
	return &Manager{cfg: cfg.withDefaults(), store: store, now: time.Now}
}

var ErrClosed = errors.New("posiciones deshabilitadas por kill-switch")

// KillSwitchEvent registra cada vez que el kill-switch se dispara: cuándo,
// qué drawdown lo activó, la política aplicada y qué posiciones se cerraron.
// Nunca contiene secretos ni credenciales.
type KillSwitchEvent struct {
	When      time.Time
	Drawdown  float64
	Policy    string
	ClosedIDs []int64
}

// SetClock permite fijar el reloj para testear gates por día/drawdown.
func (m *Manager) SetClock(fn func() time.Time) {
	if fn != nil {
		m.now = fn
	}
}

// SetMarkStore opcional permite al kill-switch "close_all" cerrar posiciones
// al último mark conocido en vez del precio de entrada.
func (m *Manager) SetMarkStore(ms domain.MarkStore) {
	m.marks = ms
}

func (m *Manager) EvaluateSignal(ctx context.Context, ev domain.SignalEvent) (Decision, error) {
	return m.Evaluate(ctx, ev)
}

func (m *Manager) Evaluate(ctx context.Context, ev domain.SignalEvent) (Decision, error) {
	// Kill-switch pegajoso: una vez disparado, bloquee todas las entradas hasta
	// que el proceso se reinicie (reinicio = re-evaluación desde WEEX).
	m.mu.Lock()
	killed := m.killed
	m.mu.Unlock()
	if killed {
		d := Decision{Allowed: false, Killed: true, Side: ev.Direction, EntryPrice: ev.Price}
		d.Reason = fmt.Sprintf("kill-switch: %s", ErrClosed.Error())
		return d, ErrClosed
	}

	acc, err := m.store.GetAccount(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		acc = domain.Account{
			Balance:        m.cfg.StartingBalance,
			Equity:         m.cfg.StartingBalance,
			InitialBalance: m.cfg.StartingBalance,
			PeakEquity:     m.cfg.StartingBalance,
		}
	} else if err != nil {
		return Decision{}, fmt.Errorf("obteniendo cuenta: %w", err)
	}
	if acc.PeakEquity <= 0 {
		acc.PeakEquity = acc.Balance
	}
	// Equity incluye el PnL no realizado; para el kill-switch usamos equity.
	eq := acc.Equity
	if eq <= 0 {
		eq = acc.Balance
	}

	d := Decision{
		Side:       ev.Direction,
		EntryPrice: ev.Price,
		Drawdown:   drawdown(eq, acc.PeakEquity),
	}

	dd := math.Abs(d.Drawdown)
	// 1) Kill-switch HARDO: pegajoso y, según política, cierra posiciones.
	if dd >= m.cfg.KillSwitchPct {
		return m.killSwitch(ctx, d, dd, acc)
	}

	// 2) Gate suave por drawdown: bloquea entradas sin cerrar nada.
	if dd >= m.cfg.MaxDrawdownPct {
		d.Allowed = false
		d.Reason = fmt.Sprintf("drawdown %.1f%% ≥ gate suave %.1f%%: sin nuevas entradas", dd*100, m.cfg.MaxDrawdownPct*100)
		return d, nil
	}

	// 3) Límite de pérdida diaria (realizada hoy).
	dayLoss, err := m.dayLoss(ctx)
	if err != nil {
		return Decision{}, fmt.Errorf("calculando pérdida diaria: %w", err)
	}
	if dayLoss <= -m.cfg.MaxDailyLossPct*accountInitial(&acc) {
		d.Allowed = false
		d.Reason = fmt.Sprintf("pérdida diaria $%.2f ≥ límite $%.2f: sin nuevas entradas", dayLoss, -m.cfg.MaxDailyLossPct*accountInitial(&acc))
		return d, nil
	}

	d.DailyLoss = dayLoss

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
	d.Quantity = PositionQuantity(d.RiskAmount, d.EntryPrice, d.StopLoss)
	d.Allowed = true
	d.Reason = fmt.Sprintf("riesgo $%.2f (%.1f%%), R/R %.1f:1", d.RiskAmount, m.cfg.RiskPct*100, rr)
	return d, nil
}

func accountInitial(acc *domain.Account) float64 {
	if acc.InitialBalance > 0 {
		return acc.InitialBalance
	}
	if acc.Balance > 0 {
		return acc.Balance
	}
	return 0
}

// killSwitch activa el estado pegajoso, registra el evento y, según la
// política, cierra las posiciones abiertas al último mark conocido (o al
// precio de entrada si no hay mark). Devuelve siempre ErrClosed para bloquear
// nuevas operaciones reales.
func (m *Manager) killSwitch(ctx context.Context, d Decision, dd float64, acc domain.Account) (Decision, error) {
	m.mu.Lock()
	first := !m.killed
	m.killed = true
	if first {
		m.killWhen = m.now()
	}
	m.mu.Unlock()

	d.Allowed = false
	d.Killed = true
	d.Reason = fmt.Sprintf("kill-switch: drawdown %.1f%% ≥ %.1f%%", dd*100, m.cfg.KillSwitchPct*100)

	ev := KillSwitchEvent{When: m.now(), Drawdown: dd, Policy: m.cfg.KillSwitchPolicy}

	if m.cfg.KillSwitchPolicy == "close_all" {
		open, err := m.store.OpenPositions(ctx)
		if err != nil {
			return d, fmt.Errorf("kill-switch: listando posiciones: %w", err)
		}
		for _, p := range open {
			price := m.markPrice(ctx, p)
			if _, err := m.store.ClosePosition(ctx, p.ID, price, m.now(), domain.CloseOptions{Reason: "kill-switch"}); err != nil {
				return d, fmt.Errorf("kill-switch: cerrando posición %d: %w", p.ID, err)
			}
			ev.ClosedIDs = append(ev.ClosedIDs, p.ID)
		}
	}

	m.mu.Lock()
	m.events = append(m.events, ev)
	m.mu.Unlock()

	observability.Log(observability.KillSwitch, "first", first, "drawdown_pct", dd*100, "policy", ev.Policy, "closed", len(ev.ClosedIDs))
	log.Printf("KILL_SWITCH first=%v policy=%s drawdown=%.2f%% closed=%d", first, ev.Policy, dd*100, len(ev.ClosedIDs))
	return d, ErrClosed
}

// dayLoss calcula la pérdida realizada de hoy (NetPnL acumulado de posiciones
// cerradas con exit_ts en el día actual según m.now()).
func (m *Manager) dayLoss(ctx context.Context) (float64, error) {
	closed, err := m.store.ClosedPositions(ctx)
	if err != nil {
		return 0, err
	}
	now := m.now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var total float64
	for _, p := range closed {
		if p.ExitTS.Before(startOfDay) {
			continue
		}
		total += p.NetPnL
	}
	return total, nil
}

// markPrice devuelve el último precio conocido de la posición (MarkStore) o,
// si no hay mark, el precio de entrada, para poder cerrarla en el kill-switch
// sin depender de un feed en vivo.
func (m *Manager) markPrice(ctx context.Context, p domain.Position) float64 {
	if m.marks != nil {
		marks, err := m.marks.Marks(ctx)
		if err == nil {
			for _, mk := range marks {
				if mk.Symbol == p.Symbol && mk.Timeframe == p.Timeframe && mk.Price > 0 {
					return mk.Price
				}
			}
		}
	}
	return p.EntryPrice
}

// KillSwitchEvents expone el historial de eventos (observabilidad).
func (m *Manager) KillSwitchEvents() []KillSwitchEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]KillSwitchEvent, len(m.events))
	copy(out, m.events)
	return out
}

// PositionQuantity dimensiona la cantidad según riesgo por unidad.
func PositionQuantity(riskAmount, entryPrice, stopLoss float64) float64 {
	riskPerUnit := math.Abs(entryPrice - stopLoss)
	if riskPerUnit <= 0 {
		return 0
	}
	return riskAmount / riskPerUnit
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
