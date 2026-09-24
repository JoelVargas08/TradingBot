package processor

import (
	"context"
	"errors"
	"log"

	"tradingview-bot/internal/dedupe"
	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/observability"
)

// SessionGate permite al processor consultar si la sesión de trading activa.
type SessionGate interface {
	IsActive() bool
}

// FeedGate permite al processor bloquear la ejecución de nuevas posiciones
// cuando el feed de mercado está stale (datos viejos o no confiables).
type FeedGate interface {
	Healthy() bool
}

type Source interface {
	Subscribe() <-chan domain.SignalEvent
}

type Processor struct {
	src      Source
	store    domain.SignalStore
	dedupe   *dedupe.Deduplicator
	eval     domain.Evaluator
	notify   domain.Notifier
	position domain.PositionController
	session  SessionGate
	feed     FeedGate
}

func New(src Source, store domain.SignalStore, eval domain.Evaluator, notify domain.Notifier, position domain.PositionController, session SessionGate, dedupeLimit int) *Processor {
	if dedupeLimit <= 0 {
		dedupeLimit = 10000
	}
	return &Processor{
		src:      src,
		store:    store,
		dedupe:   dedupe.New(dedupeLimit),
		eval:     eval,
		notify:   notify,
		position: position,
		session:  session,
	}
}

// SetFeedGate habilita el cortafuegos por feed stale: con el mercado sin
// datos frescos las señales se registran pero no abren posiciones nuevas.
func (p *Processor) SetFeedGate(g FeedGate) {
	p.feed = g
}

func (p *Processor) Start(ctx context.Context) {
	go p.run(ctx)
}

func (p *Processor) run(ctx context.Context) {
	ch := p.src.Subscribe()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			p.handle(ctx, ev)
		}
	}
}

func (p *Processor) handle(ctx context.Context, ev domain.SignalEvent) {
	if p.dedupe.Seen(ev.BarKey()) {
		log.Printf("señal duplicada por barra ignorada: %s", ev.BarKey())
		return
	}
	emit, err := p.eval.Evaluate(ctx, ev)
	if err != nil {
		log.Printf("error evaluando señal %s: %v", ev.Key(), err)
		return
	}
	if err := p.store.SaveSignal(ctx, ev); err != nil {
		if errors.Is(err, domain.ErrDuplicate) {
			log.Printf("señal ya persistida, se omite notificación: %s", ev.BarKey())
			return
		}
		log.Printf("error guardando señal %s: %v", ev.Key(), err)
	}
	if !emit {
		log.Printf("sin cambio de dirección (%s → %s), sin notificar", ev.Key(), ev.Direction)
		return
	}
	observability.Log(observability.SignalCreated, "strategy", ev.StrategyID, "symbol", ev.Symbol, "timeframe", ev.Timeframe, "direction", ev.Direction, "bar_ts", ev.BarTS.UnixMilli(), "price", ev.Price)
	if err := p.notify.Notify(ctx, ev); err != nil {
		log.Printf("error notificando señal %s: %v", ev.Key(), err)
	}
	if p.position == nil {
		return
	}
	if p.session != nil && !p.session.IsActive() {
		log.Printf(
			"sesión detenida: señal %s registrada pero no ejecutada",
			ev.Key(),
		)
		return
	}
	if p.feed != nil && !p.feed.Healthy() {
		log.Printf(
			"feed de mercado stale: señal %s registrada pero no ejecutada",
			ev.Key(),
		)
		return
	}
	if err := p.position.OnSignal(ctx, ev); err != nil {
		log.Printf(
			"error gestionando posición %s: %v",
			ev.Key(),
			err,
		)
	}
}
