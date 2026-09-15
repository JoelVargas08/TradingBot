package risk

import (
	"context"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

type memStore struct {
	positions []domain.Position
	account   domain.Account
	nextID    int64
}

func (m *memStore) OpenPosition(_ context.Context, p domain.Position) (int64, error) {
	for _, q := range m.positions {
		if q.StrategyID == p.StrategyID && q.Symbol == p.Symbol && q.Timeframe == p.Timeframe && q.Status == domain.PositionOpen {
			return 0, domain.ErrDuplicate
		}
	}
	m.nextID++
	p.ID = m.nextID
	m.positions = append(m.positions, p)
	return p.ID, nil
}

func (m *memStore) ClosePosition(_ context.Context, id int64, exitPrice float64, exitTS time.Time, opts domain.CloseOptions) (domain.Position, error) {
	for i, p := range m.positions {
		if p.ID == id {
			p.Status = domain.PositionClosed
			p.ExitPrice = exitPrice
			p.ExitTS = exitTS
			p.ExitReason = opts.Reason
			if p.Side == domain.DirectionBuy {
				p.GrossPnL = (exitPrice - p.EntryPrice) * p.Quantity
			} else {
				p.GrossPnL = (p.EntryPrice - exitPrice) * p.Quantity
			}
			p.NetPnL = p.GrossPnL - (opts.EntryFee + opts.ExitFee + opts.SlippageEntry + opts.SlippageExit)
			p.PnL = p.NetPnL
			m.account.Balance += p.NetPnL
			if m.account.Balance > m.account.PeakEquity {
				m.account.PeakEquity = m.account.Balance
			}
			m.positions[i] = p
			return p, nil
		}
	}
	return domain.Position{}, domain.ErrNotFound
}

func (m *memStore) ClosedPositions(_ context.Context) ([]domain.Position, error) {
	var out []domain.Position
	for _, p := range m.positions {
		if p.Status == domain.PositionClosed {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *memStore) OpenPositions(_ context.Context) ([]domain.Position, error) {
	var out []domain.Position
	for _, p := range m.positions {
		if p.Status == domain.PositionOpen {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *memStore) GetAccount(_ context.Context) (domain.Account, error) {
	if m.account.Balance <= 0 && m.account.PeakEquity <= 0 {
		return domain.Account{}, domain.ErrNotFound
	}
	return m.account, nil
}

func (m *memStore) UpdateAccount(_ context.Context, a domain.Account) error {
	m.account = a
	return nil
}

func newTestManager() (*Manager, *memStore) {
	st := &memStore{account: domain.Account{Balance: 10000, PeakEquity: 10000}}
	return New(st, Config{}), st
}

func buySignal() domain.SignalEvent {
	return domain.SignalEvent{
		StrategyID: "chandelier",
		Symbol:     "BTCUSDT",
		Timeframe:  "1h",
		Direction:  domain.DirectionBuy,
		Price:      100,
		Meta: map[string]any{
			"stop_loss": 97.0, "take_profit": 109.0,
		},
	}
}

func TestEvaluateAllowsValidLong(t *testing.T) {
	m, _ := newTestManager()
	d, err := m.Evaluate(context.Background(), buySignal())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !d.Allowed {
		t.Fatalf("se esperaba permitida: %s", d.Reason)
	}
	if mathAbs(d.Quantity-33.3333) > 0.01 {
		t.Errorf("quantity = %.4f, want ~33.3333", d.Quantity)
	}
	if d.StopLoss != 97 {
		t.Errorf("stop = %v, want 97", d.StopLoss)
	}
	if d.TakeProfit != 109 {
		t.Errorf("target = %v, want 109", d.TakeProfit)
	}
	if d.RiskAmount != 100 {
		t.Errorf("risk = %.2f, want 100 (1%% de 10000)", d.RiskAmount)
	}
}

func TestEvaluateDefaultsWhenNoMeta(t *testing.T) {
	m, _ := newTestManager()
	ev := buySignal()
	ev.Meta = nil
	d, err := m.Evaluate(context.Background(), ev)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !d.Allowed {
		t.Fatalf("se esperaba permitida: %s", d.Reason)
	}
	if d.StopLoss != 97 {
		t.Errorf("stop por defecto = %v, want 97 (3%%)", d.StopLoss)
	}
	if d.TakeProfit != 106 {
		t.Errorf("target por defecto = %v, want 106 (R/R 2:1)", d.TakeProfit)
	}
}

func TestEvaluateRejectsBadRR(t *testing.T) {
	m, _ := newTestManager()
	ev := buySignal()
	ev.Meta = map[string]any{"stop_loss": 95.0, "take_profit": 102.0}
	d, err := m.Evaluate(context.Background(), ev)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if d.Allowed {
		t.Fatal("se esperaba rechazo por R/R")
	}
	if d.Reason == "" {
		t.Fatal("reason vacío")
	}
}

func TestEvaluateRespectsMaxOpenPositions(t *testing.T) {
	m, st := newTestManager()
	for i := 0; i < 3; i++ {
		ev := buySignal()
		if i > 0 {
			ev.Symbol = "ETHUSDT"
		}
		if i == 2 {
			ev.Symbol = "SOLUSDT"
		}
		_, err := st.OpenPosition(context.Background(), domain.Position{
			StrategyID: ev.StrategyID, Symbol: ev.Symbol, Timeframe: ev.Timeframe,
			Side: domain.DirectionBuy, EntryPrice: 100, Status: domain.PositionOpen,
		})
		if err != nil {
			t.Fatalf("preparando posición %d: %v", i, err)
		}
	}
	d, err := m.Evaluate(context.Background(), buySignal())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if d.Allowed {
		t.Fatal("se esperaba rechazo por límite de posiciones")
	}
	if d.OpenCount != 3 {
		t.Errorf("open count = %d, want 3", d.OpenCount)
	}
}

func TestEvaluateKillSwitch(t *testing.T) {
	m, st := newTestManager()
	st.account = domain.Account{Balance: 1000, PeakEquity: 10000}
	d, err := m.Evaluate(context.Background(), buySignal())
	if err == nil {
		t.Fatal("se esperaba error ErrClosed por kill-switch")
	}
	if d.Allowed {
		t.Fatal("se esperaba rechazo por kill-switch")
	}
}

func TestOnSignalOpensAndReverses(t *testing.T) {
	m, st := newTestManager()
	if err := m.OnSignal(context.Background(), buySignal()); err != nil {
		t.Fatalf("OnSignal abrir: %v", err)
	}
	open, _ := st.OpenPositions(context.Background())
	if len(open) != 1 {
		t.Fatalf("posiciones abiertas = %d, want 1", len(open))
	}
	if open[0].Side != domain.DirectionBuy {
		t.Fatalf("side = %s, want buy", open[0].Side)
	}

	sell := domain.SignalEvent{
		StrategyID: "chandelier",
		Symbol:     "BTCUSDT",
		Timeframe:  "1h",
		Direction:  domain.DirectionSell,
		Price:      105,
		Meta:       map[string]any{"stop_loss": 108.0, "take_profit": 92.0},
	}
	if err := m.OnSignal(context.Background(), sell); err != nil {
		t.Fatalf("OnSignal invertir: %v", err)
	}
	open, _ = st.OpenPositions(context.Background())
	if len(open) != 1 {
		t.Fatalf("posiciones abiertas = %d, want 1 tras reversión", len(open))
	}
	if open[0].Side != domain.DirectionSell {
		t.Fatalf("side = %s, want sell tras reversión", open[0].Side)
	}
	if st.account.Balance <= 10000 {
		t.Fatalf("balance = %.2f, want > 10000 tras cierre con beneficio", st.account.Balance)
	}
}

func TestOnSignalIgnoresSameDirection(t *testing.T) {
	m, st := newTestManager()
	if err := m.OnSignal(context.Background(), buySignal()); err != nil {
		t.Fatalf("OnSignal abrir: %v", err)
	}
	if err := m.OnSignal(context.Background(), buySignal()); err != nil {
		t.Fatalf("OnSignal duplicado no debía fallar: %v", err)
	}
	open, _ := st.OpenPositions(context.Background())
	if len(open) != 1 {
		t.Fatalf("posiciones abiertas = %d, want 1", len(open))
	}
}

func mathAbs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
