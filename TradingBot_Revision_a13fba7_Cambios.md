# TradingBot — Revisión del commit a13fba7

Repositorio: `JoelVargas08/TradingBot`
Commit revisado: `a13fba7d20051bf442146b2c341dee47c4715eed`

## Resultado

El commit incorpora Paper Engine, métricas netas, `MarkPrice`, configuración de horario y walk-forward. Sin embargo, todavía hay bloqueadores que deben corregirse antes de considerar válida la fase y antes de pasar a Paper Trading prolongado o Weex.

## 1. BLOQUEADOR — Walk-forward incorrecto

Archivo: `internal/strategymanager/backtest.go`

Se crea un `evalContext` con toda la serie:

```go
ctx := newEvalContext(ks, spec.Indicators)
```

pero `runEquity()` recibe `testKS`, cuyo índice vuelve a empezar en cero. Así, el segundo/tercer fold consulta los indicadores de las primeras velas de la serie completa.

### Cambio

Modificar:

```go
runEquity(ctx, spec, testKS, 0.001)
```

a una firma con offset:

```go
runEquity(ctx, spec, testKS, testStart, costs)
```

y usar:

```go
globalIndex := testStart + i
```

para `ruleMatches()` y las condiciones.

Añadir `TestWalkForwardUsesGlobalIndicatorIndex`.

## 2. BLOQUEADOR — no existe TRAIN real

El código llama al proceso "walk-forward", pero ningún fold usa una fase TRAIN para calibrar parámetros.

La estructura debe quedar explícitamente:

```text
TRAIN 1 → TEST 1
TRAIN 1+2 → TEST 2
TRAIN 1+2+3 → TEST 3
...
```

Aunque la estrategia actual no optimice parámetros, mantener la separación TRAIN/OOS y documentarla. No permitir que parámetros futuros entren en el OOS.

Añadir `TestWalkForwardHasNoLookahead`.

## 3. BLOQUEADOR — TotalReturn agregado incorrecto

Actualmente:

```go
math.Exp(sum(allRets)) - 1
```

pero `allRets` contiene retornos simples, no log-retornos.

Cambiar a composición:

```go
equity := 1.0
for _, r := range allRets {
    equity *= 1 + r
}
aggReturn := (equity - 1) * 100
```

Añadir `TestAggregatedReturnCompoundsSimpleReturns`.

## 4. Profit Factor del backtest mezcla bruto/neto

`runEquity()` determina wins/losses usando `net`, pero calcula Profit Factor con `gross`.

Debe calcularse todo con PnL neto:

```text
net profit / net loss
```

Añadir `TestProfitFactorUsesNetReturns`.

## 5. Costes del backtest

Actualmente se resta un único `fee` por trade:

```go
net := gross - fee
```

Debe existir una configuración:

```go
type BacktestCosts struct {
    FeeRate      float64
    SlippageRate float64
}
```

y modelar entrada/salida, manteniendo el mismo criterio que Paper Engine.

## 6. BLOQUEADOR — horario automático no es realmente automático

`session.Manager.IsActive()` exige primero:

```go
m.active == true
```

y `main.go` usa por defecto:

```env
TRADING_SESSION_START_ACTIVE=false
```

Por tanto activar:

```env
TRADING_SESSION_SCHEDULE_ENABLED=true
TRADING_SESSION_START=09:00
TRADING_SESSION_END=17:00
```

no hace que el bot se active a las 09:00.

### Cambio

Separar:

```text
manual state
automatic schedule
manual override
```

Recomendación:

```go
type Override int

const (
    OverrideNone Override = iota
    OverrideStart
    OverrideStop
)
```

Con horario activo:

```text
dentro de ventana → ACTIVE
fuera de ventana → INACTIVE
```

y `/session_start` y `/session_stop` actúan como override explícito.

## 7. Reinicio con horario

Al reiniciar a las 12:00, si el horario es 09:00–17:00, el bot debe quedar ACTIVE automáticamente.

No depender de `START_ACTIVE`.

## 8. StartedAt automático

Actualizar `StartedAt` cuando haya transición:

```text
INACTIVE → ACTIVE
```

por el horario, no solamente con `/session_start`.

## 9. Evaluación de transiciones

Añadir:

```go
func (m *Manager) Run(ctx context.Context)
```

con ticker (por ejemplo, 1 minuto) para detectar:

```text
ACTIVE → INACTIVE
INACTIVE → ACTIVE
```

No cerrar posiciones al terminar la sesión.

Añadir tests de horario normal, medianoche, timezone, reinicio y overrides manuales.

## 10. BLOQUEADOR — Kill Switch usa Balance

`internal/risk/risk.go` usa:

```go
drawdown(acc.Balance, acc.PeakEquity)
```

Debe usar:

```go
drawdown(acc.Equity, acc.PeakEquity)
```

porque ya existe PnL no realizado.

Añadir `TestKillSwitchUsesEquity`.

## 11. Telegram `/risk`

Mostrar:

```text
Balance
Equity
Unrealized PnL
Peak Equity
Drawdown
Max Drawdown
```

y calcular drawdown sobre `Equity`, no Balance.

## 12. BLOQUEADOR — MarkPrice pierde otros símbolos

`internal/paper/paper.go` recalcula `UnrealizedPnL` solo con la vela actual y asigna ese resultado a toda la cuenta.

Con BTC y ETH:

```text
vela BTC → desaparece PnL de ETH
vela ETH → desaparece PnL de BTC
```

### Cambio

Mantener últimos precios por:

```text
symbol + timeframe
```

y calcular el PnL de TODAS las posiciones abiertas.

Recomendado: persistir esos marks en SQLite para sobrevivir reinicios.

Tabla sugerida:

```sql
CREATE TABLE IF NOT EXISTS marks (
    symbol TEXT NOT NULL,
    timeframe TEXT NOT NULL,
    price REAL NOT NULL,
    ts INTEGER NOT NULL,
    PRIMARY KEY(symbol, timeframe)
);
```

Añadir `TestMarkPriceMultipleSymbols`.

## 13. Inicializar correctamente Account

Cuando no existe cuenta, crear y persistir:

```go
Account{
    Balance:        starting,
    Equity:         starting,
    InitialBalance: starting,
    PeakEquity:     starting,
}
```

No esperar al primer cierre.

Añadir `TestAccountInitializesWithEquity`.

## 14. Persistencia incompleta de BacktestResult

`domain.BacktestResult` ya contiene:

```text
Sortino
Folds
OOSFolds
Status
```

pero SQLite no guarda esos campos.

Añadir columnas:

```text
sortino REAL
folds INTEGER
oos_folds TEXT
status TEXT
```

Guardar `OOSFolds` como JSON.

Actualizar `LastBacktest()` para recuperar todo.

## 15. Ciclo de estrategia correcto

Documentar:

```text
DRAFT
 ↓
BACKTESTING
 ↓
CANDIDATE
 ↓
revisión manual
 ↓
ACTIVE
```

No activar automáticamente una estrategia recién aprendida.

## 16. `/activate` anunciado pero no conectado

`handlers/strategies.go` muestra:

```text
/backtest ID · /activate ID
```

pero `main.go` no tiene `case "activate"`.

Implementar:

```text
/activate <id>
```

y permitirlo únicamente para:

```text
CANDIDATE → ACTIVE
```

No activar silenciosamente una estrategia `REJECTED`.

Añadir tests.

## 17. MODE=live

Actualmente `MODE=live` sigue simulando operaciones.

Hasta que exista Weex, `MODE=live` debe:

```text
rechazar el arranque
```

o quedar explícitamente deshabilitado.

Nunca debe parecer que está operando con dinero real cuando solo escribe en SQLite.

## 18. Antes de Weex

Crear:

```go
type Broker interface {
    GetAccount(ctx context.Context) (Account, error)
    GetPositions(ctx context.Context) ([]Position, error)
    PlaceOrder(ctx context.Context, order Order) (OrderResult, error)
    CancelOrder(ctx context.Context, id string) error
}
```

Después:

```text
PaperBroker
WeexBroker
```

La estrategia no debe depender de Weex.

## 19. Validación obligatoria

Después de los cambios:

```powershell
gofmt -w .
go test ./...
```

Probar además:

```text
/ping
/session
/session_schedule
/session_start
/session_stop
/performance
/strategies
/strategy ID
/backtest ID
/activate ID
```

## 20. Orden de trabajo

```text
1. Corregir índices OOS
2. Corregir TRAIN/OOS
3. Corregir TotalReturn
4. Corregir Profit Factor neto
5. Costes completos del backtest
6. Corregir horario automático
7. Persistencia/reinicio de sesión
8. Kill Switch con Equity
9. MarkPrice multi-símbolo
10. Persistencia de marks
11. Persistencia completa de backtests
12. /activate
13. Bloquear MODE=live
14. go test ./...
15. Paper Trading prolongado
16. Broker interface
17. Weex
```

**Punto de parada:** no pasar a Weex ni considerar la estrategia apta para dinero real hasta que estos tests y validaciones estén verdes.
