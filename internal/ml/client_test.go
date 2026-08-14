package ml

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPredictBatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/predict_batch" {
			t.Fatalf("ruta inesperada %s", r.URL.Path)
		}
		var body struct {
			Candles []candleJSON `json:"candles"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Candles) != 70 {
			t.Fatalf("candles = %d, want 70", len(body.Candles))
		}
		w.Header().Set("Content-Type", "application/json")
		// 64 velas → primeras 64 sin señal, desde la 65 en adelante alterna
		var sigs []BatchSignal
		for i := 0; i < len(body.Candles); i++ {
			s := "none"
			if i >= 64 {
				s = "buy"
			}
			sigs = append(sigs, BatchSignal{Idx: i, TS: int64(1700000000000 + i), Signal: s, ProbUp: 0.7, ProbDown: 0.3, Confidence: 0.4})
		}
		json.NewEncoder(w).Encode(BatchResult{Symbol: "BTCUSDT", Timeframe: "1h", Bars: len(body.Candles), Signals: sigs})
	}))
	defer srv.Close()

	c := NewClient(Config{URL: srv.URL})
	ks := klines(70, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	br, err := c.PredictBatch(context.Background(), "BTCUSDT", "1h", ks)
	if err != nil {
		t.Fatal(err)
	}
	if br.Bars != 70 {
		t.Fatalf("Bars = %d, want 70", br.Bars)
	}
	if len(br.Signals) != 70 {
		t.Fatalf("Signals = %d, want 70", len(br.Signals))
	}
	if br.Signals[0].Signal != "none" || br.Signals[69].Signal != "buy" {
		t.Fatalf("señales mal mapeadas: first=%s last=%s", br.Signals[0].Signal, br.Signals[69].Signal)
	}
}

func TestPredictBatchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "modelo no cargado", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := NewClient(Config{URL: srv.URL})
	ks := klines(70, time.Now())
	_, err := c.PredictBatch(context.Background(), "BTCUSDT", "1h", ks)
	if err == nil {
		t.Fatal("esperaba error HTTP 503")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("error = %v, esperaba mención del 503", err)
	}
}
