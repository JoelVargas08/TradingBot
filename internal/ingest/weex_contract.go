package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Contrato de GET /capi/v3/market/historyKlines verificado el 2026-11-15 con
// la documentación oficial de WEEX (Market_API/GetHistoryKlines) y con
// peticiones reales al endpoint público:
//
//   - startTime y endTime son Unix milisegundos e INCLUSIVOS; filtran por el
//     instante de apertura de la vela (startTime == endTime devuelve 1 vela).
//   - Ninguno puede ser posterior a la hora del servidor: endTime = ahora+2s ya
//     responde 400 {"code":-1142,"msg":"Parameter 'endTime' is invalid."} y un
//     startTime futuro responde -1142 "Parameter 'startTime' is invalid.".
//   - startTime > endTime responde -1171; un rango > 90 días responde -1170.
//   - El tamaño de página es `limit` (1..100, por defecto 100). `size` no existe:
//     WEEX lo ignora en silencio, por eso el código antiguo siempre recibía 100.
//   - La respuesta llega en orden DESCENDENTE y nunca incluye la vela en
//     formación (solo velas cerradas).
//   - Segundos en lugar de milisegundos responde -1142.
const (
	// HistoryClockSkew es el margen que se resta al reloj local antes de usarlo
	// como endTime, para que un reloj ligeramente adelantado respecto al
	// servidor no produzca un endTime "futuro" (-1142).
	HistoryClockSkew = 5 * time.Second
	// MaxRetryAfter acota la espera indicada por Retry-After para no bloquear
	// el feed indefinidamente.
	MaxRetryAfter = 60 * time.Second
)

// ErrHistoryContract indica que la petición construida violaría el contrato
// temporal del endpoint; se detecta ANTES de enviarla.
var ErrHistoryContract = errors.New("weex history: petición fuera de contrato")

// Clases de error del historial WEEX. Se separan los errores HTTP de los
// errores de negocio (código -11xx en el cuerpo) para poder auditarlos.
const (
	HistoryErrContract  = "contrato"   // la petición no se envió: violaría el contrato
	HistoryErrAPI       = "negocio"    // 4xx con código de negocio WEEX
	HistoryErrHTTP      = "http"       // 4xx sin código de negocio reconocible
	HistoryErrRateLimit = "rate_limit" // 429
	HistoryErrServer    = "servidor"   // 5xx
	HistoryErrTimeout   = "timeout"
	HistoryErrNetwork   = "red"
	HistoryErrParse     = "parseo"
)

// HistoryError describe un fallo de /historyKlines con la información necesaria
// para auditarlo sin exponer credenciales (el endpoint es público, pero el
// mensaje se sanea igualmente).
type HistoryError struct {
	Kind       string
	HTTPStatus int
	Code       int
	Message    string
	RetryAfter time.Duration
	Retriable  bool
	Request    string
}

func (e *HistoryError) Error() string {
	var b strings.Builder
	b.WriteString("weex history")
	if e.HTTPStatus > 0 {
		fmt.Fprintf(&b, " %d", e.HTTPStatus)
	}
	fmt.Fprintf(&b, " [%s]", e.Kind)
	if e.Code != 0 {
		fmt.Fprintf(&b, " code=%d", e.Code)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	return b.String()
}

func (e *HistoryError) Unwrap() error {
	if e.Kind == HistoryErrContract {
		return ErrHistoryContract
	}
	return nil
}

// alignDown devuelve el inicio de la vela de duración step que contiene t.
// Las velas de minutos/horas/días de WEEX están alineadas a UTC desde el epoch
// Unix; las semanales empiezan en lunes (el epoch fue jueves).
func alignDown(t time.Time, step time.Duration) time.Time {
	s := step.Milliseconds()
	if s <= 0 {
		return t
	}
	var off int64
	week := (7 * 24 * time.Hour).Milliseconds()
	if s%week == 0 {
		off = (4 * 24 * time.Hour).Milliseconds()
	}
	v := t.UnixMilli() - off
	r := v % s
	if r < 0 {
		r += s
	}
	return time.UnixMilli(v - r + off)
}

func isAligned(t time.Time, step time.Duration) bool {
	return alignDown(t, step).Equal(t)
}

// lastClosedStart devuelve el inicio de la vela más reciente que puede darse
// por cerrada en el servidor según el reloj local menos el margen de skew.
func lastClosedStart(now time.Time, step, skew time.Duration) time.Time {
	return alignDown(now.Add(-skew), step).Add(-step)
}

// barsBetween cuenta las velas cuyo inicio está en [from, to] (inclusivo).
func barsBetween(from, to time.Time, step time.Duration) int {
	if step <= 0 || to.Before(from) {
		return 0
	}
	return int(to.Sub(from)/step) + 1
}

// validateHistoryWindow comprueba el contrato temporal ANTES de enviar la
// petición. now es el reloj local (sin restar skew).
func validateHistoryWindow(start, end, now time.Time) error {
	switch {
	case start.IsZero() || end.IsZero():
		return fmt.Errorf("%w: startTime y endTime requeridos", ErrHistoryContract)
	case start.After(end):
		return fmt.Errorf("%w: startTime %s > endTime %s", ErrHistoryContract, fmtMs(start), fmtMs(end))
	case start.After(now):
		return fmt.Errorf("%w: startTime %s es futuro (ahora %s)", ErrHistoryContract, fmtMs(start), fmtMs(now))
	case end.After(now):
		return fmt.Errorf("%w: endTime %s es futuro (ahora %s)", ErrHistoryContract, fmtMs(end), fmtMs(now))
	case end.Sub(start) > HistoryMaxRange:
		return fmt.Errorf("%w: rango %s supera %s", ErrHistoryContract, end.Sub(start), HistoryMaxRange)
	}
	return nil
}

// fmtMs representa un instante como "ms (RFC3339 UTC)" para logs auditables.
func fmtMs(t time.Time) string {
	if t.IsZero() {
		return "0"
	}
	return fmt.Sprintf("%d(%s)", t.UnixMilli(), t.UTC().Format(time.RFC3339))
}

// describeHistoryRequest es la representación segura de una petición: no
// incluye host, cabeceras ni ningún parámetro que no sea de mercado.
func describeHistoryRequest(symbol, timeframe string, start, end time.Time, limit int) string {
	return fmt.Sprintf("GET /capi/v3/market/historyKlines symbol=%s interval=%s startTime=%s endTime=%s limit=%d",
		symbol, timeframe, fmtMs(start), fmtMs(end), limit)
}

var secretParam = regexp.MustCompile(`(?i)(sign|signature|api[-_]?key|access[-_]?key|token|passphrase|secret)([=:]\s*"?)[^&\s",}]+`)

// sanitizeMessage elimina caracteres de control, enmascara posibles secretos y
// acota la longitud de un mensaje externo antes de registrarlo o mostrarlo.
func sanitizeMessage(s string) string {
	s = secretParam.ReplaceAllString(s, "${1}${2}***")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	const max = 240
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

// classifyHistoryResponse convierte una respuesta no-200 en HistoryError.
func classifyHistoryResponse(status int, body []byte, header http.Header, now time.Time) *HistoryError {
	he := &HistoryError{HTTPStatus: status, Message: sanitizeMessage(string(body))}
	var biz struct {
		Code json.Number `json:"code"`
		Msg  string      `json:"msg"`
	}
	if err := json.Unmarshal(body, &biz); err == nil && biz.Code != "" {
		if c, err := strconv.Atoi(biz.Code.String()); err == nil {
			he.Code = c
		}
		if biz.Msg != "" {
			he.Message = sanitizeMessage(biz.Msg)
		}
	}
	switch {
	case status == http.StatusTooManyRequests:
		he.Kind, he.Retriable = HistoryErrRateLimit, true
		he.RetryAfter = parseRetryAfter(header.Get("Retry-After"), now)
	case status >= 500:
		he.Kind, he.Retriable = HistoryErrServer, true
		he.RetryAfter = parseRetryAfter(header.Get("Retry-After"), now)
	case he.Code != 0:
		he.Kind = HistoryErrAPI
	default:
		he.Kind = HistoryErrHTTP
	}
	return he
}

// classifyTransportError clasifica un error de client.Do. La cancelación del
// contexto padre no es un error del proveedor y se devuelve tal cual.
func classifyTransportError(ctx context.Context, err error) error {
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		return ctx.Err()
	}
	he := &HistoryError{Kind: HistoryErrNetwork, Retriable: true, Message: sanitizeMessage(err.Error())}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		he.Kind = HistoryErrTimeout
	}
	return he
}

// parseRetryAfter interpreta Retry-After en segundos o como fecha HTTP.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var d time.Duration
	if secs, err := strconv.Atoi(v); err == nil {
		d = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
	}
	if d < 0 {
		return 0
	}
	if d > MaxRetryAfter {
		return MaxRetryAfter
	}
	return d
}

// retryBackoff es el backoff exponencial acotado entre reintentos REST.
func retryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 5 {
		attempt = 5
	}
	d := time.Duration(1<<uint(attempt)) * 300 * time.Millisecond
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return d
}
