package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"tradingview-bot/internal/domain"

	"github.com/gorilla/websocket"
)

// Semántica del stream WEEX (canal público kline, docs oficiales v3):
//
//   - El canal empuja la vela EN FORMACIÓN cada vez que se crea o se refresca;
//     no existe un indicador de "vela final". Una vela solo se considera cerrada
//     cuando llega con now >= T (cierre) o cuando se confirma por REST.
//   - El cursor del feed (feedState.lastClosed) es el inicio de la última vela
//     CERRADA entregada aguas abajo. Nunca avanza con una vela abierta: así una
//     desconexión no puede saltarse velas cerradas pendientes.
//   - Al cambiar de vela (llega la vela en formación X), todas las velas < X
//     están cerradas: las que falten desde el cursor se recuperan por REST con
//     un rango que respeta el contrato (startTime/endTime nunca futuros).
//   - El servidor envía pings {"event":"ping","time":"..."} en el canal público
//     y cierra la sesión si no recibe {"method":"PONG","id":1}.

// Subscribe abre un canal de velas en streaming para un symbol/timeframe.
// Gestiona internamente la reconexión con backoff, responde al ping de WEEX y
// recupera por REST las velas cerradas que falten.
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
	st := p.feed(symbol, timeframe)
	backoff := p.cfg.ReconnectBase
	for {
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		err := p.runConnection(ctx, st, symbol, timeframe, out)
		now := p.now()
		reason := "conexión finalizada"
		if err != nil {
			reason = err.Error()
		}
		st.mu.Lock()
		st.connected = false
		st.lastDisconnectAt = now
		st.lastDisconnect = sanitizeMessage(reason)
		st.reconnects++
		cursor := st.lastClosed
		st.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		log.Printf("weex ws: conexión finalizada (%s %s): %v; cursor=%s", symbol, timeframe, err, fmtMs(cursor))
		p.recordError(ProviderError{At: now, Component: "ws", Symbol: symbol, Timeframe: timeframe,
			Kind: "desconexion", Message: reason, Severity: SeverityWarn})
		p.emitEvent(ctx, st, IngestEvent{Key: eventKey(EventWSDisconnected, symbol, timeframe, now),
			Symbol: symbol, Timeframe: timeframe, Kind: EventWSDisconnected, Severity: SeverityWarn,
			Cursor: cursor, Message: reason, At: now})
		// Recuperar las velas cerradas que pudieron perderse sin conexión.
		p.reconcile(ctx, st, out, time.Time{}, "reconexion")
		if time.Since(started) >= 60*time.Second {
			backoff = p.cfg.ReconnectBase
		}
		if !waitOrDone(ctx, backoff) {
			return
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

func (p *WeexProvider) runConnection(ctx context.Context, st *feedState, symbol, timeframe string, out chan<- domain.Kline) error {
	dialer := websocket.Dialer{HandshakeTimeout: p.cfg.DialTimeout}
	hdr := http.Header{}
	hdr.Set("User-Agent", p.cfg.UserAgent)
	conn, _, err := dialer.DialContext(ctx, p.cfg.WSURL, hdr)
	if err != nil {
		return fmt.Errorf("dial ws: %w", err)
	}
	defer conn.Close()
	// ReadMessage bloquea hasta el deadline; cerrar la conexión al cancelar el
	// contexto permite un apagado inmediato.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetReadLimit(4 << 20)

	sub := map[string]any{
		"method": "SUBSCRIBE",
		"params": []string{p.channelName(symbol, timeframe)},
		"id":     1,
	}
	if err := conn.WriteJSON(sub); err != nil {
		return fmt.Errorf("suscribir: %w", err)
	}
	now := p.now()
	st.mu.Lock()
	st.connected = true
	st.connectedAt = now
	cursor := st.lastClosed
	st.mu.Unlock()
	p.emitEvent(ctx, st, IngestEvent{Key: eventKey(EventWSConnected, symbol, timeframe, now),
		Symbol: symbol, Timeframe: timeframe, Kind: EventWSConnected, Severity: SeverityInfo, Cursor: cursor, At: now})

	for {
		conn.SetReadDeadline(time.Now().Add(p.cfg.PongWait))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		st.mu.Lock()
		st.lastMessageAt = p.now()
		st.counters.Messages++
		st.mu.Unlock()
		if p.isPing(msg) {
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteJSON(map[string]any{"method": "PONG", "id": 1}); err != nil {
				return fmt.Errorf("pong: %w", err)
			}
			st.mu.Lock()
			st.counters.Pings++
			st.counters.Pongs++
			st.mu.Unlock()
			continue
		}
		bars, err := p.parseWSKlines(msg, symbol, timeframe)
		if err != nil {
			log.Printf("weex ws: mensaje kline inválido: %v", err)
			continue
		}
		for _, c := range bars {
			if err := p.handleWSBar(ctx, st, out, c); err != nil {
				return err
			}
		}
	}
}

// handleWSBar aplica la semántica de cursor a una vela recibida por WS.
func (p *WeexProvider) handleWSBar(ctx context.Context, st *feedState, out chan<- domain.Kline, c domain.Kline) error {
	step := st.step
	if !isAligned(c.Start, step) {
		st.mu.Lock()
		st.counters.Misaligned++
		st.mu.Unlock()
		log.Printf("weex ws: vela descartada %s %s ts=%s: no alineada a %s", c.Symbol, c.Timeframe, fmtMs(c.Start), step)
		return nil
	}
	st.mu.Lock()
	lastClosed, lastOpen := st.lastClosed, st.lastOpen
	st.mu.Unlock()

	if c.Closed {
		if !lastClosed.IsZero() && !c.Start.After(lastClosed) {
			st.mu.Lock()
			st.counters.Duplicates++
			st.mu.Unlock()
			return nil
		}
		// Antes de entregar la vela cerrada, recuperar las anteriores que falten
		// para mantener el orden cronológico aguas abajo.
		p.reconcile(ctx, st, out, c.Start.Add(-step), "ws_hueco")
		if err := emitKline(ctx, out, c); err != nil {
			return err
		}
		st.mu.Lock()
		st.advanceOver(c.Start)
		st.markClosed(c.Start, "ws", p.now())
		st.mu.Unlock()
		return nil
	}

	// Vela en formación.
	if (!lastClosed.IsZero() && !c.Start.After(lastClosed)) || (!lastOpen.IsZero() && c.Start.Before(lastOpen)) {
		// Su versión cerrada ya se entregó (o es un mensaje atrasado): no
		// sobrescribirla con datos parciales.
		st.mu.Lock()
		st.counters.StaleOpen++
		st.mu.Unlock()
		return nil
	}
	now := p.now()
	st.mu.Lock()
	rollover := c.Start.After(lastOpen)
	if rollover {
		st.lastOpen = c.Start
		if st.firstOpen.IsZero() {
			st.firstOpen = c.Start
		}
	}
	st.counters.OpenBars++
	due := st.retryDue(now)
	st.mu.Unlock()
	switch {
	case rollover && (!lastOpen.IsZero() || !lastClosed.IsZero()):
		p.reconcile(ctx, st, out, c.Start.Add(-step), "cambio_de_vela")
	case due:
		p.reconcile(ctx, st, out, c.Start.Add(-step), "reintento")
	}
	return emitKline(ctx, out, c)
}

func emitKline(ctx context.Context, out chan<- domain.Kline, k domain.Kline) error {
	select {
	case out <- k:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retryDue indica si hay un intervalo pendiente cuyo reintento ya venció.
func (s *feedState) retryDue(now time.Time) bool {
	if s.tail != nil && !s.tail.NextRetry.After(now) {
		return true
	}
	for _, h := range s.holes {
		if h.Status == GapPending && !h.NextRetry.After(now) {
			return true
		}
	}
	return false
}

// advanceOver mueve a "huecos" la parte del intervalo de cola que queda por
// debajo de un cursor nuevo (vela cerrada por WS antes de recuperar el hueco).
// Así el avance del cursor nunca oculta velas pendientes: quedan explícitas y
// se siguen reintentando.
func (s *feedState) advanceOver(newCursor time.Time) {
	if s.tail == nil || !s.tail.From.Before(newCursor) {
		return
	}
	holeTo := s.tail.To
	if lim := newCursor.Add(-s.step); holeTo.After(lim) {
		holeTo = lim
	}
	if !holeTo.Before(s.tail.From) {
		h := *s.tail
		h.To = holeTo
		h.Bars = barsBetween(h.From, h.To, s.step)
		h.Status = GapPending
		h.Reason = "cursor_avanzado_por_ws"
		s.addHole(h)
	}
	if s.tail.To.After(newCursor) {
		s.tail.From = newCursor.Add(s.step)
		s.tail.Bars = barsBetween(s.tail.From, s.tail.To, s.step)
	} else {
		s.tail = nil
	}
}

func (p *WeexProvider) gapRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 8 {
		attempts = 8
	}
	d := time.Duration(1<<uint(attempts-1)) * 2 * time.Second
	if d > p.cfg.GapRetryMax {
		d = p.cfg.GapRetryMax
	}
	return d
}

// reconcile garantiza que todas las velas cerradas posteriores al cursor y
// hasta upTo (inicio de vela, inclusivo; cero = la última cerrada según el
// reloj) se entreguen. Nunca construye un rango futuro: solo pide velas que ya
// están cerradas en el servidor según el reloj local menos ClockSkew.
func (p *WeexProvider) reconcile(ctx context.Context, st *feedState, out chan<- domain.Kline, upTo time.Time, trigger string) {
	now := p.now()
	step := st.step
	safe := lastClosedStart(now, step, p.cfg.ClockSkew)
	target := upTo
	if target.IsZero() {
		target = safe
	}

	st.mu.Lock()
	floor := st.floor()
	if floor.IsZero() {
		st.mu.Unlock()
		p.retryHoles(ctx, st, out)
		return
	}
	from := floor.Add(step)
	if target.Before(from) {
		if st.tail != nil && st.tail.To.Before(from) {
			st.tail = nil
		}
		st.mu.Unlock()
		p.retryHoles(ctx, st, out)
		return
	}
	newGap := false
	switch {
	case st.tail == nil || !st.tail.From.Equal(from):
		st.tail = &GapRange{From: from, To: target, DetectedAt: now, Status: GapPending}
		newGap = true
	case target.After(st.tail.To):
		st.tail.To = target
		newGap = true
	}
	tail := st.tail
	tail.Bars = barsBetween(tail.From, tail.To, step)
	routine := tail.Bars == 1 && tail.Attempts == 0 && (trigger == "cambio_de_vela" || trigger == "reintento")
	due := newGap || !tail.NextRetry.After(now)
	snapshot := *tail
	st.mu.Unlock()

	if newGap && !routine {
		log.Printf("weex ws: hueco detectado %s %s cursor=%s faltan %d velas [%s .. %s] (disparador %s)",
			st.symbol, st.timeframe, fmtMs(floor), snapshot.Bars, fmtMs(snapshot.From), fmtMs(snapshot.To), trigger)
		p.emitEvent(ctx, st, IngestEvent{Key: eventKey(EventGapDetected, st.symbol, st.timeframe, snapshot.From, snapshot.To),
			Symbol: st.symbol, Timeframe: st.timeframe, Kind: EventGapDetected, Severity: SeverityWarn,
			Cursor: floor, From: snapshot.From, To: snapshot.To, Expected: snapshot.Bars,
			Detail: map[string]any{"disparador": trigger}})
	}
	if !due {
		p.retryHoles(ctx, st, out)
		return
	}

	fetchTo := target
	if fetchTo.After(safe) {
		fetchTo = safe
	}
	if fetchTo.Before(from) {
		// Ninguna vela del intervalo está cerrada aún en el servidor: se
		// reintenta en cuanto la primera lo esté (no se envía nada futuro).
		st.mu.Lock()
		if st.tail != nil {
			st.tail.NextRetry = from.Add(step).Add(p.cfg.ClockSkew)
		}
		st.mu.Unlock()
		return
	}
	fetchFrom := from
	if exp := barsBetween(from, fetchTo, step); exp > p.cfg.BackfillBars {
		fetchFrom = fetchTo.Add(-time.Duration(p.cfg.BackfillBars-1) * step)
		h := GapRange{From: from, To: fetchFrom.Add(-step), DetectedAt: now, Status: GapPending,
			Reason: "supera_maximo_por_recuperacion", NextRetry: now}
		h.Bars = barsBetween(h.From, h.To, step)
		st.mu.Lock()
		st.addHole(h)
		st.mu.Unlock()
		log.Printf("weex ws: hueco %s %s de %d velas supera el máximo (%d); se recupera lo más reciente y [%s .. %s] queda pendiente",
			st.symbol, st.timeframe, exp, p.cfg.BackfillBars, fmtMs(h.From), fmtMs(h.To))
	}

	res := p.fetchAndEmit(ctx, st, out, fetchFrom, fetchTo, trigger, snapshot.Attempts+1)

	st.mu.Lock()
	if res.err != nil {
		if st.tail != nil {
			st.tail.Attempts++
			st.tail.LastError = res.rec.Error
			st.tail.NextRetry = now.Add(p.gapRetryDelay(st.tail.Attempts))
		}
		st.counters.RecoveryFailures++
		st.addBackfill(res.rec)
		st.mu.Unlock()
		return
	}
	p.registerInternalHoles(st, fetchFrom, res.received, now)
	newFloor := st.floor()
	nextFrom := newFloor.Add(step)
	var unrecoverable *GapRange
	switch {
	case target.Before(nextFrom):
		st.tail = nil
	default:
		prevAttempts := 0
		if st.tail != nil && st.tail.From.Equal(nextFrom) {
			prevAttempts = st.tail.Attempts
		}
		st.tail = &GapRange{From: nextFrom, To: target, DetectedAt: snapshot.DetectedAt, Status: GapPending, Attempts: prevAttempts}
		st.tail.Bars = barsBetween(nextFrom, target, step)
		if !fetchTo.Before(nextFrom) {
			// WEEX respondió sin las velas cerradas esperadas.
			st.tail.Attempts++
			st.tail.LastError = fmt.Sprintf("WEEX no devolvió %d velas cerradas", barsBetween(nextFrom, fetchTo, step))
			st.tail.NextRetry = now.Add(p.gapRetryDelay(st.tail.Attempts))
			if st.tail.Attempts >= p.cfg.MaxGapRetries && !fetchTo.Add(step).Add(p.cfg.GapUnrecoverableAfter).After(now) {
				// Persistentemente ausentes mucho después de su cierre: se
				// declaran irrecuperables y el cursor deja constancia.
				u := GapRange{From: nextFrom, To: fetchTo, DetectedAt: snapshot.DetectedAt, Attempts: st.tail.Attempts,
					Status: GapUnrecoverable, Reason: "weex_sin_datos", LastError: st.tail.LastError}
				u.Bars = barsBetween(u.From, u.To, step)
				st.addHole(u)
				unrecoverable = &u
				st.lastClosed = fetchTo
				st.lastClosedSource = "irrecuperable"
				if target.After(fetchTo) {
					st.tail = &GapRange{From: fetchTo.Add(step), To: target, DetectedAt: now, Status: GapPending}
					st.tail.Bars = barsBetween(st.tail.From, target, step)
				} else {
					st.tail = nil
				}
			}
		} else {
			st.tail.NextRetry = nextFrom.Add(step).Add(p.cfg.ClockSkew)
		}
	}
	if !(routine && res.rec.Outcome == "ok") {
		st.addBackfill(res.rec)
	}
	st.mu.Unlock()

	if unrecoverable != nil {
		p.declareUnrecoverable(ctx, st, *unrecoverable)
	}
	p.retryHoles(ctx, st, out)
}

func (p *WeexProvider) declareUnrecoverable(ctx context.Context, st *feedState, g GapRange) {
	log.Printf("weex ws: hueco IRRECUPERABLE %s %s [%s .. %s] (%d velas) tras %d intentos: %s",
		st.symbol, st.timeframe, fmtMs(g.From), fmtMs(g.To), g.Bars, g.Attempts, g.LastError)
	p.recordError(ProviderError{Component: "backfill", Symbol: st.symbol, Timeframe: st.timeframe,
		Kind: "hueco_irrecuperable", Message: fmt.Sprintf("[%s .. %s] %s", fmtMs(g.From), fmtMs(g.To), g.LastError), Severity: SeverityFail})
	p.emitEvent(ctx, st, IngestEvent{Key: eventKey(EventGapUnrecoverable, st.symbol, st.timeframe, g.From, g.To),
		Symbol: st.symbol, Timeframe: st.timeframe, Kind: EventGapUnrecoverable, Severity: SeverityFail,
		From: g.From, To: g.To, Expected: g.Bars, Message: g.LastError})
}

// registerInternalHoles registra como huecos pendientes las velas ausentes
// ENTRE velas recibidas (la cola se gestiona aparte). Debe llamarse con st.mu.
func (p *WeexProvider) registerInternalHoles(st *feedState, from time.Time, received []time.Time, now time.Time) {
	if len(received) == 0 {
		return
	}
	step := st.step
	last := received[len(received)-1]
	got := make(map[int64]bool, len(received))
	for _, ts := range received {
		got[ts.UnixMilli()] = true
	}
	var cur *GapRange
	for ts := from; ts.Before(last); ts = ts.Add(step) {
		if got[ts.UnixMilli()] {
			if cur != nil {
				cur.Bars = barsBetween(cur.From, cur.To, step)
				st.addHole(*cur)
				cur = nil
			}
			continue
		}
		if cur == nil {
			cur = &GapRange{From: ts, DetectedAt: now, Status: GapPending, Attempts: 1,
				NextRetry: now.Add(p.gapRetryDelay(1)), Reason: "weex_omitio_velas_intermedias"}
		}
		cur.To = ts
	}
	if cur != nil {
		cur.Bars = barsBetween(cur.From, cur.To, step)
		st.addHole(*cur)
	}
}

// retryHoles reintenta (como máximo dos por llamada) los huecos pendientes
// cuyo reintento venció. Las velas recuperadas son anteriores al cursor: se
// entregan para que se persistan, y el motor las ignora por antigüedad.
func (p *WeexProvider) retryHoles(ctx context.Context, st *feedState, out chan<- domain.Kline) {
	now := p.now()
	st.mu.Lock()
	var due []GapRange
	for _, h := range st.holes {
		if h.Status == GapPending && !h.NextRetry.After(now) && len(due) < 2 {
			due = append(due, h)
		}
	}
	st.mu.Unlock()
	for _, h := range due {
		res := p.fetchAndEmit(ctx, st, out, h.From, h.To, "hueco_reintento", h.Attempts+1)
		st.mu.Lock()
		idx := -1
		for i := range st.holes {
			if st.holes[i].From.Equal(h.From) && st.holes[i].To.Equal(h.To) {
				idx = i
				break
			}
		}
		if idx < 0 {
			st.mu.Unlock()
			continue
		}
		g := &st.holes[idx]
		g.Attempts++
		st.addBackfill(res.rec)
		var unrecoverable *GapRange
		switch {
		case res.err != nil:
			g.LastError = res.rec.Error
			g.NextRetry = now.Add(p.gapRetryDelay(g.Attempts))
			st.counters.RecoveryFailures++
		case len(res.received) == g.Bars:
			st.holes = append(st.holes[:idx], st.holes[idx+1:]...)
		default:
			g.LastError = fmt.Sprintf("WEEX devolvió %d de %d velas", len(res.received), g.Bars)
			g.NextRetry = now.Add(p.gapRetryDelay(g.Attempts))
			if g.Attempts >= p.cfg.MaxGapRetries {
				g.Status = GapUnrecoverable
				u := *g
				unrecoverable = &u
			}
		}
		st.mu.Unlock()
		if unrecoverable != nil {
			p.declareUnrecoverable(ctx, st, *unrecoverable)
		}
	}
}

type gapFetchResult struct {
	rec      BackfillRecord
	received []time.Time
	err      error
}

// fetchAndEmit descarga las velas cerradas [from, to] (inicios inclusivos),
// las valida, las entrega en orden cronológico y avanza el cursor.
func (p *WeexProvider) fetchAndEmit(ctx context.Context, st *feedState, out chan<- domain.Kline, from, to time.Time, trigger string, attempt int) gapFetchResult {
	step := st.step
	began := time.Now()
	now := p.now()
	expected := barsBetween(from, to, step)
	// endTime = fin del periodo de la última vela pedida; nunca supera el reloj
	// menos skew porque `to` ya es como mucho la última vela cerrada.
	end := to.Add(step - time.Millisecond)
	st.mu.Lock()
	cursorBefore := st.lastClosed
	st.mu.Unlock()
	rec := BackfillRecord{At: now, Trigger: trigger, Symbol: st.symbol, Timeframe: st.timeframe,
		CursorBefore: cursorBefore, From: from, To: to, Expected: expected,
		Request: describeHistoryRequest(st.symbol, st.timeframe, from, end, HistoryMaxBars)}

	rctx, cancel := context.WithTimeout(ctx, p.cfg.GapRecoveryTimeout)
	bars, err := p.GetKlines(rctx, st.symbol, st.timeframe, from, end, expected)
	cancel()
	if err != nil {
		rec.Outcome, rec.Error, rec.Duration, rec.CursorAfter = "fallido", sanitizeMessage(err.Error()), time.Since(began), cursorBefore
		log.Printf("weex ws: backfill de hueco %s %s falló (intento %d, %s): %v; cursor=%s pendiente=[%s .. %s] petición=%s",
			st.symbol, st.timeframe, attempt, trigger, err, fmtMs(cursorBefore), fmtMs(from), fmtMs(to), rec.Request)
		ev := IngestEvent{Key: eventKey(EventBackfillFailed, st.symbol, st.timeframe, from, to, attempt),
			Symbol: st.symbol, Timeframe: st.timeframe, Kind: EventBackfillFailed, Severity: SeverityFail,
			Cursor: cursorBefore, From: from, To: to, Expected: expected, Message: rec.Error,
			Detail: map[string]any{"disparador": trigger, "intento": attempt, "peticion": rec.Request}}
		if he, ok := err.(*HistoryError); ok {
			ev.HTTPStatus, ev.Code = he.HTTPStatus, he.Code
		}
		p.emitEvent(ctx, st, ev)
		return gapFetchResult{rec: rec, err: err}
	}

	reasons := map[string]int{}
	seen := map[int64]bool{}
	valid := make([]domain.Kline, 0, len(bars))
	for _, k := range bars {
		switch {
		case !isAligned(k.Start, step):
			reasons["no_alineada"]++
		case k.Start.Before(from) || k.Start.After(to):
			reasons["fuera_de_rango"]++
		case !k.Closed:
			reasons["abierta"]++
		case seen[k.Start.UnixMilli()]:
			reasons["duplicada"]++
		default:
			seen[k.Start.UnixMilli()] = true
			valid = append(valid, k)
		}
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].Start.Before(valid[j].Start) })
	received := make([]time.Time, 0, len(valid))
	late := 0
	for _, k := range valid {
		if err := emitKline(ctx, out, k); err != nil {
			rec.Outcome, rec.Error = "fallido", err.Error()
			return gapFetchResult{rec: rec, received: received, err: err}
		}
		st.mu.Lock()
		if !k.Start.After(st.lastClosed) {
			late++
		}
		st.markClosed(k.Start, "rest", p.now())
		st.counters.BarsRecovered++
		st.mu.Unlock()
		received = append(received, k.Start)
	}
	discarded := len(bars) - len(valid)
	st.mu.Lock()
	st.counters.Recoveries++
	st.counters.BarsDiscarded += int64(discarded)
	rec.CursorAfter = st.lastClosed
	st.mu.Unlock()
	rec.Received, rec.Emitted, rec.Late, rec.Discarded = len(bars), len(valid), late, discarded
	if len(reasons) > 0 {
		rec.DiscardReasons = reasons
	}
	rec.Duration = time.Since(began)
	kind, sev := EventBackfillOK, SeverityInfo
	switch {
	case len(valid) == expected:
		rec.Outcome = "ok"
	case len(valid) == 0:
		rec.Outcome, kind, sev = "vacio", EventBackfillPartial, SeverityWarn
	default:
		rec.Outcome, kind, sev = "parcial", EventBackfillPartial, SeverityWarn
	}
	if !(rec.Outcome == "ok" && expected == 1 && (trigger == "cambio_de_vela" || trigger == "reintento")) {
		log.Printf("weex ws: backfill de hueco %s %s [%s .. %s] cursor %s→%s esperadas=%d recibidas=%d entregadas=%d tardías=%d descartadas=%d %v (%s, intento %d) resultado=%s",
			st.symbol, st.timeframe, fmtMs(from), fmtMs(to), fmtMs(cursorBefore), fmtMs(rec.CursorAfter),
			expected, len(bars), len(valid), late, discarded, reasons, trigger, attempt, rec.Outcome)
		p.emitEvent(ctx, st, IngestEvent{Key: eventKey(kind, st.symbol, st.timeframe, from, to, attempt),
			Symbol: st.symbol, Timeframe: st.timeframe, Kind: kind, Severity: sev,
			Cursor: cursorBefore, From: from, To: to, Expected: expected, Received: len(valid), Discarded: discarded,
			Detail: map[string]any{"disparador": trigger, "intento": attempt, "tardias": late, "motivos_descarte": reasons}})
	}
	return gapFetchResult{rec: rec, received: received}
}

// isPing reconoce los pings de WEEX: {"event":"ping"} (canal público),
// {"type":"ping"} (privado) y variantes con "method"/"ping".
func (p *WeexProvider) isPing(msg []byte) bool {
	var m struct {
		Method string          `json:"method"`
		Type   string          `json:"type"`
		Event  string          `json:"event"`
		Ping   json.RawMessage `json:"ping"`
	}
	if err := json.Unmarshal(msg, &m); err != nil {
		return false
	}
	if len(m.Ping) > 0 && string(m.Ping) != "null" {
		return true
	}
	kind := strings.ToLower(m.Method + " " + m.Type + " " + m.Event)
	return strings.Contains(kind, "ping") && !strings.Contains(kind, "pong")
}

// parseWSKline devuelve la vela más reciente de un mensaje (compatibilidad).
func (p *WeexProvider) parseWSKline(msg []byte, symbol, timeframe string) (domain.Kline, bool, error) {
	bars, err := p.parseWSKlines(msg, symbol, timeframe)
	if err != nil || len(bars) == 0 {
		return domain.Kline{}, false, err
	}
	return bars[len(bars)-1], true, nil
}

// parseWSKlines extrae TODAS las velas de un mensaje del canal kline, en orden
// cronológico. WEEX envía "d" como array; si incluye la vela anterior ya
// cerrada junto a la nueva, ambas se procesan. Tolera "data" y la raíz.
func (p *WeexProvider) parseWSKlines(msg []byte, symbol, timeframe string) ([]domain.Kline, error) {
	var top struct {
		Data json.RawMessage `json:"data"`
		D    json.RawMessage `json:"d"`
	}
	if err := json.Unmarshal(msg, &top); err != nil {
		return nil, err
	}
	now := p.now()
	var out []domain.Kline
	for _, raw := range []json.RawMessage{top.Data, top.D} {
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var fields weexKlineFields
		if err := json.Unmarshal(raw, &fields); err == nil && fields.hasPrice() {
			out = append(out, buildKline(fields, symbol, timeframe, p.isClosed(fields, now)))
			continue
		}
		var arr []weexKlineFields
		if err := json.Unmarshal(raw, &arr); err == nil {
			for _, f := range arr {
				if f.hasPrice() {
					out = append(out, buildKline(f, symbol, timeframe, p.isClosed(f, now)))
				}
			}
		}
	}
	if len(out) == 0 {
		var fields weexKlineFields
		if err := json.Unmarshal(msg, &fields); err == nil && fields.hasPrice() {
			out = append(out, buildKline(fields, symbol, timeframe, p.isClosed(fields, now)))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	// Si un mismo inicio aparece dos veces, conservar la última aparición.
	dedup := out[:0]
	for i := range out {
		if len(dedup) > 0 && dedup[len(dedup)-1].Start.Equal(out[i].Start) {
			dedup[len(dedup)-1] = out[i]
			continue
		}
		dedup = append(dedup, out[i])
	}
	return dedup, nil
}
