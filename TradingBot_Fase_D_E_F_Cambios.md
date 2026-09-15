# TradingBot — Fase D/E/F: correcciones imprescindibles y continuación del plan

**Repositorio:** `JoelVargas08/TradingBot`  
**Revisión:** `main`  
**Commit actual revisado:** `5fa92b85fe19298d5eb8a676e20b1ad997b950f9`  
**Fecha:** 2026-09-15

## 1. Estado actual

La revisión del commit actual confirma que ya están implementados:

- Telegram mediante long polling.
- `/ping`.
- Session Manager manual.
- `/session`, `/session_start`, `/session_stop`.
- Bloqueo de nuevas entradas cuando la sesión está detenida.
- Paper Engine.
- SL/TP y cierre por señal contraria.
- Fees y slippage simulados.
- Métricas `/performance`.
- SQLite para posiciones/cuenta/señales.
- Backtesting existente.
- Aprendizaje de una estrategia desde PDF.
- Motor ML.
- Modo `paper`.

**Pero no debemos pasar todavía a horario automático ni a Weex.** Hay un problema de arquitectura en la ruta real de ejecución que debe corregirse primero.

---

# 2. BLOQUEADOR PRINCIPAL: Paper Engine no está en la ruta de ejecución de señales

## Problema

En `main.go` actualmente se crea:

```go
positionCtrl = risk.New(sqliteStore, riskCfg)
paperEngine = paper.New(sqliteStore, positionCtrl, paper.Config{})
```

pero el `Processor` recibe:

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

Por tanto:

```text
señal
 ↓
processor
 ↓
risk.Manager
 ↓
SQLite directamente
```

y NO:

```text
señal
 ↓
processor
 ↓
Paper Engine
 ↓
Risk Manager
 ↓
SQLite
```

Esto significa que `paper.Engine.Close()` no participa cuando una señal contraria cierra una posición.

### Consecuencia

Las operaciones cerradas por señal contraria pasan por:

```go
risk.Manager.OnSignal()
```

que ejecuta directamente:

```go
store.ClosePosition(...)
```

y por tanto **no aplica los fees/slippage del Paper Engine**.

Los cierres por SL/TP sí pasan por `paperEngine.CheckStops()` y sí aplican esos costes.

Tenemos entonces dos comportamientos diferentes:

```text
SL/TP                  → fees + slippage
señal contraria        → sin fees + slippage
```

Esto hace que las métricas de paper trading sean inconsistentes.

---

# 3. Corrección arquitectónica obligatoria

No intentar simplemente cambiar una línea sin modificar el flujo interno.

El objetivo debe ser:

```text
                 SignalEvent
                     │
                     ▼
                Processor
                     │
             Session ACTIVE?
                     │
                     ▼
                PaperEngine
                     │
          ┌──────────┴──────────┐
          ▼                     ▼
      Risk Manager          Execution
          │                     │
          │                     ▼
          │                 SQLite
          │
          ▼
      Decision
```

El Risk Manager debe decidir:

```text
¿se puede operar?
qué lado
entrada
SL
TP
quantity
riesgo
```

El Paper Engine debe encargarse de:

```text
abrir
cerrar
fees
slippage
PnL
account
```

---

# 4. Modificar `internal/risk/risk.go`

Separar la decisión de riesgo de la ejecución.

Ya existe:

```go
func (m *Manager) Evaluate(
    ctx context.Context,
    ev domain.SignalEvent,
) (Decision, error)
```

Esta función debe convertirse en la pieza principal para decidir.

Crear una nueva función:

```go
func (m *Manager) EvaluateSignal(
    ctx context.Context,
    ev domain.SignalEvent,
) (Decision, error) {
    return m.Evaluate(ctx, ev)
}
```

Más importante: dejar de hacer que `risk.Manager.OnSignal()` sea el lugar definitivo donde se ejecuta una posición cuando estamos usando Paper Engine.

La lógica de:

```text
cerrar posición contraria
abrir posición nueva
```

debe poder ser llamada por el Paper Engine.

---

# 5. Modificar `internal/paper/paper.go`

`Engine.OnSignal()` debe dejar de ser solamente:

```go
return e.riskCtrl.OnSignal(ctx, ev)
```

Debe implementar el flujo de ejecución paper.

Conceptualmente:

```go
func (e *Engine) OnSignal(
    ctx context.Context,
    ev domain.SignalEvent,
) error {
    if e.riskCtrl == nil {
        return fmt.Errorf("controlador de riesgo no configurado")
    }

    // 1. Buscar posición existente de la misma estrategia/símbolo/TF.
    // 2. Si existe y la señal es contraria:
    //    cerrar usando e.closePosition(...)
    //    para aplicar fees/slippage.
    // 3. Si la señal es del mismo lado:
    //    no duplicar posición.
    // 4. Evaluar riesgo:
    decision, err := e.riskCtrl.Evaluate(ctx, ev)
    // 5. Si no está permitido, devolver el motivo.
    // 6. Si está permitido, abrir con e.store.OpenPosition(...).
}
```

Para abrir:

```go
e.store.OpenPosition(ctx, domain.Position{
    StrategyID: ev.StrategyID,
    Symbol: ev.Symbol,
    Timeframe: ev.Timeframe,
    Side: decision.Side,
    EntryTS: time.Now(),
    EntryPrice: decision.EntryPrice,
    StopLoss: decision.StopLoss,
    TakeProfit: decision.TakeProfit,
    Quantity: decision.Quantity,
    RiskAmount: decision.RiskAmount,
    Status: domain.PositionOpen,
})
```

La comisión de entrada se contabilizará al cierre junto con la comisión de salida, tal como hace actualmente `closePosition`.

---

# 6. Modificar `main.go`

Una vez que `paper.Engine.OnSignal()` sea el controlador real:

```go
processorPositionController := domain.PositionController(positionCtrl)

if paperEngine != nil {
    processorPositionController = paperEngine
}
```

y:

```go
processorSvc := processor.New(
    eventBus,
    sqliteStore,
    evaluatorSvc,
    notifierSvc,
    processorPositionController,
    tradingSession,
    10000,
)
```

La variable `positionCtrl` debe continuar existiendo como dependencia del Paper Engine.

El resultado debe ser:

```text
Risk Manager
   ↓
Paper Engine
   ↓
SQLite
```

y no:

```text
Risk Manager
   ↓
SQLite
```

cuando el modo sea paper.

---

# 7. No utilizar todavía `MODE=live`

Actualmente `MODE=live` no tiene un broker real conectado.

Debe mantenerse la protección:

```text
MODE=paper → Paper Engine
MODE=live  → todavía NO habilitar órdenes reales
```

Hasta que exista una interfaz de broker y un adaptador Weex probado.

No entregar todavía API keys de Weex al programa.

---

# 8. Corrección de métricas del Paper Engine

Archivo:

```text
internal/paper/paper.go
```

Actualmente `Performance()` mezcla:

- `NetPnL` para TotalPnL.
- `GrossPnL` para Profit Factor.
- `RMultiple` para Expectancy.

Esto produce métricas calculadas con bases diferentes.

## Regla

Para evaluar una estrategia en paper trading, usar PnL neto después de:

- comisión;
- slippage.

Por tanto:

```text
grossProfit = suma de NetPnL positivos
grossLoss   = suma absoluta de NetPnL negativos
```

y:

```text
ProfitFactor = grossProfit / grossLoss
```

No usar `GrossPnL` para Profit Factor.

---

# 9. Definir correctamente Expectancy

No mezclar `RMultiple` con PnL monetario.

Usar:

```text
ExpectancyR =
    (WinRate × AverageWinR)
    -
    (LossRate × AverageLossR)
```

Si se quiere también la versión monetaria:

```text
ExpectancyPnL =
    TotalPnL / Trades
```

Añadir ambos campos al dominio:

```go
ExpectancyR   float64
ExpectancyPnL float64
```

o sustituir el campo ambiguo `Expectancy` por uno claramente definido.

---

# 10. Sharpe y Sortino

El código actual calcula estos ratios sobre `NetPnL` por trade.

Eso puede mantenerse para una primera versión, pero debe documentarse como:

```text
trade-based Sharpe
trade-based Sortino
```

No presentarlos como Sharpe anualizado de una serie temporal.

Para una métrica más rigurosa posteriormente:

```text
equity curve diaria
        ↓
retornos diarios
        ↓
Sharpe anualizado
Sortino anualizado
```

Esto debe hacerse antes de comparar formalmente la estrategia con benchmarks.

---

# 11. Drawdown: mejora necesaria

El `Account` tiene:

```go
Balance
Equity
UnrealizedPnL
PeakEquity
MaxDrawdown
```

pero actualmente la cuenta se actualiza principalmente al cerrar posiciones.

Para un paper trading serio debemos actualizar:

```text
Equity = Balance + UnrealizedPnL
```

mientras una posición está abierta.

Crear en `internal/paper/paper.go`:

```go
func (e *Engine) MarkPrice(
    ctx context.Context,
    k domain.Kline,
) error
```

Debe:

1. leer posiciones abiertas;
2. calcular PnL no realizado;
3. actualizar `Account.Equity`;
4. actualizar `PeakEquity`;
5. actualizar `MaxDrawdown`.

Para LONG:

```text
unrealized = (price - entry) * quantity
```

Para SHORT:

```text
unrealized = (entry - price) * quantity
```

Los costes de entrada pueden reflejarse como parte del equity si se decide que las métricas serán "fully loaded"; esa decisión debe mantenerse consistente.

---

# 12. Orden del monitor de velas

En `main.go`, cuando llegue una vela cerrada:

```text
guardar vela
   ↓
MarkPrice
   ↓
CheckStops
   ↓
detectores
   ↓
ML
```

Esto permite que el drawdown se actualice antes de evaluar nuevas señales.

---

# 13. Tests obligatorios para esta corrección

Añadir a:

```text
internal/paper/paper_test.go
```

## Test 1 — señal contraria aplica costes

Abrir:

```text
LONG 100
qty 1
```

Recibir señal:

```text
SHORT 103
```

Verificar:

```text
ExitReason = signal-contrary
GrossPnL = 3
NetPnL < 3
EntryFee > 0
ExitFee > 0
SlippageEntry > 0
SlippageExit > 0
```

## Test 2 — mismo lado no duplica

```text
LONG
+
LONG
```

Resultado:

```text
1 sola posición abierta
```

## Test 3 — sesión detenida

Con `SessionGate` detenido:

```text
signal → saved
signal → notified
position → NO
```

## Test 4 — sesión activa

```text
signal → position abierta
```

## Test 5 — drawdown intratrade

Abrir LONG.

Marcar precio por debajo de entrada.

Verificar:

```text
Equity < Balance
Drawdown > 0
PeakEquity no disminuye
```

---

# 14. Después de corregir el Paper Engine: Fase E — horario automático

Archivo principal:

```text
internal/session/session.go
```

Extender el Session Manager para soportar un horario.

Crear:

```go
type Schedule struct {
    Enabled  bool
    Start    time.Duration
    End      time.Duration
    Location *time.Location
}
```

O, preferiblemente, almacenar `time.Time`/`time.Local` de forma que la zona horaria quede explícita.

Configuración:

```env
TRADING_SESSION_ENABLED=true
TRADING_SESSION_START_ACTIVE=false

TRADING_SESSION_SCHEDULE_ENABLED=false
TRADING_SESSION_START=09:00
TRADING_SESSION_END=17:00
TRADING_SESSION_TIMEZONE=America/New_York
```

---

# 15. Regla del horario

El horario debe ser evaluado con la zona horaria configurada.

Nunca:

```go
time.Now()
```

para decidir la sesión sin convertir primero a la zona configurada.

Usar:

```go
now := time.Now().In(location)
```

---

# 16. Casos que el horario debe soportar

### Horario normal

```text
09:00 → 17:00
```

### Cruce de medianoche

```text
22:00 → 06:00
```

Debe interpretarse como:

```text
22:00–23:59
00:00–06:00
```

### Inicio = fin

No aceptar silenciosamente:

```text
09:00 → 09:00
```

Debe producir error de configuración o interpretarse explícitamente como 24 horas. Para seguridad, se recomienda rechazarlo.

---

# 17. Cierre al finalizar sesión

Por defecto:

```text
fin de sesión
    ↓
NO abrir nuevas posiciones
    ↓
mantener posiciones existentes
```

No cerrar automáticamente.

Añadir posteriormente una opción:

```env
TRADING_SESSION_CLOSE_POSITIONS=false
```

Solo cuando haya tests suficientes.

---

# 18. Telegram para horario

Añadir:

```text
/session_schedule
```

Ejemplo:

```text
⏰ <b>Horario de trading</b>

Estado: ACTIVO
Horario: 09:00 → 17:00
Zona: America/New_York
Cierre al terminar: NO
```

---

# 19. Fase F — Walk-forward real

No saltar todavía a CNN.

El backtest de `internal/strategymanager/backtest.go` usa actualmente una única división train/test.

Hay que convertirlo en múltiples ventanas OOS.

Ejemplo:

```text
Datos completos
│
├── Train 1 ── Test 1
│
├──── Train 2 ── Test 2
│
├──────── Train 3 ── Test 3
│
└──────────── Train 4 ── Test 4
```

Cada fold debe calcular:

```text
Trades
WinRate
ProfitFactor
Sharpe
Sortino
MaxDD
TotalReturn
```

Luego producir:

```text
OOS agregado
```

---

# 20. Evitar lookahead

Nunca calcular parámetros usando datos posteriores al comienzo del fold OOS.

Regla:

```text
TRAIN:
    puede utilizar datos hasta train_end

TEST:
    solamente datos posteriores a train_end
```

Los indicadores que requieran una ventana previa deben utilizar únicamente el histórico disponible antes de cada barra OOS.

---

# 21. Comparativa obligatoria

Para cada fold:

```text
Buy & Hold
Chandelier
Estrategia del PDF
ML/XGBoost si está habilitado
```

Todos con:

```text
mismo capital inicial
mismos fees
mismo slippage
mismo periodo
```

---

# 22. Criterio de paso a Paper Trading

No basta:

```text
un backtest positivo
```

Debe existir:

```text
OOS consistente
+
Profit Factor razonable
+
Sharpe positivo
+
MaxDD controlado
+
número suficiente de trades
```

Los umbrales actuales de investigación pueden mantenerse para no bloquear el desarrollo, pero crear un estado distinto:

```text
RESEARCH_PASS
PRODUCTION_CANDIDATE
```

No activar automáticamente una estrategia para dinero real.

---

# 23. Paper Trading de 4–6 semanas

Una vez que:

```text
Paper Engine correcto
+
Sesión automática
+
Walk-forward
```

funcionen:

```text
PDF
 ↓
StrategySpec
 ↓
Backtest OOS
 ↓
Production Candidate
 ↓
Paper Trading 4–6 semanas
 ↓
Reporte
 ↓
Decisión manual
```

---

# 24. TradingView Paper Trading: aclaración importante

No implementar scraping ni login automatizado de la interfaz de TradingView.

La integración debe ser:

```text
TradingView
    │
    │ alerta
    ▼
TradingBot /webhook
    │
    ▼
Paper Engine propio
```

TradingView documenta sus webhooks como mecanismo para enviar un `POST` a una aplicación externa cuando se activa una alerta. También documenta Paper Trading como un simulador dentro de Supercharts.

Por tanto, para nuestro proyecto el Paper Engine interno será la fuente de verdad de:

```text
balance
equity
positions
trades
PnL
win rate
drawdown
```

TradingView se utilizará como fuente de señales.

---

# 25. Weex queda para después

No crear todavía:

```text
Weex API keys
Live orders
Withdraw permissions
```

Primero crear:

```go
type Broker interface {
    GetAccount(ctx context.Context) (Account, error)
    GetPositions(ctx context.Context) ([]Position, error)
    PlaceOrder(ctx context.Context, order Order) (OrderResult, error)
    CancelOrder(ctx context.Context, id string) error
}
```

y después:

```text
PaperBroker
WeexBroker
```

La estrategia no debe conocer el broker.

---

# 26. Orden exacto desde este punto

```text
1. CORREGIR Paper Engine en la ruta de señales
        ↓
2. CORREGIR métricas netas
        ↓
3. Añadir MarkPrice / equity intratrade
        ↓
4. Tests completos
        ↓
5. Horario automático de sesión
        ↓
6. Tests de timezone y medianoche
        ↓
7. Walk-forward OOS
        ↓
8. Paper Trading 4–6 semanas
        ↓
9. Reportes/KPIs
        ↓
10. Broker interface
        ↓
11. Weex Adapter
        ↓
12. Live con límites estrictos
```

---

# 27. Comandos de validación

Después de aplicar los cambios:

```powershell
gofmt -w .
go test ./...
```

Después:

```powershell
go run .
```

Pruebas Telegram:

```text
/ping
/help
/session
/session_start
/session
/session_stop
/session
```

Prueba de ejecución:

```text
sesión OFF
→ señal registrada
→ NO posición

sesión ON
→ señal registrada
→ riesgo
→ Paper Engine
→ posición
```

Prueba de salida:

```text
SL
TP
señal contraria
```

Todos deben generar PnL neto con costes.

---

# 28. Punto de parada

**No continuar a Weex ni a CNN hasta que estos tests pasen:**

```text
go test ./...

Telegram responde

Session OFF bloquea entradas

Session ON permite entradas

SL cierra correctamente

TP cierra correctamente

señal contraria cierra correctamente

señal contraria aplica fees/slippage

equity intratrade funciona

drawdown funciona

balance persiste

restart no corrompe SQLite
```

Una vez superados, la siguiente implementación será el horario automático de sesión y después el walk-forward OOS.
