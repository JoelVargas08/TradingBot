# TradingBot — Revisión actualizada y siguiente implementación
## Fase B → C: Sesión de trading + Paper Trading controlado

Fecha: 2026-09-15
Repositorio: https://github.com/JoelVargas08/TradingBot
Rama revisada: `main`

---

## 0. Estado real encontrado

La versión actual del repositorio ya incorpora varios cambios importantes respecto a la revisión anterior:

- `handlers/webhook.go` ya acepta `price` como número o string mediante `FlexPrice`.
- Valida `action`.
- Usa comparación de secreto en tiempo constante.
- Publica la señal al bus interno y responde `202`.
- `main.go` ya tiene timeouts HTTP, límite de body, recover/rate-limit y shutdown.
- Existe bus interno.
- Existe deduplicación por estrategia/símbolo/timeframe/barra.
- Existe SQLite.
- Existe ingesta.
- Existe riesgo y posiciones simuladas.
- Existe aprendizaje desde PDF.
- Existe motor ML.
- Existe backtesting con fees/slippage y métricas.
- Existe `MODE=paper|live`.

Esto está documentado también en `PLAN_DE_ACCION.md`, que marca las fases 0–7 como completadas y deja pendiente el paper trading 4–6 semanas/CNN-1D. citeturn1view0

### Pero hay una diferencia importante

El repositorio **todavía no tiene un Session Manager**.

El `processor` recibe una señal, la evalúa, la guarda, notifica y finalmente llama directamente a `PositionController.OnSignal()`. No existe ninguna condición de "sesión de trading activa" entre la señal y la apertura de posición. citeturn2view2

Por tanto, ésta es ahora la siguiente modificación imprescindible.

---

# FASE B — SESSION MANAGER

## 1. Crear `internal/session/session.go`

Crear un paquete independiente para controlar si el motor puede abrir nuevas operaciones.

### API propuesta

```go
package session

import (
    "sync"
    "time"
)

type Manager struct {
    mu      sync.RWMutex
    active  bool
    started time.Time
}

func New(startActive bool) *Manager {
    m := &Manager{
        active: startActive,
    }
    if startActive {
        m.started = time.Now()
    }
    return m
}

func (m *Manager) Start() {
    m.mu.Lock()
    defer m.mu.Unlock()

    if !m.active {
        m.active = true
        m.started = time.Now()
    }
}

func (m *Manager) Stop() {
    m.mu.Lock()
    defer m.mu.Unlock()

    m.active = false
}

func (m *Manager) IsActive() bool {
    m.mu.RLock()
    defer m.mu.RUnlock()
    return m.active
}

func (m *Manager) StartedAt() time.Time {
    m.mu.RLock()
    defer m.mu.RUnlock()
    return m.started
}
```

### Regla importante

`Stop()` NO debe cerrar automáticamente las posiciones existentes.

Su función inicial será:

> impedir nuevas entradas.

Las posiciones ya abiertas seguirán siendo vigiladas por el motor de posiciones.

Más adelante se añadirá una opción explícita de:

```text
/session_stop close
```

para cerrar posiciones al terminar una sesión.

---

# 2. Añadir estado de sesión al `Processor`

Modificar:

```text
internal/processor/processor.go
```

Actualmente:

```go
type Processor struct {
    src      Source
    store    domain.SignalStore
    dedupe   *dedupe.Deduplicator
    eval     domain.Evaluator
    notify   domain.Notifier
    position domain.PositionController
}
```

Cambiar a:

```go
type Processor struct {
    src      Source
    store    domain.SignalStore
    dedupe   *dedupe.Deduplicator
    eval     domain.Evaluator
    notify   domain.Notifier
    position domain.PositionController
    session  SessionGate
}
```

Crear una interfaz pequeña para no acoplar el processor al paquete concreto:

```go
type SessionGate interface {
    IsActive() bool
}
```

Modificar el constructor:

```go
func New(
    src Source,
    store domain.SignalStore,
    eval domain.Evaluator,
    notify domain.Notifier,
    position domain.PositionController,
    session SessionGate,
    dedupeLimit int,
) *Processor
```

---

# 3. No bloquear el almacenamiento de señales

En `handle()` el orden debe quedar:

```text
dedupe
 ↓
evaluate
 ↓
SaveSignal
 ↓
notify
 ↓
¿sesión activa?
 ↓
PositionController
```

Es decir:

### Sesión detenida

```text
TradingView
    ↓
Webhook
    ↓
SignalEvent
    ↓
SQLite
    ↓
Telegram/notificación
    ↓
NO ejecutar operación
```

### Sesión activa

```text
TradingView
    ↓
Webhook
    ↓
SignalEvent
    ↓
SQLite
    ↓
Telegram
    ↓
Risk Manager
    ↓
Paper Position
```

Esto es importante porque queremos conservar las señales aunque el trading esté apagado.

Modificar la parte final de `handle()`:

```go
if p.position == nil {
    return
}

if p.session != nil && !p.session.IsActive() {
    log.Printf(
        "sesión detenida: señal %s registrada pero no ejecutada",
        ev.Key(),
    )
    return
}

if err := p.position.OnSignal(ctx, ev); err != nil {
    log.Printf(
        "error gestionando posición %s: %v",
        ev.Key(),
        err,
    )
}
```

---

# 4. Comportamiento por defecto

Agregar configuración:

```text
TRADING_SESSION_ENABLED
TRADING_SESSION_START_ACTIVE
```

Recomendación inicial:

```env
TRADING_SESSION_ENABLED=true
TRADING_SESSION_START_ACTIVE=false
```

Así el bot arranca con:

```text
Telegram: funcionando
HTTP: funcionando
Webhooks: funcionando
Ingesta: funcionando
Señales: registrándose
Trading: DETENIDO
```

Esto es mucho más seguro para las pruebas.

---

# 5. Modificar `config/config.go`

Añadir:

```go
TradingSessionEnabled     bool
TradingSessionStartActive bool
```

Carga:

```go
TradingSessionEnabled: getEnvBool(
    "TRADING_SESSION_ENABLED",
    true,
),

TradingSessionStartActive: getEnvBool(
    "TRADING_SESSION_START_ACTIVE",
    false,
),
```

---

# FASE B.1 — TELEGRAM

## 6. Añadir `/session`

En `handlers/commands.go`.

El handler debe recibir el manager:

```go
type SessionController interface {
    Start()
    Stop()
    IsActive() bool
    StartedAt() time.Time
}
```

Modificar:

```go
type CommandsHandler struct {
    userManager *models.UserManager
    telegram    *services.TelegramService
    positions   domain.PositionStore
    session     SessionController
}
```

Constructor:

```go
func NewCommandsHandler(
    um *models.UserManager,
    tg *services.TelegramService,
    positions domain.PositionStore,
    session SessionController,
) *CommandsHandler
```

---

# 7. Implementar `/session`

```go
func (ch *CommandsHandler) HandleSession(chatID int64) {
    if ch.session == nil {
        ch.telegram.SendMessage(
            chatID,
            "❌ Control de sesión no disponible",
        )
        return
    }

    if ch.session.IsActive() {
        started := ch.session.StartedAt()

        ch.telegram.SendMessage(
            chatID,
            fmt.Sprintf(
                "🟢 <b>Sesión de trading ACTIVA</b>\n\n"+
                    "Iniciada: %s\n"+
                    "Nuevas operaciones: habilitadas",
                started.Format("2006-01-02 15:04:05"),
            ),
        )
        return
    }

    ch.telegram.SendMessage(
        chatID,
        "🔴 <b>Sesión de trading DETENIDA</b>\n\n"+
            "Nuevas operaciones: deshabilitadas\n"+
            "Las señales continúan registrándose.",
    )
}
```

---

# 8. Implementar `/session_start`

```go
func (ch *CommandsHandler) HandleSessionStart(chatID int64) {
    if ch.session == nil {
        ch.telegram.SendMessage(
            chatID,
            "❌ Control de sesión no disponible",
        )
        return
    }

    ch.session.Start()

    ch.telegram.SendMessage(
        chatID,
        "🟢 <b>Sesión de trading INICIADA</b>\n\n"+
            "Nuevas operaciones: habilitadas\n"+
            "Modo de ejecución: PAPER",
    )
}
```

---

# 9. Implementar `/session_stop`

```go
func (ch *CommandsHandler) HandleSessionStop(chatID int64) {
    if ch.session == nil {
        ch.telegram.SendMessage(
            chatID,
            "❌ Control de sesión no disponible",
        )
        return
    }

    ch.session.Stop()

    ch.telegram.SendMessage(
        chatID,
        "🔴 <b>Sesión de trading DETENIDA</b>\n\n"+
            "No se abrirán nuevas posiciones.\n"+
            "Las posiciones existentes no se cierran automáticamente.",
    )
}
```

---

# 10. Añadir comandos a `main.go`

En el `switch` actual, que actualmente contiene `/start`, `/close`, `/myid`, `/positions`, `/risk`, `/learn`, etc., añadir:

```go
case "session":
    commandsHandler.HandleSession(chatID)

case "session_start":
    commandsHandler.HandleSessionStart(chatID)

case "session_stop":
    commandsHandler.HandleSessionStop(chatID)
```

Actualmente el switch de Telegram está en `main.go` y todavía no contiene estos comandos. citeturn3view0

---

# 11. Actualizar `/help`

Añadir:

```text
/session - Estado de la sesión de trading
/session_start - Iniciar trading
/session_stop - Detener nuevas operaciones
```

---

# FASE B.2 — INICIALIZACIÓN

En `main.go`, crear el manager antes del processor:

```go
tradingSession := session.New(
    cfg.TradingSessionStartActive,
)
```

Después:

```go
processorSvc := processor.New(
    eventBus,
    sqliteStore,
    evaluatorSvc,
    notifierSvc,
    positionCtrl,
    tradingSession,
    10000,
)
```

Actualmente el processor se construye sin ninguna dependencia de sesión. citeturn3view1

---

# 12. Añadir logs de seguridad

Al arrancar:

```go
if tradingSession.IsActive() {
    log.Println("Sesión de trading: ACTIVA")
} else {
    log.Println("Sesión de trading: DETENIDA")
}
```

Esto permitirá saber inmediatamente que el bot NO va a abrir operaciones.

---

# FASE C — MEJORAR EL PAPER TRADING

## 13. Situación actual

El proyecto ya tiene `PositionStore` y `risk.Manager`.

El riesgo actualmente:

- obtiene la cuenta;
- controla drawdown;
- limita posiciones;
- calcula SL;
- calcula TP;
- calcula quantity;
- abre posiciones;
- cierra una posición cuando llega una señal contraria. citeturn4view1

El dominio ya contempla:

```go
Position
Account
Trade
PositionStore
```

pero el modelo de `Position` todavía es demasiado pequeño para el paper trading que necesitamos. citeturn4view2

---

# 14. Extender `domain.Position`

Añadir:

```go
ExitReason string

EntryFee float64
ExitFee  float64

SlippageEntry float64
SlippageExit  float64

GrossPnL float64
NetPnL   float64

RMultiple float64

Duration time.Duration
```

No eliminar los campos actuales.

---

# 15. Extender `domain.Account`

Añadir:

```go
Equity       float64
InitialBalance float64
RealizedPnL  float64
UnrealizedPnL float64
Fees         float64
PeakEquity   float64
MaxDrawdown  float64
```

Actualmente `Account` solamente contiene:

```go
Balance
PeakEquity
UpdatedAt
```

por lo que no alcanza para generar los reportes finales que queremos. citeturn4view2

---

# 16. Crear `internal/paper`

Crear:

```text
internal/paper/paper.go
```

Responsabilidades:

```text
Open
Close
Update
MarkPrice
CheckStops
CheckTakeProfits
Account
OpenPositions
Trades
```

El Paper Engine debe implementar `domain.PositionController` y `domain.PositionStore` cuando sea apropiado, o envolver el store SQLite existente.

---

# 17. Regla de SL/TP

El Paper Engine debe revisar las velas cerradas.

Para LONG:

```text
Low <= StopLoss
    → cerrar por STOP

High >= TakeProfit
    → cerrar por TAKE
```

Para SHORT:

```text
High >= StopLoss
    → cerrar por STOP

Low <= TakeProfit
    → cerrar por TAKE
```

### Caso ambiguo

Si en una misma vela se alcanzan SL y TP:

NO elegir arbitrariamente el resultado favorable.

Para una primera implementación conservadora:

```text
si ambos fueron alcanzados
→ asumir STOP primero
```

y registrar:

```text
ambiguous_bar=true
```

Posteriormente puede añadirse una política configurable.

---

# 18. Cálculo de PnL

LONG:

```text
gross = (exit - entry) * quantity
```

SHORT:

```text
gross = (entry - exit) * quantity
```

Después:

```text
net = gross
    - entryFee
    - exitFee
    - entrySlippageCost
    - exitSlippageCost
```

El balance se actualiza únicamente cuando la posición se cierra.

---

# 19. Peak Equity y Drawdown

No utilizar únicamente:

```text
Balance
```

Para el drawdown.

Debe utilizarse:

```text
Equity = Balance + UnrealizedPnL
```

y:

```text
PeakEquity = max(PeakEquity, Equity)
```

Entonces:

```text
Drawdown =
    (PeakEquity - Equity) / PeakEquity
```

---

# 20. Métricas de Paper Trading

Crear una estructura:

```go
type Performance struct {
    Trades           int
    Wins             int
    Losses           int
    WinRate          float64
    ProfitFactor     float64
    Expectancy       float64
    AverageWin       float64
    AverageLoss      float64
    TotalPnL         float64
    ReturnPct        float64
    MaxDrawdown      float64
    Sharpe           float64
    Sortino          float64
    ConsecutiveWins  int
    ConsecutiveLosses int
}
```

---

# 21. Nuevo comando `/performance`

Debe mostrar:

```text
📊 <b>Paper Trading</b>

Balance: $10,425.30
Equity: $10,438.20

Trades: 37
Win rate: 59.46%
Profit factor: 1.83

PnL: +$425.30
Retorno: +4.25%

Max DD: -3.72%

Ganancia media: +$42.30
Pérdida media: -$31.80

Wins consecutivos: 4
Losses consecutivos: 2
```

---

# FASE C.1 — IMPORTANTE: NO USAR TODAVÍA `MODE=live`

Actualmente `main.go` permite:

```text
MODE=paper
MODE=live
```

pero el propio código reconoce que `MODE=live` todavía no tiene broker externo y continúa usando posiciones simuladas. citeturn3view2

Por seguridad, NO debemos crear todavía un adaptador Weex.

Primero debe existir:

```text
ExecutionBroker interface
```

y únicamente después:

```text
PaperBroker
WeexBroker
```

---

# FASE D — SESIÓN AUTOMÁTICA

Después de que `/session_start` y `/session_stop` estén probados, añadir:

```env
TRADING_SESSION_SCHEDULE_ENABLED=false
TRADING_SESSION_START=09:00
TRADING_SESSION_END=17:00
TRADING_SESSION_TIMEZONE=America/New_York
```

Nunca utilizar la zona horaria del servidor de forma implícita.

---

# FASE E — BACKTESTING

El motor actual ya soporta:

- fees;
- slippage;
- stop;
- take;
- Sharpe;
- Sortino;
- MaxDD;
- Profit Factor;
- Win Rate;
- Buy & Hold. citeturn2view5

No hace falta rehacerlo.

Pero antes de considerar una estrategia del PDF como validada, hay que añadir:

```text
Walk-forward
Purged CV
Embargo
OOS agregado
```

La estrategia no debe aprobarse simplemente por un único backtest.

---

# 22. Umbrales

Los valores actuales del `config.go` son:

```text
MinTrades       = 8
MinWinRate      = 45%
MinProfitFactor = 1.2
MinSharpe       = 0.5
MaxDrawdown     = 30%
```

Son demasiado permisivos para decidir que una estrategia está preparada para trading real. citeturn4view0

Para la etapa de investigación pueden mantenerse.

Pero crear dos niveles:

```text
RESEARCH
PRODUCTION_CANDIDATE
```

Una estrategia `PRODUCTION_CANDIDATE` debería exigir criterios más estrictos y, además, validación OOS.

---

# FASE F — ORDEN DE PRUEBA

Después de modificar el código:

```powershell
gofmt -w .
go test ./...
```

Después:

```powershell
go run .
```

---

# 23. Prueba 1 — Telegram

Enviar:

```text
/help
```

Debe incluir:

```text
/session
/session_start
/session_stop
```

---

# 24. Prueba 2 — sesión inicial

Enviar:

```text
/session
```

Esperado:

```text
🔴 Sesión de trading DETENIDA
```

---

# 25. Prueba 3 — activar

Enviar:

```text
/session_start
```

Luego:

```text
/session
```

Esperado:

```text
🟢 Sesión de trading ACTIVA
```

---

# 26. Prueba 4 — detener

Enviar:

```text
/session_stop
```

Luego:

```text
/session
```

Esperado:

```text
🔴 Sesión de trading DETENIDA
```

---

# 27. Prueba 5 — webhook con sesión detenida

Enviar una señal de prueba.

Esperado:

```text
Webhook → 202
Signal saved → YES
Telegram notification → YES
Position opened → NO
```

---

# 28. Prueba 6 — webhook con sesión activa

Ejecutar:

```text
/session_start
```

Enviar señal válida.

Esperado:

```text
Webhook → 202
Signal saved → YES
Telegram notification → YES
Risk evaluation → YES
Position opened → YES
```

siempre que pase las reglas de riesgo.

---

# 29. Prueba 7 — stop no debe apagar el bot

Después de:

```text
/session_stop
```

debe seguir funcionando:

```text
/positions
/risk
/help
/learn
/strategies
/webhook
/health
```

---

# 30. Arquitectura final de esta etapa

```text
                    TradingView
                         │
                         │ Webhook
                         ▼
                  ┌──────────────┐
                  │ Webhook      │
                  │ Handler      │
                  └──────┬───────┘
                         │
                         ▼
                  ┌──────────────┐
                  │ Event Bus    │
                  └──────┬───────┘
                         │
                         ▼
                  ┌──────────────┐
                  │ Processor    │
                  └──────┬───────┘
                         │
              ┌──────────┴──────────┐
              ▼                     ▼
          SQLite                Telegram
              │
              ▼
        ┌─────────────┐
        │ Session     │
        │ Manager     │
        └──────┬──────┘
               │
        ¿ACTIVA?
          /     \
        NO       SÍ
        │         │
        ▼         ▼
      Guardar    Risk
      señal       │
                  ▼
             Paper Engine
                  │
          ┌───────┴────────┐
          ▼                ▼
       Position          Account
          │                │
          └───────┬────────┘
                  ▼
              Metrics
                  │
                  ▼
              Telegram
```

---

# 31. Lo que NO se debe hacer todavía

No implementar todavía:

```text
❌ API de TradingView Paper Trading
❌ scraping de TradingView
❌ credenciales de Weex
❌ órdenes reales
❌ CNN-1D
❌ reinforcement learning
❌ más PDFs
```

Primero debemos conseguir:

```text
Telegram estable
+
Session Manager
+
Paper Engine fiable
+
métricas
+
backtest OOS
```

---

# 32. Siguiente punto de control

La implementación debe detenerse después de Fase C si cualquiera de estas pruebas falla:

```text
go test ./...

Telegram /session

Webhook con sesión OFF no abre posición

Webhook con sesión ON abre posición

SL funciona

TP funciona

PnL correcto

Drawdown correcto

Balance persistente después de reiniciar
```

Si todas pasan, entonces se puede continuar con:

```text
Fase D → horario automático
Fase E → walk-forward
Fase F → periodo de paper trading
```

---

## Conclusión

La actualización actual **sí incorporó buena parte de las correcciones de infraestructura que faltaban**, pero el proyecto todavía no tiene el control de sesión necesario para comenzar el paper trading real del proyecto.

El cambio inmediato es, por tanto:

**`internal/session` → integración con `processor` → comandos Telegram → Paper Engine → métricas.**

No se debe saltar directamente a Weex.
