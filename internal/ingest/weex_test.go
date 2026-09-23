package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tradingview-bot/internal/domain"

	"github.com/gorilla/websocket"
)

func TestParseWeexKlines(t *testing.T) {
	body := `[
		[1704067200000,"60000.0","60200.0","59900.0","60100.5","123.45",1704067260000,"60000.0",100,"100.0","200.0","0"],
		[1704067260000,60100.5,60300.0,60050.0,60200.0,200.0,1704067320000,60100.5,150,"150.0","300.0","0"]
	]`
	bars, err := parseWeexKlines([]byte(body), "BTCUSDT", "1m")
	if err != nil {
		t.Fatalf("parseWeexKlines: %v", err)
	}
	if len(bars) != 2 {
		t.Fatalf("bars = %d, want 2", len(bars))
	}
	first := bars[0]
	if first.Symbol != "BTCUSDT" || first.Timeframe != "1m" || !first.Closed {
		t.Errorf("symbol/timeframe/closed inválidos: %+v", first)
	}
	if first.Start.UnixMilli() != 1704067200000 {
		t.Errorf("Start = %d, want 1704067200000", first.Start.UnixMilli())
	}
	if first.Open != 60000 || first.High != 60200 || first.Low != 59900 || first.Close != 60100.5 || first.Volume != 123.45 {
		t.Errorf("OHLCV inválido: %+v", first)
	}
	if !bars[1].Closed || bars[1].Close != 60200 {
		t.Errorf("segunda vela inválida: %+v", bars[1])
	}
}

func TestParseWeexKlinesErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"json inválido", `{no}`},
		{"fila incompleta", `[[1704067200000,"60000.0"]]`},
		{"precio inválido", `[[1704067200000,"x","60000","59000","60000","1"]]`},
		{"openTime inválido", `[["abc","60000","60000","59000","60000","1"]]`},
		{"OHLC incoherente", `[[1704067200000,"60000","58700","59900","60000","1"]]`},
		{"precios no positivos", `[[1704067200000,"60000","60000","59000","0","1"]]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseWeexKlines([]byte(c.body), "BTCUSDT", "1m"); err == nil {
				t.Errorf("debería fallar: %q", c.body)
			}
		})
	}
}

func TestParseWSKlineObjectAndClosedFlag(t *testing.T) {
	p := NewWEEXProvider(nil, WeexConfig{})
	msg := []byte(`{"data":{"t":1704067200000,"T":1704067260000,"s":"BTCUSDT","i":"1h","o":"50000","c":"50100","h":"50200","l":"49900","v":"10.5"}}`)

	p.now = func() time.Time { return time.UnixMilli(1704067000000) }
	k, ok, err := p.parseWSKline(msg, "BTCUSDT", "1h")
	if err != nil {
		t.Fatalf("parseWSKline: %v", err)
	}
	if !ok {
		t.Fatal("debería haber parseado la vela")
	}
	if k.Closed {
		t.Error("vela con T futuro debería estar en formación")
	}
	if k.Open != 50000 || k.Close != 50100 || k.Volume != 10.5 || k.Start.UnixMilli() != 1704067200000 {
		t.Errorf("fields inválidos: %+v", k)
	}

	p.now = func() time.Time { return time.UnixMilli(1704067300000) }
	k, ok, err = p.parseWSKline(msg, "BTCUSDT", "1h")
	if err != nil || !ok {
		t.Fatalf("parseWSKline: ok=%v err=%v", ok, err)
	}
	if !k.Closed {
		t.Error("vela con T pasado debería estar cerrada")
	}
}

func TestParseWSKlineArrayAndRootFallback(t *testing.T) {
	p := NewWEEXProvider(nil, WeexConfig{})
	p.now = func() time.Time { return time.UnixMilli(1704067300000) }
	msg := []byte(`{"data":[{"t":1704067200000,"T":1704067260000,"s":"BTCUSDT","i":"1h","o":"50000","c":"50100","h":"50200","l":"49900","v":"10.5"}]}`)
	k, ok, _ := p.parseWSKline(msg, "BTCUSDT", "1h")
	if !ok || k.Close != 50100 {
		t.Fatalf("array en data no parseado: ok=%v k=%+v", ok, k)
	}

	// Campos en la raíz (sin data): usar el symbol/timeframe por defecto.
	msg2 := []byte(`{"t":1704067200000,"T":1704067260000,"o":"50000","c":"50100","h":"50200","l":"49900","v":"10.5"}`)
	k, ok, _ = p.parseWSKline(msg2, "BTCUSDT", "1h")
	if !ok || k.Symbol != "BTCUSDT" || k.Timeframe != "1h" || k.Close != 50100 {
		t.Fatalf("raíz no parseada: ok=%v k=%+v", ok, k)
	}
}

func TestWeexIsPing(t *testing.T) {
	p := NewWEEXProvider(nil, WeexConfig{})
	pingCases := []string{
		`{"method":"PING","id":1}`,
		`{"method":"ping"}`,
		`{"type":"ping"}`,
		`{"ping":123456}`,
	}
	for _, m := range pingCases {
		if !p.isPing([]byte(m)) {
			t.Errorf("debería ser ping: %s", m)
		}
	}
	notPing := []string{
		`{"method":"SUBSCRIBE","params":["x"],"id":1}`,
		`{"method":"PONG","id":1}`,
		`{"data":{"t":1}}`,
		`{"type":"pingpong"}`,
	}
	for _, m := range notPing {
		if p.isPing([]byte(m)) {
			t.Errorf("no debería ser ping: %s", m)
		}
	}
}

func TestWeexChannelName(t *testing.T) {
	p := NewWEEXProvider(nil, WeexConfig{PriceType: "LAST_PRICE"})
	if got := p.channelName("btcusdt", "1h"); got != "BTCUSDT@kline_1h_LAST_PRICE" {
		t.Errorf("channel = %q", got)
	}
}

func TestWeexBackfillSavesAndValidatesQuery(t *testing.T) {
	body := `[[1704067200000,"60000.0","60200.0","59900.0","60100.5","123.45",1704067260000,"60000.0",100,"100.0","200.0","0"]]`
	var mu sync.Mutex
	var symbol, interval, limit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/capi/v3/market/klines" {
			t.Errorf("path = %q", r.URL.Path)
		}
		mu.Lock()
		symbol = r.URL.Query().Get("symbol")
		interval = r.URL.Query().Get("interval")
		limit = r.URL.Query().Get("limit")
		mu.Unlock()
		w.Write([]byte(body))
	}))
	defer srv.Close()

	store := &fakeCandleStore{dup: make(map[string]bool)}
	p := NewWEEXProvider(store, WeexConfig{RestURL: srv.URL})
	if err := p.Backfill(context.Background(), "BTCUSDT", "1h", 300); err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	mu.Lock()
	if symbol != "BTCUSDT" || interval != "1h" || limit != "300" {
		t.Errorf("query = %s %s %s", symbol, interval, limit)
	}
	mu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.candles) != 1 {
		t.Fatalf("velas = %d, want 1", len(store.candles))
	}
	if !store.candles[0].Closed || store.candles[0].Close != 60100.5 {
		t.Errorf("vela guardada inválida: %+v", store.candles[0])
	}
}

func TestWeexBackfillHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := NewWEEXProvider(&fakeCandleStore{}, WeexConfig{RestURL: srv.URL})
	if err := p.Backfill(context.Background(), "BTCUSDT", "1h", 10); err == nil {
		t.Error("Backfill con error HTTP debería fallar")
	}
}

func TestWeexBackfillCapsLimitAt1000(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("limit")
		w.Write([]byte("[]"))
	}))
	defer srv.Close()
	p := NewWEEXProvider(&fakeCandleStore{}, WeexConfig{RestURL: srv.URL})
	if err := p.Backfill(context.Background(), "BTCUSDT", "1h", 5000); err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if got != "1000" {
		t.Errorf("limit = %s, want 1000", got)
	}
}

func wexTestProvider(wsURL, priceType string) *WeexProvider {
	cfg := WeexConfig{
		WSURL:         wsURL,
		PriceType:     priceType,
		ReconnectBase: 50 * time.Millisecond,
		ReconnectMax:  100 * time.Millisecond,
		PongWait:      2 * time.Second,
		BackfillBars:  300,
	}
	return NewWEEXProvider(&fakeCandleStore{}, cfg)
}

func wsURL(t *testing.T, s *httptest.Server) string {
	t.Helper()
	return "ws" + strings.TrimPrefix(s.URL, "http")
}

func upgrade(t *testing.T, w http.ResponseWriter, r *http.Request) *websocket.Conn {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		t.Errorf("upgrade: %v", err)
		return nil
	}
	return conn
}

func readWexMsg(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(msg, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", msg, err)
	}
	return m
}

// writeWexCandle envía una vela WS con T en el pasado (cerrada) o futuro (abierta).
func writeWexCandle(t *testing.T, conn *websocket.Conn, symbol string, openT, closeT int64, close float64) {
	t.Helper()
	payload := map[string]any{
		"data": map[string]any{
			"t": openT, "T": closeT, "s": symbol, "i": "1h",
			"o": close - 100, "h": close + 200, "l": close - 300, "c": close, "v": 10.5,
		},
	}
	if err := conn.WriteJSON(payload); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
}

func TestWexWS_subscribePingPongAndCandles(t *testing.T) {
	const nowMs = int64(1704067300000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := upgrade(t, w, r)
		if conn == nil {
			return
		}
		defer conn.Close()

		sub := readWexMsg(t, conn)
		if sub["method"] != "SUBSCRIBE" {
			t.Fatalf("primer mensaje = %v, want SUBSCRIBE", sub)
		}
		params, ok := sub["params"].([]any)
		if !ok || len(params) == 0 || params[0] != "BTCUSDT@kline_1h_LAST_PRICE" {
			t.Fatalf("params = %v", sub["params"])
		}

		// ping de WEEX → el cliente debe responder PONG
		if err := conn.WriteJSON(map[string]any{"method": "PING", "ping": nowMs}); err != nil {
			t.Fatalf("WriteJSON ping: %v", err)
		}
		pong := readWexMsg(t, conn)
		if pong["method"] != "PONG" {
			t.Fatalf("cliente no respondió PONG: %v", pong)
		}

		// vela cerrada y luego vela en formación
		writeWexCandle(t, conn, "BTCUSDT", nowMs-2*3600000, nowMs-3600000, 50000)
		writeWexCandle(t, conn, "BTCUSDT", nowMs-7200000, nowMs+3600000, 50100)
	}))
	defer srv.Close()

	p := wexTestProvider(wsURL(t, srv), "LAST_PRICE")
	p.now = func() time.Time { return time.UnixMilli(nowMs) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := p.Subscribe(ctx, "BTCUSDT", "1h")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	first := readWexKline(t, ch)
	if !first.Closed || first.Close != 50000 {
		t.Errorf("primera vela debería ser cerrada: %+v", first)
	}
	second := readWexKline(t, ch)
	if second.Closed || second.Close != 50100 {
		t.Errorf("segunda vela debería estar en formación: %+v", second)
	}
}

func readWexKline(t *testing.T, ch <-chan domain.Kline) domain.Kline {
	t.Helper()
	select {
	case k := <-ch:
		return k
	case <-time.After(3 * time.Second):
		t.Fatal("timeout esperando vela WS")
		return domain.Kline{}
	}
}

func TestWexWS_reconnect(t *testing.T) {
	const nowMs = int64(1704067300000)
	var dials atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := dials.Add(1)
		conn := upgrade(t, w, r)
		if conn == nil {
			return
		}
		defer conn.Close()
		sub := readWexMsg(t, conn)
		if sub["method"] != "SUBSCRIBE" {
			t.Fatalf("mensaje = %v, want SUBSCRIBE", sub)
		}
		if n == 1 {
			return // primera conexión: cerrar inmediatamente
		}
		writeWexCandle(t, conn, "BTCUSDT", nowMs-3600000, nowMs-1, 50000)
	}))
	defer srv.Close()

	p := wexTestProvider(wsURL(t, srv), "LAST_PRICE")
	p.now = func() time.Time { return time.UnixMilli(nowMs) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := p.Subscribe(ctx, "BTCUSDT", "1h")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	k := readWexKline(t, ch)
	if !k.Closed {
		t.Errorf("tras reconexión la vela debería llegar cerrada: %+v", k)
	}
	if dials.Load() < 2 {
		t.Errorf("dials = %d, want >= 2 (reconexión)", dials.Load())
	}
}

func TestWexWS_duplicateSubscriptionRejected(t *testing.T) {
	p := wexTestProvider("wss://invalid.example/ws", "LAST_PRICE")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := p.Subscribe(ctx, "BTCUSDT", "1h"); err != nil {
		t.Fatalf("primera suscripción: %v", err)
	}
	if _, err := p.Subscribe(ctx, "BTCUSDT", "1h"); err == nil {
		t.Error("segunda suscripción del mismo par debería fallar")
	}
	// Con ctx cancelado, runStream libera la marca.
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		active := p.active["BTCUSDT|1h"]
		p.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("la marca activa no se liberó tras cancelar el contexto")
}

func TestParseWSKlineDField(t *testing.T) {
	p := NewWEEXProvider(nil, WeexConfig{})
	p.now = func() time.Time { return time.UnixMilli(1704067300000) }

	// Formato WEEX con "d" como array (d[]).
	msg := []byte(`{"topic":"BTCUSDT@kline_1h_LAST_PRICE","d":[{"t":1704067200000,"T":1704067260000,"s":"BTCUSDT","i":"1h","o":"50000","c":"50100","h":"50200","l":"49900","v":"10.5"}]}`)
	k, ok, err := p.parseWSKline(msg, "BTCUSDT", "1h")
	if err != nil || !ok {
		t.Fatalf("d[] no parseado: ok=%v err=%v", ok, err)
	}
	if k.Close != 50100 || k.Start.UnixMilli() != 1704067200000 || !k.Closed {
		t.Errorf("vela d[] inválida: %+v", k)
	}

	// Formato WEEX con "d" como objeto.
	msg2 := []byte(`{"d":{"t":1704067260000,"T":1704067320000,"s":"BTCUSDT","i":"1h","o":"50100","c":"50200","h":"50300","l":"50100","v":"5.5"}}`)
	k2, ok2, _ := p.parseWSKline(msg2, "BTCUSDT", "1h")
	if !ok2 || k2.Close != 50200 {
		t.Fatalf("d objeto no parseado: ok=%v k=%+v", ok2, k2)
	}
}

func TestWeexGetKlinesPaginatedSortedDeduped(t *testing.T) {
	const pageSize = 1000
	const step = int64(3600000)
	var calls atomic.Int32
	var limits []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		mu := sync.Mutex{}
		limit := 0
		fmt.Sscanf(r.URL.Query().Get("limit"), "%d", &limit)
		mu.Lock()
		limits = append(limits, limit)
		mu.Unlock()
		var start int64
		fmt.Sscanf(r.URL.Query().Get("startTime"), "%d", &start)
		if start >= 4000*step {
			w.Write([]byte("[]"))
			return
		}
		// Devolver velas con números como strings y con un duplicado en la
		// frontera de página: la vela de entrada de la página 2 ya estaba en la 1.
		rows := []string{}
		for i := 0; i < pageSize; i++ {
			ts := start + int64(i)*step
			rows = append(rows, klineRow(ts))
		}
		w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
	}))
	defer srv.Close()

	p := NewWEEXProvider(&fakeCandleStore{}, WeexConfig{RestURL: srv.URL})
	end := time.UnixMilli(5000 * step)
	start := time.UnixMilli(1)
	bars, err := p.GetKlines(context.Background(), "BTCUSDT", "1h", start, end, 2000)
	if err != nil {
		t.Fatalf("GetKlines: %v", err)
	}
	if len(bars) != 2000 {
		t.Fatalf("bars = %d, want 2000", len(bars))
	}
	for i := 1; i < len(bars); i++ {
		if bars[i].Start.Before(bars[i-1].Start) {
			t.Fatalf("velas desordenadas en índice %d", i)
		}
		if bars[i].Start.Equal(bars[i-1].Start) {
			t.Fatalf("vela duplicada en índice %d", i)
		}
	}
	if bars[0].Start.UnixMilli() != 1 {
		t.Errorf("primera vela = %d, want 1", bars[0].Start.UnixMilli())
	}
}

func TestWeexGetKlinesValidation(t *testing.T) {
	p := NewWEEXProvider(nil, WeexConfig{})
	ctx := context.Background()
	if _, err := p.GetKlines(ctx, "", "1h", time.Now().Add(-time.Hour), time.Now(), 10); err == nil {
		t.Error("symbol vacío debería fallar")
	}
	if _, err := p.GetKlines(ctx, "BTCUSDT", "", time.Now().Add(-time.Hour), time.Now(), 10); err == nil {
		t.Error("timeframe vacío debería fallar")
	}
	if _, err := p.GetKlines(ctx, "BTC US", "1h", time.Now().Add(-time.Hour), time.Now(), 10); err == nil {
		t.Error("symbol con espacio debería fallar")
	}
	if _, err := p.GetKlines(ctx, "BTCUSDT", "1x", time.Now().Add(-time.Hour), time.Now(), 10); err == nil {
		t.Error("timeframe inválido debería fallar")
	}
	if _, err := p.GetKlines(ctx, "BTCUSDT", "1h", time.Time{}, time.Now(), 10); err == nil {
		t.Error("start cero debería fallar")
	}
}

func TestValidateKlineVolumeAndNaN(t *testing.T) {
	good := domain.Kline{Symbol: "BTCUSDT", Timeframe: "1h", Start: time.Now(), Open: 100, High: 101, Low: 99, Close: 100.5, Volume: 10}
	if err := validateKline(good); err != nil {
		t.Errorf("vela buena rechazada: %v", err)
	}
	negVol := good
	negVol.Volume = -1
	if err := validateKline(negVol); err == nil {
		t.Error("volumen negativo debería fallar")
	}
	nanPrice := good
	nanPrice.Close = math.NaN()
	if err := validateKline(nanPrice); err == nil {
		t.Error("precio NaN debería fallar")
	}
	infPrice := good
	infPrice.High = math.Inf(1)
	if err := validateKline(infPrice); err == nil {
		t.Error("precio Inf debería fallar")
	}
}

func TestValidateKlinesData(t *testing.T) {
	base := domain.Kline{Symbol: "BTCUSDT", Timeframe: "1h", Open: 100, High: 101, Low: 99, Close: 100.5, Volume: 10}
	t1 := base
	t1.Start = time.UnixMilli(1000)
	t2 := base
	t2.Start = time.UnixMilli(2000)
	t3 := base
	t3.Start = time.UnixMilli(3000)

	if err := ValidateKlinesData([]domain.Kline{t1, t2, t3}); err != nil {
		t.Errorf("serie válida rechazada: %v", err)
	}
	if err := ValidateKlinesData([]domain.Kline{t2, t1, t3}); err == nil {
		t.Error("timestamps desordenados deberían fallar")
	}
	if err := ValidateKlinesData([]domain.Kline{t1, t1, t2}); err == nil {
		t.Error("vela duplicada debería fallar")
	}
	bad := t2
	bad.High = 98
	if err := ValidateKlinesData([]domain.Kline{t1, bad}); err == nil {
		t.Error("OHLC incoherente debería fallar")
	}
}

func TestWeexBackfillRetriesOn429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	p := NewWEEXProvider(&fakeCandleStore{}, WeexConfig{RestURL: srv.URL, MaxAttempts: 4})
	if err := p.Backfill(context.Background(), "BTCUSDT", "1h", 100); err != nil {
		t.Fatalf("Backfill con retry: %v", err)
	}
	if calls.Load() < 3 {
		t.Errorf("llamadas = %d, want >= 3 (reintentos)", calls.Load())
	}
}

func TestWeexGetKlinesDedupeOverlappingPages(t *testing.T) {
	// La página 2 repite la última vela de la página 1: GetKlines debe descartarla.
	var seq atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := seq.Add(1)
		var start int64
		fmt.Sscanf(r.URL.Query().Get("startTime"), "%d", &start)
		rows := []string{}
		for i := 0; i < 1000; i++ {
			rows = append(rows, klineRow(start+int64(i)*int64(3600000)))
		}
		if n >= 2 {
			// Página 2: repite el primer elemento de la página 1 (duplicado).
			rows = append(rows, klineRow(start))
		}
		w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
	}))
	defer srv.Close()

	p := NewWEEXProvider(&fakeCandleStore{}, WeexConfig{RestURL: srv.URL})
	bars, err := p.GetKlines(context.Background(), "BTCUSDT", "1h", time.UnixMilli(1), time.UnixMilli(5000*3600000), 1500)
	if err != nil {
		t.Fatalf("GetKlines: %v", err)
	}
	seen := make(map[int64]int, len(bars))
	for _, k := range bars {
		seen[k.Start.UnixMilli()]++
	}
	for ts, c := range seen {
		if c > 1 {
			t.Fatalf("ts=%d duplicado %d veces", ts, c)
		}
	}
}

func TestWeexConfigWithDefaults(t *testing.T) {
	c := (WeexConfig{}).withDefaults()
	if c.RestURL != "https://api-contract.weex.com" {
		t.Errorf("RestURL = %q", c.RestURL)
	}
	if c.WSURL != "wss://ws-contract.weex.com/v3/ws/public" {
		t.Errorf("WSURL = %q", c.WSURL)
	}
	if c.PriceType != "LAST_PRICE" {
		t.Errorf("PriceType = %q", c.PriceType)
	}
	if c.BackfillBars != 5000 || c.HTTPClient == nil || c.MaxAttempts <= 1 {
		t.Errorf("defaults inválidos: %+v", c)
	}
}
