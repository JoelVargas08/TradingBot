# TRADINGBOT — PLAN MAESTRO DE CAMBIOS PARA COMPLETAR EL PROYECTO

## Objetivo

Completar el bot con este flujo:

```text
WEEX
 ├── REST histórico
 └── WebSocket tiempo real
          ↓
     CandleStore
          ↓
     1H ─────┐
     15m ────┼── Investing Bulls MTF
     5m ─────┘
          ↓
     Learn
          ↓
     Walk-Forward / OOS
          ↓
     Strategy ACTIVE
          ↓
     LiveEngine
          ↓
     Risk Engine
          ↓
   ┌──────┴──────┐
   │             │
PaperBroker   WEEXBroker
   │             │
   └──────┬──────┘
          ↓
     Telegram / logs
```

La estrategia procede de `ESTRATEGIA THE INVESTING BULLS.pdf`. El PDF es fuente de reglas y no necesita estar adjunto al bot en tiempo de ejecución.

El componente `Learn` actual es optimización determinista + backtesting/validación; no debe tratarse como una red neuronal.

---

## 1. Datos WEEX

### Archivos
Revisar:

```text
internal/ingest/provider.go
internal/ingest/weex.go
internal/ingest/service.go
handlers/market.go
```

### REST histórico

Implementar/completar una API equivalente a:

```go
GetKlines(ctx context.Context, symbol, timeframe string,
    start, end time.Time, limit int) ([]domain.Kline, error)
```

Debe validar símbolo/timeframe, convertir OHLCV, ordenar, eliminar duplicados y marcar `Closed`.

### Backfill paginado

Crear/completar:

```text
internal/ingest/backfill.go
```

Debe descargar bloques, avanzar por timestamp, evitar duplicados, reintentar errores transitorios y permitir miles de velas.

Configurar, por ejemplo:

```env
WEEX_BACKFILL_BARS=5000
```

No depender de una sola llamada REST.

---

## 2. WebSocket WEEX

Revisar el parser contra el formato real de WEEX. No asumir un campo `data` genérico si el payload de kline usa `d[]`.

Crear tipos internos específicos y adaptar el parser sin exponer el formato WEEX al resto del sistema.

Debe soportar:

- conexión;
- suscripción;
- mensajes de datos;
- heartbeat/ping;
- errores;
- cierre;
- reconexión;
- resuscripción;
- backoff;
- backfill del hueco después de reconectar.

Flujo:

```text
connect → subscribe → read
   ↓ error
backoff → reconnect → resubscribe → backfill gap
```

---

## 3. Market Data multi-timeframe

Mantener simultáneamente:

```text
BTCUSDT / 1H
BTCUSDT / 15m
BTCUSDT / 5m
```

La clave lógica debe ser:

```text
symbol + timeframe + candle_start
```

El `CandleStore` debe permitir consultas por símbolo/timeframe y rangos temporales.

---

## 4. Closed candles y no-lookahead

Todo aprendizaje, OOS y señal live debe utilizar exclusivamente velas cerradas.

Regla:

```text
vela abierta → no usar
vela cerrada → sí usar
```

Para una decisión en timestamp `T`:

- 1H debe haber cerrado <= T;
- 15m debe haber cerrado <= T;
- 5m debe haber cerrado <= T;
- jamás utilizar datos posteriores.

Crear tests donde modificar una vela futura no cambie decisiones anteriores.

---

## 5. Investing Bulls

Conservar los conceptos de la fuente:

- estructura;
- tendencia;
- BOS;
- CHOCH;
- Fibonacci;
- imbalance;
- supply/demand;
- order block;
- liquidez/manipulación;
- confluencia.

Niveles Fibonacci:

```text
0, 0.45, 0.50, 0.72, 0.85, 1
```

Zonas:

```text
0.45–0.50
0.72–0.85
```

Objetivos/extensiones:

```text
1.34
1.53
```

Implementación MTF:

```text
1H  → dirección
15m → setup/entrada
5m  → confirmación opcional
```

---

## 6. Estructura, BOS y CHOCH

Archivo:

```text
internal/investingbulls/structure.go
```

Mantener:

```text
TrendUnknown
TrendBullish
TrendBearish
TrendRange
BOS
CHOCH
```

Un break se confirma mediante cierre de vela, no por wick.

Test obligatorio:

```text
wick rompe + cierre no rompe → no hay break
```

---

## 7. Cuatro setups

Archivo:

```text
internal/investingbulls/setup.go
```

Mantener:

```text
SetupCHOCHLong
SetupCHOCHShort
SetupContinuationLong
SetupContinuationShort
```

### CHOCH Long

```text
tendencia bearish
→ ruptura del máximo previo
→ CHOCH alcista
→ Fibonacci
→ zona
→ OB + imbalance
→ entrada
```

### CHOCH Short

```text
tendencia bullish
→ ruptura del mínimo previo
→ CHOCH bajista
→ Fibonacci
→ zona
→ OB + imbalance
→ entrada
```

### Continuation Long

```text
estructura bullish
→ BOS bullish
→ retroceso
→ Fibonacci
→ demand/OB + imbalance
→ entrada
```

### Continuation Short

```text
estructura bearish
→ BOS bearish
→ retroceso
→ Fibonacci
→ supply/OB + imbalance
→ entrada
```

Rechazar setups contrarios a la estructura si no cumplen las condiciones correspondientes.

---

## 8. Fibonacci

Archivo:

```text
internal/investingbulls/fibonacci.go
```

El Fibonacci debe estar asociado al swing/impulso que originó el setup, no a cualquier par de precios.

Guardar:

```text
swing origen
swing destino
dirección
niveles
zonas
targets
```

Evitar que el optimizador utilice un Fibonacci de un movimiento no relacionado con el setup.

---

## 9. Imbalance

Archivo:

```text
internal/investingbulls/imbalance.go
```

Completar:

- detección;
- dirección;
- rango;
- fill parcial;
- fill total;
- invalidación;
- estado activo.

No reutilizar indefinidamente un imbalance consumido.

---

## 10. Order Block

Archivo:

```text
internal/investingbulls/orderblock.go
```

Revisar especialmente `meetsImpulse()`.

El impulso debe ser direccional:

```text
bullish OB → impulso bullish
bearish OB → impulso bearish
```

Guardar:

```text
origin candle
direction
high
low
tested
valid
invalidated
```

No utilizar OB invalidado.

---

## 11. Confluencia

Archivo:

```text
internal/investingbulls/confluence.go
```

La confluencia base es:

```text
Fibonacci + Order Block + Imbalance
```

y debe respetar dirección/setup.

Si `FibonacciPrice` representa realmente el `close` actual, renombrarlo a `ReferencePrice` o almacenar el nivel Fibonacci real. No confundir precio actual con nivel Fibonacci.

---

## 12. Trade Plan

Archivo:

```text
internal/investingbulls/tradeplan.go
```

Calcular:

```text
entry
stop
target
risk
reward
RR
```

Respetar el límite de stop de aproximadamente 2% indicado por la estrategia/configuración.

El stop debe estar relacionado con swing/OB:

```text
long  → debajo
short → encima
```

---

## 13. Costes

Separar:

```text
market price
execution price
slippage
fees
PnL
```

Aplicar slippage una sola vez.

Crear:

```text
TestNoDoubleSlippage
TestFeesAppliedOnce
```

---

## 14. Learn

Archivo:

```text
internal/investingbulls/learn.go
```

Flujo:

```text
datos históricos
→ estructura
→ setups
→ Fibonacci
→ OB
→ imbalance
→ confluencia
→ trade plan
→ simulación
→ métricas
→ optimización
→ candidato
```

No utilizar información futura para construir la señal.

El modelo seleccionado debe quedar congelado para OOS.

---

## 15. Persistir todos los parámetros

`LearnedModel` debe contener todo lo necesario para reconstruirse exactamente.

Agregar si falta:

```go
InitialBalance float64
FeeRate        float64
Slippage       float64
MinTrades      int
```

y guardar:

```text
swing left/right
fib config
confluence config
trade plan config
timeframes
5m confirmation
setups permitidos
```

Eliminar valores hardcoded en `cfgFromModel`.

Objetivo:

```text
modelo persistido → reconstrucción exacta → mismo resultado
```

---

## 16. Reproducibilidad

Guardar:

```text
version
family
symbol
timeframes
learned_at
dataset_hash
dataset_start
dataset_end
dataset_bars
code_version
```

El dataset hash debe identificar exactamente el histórico utilizado.

---

## 17. Métricas

Separar claramente:

```text
IN-SAMPLE
OUT-OF-SAMPLE
```

Métricas mínimas:

```text
Trades
WinRate
ProfitFactor
Sharpe
Sortino
TotalReturn
MaxDrawdown
```

Añadir cuando corresponda:

```text
AverageWin
AverageLoss
Expectancy
GrossProfit
GrossLoss
FeesPaid
```

---

## 18. Estadísticas por setup

Guardar estadísticas independientes para:

```text
CHOCH Long
CHOCH Short
Continuation Long
Continuation Short
```

Por ejemplo:

```go
type SetupStats struct {
    Trades       int
    WinRate      float64
    ProfitFactor float64
    TotalReturn  float64
    MaxDrawdown  float64
}
```

Solo permitir setups con muestras/criterios configurables.

---

## 19. Multi-Timeframe Learn

Archivo:

```text
internal/investingbulls/multitimeframe.go
```

Configuración:

```text
Main    = 1H
Entry   = 15m
Confirm = 5m
```

Flujo:

```text
1H trend
→ 15m setup
→ Fibonacci
→ OB
→ imbalance
→ 5m confirmation opcional
→ entrada
```

La dirección 1H funciona como filtro.

---

## 20. Walk-Forward

Archivo:

```text
internal/investingbulls/walkforward.go
```

Configuración inicial:

```text
Folds              = 4
TrainPct           = 60%
OOSPct             = 10%
StepPct            = 10%
MinOOSTrades       = 3
MinOOSProfitFactor = 1.0
MaxOOSDrawdown     = 8%
MinPositiveFolds   = 3
```

Debe ser cronológico y sin shuffle.

---

## 21. Fixed-Candidate OOS

`ValidateMultiTimeframeCandidate` debe validar exactamente el modelo persistido.

Correcto:

```text
Learn M
↓
persist M
↓
freeze M
↓
OOS de M
↓
PASS / REJECT
```

No volver a entrenar otro modelo durante la validación y activar el primero.

---

## 22. Estados de Strategy

Mantener:

```text
draft
backtesting
candidate
active
rejected
```

Flujo:

```text
draft → backtesting → candidate → OOS
                               ├→ active
                               └→ rejected
```

Solo `active` puede generar ejecución live.

Evitar estrategias activas duplicadas para la misma combinación de familia/símbolo/timeframes.

---

## 23. Live Engine

Archivos:

```text
internal/investingbulls/live.go
internal/strategymanager/live.go
```

Flujo:

```text
vela cerrada
→ actualizar CandleStore
→ cargar estrategia active
→ reconstruir modelo
→ evaluar 1H
→ evaluar 15m
→ evaluar 5m
→ validar setup
→ validar confluencia
→ crear señal
→ Risk Engine
→ Broker
```

No volver a probar Telegram/TradingView para esto.

---

## 24. Idempotencia

Crear identificador de señal/orden a partir de:

```text
strategy_id
symbol
timeframe
candle_timestamp
setup
direction
```

Una repetición del WebSocket o reinicio no debe crear una segunda orden.

Crear:

```text
TestSignalIdempotency
```

---

## 25. PaperBroker

Crear/completar:

```text
internal/broker/paper.go
```

Debe simular:

- órdenes;
- fills;
- posiciones;
- SL;
- TP;
- fees;
- slippage;
- balance;
- PnL.

Paper es obligatorio antes del trading real.

---

## 26. WEEXBroker

Crear:

```text
internal/broker/weex.go
```

API aislada:

```go
PlaceOrder(...)
CancelOrder(...)
GetOrder(...)
GetOpenOrders(...)
GetPosition(...)
GetBalance(...)
```

La estrategia no debe conocer HTTP/REST de WEEX.

---

## 27. Idempotencia de órdenes reales

Usar `clientOrderID` determinista, por ejemplo:

```text
IB-{strategy}-{symbol}-{timestamp}-{direction}
```

Consultar/usar ese ID antes de crear una orden nueva.

---

## 28. Reconciliación

Crear:

```text
internal/execution/reconcile.go
```

Comparar:

```text
estado local
vs
estado WEEX
```

Detectar:

```text
posición local inexistente en WEEX
posición WEEX inexistente localmente
cantidad diferente
orden pendiente desconocida
```

Al recuperar estado, WEEX debe ser la fuente de verdad para la posición real.

---

## 29. Restart Recovery

Al reiniciar:

```text
cargar estado local
→ consultar WEEX
→ reconciliar
→ reconectar WS
→ backfill del hueco
→ reanudar
```

Nunca asumir que un reinicio significa que no hay posición.

---

## 30. Risk Engine

Separar completamente de Investing Bulls.

Debe controlar:

```text
max positions
max daily loss
max drawdown
risk per trade
position size
SL
TP
kill switch
```

La estrategia entrega:

```text
direction
entry
stop
target
```

Risk decide si y cuánto operar.

---

## 31. Kill Switch

Mantener el límite configurado actualmente de 15%, pero asegurar que también bloquee nuevas operaciones reales.

Registrar el evento y gestionar las posiciones existentes según política explícita.

---

## 32. Market Data Health

Crear/completar:

```text
internal/health/market.go
```

Detectar:

```text
WS desconectado
velas atrasadas
timestamps congelados
gaps
OHLC inválido
```

Si el feed está stale:

```text
no abrir nuevas posiciones
```

---

## 33. Secrets y modo LIVE

Nunca guardar secretos en Git.

Variables:

```env
WEEX_API_KEY=
WEEX_API_SECRET=
WEEX_TESTNET=
WEEX_SYMBOL=BTCUSDT
TRADING_MODE=PAPER
LIVE_TRADING_CONFIRM=false
```

Por defecto:

```text
PAPER
```

Exigir para LIVE:

```text
TRADING_MODE=LIVE
WEEX_API_KEY
WEEX_API_SECRET
LIVE_TRADING_CONFIRM=true
```

---

## 34. Telegram

Telegram queda como interfaz de control/observabilidad.

Mantener:

```text
/start
/help
/learnibmtf
/validateibmtf
```

Añadir posteriormente:

```text
/status
/strategy
/positions
/balance
/paper
```

Opcional al final:

```text
/autoib
```

Telegram no debe ser dependencia de la estrategia.

---

## 35. `/autoib`

Cuando el pipeline esté estable:

```text
backfill
→ learn
→ persist candidate
→ OOS
→ PASS → active
→ FAIL → rejected
```

Nunca ejecutar una estrategia no validada.

---

## 36. Reentrenamiento

Configurable:

```env
INVESTING_BULLS_RETRAIN_HOURS=24
```

Un modelo nuevo debe pasar por el mismo flujo:

```text
learn
→ persist candidate
→ OOS
→ activar solo si PASS
```

No reemplazar automáticamente un modelo activo por uno no validado.

---

## 37. Versionado

Guardar:

```text
strategy_id
model_version
dataset_hash
code_version
created_at
```

Conservar versiones anteriores para rollback.

---

## 38. SQLite

Revisar/crear persistencia para:

```text
candles
strategies
backtests
signals
orders
positions
model_versions
```

Usar migraciones sin borrar histórico.

---

## 39. Observabilidad

Logs estructurados para:

```text
MARKET_CONNECTED
MARKET_DISCONNECTED
BACKFILL_STARTED
BACKFILL_COMPLETED
LEARN_STARTED
LEARN_COMPLETED
OOS_STARTED
OOS_COMPLETED
STRATEGY_ACTIVATED
STRATEGY_REJECTED
SIGNAL_CREATED
ORDER_CREATED
ORDER_FILLED
ORDER_REJECTED
POSITION_OPENED
POSITION_CLOSED
KILL_SWITCH
RECONCILIATION
```

Nunca imprimir secretos.

---

## 40. Tests obligatorios

### Investing Bulls

```text
structure
fibonacci
imbalance
orderblock
confluence
tradeplan
setup
learn
walkforward
multitimeframe
```

### No-lookahead

Modificar una vela futura y comprobar que decisiones previas no cambian.

### WEEX

Mockear:

```text
REST
WebSocket
pagination
duplicate candle
open/closed candle
disconnect/reconnect
rate limit
invalid payload
```

### Ejecución

```text
Paper order
SL
TP
fees
slippage
idempotency
reconciliation
restart recovery
```

### Seguridad

Verificar:

```text
secret no aparece en logs
PAPER es default
LIVE exige confirmación
candidate/rejected no ejecutan
duplicados no crean órdenes
```

---

## 41. Integración completa

Crear una prueba de pipeline:

```text
historical data
→ Learn
→ candidate
→ OOS
→ active
→ closed candle
→ signal
→ Risk
→ PaperBroker
→ position
```

Debe funcionar sin Telegram.

---

## 42. Validación local

Desde PowerShell:

```powershell
go test ./...
go vet ./...
go build ./...
go test -race ./...
```

No considerar terminado si alguno falla.

Importante: ejecutar estas pruebas después de las últimas modificaciones; resultados históricos anteriores no sustituyen una ejecución actual.

---

## 43. Validación de datos

Antes de aprender:

```text
symbol correcto
timeframe correcto
timestamps ordenados
sin duplicados
high >= low
open/close dentro del rango
volume >= 0
Closed correcto
```

Datos corruptos deben provocar rechazo, no aprendizaje silencioso.

---

## 44. Configuración centralizada

Evitar constantes dispersas.

Variables iniciales:

```env
INVESTING_BULLS_MAIN_TIMEFRAME=1H
INVESTING_BULLS_ENTRY_TIMEFRAME=15m
INVESTING_BULLS_CONFIRM_TIMEFRAME=5m
INVESTING_BULLS_CONFIRM_5M=true
INVESTING_BULLS_MIN_TRADES=8
INVESTING_BULLS_MAX_STOP=0.02

INVESTING_BULLS_WF_FOLDS=4
INVESTING_BULLS_WF_TRAIN_PCT=0.60
INVESTING_BULLS_WF_OOS_PCT=0.10
INVESTING_BULLS_WF_STEP_PCT=0.10
```

---

## 45. Orden exacto de implementación

### Fase 1 — Datos

1. Corregir WebSocket WEEX.
2. Terminar REST histórico.
3. Implementar paginación/backfill.
4. Mantener 1H/15m/5m simultáneamente.
5. Garantizar closed candles.
6. Validar datos.
7. Implementar reconexión + backfill de gaps.

### Fase 2 — Estrategia

8. Asociar Fibonacci correctamente a swings.
9. Corregir impulso direccional del OB.
10. Revisar confluencia.
11. Revisar clasificación de setups.
12. Revisar trade plan.
13. Eliminar doble slippage.
14. Completar estadísticas por setup.

### Fase 3 — Learn

15. Completar `Learn`.
16. Persistir todos los parámetros.
17. Guardar hash/metadatos del dataset.
18. Completar métricas.
19. Completar MTF Learn.
20. Asegurar no-lookahead.

### Fase 4 — Validation

21. Walk-forward.
22. Fixed-candidate OOS.
23. Estados candidate/active/rejected.
24. Evitar duplicados activos.

### Fase 5 — Execution

25. PaperBroker.
26. Risk Engine.
27. Idempotencia.
28. Reconciliación.
29. Restart recovery.
30. Health/stale-feed.
31. Kill switch.

### Fase 6 — WEEX real

32. WEEXBroker.
33. Autenticación.
34. Órdenes.
35. Cancelaciones.
36. Posiciones.
37. Balance.
38. Protección contra duplicados.

### Fase 7 — Operación

39. `/autoib`.
40. Retraining.
41. Versionado.
42. Observabilidad.
43. Telegram como interfaz.

---

## 46. Criterio de proyecto terminado

```text
[ ] WEEX REST histórico
[ ] WEEX WebSocket
[ ] reconexión
[ ] backfill
[ ] 1H / 15m / 5m
[ ] closed candles
[ ] no-lookahead
[ ] Investing Bulls
[ ] 4 setups
[ ] Fibonacci
[ ] imbalance
[ ] order block
[ ] confluencia
[ ] trade plan
[ ] learning
[ ] persistencia
[ ] dataset reproducible
[ ] walk-forward
[ ] fixed-candidate OOS
[ ] candidate/active/rejected
[ ] PaperBroker
[ ] Risk Engine
[ ] idempotencia
[ ] reconciliation
[ ] restart recovery
[ ] kill switch
[ ] stale-feed protection
[ ] WEEXBroker
[ ] secrets seguros
[ ] tests unitarios
[ ] tests integración
[ ] race tests
[ ] paper trading prolongado
```

---

## 47. Arquitectura final

```text
                    ┌───────────────┐
                    │     WEEX      │
                    └───────┬───────┘
                            │
                 ┌──────────┴──────────┐
                 │                     │
                REST                  WS
                 │                     │
                 └──────────┬──────────┘
                            ↓
                     Market Data Layer
                            ↓
                       CandleStore
                 ┌──────────┼──────────┐
                 ↓          ↓          ↓
                1H         15m         5m
                 │          │          │
                 └──────────┼──────────┘
                            ↓
                  Investing Bulls MTF
                            ↓
                   Learn / Backtest
                            ↓
                   Walk-Forward OOS
                            ↓
                    Strategy Registry
                            ↓
                     ACTIVE MODEL
                            ↓
                       LiveEngine
                            ↓
                       Risk Engine
                            ↓
                 ┌──────────┴──────────┐
                 ↓                     ↓
             PaperBroker          WEEXBroker
                 │                     │
                 └──────────┬──────────┘
                            ↓
                    Orders / Positions
                            ↓
                 Reconciliation Layer
                            ↓
                  SQLite + Telegram
```

---

## 48. Principio de diseño

Mantener separación estricta:

```text
Market Data
     ↓
Strategy
     ↓
Validation
     ↓
Risk
     ↓
Execution
     ↓
Persistence
```

La estrategia no debe depender de:

```text
WEEX HTTP
Telegram
secrets
```

Esto permite cambiar proveedor/broker sin reescribir Investing Bulls.

---

## 49. Qué no implementar todavía

No añadir todavía:

- redes neuronales;
- reinforcement learning;
- LLM dentro del loop de trading;
- cientos de parámetros;
- demasiados indicadores;
- múltiples estrategias nuevas;
- múltiples exchanges.

Primero terminar:

```text
datos fiables
+
estrategia reproducible
+
OOS
+
paper execution
+
seguridad
```

---

## 50. Resultado final esperado

El bot deberá poder:

1. Obtener histórico WEEX.
2. Mantener 1H/15m/5m.
3. Aprender/optimizar Investing Bulls.
4. Guardar el modelo exacto.
5. Validarlo fuera de muestra.
6. Activarlo únicamente si pasa.
7. Recibir nuevas velas.
8. Generar señales MTF.
9. Pasarlas por Risk Engine.
10. Ejecutarlas primero en PaperBroker.
11. Registrar todo.
12. Recuperarse de reinicios.
13. Reconciliar con WEEX.
14. Operar en WEEX real solo cuando `LIVE` sea activado explícitamente.

TradingView/webhooks no forman parte del camino crítico.

---

## 51. Próximo bloque inmediato

La siguiente implementación debe comenzar por:

```text
1. Auditar API real de WEEX.
2. Corregir parser WebSocket d[].
3. Completar backfill paginado.
4. Garantizar 1H/15m/5m.
5. Ejecutar go test ./...
6. Corregir errores de compilación/API de MTF.
7. Completar persistencia exacta del modelo.
8. Completar fixed-candidate OOS.
9. Ejecutar tests.
10. Implementar PaperBroker + Risk.
11. Integrar LiveEngine con PaperBroker.
12. Ejecutar paper trading prolongado.
13. Implementar WEEXBroker.
14. Probar reconciliación.
15. Solo entonces habilitar LIVE.
```
