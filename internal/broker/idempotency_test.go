package broker

import (
	"context"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func ibSignal(ts time.Time, dir domain.Direction, setup string) domain.SignalEvent {
	return domain.SignalEvent{
		StrategyID: "ib-mtf",
		Symbol:     "BTCUSDT",
		Timeframe:  "15m",
		BarTS:      ts,
		Direction:  dir,
		Price:      100,
		Meta:       map[string]any{domain.MetaKeySetup: setup},
	}
}

// TestSignalIdempotency verifica que el identificador de señal/orden es
// determinista: una repetición del WebSocket o un reinicio no crea una segunda
// orden (sec 24).
func TestSignalIdempotency(t *testing.T) {
	ts := time.UnixMilli(1700000000000)
	sig := ibSignal(ts, domain.DirectionBuy, "imm")

	// La misma señal (reintento del WebSocket) siempre produce el mismo ID.
	first := SignalClientID(sig)
	if got := SignalClientID(sig); got != first {
		t.Fatalf("misma señal → distinto id: %q vs %q", got, first)
	}

	// Cambios en cada componente deben producir un ID distinto.
	mutate := []struct {
		name string
		fn   func(*domain.SignalEvent)
	}{
		{"dirección", func(e *domain.SignalEvent) { e.Direction = domain.DirectionSell }},
		{"setup", func(e *domain.SignalEvent) { e.Meta[domain.MetaKeySetup] = "order_block" }},
		{"símbolo", func(e *domain.SignalEvent) { e.Symbol = "ETHUSDT" }},
		{"timeframe", func(e *domain.SignalEvent) { e.Timeframe = "5m" }},
		{"estrategia", func(e *domain.SignalEvent) { e.StrategyID = "chandelier" }},
		{"vela", func(e *domain.SignalEvent) { e.BarTS = e.BarTS.Add(time.Minute) }},
		{"sin setup", func(e *domain.SignalEvent) { delete(e.Meta, domain.MetaKeySetup) }},
	}
	for _, m := range mutate {
		other := sig
		m.fn(&other)
		if got := SignalClientID(other); got == first {
			t.Errorf("cambio de %s no altera el id de la orden", m.name)
		}
	}

	if first == "" || len(first) > 128 {
		t.Fatalf("id %q debe ser no vacío y de longitud razonable", first)
	}
}

// TestSignalIdempotencyNoDuplicateOrder prueba el flujo completo: mismo
// clientID derivado de la señal → PaperBroker no crea una segunda orden ni
// una segunda posición.
func TestSignalIdempotencyNoDuplicateOrder(t *testing.T) {
	b, s := newPaper(t)
	ctx := context.Background()
	ts := time.UnixMilli(1700000001000)
	sig := ibSignal(ts, domain.DirectionBuy, "imm")
	clientID := SignalClientID(sig)

	order := domain.Order{
		ClientID:   clientID,
		StrategyID: sig.StrategyID,
		Symbol:     sig.Symbol,
		Timeframe:  sig.Timeframe,
		Side:       sig.Direction,
		Quantity:   1,
		Price:      sig.Price,
		StopLoss:   97,
		TakeProfit: 109,
	}
	// Entrega original y reintentos (WS repetido / reinicio).
	first, err := b.PlaceOrder(ctx, order)
	if err != nil {
		t.Fatalf("PlaceOrder 1: %v", err)
	}
	for i := 0; i < 3; i++ {
		again, err := b.PlaceOrder(ctx, order)
		if err != nil {
			t.Fatalf("PlaceOrder reintento %d: %v", i, err)
		}
		if again.ClientID != first.ClientID || again.FilledPrice != first.FilledPrice {
			t.Fatalf("reintento %d re-fill: %+v vs %+v", i, again, first)
		}
	}
	all, err := s.ListOrders(ctx, "")
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("órdenes persistidas = %d, want 1 (idempotencia)", len(all))
	}
	open, _ := b.GetPositions(ctx)
	if len(open) != 1 {
		t.Fatalf("posiciones = %d, want 1", len(open))
	}
	if open[0].EntryTS.UnixMilli() != first.Time.UnixMilli() {
		t.Fatalf("segunda entrega podría duplicar la posición")
	}
}
