package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tradingview-bot/internal/domain"
)

type fakePublisher struct {
	published []domain.SignalEvent
	err       error
}

func (f *fakePublisher) Publish(ctx context.Context, ev domain.SignalEvent) error {
	if f.err != nil {
		return f.err
	}
	f.published = append(f.published, ev)
	return nil
}

func postJSON(h *WebhookHandler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleWebhook(w, req)
	return w
}

func TestFlexPriceNumberAndString(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  float64
	}{
		{"12345.67", 12345.67},
		{`"12345.67"`, 12345.67},
		{`"0.00012"`, 0.00012},
	} {
		var p FlexPrice
		if err := json.Unmarshal([]byte(tc.input), &p); err != nil {
			t.Fatalf("Unmarshal(%s) error: %v", tc.input, err)
		}
		if float64(p) != tc.want {
			t.Errorf("Unmarshal(%s) = %v, want %v", tc.input, p, tc.want)
		}
	}
}

func TestFlexPriceInvalid(t *testing.T) {
	var p FlexPrice
	if err := json.Unmarshal([]byte(`"not-a-number"`), &p); err == nil {
		t.Error("esperaba error con precio inválido")
	}
}

func TestWebhookRejectsBadSecret(t *testing.T) {
	h := NewWebhookHandler(&fakePublisher{}, "correct-secret")
	w := postJSON(h, `{"symbol":"BTCUSDT","action":"buy","price":60000,"secret":"wrong"}`)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestWebhookRejectsBadAction(t *testing.T) {
	h := NewWebhookHandler(&fakePublisher{}, "correct-secret")
	w := postJSON(h, `{"symbol":"BTCUSDT","action":"hold","price":60000,"secret":"correct-secret"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestWebhookRejectsBadPrice(t *testing.T) {
	h := NewWebhookHandler(&fakePublisher{}, "correct-secret")
	w := postJSON(h, `{"symbol":"BTCUSDT","action":"buy","price":-5,"secret":"correct-secret"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestWebhookRejectsMissingSymbol(t *testing.T) {
	h := NewWebhookHandler(&fakePublisher{}, "correct-secret")
	w := postJSON(h, `{"action":"buy","price":60000,"secret":"correct-secret"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestWebhookPublishesAndReturns202(t *testing.T) {
	pub := &fakePublisher{}
	h := NewWebhookHandler(pub, "correct-secret")

	w := postJSON(h, `{"strategy":"chandelier","timeframe":"1h","symbol":"BTCUSDT","action":"buy","price":"60000.5","time":1700000000,"secret":"correct-secret"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	if len(pub.published) != 1 {
		t.Fatalf("published = %d, want 1", len(pub.published))
	}
	ev := pub.published[0]
	if ev.StrategyID != "chandelier" || ev.Symbol != "BTCUSDT" || ev.Timeframe != "1h" {
		t.Errorf("evento con datos incorrectos: %+v", ev)
	}
	if ev.Direction != domain.DirectionBuy || ev.Price != 60000.5 {
		t.Errorf("evento con dirección/precio incorrectos: %+v", ev)
	}
	if ev.BarTS.Unix() != 1700000000 {
		t.Errorf("BarTS = %d, want 1700000000", ev.BarTS.Unix())
	}
	if ev.ReceivedAt.IsZero() {
		t.Error("ReceivedAt no debería ser cero")
	}
}

func TestWebhookDefaultsStrategyAndTimeframe(t *testing.T) {
	pub := &fakePublisher{}
	h := NewWebhookHandler(pub, "correct-secret")

	postJSON(h, `{"symbol":"BTCUSDT","action":"sell","price":60000,"secret":"correct-secret"}`)
	if len(pub.published) != 1 {
		t.Fatalf("published = %d, want 1", len(pub.published))
	}
	ev := pub.published[0]
	if ev.StrategyID != defaultStrategy || ev.Timeframe != defaultTimeframe {
		t.Errorf("defaults no aplicados: %+v", ev)
	}
}

func TestWebhookServiceUnavailableOnFullBus(t *testing.T) {
	h := NewWebhookHandler(&fakePublisher{err: context.DeadlineExceeded}, "correct-secret")
	w := postJSON(h, `{"symbol":"BTCUSDT","action":"buy","price":60000,"secret":"correct-secret"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestSecureEqual(t *testing.T) {
	if !secureEqual("abc", "abc") {
		t.Error("secureEqual('abc','abc') = false, want true")
	}
	if secureEqual("abc", "abd") {
		t.Error("secureEqual('abc','abd') = true, want false")
	}
	if secureEqual("abc", "abcd") {
		t.Error("secureEqual con longitudes distintas = true, want false")
	}
}
