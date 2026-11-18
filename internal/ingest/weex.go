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
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"tradingview-bot/internal/domain"
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
	// HistoryPageDelay separa las llamadas a /historyKlines para no saturar
	// el rate limit público. No afecta a /klines ni al streaming.
	HistoryPageDelay time.Duration
	// SkipInvalidHistory descarta velas históricas corruptas (OHLCV inválido)
	// en lugar de abortar toda la descarga. Solo se aplica al backfill.
	SkipInvalidHistory bool
	// ClockSkew se resta al reloj local antes de usarlo como endTime para no
	// enviar nunca un límite futuro al servidor (-1142).
	ClockSkew time.Duration
	// GapRecoveryTimeout acota cuánto puede bloquear el stream una recuperación
	// de hueco por REST; al agotarse, el intervalo queda pendiente y explícito.
	GapRecoveryTimeout time.Duration
	// MaxGapRetries es el número de intentos tras el cual un hueco que WEEX no
	// devuelve se marca como irrecuperable (estado degradado explícito).
	MaxGapRetries int
	// GapRetryMax es la espera máxima entre reintentos de un hueco pendiente.
	GapRetryMax time.Duration
	// GapUnrecoverableAfter es el margen tras el cierre de la última vela de un
	// hueco antes de poder declararlo irrecuperable: evita confundir el retraso
	// normal de publicación REST con una ausencia real de datos.
	GapUnrecoverableAfter time.Duration
	// UserAgent identifica al cliente en el handshake del canal público (WEEX
	// exige cabecera User-Agent). Es genérico: no incluye datos del usuario.
	UserAgent string
}

const (
	// RestMaxBars es el tope de /capi/v3/market/klines, que solo devuelve las
	// velas más recientes e ignora startTime/endTime.
	RestMaxBars = 1000
	// HistoryMaxBars es el máximo de velas por respuesta de /historyKlines.
	HistoryMaxBars = 100
	// HistoryMaxRange es el rango temporal máximo por consulta de /historyKlines.
	HistoryMaxRange = 90 * 24 * time.Hour
	// MaxBackfillBars acota una descarga histórica para evitar consumos abusivos.
	MaxBackfillBars = 200000
)

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
		c.BackfillBars = RestMaxBars
	}
	if c.MaxAttempts <= 1 {
		c.MaxAttempts = 4
	}
	if c.HistoryPageDelay <= 0 {
		c.HistoryPageDelay = 50 * time.Millisecond
	}
	if c.ClockSkew <= 0 {
		c.ClockSkew = HistoryClockSkew
	}
	if c.GapRecoveryTimeout <= 0 {
		c.GapRecoveryTimeout = 20 * time.Second
	}
	if c.MaxGapRetries <= 0 {
		c.MaxGapRetries = 6
	}
	if c.GapRetryMax <= 0 {
		c.GapRetryMax = 60 * time.Second
	}
	if c.GapUnrecoverableAfter <= 0 {
		c.GapUnrecoverableAfter = 2 * time.Minute
	}
	if c.UserAgent == "" {
		c.UserAgent = "tradingview-bot-weex-ingest/1.0"
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
	// sleep espera d o hasta que se cancele ctx (false). Inyectable en tests
	// para verificar backoff y Retry-After sin esperas reales.
	sleep func(ctx context.Context, d time.Duration) bool

	mu     sync.Mutex
	active map[string]bool
	feeds  map[string]*feedState
	sink   IngestEventSink

	errMu sync.Mutex
	errs  []ProviderError
}

func NewWEEXProvider(store domain.CandleStore, cfg WeexConfig) *WeexProvider {
	cfg = cfg.withDefaults()
	return &WeexProvider{
		cfg:    cfg,
		client: cfg.HTTPClient,
		store:  store,
		now:    time.Now,
		sleep:  waitOrDone,
		active: make(map[string]bool),
		feeds:  make(map[string]*feedState),
	}
}

// Backfill descarga velas históricas cerradas y las persiste en el CandleStore.
// Usa /historyKlines, que sí respeta startTime/endTime, con paginación inversa
// en ventanas de 90 días (máximo de la API) y 100 velas por respuesta.
func (p *WeexProvider) Backfill(ctx context.Context, symbol, timeframe string, limit int) error {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	timeframe = strings.TrimSpace(timeframe)
	if err := validateSymbolTimeframe(symbol, timeframe); err != nil {
		return err
	}
	if limit <= 0 {
		limit = p.cfg.BackfillBars
	}
	if limit > MaxBackfillBars {
		log.Printf("weex backfill: limit %d excede el máximo seguro (%d); se acotará", limit, MaxBackfillBars)
		limit = MaxBackfillBars
	}
	step := parseInterval(timeframe)
	began := time.Now()
	current := alignDown(p.now(), step)
	start := current.Add(-time.Duration(limit) * step)
	st := p.feed(symbol, timeframe)
	rec := BackfillRecord{At: p.now(), Trigger: "inicial", Symbol: symbol, Timeframe: timeframe,
		From: start, To: current.Add(-step), Expected: limit,
		Request: describeHistoryRequest(symbol, timeframe, start, current, HistoryMaxBars)}
	rows, err := p.GetKlines(ctx, symbol, timeframe, start, current, limit)
	if err != nil {
		rec.Outcome, rec.Error, rec.Duration = "fallido", sanitizeMessage(err.Error()), time.Since(began)
		st.mu.Lock()
		rec.CursorBefore, rec.CursorAfter = st.lastClosed, st.lastClosed
		st.addBackfill(rec)
		st.counters.RecoveryFailures++
		st.mu.Unlock()
		p.emitEvent(ctx, st, IngestEvent{Key: eventKey("backfill_inicial", symbol, timeframe, start, rec.At), Symbol: symbol, Timeframe: timeframe,
			Kind: EventBackfillFailed, Severity: SeverityFail, From: start, To: rec.To, Expected: limit, Message: rec.Error})
		return fmt.Errorf("weex backfill %s %s: %w", symbol, timeframe, err)
	}
	seen := 0
	var newest time.Time
	for _, k := range rows {
		if err := p.store.SaveCandle(ctx, k); err != nil && !errors.Is(err, domain.ErrDuplicate) {
			return err
		}
		seen++
		if k.Start.After(newest) {
			newest = k.Start
		}
	}
	// El backfill inicial siembra el cursor del stream: a partir de aquí, la
	// reconexión y el cambio de vela recuperan solo lo posterior a esta vela.
	st.mu.Lock()
	rec.CursorBefore = st.lastClosed
	if !newest.IsZero() {
		st.markClosed(newest, "rest", p.now())
	}
	rec.CursorAfter = st.lastClosed
	rec.Received, rec.Emitted, rec.Duration = seen, seen, time.Since(began)
	rec.Outcome = "ok"
	if seen < limit {
		rec.Outcome = "parcial"
	}
	st.addBackfill(rec)
	st.mu.Unlock()
	if seen < limit {
		log.Printf("weex backfill: %s %s -> %d velas (pedidas %d); el histórico disponible en /historyKlines puede ser menor", symbol, timeframe, seen, limit)
	} else {
		log.Printf("weex backfill: %s %s -> %d velas almacenadas", symbol, timeframe, seen)
	}
	return nil
}

// GetKlines descarga velas cerradas por rango usando /historyKlines, que sí
// honra startTime/endTime. La API limita cada respuesta a HistoryMaxBars y cada
// consulta a HistoryMaxRange, así que se pagina hacia atrás por endTime y se
// recorren ventanas consecutivas hasta cubrir [start, end] o alcanzar limit.
func (p *WeexProvider) GetKlines(ctx context.Context, symbol, timeframe string, start, end time.Time, limit int) ([]domain.Kline, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	timeframe = strings.TrimSpace(timeframe)
	if err := validateSymbolTimeframe(symbol, timeframe); err != nil {
		return nil, err
	}
	if start.IsZero() {
		return nil, fmt.Errorf("start requerido")
	}
	now := p.now()
	if end.IsZero() {
		end = now
	}
	if !end.After(start) {
		return nil, fmt.Errorf("rango inválido: end debe ser posterior a start")
	}
	// WEEX rechaza endTime/startTime futuros (-1142). La vela en formación no
	// se devuelve nunca, así que recortar al reloj (menos skew) no pierde datos.
	if safeEnd := now.Add(-p.cfg.ClockSkew); end.After(safeEnd) {
		end = safeEnd
	}
	if end.Before(start) {
		// Aún no existe ninguna vela cerrada en el rango pedido: no es un error
		// del proveedor y no se envía una petición fuera de contrato.
		return nil, nil
	}
	if limit <= 0 {
		limit = p.cfg.BackfillBars
	}
	if limit > MaxBackfillBars {
		limit = MaxBackfillBars
	}
	bars, err := p.fetchHistoryRange(ctx, symbol, timeframe, start, end, limit)
	if err != nil {
		return nil, err
	}
	if len(bars) > limit {
		bars = bars[len(bars)-limit:]
	}
	return bars, nil
}

type historyPage struct {
	Bars      []domain.Kline
	RawRows   int
	RawOldest time.Time
	Skipped   int
}

// fetchHistoryRange recorre el rango de más reciente a más antiguo. En cada
// ventana de HistoryMaxRange pagina hacia atrás usando el timestamp de la vela
// más antigua recibida. Si se descartan velas inválidas, el avance usa el
// timestamp crudo para no dejar huecos ni entrar en bucles.
func (p *WeexProvider) fetchHistoryRange(ctx context.Context, symbol, timeframe string, start, end time.Time, limit int) ([]domain.Kline, error) {
	var out []domain.Kline
	anchorEnd := end
	for len(out) < limit && !anchorEnd.Before(start) {
		windowStart := anchorEnd.Add(-HistoryMaxRange)
		if windowStart.Before(start) {
			windowStart = start
		}
		pageEnd := anchorEnd
		firstPage := true
		exhausted := false
		for len(out) < limit {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !firstPage && !waitHistoryPage(ctx, p.cfg.HistoryPageDelay) {
				return nil, ctx.Err()
			}
			firstPage = false
			page, err := p.fetchHistoryPage(ctx, symbol, timeframe, windowStart, pageEnd)
			if err != nil {
				return nil, err
			}
			if page.RawRows == 0 {
				exhausted = true
				break
			}
			if page.Skipped > 0 {
				log.Printf("weex history: %s %s -> %d velas inválidas descartadas", symbol, timeframe, page.Skipped)
			}
			out = append(out, page.Bars...)
			if page.RawRows < HistoryMaxBars {
				break
			}
			oldest := page.RawOldest
			if oldest.IsZero() && len(page.Bars) > 0 {
				oldest = page.Bars[0].Start
			}
			if oldest.IsZero() {
				break
			}
			nextEnd := oldest.Add(-time.Millisecond)
			if nextEnd.Before(windowStart) {
				break
			}
			pageEnd = nextEnd
		}
		if exhausted {
			break
		}
		nextWindowEnd := windowStart.Add(-time.Millisecond)
		if !nextWindowEnd.Before(anchorEnd) {
			break
		}
		anchorEnd = nextWindowEnd
	}
	return dedupeSortKlines(out), nil
}

func waitHistoryPage(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// historyURL construye la petición con el parámetro documentado `limit`
// (1..100). El antiguo `size` no existe en la API y WEEX lo ignoraba.
func (p *WeexProvider) historyURL(symbol, timeframe string, start, end time.Time) string {
	q := url.Values{}
	q.Set("symbol", symbol)
	q.Set("interval", timeframe)
	q.Set("startTime", strconv.FormatInt(start.UnixMilli(), 10))
	q.Set("endTime", strconv.FormatInt(end.UnixMilli(), 10))
	q.Set("limit", strconv.Itoa(HistoryMaxBars))
	return strings.TrimRight(p.cfg.RestURL, "/") + "/capi/v3/market/historyKlines?" + q.Encode()
}

// fetchHistoryPage recupera un bloque de /historyKlines. Reintenta solo
// errores transitorios (429, 5xx, timeout, red) con backoff acotado y respeta
// Retry-After. Los errores de negocio (400 -11xx) no se reintentan.
func (p *WeexProvider) fetchHistoryPage(ctx context.Context, symbol, timeframe string, start, end time.Time) (historyPage, error) {
	reqDesc := describeHistoryRequest(symbol, timeframe, start, end, HistoryMaxBars)
	if err := validateHistoryWindow(start, end, p.now()); err != nil {
		he := &HistoryError{Kind: HistoryErrContract, Message: err.Error(), Request: reqDesc}
		p.recordError(ProviderError{Component: "rest", Symbol: symbol, Timeframe: timeframe, Kind: he.Kind, Message: he.Message, Severity: SeverityFail})
		log.Printf("weex history: petición NO enviada por violar el contrato: %s (%v)", reqDesc, err)
		return historyPage{}, he
	}
	var lastErr error
	for attempt := 1; attempt <= p.cfg.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return historyPage{}, ctx.Err()
		}
		page, err := p.doFetchHistory(ctx, symbol, timeframe, start, end)
		if err == nil {
			return page, nil
		}
		var he *HistoryError
		if !errors.As(err, &he) {
			// Cancelación del contexto u otro error no clasificable.
			return historyPage{}, err
		}
		he.Request = reqDesc
		lastErr = he
		sev := SeverityWarn
		if !he.Retriable || attempt == p.cfg.MaxAttempts {
			sev = SeverityFail
		}
		p.recordError(ProviderError{Component: "rest", Symbol: symbol, Timeframe: timeframe, Kind: he.Kind,
			HTTPStatus: he.HTTPStatus, Code: he.Code, Message: he.Message, Severity: sev})
		if !he.Retriable {
			log.Printf("weex history: error no reintentable [%s] status=%d code=%d msg=%q petición=%s",
				he.Kind, he.HTTPStatus, he.Code, he.Message, reqDesc)
			return historyPage{}, he
		}
		if attempt == p.cfg.MaxAttempts {
			break
		}
		delay := retryBackoff(attempt)
		if he.RetryAfter > 0 {
			delay = he.RetryAfter
		}
		log.Printf("weex history: intento %d/%d falló [%s] status=%d code=%d; reintentando en %v (petición=%s)",
			attempt, p.cfg.MaxAttempts, he.Kind, he.HTTPStatus, he.Code, delay, reqDesc)
		if !p.sleep(ctx, delay) {
			return historyPage{}, ctx.Err()
		}
	}
	return historyPage{}, lastErr
}

// doFetchHistory ejecuta una única llamada REST y clasifica su resultado.
func (p *WeexProvider) doFetchHistory(ctx context.Context, symbol, timeframe string, start, end time.Time) (historyPage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.historyURL(symbol, timeframe, start, end), nil)
	if err != nil {
		return historyPage{}, &HistoryError{Kind: HistoryErrContract, Message: err.Error()}
	}
	req.Header.Set("User-Agent", p.cfg.UserAgent)
	resp, err := p.client.Do(req)
	if err != nil {
		return historyPage{}, classifyTransportError(ctx, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return historyPage{}, classifyTransportError(ctx, err)
	}
	if resp.StatusCode != http.StatusOK {
		return historyPage{}, classifyHistoryResponse(resp.StatusCode, body, resp.Header, p.now())
	}
	page, err := parseWeexKlinesPage(body, symbol, timeframe, p.cfg.SkipInvalidHistory)
	if err != nil {
		return historyPage{}, &HistoryError{Kind: HistoryErrParse, HTTPStatus: resp.StatusCode, Message: sanitizeMessage(err.Error())}
	}
	return page, nil
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
	page, err := parseWeexKlinesPage(body, symbol, timeframe, false)
	if err != nil {
		return nil, err
	}
	return page.Bars, nil
}

// parseWeexKlinesPage permite descartar filas inválidas en descargas
// históricas. En modo estricto cualquier fila corrupta aborta el parseo.
func parseWeexKlinesPage(body []byte, symbol, timeframe string, skipInvalid bool) (historyPage, error) {
	var rows [][]json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return historyPage{}, fmt.Errorf("parseando klines weex: %w", err)
	}
	page := historyPage{RawRows: len(rows)}
	page.Bars = make([]domain.Kline, 0, len(rows))
	for _, row := range rows {
		k, ts, err := parseWeexKlineRow(row, symbol, timeframe)
		if ts > 0 {
			start := time.UnixMilli(ts)
			if page.RawOldest.IsZero() || start.Before(page.RawOldest) {
				page.RawOldest = start
			}
		}
		if err != nil {
			if !skipInvalid {
				return historyPage{}, err
			}
			page.Skipped++
			continue
		}
		page.Bars = append(page.Bars, k)
	}
	page.Bars = dedupeSortKlines(page.Bars)
	return page, nil
}

func parseWeexKlineRow(row []json.RawMessage, symbol, timeframe string) (domain.Kline, int64, error) {
	if len(row) < 6 {
		return domain.Kline{}, 0, fmt.Errorf("fila de kline con %d campos, want ≥6", len(row))
	}
	var f weexKlineFields
	if err := json.Unmarshal(row[0], &f.Start); err != nil {
		if s, err2 := unmarshalString(row[0]); err2 == nil {
			if v, err3 := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err3 == nil {
				f.Start = v
			} else {
				return domain.Kline{}, 0, fmt.Errorf("openTime inválido %q", s)
			}
		} else {
			return domain.Kline{}, 0, fmt.Errorf("openTime inválido: %v", err)
		}
	}
	priceFields := []*flexNumber{&f.Open, &f.High, &f.Low, &f.Close, &f.Volume}
	for i, dst := range priceFields {
		if err := json.Unmarshal(row[1+i], dst); err != nil {
			return domain.Kline{}, f.Start, fmt.Errorf("precio %d inválido: %w", i+1, err)
		}
	}
	if !f.hasPrice() {
		return domain.Kline{}, f.Start, fmt.Errorf("kline incompleta (ts=%d)", f.Start)
	}
	k := buildKline(f, symbol, timeframe, true)
	if err := validateKline(k); err != nil {
		return k, f.Start, fmt.Errorf("kline inválida: %w", err)
	}
	return k, f.Start, nil
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
