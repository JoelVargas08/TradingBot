package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

func TestParseWeexKlinesPageSkipInvalid(t *testing.T) {
	body := `[
		[1704067200000,"60000.0","60100.0","59900.0","60050.5","123.45"],
		[1704067260000,"60000.0","60100.0","59900.0","60050.5","-1"],
		[1704067320000,"60000.0","60100.0","59900.0","60050.5","123.45"]
	]`
	page, err := parseWeexKlinesPage([]byte(body), "BTCUSDT", "1h", true)
	if err != nil {
		t.Fatalf("parseWeexKlinesPage: %v", err)
	}
	if page.RawRows != 3 || page.Skipped != 1 || len(page.Bars) != 2 {
		t.Fatalf("page = raw:%d skipped:%d bars:%d", page.RawRows, page.Skipped, len(page.Bars))
	}
	if page.RawOldest.UnixMilli() != 1704067200000 {
		t.Errorf("RawOldest = %d, want 1704067200000", page.RawOldest.UnixMilli())
	}
	if _, err := parseWeexKlinesPage([]byte(body), "BTCUSDT", "1h", false); err == nil {
		t.Error("modo estricto debería rechazar la vela inválida")
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

func TestWeexBackfillUsesHistoryEndpoint(t *testing.T) {
	const nowMs = int64(1704067200000)
	var path, size string
	var start, end int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		size = r.URL.Query().Get("size")
		start = queryInt(r, "startTime")
		end = queryInt(r, "endTime")
		w.Write([]byte("[" + klineRow(nowMs-2*3600000) + "]"))
	}))
	defer srv.Close()

	store := &fakeCandleStore{dup: make(map[string]bool)}
	p := NewWEEXProvider(store, WeexConfig{RestURL: srv.URL, BackfillBars: 300, HistoryPageDelay: time.Nanosecond})
	p.now = func() time.Time { return time.UnixMilli(nowMs) }
	if err := p.Backfill(context.Background(), "BTCUSDT", "1h", 300); err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if path != "/capi/v3/market/historyKlines" {
		t.Errorf("path = %q, want /capi/v3/market/historyKlines", path)
	}
	if size != "100" {
		t.Errorf("size = %s, want 100", size)
	}
	if end != nowMs {
		t.Errorf("endTime = %d, want %d", end, nowMs)
	}
	if start != nowMs-300*3600000 {
		t.Errorf("startTime = %d, want %d", start, nowMs-300*3600000)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.candles) != 1 {
		t.Fatalf("velas = %d, want 1", len(store.candles))
	}
	if !store.candles[0].Closed || store.candles[0].Close != 60050.5 {
		t.Errorf("vela guardada inválida: %+v", store.candles[0])
	}
}

func TestWeexBackfillHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := NewWEEXProvider(&fakeCandleStore{}, WeexConfig{RestURL: srv.URL, MaxAttempts: 2, HistoryPageDelay: time.Nanosecond})
	if err := p.Backfill(context.Background(), "BTCUSDT", "1h", 10); err == nil {
		t.Error("Backfill con error HTTP debería fallar")
	}
}

func TestWeexBackfillPaginatesHistory(t *testing.T) {
	const nowMs = int64(1704067200000)
	const step = int64(3600000)
	const total = 250
	bars := make([]int64, 0, total)
	for i := total; i >= 1; i-- {
		bars = append(bars, nowMs-int64(i)*step)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		serveHistory(w, r, bars)
	}))
	defer srv.Close()

	store := &fakeCandleStore{dup: make(map[string]bool)}
	p := NewWEEXProvider(store, WeexConfig{RestURL: srv.URL, HistoryPageDelay: time.Nanosecond})
	p.now = func() time.Time { return time.UnixMilli(nowMs) }
	if err := p.Backfill(context.Background(), "BTCUSDT", "1h", total); err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if calls.Load() < 3 {
		t.Errorf("llamadas = %d, want >= 3 (paginación)", calls.Load())
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.candles) != total {
		t.Fatalf("velas = %d, want %d", len(store.candles), total)
	}
	if got := store.candles[0].Start.UnixMilli(); got != nowMs-total*step {
		t.Errorf("primera vela = %d, want %d", got, nowMs-total*step)
	}
	if got := store.candles[len(store.candles)-1].Start.UnixMilli(); got != nowMs-step {
		t.Errorf("última vela = %d, want %d", got, nowMs-step)
	}
}

func TestWeexBackfillSkipsInvalidHistoryAndPaginates(t *testing.T) {
	const nowMs = int64(1704067200000)
	const step = int64(3600000)
	const total = 250
	const badTS = nowMs - 50*step
	bars := make([]int64, 0, total)
	for i := total; i >= 1; i-- {
		bars = append(bars, nowMs-int64(i)*step)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := queryInt(r, "startTime")
		end := queryInt(r, "endTime")
		rows := make([]string, 0, HistoryMaxBars)
		for i := len(bars) - 1; i >= 0 && len(rows) < HistoryMaxBars; i-- {
			ts := bars[i]
			if ts < start || ts > end {
				continue
			}
			if ts == badTS {
				rows = append(rows, fmt.Sprintf(`[%d,"60000","60000","59000","60000","-1"]`, ts))
				continue
			}
			rows = append(rows, klineRow(ts))
		}
		w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
	}))
	defer srv.Close()

	store := &fakeCandleStore{dup: make(map[string]bool)}
	p := NewWEEXProvider(store, WeexConfig{
		RestURL:            srv.URL,
		HistoryPageDelay:   time.Nanosecond,
		SkipInvalidHistory: true,
	})
	p.now = func() time.Time { return time.UnixMilli(nowMs) }
	if err := p.Backfill(context.Background(), "BTCUSDT", "1h", total); err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.candles) != total-1 {
		t.Fatalf("velas = %d, want %d", len(store.candles), total-1)
	}
	if got := store.candles[0].Start.UnixMilli(); got != nowMs-total*step {
		t.Errorf("primera vela = %d, want %d", got, nowMs-total*step)
	}
	if got := store.candles[len(store.candles)-1].Start.UnixMilli(); got != nowMs-step {
		t.Errorf("última vela = %d, want %d", got, nowMs-step)
	}
}

func TestWeexBackfillStrictRejectsInvalidHistory(t *testing.T) {
	const nowMs = int64(1704067200000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[[` + strconv.FormatInt(nowMs-3600000, 10) + `,"60000","60000","59000","60000","-1"]]`))
	}))
	defer srv.Close()

	p := NewWEEXProvider(&fakeCandleStore{dup: make(map[string]bool)}, WeexConfig{
		RestURL:          srv.URL,
		HistoryPageDelay: time.Nanosecond,
	})
	p.now = func() time.Time { return time.UnixMilli(nowMs) }
	if err := p.Backfill(context.Background(), "BTCUSDT", "1h", 10); err == nil {
		t.Fatal("Backfill estricto debería fallar con una vela inválida")
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

func TestWeexGetKlinesPaginatesBeyondOldCap(t *testing.T) {
	const step = int64(3600000)
	const total = 1200
	base := int64(1704067200000) + total*step
	bars := make([]int64, 0, total)
	for i := total; i >= 1; i-- {
		bars = append(bars, base-int64(i)*step)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/capi/v3/market/historyKlines" {
			t.Errorf("path = %q", r.URL.Path)
		}
		calls.Add(1)
		serveHistory(w, r, bars)
	}))
	defer srv.Close()

	p := NewWEEXProvider(&fakeCandleStore{}, WeexConfig{RestURL: srv.URL, HistoryPageDelay: time.Nanosecond})
	start := time.UnixMilli(base - total*step)
	end := time.UnixMilli(base)
	got, err := p.GetKlines(context.Background(), "BTCUSDT", "1h", start, end, total)
	if err != nil {
		t.Fatalf("GetKlines: %v", err)
	}
	if calls.Load() < 12 {
		t.Errorf("llamadas = %d, want >= 12 (100 velas por llamada)", calls.Load())
	}
	if len(got) != total {
		t.Fatalf("bars = %d, want %d", len(got), total)
	}
	for i := 1; i < len(got); i++ {
		if !got[i].Start.After(got[i-1].Start) {
			t.Fatalf("velas no estrictamente crecientes en índice %d", i)
		}
	}
	if got[0].Start.UnixMilli() != base-total*step || got[len(got)-1].Start.UnixMilli() != base-step {
		t.Errorf("rango devuelto = [%d,%d]", got[0].Start.UnixMilli(), got[len(got)-1].Start.UnixMilli())
	}
}

func TestWeexGetKlinesWindowsMax90Days(t *testing.T) {
	const day = int64(24 * 3600000)
	const total = 200
	base := int64(1704067200000) + total*day
	bars := make([]int64, 0, total)
	for i := total; i >= 1; i-- {
		bars = append(bars, base-int64(i)*day)
	}
	var ranges [][2]int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := queryInt(r, "startTime")
		end := queryInt(r, "endTime")
		ranges = append(ranges, [2]int64{start, end})
		serveHistory(w, r, bars)
	}))
	defer srv.Close()

	p := NewWEEXProvider(&fakeCandleStore{}, WeexConfig{RestURL: srv.URL, HistoryPageDelay: time.Nanosecond})
	got, err := p.GetKlines(context.Background(), "BTCUSDT", "1d",
		time.UnixMilli(base-total*day), time.UnixMilli(base), total)
	if err != nil {
		t.Fatalf("GetKlines: %v", err)
	}
	if len(got) != total {
		t.Fatalf("bars = %d, want %d", len(got), total)
	}
	if len(ranges) < 3 {
		t.Errorf("consultas = %d, want >= 3 ventanas de 90 días", len(ranges))
	}
	maxRange := int64(HistoryMaxRange / time.Millisecond)
	for i, r := range ranges {
		if r[1]-r[0] > maxRange {
			t.Errorf("ventana %d excede 90 días: %d ms", i, r[1]-r[0])
		}
	}
}

func TestWeexGetKlinesStopsWhenHistoryExhausted(t *testing.T) {
	const step = int64(3600000)
	const total = 250
	base := int64(1704067200000) + total*step
	bars := make([]int64, 0, total)
	for i := total; i >= 1; i-- {
		bars = append(bars, base-int64(i)*step)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		serveHistory(w, r, bars)
	}))
	defer srv.Close()

	p := NewWEEXProvider(&fakeCandleStore{}, WeexConfig{RestURL: srv.URL, HistoryPageDelay: time.Nanosecond})
	start := time.UnixMilli(base - 365*24*step)
	got, err := p.GetKlines(context.Background(), "BTCUSDT", "1h", start, time.UnixMilli(base), 100000)
	if err != nil {
		t.Fatalf("GetKlines: %v", err)
	}
	if len(got) != total {
		t.Fatalf("bars = %d, want %d", len(got), total)
	}
	if got[0].Start.UnixMilli() != base-total*step {
		t.Errorf("primera vela = %d, want %d", got[0].Start.UnixMilli(), base-total*step)
	}
	if calls.Load() > 5 {
		t.Errorf("llamadas = %d, want <= 5 (parar al agotar histórico)", calls.Load())
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
	if _, err := p.GetKlines(ctx, "BTCUSDT", "1h", time.Now(), time.Now().Add(-time.Hour), 10); err == nil {
		t.Error("rango invertido debería fallar")
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

func TestWeexGetKlinesDeduplicatesWithinResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows := []string{}
		for i := 1; i <= 10; i++ {
			rows = append(rows, klineRow(int64(i)*int64(3600000)))
		}
		rows = append(rows, klineRow(5*int64(3600000)))
		w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
	}))
	defer srv.Close()

	p := NewWEEXProvider(&fakeCandleStore{}, WeexConfig{RestURL: srv.URL})
	bars, err := p.GetKlines(context.Background(), "BTCUSDT", "1h", time.UnixMilli(1), time.UnixMilli(5000*3600000), 1000)
	if err != nil {
		t.Fatalf("GetKlines: %v", err)
	}
	if len(bars) != 10 {
		t.Fatalf("bars = %d, want 10 tras deduplicar", len(bars))
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
	if c.BackfillBars != RestMaxBars || c.HTTPClient == nil || c.MaxAttempts <= 1 {
		t.Errorf("defaults inválidos: %+v", c)
	}
	if c.HistoryPageDelay <= 0 {
		t.Errorf("HistoryPageDelay = %v, want > 0", c.HistoryPageDelay)
	}
}

func queryInt(r *http.Request, name string) int64 {
	v, _ := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	return v
}

// serveHistory simula /historyKlines: devuelve como máximo HistoryMaxBars velas
// dentro de [startTime, endTime], de la más reciente a la más antigua.
func serveHistory(w http.ResponseWriter, r *http.Request, bars []int64) {
	start := queryInt(r, "startTime")
	end := queryInt(r, "endTime")
	rows := make([]string, 0, HistoryMaxBars)
	for i := len(bars) - 1; i >= 0 && len(rows) < HistoryMaxBars; i-- {
		ts := bars[i]
		if ts < start || ts > end {
			continue
		}
		rows = append(rows, klineRow(ts))
	}
	w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
}
