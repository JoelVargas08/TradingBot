package processor

import (
	"context"
	"errors"
	"log"
	"time"

	"tradingview-bot/internal/dedupe"
	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/ibeval"
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
	src        Source
	store      domain.SignalStore
	dedupe     *dedupe.Deduplicator
	eval       domain.Evaluator
	notify     domain.Notifier
	position   domain.PositionController
	session    SessionGate
	feed       FeedGate
	receipts   ibeval.ReceiptStore
	paperLease time.Duration
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

// SetIBReceipts activa la entrega duradera de señales IB: señal + recibo en
// una transacción, reclamación PAPER con lease y ack del Outbox al completar.
func (p *Processor) SetIBReceipts(r ibeval.ReceiptStore) {
	p.receipts = r
}

// SetIBPaperLease ajusta el lease con el que el processor reclama el recibo
// PAPER. Si no se configura, el store aplica su valor seguro por defecto.
func (p *Processor) SetIBPaperLease(d time.Duration) {
	p.paperLease = d
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
	if p.receipts != nil && ibeval.MetaString(ev.Meta, ibeval.MetaOutboxKey) != "" {
		p.handleIB(ctx, ev)
		return
	}
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

// handleIB procesa una señal entregada por el Outbox. Los efectos persistentes
// (señal, notificación y PAPER) se protegen con el recibo: una reentrega no
// crea una segunda señal, no repite la notificación y no vuelve a ejecutar
// PAPER si ya terminó. La notificación se marca antes de encolar para que una
// reentrega no la duplique; el Outbox conserva al menos una entrega.
func (p *Processor) handleIB(ctx context.Context, ev domain.SignalEvent) {
	now := time.Now().UTC()
	emit, err := p.eval.Evaluate(ctx, ev)
	if err != nil {
		log.Printf("error evaluando señal IB %s: %v", ev.Key(), err)
		return
	}
	receipt, err := p.receipts.SaveSignalWithReceipt(ctx, ev, emit, emit && p.position != nil, now)
	if err != nil {
		log.Printf("error guardando señal/recibo IB %s: %v", ev.Key(), err)
		return
	}
	outboxKey := receipt.OutboxKey
	if outboxKey == "" {
		outboxKey = ibeval.MetaString(ev.Meta, ibeval.MetaOutboxKey)
	}

	if receipt.ShouldNotify && receipt.NotifiedAt.IsZero() {
		// Marcar primero evita que un crash entre envío y marca duplique el
		// aviso en la siguiente entrega. La cola del notifier es asíncrona.
		if err := p.receipts.MarkReceiptNotified(ctx, receipt.SignalKey, now); err != nil {
			log.Printf("error marcando recibo notificado %s: %v", receipt.SignalKey, err)
			return
		}
		observability.Log(observability.SignalCreated, "strategy", ev.StrategyID, "symbol", ev.Symbol,
			"timeframe", ev.Timeframe, "direction", ev.Direction, "bar_ts", ev.BarTS.UnixMilli(), "price", ev.Price)
		if err := p.notify.Notify(ctx, ev); err != nil {
			log.Printf("error notificando señal IB %s: %v", ev.Key(), err)
			// El recibo ya está marcado; PAPER sigue siendo idempotente. No se
			// confirma el Outbox para que el fallo quede reintentable.
			return
		}
	}

	if isTerminalPaper(receipt.PaperStatus) {
		p.ack(ctx, outboxKey)
		return
	}
	if !receipt.ShouldNotify && !emit {
		// Sin cambio de dirección: no hay PAPER que ejecutar.
		_ = p.receipts.FinishReceiptPaper(ctx, receipt.SignalKey, ibeval.PaperSkipped, "sin_cambio_direccion", now)
		p.ack(ctx, outboxKey)
		return
	}
	if p.position == nil {
		_ = p.receipts.FinishReceiptPaper(ctx, receipt.SignalKey, ibeval.PaperSkipped, "paper_deshabilitado", now)
		p.ack(ctx, outboxKey)
		return
	}
	if p.session != nil && !p.session.IsActive() {
		log.Printf("sesión detenida: señal IB %s registrada pero no ejecutada", ev.Key())
		_ = p.receipts.FinishReceiptPaper(ctx, receipt.SignalKey, ibeval.PaperSkipped, "sesion_detenida", now)
		p.ack(ctx, outboxKey)
		return
	}
	if p.feed != nil && !p.feed.Healthy() {
		log.Printf("feed de mercado stale: señal IB %s registrada pero no ejecutada", ev.Key())
		_ = p.receipts.FinishReceiptPaper(ctx, receipt.SignalKey, ibeval.PaperSkipped, "feed_stale", now)
		p.ack(ctx, outboxKey)
		return
	}

	claimed, err := p.receipts.ClaimReceiptPaper(ctx, receipt.SignalKey, now, p.paperLease)
	if err != nil {
		log.Printf("error reclamando recibo PAPER %s: %v", receipt.SignalKey, err)
		return
	}
	if !claimed {
		// Puede ser terminal (ya hecho) o estar siendo procesado por otro lease.
		// Consultar de nuevo evita reintentos infinitos de un recibo terminado.
		current, rerr := p.receipts.SaveSignalWithReceipt(ctx, ev, false, false, now)
		if rerr == nil && isTerminalPaper(current.PaperStatus) {
			p.ack(ctx, outboxKey)
		}
		return
	}
	if err := p.position.OnSignal(ctx, ev); err != nil {
		log.Printf("error gestionando posición IB %s: %v", ev.Key(), err)
		_ = p.receipts.FinishReceiptPaper(ctx, receipt.SignalKey, ibeval.PaperError, err.Error(), now)
		return
	}
	if err := p.receipts.FinishReceiptPaper(ctx, receipt.SignalKey, ibeval.PaperDone, "", now); err != nil {
		log.Printf("error completando recibo PAPER %s: %v", receipt.SignalKey, err)
		return
	}
	p.ack(ctx, outboxKey)
}

func (p *Processor) ack(ctx context.Context, outboxKey string) {
	if outboxKey == "" {
		return
	}
	if err := p.receipts.AckOutbox(ctx, outboxKey, time.Now().UTC()); err != nil {
		log.Printf("error confirmando Outbox IB %s: %v", outboxKey, err)
	}
}

func isTerminalPaper(status string) bool {
	switch status {
	case ibeval.PaperDone, ibeval.PaperRejected, ibeval.PaperSkipped:
		return true
	}
	return false
}
