package services

import (
	"fmt"
	"log"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type TelegramService struct {
	bot *tgbotapi.BotAPI
}

func NewTelegramService(bot *tgbotapi.BotAPI) *TelegramService {
	return &TelegramService{bot: bot}
}
func (ts *TelegramService) SendMessage(chatID int64, text string) error {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "HTML"
	_, err := ts.bot.Send(msg)
	return err
}
func (ts *TelegramService) SendAlert(chatID int64, symbol, action string, price float64) {
	var emoji, signalText string
	switch action {
	case "buy":
		emoji = "🟢"
		signalText = "BUY"
	case "sell":
		emoji = "🔴"
		signalText = "SELL"
	default:
		emoji = "⚪"
		signalText = action
	}
	text := fmt.Sprintf(
		"%s <b>Alerta: %s</b>\n\nSeñal cambió a: <b>%s</b>\nPrecio: $%s",
		emoji, symbol, signalText, formatPrice(price),
	)
	if err := ts.SendMessage(chatID, text); err != nil {
		log.Printf("Error enviando alerta a %d: %v", chatID, err)
	}
}

func formatPrice(p float64) string {
	switch {
	case p >= 1000:
		return fmt.Sprintf("%.2f", p)
	case p >= 1:
		return fmt.Sprintf("%.4f", p)
	default:
		return fmt.Sprintf("%.6f", p)
	}
}
