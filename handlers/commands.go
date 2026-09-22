package handlers

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/models"
	"tradingview-bot/services"
)

// SessionController expone el control de sesión de trading al handler.
type SessionController interface {
	Start()
	Stop()
	IsActive() bool
	StartedAt() time.Time
	StatusText() string
	ScheduleText() string
	TimezoneName() string
}

// PerformanceProvider ofrece métricas de paper trading al handler.
type PerformanceProvider interface {
	Performance(ctx context.Context) (domain.Performance, error)
}

type CommandsHandler struct {
	userManager *models.UserManager
	telegram    *services.TelegramService
	positions   domain.PositionStore
	session     SessionController
	performance PerformanceProvider
}

func NewCommandsHandler(um *models.UserManager, tg *services.TelegramService, positions domain.PositionStore, session SessionController, performance PerformanceProvider) *CommandsHandler {
	return &CommandsHandler{
		userManager: um,
		telegram:    tg,
		positions:   positions,
		session:     session,
		performance: performance,
	}
}
func (ch *CommandsHandler) HandleStart(chatID int64, args string) {
	parts := strings.Fields(args)
	if len(parts) < 1 {
		ch.telegram.SendMessage(chatID,
			"uso: /start &lt;tu_chat_id&gt;\n\n "+
				"Para obtener tu chat_id, envía /myid al bot @userinfobot")
		return
	}
	targetChatID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || targetChatID == 0 {
		ch.telegram.SendMessage(
			chatID,
			"❌ Chat ID inválido\n\nEjemplo:\n/start 123456789",
		)
		return
	}
	ch.userManager.Activate(targetChatID)
	ch.telegram.SendMessage(targetChatID,
		"✅ <b>Alertas activadas</b>\n\n"+
			"Recibirás notificaciones cuando el indicador Chandelier Exit cambie de señal.\n\n"+
			"Para desactivar: /close")

	if targetChatID != chatID {
		ch.telegram.SendMessage(chatID, fmt.Sprintf("✅ Alertas activadas para chat ID: %d", targetChatID))
	}
}
func (ch *CommandsHandler) HandleClose(chatID int64) {
	ch.userManager.Deactivate(chatID)
	ch.telegram.SendMessage(chatID,
		"❌ <b>Alertas desactivadas</b>\n\n"+
			"No recibirás más notificaciones.\n"+
			"Para reactivar: /start &lt;tu_chat_id&gt;")
}
func (ch *CommandsHandler) HandleMyID(chatID int64) {
	ch.telegram.SendMessage(chatID, fmt.Sprintf("Tu chat ID es: <code>%d</code>", chatID))
}
func (ch *CommandsHandler) HandlePing(chatID int64) {
	ch.telegram.SendMessage(
		chatID,
		"🏓 <b>Pong!</b>\nBot funcionando correctamente.",
	)
}
func (ch *CommandsHandler) HandleSession(chatID int64) {
	if ch.session == nil {
		ch.telegram.SendMessage(
			chatID,
			"❌ Control de sesión no disponible",
		)
		return
	}
	if ch.session.IsActive() {
		started := ch.session.StartedAt()
		startText := "-"
		if !started.IsZero() {
			startText = started.Format("2006-01-02 15:04:05")
		}
		ch.telegram.SendMessage(
			chatID,
			fmt.Sprintf(
				"🟢 <b>Sesión de trading ACTIVA</b>\n\n"+
					"Iniciada: %s\n"+
					"Nuevas operaciones: habilitadas",
				startText,
			),
		)
		return
	}
	ch.telegram.SendMessage(
		chatID,
		"🔴 <b>Sesión de trading DETENIDA</b>\n\n"+
			"Nuevas operaciones: deshabilitadas\n"+
			"Las señales continúan registrándose.",
	)
}
func (ch *CommandsHandler) HandleSessionStart(chatID int64) {
	if ch.session == nil {
		ch.telegram.SendMessage(
			chatID,
			"❌ Control de sesión no disponible",
		)
		return
	}
	ch.session.Start()
	ch.telegram.SendMessage(
		chatID,
		"🟢 <b>Sesión de trading INICIADA</b>\n\n"+
			"Nuevas operaciones: habilitadas\n"+
			"Modo de ejecución: PAPER",
	)
}
func (ch *CommandsHandler) HandleSessionStop(chatID int64) {
	if ch.session == nil {
		ch.telegram.SendMessage(
			chatID,
			"❌ Control de sesión no disponible",
		)
		return
	}
	ch.session.Stop()
	ch.telegram.SendMessage(
		chatID,
		"🔴 <b>Sesión de trading DETENIDA</b>\n\n"+
			"No se abrirán nuevas posiciones.\n"+
			"Las posiciones existentes no se cierran automáticamente.",
	)
}
func (ch *CommandsHandler) HandleSessionSchedule(chatID int64) {
	if ch.session == nil {
		ch.telegram.SendMessage(
			chatID,
			"❌ Control de sesión no disponible",
		)
		return
	}
	closeText := "NO"
	ch.telegram.SendMessage(
		chatID,
		fmt.Sprintf(
			"⏰ <b>Horario de trading</b>\n\n"+
				"Estado: %s\n"+
				"Horario: %s\n"+
				"Zona: %s\n"+
				"Cierre al terminar: %s",
			ch.session.StatusText(),
			ch.session.ScheduleText(),
			ch.session.TimezoneName(),
			closeText,
		),
	)
}
func (ch *CommandsHandler) HandleHelp(chatID int64) {
	text := "📚 <b>Comandos disponibles:</b>\n\n" +
		"/ping - Comprobar conexión con el bot\n" +
		"/start <chat_id> - Activar alertas\n" +
		"/close - Desactivar alertas\n" +
		"/myid - Obtener tu chat ID\n" +
		"/positions - Posiciones abiertas\n" +
		"/risk - Estado de riesgo y cuenta\n" +
		"/session - Estado de la sesión de trading\n" +
		"/session_start - Iniciar trading\n" +
		"/session_stop - Detener nuevas operaciones\n" +
		"/session_schedule - Mostrar el horario de trading\n" +
		"/performance - Métricas de paper trading\n" +
		"/learn - Aprender estrategia desde un PDF adjunto\n" +
		"/learnib <symbol> <tf> [limit] - Aprender Investing Bulls\n" +
		"/learnibmtf <symbol> [limit] [confirm5m] - Aprender 1H→15m→5m\n" +
		"/validateibmtf <strategy_id> <symbol> [limit] - Validar OOS fijo y activar\n" +
		"/validateib <strategy_id> <symbol> <tf> [limit] - Validar OOS y activar si pasa\n" +
		"/strategies - Listar estrategias aprendidas\n" +
		"/strategy <id> - Detalle de una estrategia\n" +
		"/backtest <id> - Re-validar una estrategia (OOS)\n" +
		"/market - Estado del mercado en vivo (WEEX)\n" +
		"/symbol <contrato> - Cambiar el símbolo del feed\n" +
		"/timeframe <tf> - Cambiar la temporalidad del feed\n" +
		"/help - Mostrar esta ayuda"
	ch.telegram.SendMessage(chatID, text)
}

func (ch *CommandsHandler) HandlePositions(chatID int64) {
	if ch.positions == nil {
		ch.telegram.SendMessage(chatID, "❌ Gestión de posiciones no disponible")
		return
	}
	open, err := ch.positions.OpenPositions(context.Background())
	if err != nil {
		ch.telegram.SendMessage(chatID, "❌ Error consultando posiciones")
		return
	}
	if len(open) == 0 {
		ch.telegram.SendMessage(chatID, "📭 No hay posiciones abiertas")
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📊 <b>Posiciones abiertas (%d)</b>\n\n", len(open))
	for _, p := range open {
		side := "🔴 SHORT"
		if p.Side == domain.DirectionBuy {
			side = "🟢 LONG"
		}
		fmt.Fprintf(&b, "%s %s\n", side, p.Symbol)
		fmt.Fprintf(&b, "  Estrategia: %s · TF %s\n", p.StrategyID, p.Timeframe)
		fmt.Fprintf(&b, "  Entrada: $%s\n", formatAmount(p.EntryPrice))
		fmt.Fprintf(&b, "  Stop: $%s · Objetivo: $%s\n", formatAmount(p.StopLoss), formatAmount(p.TakeProfit))
		fmt.Fprintf(&b, "  Qty: %.6f · Riesgo: $%.2f\n\n", p.Quantity, p.RiskAmount)
	}
	ch.telegram.SendMessage(chatID, strings.TrimRight(b.String(), "\n"))
}

func (ch *CommandsHandler) HandleRisk(chatID int64) {
	if ch.positions == nil {
		ch.telegram.SendMessage(chatID, "❌ Gestión de riesgo no disponible")
		return
	}
	acc, err := ch.positions.GetAccount(context.Background())
	if err != nil {
		ch.telegram.SendMessage(chatID, "❌ Cuenta no inicializada todavía")
		return
	}
	open, err := ch.positions.OpenPositions(context.Background())
	if err != nil {
		ch.telegram.SendMessage(chatID, "❌ Error consultando posiciones")
		return
	}
	// El drawdown se calcula sobre Equity (incluye PnL no realizado).
	equity := acc.Equity
	if equity <= 0 {
		equity = acc.Balance
	}
	drawdown := 0.0
	if acc.PeakEquity > 0 {
		drawdown = (equity - acc.PeakEquity) / acc.PeakEquity * 100
	}
	text := fmt.Sprintf("💰 <b>Estado de cuenta</b>\n\n"+
		"Balance: $%.2f\n"+
		"Equity: $%.2f\n"+
		"PnL no realizado: %s\n"+
		"Peak equity: $%.2f\n"+
		"Drawdown: %.1f%%\n"+
		"Max drawdown: %.1f%%\n"+
		"Posiciones abiertas: %d",
		acc.Balance, acc.Equity, formatSigned(acc.UnrealizedPnL),
		acc.PeakEquity, drawdown, -acc.MaxDrawdown*100, len(open))
	ch.telegram.SendMessage(chatID, text)
}

func (ch *CommandsHandler) HandlePerformance(chatID int64) {
	if ch.positions == nil || ch.performance == nil {
		ch.telegram.SendMessage(chatID, "❌ Métricas de paper trading no disponibles")
		return
	}
	ctx := context.Background()
	acc, err := ch.positions.GetAccount(ctx)
	if err != nil {
		ch.telegram.SendMessage(chatID, "❌ Cuenta no inicializada todavía")
		return
	}
	perf, err := ch.performance.Performance(ctx)
	if err != nil {
		ch.telegram.SendMessage(chatID, "❌ Error calculando métricas")
		return
	}
	if perf.Trades == 0 {
		ch.telegram.SendMessage(chatID, "📭 Aún no hay trades cerrados")
		return
	}
	pf := formatPF(perf.ProfitFactor)
	text := fmt.Sprintf("📊 <b>Paper Trading</b>\n\n"+
		"Balance: %s\n"+
		"Equity: %s\n\n"+
		"Trades: %d\n"+
		"Win rate: %.2f%%\n"+
		"Profit factor: %s\n\n"+
		"PnL: %s\n"+
		"Retorno: %+.2f%%\n\n"+
		"Max DD: %.2f%%\n\n"+
		"Ganancia media: %s\n"+
		"Pérdida media: %s\n\n"+
		"Wins consecutivos: %d\n"+
		"Losses consecutivos: %d",
		formatSigned(acc.Balance), formatSigned(acc.Equity),
		perf.Trades, perf.WinRate, pf,
		formatSigned(perf.TotalPnL), perf.ReturnPct,
		-acc.MaxDrawdown*100,
		formatSigned(perf.AverageWin), formatSigned(perf.AverageLoss),
		perf.ConsecutiveWins, perf.ConsecutiveLosses)
	ch.telegram.SendMessage(chatID, text)
}

func formatAmount(v float64) string {
	return fmt.Sprintf("%.2f", v)
}

func formatSigned(v float64) string {
	if v < 0 {
		return fmt.Sprintf("-$%.2f", -v)
	}
	return fmt.Sprintf("+$%.2f", v)
}

func formatPF(v float64) string {
	if v == math.Inf(1) {
		return "∞"
	}
	return fmt.Sprintf("%.2f", v)
}
