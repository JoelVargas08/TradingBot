package ml

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func klines(n int, start time.Time) []domain.Kline {
	out := make([]domain.Kline, n)
	for i := 0; i < n; i++ {
		out[i] = domain.Kline{
			Symbol:    "BTCUSDT",
			Timeframe: "1h",
			Start:     start.Add(time.Duration(i) * time.Hour),
			Open:      100 + float64(i),
			High:      101 + float64(i),
			Low:       99 + float64(i),
			Close:     100.5 + float64(i),
			Volume:    1000,
			Closed:    true,
		}
	}
	return out
}

// stubPredictor devuelve una predicción fija.
type stubPredictor struct {
	mu  sync.Mutex
	req int
	p   Prediction
	err error
}

func (s *stubPredictor) Predict(_ context.Context, symbol, timeframe string, ks []domain.Kline) (Prediction, error) {
	s.mu.Lock()
	s.req++
	s.mu.Unlock()
	if s.err != nil {
		return Prediction{}, s.err
	}
	out := s.p
	out.Symbol, out.Timeframe = symbol, timeframe
	return out, nil
}

func (s *stubPredictor) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.req
}

func TestEngineEmitsOnConfidence(t *testing.T) {
	stub := &stubPredictor{p: Prediction{
		Signal: "buy", Confidence: 0.8, Price: 150,
		RSI: 55, VolumeRatio: 1.2, StopPct: 1.0, TakeProfitPct: 2.0,
	}}
	eng := New(Config{URL: "http://unused", Window: 5, Confidence: 0.6, Cooldown: time.Minute}, stub)
	start := time.Now().Add(-10 * time.Hour)
	for _, k := range klines(5, start) {
		evs, err := eng.OnCandle(context.Background(), k)
		if err != nil {
			t.Fatalf("OnCandle: %v", err)
		}
		if len(evs) == 1 {
			ev := evs[0]
			if ev.StrategyID != "ml-xgboost" || ev.Direction != domain.DirectionBuy {
				t.Errorf("evento inesperado: %+v", ev)
			}
			if ev.Meta[domain.MetaKeyConfidence] != 0.8 {
				t.Errorf("confianza no propagada: %v", ev.Meta)
			}
			return
		}
	}
	t.Fatal("no se emitió señal con confianza suficiente")
}

func TestEngineCooldownSuppressesRepeats(t *testing.T) {
	stub := &stubPredictor{p: Prediction{Signal: "buy", Confidence: 0.8, Price: 150}}
	eng := New(Config{Window: 5, Confidence: 0.6, Cooldown: time.Hour}, stub)
	start := time.Now().Add(-10 * time.Hour)
	ks := klines(5, start)
	for _, k := range ks {
		if _, err := eng.OnCandle(context.Background(), k); err != nil {
			t.Fatalf("OnCandle: %v", err)
		}
	}
	emitted := stub.Calls() > 0
	// una vela más dentro del cooldown → sin emisión
	extra := klines(1, start.Add(6*time.Hour))[0]
	evs, err := eng.OnCandle(context.Background(), extra)
	if err != nil {
		t.Fatalf("OnCandle: %v", err)
	}
	if !emitted {
		t.Fatal("la primera señal debería haberse emitido")
	}
	if len(evs) != 0 {
		t.Error("cooldown no suprimió la segunda señal")
	}
}

func TestEngineLowConfidenceNoEmit(t *testing.T) {
	stub := &stubPredictor{p: Prediction{Signal: "sell", Confidence: 0.4, Price: 150}}
	eng := New(Config{Window: 5, Confidence: 0.6, Cooldown: time.Minute}, stub)
	start := time.Now().Add(-10 * time.Hour)
	for _, k := range klines(5, start) {
		evs, err := eng.OnCandle(context.Background(), k)
		if err != nil {
			t.Fatalf("OnCandle: %v", err)
		}
		if len(evs) != 0 {
			t.Fatal("confianza baja no debería emitir")
		}
	}
}

func TestEngineIgnoresOpenCandle(t *testing.T) {
	stub := &stubPredictor{p: Prediction{Signal: "buy", Confidence: 0.8, Price: 150}}
	eng := New(Config{Window: 1, Confidence: 0.5, Cooldown: time.Second}, stub)
	open := klines(1, time.Now())[0]
	open.Closed = false
	evs, err := eng.OnCandle(context.Background(), open)
	if err != nil {
		t.Fatalf("OnCandle: %v", err)
	}
	if len(evs) != 0 || stub.Calls() != 0 {
		t.Error("velas abiertas no deben generar predicciones")
	}
}

func TestEngineSeedBuffer(t *testing.T) {
	stub := &stubPredictor{p: Prediction{Signal: "buy", Confidence: 0.9, Price: 150}}
	eng := New(Config{Window: 5, Confidence: 0.5, Cooldown: time.Minute}, stub)
	eng.Seed("BTCUSDT", "1h", klines(100, time.Now().Add(-100*time.Hour)))
	if got := eng.Buffered("BTCUSDT", "1h"); got != 5 {
		t.Errorf("buffer tras seed = %d, want 5", got)
	}
	// una vela nueva debe bastar para emitir
	evs, err := eng.OnCandle(context.Background(), klines(1, time.Now())[0])
	if err != nil {
		t.Fatalf("OnCandle: %v", err)
	}
	if len(evs) != 1 {
		t.Error("con buffer precargado debería emitir en la primera vela")
	}
}

func TestClientParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/predict" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"symbol":"BTCUSDT","timeframe":"1h","signal":"buy","confidence":0.7,"prob_up":0.7,"prob_down":0.3,"price":100.5,"rsi":60,"volume_ratio":1.1,"atr_pct":0.5,"stop_pct":0.75,"take_profit_pct":1.5}`))
	}))
	defer srv.Close()
	c := NewClient(Config{URL: srv.URL})
	p, err := c.Predict(context.Background(), "BTCUSDT", "1h", klines(64, time.Now().Add(-64*time.Hour)))
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if p.Signal != "buy" || p.Confidence != 0.7 {
		t.Errorf("respuesta mal parseada: %+v", p)
	}
}

func TestStopTarget(t *testing.T) {
	buyStop, buyTarget := stopTarget(100, domain.DirectionBuy, 1.0, 2.0)
	if buyStop != 99 || buyTarget != 102 {
		t.Errorf("buy stop/target = %v/%v", buyStop, buyTarget)
	}
	sellStop, sellTarget := stopTarget(100, domain.DirectionSell, 1.0, 2.0)
	if sellStop != 101 || sellTarget != 98 {
		t.Errorf("sell stop/target = %v/%v", sellStop, sellTarget)
	}
}
