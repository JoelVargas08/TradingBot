package jobqueue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitDone(t *testing.T, q *Queue, id string, timeout time.Duration) Job {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		j, ok := q.Get(id)
		if !ok {
			t.Fatalf("trabajo %s no existe", id)
		}
		if j.Status == StatusDone || j.Status == StatusFailed {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("trabajo %s no terminó en %s", id, timeout)
	return Job{}
}

func TestEnqueueAndComplete(t *testing.T) {
	q := New(Config{}, HandlerFunc(func(ctx context.Context, j Job) (any, error) {
		return fmt.Sprintf("ok-%s", j.Type), nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	var got atomic.Value
	q.OnDone(func(r *Result) {
		got.Store(r.Payload)
	})

	id, err := q.Enqueue(ctx, "test", map[string]string{"a": "1"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	j := waitDone(t, q, id, 2*time.Second)
	if j.Status != StatusDone {
		t.Fatalf("status = %v, want done", j.Status)
	}
	if j.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", j.Attempts)
	}
	if v := got.Load(); v != "ok-test" {
		t.Errorf("onDone payload = %v, want ok-test", v)
	}
}

func TestRetryThenSuccess(t *testing.T) {
	var calls atomic.Int32
	q := New(Config{MaxAttempts: 3, BaseBackoff: 5 * time.Millisecond}, HandlerFunc(func(ctx context.Context, j Job) (any, error) {
		if calls.Add(1) == 1 {
			return nil, RetryableError(errors.New("transitorio"), 10*time.Millisecond)
		}
		return "listo", nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	id, err := q.Enqueue(ctx, "retry", nil)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	j := waitDone(t, q, id, 2*time.Second)
	if j.Status != StatusDone {
		t.Fatalf("status = %v, want done", j.Status)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

func TestPermanentFailure(t *testing.T) {
	q := New(Config{MaxAttempts: 2, BaseBackoff: time.Millisecond}, HandlerFunc(func(ctx context.Context, j Job) (any, error) {
		return nil, errors.New("error fatal")
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	id, err := q.Enqueue(ctx, "fail", nil)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	j := waitDone(t, q, id, 2*time.Second)
	if j.Status != StatusFailed {
		t.Fatalf("status = %v, want failed", j.Status)
	}
	if j.Error != "error fatal" {
		t.Errorf("error = %q", j.Error)
	}
}

func TestMaxAttemptsExhausted(t *testing.T) {
	var calls atomic.Int32
	q := New(Config{MaxAttempts: 3, BaseBackoff: time.Millisecond}, HandlerFunc(func(ctx context.Context, j Job) (any, error) {
		calls.Add(1)
		return nil, RetryableError(errors.New("siempre falla"), time.Millisecond)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	id, err := q.Enqueue(ctx, "exhaust", nil)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	j := waitDone(t, q, id, 2*time.Second)
	if j.Status != StatusFailed {
		t.Fatalf("status = %v, want failed", j.Status)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestPermanentFailureNotifiesOnFailed(t *testing.T) {
	payload := map[string]string{"chat": "42", "name": "estrat"}
	q := New(Config{MaxAttempts: 2, BaseBackoff: time.Millisecond}, HandlerFunc(func(ctx context.Context, j Job) (any, error) {
		return nil, RetryableError(errors.New("fallo final"), time.Millisecond)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	var got atomic.Value
	q.OnFailed(func(r *Result) {
		got.Store(*r)
	})

	id, err := q.Enqueue(ctx, "fail", payload)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitDone(t, q, id, 2*time.Second)

	r, ok := got.Load().(Result)
	if !ok {
		t.Fatal("OnFailed no fue invocado")
	}
	if r.JobID != id {
		t.Errorf("JobID = %s, want %s", r.JobID, id)
	}
	if r.Error != "fallo final" {
		t.Errorf("Error = %q, want fallo final", r.Error)
	}
	gotPayload, ok := r.Payload.(map[string]string)
	if !ok || gotPayload["name"] != "estrat" {
		t.Errorf("Payload = %v, want el original del job", r.Payload)
	}
}

func TestConcurrentEnqueues(t *testing.T) {
	const n = 50
	var mu sync.Mutex
	var processed []string
	q := New(Config{Workers: 4}, HandlerFunc(func(ctx context.Context, j Job) (any, error) {
		mu.Lock()
		processed = append(processed, j.Type)
		mu.Unlock()
		return nil, nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := q.Enqueue(ctx, fmt.Sprintf("job-%d", i), nil); err != nil {
				t.Errorf("Enqueue: %v", err)
			}
		}(i)
	}
	wg.Wait()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(processed) == n {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(processed) != n {
		t.Errorf("procesados = %d, want %d", len(processed), n)
	}
	if q.Count(StatusDone) != n {
		t.Errorf("done = %d, want %d", q.Count(StatusDone), n)
	}
}

func TestCanceledEnqueueFails(t *testing.T) {
	q := New(Config{QueueSize: 1}, HandlerFunc(func(ctx context.Context, j Job) (any, error) {
		time.Sleep(200 * time.Millisecond)
		return nil, nil
	}))
	// Sin workers, el primer trabajo llena el buffer y el segundo bloquea.
	blocked, err := q.Enqueue(context.Background(), "x", nil)
	if err != nil {
		t.Fatalf("primer enqueue: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	id, err := q.Enqueue(ctx, "y", nil)
	if err == nil {
		t.Fatal("Enqueue debería fallar con ctx cancelado y buffer lleno")
	}
	if id == blocked {
		t.Error("el id del job cancelado no debería coincidir con el primero")
	}
	if j, ok := q.Get(id); ok && j.Status != StatusFailed {
		t.Errorf("job debería quedar failed, got %v", j.Status)
	}
}
