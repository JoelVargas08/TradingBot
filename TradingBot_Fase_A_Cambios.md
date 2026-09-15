# TradingBot — Fase A: cambios de código

## Objetivo

Dejar Telegram funcionando de forma fiable antes de continuar con PDF, backtesting, TradingView y WEEX.

> Esta fase no modifica todavía el sistema de aprendizaje desde PDF, TradingView, WEEX ni el backtesting.

---

## 1. `main.go` — comprobar Telegram y eliminar webhook previo

Después de crear el bot:

```go
bot, err := tgbotapi.NewBotAPI(cfg.TelegramBotToken)
if err != nil {
    log.Fatalf("Error creando bot: %v", err)
}
```

añadir:

```go
log.Printf("Telegram conectado como @%s", bot.Self.UserName)

// Fase A: usamos polling, por lo que eliminamos cualquier webhook
// anterior que pueda impedir que getUpdates funcione.
if _, err := bot.Request(tgbotapi.DeleteWebhookConfig{
    DropPendingUpdates: false,
}); err != nil {
    log.Fatalf("Error eliminando webhook de Telegram: %v", err)
}

log.Println("Webhook de Telegram eliminado; polling preparado")
```

### Importante

El proyecto utiliza **polling** para recibir mensajes de Telegram. El endpoint HTTP `/webhook` del proyecto es para las alertas de TradingView y no debe confundirse con el webhook de Telegram.

---

## 2. `main.go` — añadir diagnóstico del polling

Localizar:

```go
u := tgbotapi.NewUpdate(0)
u.Timeout = 60

updates := bot.GetUpdatesChan(u)

go func() {
    for update := range updates {
```

Cambiarlo por:

```go
u := tgbotapi.NewUpdate(0)
u.Timeout = 60

updates := bot.GetUpdatesChan(u)

log.Println("Telegram polling iniciado; esperando mensajes...")

go func() {
    for update := range updates {
        log.Printf(
            "Telegram update recibido: update_id=%d",
            update.UpdateID,
        )
```

Esto permite comprobar si Telegram realmente está entregando los mensajes al bot.

---

## 3. `services/telegram.go` — registrar errores de envío

Añadir:

```go
"log"
```

al bloque de imports.

Reemplazar `SendMessage` por:

```go
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
```

---

## 4. `handlers/commands.go` — validar correctamente `chat_id`

Añadir:

```go
"strconv"
```

al bloque de imports.

Localizar:

```go
var targetChatID int64
fmt.Sscanf(parts[0], "%d", &targetChatID)

if targetChatID == 0 {
```

y reemplazarlo por:

```go
targetChatID, err := strconv.ParseInt(parts[0], 10, 64)
if err != nil || targetChatID == 0 {
    ch.telegram.SendMessage(
        chatID,
        "❌ Chat ID inválido

Ejemplo:
/start 123456789",
    )
    return
}
```

---

## 5. `handlers/commands.go` — añadir `/ping`

Añadir:

```go
func (ch *CommandsHandler) HandlePing(chatID int64) {
    ch.telegram.SendMessage(
        chatID,
        "🏓 <b>Pong!</b>\n\nTelegram está funcionando correctamente.",
    )
}
```

Este comando sirve como prueba mínima de comunicación con Telegram.

---

## 6. `main.go` — registrar `/ping`

En el `switch` que procesa los comandos, añadir:

```go
case "ping":
    commandsHandler.HandlePing(chatID)
```

---

## 7. `handlers/commands.go` — actualizar `/help`

Añadir esta línea:

```go
"/ping - Comprobar conexión con el bot
" +
```

El comienzo del texto debe quedar aproximadamente así:

```go
text := "📚 <b>Comandos disponibles:</b>

" +
    "/ping - Comprobar conexión con el bot
" +
    "/start <chat_id> - Activar alertas
" +
    "/close - Desactivar alertas
" +
    "/myid - Obtener tu chat ID
" +
    "/positions - Posiciones abiertas
" +
    "/risk - Estado de riesgo y cuenta
" +
    "/learn - Aprender estrategia desde un PDF adjunto
" +
    "/strategies - Listar estrategias aprendidas
" +
    "/strategy <id> - Detalle de una estrategia
" +
    "/backtest <id> - Re-validar una estrategia (OOS)
" +
    "/help - Mostrar esta ayuda"
```

---

# 8. Mejora prevista para `/start`

Actualmente el comando exige:

```text
/start <chat_id>
```

Sin embargo, Telegram ya proporciona automáticamente el `chatID` del usuario que envía el comando.

Por tanto, después de comprobar que Telegram funciona, se recomienda modificar `/start` para aceptar:

```text
/start
```

usando directamente el `chatID` del mensaje.

También se puede mantener:

```text
/start 123456789
```

para permitir activaciones explícitas de otro chat cuando sea necesario.

**No es imprescindible para la primera prueba de Fase A.**

---

# 9. Orden exacto de pruebas

Después de realizar los cambios:

### Formatear código

Desde:

```text
D:\ETECSA\Programación\GO\Proyectos\Trading algoritmico
```

ejecutar:

```powershell
gofmt -w .\main.go .\services	elegram.go .\handlers\commands.go
```

### Ejecutar tests

```powershell
go test ./...
```

Debe terminar sin errores.

### Ejecutar el bot

```powershell
go run .
```

La consola debería mostrar:

```text
Telegram conectado como @...
Webhook de Telegram eliminado; polling preparado
Telegram polling iniciado; esperando mensajes...
```

---

# 10. Prueba en Telegram

Abrir el bot y enviar:

```text
/ping
```

Debe responder:

```text
🏓 Pong!

Telegram está funcionando correctamente.
```

Después enviar:

```text
/help
```

Y:

```text
/myid
```

Cuando se envíe `/ping`, la consola debería registrar algo parecido a:

```text
Telegram update recibido: update_id=123456789
```

---

# 11. Diagnóstico según el resultado

## Caso A — no aparece `Telegram polling iniciado`

Problema en la inicialización del polling.

## Caso B — aparece `polling iniciado`, pero no aparece `Telegram update recibido`

El mensaje no está llegando al proceso.

Revisar principalmente:

- token del bot;
- otro proceso ejecutando el mismo bot;
- webhook anterior;
- conexión de red;
- configuración de Telegram.

## Caso C — aparece `Telegram update recibido`, pero el bot no responde

El mensaje está llegando y el problema está en:

- identificación del comando;
- `switch` de comandos;
- `CommandsHandler`;
- `TelegramService.SendMessage`.

El nuevo registro de errores de `SendMessage` ayudará a identificarlo.

## Caso D — `/ping` funciona

Telegram queda validado para continuar con las siguientes fases.

---

# 12. Qué NO modificar todavía

No hacer todavía cambios en:

- aprendizaje de PDF;
- extracción de estrategias;
- StrategyManager;
- backtesting;
- TradingView Paper Trading;
- sesiones de trading;
- conexión con WEEX;
- ejecución con dinero real;
- otras plataformas.

La prioridad es:

```text
Telegram
   ↓
/ping
   ↓
/help
   ↓
/myid
   ↓
/start
   ↓
alertas
```

Después:

```text
PDF
   ↓
estrategia estructurada
   ↓
backtesting
   ↓
métricas / winrate
   ↓
sesiones de trading
   ↓
TradingView Paper Trading
   ↓
WEEX demo/paper si está disponible
   ↓
WEEX real
```

---

# 13. Estado esperado al terminar Fase A

El bot debe:

- conectarse correctamente a Telegram;
- eliminar un webhook antiguo si existe;
- utilizar polling;
- recibir mensajes;
- responder `/ping`;
- responder `/help`;
- responder `/myid`;
- responder `/start`;
- responder `/close`;
- registrar errores de Telegram;
- superar `go test ./...`.

Solo después de cumplir estos puntos se debe avanzar a la siguiente fase.
