package broker

import (
	"context"
	"strings"

	"tradingview-bot/internal/domain"
)

// Orden interna (estado persistido en la tabla orders, columna status).
const (
	OrderPending  = "pending"
	OrderFilled   = "filled"
	OrderCanceled = "canceled"
	OrderRejected = "rejected"
)

// SignalClientID deriva el clientOrderID determinista de una señal a partir de
// su IdempotencyKey (strategy|symbol|timeframe|candle_ts|setup|direction):
// una repetición del WebSocket o un reinicio produce la MISMA orden, nunca
// una segunda. El caracter "|" se sustituye por "-" para cumplir el formato de
// client order id de los exchanges.
func SignalClientID(ev domain.SignalEvent) string {
	var b strings.Builder
	b.Grow(len(ev.IdempotencyKey()))
	for _, r := range ev.IdempotencyKey() {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// Broker abstrae un broker de contratos (simulado o real). La estrategia y el
// Risk Engine nunca hablan HTTP/REST: solo esta interfaz.
type Broker interface {
	PlaceOrder(ctx context.Context, order domain.Order) (domain.Order, error)
	CancelOrder(ctx context.Context, orderID string) error
	GetOrder(ctx context.Context, orderID string) (domain.Order, error)
	GetOpenOrders(ctx context.Context) ([]domain.Order, error)
	GetPosition(ctx context.Context, symbol string) (domain.Position, error)
	GetPositions(ctx context.Context) ([]domain.Position, error)
	GetBalance(ctx context.Context) (domain.Account, error)
	Close(ctx context.Context) error
}

// OrderStore persiste/consulta órdenes. *store.Store lo implementa
// (SaveOrder/OrderByClientID/ListOrders con la tabla orders).
type OrderStore interface {
	SaveOrder(ctx context.Context, o domain.Order) error
	OrderByClientID(ctx context.Context, clientID string) (domain.Order, error)
	ListOrders(ctx context.Context, status string) ([]domain.Order, error)
}

var _ Broker = (*PaperBroker)(nil)
var _ Broker = (*WeexBroker)(nil)
