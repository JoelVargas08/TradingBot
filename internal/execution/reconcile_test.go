package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"tradingview-bot/internal/broker"
	"tradingview-bot/internal/domain"
)

func pos(id int64, symbol string, side domain.Direction, qty, entry float64) domain.Position {
	return domain.Position{
		ID:         id,
		StrategyID: "chandelier",
		Symbol:     symbol,
		Side:       side,
		EntryTS:    time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC),
		EntryPrice: entry,
		Quantity:   qty,
		Status:     domain.PositionOpen,
	}
}

func order(clientID, id, symbol, status string) domain.Order {
	return domain.Order{
		ClientID: clientID,
		ID:       id,
		Symbol:   symbol,
		Side:     domain.DirectionBuy,
		Quantity: 1,
		Time:     time.Now().UTC(),
		Status:   status,
	}
}

type fakeLocal struct {
	positions []domain.Position
	orders    []domain.Order
	marks     []domain.Mark
	closed    []domain.Position
	opened    []domain.Position
	nextID    int64
}

func (f *fakeLocal) OpenPositions(context.Context) ([]domain.Position, error) {
	return f.positions, nil
}
func (f *fakeLocal) OpenPosition(_ context.Context, p domain.Position) (int64, error) {
	f.nextID++
	p.ID = f.nextID
	f.opened = append(f.opened, p)
	f.positions = append(f.positions, p)
	return p.ID, nil
}
func (f *fakeLocal) ClosePosition(_ context.Context, id int64, exitPrice float64, ts time.Time, opts domain.CloseOptions) (domain.Position, error) {
	for i := range f.positions {
		if f.positions[i].ID == id {
			f.positions[i].Status = domain.PositionClosed
			f.positions[i].ExitPrice = exitPrice
			f.positions[i].ExitTS = ts
			f.positions[i].ExitReason = opts.Reason
			f.closed = append(f.closed, f.positions[i])
			return f.positions[i], nil
		}
	}
	return domain.Position{}, errors.New("no encontrada")
}
func (f *fakeLocal) SaveOrder(_ context.Context, o domain.Order) error {
	o.UpdatedAt = time.Now().UTC()
	for i := range f.orders {
		if f.orders[i].ClientID == o.ClientID {
			f.orders[i] = o
			return nil
		}
	}
	f.orders = append(f.orders, o)
	return nil
}
func (f *fakeLocal) ListOrders(_ context.Context, status string) ([]domain.Order, error) {
	if status == "" {
		return f.orders, nil
	}
	var out []domain.Order
	for _, o := range f.orders {
		if o.Status == status {
			out = append(out, o)
		}
	}
	return out, nil
}
func (f *fakeLocal) Marks(context.Context) ([]domain.Mark, error) { return f.marks, nil }

type fakeExchange struct {
	positions []domain.Position
	orders    []domain.Order
}

func (f *fakeExchange) GetPositions(context.Context) ([]domain.Position, error) {
	return f.positions, nil
}
func (f *fakeExchange) GetOpenOrders(context.Context) ([]domain.Order, error) { return f.orders, nil }

func newReconciler(l *fakeLocal, e *fakeExchange) *Reconciler {
	r := NewReconciler(l, e)
	r.now = func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC) }
	return r
}

func TestDiffIdentical(t *testing.T) {
	l := &fakeLocal{positions: []domain.Position{pos(1, "BTCUSDT", domain.DirectionBuy, 0.1, 60000)}}
	e := &fakeExchange{positions: []domain.Position{pos(0, "BTCUSDT", domain.DirectionBuy, 0.1, 60000)}}
	d, err := newReconciler(l, e).Diff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !d.Empty() {
		t.Fatalf("estado idéntico no debe tener discrepancias: %+v", d)
	}
}

func TestDiffGhostLocal(t *testing.T) {
	l := &fakeLocal{positions: []domain.Position{pos(1, "BTCUSDT", domain.DirectionBuy, 0.1, 60000)}}
	d, err := newReconciler(l, &fakeExchange{}).Diff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.LocalOnly) != 1 || len(d.ExchangeOnly) != 0 {
		t.Fatalf("fantasma local no detectado: %+v", d)
	}
}

func TestDiffExchangeOnly(t *testing.T) {
	e := &fakeExchange{positions: []domain.Position{pos(0, "ETHUSDT", domain.DirectionSell, 2, 3000)}}
	d, err := newReconciler(&fakeLocal{}, e).Diff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.ExchangeOnly) != 1 || len(d.LocalOnly) != 0 {
		t.Fatalf("posición solo-exchange no detectada: %+v", d)
	}
}

func TestDiffQuantityDelta(t *testing.T) {
	l := &fakeLocal{positions: []domain.Position{pos(1, "BTCUSDT", domain.DirectionBuy, 0.1, 60000)}}
	e := &fakeExchange{positions: []domain.Position{pos(0, "BTCUSDT", domain.DirectionBuy, 0.2, 60000)}}
	d, err := newReconciler(l, e).Diff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.QuantityDelta) != 1 {
		t.Fatalf("delta de cantidad no detectado: %+v", d)
	}
	if d.QuantityDelta[0].ExchangeQty != 0.2 {
		t.Fatalf("cantidad del exchange incorrecta: %+v", d.QuantityDelta[0])
	}
}

func TestDiffOrders(t *testing.T) {
	l := &fakeLocal{orders: []domain.Order{order("local-1", "ord-local-1", "BTCUSDT", broker.OrderPending)}}
	e := &fakeExchange{
		orders: []domain.Order{
			order("ex-1", "ord-ex-1", "BTCUSDT", ""),
			order("local-1", "ord-local-1", "BTCUSDT", ""),
		},
	}
	d, err := newReconciler(l, e).Diff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.ExchangeOrders) != 1 {
		t.Fatalf("orden pendiente desconocida no detectada: %+v", d)
	}
	if len(d.StaleOrders) != 0 {
		t.Fatalf("orden local reconocida por el exchange no debe ser stale: %+v", d.StaleOrders)
	}
}

func TestDiffStaleOrder(t *testing.T) {
	l := &fakeLocal{orders: []domain.Order{order("local-1", "ord-local-1", "BTCUSDT", broker.OrderPending)}}
	d, err := newReconciler(l, &fakeExchange{}).Diff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.StaleOrders) != 1 {
		t.Fatalf("orden local muerta no detectada: %+v", d)
	}
}

func TestRecoverClosesGhost(t *testing.T) {
	l := &fakeLocal{positions: []domain.Position{pos(1, "BTCUSDT", domain.DirectionBuy, 0.1, 60000)}}
	r := newReconciler(l, &fakeExchange{})
	d, err := r.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(l.closed) != 1 || l.closed[0].ID != 1 {
		t.Fatalf("fantasma local no cerrado: %+v", l.closed)
	}
	if len(d.Actions) != 1 {
		t.Fatalf("falta acción de cierre: %+v", d.Actions)
	}
}

func TestRecoverAdoptsExchangeOnly(t *testing.T) {
	l := &fakeLocal{}
	e := &fakeExchange{positions: []domain.Position{pos(0, "ETHUSDT", domain.DirectionSell, 2, 3000)}}
	r := newReconciler(l, e)
	d, err := r.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(l.opened) != 1 {
		t.Fatalf("posición del exchange no adoptada: %+v", l.opened)
	}
	adopted := l.opened[0]
	if adopted.Symbol != "ETHUSDT" || adopted.Quantity != 2 || adopted.Status != domain.PositionOpen {
		t.Fatalf("posición adoptada incorrecta: %+v", adopted)
	}
	if len(d.Actions) != 1 {
		t.Fatalf("falta acción de adopción: %+v", d.Actions)
	}
}

func TestRecoverAdjustsQuantity(t *testing.T) {
	l := &fakeLocal{positions: []domain.Position{pos(1, "BTCUSDT", domain.DirectionBuy, 0.1, 60000)}}
	e := &fakeExchange{positions: []domain.Position{pos(0, "BTCUSDT", domain.DirectionBuy, 0.25, 61000)}}
	r := newReconciler(l, e)
	d, err := r.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(l.closed) != 1 || len(l.opened) != 1 {
		t.Fatalf("ajuste de cantidad incompleto: cerradas=%+v abiertas=%+v", l.closed, l.opened)
	}
	if l.opened[0].Quantity != 0.25 {
		t.Fatalf("cantidad reabierta no sigue al exchange: %+v", l.opened[0])
	}
	if len(d.Actions) != 1 {
		t.Fatalf("falta acción de ajuste: %+v", d.Actions)
	}
}

func TestRecoverCancelsStaleOrders(t *testing.T) {
	l := &fakeLocal{orders: []domain.Order{order("local-1", "ord-local-1", "BTCUSDT", broker.OrderPending)}}
	r := newReconciler(l, &fakeExchange{})
	d, err := r.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(l.orders) != 1 || l.orders[0].Status != broker.OrderCanceled {
		t.Fatalf("orden local muerta no cancelada: %+v", l.orders)
	}
	if len(d.Actions) != 1 {
		t.Fatalf("falta acción de cancelación: %+v", d.Actions)
	}
}

func TestRecoverUsesMarkPrice(t *testing.T) {
	ts := time.Now().UTC()
	l := &fakeLocal{
		positions: []domain.Position{pos(1, "BTCUSDT", domain.DirectionBuy, 0.1, 60000)},
		marks:     []domain.Mark{{Symbol: "BTCUSDT", Price: 65000, TS: ts}},
	}
	r := newReconciler(l, &fakeExchange{})
	if _, err := r.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(l.closed) != 1 || l.closed[0].ExitPrice != 65000 {
		t.Fatalf("cierre fantasma no usa mark price: %+v", l.closed)
	}
}
