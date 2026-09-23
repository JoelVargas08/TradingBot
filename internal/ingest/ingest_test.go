package ingest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"tradingview-bot/internal/domain"

	"github.com/gorilla/websocket"
)

func TestParseKlineMessage(t *testing.T) {
	k, err := parseKline([]byte(`{
		"e":"kline","E":1672515782136,"s":"BTCUSDT",
		"k":{"t":1672515780000,"T":1672515840000,"s":"BTCUSDT","i":"1m",
		     "o":"60000.0","c":"60100.5","h":"60200.0","l":"59900.0","v":"123.45","n":100,"x":true}
	}`))
	if err != nil {
		t.Fatalf("parseKline: %v", err)
	}
	if k.Symbol != "BTCUSDT" || k.Timeframe != "1m" || !k.Closed {
		t.Errorf("kline inválida: %+v", k)
	}
	if k.Open != 60000 || k.Close != 60100.5 || k.High != 60200 || k.Low != 59900 {
		t.Errorf("precios inválidos: %+v", k)
	}
	if k.Volume != 123.45 || k.Start.UnixMilli() != 1672515780000 {
		t.Errorf("volumen/inicio inválidos: %+v", k)
	}
}

func TestParseTradeMessage(t *testing.T) {
	tr, err := parseTrade([]byte(`{"e":"aggTrade","E":1672515782136,"s":"BTCUSDT","a":1,"p":"60000.0","q":"2.0","T":1672515782136,"m":true}`))
	if err != nil {
		t.Fatalf("parseTrade: %v", err)
	}
	if tr.Symbol != "BTCUSDT" || tr.Notional != 120000 {
		t.Errorf("trade inválido: %+v", tr)
	}
}

func TestParseKlinesBackfill(t *testing.T) {
	body := `[
		[1672515780000,"60000.0","60200.0","59900.0","60100.5","123.45",1672515840000,"60000.0",100,"100.0","200.0","0"],
		[1672515840000,"60100.5","60300.0","60050.0","60200.0","200.0",1672515900000,"60100.5",150,"150.0","300.0","0"]
	]`
	bars, err := parseKlines([]byte(body), "BTCUSDT", "1m")
	if err != nil {
		t.Fatalf("parseKlines: %v", err)
	}
	if len(bars) != 2 {
		t.Fatalf("bars = %d, want 2", len(bars))
	}
	if bars[0].Close != 60100.5 || bars[1].Start.UnixMilli() != 1672515840000 || !bars[1].Closed {
		t.Errorf("bars inválidas: %+v", bars)
	}
}

func TestBinanceWSDeliversKlinesAndTrades(t *testing.T) {
	klineMsg := `{"stream":"btcusdt@kline_1m","data":{"e":"kline","E":1672515782136,"s":"BTCUSDT","k":{"t":1672515780000,"T":1672515840000,"s":"BTCUSDT","i":"1m","f":100,"L":200,"o":"60000.0","c":"60100.5","h":"60200.0","l":"59900.0","v":"123.45","n":100,"x":true,"q":"7400000.5"}}}`
	tradeMsg := `{"stream":"btcusdt@aggTrade","data":{"e":"aggTrade","E":1672515782136,"s":"BTCUSDT","a":26129,"p":"60000.0","q":"2.0","f":27781,"l":27781,"T":1672515782136,"m":true}}`

	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.WriteMessage(websocket.TextMessage, []byte(klineMsg))
		conn.WriteMessage(websocket.TextMessage, []byte(tradeMsg))
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	b := NewBinance(Config{
		WSURL:         wsURL,
		Symbols:       []string{"BTCUSDT"},
		Timeframes:    []string{"1m"},
		WatchTrades:   true,
		ReconnectBase: 20 * time.Millisecond,
		ReconnectMax:  50 * time.Millisecond,
		PingInterval:  time.Hour,
		PongWait:      2 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case k := <-b.Candles():
		if k.Symbol != "BTCUSDT" || k.Timeframe != "1m" || k.Close != 60100.5 || !k.Closed {
			t.Errorf("kline inesperada: %+v", k)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout esperando kline")
	}
	select {
	case tr := <-b.Trades():
		if tr.Symbol != "BTCUSDT" || tr.Notional != 120000 {
			t.Errorf("trade inesperado: %+v", tr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout esperando trade")
	}
}

func TestBinanceStartValidates(t *testing.T) {
	b := NewBinance(Config{Symbols: []string{"BTCUSDT"}, Timeframes: []string{"1m"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatalf("primer Start: %v", err)
	}
	if err := b.Start(ctx); err == nil {
		t.Error("segundo Start debería fallar")
	}
}

type fakeCandleStore struct {
	mu      sync.Mutex
	candles []domain.Kline
	dup     map[string]bool
}

func (f *fakeCandleStore) SaveCandle(ctx context.Context, k domain.Kline) error {
	key := fmt.Sprintf("%s|%s|%d", k.Symbol, k.Timeframe, k.Start.UnixMilli())
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dup[key] {
		return domain.ErrDuplicate
	}
	f.dup[key] = true
	f.candles = append(f.candles, k)
	return nil
}

func (f *fakeCandleStore) RecentCandles(ctx context.Context, symbol, timeframe string, limit int) ([]domain.Kline, error) {
	return nil, nil
}

func (f *fakeCandleStore) CandlesBetween(ctx context.Context, symbol, timeframe string, start, end time.Time) ([]domain.Kline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Kline
	for _, k := range f.candles {
		if k.Symbol == symbol && k.Timeframe == timeframe && !k.Start.Before(start) && k.Start.Before(end) {
			out = append(out, k)
		}
	}
	return out, nil
}

func klineRow(openTime int64) string {
	return fmt.Sprintf(`[%d,"60000.0","60100.0","59900.0","60050.5","123.45",%d,"60000.0",100,"100.0","200.0","0"]`,
		openTime, openTime+60000)
}

func TestBackfillPaginatesAndPersists(t *testing.T) {
	const first = int64(1000)
	const step = int64(3600000)
	page2Start := first + 1000*step

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := int64(0)
		fmt.Sscanf(r.URL.Query().Get("startTime"), "%d", &start)
		switch start {
		case first:
			var rows []string
			for i := 0; i < 1000; i++ {
				rows = append(rows, klineRow(first+int64(i)*step))
			}
			w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
		case page2Start:
			w.Write([]byte("[" + klineRow(page2Start) + "," + klineRow(page2Start+step) + "]"))
		default:
			w.Write([]byte("[]"))
		}
	}))
	defer srv.Close()

	store := &fakeCandleStore{dup: make(map[string]bool)}
	bf := NewBackfiller(store, srv.URL)
	n, err := bf.Backfill(context.Background(), "BTCUSDT", "1h", time.UnixMilli(first))
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if n != 1002 {
		t.Errorf("velas = %d, want 1002", n)
	}
	if len(store.candles) != 1002 {
		t.Fatalf("persistidas = %d, want 1002", len(store.candles))
	}
	if store.candles[0].Start.UnixMilli() != first {
		t.Errorf("primera vela = %d, want %d", store.candles[0].Start.UnixMilli(), first)
	}
}

func TestBackfillHandlesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	store := &fakeCandleStore{dup: make(map[string]bool)}
	bf := NewBackfiller(store, srv.URL)
	if _, err := bf.Backfill(context.Background(), "BTCUSDT", "1h", time.Now().Add(-time.Hour)); err == nil {
		t.Error("Backfill con 429 debería fallar")
	}
}

func TestParseInterval(t *testing.T) {
	cases := map[string]time.Duration{
		"1m": time.Minute,
		"5m": 5 * time.Minute,
		"1h": time.Hour,
		"1d": 24 * time.Hour,
		"1w": 7 * 24 * time.Hour,
	}
	for in, want := range cases {
		if got := parseInterval(in); got != want {
			t.Errorf("parseInterval(%s) = %v, want %v", in, got, want)
		}
	}
}
