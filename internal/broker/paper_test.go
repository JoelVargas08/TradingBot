package broker

import (
	"context"
	"errors"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/store"
)

func newPaper(t *testing.T) (*PaperBroker, *store.Store) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	b := NewPaperBroker(s, s, PaperConfig{})
	if _, err := b.GetBalance(context.Background()); err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	return b, s
}

func buyOrder(clientID string, qty float64) domain.Order {
	return domain.Order{
		ClientID:   clientID,
		StrategyID: "ib-mtf",
		Symbol:     "BTCUSDT",
		Timeframe:  "15m",
		Side:       domain.DirectionBuy,
		Quantity:   qty,
		Price:      100,
		StopLoss:   97,
		TakeProfit: 109,
	}
}

func TestPlaceOrderAppliesSlippageOnce(t *testing.T) {
	b, _ := newPaper(t)
	o, err := b.PlaceOrder(context.Background(), buyOrder("ib-mtf-BTCUSDT-1000-buy", 1))
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if o.Status != OrderFilled {
		t.Fatalf("status = %s, want %s", o.Status, OrderFilled)
	}
	want := 100 * (1 + 0.0002)
	if mathAbs(o.FilledPrice-want) > 1e-9 {
		t.Errorf("fill = %.8f, want %.8f (slippage aplicado una vez)", o.FilledPrice, want)
	}
	if o.FilledQty != 1 {
		t.Errorf("qty = %v, want 1", o.FilledQty)
	}
	if o.ID == "" || o.ID != o.ClientID {
		t.Errorf("id=%q debe coincidir con client_id=%q", o.ID, o.ClientID)
	}
}

func TestNoDoubleSlippage(t *testing.T) {
	b, s := newPaper(t)
	ctx := context.Background()
	if _, err := b.PlaceOrder(ctx, buyOrder("ib-mtf-BTCUSDT-1001-buy", 1)); err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	open, err := b.GetPositions(ctx)
	if err != nil || len(open) != 1 {
		t.Fatalf("posiciones = %d err=%v, want 1", len(open), err)
	}
	if err := b.ClosePosition(ctx, open[0].ID, 103, "take-profit"); err != nil {
		t.Fatalf("ClosePosition: %v", err)
	}

	closed, err := s.ClosedPositions(ctx)
	if err != nil || len(closed) != 1 {
		t.Fatalf("cerradas = %d err=%v, want 1", len(closed), err)
	}
	c := closed[0]
	// Slippage UNA vez por lado: entrada 100→100.02, salida 103→102.9794.
	wantFill := 100 * (1 + 0.0002)
	wantExit := 103 * (1 - 0.0002)
	wantGross := (wantExit - wantFill) * 1
	if mathAbs(c.GrossPnL-wantGross) > 1e-9 {
		t.Errorf("gross = %.8f, want %.8f (slippage sin duplicar)", c.GrossPnL, wantGross)
	}
	wantDouble := (103*(1-0.0004) - 100*(1+0.0004)) * 1
	if mathAbs(c.GrossPnL-wantDouble) < 1e-9 {
		t.Fatalf("gross %.8f no debe reflejar doble slippage (%.8f)", c.GrossPnL, wantDouble)
	}
}

func TestFeesAppliedOnce(t *testing.T) {
	b, s := newPaper(t)
	ctx := context.Background()
	if _, err := b.PlaceOrder(ctx, buyOrder("ib-mtf-BTCUSDT-1002-buy", 1)); err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	open, _ := b.GetPositions(ctx)
	if err := b.ClosePosition(ctx, open[0].ID, 103, "take-profit"); err != nil {
		t.Fatalf("ClosePosition: %v", err)
	}
	closed, _ := s.ClosedPositions(ctx)
	c := closed[0]
	wantEntryFee := 100.02 * 1 * 0.001
	wantExitFee := 102.9794 * 1 * 0.001
	if mathAbs(c.EntryFee-wantEntryFee) > 1e-9 {
		t.Errorf("entry fee = %.8f, want %.8f (una vez)", c.EntryFee, wantEntryFee)
	}
	if mathAbs(c.ExitFee-wantExitFee) > 1e-9 {
		t.Errorf("exit fee = %.8f, want %.8f (una vez)", c.ExitFee, wantExitFee)
	}
	acc, _ := b.GetBalance(ctx)
	total := wantEntryFee + wantExitFee
	if mathAbs(acc.Fees-total) > 1e-6 {
		t.Errorf("account fees = %.8f, want %.8f (fees una sola vez)", acc.Fees, total)
	}
}

func TestDuplicateClientIDDoesNotDuplicateOrder(t *testing.T) {
	b, s := newPaper(t)
	ctx := context.Background()
	first, err := b.PlaceOrder(ctx, buyOrder("ib-mtf-BTCUSDT-1003-buy", 1))
	if err != nil {
		t.Fatalf("PlaceOrder 1: %v", err)
	}
	second, err := b.PlaceOrder(ctx, buyOrder("ib-mtf-BTCUSDT-1003-buy", 1))
	if err != nil {
		t.Fatalf("PlaceOrder 2: %v", err)
	}
	if second.FilledPrice != first.FilledPrice {
		t.Errorf("segunda orden no debe re-fill: %v != %v", second.FilledPrice, first.FilledPrice)
	}
	all, err := s.ListOrders(ctx, "")
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("órdenes = %d, want 1 (sin duplicado)", len(all))
	}
	pos, _ := b.GetPositions(ctx)
	if len(pos) != 1 {
		t.Fatalf("posiciones = %d, want 1", len(pos))
	}
}

func TestGetOrderAndOpenOrders(t *testing.T) {
	b, _ := newPaper(t)
	ctx := context.Background()
	if _, err := b.PlaceOrder(ctx, buyOrder("ib-mtf-BTCUSDT-1004-buy", 1)); err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	o, err := b.GetOrder(ctx, "ib-mtf-BTCUSDT-1004-buy")
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if o.Status != OrderFilled {
		t.Errorf("status = %s, want filled", o.Status)
	}
	if _, err := b.GetOrder(ctx, "no-existe"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetOrder desconocida: %v, want ErrNotFound", err)
	}
	open, err := b.GetOpenOrders(ctx)
	if err != nil {
		t.Fatalf("GetOpenOrders: %v", err)
	}
	if len(open) != 0 {
		t.Errorf("órdenes abiertas = %d, want 0 (market fills inmediatos)", len(open))
	}
}

func TestCancelOrderPending(t *testing.T) {
	b, s := newPaper(t)
	ctx := context.Background()
	pending := domain.Order{
		ClientID:  "ib-mtf-BTCUSDT-1005-buy",
		StrategyID: "ib-mtf",
		Symbol:    "BTCUSDT",
		Side:      domain.DirectionBuy,
		Quantity:  1,
		Price:     90,
		Status:    OrderPending,
		Time:      time.Now(),
	}
	if err := s.SaveOrder(ctx, pending); err != nil {
		t.Fatalf("SaveOrder: %v", err)
	}
	if err := b.CancelOrder(ctx, pending.ClientID); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	got, err := b.GetOrder(ctx, pending.ClientID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.Status != OrderCanceled {
		t.Errorf("status = %s, want canceled", got.Status)
	}
	if err := b.CancelOrder(ctx, "no-existe"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("CancelOrder desconocida: %v, want ErrNotFound", err)
	}
}

func TestCheckStopsSLAndTP(t *testing.T) {
	b, s := newPaper(t)
	ctx := context.Background()
	if _, err := b.PlaceOrder(ctx, buyOrder("ib-mtf-BTCUSDT-1006-buy", 1)); err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	b.CheckStops(ctx, domain.Kline{
		Symbol: "BTCUSDT", Timeframe: "15m",
		Start: time.UnixMilli(2000), High: 103, Low: 96.5, Close: 97.1, Closed: true,
	})
	closed, _ := s.ClosedPositions(ctx)
	if len(closed) != 1 {
		t.Fatalf("cerradas = %d, want 1 (stop)", len(closed))
	}
	if closed[0].ExitReason != "stop" {
		t.Errorf("reason = %s, want stop", closed[0].ExitReason)
	}
	wantExit := 97 * (1 - 0.0002)
	if mathAbs(closed[0].ExitPrice-wantExit) > 1e-9 {
		t.Errorf("exit = %.8f, want %.8f (slippage de salida una vez)", closed[0].ExitPrice, wantExit)
	}
}

func mathAbs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}