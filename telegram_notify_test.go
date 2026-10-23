package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"tradingview-bot/config"
	"tradingview-bot/internal/session"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// scheduleAround devuelve un horario que siempre contiene el instante actual:
// una ventana de 12 horas centrada en now.  Sirve para probar el mensaje de
// arranque con sesión ACTIVA sin depender de la hora real del reloj.
func scheduleAround(t *testing.T, now time.Time) session.Schedule {
	t.Helper()
	loc := time.UTC
	tIn := now.In(loc)
	start := tIn.Add(-6 * time.Hour)
	end := tIn.Add(6 * time.Hour)
	return session.Schedule{
		Enabled:  true,
		Start:    time.Duration(start.Hour())*time.Hour + time.Duration(start.Minute())*time.Minute,
		End:      time.Duration(end.Hour())*time.Hour + time.Duration(end.Minute())*time.Minute,
		Location: loc,
	}
}

// scheduleEnded devuelve un horario cuya ventana terminó hace 4 horas: el
// instante actual queda siempre fuera (inWindow también cubre el cruce de
// medianoche), así el test no depende del reloj.
func scheduleEnded(t *testing.T, now time.Time) session.Schedule {
	t.Helper()
	loc := time.UTC
	tIn := now.In(loc)
	start := tIn.Add(-5 * time.Hour)
	end := tIn.Add(-4 * time.Hour)
	return session.Schedule{
		Enabled:  true,
		Start:    time.Duration(start.Hour())*time.Hour + time.Duration(start.Minute())*time.Minute,
		End:      time.Duration(end.Hour())*time.Hour + time.Duration(end.Minute())*time.Minute,
		Location: loc,
	}
}

func newBot(username string) *tgbotapi.BotAPI {
	return &tgbotapi.BotAPI{Self: tgbotapi.User{UserName: username}}
}

func connectedMarket() MarketStatus {
	return MarketStatus{Provider: "weex", Connected: true, Symbol: "BTCUSDT", Timeframe: "1h"}
}

func TestStartupMessage_ActiveSession(t *testing.T) {
	mgr := session.New(false)
	if err := mgr.SetSchedule(scheduleAround(t, time.Now())); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	if !mgr.IsActive() {
		t.Fatal("la sesión debería estar ACTIVA con la ventana configurada")
	}

	msg := startupMessage(&config.Config{Mode: "paper"}, newBot("mi_bot"), mgr, "1234", connectedMarket())

	want := []string{
		"🟢 <b>Bot PAPER en marcha</b>",
		"conectado a Telegram",
		"Sesión PAPER: ACTIVO",
		"UTC",
		"Feed de mercado: weex conectado (BTCUSDT 1h)",
		"Modo: PAPER",
		"<code>1234</code>",
		"@mi_bot",
	}
	for _, w := range want {
		if !strings.Contains(msg, w) {
			t.Errorf("el mensaje no contiene %q\n%s", w, msg)
		}
	}
	if strings.Contains(msg, "detenida") || strings.Contains(msg, "DETENIDA") {
		t.Errorf("el mensaje de una sesión activa no debe hablar de sesión detenida:\n%s", msg)
	}
}

func TestStartupMessage_InactiveScheduledSession(t *testing.T) {
	mgr := session.New(false)
	if err := mgr.SetSchedule(scheduleEnded(t, time.Now())); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	if mgr.IsActive() {
		t.Fatal("la sesión debería estar INACTIVA fuera de la ventana")
	}

	msg := startupMessage(&config.Config{Mode: "paper"}, newBot("mi_bot"), mgr, "", connectedMarket())

	want := []string{
		"⚠️ <b>Bot PAPER en marcha (sesión detenida)</b>",
		"la sesión PAPER no está operativa",
		"Sesión PAPER: INACTIVO",
		"Feed de mercado: weex conectado (BTCUSDT 1h)",
	}
	for _, w := range want {
		if !strings.Contains(msg, w) {
			t.Errorf("el mensaje no contiene %q\n%s", w, msg)
		}
	}
	// El aviso no debe prometer que el bot opera si la sesión está cerrada.
	for _, forbidden := range []string{"puede abrir operaciones", "ya analiza el mercado"} {
		if strings.Contains(msg, forbidden) {
			t.Errorf("el mensaje no debe contener %q con la sesión detenida:\n%s", forbidden, msg)
		}
	}
}

func TestStartupMessage_ManualSessionStopped(t *testing.T) {
	msg := startupMessage(&config.Config{Mode: "paper"}, newBot("mi_bot"), session.New(false), "", MarketStatus{Provider: "weex"})

	want := []string{
		"🟢 <b>Bot PAPER en marcha</b>",
		"Sesión PAPER: DETENIDA (usa /session_start para arrancarla)",
		"Feed de mercado: weex conectando… (te aviso cuando esté listo)",
	}
	for _, w := range want {
		if !strings.Contains(msg, w) {
			t.Errorf("el mensaje no contiene %q\n%s", w, msg)
		}
	}
}

func TestStartupMessage_NilSession(t *testing.T) {
	msg := startupMessage(&config.Config{Mode: "paper"}, nil, nil, "", MarketStatus{})

	for _, w := range []string{"sin control de sesión", "Feed de mercado: no configurado", "Modo: PAPER"} {
		if !strings.Contains(msg, w) {
			t.Errorf("el mensaje no contiene %q\n%s", w, msg)
		}
	}
	if strings.Contains(msg, "Bot: @") {
		t.Errorf("sin bot no debe aparecer la línea de usuario:\n%s", msg)
	}
}

func TestMarketLine(t *testing.T) {
	cases := []struct {
		name   string
		market MarketStatus
		want   string
	}{
		{"conectado con objetivo", MarketStatus{Provider: "weex", Connected: true, Symbol: "BTCUSDT", Timeframe: "1h"},
			"Feed de mercado: weex conectado (BTCUSDT 1h)"},
		{"conectado sin objetivo", MarketStatus{Provider: "weex", Connected: true},
			"Feed de mercado: weex conectado"},
		{"conectando", MarketStatus{Provider: "weex"},
			"Feed de mercado: weex conectando… (te aviso cuando esté listo)"},
		{"sin proveedor", MarketStatus{},
			"Feed de mercado: no configurado"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := marketLine(c.market); got != c.want {
				t.Errorf("marketLine() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFeedMessages(t *testing.T) {
	ready := feedReadyMessage(connectedMarket())
	if !strings.Contains(ready, "✅ <b>Feed de mercado conectado</b>") ||
		!strings.Contains(ready, "weex conectado (BTCUSDT 1h)") {
		t.Errorf("feedReadyMessage no informativo:\n%s", ready)
	}

	pending := feedUnavailableMessage(MarketStatus{Provider: "weex"}, "2m0s")
	for _, w := range []string{"⚠️ <b>El feed de mercado no ha conectado</b>", "Proveedor: weex", "Esperado: 2m0s", "WEEX"} {
		if !strings.Contains(pending, w) {
			t.Errorf("feedUnavailableMessage no contiene %q:\n%s", w, pending)
		}
	}
}

func TestShutdownMessage(t *testing.T) {
	msg := shutdownMessage("ventana programada cerrada (14:00)")
	for _, w := range []string{"🔴 <b>Bot PAPER detenido</b>", "ventana programada cerrada (14:00)", "no se cierran automáticamente"} {
		if !strings.Contains(msg, w) {
			t.Errorf("shutdownMessage no contiene %q:\n%s", w, msg)
		}
	}
}

func TestNotifyAdmin_SinChatIDNoFalla(t *testing.T) {
	// Sin TELEGRAM_ADMIN_CHAT_ID el aviso se omite en silencio: no debe
	// provocar un panic ni tumbar el runner (telegram es nil a propósito).
	notifyAdmin(nil, 0, "aviso")
}

func TestWaitForFeed_ConectadoDeInmediato(t *testing.T) {
	var notificados []string
	waitForFeed(context.Background(),
		func() MarketStatus { return connectedMarket() },
		time.Minute, time.Millisecond,
		func(m string) { notificados = append(notificados, m) }, nil)

	if len(notificados) != 1 {
		t.Fatalf("se esperaba 1 aviso, hubo %d", len(notificados))
	}
	if !strings.Contains(notificados[0], "✅") {
		t.Errorf("el aviso debería ser de éxito: %s", notificados[0])
	}
}

func TestWaitForFeed_ConectaTrasEsperar(t *testing.T) {
	llamadas := 0
	status := func() MarketStatus {
		llamadas++
		if llamadas < 3 {
			return MarketStatus{Provider: "weex"}
		}
		return connectedMarket()
	}

	var notificados []string
	waitForFeed(context.Background(), status, time.Minute, time.Millisecond,
		func(m string) { notificados = append(notificados, m) }, nil)

	if len(notificados) != 1 || !strings.Contains(notificados[0], "✅") {
		t.Fatalf("aviso inesperado: %#v", notificados)
	}
	if llamadas < 3 {
		t.Errorf("se esperaban al menos 3 consultas de estado, hubo %d", llamadas)
	}
}

func TestWaitForFeed_TiempoAgotado(t *testing.T) {
	var notificados []string
	waitForFeed(context.Background(),
		func() MarketStatus { return MarketStatus{Provider: "weex"} },
		10*time.Millisecond, time.Millisecond,
		func(m string) { notificados = append(notificados, m) }, nil)

	if len(notificados) != 1 {
		t.Fatalf("se esperaba 1 aviso, hubo %d", len(notificados))
	}
	if !strings.Contains(notificados[0], "⚠️") || !strings.Contains(notificados[0], "10ms") {
		t.Errorf("el aviso debería informar del fallo y de la espera: %s", notificados[0])
	}
}

func TestWaitForFeed_ContextoCancelado(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	notificado := false
	waitForFeed(ctx,
		func() MarketStatus { return MarketStatus{Provider: "weex"} },
		time.Minute, time.Hour,
		func(string) { notificado = true }, nil)

	if notificado {
		t.Error("con el contexto cancelado no debe enviarse ningún aviso")
	}
}
