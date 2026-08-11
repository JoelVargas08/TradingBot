package handlers

import (
	"context"
	"fmt"
	"strings"

	"tradingview-bot/internal/domain"
	"tradingview-bot/models"
	"tradingview-bot/services"
)

type CommandsHandler struct {
	userManager *models.UserManager
	telegram    *services.TelegramService
	positions   domain.PositionStore
}

func NewCommandsHandler(um *models.UserManager, tg *services.TelegramService, positions domain.PositionStore) *CommandsHandler {
	return &CommandsHandler{
		userManager: um,
		telegram:    tg,
		positions:   positions,
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
	var targetChatID int64
	fmt.Sscanf(parts[0], "%d", &targetChatID)
	if targetChatID == 0 {
		ch.telegram.SendMessage(chatID, "❌ Chat ID inválido")
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
func (ch *CommandsHandler) HandleHelp(chatID int64) {
	text := "📚 <b>Comandos disponibles:</b>\n\n" +
		"/start <chat_id> - Activar alertas\n" +
		"/close - Desactivar alertas\n" +
		"/myid - Obtener tu chat ID\n" +
		"/positions - Posiciones abiertas\n" +
		"/risk - Estado de riesgo y cuenta\n" +
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
