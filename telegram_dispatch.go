package main

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

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
	// ibDiagDefault son los argumentos de /ibdiag sin parámetros: la
	// estrategia Investing Bulls que opera en esta sesión y su símbolo.
	ibDiagDefault string
	// audit son las fuentes de solo lectura de los comandos de auditoría
	// (/ib*, /weex*). nil si el proceso no las tiene (p. ej. en pruebas).
	audit *auditSources
}

// auditCommands son los comandos de auditoría: solo lectura, nunca inician ni
// detienen la sesión, y quedan reservados al chat administrador.
var auditCommands = map[string]bool{
	"ibstatus": true, "ibaudit": true, "ibwhy": true, "ibtrace": true, "ibfoundation": true,
	"ibevals": true, "ibdata": true, "ibhealth": true, "iberrors": true, "ibops": true,
	"ibstats": true, "ibhelp": true,
	"weexstatus": true, "weexdata": true, "weexgaps": true, "weexbackfill": true, "weexerrors": true,
}

// auditReport devuelve el generador de un comando de auditoría.
func (d *telegramDispatcher) auditReport(command string) (func(context.Context, string) []string, bool) {
	a := d.audit
	switch command {
	case "ibhelp":
		return func(context.Context, string) []string { return splitTelegram(ibHelpText(), 0) }, true
	case "ibstatus":
		return a.reportIBStatus, true
	case "ibaudit":
		return a.reportIBAudit, true
	case "ibwhy":
		return a.reportIBWhy, true
	case "ibtrace":
		return a.reportIBTrace, true
	case "ibfoundation":
		return a.reportIBFoundation, true
	case "ibevals":
		return a.reportIBEvals, true
	case "ibdata":
		return a.reportIBData, true
	case "ibhealth":
		return a.reportIBHealth, true
	case "iberrors":
		return a.reportIBErrors, true
	case "ibops":
		return a.reportIBOps, true
	case "ibstats":
		return a.reportIBStats, true
	case "weexstatus":
		return a.reportWeexStatus, true
	case "weexdata":
		return a.reportWeexData, true
	case "weexgaps":
		return a.reportWeexGaps, true
	case "weexbackfill":
		return a.reportWeexBackfill, true
	case "weexerrors":
		return a.reportWeexErrors, true
	}
	return nil, false
}

// runAudit genera y envía un informe en segundo plano: nunca bloquea el loop
// de polling, acota el tiempo de consulta y un pánico en un informe no tumba
// el bot (se responde con un error claro).
func (d *telegramDispatcher) runAudit(ctx context.Context, chatID int64, command, args string) {
	build, ok := d.auditReport(command)
	if !ok {
		return
	}
	if d.audit == nil && command != "ibhelp" {
		d.telegram.SendMessage(chatID, "⚪ Auditoría no disponible en este proceso (motor o controlador no iniciado).")
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Telegram: pánico generando /%s: %v", command, r)
				d.telegram.SendMessage(chatID, "🔴 Error interno generando el informe /"+command+". El bot sigue operativo.")
			}
		}()
		rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		for _, chunk := range build(rctx, args) {
			if err := d.telegram.SendMessage(chatID, chunk); err != nil {
				log.Printf("Telegram: no se pudo enviar /%s: %v", command, err)
				return
			}
		}
	}()
}

// adminOnlyCommands controlan la sesión o el workflow, cambian la estrategia o
// lanzan trabajos pesados: solo TELEGRAM_ADMIN_CHAT_ID puede usarlos (la misma
// regla que aplica el controlador de GitHub Actions cuando no hay runner).
var adminOnlyCommands = map[string]bool{
	"session_start": true, "session_stop": true, "stop": true,
	"strategy": true, "symbol": true, "timeframe": true, "activate": true, "backtest": true,
	"learn": true, "learnib": true, "validateib": true, "learnibmtf": true, "validateibmtf": true,
	"ibdiag": true, "ibstatus": true, "ibevals": true, "ibops": true,
}

func init() {
	for c := range auditCommands {
		adminOnlyCommands[c] = true
	}
}

// restricted indica si el mensaje solo puede venir del chat administrador.
func (d *telegramDispatcher) restricted(m tginternal.Message) bool {
	if d.cfg == nil || d.cfg.TelegramAdminChatID == 0 || m.ChatID == d.cfg.TelegramAdminChatID {
		// Sin chat admin configurado (despliegue local) se mantiene el
		// comportamiento anterior.
		return false
	}
	if m.DocumentID != "" {
		return true
	}
	if !m.IsCommand {
		return false
	}
	if m.Command == "start" {
		// /start sin argumentos arranca el workflow; con argumento solo se
		// permite suscribir el propio chat a las alertas.
		args := strings.Fields(m.Arguments)
		return len(args) == 0 || args[0] != strconv.FormatInt(m.ChatID, 10)
	}
	return adminOnlyCommands[m.Command]
}

// Handle implementa tginternal.Handler.
func (d *telegramDispatcher) Handle(ctx context.Context, m tginternal.Message) error {
	log.Printf("Telegram update recibido: update_id=%d", m.UpdateID)
	chatID := m.ChatID
	if d.restricted(m) {
		log.Printf("Telegram: comando restringido rechazado (update_id=%d chat=%d comando=%q)", m.UpdateID, chatID, m.Command)
		d.telegram.SendMessage(chatID, "⛔ Comando reservado al administrador del bot.")
		return nil
	}

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
		if strings.TrimSpace(args) == "" {
			args = d.ibDiagDefault
		}
		go runInvestingBullsDiag(ctx, d.store, d.telegram, chatID, args)
	case "ibstatus", "ibaudit", "ibwhy", "ibtrace", "ibfoundation", "ibevals", "ibdata", "ibhealth",
		"iberrors", "ibops", "ibstats", "ibhelp",
		"weexstatus", "weexdata", "weexgaps", "weexbackfill", "weexerrors":
		d.runAudit(ctx, chatID, m.Command, args)
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
