# TradingBot — Continuación del proyecto
## Revisión del repositorio y fases siguientes
**Repositorio revisado:** https://github.com/JoelVargas08/TradingBot  
**Fecha:** 2026-09-15

---

## 1. Resultado de la revisión

Se revisó nuevamente el repositorio actual y, especialmente:

- `main.go`
- `handlers/commands.go`
- `handlers/strategies.go`
- `handlers/webhook.go`
- `services/telegram.go`
- `config/config.go`
- `internal/domain/domain.go`
- `internal/processor/processor.go`
- `internal/risk/risk.go`
- `internal/strategymanager/manager.go`
- `internal/strategymanager/backtest.go`
- `PLAN_DE_ACCION.md`

El repositorio ya contiene buena parte de las fases 0–7 descritas en `PLAN_DE_ACCION.md`: recepción de webhooks, bus interno, SQLite, ingesta, Chandelier Multi-Confirm, aprendizaje desde PDF, motor ML, backtesting y modo `paper`.

Sin embargo, antes de seguir construyendo encima de todo eso hay un punto importante:

> **No conviene intentar conectar directamente el bot a la cuenta interna de TradingView Paper Trading.**

TradingView documenta Paper Trading como un simulador dentro de Supercharts y documenta los webhooks como mecanismo para enviar alertas a una aplicación externa. Además, TradingView indica que no dispone de una API para obtener datos/valores de indicadores directamente. Por tanto, la arquitectura correcta para nuestro proyecto es:

```text
TradingView
   │
   │ alerta/webhook
   ▼
TradingBot
   │
   ├── Motor Paper Trading propio
   │      ├── balance
   │      ├── posiciones
   │      ├── PnL
   │      ├── win rate
   │      ├── drawdown
   │      └── historial
   │
   ├── Telegram
   │
   └── futuro:
          Weex Adapter
          Otros exchanges/brokers
```

TradingView confirma que sus webhooks envían un `POST` a una URL externa cuando una alerta se dispara. También limita el procesamiento del webhook a 3 segundos y exige HTTPS para este mecanismo. 

---

# 2. FASE A — NO CONTINUAR SIN VERIFICAR TELEGRAM

El código actual crea el bot correctamente y utiliza `GetUpdatesChan()` para recibir comandos.

El flujo actual está en `main.go`:

```go
u := tgbotapi.NewUpdate(0)
u.Timeout = 60
updates := bot.GetUpdatesChan(u)
go func() {
    for update := range updates {
        ...
    }
}()
```

Esto significa que el bot está diseñado para funcionar mediante **long polling**, no mediante webhook de Telegram.

### Problema práctico

El usuario indicó que el bot creado en Telegram todavía no responde.

Antes de avanzar con funcionalidades nuevas hay que comprobar:

1. `TELEGRAM_BOT_TOKEN` corresponde exactamente al bot correcto.
2. El proceso Go realmente está ejecutándose.
3. El log muestra:
   ```text
   Bot iniciado: @nombre_del_bot
   ```
4. No existe un webhook de Telegram configurado previamente para ese bot.
5. No hay otro proceso ejecutando el mismo token.
6. `/help` y `/myid` funcionan en un chat privado con el bot.

### Cambio recomendado

Añadir al arranque una comprobación explícita del bot y del modo de recepción.

Después de crear `bot`:

```go
log.Printf("Bot conectado: @%s (ID %d)", bot.Self.UserName, bot.Self.ID)

info, err := bot.GetWebhookInfo()
if err != nil {
    log.Printf("Advertencia consultando webhook de Telegram: %v", err)
} else if info.URL != "" {
    log.Printf(
        "ADVERTENCIA: Telegram tiene un webhook configurado: %s",
        info.URL,
    )
    log.Printf(
        "El bot utiliza long polling; elimina el webhook antes de usar GetUpdatesChan",
    )
}
```

Para eliminar un webhook anterior, hacerlo una vez desde el entorno de administración del bot o mediante la API de Telegram.

### También añadir `/ping`

En `handlers/commands.go`:

```go
func (ch *CommandsHandler) HandlePing(chatID int64) {
    ch.telegram.SendMessage(
        chatID,
        "🏓 <b>Pong!</b>\nBot funcionando correctamente.",
    )
}
```

Y en `main.go`:

```go
case "ping":
    commandsHandler.HandlePing(chatID)
```

Añadir a `/help`:

```text
/ping - Comprobar que el bot está funcionando
```

### Prueba obligatoria

Con el programa ejecutándose:

```powershell
go run .
```

Enviar al bot:

```text
/ping
```

Resultado esperado:

```text
🏓 Pong!
Bot funcionando correctamente.
```

**No continuar con las fases siguientes hasta que esto funcione.**

---

# 3. FASE B — CONTROL DE SESIÓN DE TRADING

Esta es la siguiente funcionalidad imprescindible.

El objetivo del usuario es:

- iniciar manualmente una sesión;
- detener manualmente una sesión;
- evitar que el bot opere 24/7;
- posteriormente permitir horarios automáticos;
- mantener Telegram operativo aunque la sesión de trading esté detenida.

Actualmente el pipeline procesa señales y puede crear posiciones cuando llegan eventos. No existe un concepto explícito de `TradingSession`.

## Nuevo componente

Crear:

```text
internal/session/session.go
```

Concepto:

```go
type Status string

const (
    StatusStopped Status = "stopped"
    StatusRunning Status = "running"
)

type Manager struct {
    ...
}
```

Debe permitir:

```go
Start()
Stop()
Status()
IsActive()
```

El estado debe ser seguro para concurrencia.

## Comportamiento

Cuando la sesión está detenida:

```text
TradingView → webhook → guardar señal
                         ↓
                    NO abrir posición
```

Cuando está activa:

```text
TradingView → webhook → procesar señal → riesgo → paper position
```

Esto es importante: **detener la sesión no debe apagar Telegram ni el servidor HTTP.**

---

# 4. Nuevos comandos Telegram

Añadir:

```text
/session_start
/session_stop
/session
```

Opcionalmente:

```text
/session_start 09:00 17:00
```

pero esto debe implementarse después de la versión manual.

### `/session_start`

Respuesta:

```text
🟢 Sesión de trading INICIADA

Modo: PAPER
Estrategia: ...
Trading habilitado: sí
```

### `/session_stop`

Respuesta:

```text
🔴 Sesión de trading DETENIDA

No se abrirán nuevas posiciones.
Telegram y recepción de señales continúan activos.
```

### `/session`

Respuesta:

```text
📊 Estado de trading

Sesión: 🟢 ACTIVA
Modo: PAPER
Posiciones abiertas: 1
Señales recibidas: 17
Trades cerrados: 12
```

---

# 5. Dónde debe bloquearse la operación

No se debe bloquear solamente en Telegram.

La comprobación de sesión debe estar en el pipeline central, antes de crear una posición.

Actualmente `internal/processor/processor.go` termina haciendo:

```go
if p.position != nil {
    if err := p.position.OnSignal(ctx, ev); err != nil {
        ...
    }
}
```

La sesión debe introducirse antes de llamar a `PositionController.OnSignal()`.

Ejemplo conceptual:

```go
if p.session != nil && !p.session.IsActive() {
    log.Printf(
        "sesión detenida: señal %s registrada pero no ejecutada",
        ev.Key(),
    )
    return
}
```

La señal debe seguir almacenándose.

Esto permitirá analizar posteriormente:

- cuántas señales llegaron;
- cuáles habrían sido operaciones;
- cuáles fueron ignoradas porque la sesión estaba detenida.

---

# 6. FASE C — PAPER TRADING PROPIO

El repositorio ya tiene `MODE=paper`, pero el concepto actual es básicamente un libro interno de posiciones.

Hay que convertirlo en un **Paper Trading Engine completo**.

Debe registrar:

```text
Account
Position
Trade
Equity snapshot
Session
Strategy
Signal
```

## Métricas mínimas

Cada trade cerrado debe permitir calcular:

- PnL bruto;
- PnL neto;
- comisión;
- slippage;
- duración;
- motivo de salida;
- win/loss;
- R múltiple;
- estrategia;
- símbolo;
- timeframe;
- timestamp.

## KPIs

El bot debe calcular:

```text
Win rate
Loss rate
Profit factor
Expectancy
Average win
Average loss
Largest win
Largest loss
Sharpe
Sortino
Max drawdown
Total return
Number of trades
Consecutive wins
Consecutive losses
```

---

# 7. FASE D — MONITOREO DE POSICIONES PAPER

Actualmente `/positions` muestra las posiciones abiertas, pero falta un monitor que pueda cerrar automáticamente una posición cuando:

```text
Stop Loss
Take Profit
señal contraria
fin de sesión
kill switch
```

Debe existir un componente equivalente a:

```text
internal/paper
```

con:

```go
Open(...)
Close(...)
Update(...)
CheckStops(...)
Account(...)
Trades(...)
```

Las velas recibidas desde Binance/otra fuente pueden utilizarse para comprobar SL/TP.

Importante:

> No debemos cerrar una posición simplemente porque llegó una señal contraria si la estrategia aprendida define otra regla de salida. La regla de salida debe proceder del `StrategySpec`.

---

# 8. FASE E — SESIÓN AUTOMÁTICA

Una vez funcionando `/session_start` y `/session_stop`, añadir:

```text
TRADING_SESSION_ENABLED=true
TRADING_SESSION_START=09:00
TRADING_SESSION_END=17:00
TRADING_SESSION_TIMEZONE=...
```

El bot debe determinar si está dentro del horario.

Añadir comandos:

```text
/session_schedule
```

Ejemplo:

```text
⏰ Horario de trading

09:00 → 17:00
Zona horaria: America/New_York
Estado actual: ACTIVO
```

No se debe depender de la zona horaria del servidor.

---

# 9. FASE F — MEJORAR EL BACKTEST ANTES DEL PAPER TEST

La arquitectura actual del backtest tiene una limitación importante.

`internal/strategymanager/backtest.go` utiliza:

```go
trainEnd := int(float64(len(ks)) * 0.6)
```

y después ejecuta el test sobre:

```go
ks[trainEnd:]
```

Esto es una división train/test sencilla, no un verdadero walk-forward con múltiples folds.

Además, el código comenta que calcula indicadores sobre toda la serie antes de separar el conjunto:

```go
ctx := newEvalContext(ks, spec.Indicators)
```

Para una estrategia que se quiere validar seriamente, debemos evitar cualquier posibilidad de contaminación del test.

## Siguiente versión

Implementar:

```text
Fold 1
Train → Test

Fold 2
Train ampliado → Test siguiente

Fold 3
Train ampliado → Test siguiente
...
```

Y producir:

```text
OOS total
Win rate OOS
Profit factor OOS
Sharpe OOS
MaxDD OOS
Trades OOS
```

Además:

```text
Buy & Hold
Chandelier
Estrategia aprendida
```

deben compararse bajo exactamente los mismos:

- datos;
- fees;
- slippage;
- periodo;
- capital inicial.

---

# 10. FASE G — PAPER TRADING DE 4–6 SEMANAS

Esta debe ser una fase de validación, no una fase de entrenamiento libre.

Flujo:

```text
PDF
 ↓
Extractor
 ↓
LLM
 ↓
StrategySpec
 ↓
Backtest OOS
 ↓
Si pasa
 ↓
Paper Trading
 ↓
4–6 semanas
 ↓
KPIs
 ↓
Decisión
```

La estrategia no debe pasar automáticamente a dinero real por tener un buen backtest.

---

# 11. FASE H — DASHBOARD / REPORTES

Antes de conectar dinero real, Telegram debe poder entregar un reporte.

Nuevo comando:

```text
/report
```

Ejemplo:

```text
📈 REPORTE DE PAPER TRADING

Periodo: 2026-09-15 → 2026-10-15

Trades: 87
Ganadores: 51
Perdedores: 36

Win rate: 58.6%
Profit factor: 1.72
Expectancy: +0.42R

Retorno: +8.7%
Max drawdown: -6.1%

Sharpe: 1.31
Sortino: 1.84

Estado: 🟡 CONTINUAR OBSERVACIÓN
```

Posteriormente:

```text
/report csv
/report json
/report html
```

---

# 12. FASE I — ADAPTADOR WEEX

NO conectar Weex todavía.

Primero definir una interfaz genérica:

```go
type Broker interface {
    GetAccount(ctx context.Context) (Account, error)
    GetPositions(ctx context.Context) ([]Position, error)
    PlaceOrder(ctx context.Context, order Order) (OrderResult, error)
    CancelOrder(ctx context.Context, id string) error
    ClosePosition(ctx context.Context, symbol string) error
}
```

Y separar:

```text
PaperBroker
WeexBroker
FutureBroker2
FutureBroker3
```

El motor de estrategia nunca debe conocer directamente la API de Weex.

Arquitectura:

```text
Strategy
   ↓
Risk Manager
   ↓
Execution Interface
   ├── PaperBroker
   └── WeexBroker
```

Esto permitirá cambiar de:

```env
MODE=paper
```

a:

```env
MODE=live
BROKER=weex
```

sin modificar la estrategia.

---

# 13. FASE J — WEEX LIVE, SOLO DESPUÉS DE VALIDACIÓN

Cuando el paper trading haya terminado:

1. verificar API oficial de Weex;
2. crear credenciales API;
3. limitar permisos;
4. NO permitir retiros;
5. guardar claves solamente en variables de entorno/secret manager;
6. implementar firma de requests;
7. implementar sincronización de órdenes;
8. implementar reconciliación de posiciones;
9. probar primero con cantidades mínimas;
10. añadir kill switch independiente.

Debe existir una protección doble:

```text
Strategy Risk
      +
Execution Risk
      +
Global Kill Switch
```

---

# 14. FASE K — MULTIBROKER

Después de Weex:

```text
Broker interface
 ├── Paper
 ├── Weex
 ├── Broker/Exchange 2
 ├── Broker/Exchange 3
 └── ...
```

La estrategia y el motor de riesgo permanecerán independientes del broker.

---

# 15. CAMBIOS IMPORTANTES QUE YA DEBEN QUEDAR EN EL DISEÑO

## 15.1 TradingView no debe ser tratado como broker API

No implementar:

```text
TradingBot → login TradingView → manipular Paper Trading
```

No introducir scraping del navegador para controlar Paper Trading.

La integración correcta es:

```text
TradingView → Webhook → TradingBot
```

TradingView documenta los webhooks como mecanismo de salida hacia aplicaciones externas.

## 15.2 El Paper Trading interno será nuestra fuente de verdad

El bot deberá conocer exactamente:

```text
balance
equity
positions
orders
trades
fees
slippage
PnL
drawdown
```

Esto permitirá obtener métricas reproducibles.

---

# 16. ORDEN EXACTO DE IMPLEMENTACIÓN

No saltar directamente a Weex.

Orden recomendado:

```text
[A] Reparar/verificar Telegram
        ↓
[B] Session Manager manual
        ↓
[C] Paper Trading Engine completo
        ↓
[D] Monitor SL/TP y cierres
        ↓
[E] Sesiones automáticas
        ↓
[F] Walk-forward backtesting
        ↓
[G] Paper Trading 4–6 semanas
        ↓
[H] Reportes/KPIs
        ↓
[I] Broker interface
        ↓
[J] Weex adapter
        ↓
[K] Live trading controlado
        ↓
[L] Otros brokers
```

---

# 17. PUNTO EN EL QUE DEBEMOS DETENERNOS AHORA

El siguiente cambio de código que considero imprescindible es:

## 1. Verificar/reparar Telegram

y después:

## 2. Implementar `internal/session`

No recomiendo modificar todavía Weex, ML/CNN ni acceso a TradingView Paper Trading.

La razón es que el núcleo de ejecución todavía necesita una separación clara entre:

```text
señal recibida
        ↓
sesión activa
        ↓
riesgo aprobado
        ↓
ejecución
```

Una vez implementada esta separación podremos probar el bot sin dinero real de forma controlada.

---

# 18. PRUEBAS OBLIGATORIAS ANTES DE LA SIGUIENTE FASE

Después de implementar A+B+C:

```powershell
gofmt -w .
go test ./...
go run .
```

Telegram:

```text
/ping
/help
/myid
/session
/session_start
/session
/session_stop
/session
```

Webhook:

```text
POST /health
POST /webhook
```

Debe verificarse que:

```text
sesión detenida → señal se registra pero NO abre posición
sesión activa   → señal puede abrir posición
```

Y:

```text
/session_stop
```

no debe apagar:

```text
Telegram
HTTP server
ingesta
registro de señales
```

---

## Fuentes externas utilizadas para la decisión arquitectónica

TradingView documenta que los webhooks envían un `POST` a una aplicación externa cuando se activa una alerta y que la aplicación receptora debe responder rápidamente. También documenta Paper Trading como un simulador dentro de Supercharts. TradingView además indica actualmente que no ofrece una API para obtener directamente sus datos/valores de indicadores.

Fuentes:

- TradingView — Webhook alerts
- TradingView — Paper Trading
- TradingView — TradingView API / acceso a datos

Estas limitaciones son la razón por la que el proyecto debe utilizar **TradingView como fuente de señales**, no como una cuenta Paper Trading controlada directamente por nuestro programa.
