package ingest

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// Tipos de evento de ingestión que se persisten para auditoría (/weex*).
const (
	EventWSConnected      = "ws_conectado"
	EventWSDisconnected   = "ws_desconectado"
	EventGapDetected      = "hueco_detectado"
	EventBackfillOK       = "backfill_ok"
	EventBackfillPartial  = "backfill_parcial"
	EventBackfillFailed   = "backfill_fallido"
	EventGapUnrecoverable = "hueco_irrecuperable"
	EventRESTError        = "error_rest"
)

// Severidades homogéneas con los informes de Telegram.
const (
	SeverityInfo = "INFO"
	SeverityWarn = "WARN"
	SeverityFail = "FAIL"
)

// Estados de un intervalo pendiente.
const (
	GapPending       = "pendiente"
	GapUnrecoverable = "irrecuperable"
)

// IngestEvent es el registro persistible de un suceso del proveedor. Key es
// estable: un reintento del mismo suceso no crea una fila nueva.
type IngestEvent struct {
	Key        string
	Provider   string
	Symbol     string
	Timeframe  string
	Kind       string
	Severity   string
	Cursor     time.Time
	From       time.Time
	To         time.Time
	Expected   int
	Received   int
	Discarded  int
	HTTPStatus int
	Code       int
	Message    string
	Detail     map[string]any
	At         time.Time
}

// IngestEventSink persiste eventos de ingestión (implementado por store.Store).
type IngestEventSink interface {
	RecordIngestEvent(ctx context.Context, ev IngestEvent) error
}

// GapRange es un intervalo de velas cerradas que falta entregar. From y To son
// los inicios de la primera y la última vela ausentes (inclusivos).
type GapRange struct {
	From       time.Time
	To         time.Time
	Bars       int
	DetectedAt time.Time
	Attempts   int
	NextRetry  time.Time
	LastError  string
	Status     string
	Reason     string
}

// BackfillRecord resume una recuperación (inicial o de hueco).
type BackfillRecord struct {
	At             time.Time
	Trigger        string
	Symbol         string
	Timeframe      string
	CursorBefore   time.Time
	CursorAfter    time.Time
	From           time.Time
	To             time.Time
	Expected       int
	Received       int
	Emitted        int
	Late           int
	Discarded      int
	DiscardReasons map[string]int
	Duration       time.Duration
	Outcome        string
	Error          string
	Request        string
}

// ProviderError es un error reciente del proveedor, ya saneado.
type ProviderError struct {
	At         time.Time
	Component  string
	Symbol     string
	Timeframe  string
	Kind       string
	HTTPStatus int
	Code       int
	Message    string
	Severity   string
}

// FeedCounters son contadores acumulados de un feed desde el arranque.
type FeedCounters struct {
	Messages         int64
	Pings            int64
	Pongs            int64
	OpenBars         int64
	ClosedWS         int64
	ClosedREST       int64
	Duplicates       int64
	StaleOpen        int64
	Misaligned       int64
	Recoveries       int64
	RecoveryFailures int64
	BarsRecovered    int64
	BarsDiscarded    int64
	PersistErrors    int64
}

// FeedHealth es la foto de un feed symbol/timeframe para /weexstatus y /ibhealth.
type FeedHealth struct {
	Provider         string
	Symbol           string
	Timeframe        string
	Connected        bool
	ConnectedAt      time.Time
	LastMessageAt    time.Time
	LastOpen         time.Time
	LastClosed       time.Time
	LastClosedAt     time.Time
	LastClosedSource string
	LastDisconnectAt time.Time
	LastDisconnect   string
	Reconnects       int
	Degraded         bool
	DegradedReason   string
	Pending          []GapRange
	Holes            []GapRange
	Backfills        []BackfillRecord
	Counters         FeedCounters
}

const (
	maxHealthRing   = 20
	maxProviderErrs = 50
)

// feedState es el estado mutable de un feed. Solo lo modifican el goroutine
// del stream y Backfill (que corre antes de Subscribe); los lectores usan Snapshot.
type feedState struct {
	mu        sync.Mutex
	symbol    string
	timeframe string
	step      time.Duration

	connected        bool
	connectedAt      time.Time
	lastMessageAt    time.Time
	firstOpen        time.Time
	lastOpen         time.Time
	lastClosed       time.Time
	lastClosedAt     time.Time
	lastClosedSource string
	lastDisconnectAt time.Time
	lastDisconnect   string
	reconnects       int

	tail      *GapRange
	holes     []GapRange
	backfills []BackfillRecord
	counters  FeedCounters
}

func newFeedState(symbol, timeframe string) *feedState {
	return &feedState{symbol: symbol, timeframe: timeframe, step: parseInterval(timeframe)}
}

// floor devuelve el inicio de la última vela cerrada confirmada (cursor). Si
// aún no hay ninguna, usa la vela anterior a la primera vela en formación vista
// en este stream, de modo que su versión cerrada se recupere por REST.
func (s *feedState) floor() time.Time {
	if !s.lastClosed.IsZero() {
		return s.lastClosed
	}
	if !s.firstOpen.IsZero() {
		return s.firstOpen.Add(-s.step)
	}
	return time.Time{}
}

func (s *feedState) markClosed(ts time.Time, source string, at time.Time) {
	if ts.After(s.lastClosed) {
		s.lastClosed = ts
		s.lastClosedAt = at
		s.lastClosedSource = source
	}
	if source == "ws" {
		s.counters.ClosedWS++
	} else {
		s.counters.ClosedREST++
	}
}

func (s *feedState) addBackfill(r BackfillRecord) {
	s.backfills = append(s.backfills, r)
	if len(s.backfills) > maxHealthRing {
		s.backfills = s.backfills[len(s.backfills)-maxHealthRing:]
	}
}

func (s *feedState) addHole(g GapRange) {
	for i := range s.holes {
		if s.holes[i].From.Equal(g.From) && s.holes[i].To.Equal(g.To) {
			s.holes[i] = g
			return
		}
	}
	s.holes = append(s.holes, g)
	sort.Slice(s.holes, func(i, j int) bool { return s.holes[i].From.Before(s.holes[j].From) })
	if len(s.holes) > maxHealthRing {
		s.holes = s.holes[len(s.holes)-maxHealthRing:]
	}
}

func (s *feedState) snapshot(provider string) FeedHealth {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := FeedHealth{
		Provider:         provider,
		Symbol:           s.symbol,
		Timeframe:        s.timeframe,
		Connected:        s.connected,
		ConnectedAt:      s.connectedAt,
		LastMessageAt:    s.lastMessageAt,
		LastOpen:         s.lastOpen,
		LastClosed:       s.lastClosed,
		LastClosedAt:     s.lastClosedAt,
		LastClosedSource: s.lastClosedSource,
		LastDisconnectAt: s.lastDisconnectAt,
		LastDisconnect:   s.lastDisconnect,
		Reconnects:       s.reconnects,
		Holes:            append([]GapRange(nil), s.holes...),
		Backfills:        append([]BackfillRecord(nil), s.backfills...),
		Counters:         s.counters,
	}
	if s.tail != nil {
		h.Pending = append(h.Pending, *s.tail)
	}
	for _, g := range s.holes {
		if g.Status == GapPending {
			h.Pending = append(h.Pending, g)
		}
	}
	var reasons []string
	if s.tail != nil && s.tail.Attempts > 0 {
		reasons = append(reasons, fmt.Sprintf("%d velas pendientes desde %s (%d intentos)",
			s.tail.Bars, s.tail.From.UTC().Format("2006-01-02 15:04"), s.tail.Attempts))
	}
	for _, g := range s.holes {
		switch g.Status {
		case GapPending:
			reasons = append(reasons, fmt.Sprintf("hueco pendiente %s→%s", g.From.UTC().Format("01-02 15:04"), g.To.UTC().Format("01-02 15:04")))
		case GapUnrecoverable:
			reasons = append(reasons, fmt.Sprintf("hueco irrecuperable %s→%s", g.From.UTC().Format("01-02 15:04"), g.To.UTC().Format("01-02 15:04")))
		}
	}
	if len(reasons) > 0 {
		h.Degraded = true
		h.DegradedReason = strings.Join(reasons, "; ")
	}
	return h
}

// feed devuelve (creándolo si hace falta) el estado de un feed.
func (p *WeexProvider) feed(symbol, timeframe string) *feedState {
	key := mapKey(strings.ToUpper(symbol), timeframe)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.feeds == nil {
		p.feeds = make(map[string]*feedState)
	}
	st, ok := p.feeds[key]
	if !ok {
		st = newFeedState(strings.ToUpper(symbol), timeframe)
		p.feeds[key] = st
	}
	return st
}

// SetEventSink habilita la persistencia de eventos de ingestión.
func (p *WeexProvider) SetEventSink(sink IngestEventSink) {
	p.mu.Lock()
	p.sink = sink
	p.mu.Unlock()
}

// Health devuelve el estado de todos los feeds, ordenados por symbol/timeframe.
func (p *WeexProvider) Health() []FeedHealth {
	p.mu.Lock()
	states := make([]*feedState, 0, len(p.feeds))
	for _, st := range p.feeds {
		states = append(states, st)
	}
	p.mu.Unlock()
	out := make([]FeedHealth, 0, len(states))
	for _, st := range states {
		out = append(out, st.snapshot("weex"))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Symbol != out[j].Symbol {
			return out[i].Symbol < out[j].Symbol
		}
		return parseInterval(out[i].Timeframe) > parseInterval(out[j].Timeframe)
	})
	return out
}

// RecentErrors devuelve los últimos errores del proveedor (más reciente primero).
func (p *WeexProvider) RecentErrors(limit int) []ProviderError {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	n := len(p.errs)
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]ProviderError, 0, limit)
	for i := n - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, p.errs[i])
	}
	return out
}

func (p *WeexProvider) recordError(e ProviderError) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	e.Message = sanitizeMessage(e.Message)
	p.errMu.Lock()
	p.errs = append(p.errs, e)
	if len(p.errs) > maxProviderErrs {
		p.errs = p.errs[len(p.errs)-maxProviderErrs:]
	}
	p.errMu.Unlock()
}

// emitEvent persiste un evento si hay sink. Un fallo de persistencia se
// registra como error observable; nunca se simula que el evento quedó guardado.
func (p *WeexProvider) emitEvent(ctx context.Context, st *feedState, ev IngestEvent) {
	p.mu.Lock()
	sink := p.sink
	p.mu.Unlock()
	if sink == nil {
		return
	}
	if ev.Provider == "" {
		ev.Provider = "weex"
	}
	if ev.At.IsZero() {
		ev.At = p.now()
	}
	ev.Message = sanitizeMessage(ev.Message)
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := sink.RecordIngestEvent(pctx, ev); err != nil {
		log.Printf("weex: no se pudo persistir el evento %s %s %s: %v", ev.Kind, ev.Symbol, ev.Timeframe, err)
		if st != nil {
			st.mu.Lock()
			st.counters.PersistErrors++
			st.mu.Unlock()
		}
		p.recordError(ProviderError{Component: "persistencia", Symbol: ev.Symbol, Timeframe: ev.Timeframe,
			Kind: "persistencia", Message: err.Error(), Severity: SeverityFail})
	}
}

func eventKey(parts ...any) string {
	var b strings.Builder
	for i, part := range parts {
		if i > 0 {
			b.WriteByte('|')
		}
		switch v := part.(type) {
		case time.Time:
			fmt.Fprintf(&b, "%d", v.UnixMilli())
		default:
			fmt.Fprintf(&b, "%v", v)
		}
	}
	return b.String()
}
