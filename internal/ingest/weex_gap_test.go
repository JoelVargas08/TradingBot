package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tradingview-bot/internal/domain"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// Reloj inyectable y servidor WEEX que aplica el contrato verificado.
// ---------------------------------------------------------------------------

type fakeClock struct{ ms atomic.Int64 }

func newFakeClock(t time.Time) *fakeClock {
	c := &fakeClock{}
	c.ms.Store(t.UnixMilli())
	return c
}
func (c *fakeClock) Now() time.Time                  { return time.UnixMilli(c.ms.Load()) }
func (c *fakeClock) Set(t time.Time)                 { c.ms.Store(t.UnixMilli()) }
func (c *fakeClock) Add(d time.Duration)             { c.ms.Add(d.Milliseconds()) }
func (c *fakeClock) After(d time.Duration) time.Time { return c.Now().Add(d) }

type histFailure struct {
	status     int
	body       string
	retryAfter string
	delay      time.Duration
}

type histRequest struct {
	start, end int64
	limit      string
	size       string
	interval   string
}

// weexContractServer imita /capi/v3/market/historyKlines con las reglas
// observadas en producción el 2026-11-15 (ver weex_contract.go).
type weexContractServer struct {
	clock        *fakeClock
	serverOffset time.Duration // reloj del servidor = reloj local + offset
	genesis      time.Time

	mu         sync.Mutex
	omit       map[int64]bool
	shuffle    bool
	duplicate  bool
	empty      bool
	failures   []histFailure
	requests   []histRequest
	violations []string
}

func newContractServer(clock *fakeClock, genesis time.Time) *weexContractServer {
	return &weexContractServer{clock: clock, genesis: genesis, omit: map[int64]bool{}}
}

func (s *weexContractServer) violation(format string, args ...any) {
	s.violations = append(s.violations, fmt.Sprintf(format, args...))
}

func (s *weexContractServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	interval := q.Get("interval")
	step := parseInterval(interval)
	req := histRequest{interval: interval, limit: q.Get("limit"), size: q.Get("size")}
	req.start, _ = strconv.ParseInt(q.Get("startTime"), 10, 64)
	req.end, _ = strconv.ParseInt(q.Get("endTime"), 10, 64)

	s.mu.Lock()
	s.requests = append(s.requests, req)
	if req.size != "" {
		s.violation("se envió `size`=%s, parámetro no documentado", req.size)
	}
	var fail *histFailure
	if len(s.failures) > 0 {
		f := s.failures[0]
		s.failures = s.failures[1:]
		fail = &f
	}
	serverNow := s.clock.Now().Add(s.serverOffset).UnixMilli()
	limit := 100
	if req.limit != "" {
		n, err := strconv.Atoi(req.limit)
		if err != nil || n < 1 || n > 100 {
			s.violation("limit inválido %q", req.limit)
			s.mu.Unlock()
			writeBiz(w, -1142, "Parameter 'limit' is invalid.")
			return
		}
		limit = n
	}
	switch {
	case req.start > serverNow:
		s.violation("startTime futuro %d > %d", req.start, serverNow)
		s.mu.Unlock()
		writeBiz(w, -1142, "Parameter 'startTime' is invalid.")
		return
	case req.end > serverNow:
		s.violation("endTime futuro %d > %d", req.end, serverNow)
		s.mu.Unlock()
		writeBiz(w, -1142, "Parameter 'endTime' is invalid.")
		return
	case req.start > req.end:
		s.violation("startTime %d > endTime %d", req.start, req.end)
		s.mu.Unlock()
		writeBiz(w, -1171, "startTime cannot be greater than endTime.")
		return
	case time.Duration(req.end-req.start)*time.Millisecond > HistoryMaxRange:
		s.violation("rango > 90 días")
		s.mu.Unlock()
		writeBiz(w, -1170, "Time range cannot exceed 90 days.")
		return
	case req.start%step.Milliseconds() != 0:
		// WEEX lo acepta, pero el diseño exige límites de vela exactos.
		s.violation("startTime %d no alineado a %s", req.start, interval)
	}
	omit, shuffle, dup, empty := s.omit, s.shuffle, s.duplicate, s.empty
	s.mu.Unlock()

	if fail != nil {
		if fail.delay > 0 {
			time.Sleep(fail.delay)
		}
		if fail.retryAfter != "" {
			w.Header().Set("Retry-After", fail.retryAfter)
		}
		w.WriteHeader(fail.status)
		w.Write([]byte(fail.body))
		return
	}
	rows := []string{}
	if !empty {
		first := alignDown(time.UnixMilli(req.start), step)
		if first.UnixMilli() < req.start {
			first = first.Add(step)
		}
		var starts []int64
		for ts := first; !ts.After(time.UnixMilli(req.end)); ts = ts.Add(step) {
			if ts.Before(s.genesis) || omit[ts.UnixMilli()] {
				continue
			}
			if ts.Add(step).UnixMilli() > serverNow {
				continue // nunca se devuelve la vela en formación
			}
			starts = append(starts, ts.UnixMilli())
		}
		// Descendente y con tope `limit`, como WEEX.
		sort.Slice(starts, func(i, j int) bool { return starts[i] > starts[j] })
		if len(starts) > limit {
			starts = starts[:limit]
		}
		for _, ts := range starts {
			rows = append(rows, gapRow(ts))
		}
		if dup && len(starts) > 0 {
			rows = append(rows, gapRow(starts[0]))
		}
		if shuffle {
			rnd := rand.New(rand.NewSource(7))
			rnd.Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
		}
	}
	w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
}

func writeBiz(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprintf(w, `{"code":%d,"msg":%q}`, code, msg)
}

// gapRow genera una fila determinista y OHLC coherente a partir del inicio.
func gapRow(ts int64) string {
	base := 50000 + float64((ts/60000)%997)
	return fmt.Sprintf(`[%d,"%.1f","%.1f","%.1f","%.1f","12.5","0","0","0","0","0"]`,
		ts, base, base+50, base-50, base+10)
}

func (s *weexContractServer) snapshot() ([]histRequest, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]histRequest(nil), s.requests...), append([]string(nil), s.violations...)
}

func (s *weexContractServer) assertNoViolations(t *testing.T) {
	t.Helper()
	if _, v := s.snapshot(); len(v) > 0 {
		t.Fatalf("peticiones fuera de contrato: %v", v)
	}
}

type recordingSleep struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (r *recordingSleep) sleep(ctx context.Context, d time.Duration) bool {
	r.mu.Lock()
	r.waits = append(r.waits, d)
	r.mu.Unlock()
	return ctx.Err() == nil
}

func (r *recordingSleep) all() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.waits...)
}

type memSink struct {
	mu     sync.Mutex
	events map[string]IngestEvent
	order  []string
	fail   error
}

func (m *memSink) RecordIngestEvent(ctx context.Context, ev IngestEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	if m.events == nil {
		m.events = map[string]IngestEvent{}
	}
	if _, ok := m.events[ev.Key]; ok {
		return nil // misma semántica que INSERT OR IGNORE por clave estable
	}
	m.events[ev.Key] = ev
	m.order = append(m.order, ev.Kind)
	return nil
}

func (m *memSink) kinds() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.order...)
}

func newGapProvider(t *testing.T, restURL string, clock *fakeClock) (*WeexProvider, *recordingSleep) {
	t.Helper()
	p := NewWEEXProvider(&fakeCandleStore{dup: map[string]bool{}}, WeexConfig{
		RestURL:          restURL,
		HistoryPageDelay: time.Nanosecond,
		MaxAttempts:      3,
		BackfillBars:     300,
		MaxGapRetries:    3,
	})
	p.now = clock.Now
	rs := &recordingSleep{}
	p.sleep = rs.sleep
	return p, rs
}

func drain(ch chan domain.Kline) []domain.Kline {
	var out []domain.Kline
	for {
		select {
		case k := <-ch:
			out = append(out, k)
		default:
			return out
		}
	}
}

func closedStarts(ks []domain.Kline) []int64 {
	var out []int64
	for _, k := range ks {
		if k.Closed {
			out = append(out, k.Start.UnixMilli())
		}
	}
	return out
}

func expectStarts(from time.Time, n int, step time.Duration) []int64 {
	out := make([]int64, n)
	for i := 0; i < n; i++ {
		out[i] = from.Add(time.Duration(i) * step).UnixMilli()
	}
	return out
}

func equalInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// t0 es un instante alineado a 1h (y por tanto a 5m/15m).
var t0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// 1. Reproducción del fallo de PAPER #57 y contrato temporal.
// ---------------------------------------------------------------------------

// TestOldBackfillGapRangeReproducesStartTimeInvalid reproduce la petición que
// construía el código antiguo (start = lastTS+step con lastTS = inicio de la
// vela EN FORMACIÓN; end = now+step) contra un servidor con el contrato real:
// responde exactamente el error observado en la sesión PAPER #57.
func TestOldBackfillGapRangeReproducesStartTimeInvalid(t *testing.T) {
	for _, tf := range []string{"5m", "15m", "1h"} {
		t.Run(tf, func(t *testing.T) {
			step := parseInterval(tf)
			now := t0.Add(2*step + 40*time.Second) // vela en formación: t0+2step
			clock := newFakeClock(now)
			srv := newContractServer(clock, t0.Add(-1000*step))
			hs := httptest.NewServer(srv)
			defer hs.Close()
			p, _ := newGapProvider(t, hs.URL, clock)

			lastTS := alignDown(now, step) // cursor antiguo: vela abierta
			oldStart, oldEnd := lastTS.Add(step), now.Add(step)
			if err := validateHistoryWindow(oldStart, oldEnd, now); !errors.Is(err, ErrHistoryContract) {
				t.Fatalf("el rango antiguo debería violar el contrato: %v", err)
			}
			// Enviada sin la guarda, WEEX responde -1142 startTime.
			_, err := p.doFetchHistory(context.Background(), "BTCUSDT", tf, oldStart, oldEnd)
			var he *HistoryError
			if !errors.As(err, &he) || he.Code != -1142 || !strings.Contains(he.Message, "startTime") || he.Retriable {
				t.Fatalf("esperado 400 -1142 startTime no reintentable, got %v", err)
			}
			if he.Kind != HistoryErrAPI || he.HTTPStatus != 400 {
				t.Fatalf("clasificación = %+v, want negocio/400", he)
			}
			// Con la vela anterior ya cerrada (cursor = vela cerrada), el end
			// antiguo seguía siendo futuro: -1142 endTime.
			_, err = p.doFetchHistory(context.Background(), "BTCUSDT", tf, lastTS.Add(-step), oldEnd)
			if !errors.As(err, &he) || he.Code != -1142 || !strings.Contains(he.Message, "endTime") {
				t.Fatalf("esperado -1142 endTime, got %v", err)
			}
		})
	}
}

func TestValidateHistoryWindowContract(t *testing.T) {
	now := t0.Add(30 * time.Minute)
	cases := []struct {
		name       string
		start, end time.Time
		ok         bool
	}{
		{"pasado válido", t0.Add(-time.Hour), t0, true},
		{"start == end", t0, t0, true},
		{"end == now", t0, now, true},
		{"end futuro", t0, now.Add(time.Millisecond), false},
		{"start futuro", now.Add(time.Second), now.Add(time.Hour), false},
		{"start > end", t0, t0.Add(-time.Minute), false},
		{"rango > 90d", now.Add(-91 * 24 * time.Hour), now, false},
		{"cero", time.Time{}, now, false},
	}
	for _, c := range cases {
		err := validateHistoryWindow(c.start, c.end, now)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v ok=%v", c.name, err, c.ok)
		}
	}
}

func TestHistoryURLUsesDocumentedLimitAndMillis(t *testing.T) {
	p := NewWEEXProvider(nil, WeexConfig{RestURL: "https://example.invalid/"})
	u := p.historyURL("BTCUSDT", "15m", t0, t0.Add(15*time.Minute-time.Millisecond))
	for _, want := range []string{"limit=100", "startTime=" + strconv.FormatInt(t0.UnixMilli(), 10), "interval=15m", "symbol=BTCUSDT"} {
		if !strings.Contains(u, want) {
			t.Errorf("url %q no contiene %q", u, want)
		}
	}
	if strings.Contains(u, "size=") {
		t.Errorf("url %q usa `size`, parámetro no documentado", u)
	}
}

func TestGetKlinesClampsFutureEndAndSkipsEmptyFutureRange(t *testing.T) {
	clock := newFakeClock(t0.Add(10*time.Minute + 2*time.Second))
	srv := newContractServer(clock, t0.Add(-100*time.Hour))
	hs := httptest.NewServer(srv)
	defer hs.Close()
	p, _ := newGapProvider(t, hs.URL, clock)

	bars, err := p.GetKlines(context.Background(), "BTCUSDT", "5m", t0.Add(-15*time.Minute), clock.After(time.Hour), 100)
	if err != nil {
		t.Fatalf("GetKlines: %v", err)
	}
	// Cerradas: t0-15m .. t0+5m (esta cierra en t0+10m <= now). t0+10m sigue abierta.
	if got := closedStarts(bars); !equalInts(got, expectStarts(t0.Add(-15*time.Minute), 5, 5*time.Minute)) {
		t.Fatalf("velas = %v", got)
	}
	// Rango completamente futuro: no se envía ninguna petición.
	before, _ := srv.snapshot()
	bars, err = p.GetKlines(context.Background(), "BTCUSDT", "5m", clock.After(time.Minute), clock.After(time.Hour), 10)
	if err != nil || len(bars) != 0 {
		t.Fatalf("rango futuro: bars=%d err=%v", len(bars), err)
	}
	after, _ := srv.snapshot()
	if len(after) != len(before) {
		t.Fatalf("se envió una petición con rango futuro")
	}
	srv.assertNoViolations(t)
}

// ---------------------------------------------------------------------------
// 2. Recuperación de huecos (reconcile) con cursor en vela cerrada/abierta.
// ---------------------------------------------------------------------------

func seedFeed(p *WeexProvider, symbol, tf string, lastClosed, lastOpen time.Time) *feedState {
	st := p.feed(symbol, tf)
	st.mu.Lock()
	st.lastClosed = lastClosed
	st.lastOpen = lastOpen
	if !lastOpen.IsZero() {
		st.firstOpen = lastOpen
	}
	st.mu.Unlock()
	return st
}

func TestReconcileRecoversGapsOfOneAndSeveralBars(t *testing.T) {
	for _, tf := range []string{"5m", "15m", "1h"} {
		for _, missing := range []int{1, 2, 7} {
			t.Run(fmt.Sprintf("%s/%d", tf, missing), func(t *testing.T) {
				step := parseInterval(tf)
				cursor := t0
				now := cursor.Add(time.Duration(missing+1)*step + 20*time.Second)
				clock := newFakeClock(now)
				srv := newContractServer(clock, t0.Add(-500*step))
				hs := httptest.NewServer(srv)
				defer hs.Close()
				p, _ := newGapProvider(t, hs.URL, clock)
				st := seedFeed(p, "BTCUSDT", tf, cursor, time.Time{})
				ch := make(chan domain.Kline, 64)

				p.reconcile(context.Background(), st, ch, time.Time{}, "reconexion")

				got := closedStarts(drain(ch))
				want := expectStarts(cursor.Add(step), missing, step)
				if !equalInts(got, want) {
					t.Fatalf("recuperadas %v, want %v", got, want)
				}
				h := p.Health()[0]
				if !h.LastClosed.Equal(cursor.Add(time.Duration(missing) * step)) {
					t.Fatalf("cursor = %v", h.LastClosed)
				}
				if len(h.Pending) != 0 || h.Degraded {
					t.Fatalf("no debería quedar nada pendiente: %+v", h.Pending)
				}
				// Repetir no duplica velas.
				p.reconcile(context.Background(), st, ch, time.Time{}, "reconexion")
				if extra := drain(ch); len(extra) != 0 {
					t.Fatalf("reconcile repetido duplicó %d velas", len(extra))
				}
				srv.assertNoViolations(t)
			})
		}
	}
}

// Cursor en el inicio de una vela ABIERTA (sin vela cerrada confirmada): la
// versión cerrada de esa vela se recupera en lugar de saltársela.
func TestReconcileFromOpenCandleCursorDoesNotSkipIt(t *testing.T) {
	step := 15 * time.Minute
	open := t0
	clock := newFakeClock(open.Add(3*step + 10*time.Second))
	srv := newContractServer(clock, t0.Add(-100*step))
	hs := httptest.NewServer(srv)
	defer hs.Close()
	p, _ := newGapProvider(t, hs.URL, clock)
	st := seedFeed(p, "BTCUSDT", "15m", time.Time{}, open)
	ch := make(chan domain.Kline, 16)

	p.reconcile(context.Background(), st, ch, time.Time{}, "reconexion")

	if got, want := closedStarts(drain(ch)), expectStarts(open, 3, step); !equalInts(got, want) {
		t.Fatalf("recuperadas %v, want %v (incluida la vela que estaba abierta)", got, want)
	}
	srv.assertNoViolations(t)
}

// Reloj justo después del cambio de vela: la vela recién cerrada aún no es
// pedible (margen de skew); no se envía nada futuro y se recupera después.
func TestReconcileNearIntervalBoundaryWaitsForSkew(t *testing.T) {
	step := 5 * time.Minute
	cursor := t0
	boundary := cursor.Add(2 * step) // la vela cursor+step acaba de cerrar
	clock := newFakeClock(boundary.Add(2 * time.Second))
	srv := newContractServer(clock, t0.Add(-100*step))
	srv.serverOffset = -3 * time.Second // servidor 3s por detrás del reloj local
	hs := httptest.NewServer(srv)
	defer hs.Close()
	p, _ := newGapProvider(t, hs.URL, clock)
	st := seedFeed(p, "BTCUSDT", "5m", cursor, cursor.Add(step))
	ch := make(chan domain.Kline, 16)

	p.reconcile(context.Background(), st, ch, boundary.Add(-step), "cambio_de_vela")
	if reqs, _ := srv.snapshot(); len(reqs) != 0 {
		t.Fatalf("no debería pedirse aún la vela recién cerrada: %d peticiones", len(reqs))
	}
	if got := drain(ch); len(got) != 0 {
		t.Fatalf("no debería entregarse nada aún: %v", got)
	}
	st.mu.Lock()
	due := st.retryDue(clock.Now())
	next := st.tail.NextRetry
	st.mu.Unlock()
	if due || !next.Equal(boundary.Add(HistoryClockSkew)) {
		t.Fatalf("reintento = %v (due=%v), want %v", next, due, boundary.Add(HistoryClockSkew))
	}

	clock.Set(boundary.Add(6 * time.Second))
	p.reconcile(context.Background(), st, ch, boundary.Add(-step), "reintento")
	if got, want := closedStarts(drain(ch)), expectStarts(cursor.Add(step), 1, step); !equalInts(got, want) {
		t.Fatalf("recuperadas %v, want %v", got, want)
	}
	srv.assertNoViolations(t)
}

func TestReconcileSortsAndDeduplicatesUnorderedHistory(t *testing.T) {
	step := time.Hour
	cursor := t0
	clock := newFakeClock(cursor.Add(6*step + time.Minute))
	srv := newContractServer(clock, t0.Add(-100*step))
	srv.shuffle, srv.duplicate = true, true
	hs := httptest.NewServer(srv)
	defer hs.Close()
	p, _ := newGapProvider(t, hs.URL, clock)
	st := seedFeed(p, "BTCUSDT", "1h", cursor, time.Time{})
	ch := make(chan domain.Kline, 16)

	p.reconcile(context.Background(), st, ch, time.Time{}, "reconexion")
	if got, want := closedStarts(drain(ch)), expectStarts(cursor.Add(step), 5, step); !equalInts(got, want) {
		t.Fatalf("entregadas %v, want %v (ordenadas y sin duplicados)", got, want)
	}
}

func TestReconcileEmptyHistoryKeepsGapPendingAndExplicit(t *testing.T) {
	step := 15 * time.Minute
	cursor := t0
	clock := newFakeClock(cursor.Add(4*step + time.Minute))
	srv := newContractServer(clock, t0.Add(-100*step))
	srv.empty = true
	hs := httptest.NewServer(srv)
	defer hs.Close()
	p, _ := newGapProvider(t, hs.URL, clock)
	sink := &memSink{}
	p.SetEventSink(sink)
	st := seedFeed(p, "BTCUSDT", "15m", cursor, time.Time{})
	ch := make(chan domain.Kline, 16)

	p.reconcile(context.Background(), st, ch, time.Time{}, "reconexion")
	if got := drain(ch); len(got) != 0 {
		t.Fatalf("respuesta vacía no debe entregar velas: %v", got)
	}
	h := p.Health()[0]
	if !h.LastClosed.Equal(cursor) {
		t.Fatalf("el cursor no debe avanzar sin datos: %v", h.LastClosed)
	}
	if !h.Degraded || len(h.Pending) != 1 || h.Pending[0].Bars != 3 || h.Pending[0].Attempts != 1 {
		t.Fatalf("hueco pendiente no explícito: degraded=%v pending=%+v", h.Degraded, h.Pending)
	}
	// Antes de NextRetry no se vuelve a pedir (sin bucles).
	reqs, _ := srv.snapshot()
	p.reconcile(context.Background(), st, ch, time.Time{}, "reintento")
	if again, _ := srv.snapshot(); len(again) != len(reqs) {
		t.Fatalf("reintento antes de tiempo: %d → %d peticiones", len(reqs), len(again))
	}
	// Tras MaxGapRetries respuestas vacías el hueco se declara irrecuperable.
	for i := 0; i < 5; i++ {
		clock.Add(2 * time.Minute)
		p.reconcile(context.Background(), st, ch, cursor.Add(3*step), "reintento")
	}
	h = p.Health()[0]
	unrec := 0
	for _, g := range h.Holes {
		if g.Status == GapUnrecoverable {
			unrec++
		}
	}
	if unrec != 1 || !h.Degraded {
		t.Fatalf("hueco irrecuperable no registrado: %+v", h.Holes)
	}
	kinds := strings.Join(sink.kinds(), ",")
	for _, k := range []string{EventGapDetected, EventBackfillPartial, EventGapUnrecoverable} {
		if !strings.Contains(kinds, k) {
			t.Errorf("evento %s no persistido (eventos: %s)", k, kinds)
		}
	}
	srv.assertNoViolations(t)
}

func TestReconcileInternalHoleIsRetriedAndRecoveredLate(t *testing.T) {
	step := 5 * time.Minute
	cursor := t0
	clock := newFakeClock(cursor.Add(5*step + time.Minute))
	srv := newContractServer(clock, t0.Add(-100*step))
	srv.omit[cursor.Add(2*step).UnixMilli()] = true
	hs := httptest.NewServer(srv)
	defer hs.Close()
	p, _ := newGapProvider(t, hs.URL, clock)
	st := seedFeed(p, "BTCUSDT", "5m", cursor, time.Time{})
	ch := make(chan domain.Kline, 16)

	p.reconcile(context.Background(), st, ch, time.Time{}, "reconexion")
	got := closedStarts(drain(ch))
	want := []int64{cursor.Add(step).UnixMilli(), cursor.Add(3 * step).UnixMilli(), cursor.Add(4 * step).UnixMilli()}
	if !equalInts(got, want) {
		t.Fatalf("entregadas %v, want %v", got, want)
	}
	h := p.Health()[0]
	if len(h.Holes) != 1 || !h.Holes[0].From.Equal(cursor.Add(2*step)) || h.Holes[0].Status != GapPending {
		t.Fatalf("hueco interno no registrado: %+v", h.Holes)
	}
	// WEEX ya dispone de la vela: el reintento la entrega (tardía) y cierra el hueco.
	srv.mu.Lock()
	srv.omit = map[int64]bool{}
	srv.mu.Unlock()
	clock.Add(time.Minute)
	p.reconcile(context.Background(), st, ch, time.Time{}, "reintento")
	late := drain(ch)
	if got := closedStarts(late); !equalInts(got, []int64{cursor.Add(2 * step).UnixMilli()}) {
		t.Fatalf("reintento del hueco entregó %v", got)
	}
	h = p.Health()[0]
	if len(h.Holes) != 0 || h.Degraded {
		t.Fatalf("el hueco debería cerrarse: %+v", h.Holes)
	}
	if !h.LastClosed.Equal(cursor.Add(4 * step)) {
		t.Fatalf("una vela tardía no debe mover el cursor hacia atrás: %v", h.LastClosed)
	}
}

func TestReconcileNeverViolatesContractRandomized(t *testing.T) {
	rnd := rand.New(rand.NewSource(42))
	for i := 0; i < 120; i++ {
		tf := []string{"5m", "15m", "1h"}[i%3]
		step := parseInterval(tf)
		cursor := t0.Add(time.Duration(rnd.Intn(50)) * step)
		// now arbitrario, incluidos instantes a pocos ms/segundos del cambio de vela.
		jitter := []time.Duration{0, time.Millisecond, time.Second, 4 * time.Second, 5 * time.Second, 6 * time.Second, step / 2, step - time.Second}[rnd.Intn(8)]
		now := cursor.Add(time.Duration(rnd.Intn(30))*step + jitter)
		clock := newFakeClock(now)
		srv := newContractServer(clock, t0.Add(-1000*step))
		srv.serverOffset = -time.Duration(rnd.Intn(4)) * time.Second
		hs := httptest.NewServer(srv)
		p, _ := newGapProvider(t, hs.URL, clock)
		st := seedFeed(p, "BTCUSDT", tf, cursor, time.Time{})
		ch := make(chan domain.Kline, 256)
		upTo := time.Time{}
		if rnd.Intn(2) == 0 {
			upTo = alignDown(now, step).Add(-step)
		}
		p.reconcile(context.Background(), st, ch, upTo, "reconexion")
		got := closedStarts(drain(ch))
		hs.Close()
		if _, v := srv.snapshot(); len(v) > 0 {
			t.Fatalf("caso %d (%s cursor=%s now=%s): violaciones %v", i, tf, cursor, now, v)
		}
		// Continuidad: lo entregado es contiguo desde cursor+step, sin duplicados.
		for j, ts := range got {
			if want := cursor.Add(time.Duration(j+1) * step).UnixMilli(); ts != want {
				t.Fatalf("caso %d: vela %d = %d, want %d (salto o duplicado)", i, j, ts, want)
			}
		}
		// Todo lo que ya era seguro pedir se entregó.
		safe := lastClosedStart(now, step, HistoryClockSkew)
		lim := safe
		if !upTo.IsZero() && upTo.Before(lim) {
			lim = upTo
		}
		if want := barsBetween(cursor.Add(step), lim, step); len(got) != want {
			t.Fatalf("caso %d: entregadas %d, want %d", i, len(got), want)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. Errores HTTP: 400, 429 + Retry-After, 5xx y timeout.
// ---------------------------------------------------------------------------

func TestHistoryErrorHandling(t *testing.T) {
	step := time.Hour
	cursor := t0
	type tc struct {
		name         string
		failures     []histFailure
		client       *http.Client
		wantRequests int
		wantBars     int
		wantKind     string
		wantWaits    []time.Duration
	}
	cases := []tc{
		{name: "400 negocio no se reintenta",
			failures:     []histFailure{{status: 400, body: `{"code":-1142,"msg":"Parameter 'startTime' is invalid."}`}},
			wantRequests: 1, wantKind: HistoryErrAPI},
		{name: "429 respeta Retry-After",
			failures:     []histFailure{{status: 429, body: `{"code":-1003,"msg":"Too many requests"}`, retryAfter: "7"}},
			wantRequests: 2, wantBars: 2, wantWaits: []time.Duration{7 * time.Second}},
		{name: "429 Retry-After se acota",
			failures:     []histFailure{{status: 429, retryAfter: "3600"}},
			wantRequests: 2, wantBars: 2, wantWaits: []time.Duration{MaxRetryAfter}},
		{name: "5xx con backoff",
			failures:     []histFailure{{status: 502, body: "bad gateway"}, {status: 503, body: "unavailable"}},
			wantRequests: 3, wantBars: 2, wantWaits: []time.Duration{retryBackoff(1), retryBackoff(2)}},
		{name: "5xx persistente agota intentos",
			failures:     []histFailure{{status: 500}, {status: 500}, {status: 500}},
			wantRequests: 3, wantKind: HistoryErrServer, wantWaits: []time.Duration{retryBackoff(1), retryBackoff(2)}},
		{name: "timeout se reintenta y queda pendiente",
			failures:     []histFailure{{status: 200, delay: 300 * time.Millisecond}, {status: 200, delay: 300 * time.Millisecond}, {status: 200, delay: 300 * time.Millisecond}},
			client:       &http.Client{Timeout: 60 * time.Millisecond},
			wantRequests: 3, wantKind: HistoryErrTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clock := newFakeClock(cursor.Add(3*step + time.Minute))
			srv := newContractServer(clock, t0.Add(-100*step))
			srv.failures = c.failures
			hs := httptest.NewServer(srv)
			defer hs.Close()
			p, rs := newGapProvider(t, hs.URL, clock)
			if c.client != nil {
				p.client = c.client
			}
			st := seedFeed(p, "BTCUSDT", "1h", cursor, time.Time{})
			ch := make(chan domain.Kline, 16)

			p.reconcile(context.Background(), st, ch, time.Time{}, "reconexion")

			reqs, _ := srv.snapshot()
			if len(reqs) != c.wantRequests {
				t.Fatalf("peticiones = %d, want %d", len(reqs), c.wantRequests)
			}
			if got := closedStarts(drain(ch)); len(got) != c.wantBars {
				t.Fatalf("velas = %v, want %d", got, c.wantBars)
			}
			if c.wantWaits != nil {
				waits := rs.all()
				if len(waits) != len(c.wantWaits) {
					t.Fatalf("esperas = %v, want %v", waits, c.wantWaits)
				}
				for i := range waits {
					if waits[i] != c.wantWaits[i] {
						t.Fatalf("espera %d = %v, want %v", i, waits[i], c.wantWaits[i])
					}
				}
			}
			h := p.Health()[0]
			if c.wantKind != "" {
				if !h.Degraded || len(h.Pending) != 1 || h.Pending[0].Attempts != 1 {
					t.Fatalf("el fallo debe dejar el hueco pendiente y explícito: %+v", h.Pending)
				}
				errs := p.RecentErrors(10)
				if len(errs) == 0 || errs[0].Kind != c.wantKind {
					t.Fatalf("error registrado = %+v, want kind %s", errs, c.wantKind)
				}
				if !h.LastClosed.Equal(cursor) {
					t.Fatalf("un fallo no debe mover el cursor: %v", h.LastClosed)
				}
			} else if h.Degraded {
				t.Fatalf("tras recuperarse no debería quedar degradado: %s", h.DegradedReason)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("12", now); got != 12*time.Second {
		t.Errorf("segundos: %v", got)
	}
	if got := parseRetryAfter(now.Add(9*time.Second).Format(http.TimeFormat), now); got != 9*time.Second {
		t.Errorf("fecha HTTP: %v", got)
	}
	if got := parseRetryAfter("99999", now); got != MaxRetryAfter {
		t.Errorf("tope: %v", got)
	}
	if got := parseRetryAfter("basura", now); got != 0 {
		t.Errorf("inválido: %v", got)
	}
}

func TestSanitizeMessageMasksSecretsAndControlChars(t *testing.T) {
	got := sanitizeMessage("err\nsign=abc123&apiKey=XYZ token: \"t0k\" ok")
	for _, leak := range []string{"abc123", "XYZ", "t0k", "\n"} {
		if strings.Contains(got, leak) {
			t.Errorf("mensaje saneado %q contiene %q", got, leak)
		}
	}
}

// ---------------------------------------------------------------------------
// 4. WebSocket: ping público, desconexión y recuperación extremo a extremo.
// ---------------------------------------------------------------------------

func writeWexBar(t *testing.T, conn *websocket.Conn, symbol, tf string, openT time.Time, step time.Duration, close float64) {
	t.Helper()
	payload := map[string]any{
		"e": "kline", "s": symbol, "p": "LAST_PRICE",
		"d": []map[string]any{{
			"t": openT.UnixMilli(), "T": openT.Add(step).UnixMilli() - 1, "s": symbol, "i": tf,
			"o": fmt.Sprintf("%.1f", close-10), "h": fmt.Sprintf("%.1f", close+20), "l": fmt.Sprintf("%.1f", close-30),
			"c": fmt.Sprintf("%.1f", close), "v": "3.5",
		}},
	}
	if err := conn.WriteJSON(payload); err != nil {
		t.Errorf("WriteJSON: %v", err)
	}
}

// TestWexWSAnswersPublicEventPing: WEEX envía {"event":"ping"} en el canal
// público y cierra la sesión si no recibe PONG. El código antiguo no lo
// reconocía como ping.
func TestWexWSAnswersPublicEventPing(t *testing.T) {
	p := NewWEEXProvider(nil, WeexConfig{})
	if !p.isPing([]byte(`{"event":"ping","time":"1693208170000"}`)) {
		t.Fatal(`{"event":"ping"} debe reconocerse como ping`)
	}
	if !p.isPing([]byte(`{"type":"ping","time":"1693208170000"}`)) {
		t.Fatal(`{"type":"ping"} (privado) debe reconocerse como ping`)
	}
	if p.isPing([]byte(`{"result":true,"id":1}`)) || p.isPing([]byte(`{"event":"pong"}`)) {
		t.Fatal("ack/pong no son pings")
	}

	pong := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := upgrade(t, w, r)
		if conn == nil {
			return
		}
		defer conn.Close()
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("el canal público exige User-Agent")
		}
		readWexMsg(t, conn) // SUBSCRIBE
		conn.WriteJSON(map[string]any{"event": "ping", "time": "1693208170000"})
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("sin respuesta al ping: %v", err)
			return
		}
		m := map[string]any{}
		_ = json.Unmarshal(msg, &m)
		pong <- m
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()
	p = wexTestProvider(wsURL(t, srv), "LAST_PRICE")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := p.Subscribe(ctx, "BTCUSDT", "1h"); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-pong:
		if m["method"] != "PONG" {
			t.Fatalf("respuesta = %v, want PONG", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout esperando PONG")
	}
}

func TestParseWSKlinesKeepsEveryBarOfArray(t *testing.T) {
	p := NewWEEXProvider(nil, WeexConfig{})
	open := t0
	p.now = func() time.Time { return open.Add(10 * time.Second) }
	msg := fmt.Sprintf(`{"e":"kline","d":[{"t":%d,"T":%d,"o":"1","h":"2","l":"0.5","c":"1.5","v":"1"},{"t":%d,"T":%d,"o":"1.5","h":"2","l":"1","c":"1.8","v":"1"}]}`,
		open.Add(-time.Hour).UnixMilli(), open.UnixMilli()-1, open.UnixMilli(), open.Add(time.Hour).UnixMilli()-1)
	bars, err := p.parseWSKlines([]byte(msg), "BTCUSDT", "1h")
	if err != nil || len(bars) != 2 {
		t.Fatalf("bars=%d err=%v", len(bars), err)
	}
	if !bars[0].Closed || bars[1].Closed {
		t.Fatalf("flags cerrada = %v/%v, want true/false", bars[0].Closed, bars[1].Closed)
	}
}

// TestWexWSRecoversClosedBarsAfterDisconnect es la regresión extremo a extremo:
// la conexión se cierra ("session is inactive") con una vela en formación; al
// reconectar, ya han pasado tres velas. Deben entregarse EXACTAMENTE las tres
// velas cerradas que faltan, en orden, sin duplicados y sin ninguna petición
// fuera de contrato. Con la lógica antigua la recuperación pedía startTime y
// endTime futuros (-1142) y no entregaba ninguna vela cerrada.
func TestWexWSRecoversClosedBarsAfterDisconnect(t *testing.T) {
	for _, tf := range []string{"5m", "15m", "1h"} {
		t.Run(tf, func(t *testing.T) {
			step := parseInterval(tf)
			x0 := t0
			clock := newFakeClock(x0.Add(20 * time.Second))
			hist := newContractServer(clock, t0.Add(-1000*step))
			var dials atomic.Int32
			mux := http.NewServeMux()
			mux.Handle("/capi/v3/market/historyKlines", hist)
			mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
				n := dials.Add(1)
				conn := upgrade(t, w, r)
				if conn == nil {
					return
				}
				defer conn.Close()
				readWexMsg(t, conn)
				switch n {
				case 1:
					writeWexBar(t, conn, "BTCUSDT", tf, x0, step, 50000)
					conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session is inactive"))
				default:
					clock.Set(x0.Add(3*step + 30*time.Second))
					writeWexBar(t, conn, "BTCUSDT", tf, x0.Add(3*step), step, 50300)
					conn.SetReadDeadline(time.Now().Add(3 * time.Second))
					conn.ReadMessage()
				}
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()

			p := NewWEEXProvider(&fakeCandleStore{dup: map[string]bool{}}, WeexConfig{
				RestURL: srv.URL, WSURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws",
				ReconnectBase: 30 * time.Millisecond, ReconnectMax: 60 * time.Millisecond,
				PongWait: 2 * time.Second, HistoryPageDelay: time.Nanosecond,
			})
			p.now = clock.Now
			sink := &memSink{}
			p.SetEventSink(sink)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch, err := p.Subscribe(ctx, "BTCUSDT", tf)
			if err != nil {
				t.Fatal(err)
			}
			var got []domain.Kline
			deadline := time.After(4 * time.Second)
			for len(got) < 5 {
				select {
				case k := <-ch:
					got = append(got, k)
				case <-deadline:
					t.Fatalf("timeout; recibidas %d: %+v", len(got), got)
				}
			}
			if got[0].Closed || !got[0].Start.Equal(x0) {
				t.Fatalf("primera = %+v, want vela en formación x0", got[0])
			}
			if c := closedStarts(got); !equalInts(c, expectStarts(x0, 3, step)) {
				t.Fatalf("cerradas = %v, want %v", c, expectStarts(x0, 3, step))
			}
			if last := got[4]; last.Closed || !last.Start.Equal(x0.Add(3*step)) {
				t.Fatalf("última = %+v, want vela en formación x3", last)
			}
			hist.assertNoViolations(t)
			h := p.Health()[0]
			if h.Reconnects < 1 || !h.LastClosed.Equal(x0.Add(2*step)) || h.Degraded {
				t.Fatalf("salud tras reconexión: %+v", h)
			}
			if !strings.Contains(strings.Join(sink.kinds(), ","), EventWSDisconnected) {
				t.Fatalf("la desconexión no quedó persistida: %v", sink.kinds())
			}
		})
	}
}

func TestEventSinkFailureIsObservable(t *testing.T) {
	step := time.Hour
	clock := newFakeClock(t0.Add(3*step + time.Minute))
	srv := newContractServer(clock, t0.Add(-100*step))
	hs := httptest.NewServer(srv)
	defer hs.Close()
	p, _ := newGapProvider(t, hs.URL, clock)
	p.SetEventSink(&memSink{fail: errors.New("disco lleno")})
	st := seedFeed(p, "BTCUSDT", "1h", t0, time.Time{})
	ch := make(chan domain.Kline, 16)
	p.reconcile(context.Background(), st, ch, time.Time{}, "reconexion")
	if got := closedStarts(drain(ch)); len(got) != 2 {
		t.Fatalf("la recuperación no debe depender de la persistencia de eventos: %v", got)
	}
	h := p.Health()[0]
	if h.Counters.PersistErrors == 0 {
		t.Fatal("el fallo de persistencia debe contarse")
	}
	found := false
	for _, e := range p.RecentErrors(10) {
		if e.Component == "persistencia" && strings.Contains(e.Message, "disco lleno") {
			found = true
		}
	}
	if !found {
		t.Fatal("el fallo de persistencia debe aparecer en los errores del proveedor")
	}
}

func TestAlignDownIntervals(t *testing.T) {
	ts := time.Date(2026, 11, 12, 13, 47, 31, 0, time.UTC) // jueves
	cases := map[string]time.Time{
		"5m":  time.Date(2026, 11, 12, 13, 45, 0, 0, time.UTC),
		"15m": time.Date(2026, 11, 12, 13, 45, 0, 0, time.UTC),
		"1h":  time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC),
		"4h":  time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC),
		"1d":  time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC),
		"1w":  time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), // lunes
	}
	for tf, want := range cases {
		if got := alignDown(ts, parseInterval(tf)); !got.Equal(want) {
			t.Errorf("%s: %s, want %s", tf, got.UTC(), want)
		}
	}
}
