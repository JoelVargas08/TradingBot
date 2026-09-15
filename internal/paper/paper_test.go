package paper

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
			p.EntryFee = opts.EntryFee
			p.ExitFee = opts.ExitFee
			p.SlippageEntry = opts.SlippageEntry
			p.SlippageExit = opts.SlippageExit
			p.AmbiguousBar = opts.AmbiguousBar
			p.Duration = exitTS.Sub(p.EntryTS)
			if p.Side == domain.DirectionBuy {
				p.GrossPnL = (exitPrice - p.EntryPrice) * p.Quantity
			} else {
				p.GrossPnL = (p.EntryPrice - exitPrice) * p.Quantity
			}
			totalCost := opts.EntryFee + opts.ExitFee + opts.SlippageEntry + opts.SlippageExit
			p.NetPnL = p.GrossPnL - totalCost
			if p.RiskAmount > 0 {
				p.RMultiple = p.NetPnL / p.RiskAmount
			}
			m.account.Balance += p.NetPnL
			m.account.RealizedPnL += p.NetPnL
			m.account.Fees += totalCost
			m.account.Equity = m.account.Balance
			m.account.UnrealizedPnL = 0
			if m.account.Balance > m.account.PeakEquity {
				m.account.PeakEquity = m.account.Balance
			}
			if m.account.PeakEquity > 0 {
				dd := (m.account.PeakEquity - m.account.Balance) / m.account.PeakEquity
				if dd > m.account.MaxDrawdown {
					m.account.MaxDrawdown = dd
				}
			}
			m.positions[i] = p
			return p, nil
		}
	}
	return domain.Position{}, domain.ErrNotFound
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

func (m *memStore) ClosedPositions(_ context.Context) ([]domain.Position, error) {
	var out []domain.Position
	for _, p := range m.positions {
		if p.Status == domain.PositionClosed {
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

type noopController struct{}

func (noopController) OnSignal(context.Context, domain.SignalEvent) error { return nil }

func newTestEngine() (*Engine, *memStore) {
	st := &memStore{account: domain.Account{
		Balance: 10000, InitialBalance: 10000, PeakEquity: 10000,
	}}
	return New(st, noopController{}, Config{FeeRate: 0.001, SlippageRate: 0.0002}), st
}

func seedLong(t testing.TB, e *Engine, st *memStore) int64 {
	t.Helper()
	id, err := e.OpenPosition(context.Background(), domain.Position{
		StrategyID: "chandelier",
		Symbol:     "BTCUSDT",
		Timeframe:  "1h",
		Side:       domain.DirectionBuy,
		EntryTS:    time.UnixMilli(1000),
		EntryPrice: 100,
		StopLoss:   97,
		TakeProfit: 109,
		Quantity:   1,
		RiskAmount: 3,
		Status:     domain.PositionOpen,
	})
	if err != nil {
		t.Fatalf("OpenPosition: %v", err)
	}
	return id
}

func TestCloseAppliesFeesAndPnL(t *testing.T) {
	e, st := newTestEngine()
	id := seedLong(t, e, st)
	if err := e.Close(context.Background(), id, 103, time.UnixMilli(2000), "take-profit"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed, _ := st.ClosedPositions(context.Background())
	if len(closed) != 1 {
		t.Fatalf("cerradas = %d, want 1", len(closed))
	}
	c := closed[0]
	if c.GrossPnL != 3 {
		t.Errorf("gross = %.4f, want 3", c.GrossPnL)
	}
	if c.Duration != time.Second {
		t.Errorf("duration = %v, want 1s", c.Duration)
	}
	wantNet := 3 - (0.1 + 0.103 + 0.02 + 0.0206)
	if mathAbs(c.NetPnL-wantNet) > 0.0001 {
		t.Errorf("net = %.4f, want %.4f", c.NetPnL, wantNet)
	}
	if mathAbs(c.RMultiple-wantNet/3) > 0.0001 {
		t.Errorf("R = %.4f, want %.4f", c.RMultiple, wantNet/3)
	}
	if c.ExitReason != "take-profit" {
		t.Errorf("reason = %s", c.ExitReason)
	}
	if mathAbs(st.account.Balance-(10000+wantNet)) > 0.0001 {
		t.Errorf("balance = %.4f, want %.4f", st.account.Balance, 10000+wantNet)
	}
}

func TestCheckStopsLongStopHit(t *testing.T) {
	e, st := newTestEngine()
	seedLong(t, e, st)
	e.CheckStops(context.Background(), domain.Kline{
		Symbol: "BTCUSDT", Timeframe: "1h",
		Start: time.UnixMilli(2000),
		High:  99, Low: 95, Close: 96,
		Closed: true,
	})
	closed, _ := st.ClosedPositions(context.Background())
	if len(closed) != 1 {
		t.Fatalf("cerradas = %d, want 1", len(closed))
	}
	if closed[0].ExitReason != "stop" {
		t.Errorf("reason = %s, want stop", closed[0].ExitReason)
	}
	if closed[0].ExitPrice != 97 {
		t.Errorf("exit = %.2f, want 97 (SL)", closed[0].ExitPrice)
	}
	if _, err := st.GetAccount(context.Background()); err != nil {
		t.Fatalf("cuenta no inicializada: %v", err)
	}
}

func TestCheckStopsTakeProfitHit(t *testing.T) {
	e, st := newTestEngine()
	seedLong(t, e, st)
	e.CheckStops(context.Background(), domain.Kline{
		Symbol: "BTCUSDT", Timeframe: "1h",
		Start: time.UnixMilli(2000),
		High:  110, Low: 105, Close: 109.5,
		Closed: true,
	})
	closed, _ := st.ClosedPositions(context.Background())
	if len(closed) != 1 {
		t.Fatalf("cerradas = %d, want 1", len(closed))
	}
	if closed[0].ExitReason != "take-profit" {
		t.Errorf("reason = %s, want take-profit", closed[0].ExitReason)
	}
	if closed[0].ExitPrice != 109 {
		t.Errorf("exit = %.2f, want 109 (TP)", closed[0].ExitPrice)
	}
}

func TestCheckStopsAmbiguousPreferStop(t *testing.T) {
	e, st := newTestEngine()
	seedLong(t, e, st)
	e.CheckStops(context.Background(), domain.Kline{
		Symbol: "BTCUSDT", Timeframe: "1h",
		Start: time.UnixMilli(2000),
		High:  110, Low: 95, Close: 100,
		Closed: true,
	})
	closed, _ := st.ClosedPositions(context.Background())
	if len(closed) != 1 {
		t.Fatalf("cerradas = %d, want 1", len(closed))
	}
	if closed[0].ExitReason != "stop" {
		t.Errorf("reason = %s, want stop (ambiguo)", closed[0].ExitReason)
	}
	if !closed[0].AmbiguousBar {
		t.Error("ambiguous_bar debe marcarse")
	}
}

func TestCheckStopsIgnoresOpenCandle(t *testing.T) {
	e, st := newTestEngine()
	seedLong(t, e, st)
	e.CheckStops(context.Background(), domain.Kline{
		Symbol: "BTCUSDT", Timeframe: "1h",
		Start: time.UnixMilli(2000),
		High:  110, Low: 95, Close: 100,
		Closed: false,
	})
	open, _ := st.OpenPositions(context.Background())
	if len(open) != 1 {
		t.Fatalf("abiertas = %d, want 1 (vela en curso ignorada)", len(open))
	}
}

func TestPerformance(t *testing.T) {
	e, st := newTestEngine()
	// Win: long 100 → 103 (gross +3, net +2.7564)
	w1 := seedLong(t, e, st)
	if err := e.Close(context.Background(), w1, 103, time.UnixMilli(2000), "take-profit"); err != nil {
		t.Fatalf("close w1: %v", err)
	}
	// Win consecutiva: long 100 → 106 (gross +6, net +5.7528)
	w2 := seedLong(t, e, st)
	if err := e.Close(context.Background(), w2, 106, time.UnixMilli(3000), "take-profit"); err != nil {
		t.Fatalf("close w2: %v", err)
	}
	// Loss: long 100 → 96 stop (gross -4, net -4.2352)
	l1 := seedLong(t, e, st)
	if err := e.Close(context.Background(), l1, 96, time.UnixMilli(4000), "stop"); err != nil {
		t.Fatalf("close l1: %v", err)
	}

	wantTotal := 2.7564 + 5.7528 - 4.2352

	perf, err := e.Performance(context.Background())
	if err != nil {
		t.Fatalf("Performance: %v", err)
	}
	if perf.Trades != 3 {
		t.Errorf("trades = %d, want 3", perf.Trades)
	}
	if perf.Wins != 2 || perf.Losses != 1 {
		t.Errorf("wins/losses = %d/%d, want 2/1", perf.Wins, perf.Losses)
	}
	if mathAbs(perf.WinRate-66.666) > 0.01 {
		t.Errorf("win rate = %.2f, want ~66.67", perf.WinRate)
	}
	if mathAbs(perf.TotalPnL-wantTotal) > 0.001 {
		t.Errorf("total pnl = %.4f, want %.4f", perf.TotalPnL, wantTotal)
	}
	if mathAbs(perf.ReturnPct-wantTotal/10000*100) > 0.001 {
		t.Errorf("return = %.4f, want %.4f", perf.ReturnPct, wantTotal/10000*100)
	}
	if mathAbs(perf.AverageWin-4.2546) > 0.001 {
		t.Errorf("avg win = %.4f, want 4.2546", perf.AverageWin)
	}
	if mathAbs(perf.AverageLoss-4.2352) > 0.001 {
		t.Errorf("avg loss = %.4f, want 4.2352", perf.AverageLoss)
	}
	if perf.ConsecutiveWins != 2 {
		t.Errorf("consecutive wins = %d, want 2", perf.ConsecutiveWins)
	}
	if perf.ConsecutiveLosses != 1 {
		t.Errorf("consecutive losses = %d, want 1", perf.ConsecutiveLosses)
	}
	if perf.ProfitFactor <= 0 {
		t.Error("profit factor debe ser > 0")
	}
}

func mathAbs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
