package jobqueue

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// Status representa el estado de un trabajo.
type Status string

const (
	StatusQueued   Status = "queued"
	StatusRunning  Status = "running"
	StatusDone     Status = "done"
	StatusFailed   Status = "failed"
	StatusCanceled Status = "canceled"
)

// Job es la unidad de trabajo. Payload es opaco y lo interpreta el handler.
type Job struct {
	ID         string
	Type       string
	Payload    any
	Status     Status
	Error      string
	Attempts   int
	CreatedAt  time.Time
	StartedAt  time.Time
	FinishedAt time.Time
}

// Result contiene el resultado de procesar un Job. Payload lo interpreta el
// llamador. En trabajos fallidos, Payload es el original del Job y Error el
// motivo del fallo.
type Result struct {
	JobID   string
	Payload any
	Error   string
}

// Handler procesa un trabajo. Un error de tipo retryableError se reencola
// con backoff; cualquier otro error es final.
type Handler interface {
	Handle(ctx context.Context, job Job) (any, error)
}

// HandlerFunc adapta una función al interface Handler.
type HandlerFunc func(ctx context.Context, job Job) (any, error)

func (f HandlerFunc) Handle(ctx context.Context, job Job) (any, error) {
	return f(ctx, job)
}

// retryableError marca un error como reintentable.
type retryableError struct {
	msg   string
	delay time.Duration
}

func (e *retryableError) Error() string { return e.msg }

// RetryableError envuelve un error para que el job queue lo reintente.
func RetryableError(err error, delay time.Duration) error {
	return &retryableError{msg: err.Error(), delay: delay}
}

func isRetryable(err error) (time.Duration, bool) {
	var re *retryableError
	if errors.As(err, &re) {
		return re.delay, true
	}
	return 0, false
}

// Config parametriza el Queue.
type Config struct {
	Workers     int
	QueueSize   int
	MaxAttempts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

func (c Config) withDefaults() Config {
	if c.Workers <= 0 {
		c.Workers = 2
	}
	if c.QueueSize <= 0 {
		c.QueueSize = 1024
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 3
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = 250 * time.Millisecond
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 30 * time.Second
	}
	return c
}

// Queue procesa trabajos con un pool de workers y reintentos con backoff.
type Queue struct {
	cfg     Config
	handler Handler
	queue   chan *Job

	mu     sync.RWMutex
	jobs   map[string]*Job
	onDone func(*Result)
	onFail func(*Result)

	inFlight atomic.Int64
	started  atomic.Bool
	closed   atomic.Bool
}

func New(cfg Config, h Handler) *Queue {
	cfg = cfg.withDefaults()
	return &Queue{
		cfg:     cfg,
		handler: h,
		queue:   make(chan *Job, cfg.QueueSize),
		jobs:    make(map[string]*Job),
	}
}

// OnDone registra un callback para trabajos terminados con éxito.
func (q *Queue) OnDone(fn func(*Result)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.onDone = fn
}

// OnFailed registra un callback para trabajos que fallan de forma definitiva
// (sin más reintentos). Payload contiene el original del Job y Error el motivo.
func (q *Queue) OnFailed(fn func(*Result)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.onFail = fn
}

// Enqueue añade un trabajo a la cola. Devuelve el ID generado.
func (q *Queue) Enqueue(ctx context.Context, jobType string, payload any) (string, error) {
	if q.handler == nil {
		return "", errors.New("jobqueue: sin handler registrado")
	}
	if q.closed.Load() {
		return "", errors.New("jobqueue: cola cerrada")
	}
	j := &Job{
		ID:        newID(),
		Type:      jobType,
		Payload:   payload,
		Status:    StatusQueued,
		CreatedAt: time.Now(),
	}
	q.mu.Lock()
	q.jobs[j.ID] = j
	q.mu.Unlock()

	select {
	case <-ctx.Done():
		q.setFailed(j, ctx.Err().Error())
		return j.ID, ctx.Err()
	case q.queue <- j:
		return j.ID, nil
	}
}

// Get devuelve una copia del estado de un trabajo.
func (q *Queue) Get(id string) (Job, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	j, ok := q.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

// List devuelve copias de todos los trabajos conocidos.
func (q *Queue) List() []Job {
	q.mu.RLock()
	defer q.mu.RUnlock()
	out := make([]Job, 0, len(q.jobs))
	for _, j := range q.jobs {
		out = append(out, *j)
	}
	return out
}

// Count devuelve el número de trabajos en un estado.
func (q *Queue) Count(status Status) int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	n := 0
	for _, j := range q.jobs {
		if j.Status == status {
			n++
		}
	}
	return n
}

// InFlight devuelve los trabajos ejecutándose ahora mismo.
func (q *Queue) InFlight() int64 {
	return q.inFlight.Load()
}

// Start arranca los workers. Idempotente.
func (q *Queue) Start(ctx context.Context) {
	if q.started.Swap(true) {
		return
	}
	for i := 0; i < q.cfg.Workers; i++ {
		go q.worker(ctx)
	}
}

func (q *Queue) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j, ok := <-q.queue:
			if !ok {
				return
			}
			q.process(ctx, j)
		}
	}
}

func (q *Queue) process(ctx context.Context, j *Job) {
	q.mu.Lock()
	if j.Status == StatusQueued {
		j.Status = StatusRunning
		j.StartedAt = time.Now()
		j.Attempts++
	}
	q.mu.Unlock()
	q.inFlight.Add(1)
	defer q.inFlight.Add(-1)

	payload, err := q.handler.Handle(ctx, *j)
	if err != nil {
		if delay, ok := isRetryable(err); ok && j.Attempts < q.cfg.MaxAttempts {
			q.requeueWithBackoff(ctx, j, delay)
			return
		}
		q.setFailed(j, err.Error())
		return
	}

	q.mu.Lock()
	j.Status = StatusDone
	j.FinishedAt = time.Now()
	onDone := q.onDone
	q.mu.Unlock()
	if onDone != nil {
		onDone(&Result{JobID: j.ID, Payload: payload})
	}
}

func (q *Queue) requeueWithBackoff(ctx context.Context, j *Job, delay time.Duration) {
	backoff := q.cfg.BaseBackoff << (j.Attempts - 1)
	if backoff > q.cfg.MaxBackoff {
		backoff = q.cfg.MaxBackoff
	}
	if delay > backoff {
		backoff = delay
	}
	if backoff > q.cfg.MaxBackoff {
		backoff = q.cfg.MaxBackoff
	}
	log.Printf("jobqueue: %s reintento %d/%d en %s", j.ID, j.Attempts, q.cfg.MaxAttempts, backoff)
	select {
	case <-ctx.Done():
		return
	case <-time.After(backoff):
	}
	q.mu.Lock()
	j.Status = StatusQueued
	j.StartedAt = time.Time{}
	q.mu.Unlock()
	select {
	case <-ctx.Done():
	case q.queue <- j:
	}
}

func (q *Queue) setFailed(j *Job, errMsg string) {
	q.mu.Lock()
	j.Status = StatusFailed
	j.Error = errMsg
	j.FinishedAt = time.Now()
	onFail := q.onFail
	payload := j.Payload
	q.mu.Unlock()
	log.Printf("jobqueue: %s falló: %s", j.ID, errMsg)
	if onFail != nil {
		onFail(&Result{JobID: j.ID, Payload: payload, Error: errMsg})
	}
}

var idCounter atomic.Int64

func newID() string {
	n := idCounter.Add(1)
	return fmt.Sprintf("job-%d-%d", time.Now().UnixMilli(), n)
}
