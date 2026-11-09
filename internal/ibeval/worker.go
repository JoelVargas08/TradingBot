package ibeval

import (
	"context"
	"fmt"
	"log"
	"time"
)

// WorkerConfig parametriza el Outbox. La entrega es al menos una vez: si el
// proceso cae después de publicar y antes de confirmar, el lease expira y la
// señal se vuelve a publicar. El processor desduplica por recibo persistente.
type WorkerConfig struct {
	Lease        time.Duration
	Visibility   time.Duration
	MaxAttempts  int
	BaseBackoff  time.Duration
	MaxBackoff   time.Duration
	BatchSize    int
	PollInterval time.Duration
}

func (c WorkerConfig) withDefaults() WorkerConfig {
	if c.Lease <= 0 {
		c.Lease = 30 * time.Second
	}
	if c.Visibility <= 0 {
		c.Visibility = c.Lease
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 10
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 5 * time.Minute
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 20
	}
	if c.PollInterval <= 0 {
		c.PollInterval = time.Second
	}
	return c
}

// OutboxWorker reclama mensajes del Outbox, los publica y confirma/intenta de
// nuevo con backoff. No conoce el bus concreto.
type OutboxWorker struct {
	store     OutboxStore
	publisher Publisher
	cfg       WorkerConfig
	now       func() time.Time
}

func NewOutboxWorker(store OutboxStore, publisher Publisher, cfg WorkerConfig) *OutboxWorker {
	return &OutboxWorker{store: store, publisher: publisher, cfg: cfg.withDefaults(), now: time.Now}
}

// Run procesa el Outbox hasta que se cancele el contexto.
func (w *OutboxWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if _, err := w.Drain(ctx); err != nil && ctx.Err() == nil {
			log.Printf("outbox IB: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Drain reclama y publica hasta BatchSize mensajes disponibles. Devuelve cuántos
// se publicaron correctamente.
func (w *OutboxWorker) Drain(ctx context.Context) (int, error) {
	now := w.now()
	items, err := w.store.ClaimOutbox(ctx, now, w.cfg.Lease, w.cfg.BatchSize)
	if err != nil {
		return 0, fmt.Errorf("reclamando Outbox: %w", err)
	}
	published := 0
	for _, item := range items {
		if ctx.Err() != nil {
			return published, ctx.Err()
		}
		ev, err := DecodeSignal(item.Payload)
		if err != nil {
			// Un payload corrupto no se puede reintentar para siempre: muere y
			// queda visible para auditoría.
			if uerr := w.store.UpdateOutbox(ctx, OutboxUpdate{
				Key: item.Key, Status: OutboxDead, Attempts: item.Attempts,
				LastError: err.Error(), AvailableAt: now,
			}); uerr != nil {
				return published, uerr
			}
			continue
		}
		if err := w.publisher.Publish(ctx, ev); err != nil {
			attempts := item.Attempts
			status := OutboxPending
			if attempts >= w.cfg.MaxAttempts {
				status = OutboxDead
			}
			if uerr := w.store.UpdateOutbox(ctx, OutboxUpdate{
				Key:         item.Key,
				Status:      status,
				Attempts:    attempts,
				LastError:   err.Error(),
				AvailableAt: w.now().Add(w.backoff(attempts)),
			}); uerr != nil {
				return published, fmt.Errorf("registrando fallo de Outbox %s: %w", item.Key, uerr)
			}
			continue
		}
		// Tras publicar, el mensaje permanece 'dispatched' y visible solo al
		// vencer la visibilidad; si el processor no confirma (crash, reinicio),
		// se reintenta. Un ack del processor lo pasa a 'acked' y deja de salir.
		visible := w.now().Add(w.cfg.Visibility)
		if err := w.store.UpdateOutbox(ctx, OutboxUpdate{
			Key: item.Key, Status: OutboxDispatched, Attempts: item.Attempts,
			AvailableAt: visible, LeaseUntil: time.Time{},
		}); err != nil {
			return published, fmt.Errorf("confirmando Outbox %s: %w", item.Key, err)
		}
		published++
	}
	return published, nil
}

func (w *OutboxWorker) backoff(attempts int) time.Duration {
	d := w.cfg.BaseBackoff
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= w.cfg.MaxBackoff {
			return w.cfg.MaxBackoff
		}
	}
	if d > w.cfg.MaxBackoff {
		d = w.cfg.MaxBackoff
	}
	return d
}
