package services

import (
	"errors"
	"fmt"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type TelegramService struct {
	bot *tgbotapi.BotAPI
}

func NewTelegramService(bot *tgbotapi.BotAPI) *TelegramService {
	return &TelegramService{bot: bot}
}

type retryableError struct {
	msg   string
	delay time.Duration
}

func (e *retryableError) Error() string { return e.msg }

func (e *retryableError) RetryDelay() time.Duration { return e.delay }

func (ts *TelegramService) SendMessage(chatID int64, text string) error {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "HTML"
	_, err := ts.bot.Send(msg)
	if err == nil {
		return nil
	}
	var apiErr *tgbotapi.Error
	if errors.As(err, &apiErr) && apiErr.ResponseParameters.RetryAfter > 0 {
		return &retryableError{
			msg:   fmt.Sprintf("telegram rate-limited: %v", err),
			delay: time.Duration(apiErr.ResponseParameters.RetryAfter) * time.Second,
		}
	}
	return err
}
