package main

import (
	"fmt"
	"log"

	"tradingview-bot/config"
	"tradingview-bot/internal/session"
	"tradingview-bot/services"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// notifyAdmin envía un mensaje al chat de administración. Si el chat no está
// configurado solo se registra en el log: el runner nunca debe morir por una
// notificación fallida.
func notifyAdmin(telegram *services.TelegramService, adminChatID int64, text string) {
	if adminChatID == 0 {
		log.Println("TELEGRAM_ADMIN_CHAT_ID no configurado: no se envía notificación al administrador")
		return
	}
	if err := telegram.SendMessage(adminChatID, text); err != nil {
		log.Printf("Error enviando la notificación al administrador (chat_id=%d): %v", adminChatID, err)
	}
}

// startupMessage construye el aviso de arranque del runner PAPER. El usuario
// recibe la confirmación de que el bot está trabajando aunque no haya
// conectado con Telegram durante la sesión.
func startupMessage(cfg *config.Config, bot *tgbotapi.BotAPI, tradingSession *session.Manager, runID string) string {
	username := ""
	if bot != nil {
		username = bot.Self.UserName
	}
	sessionText := "Sesión PAPER: sin control de sesión"
	if tradingSession != nil {
		sessionText = fmt.Sprintf(
			"Sesión PAPER: %s\nHorario: %s (%s)",
			tradingSession.StatusText(),
			tradingSession.ScheduleText(),
			tradingSession.TimezoneName(),
		)
	}
	runText := ""
	if runID != "" {
		runText = fmt.Sprintf("\nRun: <code>%s</code>", runID)
	}
	return fmt.Sprintf(
		"🟢 <b>Bot PAPER en marcha</b>\n\n"+
			"El trading algorítmico ya está trabajando.\n"+
			"Modo: PAPER (simulado; no se abren órdenes reales)\n"+
			"%s%s\n"+
			"Bot: @%s",
		sessionText, runText, username,
	)
}

// shutdownMessage construye el aviso de fin de sesión.
func shutdownMessage(reason string) string {
	return fmt.Sprintf(
		"🔴 <b>Bot PAPER detenido</b>\n\n"+
			"Motivo: %s\n"+
			"No se abrirán nuevas posiciones. Las posiciones existentes no se cierran automáticamente.",
		reason,
	)
}
