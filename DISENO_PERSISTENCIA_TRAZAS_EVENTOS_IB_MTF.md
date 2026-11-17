# Diseño de persistencia de trazas y eventos IB MTF

**Fecha:** 2026-11-05  
**Rama:** `integration/ib-shared-engine`  
**Worktree:** `C:\Users\Administrador\AppData\Local\Temp\opencode\tradingbot-ib-shared-engine`  
**Estado:** Propuesta técnica. No implementada.  
**Objetivo:** Cerrar el punto 5: persistir en SQLite las trazas completas y los eventos de las evaluaciones Investing Bulls MTF, relacionándolas con su señal y su operación PAPER, sin duplicar evaluaciones ni señales.

---

## 1. Contexto actual

La Fase 1 fue aceptada. El motor compartido ya demuestra que puede:

- consumir velas MTF de 1h, 15m y 5m;
- ejecutar la estrategia Investing Bulls;
- publicar señales en el bus existente;
- evitar el fallback a Chandelier;
- respetar `StrategyNone`;
- no publicar duplicados dentro del mismo proceso.

El punto 5 sigue parcialmente abierto:

- `investingbulls.EvaluateLiveWithTrace` ya construye una traza completa del pipeline.
- `LiveEngine.OnCandle` usa `EvaluateLive`, que descarta esa traza.
- `LiveEngine` no tiene un recorder persistente.
- `observability` solo emite logs; no guarda eventos en SQLite.
- No existe una relación persistente entre evaluación, señal y operación PAPER.

Restricciones vigentes:

- Solo PAPER.
- Sin operaciones reales.
- Sin commits, push, merge ni workflows.
- No tocar `.github`, `data/bot.db`, artifacts ni estado PAPER existente.
- No modificar la estrategia, filtros, stops, objetivos ni el comportamiento de riesgo.
- Mantener intactos `ResolveLive`, el runtime de Pedro, `StrategyNone`, el controlador de Telegram y los workflows.

---

## 2. Requisitos que cubre el diseño

1. Conservar la traza completa: datos, tendencia, setup, alineación, Fibonacci, confluencia, plan y resultado.
2. Registrar evaluaciones aceptadas y rechazadas, con motivo.
3. Relacionar cada evaluación con estrategia, símbolo, temporalidad, timestamp de vela, señal y operación PAPER.
4. Persistir trazas y eventos en SQLite reutilizando `internal/store`.
5. Evitar evaluaciones y señales duplicadas cuando se reprocesa la misma vela.
6. Obtener resultado y traza de una sola evaluación; no ejecutar la estrategia dos veces.
7. Añadir retención y configuración para evitar crecimiento excesivo de la base.
8. Mantener el selector, el runtime de Pedro, `StrategyNone`, Telegram y los workflows sin cambios funcionales.

---

## 3. Decisión principal

Una única llamada a `EvaluateLiveWithTrace` produce:

- el `SignalEvent`, si la evaluación fue aceptada;
- el `PipelineTrace` completo, siempre;
- el resultado y el motivo final.

Ese resultado se persiste antes de publicar la señal. No se vuelve a ejecutar la estrategia para obtener la traza.

La clave de idempotencia será determinista y persistente:

```text
evaluation_key = v1|strategy_id|symbol|timeframe|bar_ts_unix_ms
```

La autoridad contra duplicados será SQLite. `LiveEngine.lastBar` seguirá existiendo como atajo en memoria, pero dejará de ser la única protección.

---

## 4. Modelo SQLite

Se añaden dos tablas nuevas. No se modifican las tablas existentes.

```sql
CREATE TABLE IF NOT EXISTS ib_evaluations (
    evaluation_key   TEXT PRIMARY KEY,
    strategy_id      TEXT NOT NULL,
    symbol           TEXT NOT NULL,
    timeframe        TEXT NOT NULL,
    bar_ts           INTEGER NOT NULL,

    status           TEXT NOT NULL, -- running|accepted|rejected|error|interrupted
    accepted         INTEGER NOT NULL DEFAULT 0,
    final_reason     TEXT NOT NULL DEFAULT '',
    error_msg        TEXT NOT NULL DEFAULT '',

    trace_json       TEXT NOT NULL DEFAULT '{}',

    source           TEXT NOT NULL DEFAULT '',
    spec_hash        TEXT NOT NULL DEFAULT '',

    signal_idem_key  TEXT NOT NULL DEFAULT '',
    paper_position_id INTEGER NOT NULL DEFAULT 0,

    started_at       INTEGER NOT NULL,
    finished_at      INTEGER NOT NULL DEFAULT 0,
    latency_ms       INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_ib_eval_started
    ON ib_evaluations(started_at);
CREATE INDEX IF NOT EXISTS idx_ib_eval_status
    ON ib_evaluations(status, started_at);
CREATE INDEX IF NOT EXISTS idx_ib_eval_bar
    ON ib_evaluations(strategy_id, symbol, timeframe, bar_ts);
CREATE INDEX IF NOT EXISTS idx_ib_eval_signal
    ON ib_evaluations(signal_idem_key)
    WHERE signal_idem_key <> '';
CREATE INDEX IF NOT EXISTS idx_ib_eval_position
    ON ib_evaluations(paper_position_id)
    WHERE paper_position_id > 0;

CREATE TABLE IF NOT EXISTS ib_events (
    event_key        TEXT PRIMARY KEY,
    evaluation_key   TEXT NOT NULL,
    event_type       TEXT NOT NULL,
    created_at       INTEGER NOT NULL,

    signal_idem_key  TEXT NOT NULL DEFAULT '',
    order_client_id  TEXT NOT NULL DEFAULT '',
    position_id      INTEGER NOT NULL DEFAULT 0,
    detail_json      TEXT NOT NULL DEFAULT '{}',

    FOREIGN KEY(evaluation_key)
        REFERENCES ib_evaluations(evaluation_key)
        ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_ib_events_eval
    ON ib_events(evaluation_key, created_at);
CREATE INDEX IF NOT EXISTS idx_ib_events_order
    ON ib_events(order_client_id)
    WHERE order_client_id <> '';
CREATE INDEX IF NOT EXISTS idx_ib_events_position
    ON ib_events(position_id)
    WHERE position_id > 0;
```

`event_key` será determinista para evitar eventos repetidos:

```text
evaluation_key|event_type|order_client_id|position_id
```

La traza completa se guarda como JSON en `trace_json`. No se normalizan las etapas en otra tabla porque `PipelineTrace` ya contiene todo el pipeline y el volumen es bajo. Si en el futuro se necesita consultar por etapa, se pueden añadir columnas derivadas o una tabla específica.

---

## 5. Relación evaluación → señal → PAPER

No es necesario añadir columnas a `signals`, `orders` o `positions`. Se usan sus claves naturales existentes.

| Relación | Clave |
|---|---|
| Evaluación | `ib_evaluations.evaluation_key` |
| Señal | `ib_evaluations.signal_idem_key` = `signals.idem_key` |
| Orden PAPER | `ib_events.order_client_id` = `orders.client_id` |
| Posición PAPER | `ib_evaluations.paper_position_id` = `positions.id` |
| Evento | `ib_events.evaluation_key` |

Detalles:

- Cuando una evaluación es aceptada, el motor calcula `ev.IdempotencyKey()` y lo guarda en `signal_idem_key`. `signals.idem_key` ya tiene índice único.
- `paper` arma hasta dos límites con `client_id = signal_idem_key + "|1"` y `"|2"`. Cada uno genera un evento `paper_order_armed` con su `order_client_id`.
- Cuando se abre una posición, se actualiza `ib_evaluations.paper_position_id` y se registra `paper_position_opened`.
- Si el proceso se reinicia con límites armados, `paper` puede resolver la evaluación por `order_client_id` usando `ib_events`, sin cambiar la tabla `orders`.
- Las órdenes y posiciones conservan exactamente su comportamiento actual. La persistencia es observacional y no altera riesgo, cierres, stops ni ejecución.

---

## 6. Interfaces nuevas

Nuevo paquete neutral propuesto: `internal/ibeval`.

```go
package ibeval

type Meta struct {
    Key         string
    StrategyID  string
    Symbol      string
    Timeframe   string
    BarTS       time.Time
    Source      string
    SpecHash    string
    StartedAt   time.Time
}

type Result struct {
    Key           string
    Status        string
    Accepted      bool
    FinalReason   string
    Error         string
    TraceJSON     json.RawMessage
    SignalIDemKey string
    FinishedAt    time.Time
    Latency       time.Duration
}

type Event struct {
    EvaluationKey string
    Type          string
    At            time.Time
    SignalIDemKey string
    OrderClientID string
    PositionID    int64
    Detail        json.RawMessage
}

type PaperLink struct {
    EvaluationKey string
    SignalIDemKey string
    OrderClientID string
    PositionID    int64
    EventType     string
}

type Recorder interface {
    BeginEvaluation(ctx context.Context, meta Meta) (started bool, err error)
    FinishEvaluation(ctx context.Context, result Result) error
    AppendEvent(ctx context.Context, event Event) error
    LinkPaper(ctx context.Context, link PaperLink) error
    MarkInterrupted(ctx context.Context, staleBefore time.Time) (int64, error)
    Prune(ctx context.Context, before time.Time, maxRows, batch int) (int64, error)
}
```

Constantes de estado y eventos:

```go
const (
    StatusRunning     = "running"
    StatusAccepted    = "accepted"
    StatusRejected    = "rejected"
    StatusError       = "error"
    StatusInterrupted = "interrupted"
)

const (
    EventEvaluationAccepted = "evaluation_accepted"
    EventEvaluationRejected = "evaluation_rejected"
    EventEvaluationError    = "evaluation_error"
    EventSignalPublished    = "signal_published"
    EventSignalPublishError = "signal_publish_error"
    EventPaperRiskRejected  = "paper_risk_rejected"
    EventPaperOrderArmed    = "paper_order_armed"
    EventPaperOrderFilled   = "paper_order_filled"
    EventPaperOrderCanceled = "paper_order_canceled"
    EventPaperPositionOpen  = "paper_position_opened"
    EventPaperPositionClose = "paper_position_closed"
)
```

`internal/store` implementará `ibeval.Recorder`.

---

## 7. Flujo exacto en `LiveEngine.OnCandle`

Para la rama Investing Bulls:

1. Verificar vela cerrada, selección, símbolo y temporalidad.
2. Si la estrategia es `StrategyNone`, retornar sin escribir nada.
3. Cargar la estrategia activa.
4. Calcular `evaluation_key`.
5. `BeginEvaluation`:
   - `INSERT OR IGNORE` de una fila `running`;
   - si ya existe, retornar sin evaluar y sin publicar;
   - si falla la reserva, retornar error; no evaluar ni publicar.
6. Llamar una sola vez a `EvaluateLiveWithTrace`.
7. Serializar `PipelineTrace` a JSON.
8. `FinishEvaluation` con:
   - `accepted`, `rejected` o `error`;
   - `final_reason`;
   - `trace_json`;
   - `signal_idem_key` si fue aceptada;
   - latencia.
9. Si fue aceptada:
   - añadir `Meta["evaluation_key"]`;
   - publicar una sola vez;
   - registrar `signal_published` o `signal_publish_error`.
10. Si fue rechazada o hubo error, no publicar.

La fila `running` permite detectar un proceso interrumpido. Al arrancar, `MarkInterrupted` marca las filas `running` antiguas como `interrupted`; no se reevalúan automáticamente para no duplicar señales.

Además, se moverá la lectura inicial de `RecentCandles` a la rama no-IB. Actualmente la rama IB hace una lectura de 15m que luego `EvaluateLiveWithTrace` vuelve a hacer; no es una doble evaluación, pero es trabajo innecesario.

---

## 8. Funciones actuales a modificar

| Archivo | Función | Cambio |
|---|---|---|
| `internal/strategymanager/live.go` | `LiveEngine` | añadir `recorder ibeval.Recorder` |
| `internal/strategymanager/live.go` | `NewLiveEngine` / nuevo `SetIBRecorder` | inyectar el recorder sin romper callers |
| `internal/strategymanager/live.go` | `OnCandle` | reservar, evaluar una sola vez con `EvaluateLiveWithTrace`, persistir y publicar |
| `internal/investingbulls/live.go` | `EvaluateLive` | se mantiene como wrapper; `LiveEngine` ya no lo usará |
| `internal/investingbulls/trace.go` | `PipelineTrace` | sin cambios funcionales; se serializa tal cual |
| `internal/store/sqlite.go` | `migrate` | llamar a `migrateIB` |
| `internal/store/ib_events.go` | nuevo | esquema, `BeginEvaluation`, `FinishEvaluation`, `AppendEvent`, `LinkPaper`, `MarkInterrupted`, `Prune` |
| `internal/paper/paper.go` | `Engine` / nuevo `SetIBRecorder` | inyectar sink de eventos |
| `internal/paper/paper.go` | `pendingEntry` | conservar `EvaluationKey` y `OrderClientID` |
| `internal/paper/paper.go` | `OnSignal`, `openPending`, `fillPending`, `persistPending` | registrar riesgo, orden armada, ejecución y posición abierta |
| `internal/paper/paper.go` | `closePosition` | registrar cierre si el sink está configurado |
| `main.go` | wiring | configurar recorder en los dos `LiveEngine` y en `paper.Engine`; marcar interrumpidas y arrancar poda |
| `config/config.go` | `Config` / `Load` | añadir configuración de retención |
| `internal/domain/domain.go` | constantes Meta | añadir `MetaKeyEvaluationKey` |

No se tocan:

- `ResolveLive`.
- `strategyRuntime`, `sharedStrategyFor`, `liveFeedTimeframes`.
- `StrategyNone`.
- El controlador de Telegram.
- Los workflows de `.github`.
- El comportamiento de riesgo, stops, cierres, parciales o ejecución PAPER.

`processor` tampoco necesita cambios: su deduplicación por `BarKey` y el `UNIQUE` de `signals` siguen siendo la segunda y tercera barrera. La primera barrera es la evaluación persistida.

---

## 9. Deduplicación en capas

1. `LiveEngine.lastBar`: evita reevaluar en el mismo proceso.
2. `ib_evaluations.evaluation_key` PK: evita reevaluar tras reinicio o replay.
3. `signals` `UNIQUE(strategy_id, symbol, timeframe, bar_ts)` e `idem_key`: evita señal duplicada.
4. `orders.client_id` PK: evita orden duplicada.
5. `positions` índice único parcial de posición abierta: evita posición duplicada.
6. `ib_events.event_key` PK: evita eventos de enlace duplicados.

Si la misma vela se reprocesa:

- Si ya existe una evaluación aceptada: no se evalúa, no se publica y no se crea otra señal.
- Si ya existe una evaluación rechazada: no se reevalúa; se conserva el motivo original.
- Si es una evaluación `error`: no se reintenta automáticamente.
- Si se cambia de estrategia/modelo, normalmente cambia `strategy_id`; por tanto cambia `evaluation_key` y se registra una evaluación nueva.

---

## 10. Retención y configuración

Añadir a `config.Config`:

```go
IBTraceEnabled       bool
IBTraceRetentionDays int
IBTraceMaxRows       int
IBTracePruneBatch    int
IBTraceRunningTTL    time.Duration
IBTracePruneInterval time.Duration
```

Variables propuestas:

```text
IB_TRACE_ENABLED=true
IB_TRACE_RETENTION_DAYS=90
IB_TRACE_MAX_ROWS=200000
IB_TRACE_PRUNE_BATCH=1000
IB_TRACE_RUNNING_TTL_MINUTES=30
IB_TRACE_PRUNE_INTERVAL_HOURS=6
```

Comportamiento:

- Si `IBTraceEnabled=false`, no se inyecta recorder y el motor mantiene el comportamiento actual sin persistencia.
- Al arrancar: `MarkInterrupted` para filas `running` más antiguas que el TTL.
- Poda periódica cada 6 horas y también al arrancar.
- `Prune` elimina primero eventos antiguos y luego evaluaciones, en lotes, usando `started_at`.
- Después de la poda por antigüedad, aplica el límite `IBTraceMaxRows` eliminando las evaluaciones más antiguas.
- Solo se podan `ib_evaluations` e `ib_events`. No se tocan `signals`, `orders`, `positions`, `candles` ni `strategies`.
- No se ejecutará `VACUUM` automático en caliente; el checkpoint WAL existente sigue igual.

---

## 11. Pruebas necesarias

### Store

- `TestIBBeginEvaluationIsIdempotent`
- `TestIBFinishEvaluationAccepted`
- `TestIBFinishEvaluationRejected`
- `TestIBDuplicateEvaluationDoesNotCreateSecondRow`
- `TestIBEventRelations`
- `TestIBPruneRetention`
- `TestIBMarkInterrupted`
- `TestMigrateIBIdempotent`
- `TestMigrateIBPreservesExistingTables`

### LiveEngine

- `TestLiveEnginePersistsAcceptedTrace`
- `TestLiveEnginePersistsRejectedTraceWithoutSignal`
- `TestLiveEnginePersistsErrorTrace`
- `TestLiveEngineDoesNotEvaluateDuplicateCandle`
- `TestLiveEngineDoesNotEvaluateStrategyTwice`
- `TestLiveEngineStrategyNoneDoesNotRecord`
- `TestLiveEngineTraceContainsAllRequiredStages`
- `TestPipelineTraceJSONRoundTrip`

### Paper

- `TestPaperImmediatePositionLinksEvaluation`
- `TestPaperTwoLimitsLinkOrders`
- `TestPaperFilledLimitLinksPosition`
- `TestPaperRiskRejectionRecordsEvent`
- `TestPaperDoesNotDuplicateOrderOrPosition`
- `TestPaperRecoveredPendingResolvesEvaluationByOrderClientID`

### Integración

- Flujo completo con SQLite temporal: vela aceptada → una evaluación, una señal, una orden/posición, enlaces consultables.
- Misma vela dos veces → una sola evaluación, una sola señal, una sola operación.
- Vela rechazada → traza persistida, sin señal y sin operación PAPER.
- Reinicio simulado → no se reevalúa una vela ya registrada.
- Consulta de relación:

```sql
SELECT e.evaluation_key, e.status, e.final_reason,
       s.id AS signal_id,
       ev.order_client_id,
       e.paper_position_id
FROM ib_evaluations e
LEFT JOIN signals s ON s.idem_key = e.signal_idem_key
LEFT JOIN ib_events ev
       ON ev.evaluation_key = e.evaluation_key
      AND ev.event_type = 'paper_order_armed'
WHERE e.evaluation_key = ?;
```

Comandos previstos:

```powershell
go test ./internal/store ./internal/strategymanager ./internal/investingbulls ./internal/paper ./internal/processor ./config -count=1
go test -race ./internal/store ./internal/strategymanager ./internal/paper -count=1
```

---

## 12. Impacto estimado

### Memoria

- La traza ya se construye actualmente en `EvaluateLiveWithTrace`; el cambio solo la serializa y la escribe.
- Uso transitorio adicional: aproximadamente 2–10 KB por evaluación, dependiendo de `Values`.
- No se mantiene un caché de trazas en memoria.
- El mapa `lastBar` no cambia.

### CPU

- Una escritura de reserva por vela de entrada (`15m`, aproximadamente 96/día/símbolo).
- Una actualización al terminar cada evaluación.
- Cero o pocos eventos adicionales por señal/operación.
- Serialización JSON pequeña.
- No hay segunda ejecución de la estrategia.
- Se elimina la lectura redundante de `RecentCandles` en la rama IB si se aplica el reordenamiento propuesto.

### SQLite

- Crecimiento estimado: una fila de evaluación de ~2–10 KB; a 96 evaluaciones/día son ~0.2–1 MB/día por símbolo, es decir, ~20–90 MB por 90 días antes de eventos.
- Con retención de 90 días y límite de 200.000 filas, el crecimiento queda acotado.
- La base usa WAL y `MaxOpenConns(1)`, por lo que las escrituras se serializan; el volumen añadido es bajo para una temporalidad de 15m.
- La poda no compacta físicamente el archivo necesariamente; el tamaño lógico sí queda acotado. Un `VACUUM` manual podría hacerse en ventana de mantenimiento, pero no forma parte del flujo automático.

### Fallo de persistencia

- Si falla `BeginEvaluation` o `FinishEvaluation`, no se publica la señal: se prefiere no operar sin traza.
- Si falla un evento auxiliar de PAPER, se registra el error y la operación continúa; la relación principal sigue disponible por `signal_idem_key` y `order_client_id`.

---

## 13. Orden de implementación propuesto

1. `internal/ibeval` + migración y store.
2. `LiveEngine.OnCandle` con una sola evaluación y deduplicación persistente.
3. Enlaces de `paper` y eventos de operación.
4. Configuración y poda.
5. Pruebas unitarias, de integración y de migración.

---

## 14. Estado

- Diseño únicamente.
- No se ha modificado código de producción.
- No se han hecho commits, push ni merge.
- No se han ejecutado workflows.
- Implementación pendiente de aprobación.
