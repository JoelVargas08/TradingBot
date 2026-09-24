package broker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/observability"
)

// PaperConfig define los costes simulados del paper broker.
type PaperConfig struct {
	FeeRate         float64
	SlippageRate    float64
	StartingBalance float64
}

func (c PaperConfig) withDefaults() PaperConfig {
	if c.FeeRate <= 0 {
		c.FeeRate = 0.001
	}
	if c.SlippageRate <= 0 {
		c.SlippageRate = 0.0002
	}
	if c.StartingBalance <= 0 {
		c.StartingBalance = 10000
	}
	return c
}

// PaperBroker implementa Broker simulando un broker de contratos: órdenes de
// mercado → fill inmediato, slippage y fees aplicados UNA sola vez (slippage en
// el precio de fill, fees al cerrar la posición), posiciones locales, SL/TP con
// velas cerradas, balance y PnL. Cada orden se persiste vía OrderStore
// (SaveOrder) y se consulta con OrderByClientID/ListOrders, por lo que un
// reinicio no pierde el estado.
type PaperBroker struct {
	cfg    PaperConfig
	store  domain.PositionStore
	orders OrderStore
}

// NewPaperBroker construye un broker simulado sobre el PositionStore local
// (posiciones/cuenta) y el OrderStore (órdenes persistidas).
func NewPaperBroker(store domain.PositionStore, orders OrderStore, cfg PaperConfig) *PaperBroker {
	return &PaperBroker{cfg: cfg.withDefaults(), store: store, orders: orders}
}

var errNoClientID = fmt.Errorf("client_id requerido: la idempotencia exige un clientOrderID determinista")

// PlaceOrder llena una orden de mercado aplicando slippage una sola vez,
// persiste el fill y abre la posición simulada sin duplicar por client_id.
func (p *PaperBroker) PlaceOrder(ctx context.Context, order domain.Order) (domain.Order, error) {
	if order.ClientID == "" {
		observability.Log(observability.OrderRejected, "reason", "client_id requerido (idempotencia)")
		return domain.Order{}, errNoClientID
	}
	if !order.Side.Valid() {
		observability.Log(observability.OrderRejected, "symbol", order.Symbol, "reason", "side inválido")
		return domain.Order{}, fmt.Errorf("side inválido: %q", order.Side)
	}
	if order.Quantity <= 0 || order.Price <= 0 {
		observability.Log(observability.OrderRejected, "symbol", order.Symbol, "reason", "quantity o price no positivos")
		return domain.Order{}, fmt.Errorf("quantity y price deben ser mayores que 0")
	}
	ok, err := p.idempotentLookup(ctx, order.ClientID)
	if err != nil {
		return domain.Order{}, err
	}
	if ok {
		return p.orders.OrderByClientID(ctx, order.ClientID)
	}
	if err := p.ensureAccount(ctx); err != nil {
		return domain.Order{}, err
	}

	fill := fillPrice(order.Side, order.Price, p.cfg.SlippageRate)

	now := time.Now().UTC()
	o := domain.Order{
		ID:          order.ClientID,
		ClientID:    order.ClientID,
		StrategyID:  order.StrategyID,
		Symbol:      order.Symbol,
		Timeframe:   order.Timeframe,
		Side:        order.Side,
		Quantity:    order.Quantity,
		Price:       order.Price,
		StopLoss:    order.StopLoss,
		TakeProfit:  order.TakeProfit,
		Status:      OrderFilled,
		FilledPrice: fill,
		FilledQty:   order.Quantity,
		Time:        now,
		UpdatedAt:   now,
	}
	observability.Log(observability.OrderCreated, "client_id", o.ClientID, "strategy", o.StrategyID, "symbol", o.Symbol, "timeframe", o.Timeframe, "side", o.Side, "qty", o.Quantity)
	if err := p.orders.SaveOrder(ctx, o); err != nil {
		return domain.Order{}, err
	}
	if _, err := p.store.OpenPosition(ctx, domain.Position{
		StrategyID: order.StrategyID,
		Symbol:     order.Symbol,
		Timeframe:  order.Timeframe,
		Side:       order.Side,
		EntryTS:    now,
		EntryPrice: fill,
		StopLoss:   order.StopLoss,
		TakeProfit: order.TakeProfit,
		Quantity:   order.Quantity,
		RiskAmount: riskAmount(order.Quantity, fill, order.StopLoss),
		Status:     domain.PositionOpen,
	}); err != nil {
		if errors.Is(err, domain.ErrDuplicate) {
			log.Printf("paper: posición %s/%s ya abierta; orden %s persistida (sin duplicado)", order.Symbol, order.Timeframe, o.ClientID)
			return o, nil
		}
		return domain.Order{}, fmt.Errorf("paper: abriendo posición: %w", err)
	}
	log.Printf("paper: orden %s llenada a %.6f (slippage aplicado una vez); fees se aplican al cierre", o.ClientID, fill)
	observability.Log(observability.OrderFilled, "client_id", o.ClientID, "symbol", o.Symbol, "side", o.Side, "fill", fill, "qty", o.Quantity)
	observability.Log(observability.PositionOpened, "symbol", o.Symbol, "side", o.Side, "qty", o.Quantity, "entry", fill)
	return o, nil
}

// idempotentLookup devuelve true si ya existe una orden persistida con ese
// client_id (una repetición del WS o de la señal no debe crear una segunda).
func (p *PaperBroker) idempotentLookup(ctx context.Context, clientID string) (bool, error) {
	_, err := p.orders.OrderByClientID(ctx, clientID)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	return false, err
}

func (p *PaperBroker) ensureAccount(ctx context.Context) error {
	if _, err := p.store.GetAccount(ctx); err == nil {
		return nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	start := p.cfg.StartingBalance
	return p.store.UpdateAccount(ctx, domain.Account{
		Balance:        start,
		Equity:         start,
		InitialBalance: start,
		PeakEquity:     start,
		UpdatedAt:      time.Now(),
	})
}

func (p *PaperBroker) CancelOrder(ctx context.Context, orderID string) error {
	o, err := p.orders.OrderByClientID(ctx, orderID)
	if err != nil {
		return err
	}
	if o.Status != OrderPending {
		return fmt.Errorf("paper: la orden %s no está pendiente (status=%s)", orderID, o.Status)
	}
	o.Status = OrderCanceled
	o.UpdatedAt = time.Now().UTC()
	return p.orders.SaveOrder(ctx, o)
}

func (p *PaperBroker) GetOrder(ctx context.Context, orderID string) (domain.Order, error) {
	return p.orders.OrderByClientID(ctx, orderID)
}

func (p *PaperBroker) GetOpenOrders(ctx context.Context) ([]domain.Order, error) {
	return p.orders.ListOrders(ctx, OrderPending)
}

func (p *PaperBroker) GetPosition(ctx context.Context, symbol string) (domain.Position, error) {
	open, err := p.store.OpenPositions(ctx)
	if err != nil {
		return domain.Position{}, err
	}
	for _, pos := range open {
		if pos.Symbol == symbol {
			return pos, nil
		}
	}
	return domain.Position{}, domain.ErrNotFound
}

func (p *PaperBroker) GetPositions(ctx context.Context) ([]domain.Position, error) {
	return p.store.OpenPositions(ctx)
}

func (p *PaperBroker) GetBalance(ctx context.Context) (domain.Account, error) {
	if err := p.ensureAccount(ctx); err != nil {
		return domain.Account{}, err
	}
	return p.store.GetAccount(ctx)
}

func (p *PaperBroker) Close(ctx context.Context) error {
	return nil
}

// CheckStops revisa una vela cerrada y cierra las posiciones cuyo SL/TP se
// tocó. En barras ambiguas (SL y TP en la misma vela) gana el stop.
func (p *PaperBroker) CheckStops(ctx context.Context, k domain.Kline) {
	if !k.Closed {
		return
	}
	open, err := p.store.OpenPositions(ctx)
	if err != nil {
		log.Printf("paper broker: leyendo posiciones abiertas: %v", err)
		return
	}
	for _, pos := range open {
		if pos.Symbol != k.Symbol || pos.Timeframe != k.Timeframe {
			continue
		}
		p.checkPosition(ctx, pos, k)
	}
}

func (p *PaperBroker) checkPosition(ctx context.Context, pos domain.Position, k domain.Kline) {
	var hitStop, hitTarget bool
	switch pos.Side {
	case domain.DirectionBuy:
		hitStop = pos.StopLoss > 0 && k.Low <= pos.StopLoss
		hitTarget = pos.TakeProfit > 0 && k.High >= pos.TakeProfit
	case domain.DirectionSell:
		hitStop = pos.StopLoss > 0 && k.High >= pos.StopLoss
		hitTarget = pos.TakeProfit > 0 && k.Low <= pos.TakeProfit
	}
	ambiguous := hitStop && hitTarget
	switch {
	case ambiguous:
		if err := p.ClosePosition(ctx, pos.ID, pos.StopLoss, "stop"); err != nil {
			log.Printf("paper broker: cerrando %d por stop (ambiguo): %v", pos.ID, err)
		}
	case hitStop:
		if err := p.ClosePosition(ctx, pos.ID, pos.StopLoss, "stop"); err != nil {
			log.Printf("paper broker: cerrando %d por stop: %v", pos.ID, err)
		}
	case hitTarget:
		if err := p.ClosePosition(ctx, pos.ID, pos.TakeProfit, "take-profit"); err != nil {
			log.Printf("paper broker: cerrando %d por take-profit: %v", pos.ID, err)
		}
	}
}

// ClosePosition cierra una posición simulada aplicando fees una sola vez y
// slippage una sola vez (dentro del precio de salida).
func (p *PaperBroker) ClosePosition(ctx context.Context, id int64, markPrice float64, reason string) error {
	pos, err := p.openPositionByID(ctx, id)
	if err != nil {
		return err
	}
	exit := fillPrice(opposite(pos.Side), markPrice, p.cfg.SlippageRate)
	opts := domain.CloseOptions{
		Reason:        reason,
		EntryFee:      pos.EntryPrice * pos.Quantity * p.cfg.FeeRate,
		ExitFee:       exit * pos.Quantity * p.cfg.FeeRate,
		SlippageEntry: 0,
		SlippageExit:  0,
	}
	if _, err := p.store.ClosePosition(ctx, id, exit, time.Now().UTC(), opts); err != nil {
		return err
	}
	observability.Log(observability.PositionClosed, "symbol", pos.Symbol, "side", pos.Side, "qty", pos.Quantity, "exit", exit, "reason", reason)
	return nil
}

func (p *PaperBroker) openPositionByID(ctx context.Context, id int64) (domain.Position, error) {
	open, err := p.store.OpenPositions(ctx)
	if err != nil {
		return domain.Position{}, err
	}
	for _, pos := range open {
		if pos.ID == id {
			return pos, nil
		}
	}
	return domain.Position{}, fmt.Errorf("posición abierta %d no encontrada", id)
}

func fillPrice(side domain.Direction, price, slip float64) float64 {
	if side == domain.DirectionSell {
		return price * (1 - slip)
	}
	return price * (1 + slip)
}

func opposite(side domain.Direction) domain.Direction {
	if side == domain.DirectionBuy {
		return domain.DirectionSell
	}
	return domain.DirectionBuy
}

func riskAmount(qty, entry, stop float64) float64 {
	if stop <= 0 {
		return 0
	}
	return qty * absFloat(entry-stop)
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
