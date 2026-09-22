package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

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
	HTTPClient    *http.Client
}

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
		c.BackfillBars = 300
	}
	if c.HTTPClient != nil {
		return c
	}
	c.HTTPClient = &http.Client{Timeout: 20 * time.Second}
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

// Backfill descarga velas históricas cerradas y las persiste en el CandleStore.
func (p *WeexProvider) Backfill(ctx context.Context, symbol, timeframe string, limit int) error {
	if symbol == "" {
		return fmt.Errorf("symbol requerido")
	}
	if timeframe == "" {
		return fmt.Errorf("timeframe requerido")
	}
	if limit <= 0 {
		limit = p.cfg.BackfillBars
	}
	if limit > 1000 {
		limit = 1000
	}
	bars, err := p.fetchKlines(ctx, symbol, timeframe, limit)
	if err != nil {
		return err
	}
	for _, k := range bars {
		if err := p.store.SaveCandle(ctx, k); err != nil && !errors.Is(err, domain.ErrDuplicate) {
			return err
		}
	}
	log.Printf("weex backfill: %s %s -> %d velas almacenadas", symbol, timeframe, len(bars))
	return nil
}

func (p *WeexProvider) klinesURL(symbol, timeframe string, limit int) string {
	return fmt.Sprintf("%s/capi/v3/market/klines?symbol=%s&interval=%s&limit=%d",
		strings.TrimRight(p.cfg.RestURL, "/"), symbol, timeframe, limit)
}

func (p *WeexProvider) fetchKlines(ctx context.Context, symbol, timeframe string, limit int) ([]domain.Kline, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.klinesURL(symbol, timeframe, limit), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weex rest %d: %s", resp.StatusCode, string(body))
	}
	return parseWeexKlines(body, symbol, timeframe)
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
	for {
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		err := p.runConnection(ctx, symbol, timeframe, out)
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err != nil {
			log.Printf("weex ws: conexión finalizada (%s %s): %v", symbol, timeframe, err)
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

func (p *WeexProvider) channelName(symbol, timeframe string) string {
	return strings.ToUpper(symbol) + "@kline_" + timeframe + "_" + p.cfg.PriceType
}

func (p *WeexProvider) runConnection(ctx context.Context, symbol, timeframe string, out chan<- domain.Kline) error {
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
// los campos vengan en "data" (objeto o array) o en la raíz del mensaje.
func (p *WeexProvider) parseWSKline(msg []byte, symbol, timeframe string) (domain.Kline, bool, error) {
	var top struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(msg, &top); err != nil {
		return domain.Kline{}, false, err
	}
	payload := msg
	if len(top.Data) > 0 && string(top.Data) != "null" {
		payload = top.Data
	}

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
	return out, nil
}

func validateKline(k domain.Kline) error {
	if k.Symbol == "" || k.Timeframe == "" || k.Start.IsZero() {
		return fmt.Errorf("symbol/timeframe/ts requeridos")
	}
	if k.Open <= 0 || k.High <= 0 || k.Low <= 0 || k.Close <= 0 {
		return fmt.Errorf("precios deben ser positivos")
	}
	if k.Low > minF(k.Open, k.Close) || k.High < maxF(k.Open, k.Close) {
		return fmt.Errorf("OHLC incoherente")
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
