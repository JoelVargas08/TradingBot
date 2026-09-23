package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"tradingview-bot/internal/domain"

	"github.com/gorilla/websocket"
)

// WeexConfig configura el acceso REST/WS al exchange WEEX (docus 1, 4-7).
type WeexConfig struct {
	RestURL       string
	WSURL         string
	PriceType     string
	DialTimeout   time.Duration
	PongWait      time.Duration
	ReconnectBase time.Duration
	ReconnectMax  time.Duration
	BackfillBars  int
	MaxAttempts   int
	HTTPClient    *http.Client
}

// RestMaxBars es el límite de velas por llamada REST de WEEX.
const RestMaxBars = 1000

func (c WeexConfig) withDefaults() WeexConfig {
	if c.RestURL == "" {
		c.RestURL = "https://api-contract.weex.com"
	}
	if c.WSURL == "" {
		c.WSURL = "wss://ws-contract.weex.com/v3/ws/public"
	}
	if c.PriceType == "" {
		c.PriceType = "LAST_PRICE"
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 10 * time.Second
	}
	if c.PongWait <= 0 {
		c.PongWait = 75 * time.Second
	}
	if c.ReconnectBase <= 0 {
		c.ReconnectBase = time.Second
	}
	if c.ReconnectMax <= 0 {
		c.ReconnectMax = 30 * time.Second
	}
	if c.BackfillBars <= 0 {
		c.BackfillBars = 5000
	}
	if c.MaxAttempts <= 1 {
		c.MaxAttempts = 4
	}
	if c.HTTPClient != nil {
		return c
	}
	c.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	return c
}

// WeexProvider es una MarketDataProvider sobre la API pública V3 de WEEX.
// El backfill guarda velas en el CandleStore interno y el streaming emite las
// velas tanto en formación como cerradas; la capa de servicio decide qué usar.
type WeexProvider struct {
	cfg    WeexConfig
	client *http.Client
	store  domain.CandleStore
	now    func() time.Time

	mu     sync.Mutex
	active map[string]bool
}

func NewWEEXProvider(store domain.CandleStore, cfg WeexConfig) *WeexProvider {
	cfg = cfg.withDefaults()
	return &WeexProvider{
		cfg:    cfg,
		client: cfg.HTTPClient,
		store:  store,
		now:    time.Now,
		active: make(map[string]bool),
	}
}

// Backfill descarga velas históricas cerradas de forma paginada (avanzando por
// timestamp, nunca dependiendo de una única llamada REST) y las persiste en el
// CandleStore. Reintenta errores transitorios (429/5xx) con backoff.
func (p *WeexProvider) Backfill(ctx context.Context, symbol, timeframe string, limit int) error {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	timeframe = strings.TrimSpace(timeframe)
	if err := validateSymbolTimeframe(symbol, timeframe); err != nil {
		return err
	}
	if limit <= 0 {
		limit = p.cfg.BackfillBars
	}
	current := time.Now().Truncate(parseInterval(timeframe))
	start := current.Add(-time.Duration(limit) * parseInterval(timeframe))
	rows, err := p.GetKlines(ctx, symbol, timeframe, start, current, limit)
	if err != nil {
		return fmt.Errorf("weex backfill %s %s: %w", symbol, timeframe, err)
	}
	seen := 0
	for _, k := range rows {
		if err := p.store.SaveCandle(ctx, k); err != nil && !errors.Is(err, domain.ErrDuplicate) {
			return err
		}
		seen++
	}
	log.Printf("weex backfill: %s %s -> %d velas almacenadas", symbol, timeframe, seen)
	return nil
}

// GetKlines descarga velas históricas en [start, end), con paginación por
// timestamp, ordenadas ascendentemente, sin duplicados y validadas.
func (p *WeexProvider) GetKlines(ctx context.Context, symbol, timeframe string, start, end time.Time, limit int) ([]domain.Kline, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	timeframe = strings.TrimSpace(timeframe)
	if err := validateSymbolTimeframe(symbol, timeframe); err != nil {
		return nil, err
	}
	if start.IsZero() {
		return nil, fmt.Errorf("start requerido")
	}
	if end.IsZero() {
		end = time.Now()
	}
	if limit <= 0 {
		limit = p.cfg.BackfillBars
	}
	step := parseInterval(timeframe)
	from := start
	var out []domain.Kline
	remaining := limit
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if remaining <= 0 {
			break
		}
		want := remaining
		if want > RestMaxBars {
			want = RestMaxBars
		}
		bars, err := p.fetchKlinesRange(ctx, symbol, timeframe, from, end, want)
		if err != nil {
			return nil, err
		}
		out = append(out, bars...)
		if len(bars) == 0 {
			break
		}
		last := bars[len(bars)-1].Start
		remaining -= len(bars)
		if len(bars) < want || !last.Add(step).Before(end) {
			break
		}
		from = last.Add(step)
	}
	out = dedupeSortKlines(out)
	return out, nil
}

func (p *WeexProvider) klinesURL(symbol, timeframe string, start, end time.Time, limit int) string {
	u := fmt.Sprintf("%s/capi/v3/market/klines?symbol=%s&interval=%s&limit=%d",
		strings.TrimRight(p.cfg.RestURL, "/"), symbol, timeframe, limit)
	if !start.IsZero() {
		u += fmt.Sprintf("&startTime=%d", start.UnixMilli())
	}
	if !end.IsZero() {
		u += fmt.Sprintf("&endTime=%d", end.UnixMilli())
	}
	return u
}

// fetchKlines recupera un bloque REST y reintenta errores transitorios.
func (p *WeexProvider) fetchKlinesRange(ctx context.Context, symbol, timeframe string, start, end time.Time, limit int) ([]domain.Kline, error) {
	var lastErr error
	for attempt := 1; attempt <= p.cfg.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		bars, retriable, err := p.doFetchKlines(ctx, symbol, timeframe, start, end, limit)
		if err == nil {
			return bars, nil
		}
		lastErr = err
		if !retriable {
			return nil, err
		}
		delay := time.Duration(1<<uint(attempt)) * 300 * time.Millisecond
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
		log.Printf("weex rest: intento %d/%d falló (%v); reintentando en %v", attempt, p.cfg.MaxAttempts, err, delay)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, lastErr
}

// doFetchKlines ejecuta una única llamada REST. Devuelve retriable=true para
// errores transitorios (429 o 5xx).
func (p *WeexProvider) doFetchKlines(ctx context.Context, symbol, timeframe string, start, end time.Time, limit int) ([]domain.Kline, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.klinesURL(symbol, timeframe, start, end, limit), nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, true, err
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("weex rest %d: %s", resp.StatusCode, string(body))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("weex rest %d: %s", resp.StatusCode, string(body))
	}
	bars, err := parseWeexKlines(body, symbol, timeframe)
	if err != nil {
		return nil, false, err
	}
	return bars, false, nil
}

// ValidateKlinesData valida un conjunto de velas que va a ser usado por el
// pipeline de aprendizaje: timestamps ordenados, sin duplicados, OHLCV coherente.
func ValidateKlinesData(ks []domain.Kline) error {
	for i := range ks {
		if err := validateKline(ks[i]); err != nil {
			return fmt.Errorf("kline[%d]: %w", i, err)
		}
		if i > 0 {
			if ks[i].Start.Before(ks[i-1].Start) {
				return fmt.Errorf("kline[%d]: timestamps no ordenados", i)
			}
			if ks[i].Start.Equal(ks[i-1].Start) {
				return fmt.Errorf("kline[%d]: vela duplicada", i)
			}
		}
	}
	return nil
}

// Subscribe abre un canal de velas en streaming para un symbol/timeframe.
// Gestiona internamente la reconexión con backoff y responde al ping de WEEX.
func (p *WeexProvider) Subscribe(ctx context.Context, symbol, timeframe string) (<-chan domain.Kline, error) {
	if symbol == "" || timeframe == "" {
		return nil, fmt.Errorf("symbol y timeframe requeridos")
	}
	key := mapKey(symbol, timeframe)
	p.mu.Lock()
	if p.active[key] {
		p.mu.Unlock()
		return nil, fmt.Errorf("ya existe una suscripción activa para %s %s", symbol, timeframe)
	}
	p.active[key] = true
	p.mu.Unlock()

	ch := make(chan domain.Kline, 256)
	go p.runStream(ctx, key, symbol, timeframe, ch)
	return ch, nil
}

func (p *WeexProvider) runStream(ctx context.Context, key, symbol, timeframe string, out chan<- domain.Kline) {
	defer func() {
		p.mu.Lock()
		delete(p.active, key)
		p.mu.Unlock()
	}()
	backoff := p.cfg.ReconnectBase
	lastTS := time.Time{}
	for {
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		err := p.runConnection(ctx, symbol, timeframe, out, &lastTS)
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err != nil {
			log.Printf("weex ws: conexión finalizada (%s %s): %v", symbol, timeframe, err)
		}
		// Backfill del hueco: tras perder la conexión, recuperar las velas que
		// pudieron quedar sin recibir entre la última vela y ahora.
		if !lastTS.IsZero() {
			p.backfillGap(ctx, symbol, timeframe, lastTS, out)
		}
		if time.Since(started) >= 60*time.Second {
			backoff = p.cfg.ReconnectBase
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > p.cfg.ReconnectMax {
			backoff = p.cfg.ReconnectMax
		}
	}
}

// backfillGap recupera las velas posteriores a lastTS y las emite al canal,
// de modo que el consumidor (servicio) las persista y reenvíe si están cerradas.
func (p *WeexProvider) backfillGap(ctx context.Context, symbol, timeframe string, lastTS time.Time, out chan<- domain.Kline) {
	step := parseInterval(timeframe)
	start := lastTS.Add(step)
	end := time.Now().Add(step)
	bars, err := p.GetKlines(ctx, symbol, timeframe, start, end, p.cfg.BackfillBars)
	if err != nil {
		log.Printf("weex ws: backfill de hueco %s %s falló: %v", symbol, timeframe, err)
		return
	}
	for _, k := range bars {
		if !k.Start.After(lastTS) {
			continue
		}
		select {
		case out <- k:
		case <-ctx.Done():
			return
		}
	}
	if len(bars) > 0 {
		log.Printf("weex ws: backfill de hueco %s %s -> %d velas", symbol, timeframe, len(bars))
	}
}

func (p *WeexProvider) channelName(symbol, timeframe string) string {
	return strings.ToUpper(symbol) + "@kline_" + timeframe + "_" + p.cfg.PriceType
}

func (p *WeexProvider) runConnection(ctx context.Context, symbol, timeframe string, out chan<- domain.Kline, lastTS *time.Time) error {
	dialer := websocket.Dialer{HandshakeTimeout: p.cfg.DialTimeout}
	conn, _, err := dialer.DialContext(ctx, p.cfg.WSURL, nil)
	if err != nil {
		return fmt.Errorf("dial ws: %w", err)
	}
	defer conn.Close()
	conn.SetReadLimit(4 << 20)

	sub := map[string]any{
		"method": "SUBSCRIBE",
		"params": []string{p.channelName(symbol, timeframe)},
		"id":     1,
	}
	if err := conn.WriteJSON(sub); err != nil {
		return fmt.Errorf("suscribir: %w", err)
	}

	for {
		conn.SetReadDeadline(time.Now().Add(p.cfg.PongWait))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if p.isPing(msg) {
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteJSON(map[string]any{"method": "PONG", "id": 1}); err != nil {
				return fmt.Errorf("pong: %w", err)
			}
			continue
		}
		c, ok, err := p.parseWSKline(msg, symbol, timeframe)
		if err != nil {
			log.Printf("weex ws: mensaje kline inválido: %v", err)
			continue
		}
		if !ok {
			continue
		}
		if c.Start.After(*lastTS) {
			*lastTS = c.Start
		}
		select {
		case out <- c:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (p *WeexProvider) isPing(msg []byte) bool {
	var m struct {
		Method string          `json:"method"`
		Type   string          `json:"type"`
		Ping   json.RawMessage `json:"ping"`
	}
	if err := json.Unmarshal(msg, &m); err != nil {
		return false
	}
	if len(m.Ping) > 0 && string(m.Ping) != "null" {
		return true
	}
	kind := strings.ToLower(m.Method + " " + m.Type)
	return strings.Contains(kind, "ping") && !strings.Contains(kind, "pong")
}

// parseWSKline extrae una vela del mensaje de un canal kline, tolerando que
// los campos vengan en "data" (objeto o array), en "d" (formato real de algunas
// versiones WEEX, objeto o array bajo "d[]") o en la raíz del mensaje.
func (p *WeexProvider) parseWSKline(msg []byte, symbol, timeframe string) (domain.Kline, bool, error) {
	var top struct {
		Data json.RawMessage `json:"data"`
		D    json.RawMessage `json:"d"`
	}
	if err := json.Unmarshal(msg, &top); err != nil {
		return domain.Kline{}, false, err
	}
	var candidates []json.RawMessage
	for _, raw := range []json.RawMessage{top.Data, top.D} {
		if len(raw) > 0 && string(raw) != "null" {
			candidates = append(candidates, raw)
		}
	}

	for _, payload := range candidates {
		var fields weexKlineFields
		if err := json.Unmarshal(payload, &fields); err == nil && fields.hasPrice() {
			return buildKline(fields, symbol, timeframe, p.isClosed(fields, p.now())), true, nil
		}
		var arr []weexKlineFields
		if err := json.Unmarshal(payload, &arr); err == nil && len(arr) > 0 {
			last := arr[len(arr)-1]
			if last.hasPrice() {
				return buildKline(last, symbol, timeframe, p.isClosed(last, p.now())), true, nil
			}
		}
	}

	var fields weexKlineFields
	if err := json.Unmarshal(msg, &fields); err == nil && fields.hasPrice() {
		return buildKline(fields, symbol, timeframe, p.isClosed(fields, p.now())), true, nil
	}
	return domain.Kline{}, false, nil
}

func (p *WeexProvider) isClosed(c weexKlineFields, now time.Time) bool {
	if c.CloseT <= 0 {
		return true
	}
	return now.UnixMilli() >= c.CloseT
}

func buildKline(c weexKlineFields, defaultSymbol, defaultTF string, closed bool) domain.Kline {
	symbol, tf := c.Symbol, c.Interval
	if symbol == "" {
		symbol = defaultSymbol
	}
	if tf == "" {
		tf = defaultTF
	}
	return domain.Kline{
		Symbol:    symbol,
		Timeframe: tf,
		Start:     time.UnixMilli(c.Start),
		Open:      c.Open.value,
		High:      c.High.value,
		Low:       c.Low.value,
		Close:     c.Close.value,
		Volume:    c.Volume.value,
		Closed:    closed,
	}
}

// weexKlineFields son los campos de una vela WEEX; los precios aceptan tanto
// números como strings codificados en JSON (conversión flexible).
type weexKlineFields struct {
	Start    int64      `json:"t"`
	CloseT   int64      `json:"T"`
	Symbol   string     `json:"s"`
	Interval string     `json:"i"`
	Open     flexNumber `json:"o"`
	High     flexNumber `json:"h"`
	Low      flexNumber `json:"l"`
	Close    flexNumber `json:"c"`
	Volume   flexNumber `json:"v"`
}

func (c weexKlineFields) hasPrice() bool {
	return c.Start > 0 && c.Close.value > 0 && c.Open.set && c.High.set && c.Low.set
}

type flexNumber struct {
	value float64
	set   bool
}

func (f *flexNumber) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return fmt.Errorf("número en string inválido %q", s)
		}
		f.value = v
		f.set = true
		return nil
	}
	var n float64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	f.value = n
	f.set = true
	return nil
}

// parseWeexKlines convierte el array-de-arrays del endpoint klines REST:
// [openTime, open, high, low, close, volume, ...]. Los campos numéricos pueden
// venir como número o como string; se valida coherencia de OHLC.
func parseWeexKlines(body []byte, symbol, timeframe string) ([]domain.Kline, error) {
	var rows [][]json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("parseando klines weex: %w", err)
	}
	out := make([]domain.Kline, 0, len(rows))
	for _, row := range rows {
		if len(row) < 6 {
			return nil, fmt.Errorf("fila de kline con %d campos, want ≥6", len(row))
		}
		var f weexKlineFields
		if err := json.Unmarshal(row[0], &f.Start); err != nil {
			if s, err2 := unmarshalString(row[0]); err2 == nil {
				if v, err3 := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err3 == nil {
					f.Start = v
				} else {
					return nil, fmt.Errorf("openTime inválido %q", s)
				}
			} else {
				return nil, fmt.Errorf("openTime inválido: %v", err)
			}
		}
		priceFields := []*flexNumber{&f.Open, &f.High, &f.Low, &f.Close, &f.Volume}
		for i, dst := range priceFields {
			if err := json.Unmarshal(row[1+i], dst); err != nil {
				return nil, fmt.Errorf("precio %d inválido: %w", i+1, err)
			}
		}
		if !f.hasPrice() {
			return nil, fmt.Errorf("kline incompleta (ts=%d)", f.Start)
		}
		k := buildKline(f, symbol, timeframe, true)
		if err := validateKline(k); err != nil {
			return nil, fmt.Errorf("kline inválida: %w", err)
		}
		out = append(out, k)
	}
	return dedupeSortKlines(out), nil
}

// dedupeSortKlines ordena ascendentemente y elimina duplicados por timestamp
// (conservando la última aparición, que normalmente es la más completa).
func dedupeSortKlines(ks []domain.Kline) []domain.Kline {
	if len(ks) == 0 {
		return ks
	}
	sort.SliceStable(ks, func(i, j int) bool { return ks[i].Start.Before(ks[j].Start) })
	seen := make(map[int64]bool, len(ks))
	out := ks[:0]
	for i := range ks {
		ts := ks[i].Start.UnixMilli()
		if seen[ts] {
			ks[i] = domain.Kline{}
			continue
		}
		seen[ts] = true
		out = append(out, ks[i])
	}
	return out[:len(out):len(out)]
}

func validateKline(k domain.Kline) error {
	if k.Symbol == "" || k.Timeframe == "" || k.Start.IsZero() {
		return fmt.Errorf("symbol/timeframe/ts requeridos")
	}
	vals := []float64{k.Open, k.High, k.Low, k.Close, k.Volume}
	for _, v := range vals {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("precio inválido (NaN/Inf)")
		}
		if v < 0 {
			return fmt.Errorf("precios/volumen no pueden ser negativos")
		}
	}
	if k.Open <= 0 || k.High <= 0 || k.Low <= 0 || k.Close <= 0 {
		return fmt.Errorf("precios deben ser positivos")
	}
	if k.Low > minF(k.Open, k.Close) || k.High < maxF(k.Open, k.Close) {
		return fmt.Errorf("OHLC incoherente")
	}
	return nil
}

// validateSymbolTimeframe valida el formato básico de símbolo y timeframe.
func validateSymbolTimeframe(symbol, timeframe string) error {
	if symbol == "" {
		return fmt.Errorf("symbol requerido")
	}
	for _, r := range symbol {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return fmt.Errorf("symbol inválido %q", symbol)
		}
	}
	if len(symbol) < 3 {
		return fmt.Errorf("symbol inválido %q", symbol)
	}
	if err := validateTimeframe(timeframe); err != nil {
		return err
	}
	return nil
}

func unmarshalString(b []byte) (string, error) {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return "", err
	}
	return s, nil
}

func mapKey(symbol, timeframe string) string {
	return symbol + "|" + timeframe
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
