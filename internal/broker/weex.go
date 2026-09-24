package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"tradingview-bot/internal/domain"
)

// WeexBroker implementa Broker contra la API de contratos WEEX usando el
// clientOrderID determinista (IB-{strategy}-{symbol}-{ts}-{direction}) como
// clientOrderId para garantizar idempotencia del lado del exchange.
type WeexBroker struct {
	client *WeexClient
}

func NewWeexBroker(cfg WeexConfig) *WeexBroker {
	return &WeexBroker{client: NewWeexClient(cfg)}
}

// --- Órdenes ---

func (b *WeexBroker) PlaceOrder(ctx context.Context, order domain.Order) (domain.Order, error) {
	if order.ClientID == "" {
		return domain.Order{}, errNoClientID
	}
	if !order.Side.Valid() {
		return domain.Order{}, fmt.Errorf("weex: side inválido: %q", order.Side)
	}
	if order.Quantity <= 0 {
		return domain.Order{}, fmt.Errorf("weex: quantity debe ser mayor que 0")
	}
	orderType := "LIMIT"
	if order.Price <= 0 {
		orderType = "MARKET"
	}
	body := map[string]any{
		"symbol":        order.Symbol,
		"side":          weexSide(order.Side),
		"orderType":     orderType,
		"quantity":      order.Quantity,
		"clientOrderId": order.ClientID,
	}
	if order.Price > 0 {
		body["price"] = order.Price
	}
	if order.StopLoss > 0 {
		body["stopLossPrice"] = order.StopLoss
	}
	if order.TakeProfit > 0 {
		body["takeProfitPrice"] = order.TakeProfit
	}
	var raw json.RawMessage
	if err := b.client.doAuth(ctx, http.MethodPost, pathOrderCreate, nil, body, &raw); err != nil {
		return domain.Order{}, err
	}
	orders, err := parseWeexOrders(raw)
	if err != nil {
		return domain.Order{}, fmt.Errorf("weex order/create: %w", err)
	}
	if len(orders) == 0 {
		orders = []weexOrder{{
			ClientOrderID: order.ClientID,
			Symbol:        order.Symbol,
			Side:          weexSide(order.Side),
			Price:         order.Price,
			Quantity:      order.Quantity,
			Status:        OrderPending,
		}}
	}
	wo := orders[0]
	out := domain.Order{
		Symbol:      firstNonEmpty(wo.Symbol, order.Symbol),
		Timeframe:   order.Timeframe,
		Side:        weexSideToDomain(wo.Side, order.Side),
		Quantity:    firstPos(wo.Quantity, order.Quantity),
		Price:       firstPos(wo.Price, order.Price),
		StopLoss:    firstPos(wo.StopLoss, order.StopLoss),
		TakeProfit:  firstPos(wo.TakeProfit, order.TakeProfit),
		ClientID:    firstNonEmpty(wo.ClientOrderID, order.ClientID),
		Status:      weexStatusToDomain(wo.Status),
		FilledPrice: wo.FilledPrice,
		FilledQty:   wo.FilledQty,
		Time:        order.Time,
		UpdatedAt:   order.Time,
	}
	out.ID = firstNonEmpty(wo.OrderID, out.ClientID)
	return out, nil
}

func (b *WeexBroker) CancelOrder(ctx context.Context, orderID string) error {
	if orderID == "" {
		return fmt.Errorf("weex: orderID requerido")
	}
	body := map[string]any{"clientOrderId": orderID}
	return b.client.doAuth(ctx, http.MethodPost, pathOrderCancel, nil, body, nil)
}

func (b *WeexBroker) GetOrder(ctx context.Context, orderID string) (domain.Order, error) {
	q := url.Values{"clientOrderId": {orderID}}
	orders, err := b.listOrders(ctx, q)
	if err != nil {
		return domain.Order{}, err
	}
	if len(orders) == 0 {
		return domain.Order{}, domain.ErrNotFound
	}
	return orders[0], nil
}

func (b *WeexBroker) GetOpenOrders(ctx context.Context) ([]domain.Order, error) {
	orders, err := b.listOrders(ctx, url.Values{})
	if err != nil {
		return nil, err
	}
	open := make([]domain.Order, 0, len(orders))
	for _, o := range orders {
		if weexStatusIsOpen(o.Status) {
			open = append(open, o)
		}
	}
	return open, nil
}

func (b *WeexBroker) listOrders(ctx context.Context, query url.Values) ([]domain.Order, error) {
	var raw json.RawMessage
	if err := b.client.doAuth(ctx, http.MethodGet, pathOrderList, query, nil, &raw); err != nil {
		return nil, err
	}
	items, err := parseWeexOrders(raw)
	if err != nil {
		return nil, fmt.Errorf("weex order/list: %w", err)
	}
	out := make([]domain.Order, 0, len(items))
	for _, wo := range items {
		o := domain.Order{
			ID:          wo.OrderID,
			ClientID:    wo.ClientOrderID,
			Symbol:      wo.Symbol,
			Timeframe:   "",
			Side:        weexSideToDomain(wo.Side, ""),
			Quantity:    wo.Quantity,
			Price:       wo.Price,
			StopLoss:    wo.StopLoss,
			TakeProfit:  wo.TakeProfit,
			Status:      weexStatusToDomain(wo.Status),
			FilledPrice: wo.FilledPrice,
			FilledQty:   wo.FilledQty,
		}
		if wo.CreatedAt > 0 {
			o.Time = unixMillis(wo.CreatedAt)
		}
		out = append(out, o)
	}
	return out, nil
}

// --- Posiciones ---

func (b *WeexBroker) GetPosition(ctx context.Context, symbol string) (domain.Position, error) {
	q := url.Values{"symbol": {symbol}}
	positions, err := b.listPositions(ctx, q)
	if err != nil {
		return domain.Position{}, err
	}
	for _, p := range positions {
		if strings.EqualFold(p.Symbol, symbol) {
			return p, nil
		}
	}
	return domain.Position{}, domain.ErrNotFound
}

func (b *WeexBroker) GetPositions(ctx context.Context) ([]domain.Position, error) {
	return b.listPositions(ctx, url.Values{})
}

func (b *WeexBroker) listPositions(ctx context.Context, query url.Values) ([]domain.Position, error) {
	var raw json.RawMessage
	if err := b.client.doAuth(ctx, http.MethodGet, pathPositionList, query, nil, &raw); err != nil {
		return nil, err
	}
	items, err := parseWeexPositions(raw)
	if err != nil {
		return nil, fmt.Errorf("weex position/list: %w", err)
	}
	out := make([]domain.Position, 0, len(items))
	for _, wp := range items {
		out = append(out, domain.Position{
			Symbol:      wp.Symbol,
			Side:        wp.Side,
			EntryPrice:  wp.EntryPrice,
			Quantity:    wp.Quantity,
			StopLoss:    wp.StopLoss,
			TakeProfit:  wp.TakeProfit,
			PnL:         wp.UnrealizedPnL,
			Status:      domain.PositionOpen,
		})
	}
	return out, nil
}

func (b *WeexBroker) GetBalance(ctx context.Context) (domain.Account, error) {
	var raw json.RawMessage
	if err := b.client.doAuth(ctx, http.MethodGet, pathAccountList, url.Values{}, nil, &raw); err != nil {
		return domain.Account{}, err
	}
	accounts, err := parseWeexAccounts(raw)
	if err != nil {
		return domain.Account{}, fmt.Errorf("weex account/list: %w", err)
	}
	if len(accounts) == 0 {
		return domain.Account{}, domain.ErrNotFound
	}
	return accounts[0], nil
}

func (b *WeexBroker) Close(ctx context.Context) error {
	return nil
}

// --- Mapeo de campos WEEX (tolerante) y de estados ---

type weexOrder struct {
	OrderID       string
	ClientOrderID string
	Symbol        string
	Side          string
	Status        string
	Price         float64
	Quantity      float64
	FilledPrice   float64
	FilledQty     float64
	CreatedAt     int64
	StopLoss      float64
	TakeProfit    float64
}

type weexPosition struct {
	Symbol         string
	Side           domain.Direction
	EntryPrice     float64
	Quantity       float64
	StopLoss       float64
	TakeProfit     float64
	UnrealizedPnL  float64
	MarkPrice        float64
	LiquidationPrice float64
}

type weexAccount struct {
	Balance       float64
	Equity        float64
	Available     float64
	UnrealizedPnL float64
}

func parseWeexOrders(raw json.RawMessage) ([]weexOrder, error) {
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		var one map[string]any
		if err2 := json.Unmarshal(raw, &one); err2 != nil {
			return nil, fmt.Errorf("orden inválida: %w", err2)
		}
		items = []map[string]any{one}
	}
	out := make([]weexOrder, 0, len(items))
	for _, it := range items {
		out = append(out, weexOrder{
			OrderID:       flexString(it, "orderId", "order_id", "orderID", "id"),
			ClientOrderID: flexString(it, "clientOrderId", "client_order_id", "clientOrderID", "clOrdId"),
			Symbol:        flexString(it, "symbol", "contract", "contractName"),
			Side:          strings.ToUpper(flexString(it, "side", "direction")),
			Status:        flexString(it, "status", "state", "orderStatus"),
			Price:         flexFloat(it, "price", "orderPrice", "limitPrice"),
			Quantity:      flexFloat(it, "quantity", "qty", "size", "volume"),
			FilledPrice:   flexFloat(it, "filledPrice", "avgPrice", "averagePrice", "fillPrice"),
			FilledQty:     flexFloat(it, "filledQuantity", "filledQty", "cumQty", "executedQty"),
			CreatedAt:     flexInt64(it, "timestamp", "createdTime", "createTime", "ts"),
			StopLoss:      flexFloat(it, "stopLossPrice", "stopLoss", "slPrice"),
			TakeProfit:    flexFloat(it, "takeProfitPrice", "takeProfit", "tpPrice"),
		})
	}
	return out, nil
}

func parseWeexPositions(raw json.RawMessage) ([]weexPosition, error) {
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		var one map[string]any
		if err2 := json.Unmarshal(raw, &one); err2 != nil {
			return nil, fmt.Errorf("posición inválida: %w", err2)
		}
		items = []map[string]any{one}
	}
	out := make([]weexPosition, 0, len(items))
	for _, it := range items {
		sd := strings.ToUpper(flexString(it, "side", "positionSide", "direction"))
		qty := flexFloat(it, "quantity", "qty", "size", "posAmount", "positionQty")
		var dir domain.Direction
		switch {
		case strings.Contains(sd, "LONG") || strings.Contains(sd, "BUY") || qty > 0:
			dir = domain.DirectionBuy
		case strings.Contains(sd, "SHORT") || strings.Contains(sd, "SELL") || qty < 0:
			dir = domain.DirectionSell
		}
		if qty < 0 {
			qty = -qty
		}
		out = append(out, weexPosition{
			Symbol:         flexString(it, "symbol", "contract", "contractName"),
			Side:           dir,
			EntryPrice:     flexFloat(it, "entryPrice", "avgEntryPrice", "openPrice", "costPrice"),
			Quantity:       qty,
			StopLoss:       flexFloat(it, "stopLossPrice", "stopLoss", "slPrice"),
			TakeProfit:     flexFloat(it, "takeProfitPrice", "takeProfit", "tpPrice"),
			UnrealizedPnL:  flexFloat(it, "unrealizedPnL", "unrealizedProfit", "totalProfit"),
			MarkPrice:      flexFloat(it, "markPrice", "currentPrice"),
			LiquidationPrice: flexFloat(it, "liquidationPrice", "liqPrice"),
		})
	}
	return out, nil
}

func parseWeexAccounts(raw json.RawMessage) ([]domain.Account, error) {
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		var one map[string]any
		if err2 := json.Unmarshal(raw, &one); err2 != nil {
			return nil, fmt.Errorf("cuenta inválida: %w", err2)
		}
		items = []map[string]any{one}
	}
	out := make([]domain.Account, 0, len(items))
	for _, it := range items {
		bal := flexFloat(it, "balance", "availableBalance", "walletBalance")
		equity := flexFloat(it, "equity", "equityValue")
		if equity == 0 {
			equity = bal + flexFloat(it, "unrealizedPnL", "unrealizedProfit")
		}
		out = append(out, domain.Account{
			Balance:       bal,
			Equity:        equity,
			UnrealizedPnL: flexFloat(it, "unrealizedPnL", "unrealizedProfit"),
			UpdatedAt:     timeNowUTC(),
		})
	}
	return out, nil
}

func weexSide(side domain.Direction) string {
	if side == domain.DirectionBuy {
		return "BUY"
	}
	return "SELL"
}

func weexSideToDomain(side string, fallback domain.Direction) domain.Direction {
	switch {
	case strings.Contains(side, "BUY"), strings.Contains(side, "LONG"):
		return domain.DirectionBuy
	case strings.Contains(side, "SELL"), strings.Contains(side, "SHORT"):
		return domain.DirectionSell
	}
	return fallback
}

// weexStatusToDomain centraliza el mapeo de estados de orden WEEX a los
// estados internos. Supuesto documentado: 0/1 = pendiente, 2 = llenada,
// 3/4/5/6 = cancelada/rechazada/vencida. Si la doc real difiere, se ajusta
// aquí únicamente.
func weexStatusToDomain(status string) string {
	s := strings.ToLower(strings.TrimSpace(status))
	switch s {
	case "2":
		return OrderFilled
	case "3", "4", "5", "6", "7":
		return OrderCanceled
	}
	switch {
	case strings.Contains(s, "fill"), strings.Contains(s, "done"), strings.Contains(s, "complete"):
		return OrderFilled
	case strings.Contains(s, "cancel"), strings.Contains(s, "reject"), strings.Contains(s, "expire"):
		return OrderCanceled
	}
	return OrderPending
}

// weexStatusIsOpen es conservador: cualquier estado no reconocido se trata
// como abierto para que la reconciliación lo supervise (nunca se pierde un
// fill pendiente).
func weexStatusIsOpen(status string) bool {
	if status == "" {
		return true
	}
	return weexStatusToDomain(status) == OrderPending
}

func firstPos(vals ...float64) float64 {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func flexString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
			if n, ok := v.(float64); ok && n != 0 {
				return strconv.FormatFloat(n, 'f', -1, 64)
			}
		}
	}
	return ""
}

func flexFloat(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch n := v.(type) {
			case float64:
				return n
			case int:
				return float64(n)
			case int64:
				return float64(n)
			case string:
				if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
					return f
				}
			}
		}
	}
	return 0
}

func flexInt64(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch n := v.(type) {
			case float64:
				return int64(n)
			case int64:
				return n
			case int:
				return int64(n)
			case string:
				if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
					return i
				}
			}
		}
	}
	return 0
}

func unixMillis(ms int64) (t time.Time) {
	if ms > 0 {
		t = time.UnixMilli(ms)
	}
	return t
}

func timeNowUTC() time.Time {
	return time.Now().UTC()
}