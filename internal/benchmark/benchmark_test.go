package benchmark

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tradingview-bot/internal/backtest"
	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/ml"
)

func testCandles(n int) (out []domain.Kline) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	out = make([]domain.Kline, n)
	for i := 0; i < n; i++ {
		c := 100.0 + float64(i)*0.1
		out[i] = domain.Kline{
			Symbol: "ETHUSDT", Timeframe: "1h", Start: start.Add(time.Duration(i) * time.Hour),
			Open: c, High: c + 1, Low: c - 1, Close: c, Volume: 100, Closed: true,
		}
	}
	return out
}

func mlSidecar(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/predict_batch" {
			t.Fatalf("ruta inesperada %s", r.URL.Path)
		}
		var body struct {
			Candles []any `json:"candles"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		var sigs []ml.BatchSignal
		for i := range body.Candles {
			s := "none"
			conf := 0.0
			if i >= 64 {
				s, conf = "buy", 0.8
			}
			sigs = append(sigs, ml.BatchSignal{Idx: i, Signal: s, Confidence: conf, ProbUp: 0.8, ProbDown: 0.2})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ml.BatchResult{Symbol: "ETHUSDT", Timeframe: "1h", Bars: len(body.Candles), Signals: sigs})
	}))
}

func TestRunWithoutML(t *testing.T) {
	candles := testCandles(200)
	rep, err := Run(candles, backtest.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.BuyHold == nil || rep.Chandelier == nil {
		t.Fatal("faltan resultados base")
	}
	if rep.ML != nil {
		t.Fatal("ML no debería estar presente sin runner")
	}
	if rep.Bars != 200 {
		t.Fatalf("Bars = %d, want 200", rep.Bars)
	}
	if !strings.Contains(rep.Text(), "Chandelier Exit") {
		t.Fatal("la tabla debería incluir Chandelier")
	}
	if rep.Best() == "" {
		t.Fatal("Best vacío")
	}
}

func TestRunWithML(t *testing.T) {
	srv := mlSidecar(t)
	defer srv.Close()

	candles := testCandles(200)
	rep, err := Run(candles, backtest.Config{}, &Runner{
		MLClient:     ml.NewClient(ml.Config{URL: srv.URL}),
		MLConfidence: 0.6,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.ML == nil {
		t.Fatal("ML debería estar presente")
	}
	if !strings.Contains(rep.Text(), "ML XGBoost") {
		t.Fatal("la tabla debería incluir ML")
	}
	if rep.ML.Trades < 1 {
		t.Fatalf("ML Trades = %d, esperado ≥1", rep.ML.Trades)
	}
}

func TestRunShortSeries(t *testing.T) {
	if _, err := Run(testCandles(1), backtest.Config{}, nil); err == nil {
		t.Fatal("esperaba error con 1 vela")
	}
}
