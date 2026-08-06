package handlers

import (
	"fmt"
	"strings"
	"tradingview-bot/models"
	"tradingview-bot/services"
)

type CommandsHandler struct {
	userManager *models.UserManager
	telegram    *services.TelegramService
}

func NewCommandsHandler(um *models.UserManager, tg *services.TelegramService) *CommandsHandler {
	return &CommandsHandler{
		userManager: um,
		telegram:    tg,
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
		"/help - Mostrar esta ayuda"
	ch.telegram.SendMessage(chatID, text)
}
