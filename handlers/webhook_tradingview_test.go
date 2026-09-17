package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

type fakeCandleStore struct {
	candles []domain.Kline
	err     error
}

func (f *fakeCandleStore) SaveCandle(ctx context.Context, k domain.Kline) error {
	if f.err != nil {
		return f.err
	}
	f.candles = append(f.candles, k)
	return nil
}

func (f *fakeCandleStore) RecentCandles(ctx context.Context, symbol, timeframe string, limit int) ([]domain.Kline, error) {
	return f.candles, f.err
}

func TestWebhookAcceptsTradingViewCandle(t *testing.T) {
	pub := &fakePublisher{}
	candles := &fakeCandleStore{}
	h := NewWebhookHandler(pub, "tv-secret", candles)

	body := `{
		"symbol":"BTCUSDT",
		"exchange":"BINANCE",
		"timeframe":"1h",
		"time":"2026-09-17T14:00:00Z",
		"open":"76000.10",
		"high":"77100.50",
		"low":"75800.00",
		"close":"76900.25",
		"volume":"1234.56",
		"secret":"tv-secret"
	}`

	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.HandleWebhook(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	if len(candles.candles) != 1 {
		t.Fatalf("candles = %d, want 1", len(candles.candles))
	}
	k := candles.candles[0]
	if k.Symbol != "BTCUSDT" || k.Timeframe != "1h" {
		t.Fatalf("identidad incorrecta: %+v", k)
	}
	if k.Open != 76000.10 || k.High != 77100.50 || k.Low != 75800 || k.Close != 76900.25 || k.Volume != 1234.56 {
		t.Fatalf("OHLCV incorrecto: %+v", k)
	}
	if !k.Closed {
		t.Error("la vela debería quedar marcada como cerrada")
	}
	wantTS := time.Date(2026, 9, 17, 14, 0, 0, 0, time.UTC)
	if !k.Start.Equal(wantTS) {
		t.Errorf("Start = %s, want %s", k.Start, wantTS)
	}
	if len(pub.published) != 0 {
		t.Errorf("una vela sin action no debería publicar una señal: %d", len(pub.published))
	}
}

func TestWebhookRejectsOpenTradingViewCandle(t *testing.T) {
	pub := &fakePublisher{}
	candles := &fakeCandleStore{}
	h := NewWebhookHandler(pub, "tv-secret", candles)

	body := `{"symbol":"BTCUSDT","timeframe":"1h","time":1700000000,"open":60000,"high":61000,"low":59000,"close":60500,"volume":10,"closed":false,"secret":"tv-secret"}`
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.HandleWebhook(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if len(candles.candles) != 0 {
		t.Errorf("no debería guardar vela abierta: %d", len(candles.candles))
	}
}

func TestWebhookAcceptsTradingViewCandleAndSignal(t *testing.T) {
	pub := &fakePublisher{}
	candles := &fakeCandleStore{}
	h := NewWebhookHandler(pub, "tv-secret", candles)

	body := `{"strategy":"chandelier","symbol":"BTCUSDT","timeframe":"1h","time":1700000000,"open":60000,"high":61000,"low":59000,"close":60500,"volume":10,"action":"buy","secret":"tv-secret"}`
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.HandleWebhook(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	if len(candles.candles) != 1 || len(pub.published) != 1 {
		t.Fatalf("candles=%d signals=%d, want 1/1", len(candles.candles), len(pub.published))
	}
	if pub.published[0].Price != 60500 {
		t.Errorf("signal price = %v, want 60500", pub.published[0].Price)
	}
}
