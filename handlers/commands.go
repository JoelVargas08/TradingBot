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
)

// MessageSender envía texto a Telegram. Al depender de esta interfaz el
// handler puede probarse sin un bot real.
type MessageSender interface {
	SendMessage(chatID int64, text string) error
}

// SessionController expone el control de sesión de trading al handler.
type SessionController interface {
	Start()
	StartFor(time.Duration)
	Stop()
	IsActive() bool
	StartedAt() time.Time
	StatusText() string
	ScheduleText() string
	TimezoneName() string
}

// RemainingWindowProvider es opcional: si el control de sesión lo implementa,
// el bot informa del tiempo que falta para el cierre de la ventana.
type RemainingWindowProvider interface {
	RemainingWindow() time.Duration
}

// PerformanceProvider ofrece métricas de paper trading al handler.
type PerformanceProvider interface {
	Performance(ctx context.Context) (domain.Performance, error)
}

// WorkflowController controls the lifecycle of the GitHub Actions runner.
// Available() indica que el bot se está ejecutando dentro de un workflow, no
// que exista una sesión de trading activa: el estado real de la sesión lo
// gobierna SessionController.
type WorkflowController interface {
	Available() bool
	CancelCurrent(context.Context) error
	RunID() string
}

type CommandsHandler struct {
	userManager *models.UserManager
	telegram    MessageSender
	positions   domain.PositionStore
	session     SessionController
	performance PerformanceProvider
	workflow    WorkflowController
}

func NewCommandsHandler(um *models.UserManager, tg MessageSender, positions domain.PositionStore, session SessionController, performance PerformanceProvider, workflow WorkflowController) *CommandsHandler {
	return &CommandsHandler{
		userManager: um,
		telegram:    tg,
		positions:   positions,
		session:     session,
		performance: performance,
		workflow:    workflow,
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
		ch.telegram.SendMessage(chatID, "❌ Control de sesión no disponible")
		return
	}
	runText := ""
	if ch.workflow != nil {
		if runID := ch.workflow.RunID(); runID != "" {
			runText = fmt.Sprintf("\nRun: <code>%s</code>", runID)
		}
	}
	if ch.session.IsActive() {
		started := ch.session.StartedAt()
		startText := "-"
		if !started.IsZero() {
			startText = started.Format("2006-01-02 15:04:05")
		}
		ch.telegram.SendMessage(chatID, fmt.Sprintf(
			"🟢 <b>Sesión PAPER ACTIVA</b>%s\n\n"+
				"Iniciada: %s%s\n"+
				"Nuevas operaciones: habilitadas",
			runText, startText, ch.remainingWindowText()))
		return
	}
	ch.telegram.SendMessage(chatID, "🔴 <b>Sesión PAPER DETENIDA</b>\n\n"+
		"Nuevas operaciones: deshabilitadas\n"+
		"Usa /session_start para arrancarla.")
}

func (ch *CommandsHandler) HandleSessionStart(chatID int64) {
	if ch.session == nil {
		ch.telegram.SendMessage(chatID, "❌ Control de sesión no disponible")
		return
	}
	// El estado de la sesión lo gobierna el gate interno, no la presencia del
	// workflow:.available() solo indica que el bot corre dentro de Actions.
	if ch.session.IsActive() {
		ch.telegram.SendMessage(chatID, "ℹ️ La sesión PAPER ya está ACTIVA; no se arranca otra.\n"+
			"Usa /session_stop para detenerla.")
		return
	}
	ch.session.Start()
	ch.telegram.SendMessage(chatID, "🟢 <b>Sesión PAPER INICIADA</b>\n\n"+
		"Nuevas operaciones: habilitadas\n"+
		"Las posiciones abiertas no se cierran automáticamente.")
}

func (ch *CommandsHandler) HandleSessionStop(chatID int64) {
	// Primero se cierra el gate interno: así ninguna operación nueva se abre
	// aunque la cancelación del workflow tarde o falle.
	if ch.session != nil {
		ch.session.Stop()
	}
	if ch.workflow != nil && ch.workflow.Available() {
		if err := ch.workflow.CancelCurrent(context.Background()); err != nil {
			ch.telegram.SendMessage(chatID, "🔴 <b>Sesión PAPER DETENIDA</b>\n\n"+
				"No se abrirán nuevas posiciones.\n\n"+
				"⚠️ No pude cancelar la ejecución del workflow: "+err.Error())
			return
		}
		ch.telegram.SendMessage(chatID, "🔴 <b>Sesión PAPER DETENIDA</b>\n\n"+
			"No se abrirán nuevas posiciones. Las posiciones existentes no se cierran automáticamente.\n\n"+
			"GitHub Actions está cancelando la ejecución del runner.")
		return
	}
	ch.telegram.SendMessage(chatID, "🔴 <b>Sesión PAPER DETENIDA</b>\n\n"+
		"No se abrirán nuevas posiciones. Las posiciones existentes no se cierran automáticamente.")
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
				"%s"+
				"Cierre al terminar: %s",
			ch.session.StatusText(),
			ch.session.ScheduleText(),
			ch.session.TimezoneName(),
			ch.remainingWindowText(),
			closeText,
		),
	)
}

// remainingWindowText describe el tiempo restante de la ventana programada
// cuando el control de sesión lo expone.
func (ch *CommandsHandler) remainingWindowText() string {
	provider, ok := ch.session.(RemainingWindowProvider)
	if !ok {
		return ""
	}
	remaining := provider.RemainingWindow()
	if remaining <= 0 {
		return ""
	}
	return fmt.Sprintf("Ventana restante: %s\n", formatRemaining(remaining))
}

// formatRemaining formatea una duración como "2h 15m".
func formatRemaining(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	hours := int(d / time.Hour)
	minutes := int((d % time.Hour) / time.Minute)
	if hours == 0 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dh %dm", hours, minutes)
}
func (ch *CommandsHandler) HandleHelp(chatID int64) {
	text := "📚 <b>Comandos disponibles:</b>\n\n" +
		"/ping - Comprobar conexión con el bot\n" +
		"/start - Arrancar el workflow PAPER (alias de /session_start)\n" +
		"/start <chat_id> - Activar alertas para un chat\n" +
		"/stop - Apagar el workflow PAPER (alias de /session_stop)\n" +
		"/close - Desactivar alertas\n" +
		"/myid - Obtener tu chat ID\n" +
		"/positions - Posiciones abiertas\n" +
		"/risk - Estado de riesgo y cuenta\n" +
		"/session - Estado de la sesión de trading\n" +
		"/session_start - Arrancar el workflow PAPER (runner + WEEX)\n" +
		"/session_stop - Apagar el workflow PAPER y desconectar WEEX\n" +
		"/session_schedule - Mostrar el horario de trading\n" +
		"/performance - Métricas de paper trading\n" +
		"/learn - Aprender estrategia desde un PDF adjunto\n" +
		"/learnib <symbol> <tf> [limit] - Aprender Investing Bulls\n" +
		"/learnibmtf <symbol> [limit] [confirm5m] - Aprender 1H→15m→5m\n" +
		"/validateibmtf <strategy_id> <symbol> [limit] - Validar OOS fijo y activar\n" +
		"/validateib <strategy_id> <symbol> <tf> [limit] - Validar OOS y activar si pasa\n" +
		"/ibdiag <strategy_id> <symbol> [bars] - Diagnóstico del pipeline (no opera)\n" +
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
