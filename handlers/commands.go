package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/session"
	"tradingview-bot/models"
	"tradingview-bot/services"
)

type CommandsHandler struct {
	userManager *models.UserManager
	telegram    *services.TelegramService
	positions   domain.PositionStore
	session     *session.Manager
	mode        string
}

func NewCommandsHandler(um *models.UserManager, tg *services.TelegramService, positions domain.PositionStore, sess *session.Manager, mode string) *CommandsHandler {
	return &CommandsHandler{
		userManager: um,
		telegram:    tg,
		positions:   positions,
		session:     sess,
		mode:        mode,
	}
}
func (ch *CommandsHandler) HandleStart(chatID int64, args string) {
	parts := strings.Fields(args)
	if len(parts) < 1 {
		ch.telegram.SendMessage(chatID,
			"uso: /start <tu_chat_id>\n\n "+
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
			"Para reactivar: /start <tu_chat_id>")
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
func (ch *CommandsHandler) HandleSessionStart(chatID int64) {
	if ch.session == nil {
		ch.telegram.SendMessage(chatID, "❌ Gestión de sesión no disponible")
		return
	}
	ch.session.Start()
	mode := strings.ToUpper(ch.mode)
	if mode == "" {
		mode = "PAPER"
	}
	ch.telegram.SendMessage(chatID,
		"🟢 <b>Sesión de trading INICIADA</b>\n\n"+
			"Modo: "+mode+"\n"+
			"Trading habilitado: sí")
}
func (ch *CommandsHandler) HandleSessionStop(chatID int64) {
	if ch.session == nil {
		ch.telegram.SendMessage(chatID, "❌ Gestión de sesión no disponible")
		return
	}
	ch.session.Stop()
	ch.telegram.SendMessage(chatID,
		"🔴 <b>Sesión de trading DETENIDA</b>\n\n"+
			"No se abrirán nuevas posiciones.\n"+
			"Telegram y recepción de señales continúan activos.")
}
func (ch *CommandsHandler) HandleSession(chatID int64) {
	if ch.session == nil {
		ch.telegram.SendMessage(chatID, "❌ Gestión de sesión no disponible")
		return
	}
	state := "🔴 DETENIDA"
	if ch.session.IsActive() {
		state = "🟢 ACTIVA"
	}
	mode := strings.ToUpper(ch.mode)
	if mode == "" {
		mode = "PAPER"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📊 <b>Estado de trading</b>\n\n")
	fmt.Fprintf(&b, "Sesión: %s\n", state)
	fmt.Fprintf(&b, "Modo: %s\n", mode)
	if ch.positions != nil {
		open, err := ch.positions.OpenPositions(context.Background())
		if err == nil {
			fmt.Fprintf(&b, "Posiciones abiertas: %d\n", len(open))
		}
	}
	fmt.Fprintf(&b, "Señales recibidas: %d", ch.session.Signals())
	ch.telegram.SendMessage(chatID, strings.TrimRight(b.String(), "\n"))
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
		"/session_start - Iniciar sesión de trading\n" +
		"/session_stop - Detener sesión de trading\n" +
		"/learn - Aprender estrategia desde un PDF adjunto\n" +
		"/strategies - Listar estrategias aprendidas\n" +
		"/strategy <id> - Detalle de una estrategia\n" +
		"/backtest <id> - Re-validar una estrategia (OOS)\n" +
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
	drawdown := 0.0
	if acc.PeakEquity > 0 {
		drawdown = (acc.Balance - acc.PeakEquity) / acc.PeakEquity * 100
	}
	text := fmt.Sprintf("💰 <b>Estado de cuenta</b>\n\n"+
		"Balance: $%.2f\n"+
		"Peak equity: $%.2f\n"+
		"Drawdown: %.1f%%\n"+
		"Posiciones abiertas: %d", acc.Balance, acc.PeakEquity, drawdown, len(open))
	ch.telegram.SendMessage(chatID, text)
}

func formatAmount(v float64) string {
	return fmt.Sprintf("%.2f", v)
}
