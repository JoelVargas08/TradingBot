package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"tradingview-bot/models"
)

type recorder struct {
	alerts []string
}

func (r *recorder) SendAlert(chatID int64, symbol, action string, price float64) {
	r.alerts = append(r.alerts, symbol+" "+action)
}

func newTestHandler(secret string, um *models.UserManager) (*WebhookHandler, *recorder) {
	rec := &recorder{}
	return NewWebhookHandler(um, rec, secret), rec
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
	um := models.NewUserManager()
	h, _ := newTestHandler("correct-secret", um)
	w := postJSON(h, `{"symbol":"BTCUSDT","action":"buy","price":60000,"secret":"wrong"}`)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestWebhookRejectsBadAction(t *testing.T) {
	um := models.NewUserManager()
	h, _ := newTestHandler("correct-secret", um)
	w := postJSON(h, `{"symbol":"BTCUSDT","action":"hold","price":60000,"secret":"correct-secret"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestWebhookRejectsBadPrice(t *testing.T) {
	um := models.NewUserManager()
	h, _ := newTestHandler("correct-secret", um)
	w := postJSON(h, `{"symbol":"BTCUSDT","action":"buy","price":-5,"secret":"correct-secret"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestWebhookSendsAlertAndDedupsByBar(t *testing.T) {
	um := models.NewUserManager()
	um.Activate(1)
	h, rec := newTestHandler("correct-secret", um)

	body := `{"symbol":"BTCUSDT","action":"buy","price":"60000.5","time":1700000000,"secret":"correct-secret"}`
	w := postJSON(h, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if len(rec.alerts) != 1 {
		t.Fatalf("alerts = %d, want 1", len(rec.alerts))
	}

	w2 := postJSON(h, body)
	if w2.Code != http.StatusOK {
		t.Fatalf("duplicado: status = %d, want 200", w2.Code)
	}
	if len(rec.alerts) != 1 {
		t.Errorf("duplicado: alerts = %d, want 1 (cooldown por barra)", len(rec.alerts))
	}
}

func TestWebhookTogglesOnNewBar(t *testing.T) {
	um := models.NewUserManager()
	um.Activate(1)
	h, rec := newTestHandler("correct-secret", um)

	postJSON(h, `{"symbol":"BTCUSDT","action":"buy","price":60000,"time":1,"secret":"correct-secret"}`)
	postJSON(h, `{"symbol":"BTCUSDT","action":"sell","price":61000,"time":2,"secret":"correct-secret"}`)

	if len(rec.alerts) != 2 {
		t.Errorf("alerts = %d, want 2 (buy y sell en barras distintas)", len(rec.alerts))
	}
	if rec.alerts[0] != "BTCUSDT buy" || rec.alerts[1] != "BTCUSDT sell" {
		t.Errorf("alertas inesperadas: %v", rec.alerts)
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
