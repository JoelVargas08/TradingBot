package paper

import (
	"context"
	"fmt"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

type memStore struct {
	positions []domain.Position
	account   domain.Account
	candles   []domain.Kline
	nextID    int64
}

func (m *memStore) SaveCandle(_ context.Context, k domain.Kline) error {
	m.candles = append(m.candles, k)
	return nil
}

func (m *memStore) CandlesBetween(_ context.Context, symbol, timeframe string, start, end time.Time) ([]domain.Kline, error) {
	var out []domain.Kline
	for _, k := range m.candles {
		if k.Symbol == symbol && k.Timeframe == timeframe && !k.Start.Before(start) && k.Start.Before(end) {
			out = append(out, k)
		}
	}
	return out, nil
}

func (m *memStore) RecentCandles(_ context.Context, symbol, timeframe string, limit int) ([]domain.Kline, error) {
	var out []domain.Kline
	for _, k := range m.candles {
		if k.Symbol == symbol && k.Timeframe == timeframe {
			out = append(out, k)
		}
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Start.Before(out[i].Start) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
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
			remainderNet := p.GrossPnL - totalCost
			p.NetPnL = remainderNet + p.PartialPnL
			p.PnL = p.NetPnL
			if p.RiskAmount > 0 {
				p.RMultiple = p.NetPnL / p.RiskAmount
			}
			m.account.Balance += remainderNet
			m.account.RealizedPnL += remainderNet
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

func (m *memStore) PartialClosePosition(_ context.Context, id int64, closeQty, exitPrice float64, exitTS time.Time, opts domain.CloseOptions) (domain.Position, error) {
	for i, p := range m.positions {
		if p.ID != id {
			continue
		}
		if p.Status != domain.PositionOpen {
			return domain.Position{}, fmt.Errorf("posición %d no está abierta", id)
		}
		if closeQty <= 0 || closeQty >= p.Quantity {
			return domain.Position{}, fmt.Errorf("cierre parcial inválido")
		}
		var gross float64
		if p.Side == domain.DirectionBuy {
			gross = (exitPrice - p.EntryPrice) * closeQty
		} else {
			gross = (p.EntryPrice - exitPrice) * closeQty
		}
		totalCost := opts.EntryFee + opts.ExitFee + opts.SlippageEntry + opts.SlippageExit
		net := gross - totalCost
		p.Quantity -= closeQty
		if opts.NewStopLoss > 0 {
			p.StopLoss = opts.NewStopLoss
		}
		p.PartialQty += closeQty
		p.PartialPnL += net
		m.account.Balance += net
		m.account.RealizedPnL += net
		m.account.Fees += totalCost
		m.account.Equity = m.account.Balance
		if m.account.Balance > m.account.PeakEquity {
			m.account.PeakEquity = m.account.Balance
		}
		m.positions[i] = p
		return p, nil
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

func (noopController) EvaluateSignal(_ context.Context, ev domain.SignalEvent) (domain.Decision, error) {
	return domain.Decision{
		Allowed:    true,
		Side:       ev.Direction,
		EntryPrice: ev.Price,
		StopLoss:   ev.Price * 0.97,
		TakeProfit: ev.Price * 1.09,
		Quantity:   1,
		RiskAmount: 3,
	}, nil
}

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

func signal(dir domain.Direction, price float64) domain.SignalEvent {
	return domain.SignalEvent{
		StrategyID: "chandelier",
		Symbol:     "BTCUSDT",
		Timeframe:  "1h",
		Direction:  dir,
		Price:      price,
	}
}

func TestContrarySignalAppliesCosts(t *testing.T) {
	e, st := newTestEngine()
	if err := e.OnSignal(context.Background(), signal(domain.DirectionBuy, 100)); err != nil {
		t.Fatalf("OnSignal abrir LONG: %v", err)
	}
	before, _ := st.GetAccount(context.Background())

	if err := e.OnSignal(context.Background(), signal(domain.DirectionSell, 103)); err != nil {
		t.Fatalf("OnSignal invertir: %v", err)
	}

	closed, _ := st.ClosedPositions(context.Background())
	if len(closed) != 1 {
		t.Fatalf("cerradas = %d, want 1", len(closed))
	}
	c := closed[0]
	if c.ExitReason != "signal-contrary" {
		t.Errorf("reason = %s, want signal-contrary", c.ExitReason)
	}
	if c.GrossPnL != 3 {
		t.Errorf("gross = %.4f, want 3", c.GrossPnL)
	}
	if c.NetPnL >= 3 {
		t.Errorf("net = %.4f, debe ser < 3 con costes", c.NetPnL)
	}
	if c.EntryFee <= 0 || c.ExitFee <= 0 || c.SlippageEntry <= 0 || c.SlippageExit <= 0 {
		t.Errorf("costes no aplicados: entryFee=%v exitFee=%v slipEntry=%v slipExit=%v",
			c.EntryFee, c.ExitFee, c.SlippageEntry, c.SlippageExit)
	}
	after, _ := st.GetAccount(context.Background())
	if after.Balance <= before.Balance {
		t.Errorf("balance debería crecer tras cerrar LONG con beneficio: %.4f → %.4f", before.Balance, after.Balance)
	}

	open, _ := st.OpenPositions(context.Background())
	if len(open) != 1 {
		t.Fatalf("abiertas = %d, want 1 (SHORT tras reversión)", len(open))
	}
	if open[0].Side != domain.DirectionSell {
		t.Errorf("side = %s, want sell", open[0].Side)
	}
}

func TestSameDirectionNoDuplicate(t *testing.T) {
	e, st := newTestEngine()
	if err := e.OnSignal(context.Background(), signal(domain.DirectionBuy, 100)); err != nil {
		t.Fatalf("OnSignal abrir: %v", err)
	}
	if err := e.OnSignal(context.Background(), signal(domain.DirectionBuy, 100)); err != nil {
		t.Fatalf("OnSignal mismo lado no debe fallar: %v", err)
	}
	open, _ := st.OpenPositions(context.Background())
	if len(open) != 1 {
		t.Errorf("posiciones abiertas = %d, want 1 (sin duplicar)", len(open))
	}
}

func TestMarkPriceIntraTradeDrawdown(t *testing.T) {
	e, st := newTestEngine()
	if err := e.OnSignal(context.Background(), signal(domain.DirectionBuy, 100)); err != nil {
		t.Fatalf("OnSignal abrir: %v", err)
	}

	eq, _ := st.GetAccount(context.Background())
	peakBefore := eq.PeakEquity

	// Precio por debajo de entrada → pérdida no realizada.
	if err := e.MarkPrice(context.Background(), domain.Kline{
		Symbol: "BTCUSDT", Timeframe: "1h",
		Start: time.Now(), Close: 96, Closed: true,
	}); err != nil {
		t.Fatalf("MarkPrice: %v", err)
	}

	acc, err := st.GetAccount(context.Background())
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if acc.Equity >= acc.Balance {
		t.Errorf("equity %.2f debe ser < balance %.2f con pérdida intratrade", acc.Equity, acc.Balance)
	}
	if acc.MaxDrawdown <= 0 {
		t.Errorf("drawdown debe ser > 0, got %.4f", acc.MaxDrawdown)
	}
	if acc.PeakEquity != peakBefore {
		t.Errorf("peak equity no debe disminuir: %.2f → %.2f", peakBefore, acc.PeakEquity)
	}
	if acc.UnrealizedPnL >= 0 {
		t.Errorf("unrealized = %.2f, debe ser negativo", acc.UnrealizedPnL)
	}
}

func mathAbs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func TestPendingLimitDoesNotFillOnSignalBar(t *testing.T) {
	e, st := newTestEngine()
	var gate = true
	e.SetEntryGate(func() bool { return gate })
	ev := signal(domain.DirectionBuy, 105)
	ev.BarTS = time.UnixMilli(2000)
	ev.Meta = map[string]any{"entry_zone_low": 100.0, "entry_zone_high": 102.0}
	if err := e.OnSignal(context.Background(), ev); err != nil {
		t.Fatalf("arm: %v", err)
	}
	e.OnCandle(context.Background(), domain.Kline{Symbol: "BTCUSDT", Timeframe: "1h", Start: time.UnixMilli(2000), Low: 99, High: 106, Close: 103, Closed: true})
	open, _ := st.OpenPositions(context.Background())
	if len(open) != 0 {
		t.Fatalf("signal candle must not retroactively fill: %d", len(open))
	}
	e.OnCandle(context.Background(), domain.Kline{Symbol: "BTCUSDT", Timeframe: "1h", Start: time.UnixMilli(3000), Low: 99, High: 103, Close: 101, Closed: true})
	open, _ = st.OpenPositions(context.Background())
	if len(open) != 1 {
		t.Fatalf("next candle should fill limit: %d", len(open))
	}
	if open[0].EntryPrice != 102 {
		t.Fatalf("entry=%.2f want 102", open[0].EntryPrice)
	}
}

func TestPendingLimitRespectsSessionGate(t *testing.T) {
	e, st := newTestEngine()
	gate := false
	e.SetEntryGate(func() bool { return gate })
	ev := signal(domain.DirectionBuy, 105)
	ev.BarTS = time.UnixMilli(2000)
	ev.Meta = map[string]any{"entry_zone_low": 100.0, "entry_zone_high": 102.0}
	if err := e.OnSignal(context.Background(), ev); err != nil {
		t.Fatalf("inactive signal: %v", err)
	}
	open, _ := st.OpenPositions(context.Background())
	if len(open) != 0 {
		t.Fatal("inactive session must not open")
	}
	gate = true
	if err := e.OnSignal(context.Background(), ev); err != nil {
		t.Fatalf("active signal: %v", err)
	}
	e.OnCandle(context.Background(), domain.Kline{Symbol: "BTCUSDT", Timeframe: "1h", Start: time.UnixMilli(3000), Low: 101, High: 103, Close: 102, Closed: true})
	open, _ = st.OpenPositions(context.Background())
	if len(open) != 1 || open[0].EntryPrice != 102 {
		t.Fatalf("active session should fill 102: %+v", open)
	}
}

func TestOnSignalArmsTwoLimitsAndFillsOnLaterCandle(t *testing.T) {
	e, st := newTestEngine()
	ev := signal(domain.DirectionBuy, 110)
	ev.Meta = map[string]any{}
	ev.BarTS = time.UnixMilli(1000)
	ev.Meta["entry_zone_low"] = float64(100)
	ev.Meta["entry_zone_high"] = float64(105)
	if err := e.OnSignal(context.Background(), ev); err != nil {
		t.Fatalf("OnSignal: %v", err)
	}
	open, _ := st.OpenPositions(context.Background())
	if len(open) != 0 {
		t.Fatalf("zone signal must not open immediately: %d", len(open))
	}
	e.OnCandle(context.Background(), domain.Kline{Symbol: "BTCUSDT", Timeframe: "1h", Start: time.UnixMilli(1000), Low: 104, High: 108, Close: 105, Closed: true})
	open, _ = st.OpenPositions(context.Background())
	if len(open) != 0 {
		t.Fatalf("signal candle must not retroactively fill: %d", len(open))
	}
	e.OnCandle(context.Background(), domain.Kline{Symbol: "BTCUSDT", Timeframe: "1h", Start: time.UnixMilli(2000), Low: 104, High: 108, Close: 105, Closed: true})
	open, _ = st.OpenPositions(context.Background())
	if len(open) != 1 || open[0].EntryPrice != 105 {
		t.Fatalf("first limit should fill at 105: %+v", open)
	}
}

func seedPartialLong(t testing.TB, e *Engine, st *memStore) int64 {
	t.Helper()
	id, err := e.OpenPosition(context.Background(), domain.Position{
		StrategyID: "chandelier",
		Symbol:     "BTCUSDT",
		Timeframe:  "1h",
		Side:       domain.DirectionBuy,
		EntryTS:    time.UnixMilli(1000),
		EntryPrice: 100,
		StopLoss:   97,
		TakeProfit: 110,
		PartialTP:  105,
		Quantity:   1,
		RiskAmount: 3,
		Status:     domain.PositionOpen,
	})
	if err != nil {
		t.Fatalf("OpenPosition (parcial): %v", err)
	}
	return id
}

// TestPartialTPClosesHalfAndProtectsRest verifica el cierre del 50% en el
// máximo previo, la protección a breakeven y que la posición permanece abierta.
func TestPartialTPClosesHalfAndProtectsRest(t *testing.T) {
	e, st := newTestEngine()
	seedPartialLong(t, e, st)
	e.CheckStops(context.Background(), domain.Kline{
		Symbol: "BTCUSDT", Timeframe: "1h", Start: time.UnixMilli(2000),
		Low: 100.5, High: 106, Close: 105, Closed: true,
	})
	open, _ := st.OpenPositions(context.Background())
	if len(open) != 1 {
		t.Fatalf("abiertas = %d, want 1 (resto protegido)", len(open))
	}
	p := open[0]
	if mathAbs(p.Quantity-0.5) > 1e-9 {
		t.Errorf("cantidad restante = %.4f, want 0.5", p.Quantity)
	}
	if mathAbs(p.PartialQty-0.5) > 1e-9 {
		t.Errorf("parcial = %.4f, want 0.5", p.PartialQty)
	}
	if p.StopLoss != 100 {
		t.Errorf("stop = %v, want 100 (breakeven)", p.StopLoss)
	}
	if p.PartialPnL <= 0 {
		t.Errorf("partial pnl = %.4f, want > 0", p.PartialPnL)
	}
	closed, _ := st.ClosedPositions(context.Background())
	if len(closed) != 0 {
		t.Errorf("no debe haber posiciones cerradas, got %d", len(closed))
	}
	wantPartialNet := (105.0-100)*0.5 - (100*0.5*0.001 + 105*0.5*0.001 + 100*0.5*0.0002 + 105*0.5*0.0002)
	acc, _ := st.GetAccount(context.Background())
	if mathAbs(acc.Balance-(10000+wantPartialNet)) > 1e-6 {
		t.Errorf("balance = %.6f, want %.6f", acc.Balance, 10000+wantPartialNet)
	}
}

func TestPartialTPNotTriggeredBelowTarget(t *testing.T) {
	e, st := newTestEngine()
	seedPartialLong(t, e, st)
	e.CheckStops(context.Background(), domain.Kline{
		Symbol: "BTCUSDT", Timeframe: "1h", Start: time.UnixMilli(2000),
		Low: 99, High: 104, Close: 102, Closed: true,
	})
	open, _ := st.OpenPositions(context.Background())
	if len(open) != 1 {
		t.Fatalf("abiertas = %d, want 1", len(open))
	}
	if open[0].Quantity != 1 || open[0].PartialQty != 0 {
		t.Errorf("no debe parcializar por debajo del objetivo: qty=%.4f partial=%.4f", open[0].Quantity, open[0].PartialQty)
	}
}

// TestPartialTPNotReapplied simula una vela posterior que vuelve a tocar el
// objetivo: el parcial no se reaplica (idempotencia intra-sesión).
func TestPartialTPNotReapplied(t *testing.T) {
	e, st := newTestEngine()
	seedPartialLong(t, e, st)
	candle := domain.Kline{Symbol: "BTCUSDT", Timeframe: "1h", Low: 100.5, High: 106, Close: 105, Closed: true}
	candle.Start = time.UnixMilli(2000)
	e.CheckStops(context.Background(), candle)
	candle.Start = time.UnixMilli(3000)
	e.CheckStops(context.Background(), candle)
	open, _ := st.OpenPositions(context.Background())
	if len(open) != 1 {
		t.Fatalf("abiertas = %d, want 1", len(open))
	}
	if mathAbs(open[0].Quantity-0.5) > 1e-9 || mathAbs(open[0].PartialQty-0.5) > 1e-9 {
		t.Errorf("el parcial se reaplicó: qty=%.4f partial=%.4f", open[0].Quantity, open[0].PartialQty)
	}
}

// TestPartialTPRemainderClosesAtTakeProfit comprueba que el resto puede salir
// en el TP completo y que el PnL total (parcial + resto) se cuenta una vez.
func TestPartialTPRemainderClosesAtTakeProfit(t *testing.T) {
	e, st := newTestEngine()
	seedPartialLong(t, e, st)
	e.CheckStops(context.Background(), domain.Kline{
		Symbol: "BTCUSDT", Timeframe: "1h", Start: time.UnixMilli(2000),
		Low: 100.5, High: 106, Close: 105, Closed: true,
	})
	e.CheckStops(context.Background(), domain.Kline{
		Symbol: "BTCUSDT", Timeframe: "1h", Start: time.UnixMilli(3000),
		Low: 105.5, High: 111, Close: 110, Closed: true,
	})
	open, _ := st.OpenPositions(context.Background())
	if len(open) != 0 {
		t.Fatalf("abiertas = %d, want 0", len(open))
	}
	closed, _ := st.ClosedPositions(context.Background())
	if len(closed) != 1 {
		t.Fatalf("cerradas = %d, want 1", len(closed))
	}
	c := closed[0]
	if c.ExitReason != "take-profit" {
		t.Errorf("reason = %s, want take-profit", c.ExitReason)
	}
	if mathAbs(c.Quantity-0.5) > 1e-9 {
		t.Errorf("cantidad restante cerrada = %.4f, want 0.5", c.Quantity)
	}
	wantPartial := (105.0-100)*0.5 - (100*0.5*0.001 + 105*0.5*0.001 + 100*0.5*0.0002 + 105*0.5*0.0002)
	wantRemainder := (110.0-100)*0.5 - (100*0.5*0.001 + 110*0.5*0.001 + 100*0.5*0.0002 + 110*0.5*0.0002)
	if mathAbs(c.NetPnL-(wantPartial+wantRemainder)) > 1e-6 {
		t.Errorf("net total = %.6f, want %.6f (parcial+resto)", c.NetPnL, wantPartial+wantRemainder)
	}
	if mathAbs(c.RMultiple-c.NetPnL/3) > 1e-9 {
		t.Errorf("R = %.6f, want %.6f", c.RMultiple, c.NetPnL/3)
	}
	acc, _ := st.GetAccount(context.Background())
	if mathAbs(acc.Balance-(10000+wantPartial+wantRemainder)) > 1e-6 {
		t.Errorf("balance = %.6f, want %.6f", acc.Balance, 10000+wantPartial+wantRemainder)
	}
}

// TestPartialTPRemainderStopsAtBreakeven verifica que el resto sale a la entrada
// (breakeven) si el precio regresa antes de alcanzar el TP completo.
func TestPartialTPRemainderStopsAtBreakeven(t *testing.T) {
	e, st := newTestEngine()
	seedPartialLong(t, e, st)
	e.CheckStops(context.Background(), domain.Kline{
		Symbol: "BTCUSDT", Timeframe: "1h", Start: time.UnixMilli(2000),
		Low: 100.5, High: 106, Close: 105, Closed: true,
	})
	e.CheckStops(context.Background(), domain.Kline{
		Symbol: "BTCUSDT", Timeframe: "1h", Start: time.UnixMilli(3000),
		Low: 99.5, High: 106, Close: 100, Closed: true,
	})
	closed, _ := st.ClosedPositions(context.Background())
	if len(closed) != 1 {
		t.Fatalf("cerradas = %d, want 1", len(closed))
	}
	if closed[0].ExitReason != "stop" {
		t.Errorf("reason = %s, want stop", closed[0].ExitReason)
	}
	if closed[0].ExitPrice != 100 {
		t.Errorf("exit = %.2f, want 100 (breakeven)", closed[0].ExitPrice)
	}
	if closed[0].PartialQty != 0.5 {
		t.Errorf("parcial = %.4f, want 0.5", closed[0].PartialQty)
	}
}

// TestPartialTPAmbiguousBarPrefersStop: si la vela toca stop y objetivo parcial
// a la vez, se cierra todo al stop (sin información intravela) y no se registra
// parcial.
func TestPartialTPAmbiguousBarPrefersStop(t *testing.T) {
	e, st := newTestEngine()
	seedPartialLong(t, e, st)
	e.CheckStops(context.Background(), domain.Kline{
		Symbol: "BTCUSDT", Timeframe: "1h", Start: time.UnixMilli(2000),
		Low: 96, High: 106, Close: 100, Closed: true,
	})
	open, _ := st.OpenPositions(context.Background())
	if len(open) != 0 {
		t.Fatalf("abiertas = %d, want 0 (cierre total al stop)", len(open))
	}
	closed, _ := st.ClosedPositions(context.Background())
	if len(closed) != 1 {
		t.Fatalf("cerradas = %d, want 1", len(closed))
	}
	c := closed[0]
	if c.ExitReason != "stop" || !c.AmbiguousBar {
		t.Errorf("reason=%s ambiguous=%v, want stop/true", c.ExitReason, c.AmbiguousBar)
	}
	if c.ExitPrice != 97 {
		t.Errorf("exit = %.2f, want 97 (SL)", c.ExitPrice)
	}
	if c.PartialQty != 0 {
		t.Errorf("no debe registrarse parcial en vela ambigua, got %.4f", c.PartialQty)
	}
}

func TestPartialTPShortSideProtectsRest(t *testing.T) {
	e, st := newTestEngine()
	if _, err := e.OpenPosition(context.Background(), domain.Position{
		StrategyID: "chandelier", Symbol: "BTCUSDT", Timeframe: "1h",
		Side: domain.DirectionSell, EntryTS: time.UnixMilli(1000),
		EntryPrice: 100, StopLoss: 103, TakeProfit: 90, PartialTP: 95,
		Quantity: 1, RiskAmount: 3, Status: domain.PositionOpen,
	}); err != nil {
		t.Fatalf("OpenPosition short: %v", err)
	}
	e.CheckStops(context.Background(), domain.Kline{
		Symbol: "BTCUSDT", Timeframe: "1h", Start: time.UnixMilli(2000),
		Low: 94, High: 99, Close: 95, Closed: true,
	})
	open, _ := st.OpenPositions(context.Background())
	if len(open) != 1 {
		t.Fatalf("abiertas = %d, want 1", len(open))
	}
	p := open[0]
	if mathAbs(p.Quantity-0.5) > 1e-9 || mathAbs(p.PartialQty-0.5) > 1e-9 {
		t.Errorf("short parcial esperado: qty=%.4f partial=%.4f", p.Quantity, p.PartialQty)
	}
	if p.StopLoss != 100 {
		t.Errorf("stop = %v, want 100 (breakeven)", p.StopLoss)
	}
	if p.PartialPnL <= 0 {
		t.Errorf("partial pnl = %.4f, want > 0", p.PartialPnL)
	}
}

// TestReconcileDoesNotRepeatPartialAfterRestart arranca con una posición ya
// parcializada (estado persistido) y repera velas: el parcial no se duplica.
func TestReconcileDoesNotRepeatPartialAfterRestart(t *testing.T) {
	_, st := newTestEngine()
	id, err := st.OpenPosition(context.Background(), domain.Position{
		StrategyID: "chandelier", Symbol: "BTCUSDT", Timeframe: "1h",
		Side: domain.DirectionBuy, EntryTS: time.UnixMilli(1000),
		EntryPrice: 100, StopLoss: 100, TakeProfit: 110, PartialTP: 105,
		Quantity: 0.5, RiskAmount: 1.5, PartialQty: 0.5, PartialPnL: 2.0,
		Status: domain.PositionOpen,
	})
	if err != nil {
		t.Fatalf("OpenPosition persistida: %v", err)
	}
	st.candles = []domain.Kline{{
		Symbol: "BTCUSDT", Timeframe: "1h", Start: time.UnixMilli(2000),
		Low: 101, High: 106, Close: 105, Closed: true,
	}}
	e := New(st, noopController{}, Config{})
	if err := e.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if err := e.ReconcileOpenPositions(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	open, _ := st.OpenPositions(context.Background())
	if len(open) != 1 || open[0].ID != id {
		t.Fatalf("abiertas = %d, want 1 (id=%d)", len(open), id)
	}
	if mathAbs(open[0].Quantity-0.5) > 1e-9 || mathAbs(open[0].PartialQty-0.5) > 1e-9 {
		t.Errorf("reconcile duplicó el parcial: qty=%.4f partial=%.4f", open[0].Quantity, open[0].PartialQty)
	}
}
