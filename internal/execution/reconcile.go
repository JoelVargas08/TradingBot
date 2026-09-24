// Package execution implementa la recuperación y conciliación del estado real
// de posiciones/órdenes contra el exchange (secciones 28-29 del plan).
package execution

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"tradingview-bot/internal/broker"
	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/observability"
)

// LocalStore es el estado local persistido (posiciones, órdenes y marks).
type LocalStore interface {
	OpenPositions(ctx context.Context) ([]domain.Position, error)
	OpenPosition(ctx context.Context, p domain.Position) (int64, error)
	ClosePosition(ctx context.Context, id int64, exitPrice float64, exitTS time.Time, opts domain.CloseOptions) (domain.Position, error)
	SaveOrder(ctx context.Context, o domain.Order) error
	ListOrders(ctx context.Context, status string) ([]domain.Order, error)
	Marks(ctx context.Context) ([]domain.Mark, error)
}

// Exchange es la fuente de verdad del estado real (WEEX en modo live).
type Exchange interface {
	GetPositions(ctx context.Context) ([]domain.Position, error)
	GetOpenOrders(ctx context.Context) ([]domain.Order, error)
}

// QuantityDelta reporta una posición cuya cantidad local difiere del exchange.
type QuantityDelta struct {
	Local       domain.Position
	ExchangeQty float64
}

// Diff describe las discrepancias entre el estado local y el exchange.
type Diff struct {
	LocalOnly      []domain.Position // local sí, exchange no → fantasma
	ExchangeOnly   []domain.Position // exchange sí, local no → adoptar
	QuantityDelta  []QuantityDelta   // cantidad distinta → exchange manda
	StaleOrders    []domain.Order    // pendientes locales que el exchange no conoce
	ExchangeOrders []domain.Order    // órdenes abiertas del exchange sin registro local
	Actions        []string          // correcciones aplicadas por Recover
	ReconciledAt   time.Time         // momento de la conciliación
}

// Empty indica si no hay discrepancias.
func (d Diff) Empty() bool {
	return len(d.LocalOnly) == 0 && len(d.ExchangeOnly) == 0 && len(d.QuantityDelta) == 0 &&
		len(d.StaleOrders) == 0 && len(d.ExchangeOrders) == 0
}

// Reconciler compara estado local vs exchange. WEEX es la fuente de verdad
// para la posición real: al recuperar, el estado local se corrige hacia el
// estado del exchange, nunca al revés.
type Reconciler struct {
	Local    LocalStore
	Exchange Exchange
	QtyTol   float64
	now      func() time.Time
}

func NewReconciler(local LocalStore, exchange Exchange) *Reconciler {
	return &Reconciler{
		Local:    local,
		Exchange: exchange,
		QtyTol:   1e-8,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

func positionKey(p domain.Position) string {
	return strings.ToUpper(strings.TrimSpace(p.Symbol)) + "|" + string(p.Side)
}

// Diff carga el estado local y el del exchange y reporta las discrepancias
// sin modificar nada.
func (r *Reconciler) Diff(ctx context.Context) (Diff, error) {
	localPos, err := r.Local.OpenPositions(ctx)
	if err != nil {
		return Diff{}, fmt.Errorf("leyendo posiciones locales: %w", err)
	}
	exPos, err := r.Exchange.GetPositions(ctx)
	if err != nil {
		return Diff{}, fmt.Errorf("leyendo posiciones del exchange: %w", err)
	}
	d := Diff{ReconciledAt: r.now()}

	exMap := make(map[string]domain.Position, len(exPos))
	for _, ep := range exPos {
		exMap[positionKey(ep)] = ep
	}
	for _, lp := range localPos {
		key := positionKey(lp)
		ep, ok := exMap[key]
		if !ok {
			d.LocalOnly = append(d.LocalOnly, lp)
			continue
		}
		if math.Abs(lp.Quantity-ep.Quantity) > r.QtyTol {
			d.QuantityDelta = append(d.QuantityDelta, QuantityDelta{Local: lp, ExchangeQty: ep.Quantity})
		}
		delete(exMap, key)
	}
	for _, ep := range exMap {
		d.ExchangeOnly = append(d.ExchangeOnly, ep)
	}
	sort.Slice(d.ExchangeOnly, func(i, j int) bool {
		return positionKey(d.ExchangeOnly[i]) < positionKey(d.ExchangeOnly[j])
	})

	localPending, err := r.Local.ListOrders(ctx, broker.OrderPending)
	if err != nil {
		return Diff{}, fmt.Errorf("leyendo órdenes locales: %w", err)
	}
	exOpen, err := r.Exchange.GetOpenOrders(ctx)
	if err != nil {
		return Diff{}, fmt.Errorf("leyendo órdenes del exchange: %w", err)
	}
	exIdx := make(map[string]bool, len(exOpen))
	for _, eo := range exOpen {
		if eo.ID != "" {
			exIdx["id:"+eo.ID] = true
		}
		if eo.ClientID != "" {
			exIdx["client:"+eo.ClientID] = true
		}
	}
	for _, lo := range localPending {
		if !exIdx["id:"+lo.ID] && !exIdx["client:"+lo.ClientID] {
			d.StaleOrders = append(d.StaleOrders, lo)
		}
	}
	localIdx := make(map[string]bool, len(localPending))
	for _, lo := range localPending {
		if lo.ID != "" {
			localIdx["id:"+lo.ID] = true
		}
		if lo.ClientID != "" {
			localIdx["client:"+lo.ClientID] = true
		}
	}
	for _, eo := range exOpen {
		if eo.ID != "" && eo.ClientID != "" {
			if !localIdx["id:"+eo.ID] && !localIdx["client:"+eo.ClientID] {
				d.ExchangeOrders = append(d.ExchangeOrders, eo)
			}
		} else if eo.ID != "" && !localIdx["id:"+eo.ID] {
			d.ExchangeOrders = append(d.ExchangeOrders, eo)
		} else if eo.ClientID != "" && !localIdx["client:"+eo.ClientID] {
			d.ExchangeOrders = append(d.ExchangeOrders, eo)
		}
	}
	return d, nil
}

// Recover ejecuta la recuperación tras reinicio (sec 29): carga estado local,
// consulta el exchange, reconcilia con el exchange como fuente de verdad y
// devuelve el diff con las acciones aplicadas. Nunca asume que un reinicio
// significa que no hay posición.
func (r *Reconciler) Recover(ctx context.Context) (Diff, error) {
	d, err := r.Diff(ctx)
	if err != nil {
		return Diff{}, err
	}
	if d.Empty() {
		return d, nil
	}
	now := d.ReconciledAt
	defer observability.Log(observability.Reconciliation,
		"ghost_closed", len(d.LocalOnly),
		"adopted", len(d.ExchangeOnly),
		"qty_fixed", len(d.QuantityDelta),
		"orders_canceled", len(d.StaleOrders),
		"orders_unknown", len(d.ExchangeOrders))

	for _, p := range d.LocalOnly {
		px := r.markPrice(ctx, p.Symbol, p.EntryPrice)
		if _, err := r.Local.ClosePosition(ctx, p.ID, px, now, domain.CloseOptions{
			Reason: "reconcile: posición local sin contraparte en exchange",
		}); err != nil {
			return d, fmt.Errorf("cerrando posición fantasma #%d: %w", p.ID, err)
		}
		d.Actions = append(d.Actions, fmt.Sprintf(
			"cerrada posición local fantasma #%d %s %s qty=%.8f (el exchange no la reporta)",
			p.ID, p.Symbol, p.Side, p.Quantity))
		log.Printf("reconcile: cerrada posición fantasma #%d %s qty=%.8f", p.ID, p.Symbol, p.Quantity)
	}

	for _, q := range d.QuantityDelta {
		lp := q.Local
		px := r.markPrice(ctx, lp.Symbol, lp.EntryPrice)
		if _, err := r.Local.ClosePosition(ctx, lp.ID, px, now, domain.CloseOptions{
			Reason: "reconcile: ajuste de cantidad local→exchange",
		}); err != nil {
			return d, fmt.Errorf("cerrando posición #%d para ajustar cantidad: %w", lp.ID, err)
		}
		entry, epx := lp.EntryPrice, lp.EntryTS
		if ep := r.exchangePosition(ctx, lp); ep != nil {
			if ep.EntryPrice > 0 {
				entry = ep.EntryPrice
			}
			if !ep.EntryTS.IsZero() {
				epx = ep.EntryTS
			}
		}
		adopt := lp
		adopt.ID = 0
		adopt.Quantity = q.ExchangeQty
		adopt.EntryPrice = entry
		adopt.EntryTS = epx
		adopt.Status = domain.PositionOpen
		adopt.ExitTS = time.Time{}
		adopt.ExitPrice = 0
		adopt.ExitReason = ""
		adopt.PnL, adopt.NetPnL, adopt.GrossPnL = 0, 0, 0
		if _, err := r.Local.OpenPosition(ctx, adopt); err != nil {
			return d, fmt.Errorf("reabriendo posición #%d con cantidad del exchange: %w", lp.ID, err)
		}
		d.Actions = append(d.Actions, fmt.Sprintf(
			"ajustada cantidad %s %s: local %.8f → exchange %.8f",
			lp.Symbol, lp.Side, lp.Quantity, q.ExchangeQty))
		log.Printf("reconcile: cantidad ajustada %s local=%.8f exchange=%.8f", lp.Symbol, lp.Quantity, q.ExchangeQty)
	}

	for _, ep := range d.ExchangeOnly {
		adopt := ep
		adopt.ID = 0
		adopt.Status = domain.PositionOpen
		if adopt.StrategyID == "" {
			adopt.StrategyID = "recovered-exchange"
		}
		if _, err := r.Local.OpenPosition(ctx, adopt); err != nil {
			return d, fmt.Errorf("adoptando posición del exchange %s: %w", positionKey(ep), err)
		}
		d.Actions = append(d.Actions, fmt.Sprintf(
			"adoptada posición del exchange %s %s qty=%.8f entry=%.8f (existía solo en el exchange)",
			ep.Symbol, ep.Side, ep.Quantity, ep.EntryPrice))
		log.Printf("reconcile: adoptada posición del exchange %s %s qty=%.8f", ep.Symbol, ep.Side, ep.Quantity)
	}

	for _, o := range d.StaleOrders {
		o.Status = broker.OrderCanceled
		o.UpdatedAt = now
		if err := r.Local.SaveOrder(ctx, o); err != nil {
			return d, fmt.Errorf("cancelando orden local %s: %w", o.ClientID, err)
		}
		d.Actions = append(d.Actions, fmt.Sprintf(
			"cancelada orden local %s %s (desconocida por el exchange)",
			o.ClientID, o.Symbol))
		log.Printf("reconcile: orden local cancelada %s", o.ClientID)
	}

	return d, nil
}

// markPrice devuelve el último precio conocido del símbolo (cualquier
// timeframe); fallback al precio indicado si no hay marks.
func (r *Reconciler) markPrice(ctx context.Context, symbol string, fallback float64) float64 {
	marks, err := r.Local.Marks(ctx)
	if err != nil {
		return fallback
	}
	var best domain.Mark
	for _, m := range marks {
		if !strings.EqualFold(m.Symbol, symbol) {
			continue
		}
		if best.TS.IsZero() || m.TS.After(best.TS) {
			best = m
		}
	}
	if best.Price > 0 {
		return best.Price
	}
	return fallback
}

func (r *Reconciler) exchangePosition(ctx context.Context, lp domain.Position) *domain.Position {
	exPos, err := r.Exchange.GetPositions(ctx)
	if err != nil {
		return nil
	}
	for i := range exPos {
		if positionKey(exPos[i]) == positionKey(lp) {
			return &exPos[i]
		}
	}
	return nil
}
