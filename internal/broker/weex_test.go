package broker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"tradingview-bot/internal/domain"
)

const (
	mockAPIKey    = "test-api-key"
	mockAPISecret = "i-never-log-this-secret"
)

// mockSigno replica el algoritmo de firma del cliente para verificar la
// request en el servidor de prueba (sin exponer el secret al log).
func mockSigno(timestamp, query string, body []byte) string {
	msg := timestamp
	if query != "" {
		msg += "&" + query
	}
	if len(body) > 0 {
		msg += "&" + string(body)
	}
	mac := hmac.New(sha256.New, []byte(mockAPISecret))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

func newWeexMock(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get(headerAccessKey), mockAPIKey; got != want {
			t.Errorf("X-ACCESS-KEY = %q, want %q", got, want)
		}
		ts := r.Header.Get(headerAccessTS)
		if ts == "" {
			t.Error("faltó X-ACCESS-TIMESTAMP")
		}
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
		}
		want := mockSigno(ts, r.URL.RawQuery, body)
		if got := r.Header.Get(headerAccessSign); got != want {
			t.Errorf("X-ACCESS-SIGN = %q, want %q", got, want)
		}
		switch r.URL.Path {
		case pathOrderCreate:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"code":"0","msg":"OK","data":{"orderId":"wx123","clientOrderId":"ib-mtf-BTCUSDT-1-buy","symbol":"BTCUSDT","side":"BUY","price":100.5,"quantity":1,"status":"1"}}`)
		case pathOrderList:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"code":"0","data":[{"orderId":"wx123","clientOrderId":"ib-mtf-BTCUSDT-1-buy","symbol":"BTCUSDT","side":"BUY","price":100.5,"quantity":1,"status":"2","filledPrice":100.55,"filledQty":1}]}`)
		case pathOrderCancel:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"code":"0","msg":"OK"}`)
		case pathPositionList:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"code":"0","data":{"symbol":"BTCUSDT","side":"LONG","entryPrice":100.5,"quantity":1,"markPrice":101.2,"unrealizedPnL":0.7}}`)
		case pathAccountList:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"code":"0","data":{"balance":10000,"equity":10000.7,"unrealizedPnL":0.7}}`)
		default:
			t.Errorf("path inesperado: %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWeexBrokerPlaceOrderSignsRequest(t *testing.T) {
	srv := newWeexMock(t)
	b := NewWeexBroker(WeexConfig{BaseURL: srv.URL, APIKey: mockAPIKey, APISecret: mockAPISecret})
	order, err := b.PlaceOrder(context.Background(), weexTestOrder())
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if order.ID != "wx123" {
		t.Errorf("ID = %q, want wx123", order.ID)
	}
	if order.ClientID != "ib-mtf-BTCUSDT-1-buy" {
		t.Errorf("ClientID = %q", order.ClientID)
	}
	if order.Status != OrderPending {
		t.Errorf("Status = %q, want %q", order.Status, OrderPending)
	}
	if order.Quantity != 1 {
		t.Errorf("Quantity = %v, want 1", order.Quantity)
	}
}

func TestWeexBrokerGetOrder(t *testing.T) {
	srv := newWeexMock(t)
	b := NewWeexBroker(WeexConfig{BaseURL: srv.URL, APIKey: mockAPIKey, APISecret: mockAPISecret})
	order, err := b.GetOrder(context.Background(), "ib-mtf-BTCUSDT-1-buy")
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if order.Status != OrderFilled {
		t.Errorf("Status = %q, want %q", order.Status, OrderFilled)
	}
	if order.FilledPrice != 100.55 {
		t.Errorf("FilledPrice = %.4f, want 100.5500", order.FilledPrice)
	}
}

func TestWeexBrokerCancelPositionsAccount(t *testing.T) {
	srv := newWeexMock(t)
	b := NewWeexBroker(WeexConfig{BaseURL: srv.URL, APIKey: mockAPIKey, APISecret: mockAPISecret})
	ctx := context.Background()
	if err := b.CancelOrder(ctx, "ib-mtf-BTCUSDT-1-buy"); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	pos, err := b.GetPosition(ctx, "BTCUSDT")
	if err != nil {
		t.Fatalf("GetPosition: %v", err)
	}
	if pos.Side != domain.DirectionBuy {
		t.Errorf("pos.Side = %q, want %q", pos.Side, domain.DirectionBuy)
	}
	acc, err := b.GetBalance(ctx)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if acc.Balance != 10000 {
		t.Errorf("Balance = %.2f, want 10000", acc.Balance)
	}
	if acc.UnrealizedPnL != 0.7 {
		t.Errorf("UnrealizedPnL = %.2f, want 0.70", acc.UnrealizedPnL)
	}
}

func TestWeexMissingCredentials(t *testing.T) {
	srv := newWeexMock(t)
	b := NewWeexBroker(WeexConfig{BaseURL: srv.URL})
	if _, err := b.PlaceOrder(context.Background(), weexTestOrder()); err == nil {
		t.Fatal("se esperaba error sin credenciales")
	}
}

func TestWeexParsersMatchJSON(t *testing.T) {
	raw := json.RawMessage(`[
		{"clientOrderId":"a","symbol":"X","side":"LONG","status":2,"quantity":"3.5"},
		{"orderId":"b","symbol":"Y","side":"SHORT","status":"canceled","qty":"1"}
	]`)
	orders, err := parseWeexOrders(raw)
	if err != nil {
		t.Fatalf("parseWeexOrders: %v", err)
	}
	if len(orders) != 2 {
		t.Fatalf("len = %d, want 2", len(orders))
	}
	if orders[0].Quantity != 3.5 || orders[0].Side != "LONG" {
		t.Errorf("orden 0: %+v", orders[0])
	}
	if weexStatusToDomain(orders[1].Status) != OrderCanceled {
		t.Errorf("status = %q, want canceled", orders[1].Status)
	}
}

func weexTestOrder() domain.Order {
	return domain.Order{
		Symbol:     "BTCUSDT",
		Timeframe:  "1h",
		Side:       domain.DirectionBuy,
		Quantity:   1,
		Price:      100.5,
		StopLoss:   90,
		TakeProfit: 120,
		ClientID:   "ib-mtf-BTCUSDT-1-buy",
		StrategyID: "mtf",
	}
}