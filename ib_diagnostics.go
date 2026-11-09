package main

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"tradingview-bot/internal/store"
	"tradingview-bot/services"
)

func firstField(s string) string {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// ibCommandArgs interpreta [/ib... [strategy|evaluation] [limit]]. Si el primer
// argumento es numérico se conserva la estrategia por defecto y se usa como
// límite, de modo que /ibevals 10 funciona sin repetir el id.
func ibCommandArgs(args, fallback string, defaultLimit, maxLimit int) (string, int) {
	fields := strings.Fields(strings.TrimSpace(args))
	subject := strings.TrimSpace(fallback)
	limit := defaultLimit
	if len(fields) > 0 {
		if n, err := strconv.Atoi(fields[0]); err == nil {
			limit = n
		} else {
			subject = fields[0]
			if len(fields) > 1 {
				if n, err := strconv.Atoi(fields[1]); err == nil {
					limit = n
				}
			}
		}
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return subject, limit
}

func ibTime(t time.Time) string {
	if t.IsZero() || t.UnixMilli() == 0 {
		return "sin datos"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func ibShort(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func runIBStatus(ctx context.Context, st *store.Store, tg *services.TelegramService, chatID int64, strategy string) {
	sum, err := st.IBSummary(ctx, strategy)
	if err != nil {
		tg.SendMessage(chatID, "Error consultando persistencia IB: "+html.EscapeString(err.Error()))
		return
	}
	scope := "todas las estrategias"
	if strategy != "" {
		scope = strategy
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<b>Persistencia IB</b>\nEstrategia: %s\n\n", html.EscapeString(scope))
	fmt.Fprintf(&b, "Evaluaciones: %d | aceptadas %d | rechazadas %d | errores %d\n",
		sum.Evaluations, sum.Accepted, sum.Rejected, sum.Errors)
	fmt.Fprintf(&b, "Última evaluación: %s\nÚltima barra aceptada: %s\n\n",
		ibTime(sum.LastEvaluation), ibTime(sum.LastAcceptedBarTS))
	fmt.Fprintf(&b, "Outbox: pending %d | dispatched %d | acked %d | dead %d\n",
		sum.OutboxPending, sum.OutboxDispatched, sum.OutboxAcked, sum.OutboxDead)
	fmt.Fprintf(&b, "Recibos PAPER: pending %d | processing %d | done %d | skipped %d",
		sum.ReceiptsPending, sum.ReceiptsProcess, sum.ReceiptsDone, sum.ReceiptsSkipped)
	tg.SendMessage(chatID, b.String())
}

func runIBEvaluations(ctx context.Context, st *store.Store, tg *services.TelegramService, chatID int64, strategy string, limit int) {
	rows, err := st.RecentIBEvaluations(ctx, strategy, limit)
	if err != nil {
		tg.SendMessage(chatID, "Error consultando evaluaciones IB: "+html.EscapeString(err.Error()))
		return
	}
	if len(rows) == 0 {
		tg.SendMessage(chatID, "No hay evaluaciones IB registradas para ese filtro.")
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<b>Evaluaciones IB (%d)</b>\n", len(rows))
	for _, r := range rows {
		fmt.Fprintf(&b, "\n[%s] %s %s %s\n",
			html.EscapeString(r.Status), html.EscapeString(r.Symbol), html.EscapeString(r.Timeframe), ibTime(r.BarTS))
		fmt.Fprintf(&b, "  Estrategia: %s | modelo: %s\n",
			html.EscapeString(r.StrategyID), html.EscapeString(ibShort(r.ModelVersion, 24)))
		if r.Reason != "" {
			fmt.Fprintf(&b, "  Motivo: %s\n", html.EscapeString(ibShort(r.Reason, 160)))
		}
	}
	tg.SendMessage(chatID, strings.TrimRight(b.String(), "\n"))
}

func runIBOperations(ctx context.Context, st *store.Store, tg *services.TelegramService, chatID int64, subject string, limit int) {
	var (
		rows []ibevalPaperEvent
		err  error
	)
	if strings.Contains(subject, "|") {
		events, qerr := st.RecentIBPaperEvents(ctx, subject, limit)
		err = qerr
		for _, ev := range events {
			rows = append(rows, ibevalPaperEvent{ev.Kind, ev.EvaluationKey, ev.OrderClientID, ev.PositionID, ev.Price, ev.Quantity, ev.CreatedAt})
		}
	} else {
		events, qerr := st.RecentIBPaperEventsForStrategy(ctx, subject, limit)
		err = qerr
		for _, ev := range events {
			rows = append(rows, ibevalPaperEvent{ev.Kind, ev.EvaluationKey, ev.OrderClientID, ev.PositionID, ev.Price, ev.Quantity, ev.CreatedAt})
		}
	}
	if err != nil {
		tg.SendMessage(chatID, "Error consultando operaciones IB: "+html.EscapeString(err.Error()))
		return
	}
	if len(rows) == 0 {
		tg.SendMessage(chatID, "No hay eventos PAPER IB registrados para ese filtro.")
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<b>Operaciones PAPER IB (%d)</b>\n", len(rows))
	for _, ev := range rows {
		fmt.Fprintf(&b, "\n%s | %s\n", html.EscapeString(ev.kind), ibTime(ev.createdAt))
		if ev.orderClientID != "" {
			fmt.Fprintf(&b, "  Orden: %s\n", html.EscapeString(ev.orderClientID))
		}
		if ev.positionID > 0 {
			fmt.Fprintf(&b, "  Posición: %d\n", ev.positionID)
		}
		fmt.Fprintf(&b, "  Precio: %.8g | Cantidad: %.8g\n", ev.price, ev.quantity)
		fmt.Fprintf(&b, "  Evaluación: %s\n", html.EscapeString(ibShort(ev.evaluationKey, 72)))
	}
	tg.SendMessage(chatID, strings.TrimRight(b.String(), "\n"))
}

type ibevalPaperEvent struct {
	kind          string
	evaluationKey string
	orderClientID string
	positionID    int64
	price         float64
	quantity      float64
	createdAt     time.Time
}
