package telegram

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// fakeFetcher sirve lotes de mensajes predefinidos imitando el offset de
// Telegram: nunca devuelve un update con id menor que el offset solicitado.
type fakeFetcher struct {
	batches   [][]Message
	errs      []error
	calls     int
	offsets   []int
	cancel    context.CancelFunc
	stopAfter int
}

func (f *fakeFetcher) Fetch(_ context.Context, offset int, _ time.Duration) ([]Message, error) {
	f.offsets = append(f.offsets, offset)
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return nil, f.errs[i]
	}
	var batch []Message
	if i < len(f.batches) {
		for _, m := range f.batches[i] {
			if m.UpdateID >= offset {
				batch = append(batch, m)
			}
		}
	}
	if f.stopAfter > 0 && f.calls >= f.stopAfter && f.cancel != nil {
		f.cancel()
	}
	return batch, nil
}

type recordingHandler struct {
	handled  []int
	failFor  map[int]int
	failures int
}

func (h *recordingHandler) Handle(_ context.Context, m Message) error {
	h.handled = append(h.handled, m.UpdateID)
	if remaining, ok := h.failFor[m.UpdateID]; ok && remaining > 0 {
		h.failFor[m.UpdateID] = remaining - 1
		h.failures++
		return fmt.Errorf("fallo simulado en %d", m.UpdateID)
	}
	return nil
}

func newTestListener(t *testing.T, fetcher Fetcher, handler Handler, opts Options) (*Listener, *[]time.Duration) {
	t.Helper()
	delays := &[]time.Duration{}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	opts.Sleep = func(_ context.Context, d time.Duration) error {
		*delays = append(*delays, d)
		return nil
	}
	return New(fetcher, handler, opts), delays
}

func TestListenerAdvancesOffsetOnlyAfterSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fetcher := &fakeFetcher{
		batches:   [][]Message{{{UpdateID: 10}, {UpdateID: 11}}},
		cancel:    cancel,
		stopAfter: 1,
	}
	handler := &recordingHandler{}
	listener, _ := newTestListener(t, fetcher, handler, Options{})

	if err := listener.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run devolvió error inesperado: %v", err)
	}
	if got := strings.Join(intsToStrings(handler.handled), ","); got != "10,11" {
		t.Fatalf("mensajes gestionados = %s, se esperaba 10,11", got)
	}
	if listener.Offset() != 12 {
		t.Fatalf("offset = %d, se esperaba 12", listener.Offset())
	}
}

func TestListenerRetriesFailedMessageWithoutLosingIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	batch := []Message{{UpdateID: 42}}
	fetcher := &fakeFetcher{
		batches:   [][]Message{batch, batch},
		cancel:    cancel,
		stopAfter: 2,
	}
	handler := &recordingHandler{failFor: map[int]int{42: 1}}
	listener, _ := newTestListener(t, fetcher, handler, Options{})

	if err := listener.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run devolvió error inesperado: %v", err)
	}
	if len(handler.handled) != 2 {
		t.Fatalf("intentos del handler = %d, se esperaba 2", len(handler.handled))
	}
	if listener.Offset() != 43 {
		t.Fatalf("offset = %d, se esperaba 43", listener.Offset())
	}
	// El segundo fetch debe pedir el mismo offset: el mensaje fallido no se perdió.
	if len(fetcher.offsets) < 2 || fetcher.offsets[1] != 0 {
		t.Fatalf("offsets solicitados = %v, se esperaba [0 0 ...]", fetcher.offsets)
	}
}

func TestListenerDiscardsPoisonMessageAfterMaxAttempts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	batch := []Message{{UpdateID: 7}}
	fetcher := &fakeFetcher{
		batches:   [][]Message{batch, batch, batch},
		cancel:    cancel,
		stopAfter: 3,
	}
	handler := &recordingHandler{failFor: map[int]int{7: 10}}
	listener, _ := newTestListener(t, fetcher, handler, Options{MaxAttempts: 2})

	if err := listener.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run devolvió error inesperado: %v", err)
	}
	if handler.failures != 2 {
		t.Fatalf("fallos registrados = %d, se esperaba 2", handler.failures)
	}
	if listener.Offset() != 8 {
		t.Fatalf("offset = %d, se esperaba 8 (mensaje descartado)", listener.Offset())
	}
}

func TestListenerDoesNotRepeatAlreadyHandledMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	batch := []Message{{UpdateID: 1}, {UpdateID: 2}}
	fetcher := &fakeFetcher{
		batches:   [][]Message{batch, batch, batch},
		cancel:    cancel,
		stopAfter: 3,
	}
	// El segundo mensaje falla siempre: el primero no debe repetirse.
	handler := &recordingHandler{failFor: map[int]int{2: 10}}
	listener, _ := newTestListener(t, fetcher, handler, Options{MaxAttempts: 5})

	if err := listener.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run devolvió error inesperado: %v", err)
	}
	counts := map[int]int{}
	for _, id := range handler.handled {
		counts[id]++
	}
	if counts[1] != 1 {
		t.Fatalf("update 1 gestionado %d veces, se esperaba 1", counts[1])
	}
	if counts[2] < 2 {
		t.Fatalf("update 2 gestionado %d veces, se esperaba reintentos", counts[2])
	}
}

func TestListenerRetriesConflict(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fetcher := &fakeFetcher{
		batches:   [][]Message{nil, {{UpdateID: 5}}},
		errs:      []error{&tgbotapi.Error{Code: 409, Message: "Conflict"}},
		cancel:    cancel,
		stopAfter: 2,
	}
	handler := &recordingHandler{}
	listener, delays := newTestListener(t, fetcher, handler, Options{})

	if err := listener.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run devolvió error inesperado: %v", err)
	}
	if len(handler.handled) != 1 || handler.handled[0] != 5 {
		t.Fatalf("mensajes gestionados = %v, se esperaba [5]", handler.handled)
	}
	if len(*delays) == 0 {
		t.Fatal("se esperaba un retardo tras el conflicto")
	}
}

func TestListenerFailsOnAuthError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fetcher := &fakeFetcher{
		errs:   []error{&tgbotapi.Error{Code: 401, Message: "Unauthorized"}},
		cancel: cancel,
	}
	handler := &recordingHandler{}
	listener, _ := newTestListener(t, fetcher, handler, Options{})

	err := listener.Run(ctx)
	if err == nil {
		t.Fatal("se esperaba error fatal por token inválido")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Fatalf("error inesperado: %v", err)
	}
}

func TestListenerRetriesNetworkError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fetcher := &fakeFetcher{
		batches:   [][]Message{nil, {{UpdateID: 9}}},
		errs:      []error{timeoutError{}},
		cancel:    cancel,
		stopAfter: 2,
	}
	handler := &recordingHandler{}
	listener, _ := newTestListener(t, fetcher, handler, Options{})

	if err := listener.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run devolvió error inesperado: %v", err)
	}
	if len(handler.handled) != 1 {
		t.Fatalf("mensajes gestionados = %v, se esperaba 1", handler.handled)
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	listener, _ := newTestListener(t, &fakeFetcher{}, &recordingHandler{}, Options{
		BaseBackoff: time.Second,
		MaxBackoff:  8 * time.Second,
	})
	for failures := 0; failures < 6; failures++ {
		listener.failures = failures
		delay := listener.backoff()
		if delay < time.Second {
			t.Fatalf("backoff = %s, se esperaba al menos 1s", delay)
		}
		if delay > 12*time.Second {
			t.Fatalf("backoff = %s, excede el máximo permitido", delay)
		}
	}
}

func TestClasificar(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorKind
	}{
		{"unauthorized", &tgbotapi.Error{Code: 401}, KindAuth},
		{"forbidden", &tgbotapi.Error{Code: 403}, KindAuth},
		{"conflict", &tgbotapi.Error{Code: 409}, KindConflict},
		{"too many requests", &tgbotapi.Error{Code: 429}, KindRateLimit},
		{"server error", &tgbotapi.Error{Code: 502}, KindServer},
		{"timeout", timeoutError{}, KindNetwork},
		{"unknown", errors.New("boom"), KindUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Clasificar(tc.err)
			if got.Kind != tc.want {
				t.Fatalf("kind = %s, se esperaba %s", got.Kind, tc.want)
			}
		})
	}
}

func TestClasificarKeepsRetryAfter(t *testing.T) {
	err := &tgbotapi.Error{Code: 429}
	err.ResponseParameters.RetryAfter = 17
	apiErr := Clasificar(err)
	if apiErr.RetryAfter != 17*time.Second {
		t.Fatalf("retry_after = %s, se esperaba 17s", apiErr.RetryAfter)
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

var _ net.Error = timeoutError{}

func intsToStrings(values []int) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, fmt.Sprintf("%d", v))
	}
	return out
}
