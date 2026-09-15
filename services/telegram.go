package services

import (
	"errors"
	"fmt"
	"log"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type TelegramService struct {
	bot *tgbotapi.BotAPI
}

func NewTelegramService(bot *tgbotapi.BotAPI) *TelegramService {
	return &TelegramService{bot: bot}
}

const telegramMaxLen = 4096

// truncate recorta un mensaje al límite de Telegram (4096 caracteres) sin
// cortar una runa UTF-8 por la mitad.
func truncate(text string) string {
	if len(text) <= telegramMaxLen {
		return text
	}
	end := telegramMaxLen - 3
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "..."
}

type retryableError struct {
	msg   string
	delay time.Duration
}

func (e *retryableError) Error() string { return e.msg }

func (e *retryableError) RetryDelay() time.Duration { return e.delay }

func (ts *TelegramService) SendMessage(chatID int64, text string) error {
	msg := tgbotapi.NewMessage(chatID, truncate(text))
	msg.ParseMode = "HTML"

	_, err := ts.bot.Send(msg)
	if err == nil {
		return nil
	}

	var apiErr *tgbotapi.Error
	if errors.As(err, &apiErr) && apiErr.ResponseParameters.RetryAfter > 0 {
		retryErr := &retryableError{
			msg:   fmt.Sprintf("telegram rate-limited: %v", err),
			delay: time.Duration(apiErr.ResponseParameters.RetryAfter) * time.Second,
		}

		log.Printf(
			"Telegram rate limit: chat_id=%d retry_after=%s",
			chatID,
			retryErr.delay,
		)

		return retryErr
	}

	log.Printf(
		"ERROR enviando mensaje Telegram: chat_id=%d error=%v",
		chatID,
		err,
	)

	return err
}
