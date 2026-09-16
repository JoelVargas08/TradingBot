# TradingBot — Fase A.1: Telegram

**Repositorio:** `JoelVargas08/TradingBot`  
**Commit actual revisado:** `a13fba7`  
**Objetivo:** conseguir recepción verificable de mensajes y hacer funcionar `/ping` antes de continuar con PDF, paper trading, TradingView o Weex.

## 1. `main.go`: cambiar el polling

Localiza el uso de `bot.GetUpdatesChan(...)`. Para esta fase, reemplázalo por polling explícito con `bot.GetUpdates(...)`, para que el error real de Telegram quede visible.

La estructura debe ser equivalente a:

```go
func runTelegramPolling(ctx context.Context, bot *tgbotapi.BotAPI, processUpdate func(tgbotapi.Update)) {
    offset := 0
    log.Println("Telegram polling iniciado; esperando mensajes...")

    for {
        select {
        case <-ctx.Done():
            log.Println("Telegram polling detenido")
            return
        default:
        }

        cfg := tgbotapi.UpdateConfig{
            Offset:  offset,
            Timeout: 30,
        }

        updates, err := bot.GetUpdates(cfg)
        if err != nil {
            log.Printf("Telegram getUpdates error: %v", err)

            select {
            case <-ctx.Done():
                return
            case <-time.After(5 * time.Second):
            }
            continue
        }

        for _, update := range updates {
            if update.UpdateID >= offset {
                offset = update.UpdateID + 1
            }

            log.Printf("Telegram update recibido: update_id=%d", update.UpdateID)
            processUpdate(update)
        }
    }
}
```

**Importante:** adapta la función al contexto y nombres que ya existen en `main.go`; no dupliques el procesamiento de comandos existente.

## 2. Mantener la eliminación del webhook

Debe ejecutarse antes del polling:

```go
_, err := bot.Request(tgbotapi.DeleteWebhookConfig{
    DropPendingUpdates: false,
})
if err != nil {
    log.Printf("Telegram: error eliminando webhook: %v", err)
} else {
    log.Println("Telegram: webhook eliminado correctamente")
}
```

No cambiar `DropPendingUpdates` a `true`: queremos conservar updates pendientes durante el diagnóstico.

## 3. Mantener el diagnóstico de identidad

Después de crear el bot:

```go
log.Printf("Telegram conectado como @%s", bot.Self.UserName)
```

El orden esperado es aproximadamente:

```text
Telegram conectado como @cubantradebot
Telegram: webhook eliminado correctamente
Telegram polling iniciado; esperando mensajes...
```

## 4. Imports de `main.go`

Si faltan, comprobar:

```go
"context"
"log"
"time"

tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
```

Después:

```powershell
gofmt -w main.go
```

## 5. `handlers/commands.go`

No rehacer los handlers.

Conservar:

- `/ping`
- `/start`
- `/close`
- `/myid`
- `/positions`
- `/risk`
- `/help`

Comprobar que el `switch` existente tenga:

```go
case "ping":
    commandsHandler.HandlePing(update.Message.Chat.ID)
```

El objetivo de esta fase es probar primero `/ping`.

## 6. `services/telegram.go`

No modificar `SendMessage()` todavía. La implementación actual con:

```go
msg := tgbotapi.NewMessage(chatID, truncate(text))
msg.ParseMode = "HTML"
_, err := ts.bot.Send(msg)
```

es suficiente para determinar si el problema está en recepción o envío.

## 7. Pruebas de compilación

Ejecutar:

```powershell
gofmt -w .
go build ./...
go test ./...
go vet ./...
```

Todo debe quedar en verde.

## 8. Prueba funcional

Arrancar:

```powershell
go run .
```

Con el proceso activo, abrir `@cubantradebot` y enviar:

```text
/ping
```

Debe aparecer en la consola:

```text
Telegram update recibido: update_id=...
```

y Telegram debe responder:

```text
🏓 Pong!
```

## 9. Interpretación de resultados

### Si aparece `Telegram update recibido`

Telegram está entregando updates. Si no llega `Pong`, revisar:

```text
update → parser → switch → HandlePing → SendMessage
```

### Si aparece `400 Logged out`

No seguir modificando handlers. El problema está relacionado con el estado de recepción del bot en Telegram.

### Si aparece `409 Conflict`

Investigar otra instancia del bot o consumidor de `getUpdates`.

### Si aparece `401 Unauthorized`

Revisar el token configurado.

### Si aparecen errores de red

Investigar conectividad desde el equipo donde corre el bot.

### Si no aparece ni error ni `update recibido`

Revisar el contexto del polling y el bloqueo/conectividad del proceso.

## 10. Pruebas manuales opcionales

Con el bot detenido:

```powershell
Invoke-RestMethod "https://api.telegram.org/bot<TOKEN>/getMe"
```

Debe devolver `ok=True`.

Comprobar webhook:

```powershell
Invoke-RestMethod "https://api.telegram.org/bot<TOKEN>/getWebhookInfo"
```

No ejecutar `getUpdates` manualmente mientras el bot esté haciendo polling.

## 11. No tocar todavía

Hasta que `/ping` funcione:

- aprendizaje desde PDF;
- TradingView;
- Weex;
- otras exchanges;
- trading real;
- cambios de estrategia;
- cambios de riesgo;
- ampliaciones del paper engine.

Telegram será el canal de control y monitorización, por lo que debe quedar operativo antes de continuar.

## 12. Criterio de finalización

```text
go build ./...       → OK
go test ./...        → OK
go vet ./...         → OK
Bot iniciado         → OK
Webhook eliminado    → OK
Polling iniciado     → OK
/ping enviado        → OK
Update recibido      → OK
🏓 Pong!             → OK
```

## 13. Commit

Cuando la prueba funcione:

```powershell
git add main.go
git commit -m "Fix Telegram polling diagnostics"
git push origin main
git log --oneline -3
git status
```

Guardar el SHA del nuevo commit.

## 14. Orden posterior

```text
Telegram funcionando
        ↓
PDF de la estrategia
        ↓
Paper engine + sesiones + métricas
        ↓
Walk-forward / OOS
        ↓
Paper trading prolongado
        ↓
TradingView
        ↓
Exchange
        ↓
Weex con dinero real
```

**Punto crítico:** no basta con que el programa diga “Telegram conectado”. Debemos demostrar el recorrido completo:

```text
Telegram → getUpdates → update recibido → handler → SendMessage → Telegram
```

La prueba mínima es `/ping`.
