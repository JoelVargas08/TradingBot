package main

import (
	"context"
	"log"
	"strings"

	"tradingview-bot/config"
	"tradingview-bot/handlers"
	"tradingview-bot/internal/session"
	"tradingview-bot/internal/store"
	tginternal "tradingview-bot/internal/telegram"
	"tradingview-bot/services"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// telegramDispatcher enruta los mensajes recibidos de Telegram a los handlers
// de la aplicación. Vive fuera de main() para que el flujo de control sea
// legible y quede aislado del arranque del bot.
type telegramDispatcher struct {
	commands         *handlers.CommandsHandler
	telegram         *services.TelegramService
	strategyCommands *handlers.StrategyCommands
	marketCommands   *handlers.MarketCommands
	store            *store.Store
	session          *session.Manager
	cfg              *config.Config
}

// Handle implementa tginternal.Handler.
func (d *telegramDispatcher) Handle(ctx context.Context, m tginternal.Message) error {
	log.Printf("Telegram update recibido: update_id=%d", m.UpdateID)
	chatID := m.ChatID

	// Documento adjunto → aprender la estrategia del PDF.
	if m.DocumentID != "" {
		if d.strategyCommands != nil {
			d.strategyCommands.HandleDocument(chatID, &tgbotapi.Document{FileID: m.DocumentID}, m.Caption)
		} else {
			d.telegram.SendMessage(chatID, "❌ Aprendizaje desde PDF no está habilitado (LLM_ENABLED=false)")
		}
		return nil
	}
	if !m.IsCommand {
		return nil
	}
	args := m.Arguments
	switch m.Command {
	case "start":
		if strings.TrimSpace(args) == "" {
			d.commands.HandleSessionStart(chatID)
		} else {
			d.commands.HandleStart(chatID, args)
		}
	case "close":
		d.commands.HandleClose(chatID)
	case "myid":
		d.commands.HandleMyID(chatID)
	case "positions":
		d.commands.HandlePositions(chatID)
	case "risk":
		d.commands.HandleRisk(chatID)
	case "performance":
		d.commands.HandlePerformance(chatID)
	case "learn":
		d.telegram.SendMessage(chatID, "📥 Envíame el PDF de la estrategia como documento adjunto (opcionalmente con nombre en el caption).")
	case "learnib":
		go runInvestingBullsLearn(ctx, d.store, d.telegram, chatID, args, d.cfg)
	case "validateib":
		go runInvestingBullsValidate(ctx, d.store, d.telegram, chatID, args, d.cfg)
	case "learnibmtf":
		go runInvestingBullsLearnMTF(ctx, d.store, d.telegram, chatID, args, d.cfg)
	case "validateibmtf":
		go runInvestingBullsValidateMTF(ctx, d.store, d.telegram, chatID, args, d.cfg, d.session)
	case "ibdiag":
		go runInvestingBullsDiag(ctx, d.store, d.telegram, chatID, args)
	case "strategies":
		if d.strategyCommands != nil {
			d.strategyCommands.HandleStrategies(chatID)
		} else {
			d.telegram.SendMessage(chatID, "❌ Aprendizaje desde PDF no está habilitado (LLM_ENABLED=false)")
		}
	case "strategy":
		switch {
		case d.marketCommands != nil:
			d.marketCommands.HandleStrategy(chatID, args)
		case d.strategyCommands != nil:
			d.strategyCommands.HandleStrategy(chatID, args)
		default:
			d.telegram.SendMessage(chatID, "❌ Gestión de estrategias no está habilitado (LLM_ENABLED=false)")
		}
	case "market":
		if d.marketCommands != nil {
			d.marketCommands.HandleMarket(chatID)
		} else {
			d.telegram.SendMessage(chatID, "❌ Mercado en vivo no está habilitado (MARKET_DATA_PROVIDER=weex)")
		}
	case "symbol":
		if d.marketCommands != nil {
			d.marketCommands.HandleSymbol(chatID, args)
		} else {
			d.telegram.SendMessage(chatID, "❌ Mercado en vivo no está habilitado (MARKET_DATA_PROVIDER=weex)")
		}
	case "timeframe":
		if d.marketCommands != nil {
			d.marketCommands.HandleTimeframe(chatID, args)
		} else {
			d.telegram.SendMessage(chatID, "❌ Mercado en vivo no está habilitado (MARKET_DATA_PROVIDER=weex)")
		}
	case "backtest":
		if d.strategyCommands != nil {
			d.strategyCommands.HandleBacktest(chatID, args)
		} else {
			d.telegram.SendMessage(chatID, "❌ Aprendizaje desde PDF no está habilitado (LLM_ENABLED=false)")
		}
	case "activate":
		if d.strategyCommands != nil {
			d.strategyCommands.HandleActivate(chatID, args)
		} else {
			d.telegram.SendMessage(chatID, "❌ Gestión de estrategias no está habilitado (LLM_ENABLED=false)")
		}
	case "help":
		d.commands.HandleHelp(chatID)
	case "ping":
		d.commands.HandlePing(chatID)
	case "session":
		d.commands.HandleSession(chatID)
	case "session_start":
		d.commands.HandleSessionStart(chatID)
	case "session_stop", "stop":
		d.commands.HandleSessionStop(chatID)
	case "session_schedule":
		d.commands.HandleSessionSchedule(chatID)
	default:
		d.telegram.SendMessage(chatID, "Comando no reconocido. Usa /help")
	}
	return nil
}
