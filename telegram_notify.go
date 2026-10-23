package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"tradingview-bot/config"
	"tradingview-bot/internal/session"
	"tradingview-bot/services"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// MarketStatus resume el estado del feed de mercado para los avisos. Se
// mantiene como estructura propia (y no como el tipo del servicio de ingesta)
// para que los avisos se puedan probar sin WEEX ni red.
type MarketStatus struct {
	Provider  string
	Connected bool
	Symbol    string
	Timeframe string
}

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
//
// El mensaje no afirma nada que los datos no respalden: si la sesión está
// detenida (el runner arrancó fuera de la ventana 08:00-14:00) o el feed de
// mercado aún no está conectado, el aviso lo dice explícitamente en lugar de
// prometer que el trading ya está operando.
func startupMessage(cfg *config.Config, bot *tgbotapi.BotAPI, tradingSession *session.Manager, runID string, market MarketStatus) string {
	username := ""
	if bot != nil {
		username = bot.Self.UserName
	}

	title := "🟢 <b>Bot PAPER en marcha</b>"
	lead := "El bot está conectado a Telegram y escuchando comandos."
	var sessionText string
	switch {
	case tradingSession == nil:
		sessionText = "Sesión PAPER: sin control de sesión"
	case tradingSession.IsActive():
		sessionText = fmt.Sprintf(
			"Sesión PAPER: %s\nHorario: %s (%s)",
			tradingSession.StatusText(),
			tradingSession.ScheduleText(),
			tradingSession.TimezoneName(),
		)
	case tradingSession.Schedule().Enabled:
		// El runner arrancó fuera de la ventana: el gate está cerrado y el
		// propio runner se apagará al terminar.  No se puede decir que el
		// trading está trabajando.
		title = "⚠️ <b>Bot PAPER en marcha (sesión detenida)</b>"
		lead = "El bot está conectado a Telegram y escuchando comandos,\n" +
			"pero la sesión PAPER no está operativa ahora mismo."
		sessionText = fmt.Sprintf(
			"Sesión PAPER: %s\nHorario: %s (%s)",
			tradingSession.StatusText(),
			tradingSession.ScheduleText(),
			tradingSession.TimezoneName(),
		)
	default:
		sessionText = "Sesión PAPER: DETENIDA (usa /session_start para arrancarla)"
	}

	runText := ""
	if runID != "" {
		runText = fmt.Sprintf("\nRun: <code>%s</code>", runID)
	}
	mode := "PAPER"
	if cfg != nil && cfg.Mode != "" {
		mode = strings.ToUpper(cfg.Mode)
	}
	botLine := ""
	if username != "" {
		botLine = fmt.Sprintf("\nBot: @%s", username)
	}
	return fmt.Sprintf(
		"%s\n\n"+
			"%s\n"+
			"%s%s\n"+
			"%s\n"+
			"Modo: %s (simulado; no se abren órdenes reales)%s",
		title,
		lead,
		sessionText, runText,
		marketLine(market),
		mode,
		botLine,
	)
}

// marketLine describe el feed de mercado sin afirmar más de lo que se sabe.
func marketLine(market MarketStatus) string {
	provider := strings.TrimSpace(market.Provider)
	if provider == "" {
		return "Feed de mercado: no configurado"
	}
	target := strings.TrimSpace(strings.TrimSpace(market.Symbol) + " " + strings.TrimSpace(market.Timeframe))
	if market.Connected {
		if target != "" {
			return fmt.Sprintf("Feed de mercado: %s conectado (%s)", provider, target)
		}
		return fmt.Sprintf("Feed de mercado: %s conectado", provider)
	}
	return fmt.Sprintf("Feed de mercado: %s conectando… (te aviso cuando esté listo)", provider)
}

// feedReadyMessage confirma que el feed de mercado está vivo: es la evidencia
// de que el bot ya puede analizar el mercado.
func feedReadyMessage(market MarketStatus) string {
	return fmt.Sprintf(
		"✅ <b>Feed de mercado conectado</b>\n\n"+
			"%s\n"+
			"El bot ya analiza el mercado y puede abrir operaciones PAPER.",
		marketLine(market),
	)
}

// feedUnavailableMessage avisa de que el feed no conecta dentro del margen:
// es preferible decirlo a fingir que todo está bien.
func feedUnavailableMessage(market MarketStatus, wait string) string {
	provider := strings.TrimSpace(market.Provider)
	if provider == "" {
		provider = "no configurado"
	}
	return fmt.Sprintf(
		"⚠️ <b>El feed de mercado no ha conectado</b>\n\n"+
			"Proveedor: %s\n"+
			"Esperado: %s\n"+
			"El bot sigue escuchando comandos, pero no puede analizar el mercado.\n"+
			"Revisa el log del workflow y la conectividad con WEEX.",
		provider,
		wait,
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

const (
	// feedReadyTimeout es el margen que se espera al feed de mercado antes de
	// avisar de que no conecta.  El arranque incluye el checkout, la compilación
	// de Go y el backfill de WEEX: dos minutos son holgados.
	feedReadyTimeout = 2 * time.Minute
	// feedPollInterval es la cadencia de comprobación de la conexión del feed.
	feedPollInterval = 5 * time.Second
)

// waitForFeed avisa cuando el feed de mercado conecta (una sola vez) o cuando
// se agota el margen de espera.  Nunca lanza: un aviso fallido no debe tumbar
// el runner.  status() y notify() se inyectan para poder probar el comportamiento
// sin WEEX ni Telegram.
func waitForFeed(
	ctx context.Context,
	status func() MarketStatus,
	timeout, interval time.Duration,
	notify func(string),
	logf func(format string, args ...any),
) {
	if timeout <= 0 {
		timeout = feedReadyTimeout
	}
	if interval <= 0 {
		interval = feedPollInterval
	}
	deadline := time.Now().Add(timeout)
	for {
		st := status()
		if st.Connected {
			notify(feedReadyMessage(st))
			return
		}
		if !time.Now().Before(deadline) {
			notify(feedUnavailableMessage(st, timeout.String()))
			return
		}
		if logf != nil {
			logf("feed de mercado aún no conectado (proveedor=%s); esperando hasta %s",
				st.Provider, timeout)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

