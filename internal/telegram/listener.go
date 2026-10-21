// Package telegram encapsula el consumo de updates de Telegram con long
// polling. Aísla el transporte (Telegram) del despacho de comandos, de modo
// que el listener pueda probarse sin red y sin bot real.
//
// Garantías del listener:
//   - el offset solo avanza cuando el mensaje se ha gestionado con éxito;
//   - un mensaje fallido se reintenta y nunca se pierde;
//   - un mensaje que falla demasiadas veces se descarta (cola bloqueada);
//   - los errores de Telegram se clasifican (conflicto, autenticación, límite
//     de tasa, servidor, red) y se reintentan con backoff exponencial.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Message es la versión normalizada de un update de Telegram que consume el
// despachador de la aplicación.
type Message struct {
	UpdateID  int
	ChatID    int64
	Text      string
	IsCommand bool
	// Command es el comando sin la barra inicial y en minúsculas.
	Command string
	// Arguments son los argumentos del comando ya normalizados.
	Arguments  string
	DocumentID string
	Caption    string
}

// Fetcher obtiene los mensajes nuevos a partir de un offset. offset == 0
// significa "entrega desde el primer update pendiente".
type Fetcher interface {
	Fetch(ctx context.Context, offset int, timeout time.Duration) ([]Message, error)
}

// Handler gestiona un mensaje. Si devuelve error, el listener no avanza el
// offset: el mensaje volverá a entregarse en el siguiente ciclo.
type Handler interface {
	Handle(ctx context.Context, m Message) error
}

// HandlerFunc adapta una función a la interfaz Handler.
type HandlerFunc func(ctx context.Context, m Message) error

// Handle implementa Handler.
func (f HandlerFunc) Handle(ctx context.Context, m Message) error { return f(ctx, m) }

// BotFetcher obtiene los mensajes usando la API de Telegram.
type BotFetcher struct {
	bot *tgbotapi.BotAPI
}

// NewBotFetcher crea un BotFetcher sobre un bot ya inicializado.
func NewBotFetcher(bot *tgbotapi.BotAPI) *BotFetcher {
	return &BotFetcher{bot: bot}
}

// Fetch consulta getUpdates y normaliza los mensajes.
func (f *BotFetcher) Fetch(_ context.Context, offset int, timeout time.Duration) ([]Message, error) {
	seconds := int(timeout.Seconds())
	if seconds < 0 {
		seconds = 0
	}
	updates, err := f.bot.GetUpdates(tgbotapi.UpdateConfig{
		Offset:  offset,
		Timeout: seconds,
	})
	if err != nil {
		return nil, err
	}
	messages := make([]Message, 0, len(updates))
	for _, u := range updates {
		if u.Message == nil {
			continue
		}
		m := Message{
			UpdateID: u.UpdateID,
			ChatID:   u.Message.Chat.ID,
			Text:     u.Message.Text,
		}
		if u.Message.Document != nil {
			m.DocumentID = u.Message.Document.FileID
			m.Caption = u.Message.Caption
		}
		if u.Message.IsCommand() {
			m.IsCommand = true
			m.Command = strings.ToLower(u.Message.Command())
			m.Arguments = strings.TrimSpace(u.Message.CommandArguments())
		}
		messages = append(messages, m)
	}
	return messages, nil
}

// ErrorKind clasifica los fallos devueltos por la API de Telegram.
type ErrorKind int

const (
	// KindUnknown agrupa errores no clasificables (tratables como transitorios).
	KindUnknown ErrorKind = iota
	// KindConflict es el HTTP 409: otro consumidor está usando getUpdates.
	KindConflict
	// KindAuth es el HTTP 401/403: el token es inválido o no tiene permisos.
	KindAuth
	// KindRateLimit es el HTTP 429: límite de tasa de Telegram.
	KindRateLimit
	// KindServer es cualquier HTTP 5xx.
	KindServer
	// KindNetwork son timeouts, EOF y fallos de conexión.
	KindNetwork
)

// String devuelve el nombre legible del tipo de error.
func (k ErrorKind) String() string {
	switch k {
	case KindConflict:
		return "conflicto"
	case KindAuth:
		return "autenticación"
	case KindRateLimit:
		return "límite de tasa"
	case KindServer:
		return "servidor"
	case KindNetwork:
		return "red"
	default:
		return "desconocido"
	}
}

// APIError es un error de Telegram ya clasificado.
type APIError struct {
	Kind       ErrorKind
	Status     int
	RetryAfter time.Duration
	Err        error
}

// Error implementa error.
func (e *APIError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("telegram %s (HTTP %d): %v", e.Kind, e.Status, e.Err)
	}
	return fmt.Sprintf("telegram %s: %v", e.Kind, e.Err)
}

// Unwrap expone el error original.
func (e *APIError) Unwrap() error { return e.Err }

// Clasificar convierte un error de Telegram en un APIError con su tipo. Un
// error no clasificable se trata como transitorio (KindUnknown) para no
// tumbar el bot por un fallo puntual.
func Clasificar(err error) *APIError {
	if err == nil {
		return nil
	}
	var apiErr *tgbotapi.Error
	if errors.As(err, &apiErr) {
		kind := KindUnknown
		switch {
		case apiErr.Code == http.StatusConflict:
			kind = KindConflict
		case apiErr.Code == http.StatusUnauthorized, apiErr.Code == http.StatusForbidden:
			kind = KindAuth
		case apiErr.Code == http.StatusTooManyRequests:
			kind = KindRateLimit
		case apiErr.Code >= 500:
			kind = KindServer
		}
		return &APIError{
			Kind:       kind,
			Status:     apiErr.Code,
			RetryAfter: time.Duration(apiErr.ResponseParameters.RetryAfter) * time.Second,
			Err:        err,
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return &APIError{Kind: KindNetwork, Err: err}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return &APIError{Kind: KindNetwork, Err: err}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &APIError{Kind: KindNetwork, Err: err}
	}
	return &APIError{Kind: KindUnknown, Err: err}
}

// Options configura el listener.
type Options struct {
	// Offset inicial (0 = desde el primer update pendiente).
	Offset int
	// PollTimeout es el long poll de Telegram.
	PollTimeout time.Duration
	// BaseBackoff es el primer retardo ante un fallo transitorio.
	BaseBackoff time.Duration
	// MaxBackoff acota el retardo máximo.
	MaxBackoff time.Duration
	// MaxAttempts es el número de intentos por mensaje antes de descartarlo.
	MaxAttempts int
	// Logf escribe los mensajes de diagnóstico.
	Logf func(format string, args ...any)
	// Sleep permite sustituir la espera (tests).
	Sleep func(ctx context.Context, d time.Duration) error
}

// Listener consume los mensajes de Telegram y los entrega al Handler.
type Listener struct {
	fetcher Fetcher
	handler Handler
	opts    Options

	offset   int
	failures int
	handled  map[int]bool
	attempts map[int]int
}

// New crea un Listener.
func New(fetcher Fetcher, handler Handler, opts Options) *Listener {
	if opts.PollTimeout <= 0 {
		opts.PollTimeout = 30 * time.Second
	}
	if opts.BaseBackoff <= 0 {
		opts.BaseBackoff = time.Second
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 30 * time.Second
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 3
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	if opts.Sleep == nil {
		opts.Sleep = sleepCtx
	}
	return &Listener{
		fetcher:  fetcher,
		handler:  handler,
		opts:     opts,
		offset:   opts.Offset,
		handled:  make(map[int]bool),
		attempts: make(map[int]int),
	}
}

// Offset devuelve el siguiente update que se solicitará a Telegram.
func (l *Listener) Offset() int { return l.offset }

// Run consume mensajes hasta que se cancela el contexto. Devuelve error solo
// ante fallos irrecuperables (token inválido); los errores transitorios se
// reintentan con backoff.
func (l *Listener) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		messages, err := l.fetcher.Fetch(ctx, l.offset, l.opts.PollTimeout)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			apiErr := Clasificar(err)
			if apiErr.Kind == KindAuth {
				return fmt.Errorf("telegram: token no válido o sin permisos: %w", apiErr)
			}
			delay := l.backoff()
			l.opts.Logf("telegram: getUpdates falló (%s): %v; reintento en %s", apiErr.Kind, err, delay)
			if sleepErr := l.opts.Sleep(ctx, delay); sleepErr != nil {
				return sleepErr
			}
			l.failures++
			continue
		}
		l.failures = 0
		if len(messages) == 0 {
			continue
		}
		if err := l.process(ctx, messages); err != nil {
			return err
		}
	}
}

// process entrega los mensajes al handler. El offset solo avanza con mensajes
// gestionados correctamente.
func (l *Listener) process(ctx context.Context, messages []Message) error {
	for _, m := range messages {
		if l.handled[m.UpdateID] {
			// Ya gestionado en un ciclo anterior: no se repite la acción.
			continue
		}
		if err := l.handler.Handle(ctx, m); err != nil {
			l.attempts[m.UpdateID]++
			attempts := l.attempts[m.UpdateID]
			if attempts >= l.opts.MaxAttempts {
				l.opts.Logf(
					"telegram: update %d descartado tras %d intentos: %v",
					m.UpdateID, attempts, err,
				)
				delete(l.attempts, m.UpdateID)
				delete(l.handled, m.UpdateID)
				l.offset = m.UpdateID + 1
				l.prune()
				continue
			}
			l.opts.Logf(
				"telegram: update %d falló (%d/%d): %v; se reintentará",
				m.UpdateID, attempts, l.opts.MaxAttempts, err,
			)
			// El offset no avanza: Telegram volverá a entregar este mensaje.
			return nil
		}
		delete(l.attempts, m.UpdateID)
		l.handled[m.UpdateID] = true
		l.offset = m.UpdateID + 1
	}
	l.prune()
	return nil
}

// prune descarta los mensajes ya confirmados del registro de gestionados.
func (l *Listener) prune() {
	for id := range l.handled {
		if id < l.offset {
			delete(l.handled, id)
		}
	}
}

// backoff calcula el retardo del próximo reintento: exponencial con jitter.
func (l *Listener) backoff() time.Duration {
	delay := l.opts.BaseBackoff
	for i := 0; i < l.failures && delay < l.opts.MaxBackoff; i++ {
		delay *= 2
	}
	if delay > l.opts.MaxBackoff {
		delay = l.opts.MaxBackoff
	}
	jitter := time.Duration(rand.Int64N(int64(delay/2) + 1))
	return delay + jitter
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
