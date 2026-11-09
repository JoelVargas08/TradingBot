# Diseño V2: persistencia, Outbox y trazabilidad IB MTF

**Fecha:** 2026-11-05  
**Rama:** `integration/ib-shared-engine`  
**Worktree:** `C:\Users\Administrador\AppData\Local\Temp\opencode\tradingbot-ib-shared-engine`  
**Reemplaza a:** `DISENO_PERSISTENCIA_TRAZAS_EVENTOS_IB_MTF.md` (V1, conservado como historial)  
**Estado:** Implementada en `integration/ib-shared-engine` (fases 2–7).  
**Objetivo:** Cerrar el punto 5 sin aceptar evaluaciones sin señal, sin perder señales si el publicador se interrumpe, sin duplicar operaciones PAPER y soportando relación uno-a-varios evaluación → órdenes/posiciones.

---

## 1. Resumen ejecutivo

V2 conserva la arquitectura general aprobada de V1:

- una sola llamada a `EvaluateLiveWithTrace`;
- traza completa persistida;
- tablas nuevas en SQLite;
- sin modificar estrategia, filtros, stops, objetivos, riesgo ni ejecución;
- sin tocar `ResolveLive`, runtime de Pedro, `StrategyNone`, Telegram ni workflows.

V2 añade las cuatro correcciones solicitadas:

1. **Outbox transaccional:** la evaluación `accepted` y la intención de publicar se guardan en la misma transacción SQLite. No existe evaluación aceptada sin fila de Outbox.
2. **Relación uno-a-varios:** se elimina `paper_position_id` como vínculo único y se usan tablas de enlace para N órdenes y N posiciones por evaluación.
3. **Recuperación controlada:** una evaluación interrumpida se marca `interrupted` y no se reevalúa, no publica y no crea órdenes/posiciones. Los reintentos automáticos se aplican al Outbox, nunca a la estrategia.
4. **Identidad versionada:** la identidad de evaluación incluye una huella semántica del modelo/spec, pero la identidad de publicación sigue siendo la identidad de barra actual para conservar la deduplicación de señales, órdenes y posiciones.

Además, V2 introduce un recibo duradero de consumo para que un reintento del Outbox no haga que PAPER procese dos veces la misma señal.

---

## 2. Cambios concretos respecto a V1

| Tema | V1 | V2 |
|---|---|---|
| Evaluación aceptada | `FinishEvaluation` y publicación separadas | `accepted` + Outbox en la misma transacción |
| Publicación | `bus.Publish` después de persistir | Outbox worker publica y espera ack del consumidor |
| Interrupción del publicador | No resuelta | Estados `processing`, `dispatched`, `uncertain`, `dead` y leases |
| Posición PAPER | `ib_evaluations.paper_position_id` | `ib_eval_positions` (N posiciones) |
| Órdenes PAPER | Solo eventos | `ib_eval_orders` (N órdenes) + eventos |
| Identidad | `v1\|strategy\|symbol\|tf\|bar` | `evaluation_key` con `model_fp`; `publication_key` estable por barra |
| Errores | `error` genérico | `error_class`: rechazo, definitivo, transitorio |
| Deduplicación PAPER | Confiaba en `processor` y claves existentes | Recibo duradero por señal/barra + ack del Outbox |
| `processor` | Sin cambios | Acker y recibo de consumo opcionales |
| Evento de publicación | `signal_published` | `signal_enqueued` + `signal_delivered` |

V2 no cambia el comportamiento operativo de PAPER. Los nuevos mecanismos son de persistencia, entrega y auditoría.

---

## 3. Invariantes de V2

1. Una evaluación solo pasa a `accepted` si, en la misma transacción, existe una fila de Outbox para su `publication_key`.
2. Como máximo existe una fila de Outbox por `publication_key`.
3. Como máximo se publica una señal lógica por barra, igual que hoy.
4. `signals` conserva `UNIQUE(strategy_id, symbol, timeframe, bar_ts)` y el índice único de `idem_key`.
5. `orders.client_id` sigue siendo la clave idempotente de órdenes.
6. El índice único de posición abierta por estrategia/símbolo/timeframe sigue vigente.
7. Una evaluación `interrupted` no se reevalúa automáticamente.
8. Un reintento del Outbox nunca vuelve a ejecutar la estrategia.
9. PAPER no ejecuta dos veces la misma `publication_key`.
10. Un fallo auxiliar de eventos/enlaces no cambia el resultado de la estrategia ni de la operación PAPER.
11. Si `IB_TRACE_ENABLED=false`, el comportamiento es exactamente el actual.

---

## 4. Identidad versionada

Se separan tres identidades con responsabilidades distintas.

### 4.1 Identidad de evaluación

```text
model_fp       = sha256(canonical_behavior_spec)[:32]
evaluation_key = v2|strategy_id|symbol|timeframe|bar_ts_unix_ms|model_fp
```

`model_fp` debe representar el comportamiento del modelo, no metadatos de auditoría.

Incluye, canonicalizado y ordenado:

- `CodeVersion`;
- `version`, `family`, `symbol`;
- `Timeframes` (incluye `RequireConfirm`);
- `Base` (parámetros de swings, Fibonacci, confluencia, trade plan, etc.);
- `AllowedSetups`;
- cualquier campo del spec que altere una decisión del pipeline.

Excluye:

- `LearnedAt`;
- `DatasetHash`;
- `DatasetBars`;
- campos de diagnóstico o timestamps que no cambian el comportamiento.

Así, un reentrenamiento con los mismos parámetros no crea una evaluación nueva; un cambio real de parámetros o de versión de código sí.

### 4.2 Identidad de publicación

```text
publication_key = SignalEvent.BarKey()
                = strategy_id|symbol|timeframe|bar_ts_unix_ms
```

Es la misma autoridad lógica que ya usa `signals` para impedir dos señales en la misma barra.

`publication_key` **no incluye** `model_fp`, `setup` ni `direction`. Por tanto, aunque un modelo nuevo produzca otra evaluación para la misma vela, no puede publicar una segunda señal operativa sobre esa barra.

### 4.3 Identidad de señal

```text
signal_idem_key = SignalEvent.IdempotencyKey()
```

Incluye estrategia, símbolo, temporalidad, barra, setup y dirección. Se conserva para enlazar con `signals.idem_key` y con `orders.client_id`.

### 4.4 Casos de deduplicación

| Caso | Evaluación | Publicación | Señal/orden |
|---|---|---|---|
| Misma vela, mismo modelo | `evaluation_key` existe: no se reevalúa | No se crea otra | Sin cambios |
| Misma vela, modelo con parámetros distintos | Nueva evaluación por `model_fp` | `publication_key` ya existe: no se publica otra | Se registra `signal_deduplicated` |
| Misma vela, mismo `publication_key`, dirección/setup distintos | Nueva evaluación posible | Se suprime la publicación | Sin segunda señal |
| Vela nueva | Nueva evaluación | Nueva fila de Outbox | Flujo normal |

La evaluación nueva queda auditada, pero no duplica la operación. Esto es deliberado: la deduplicación actual manda sobre la barra.

---

## 5. Modelo SQLite V2

Se añaden tablas nuevas. No se modifican `signals`, `orders`, `positions`, `candles`, `strategies` ni `account`.

### 5.1 Evaluaciones

```sql
CREATE TABLE IF NOT EXISTS ib_evaluations (
    evaluation_key   TEXT PRIMARY KEY,

    strategy_id      TEXT NOT NULL,
    symbol           TEXT NOT NULL,
    timeframe        TEXT NOT NULL,
    bar_ts           INTEGER NOT NULL,

    model_fp         TEXT NOT NULL,
    code_version     TEXT NOT NULL DEFAULT '',
    source           TEXT NOT NULL DEFAULT '',

    status           TEXT NOT NULL, -- running|accepted|rejected|error|interrupted
    accepted         INTEGER NOT NULL DEFAULT 0,
    final_reason     TEXT NOT NULL DEFAULT '',
    error_class      TEXT NOT NULL DEFAULT '', -- rejected|definitive|transient
    error_msg        TEXT NOT NULL DEFAULT '',

    trace_json       TEXT NOT NULL DEFAULT '{}',

    signal_idem_key  TEXT NOT NULL DEFAULT '',
    publication_key  TEXT NOT NULL DEFAULT '',
    outbox_key       TEXT NOT NULL DEFAULT '',
    signal_outcome   TEXT NOT NULL DEFAULT '', -- queued|deduplicated|delivered|delivery_failed|none

    started_at       INTEGER NOT NULL,
    finished_at      INTEGER NOT NULL DEFAULT 0,
    latency_ms       INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_ib_eval_identity
    ON ib_evaluations(strategy_id, symbol, timeframe, bar_ts, model_fp);

CREATE INDEX IF NOT EXISTS idx_ib_eval_started
    ON ib_evaluations(started_at);
CREATE INDEX IF NOT EXISTS idx_ib_eval_status
    ON ib_evaluations(status, started_at);
CREATE INDEX IF NOT EXISTS idx_ib_eval_bar
    ON ib_evaluations(strategy_id, symbol, timeframe, bar_ts);
CREATE INDEX IF NOT EXISTS idx_ib_eval_publication
    ON ib_evaluations(publication_key)
    WHERE publication_key <> '';
CREATE INDEX IF NOT EXISTS idx_ib_eval_signal
    ON ib_evaluations(signal_idem_key)
    WHERE signal_idem_key <> '';
```

No hay `paper_position_id`. El vínculo con posiciones es uno-a-varios mediante `ib_eval_positions`.

### 5.2 Outbox transaccional

```sql
CREATE TABLE IF NOT EXISTS ib_outbox (
    outbox_key       TEXT PRIMARY KEY,
    publication_key  TEXT NOT NULL,

    evaluation_key   TEXT NOT NULL,
    signal_idem_key  TEXT NOT NULL,
    event_json       TEXT NOT NULL,

    status           TEXT NOT NULL, -- pending|processing|dispatched|done|uncertain|dead
    attempts         INTEGER NOT NULL DEFAULT 0,

    available_at     INTEGER NOT NULL,
    lease_owner      TEXT NOT NULL DEFAULT '',
    lease_until      INTEGER NOT NULL DEFAULT 0,

    dispatched_at    INTEGER NOT NULL DEFAULT 0,
    acked_at         INTEGER NOT NULL DEFAULT 0,
    last_error       TEXT NOT NULL DEFAULT '',

    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,

    UNIQUE(publication_key),
    FOREIGN KEY(evaluation_key)
        REFERENCES ib_evaluations(evaluation_key)
        ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_ib_outbox_ready
    ON ib_outbox(status, available_at);
CREATE INDEX IF NOT EXISTS idx_ib_outbox_lease
    ON ib_outbox(status, lease_until);
```

`outbox_key` es determinista:

```text
outbox_key = v2o|publication_key
```

`event_json` contiene el `SignalEvent` exacto que se publicará, incluyendo en `Meta`:

```text
ib_outbox_key
ib_evaluation_key
ib_publication_key
ib_model_fp
```

### 5.3 Relación N órdenes

```sql
CREATE TABLE IF NOT EXISTS ib_eval_orders (
    evaluation_key   TEXT NOT NULL,
    order_client_id  TEXT NOT NULL,
    signal_idem_key  TEXT NOT NULL DEFAULT '',
    slot             INTEGER NOT NULL DEFAULT 0,
    created_at       INTEGER NOT NULL,

    PRIMARY KEY(evaluation_key, order_client_id),
    FOREIGN KEY(evaluation_key)
        REFERENCES ib_evaluations(evaluation_key)
        ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_ib_eval_orders_client
    ON ib_eval_orders(order_client_id);
CREATE INDEX IF NOT EXISTS idx_ib_eval_orders_eval
    ON ib_eval_orders(evaluation_key);
```

Una orden pertenece a una evaluación; una evaluación puede tener N órdenes.

### 5.4 Relación N posiciones

```sql
CREATE TABLE IF NOT EXISTS ib_eval_positions (
    evaluation_key   TEXT NOT NULL,
    position_id      INTEGER NOT NULL,
    order_client_id  TEXT NOT NULL DEFAULT '',
    signal_idem_key  TEXT NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL,

    PRIMARY KEY(evaluation_key, position_id),
    FOREIGN KEY(evaluation_key)
        REFERENCES ib_evaluations(evaluation_key)
        ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_ib_eval_positions_position
    ON ib_eval_positions(position_id);
CREATE INDEX IF NOT EXISTS idx_ib_eval_positions_eval
    ON ib_eval_positions(evaluation_key);
CREATE INDEX IF NOT EXISTS idx_ib_eval_positions_order
    ON ib_eval_positions(order_client_id)
    WHERE order_client_id <> '';
```

Una posición pertenece a una evaluación; una evaluación puede tener N posiciones.

La implementación actual produce como máximo una posición por evaluación, pero el esquema no impone ese límite. Esto deja espacio para fills parciales, múltiples límites ejecutados o cambios futuros sin migrar de nuevo.

### 5.5 Recibo de consumo PAPER

```sql
CREATE TABLE IF NOT EXISTS ib_signal_receipts (
    publication_key  TEXT PRIMARY KEY,

    outbox_key       TEXT NOT NULL DEFAULT '',
    evaluation_key   TEXT NOT NULL DEFAULT '',
    signal_idem_key  TEXT NOT NULL DEFAULT '',

    should_notify    INTEGER NOT NULL DEFAULT 0,
    should_paper     INTEGER NOT NULL DEFAULT 0,

    notified_at      INTEGER NOT NULL DEFAULT 0,

    paper_status     TEXT NOT NULL DEFAULT 'pending',
    -- pending|processing|done|rejected|skipped|error
    paper_attempts   INTEGER NOT NULL DEFAULT 0,
    paper_error      TEXT NOT NULL DEFAULT '',
    paper_order_ids  TEXT NOT NULL DEFAULT '[]',
    paper_position_ids TEXT NOT NULL DEFAULT '[]',

    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_ib_receipts_status
    ON ib_signal_receipts(paper_status, updated_at);
```

El recibo se crea en la misma transacción que inserta la señal, cuando la señal se inserta por primera vez. Guarda si esa señal debe notificar y si debe pasar a PAPER.

### 5.6 Eventos

```sql
CREATE TABLE IF NOT EXISTS ib_events (
    event_key        TEXT PRIMARY KEY,
    evaluation_key   TEXT NOT NULL,
    event_type       TEXT NOT NULL,
    created_at       INTEGER NOT NULL,

    publication_key  TEXT NOT NULL DEFAULT '',
    outbox_key       TEXT NOT NULL DEFAULT '',
    signal_idem_key  TEXT NOT NULL DEFAULT '',
    order_client_id  TEXT NOT NULL DEFAULT '',
    position_id      INTEGER NOT NULL DEFAULT 0,
    attempt          INTEGER NOT NULL DEFAULT 0,
    detail_json      TEXT NOT NULL DEFAULT '{}',

    FOREIGN KEY(evaluation_key)
        REFERENCES ib_evaluations(evaluation_key)
        ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_ib_events_eval
    ON ib_events(evaluation_key, created_at);
CREATE INDEX IF NOT EXISTS idx_ib_events_outbox
    ON ib_events(outbox_key, created_at)
    WHERE outbox_key <> '';
CREATE INDEX IF NOT EXISTS idx_ib_events_order
    ON ib_events(order_client_id)
    WHERE order_client_id <> '';
CREATE INDEX IF NOT EXISTS idx_ib_events_position
    ON ib_events(position_id)
    WHERE position_id > 0;
```

`event_key` incluye el intento y un hash del detalle:

```text
event_key = evaluation_key|event_type|subject|attempt|sha256(canonical_detail)[:16]
```

Así, un reintento no duplica el mismo evento, pero sí puede registrar un error distinto.

---

## 6. Flujo transaccional en `LiveEngine.OnCandle`

### 6.1 Reserva

1. Verificar vela cerrada, selección, símbolo y temporalidad.
2. Si `StrategyNone`, retornar sin escribir.
3. Cargar la estrategia activa.
4. Calcular `model_fp`, `evaluation_key` y `publication_key`.
5. `BeginEvaluation`:
   - `INSERT OR IGNORE` de una fila `running`;
   - si ya existe, retornar sin evaluar y sin publicar;
   - si falla la reserva, retornar error.

`lastBar` en memoria sigue existiendo como atajo, pero SQLite es la autoridad.

### 6.2 Evaluación

6. Llamar una sola vez a `EvaluateLiveWithTrace`.
7. Serializar `PipelineTrace`.
8. Determinar:
   - `accepted` si `ok == true`;
   - `rejected` si `ok == false` y no hubo error;
   - `error` si hubo error, con `error_class`.

### 6.3 Transacción de cierre

La transacción de cierre es única y hace todo lo siguiente:

- actualizar `ib_evaluations`;
- si `accepted`, insertar o localizar la fila de Outbox;
- insertar eventos de evaluación y de publicación;
- si la publicación ya existía, marcar `signal_outcome='deduplicated'` y enlazar al Outbox existente;
- si no hay outbox nuevo, no publicar.

Pseudo-flujo:

```text
BEGIN IMMEDIATE

UPDATE ib_evaluations
SET status = accepted, accepted = 1, trace_json = ?, ...
WHERE evaluation_key = ? AND status = running

INSERT OR IGNORE INTO ib_outbox (...)
VALUES (outbox_key, publication_key, evaluation_key, signal_idem_key, event_json, pending, ...)

if inserted:
    UPDATE ib_evaluations SET signal_outcome = 'queued', outbox_key = ?
else if existing outbox:
    UPDATE ib_evaluations SET signal_outcome = 'deduplicated', outbox_key = existing
    INSERT event signal_deduplicated
else if signals already has publication_key:
    UPDATE ib_evaluations SET signal_outcome = 'deduplicated'
    INSERT event signal_deduplicated

COMMIT
```

Si la transacción falla:

- la evaluación queda `running`;
- no hay Outbox;
- no hay señal;
- no hay operación PAPER;
- la recuperación la marcará `interrupted`.

La publicación **nunca** ocurre dentro de la transacción.

### 6.4 Rechazo o error

Para `rejected` o `error`:

- se actualiza la evaluación con traza, motivo y `error_class`;
- no se crea Outbox;
- no se publica;
- no se llama a PAPER.

---

## 7. Outbox: publicación y entrega

### 7.1 Estados

| Estado | Significado |
|---|---|
| `pending` | Intención persistida; pendiente de publicar |
| `processing` | Reclamada por un worker con lease |
| `dispatched` | Publicada en el bus; pendiente de ack del consumidor |
| `done` | El consumidor confirmó el procesamiento |
| `uncertain` | Resultado de publicación incierto o lease vencido |
| `dead` | Reintentos agotados; requiere intervención |

`dispatched` no significa “procesada”. Solo `done` significa que el consumidor terminó.

### 7.2 Claim y lease

El worker hace un claim atómico:

```sql
UPDATE ib_outbox
SET status = 'processing',
    lease_owner = ?,
    lease_until = ?,
    attempts = attempts + 1,
    updated_at = ?
WHERE outbox_key = ?
  AND status IN ('pending', 'uncertain')
  AND available_at <= ?
```

Si `RowsAffected() == 1`, el worker es dueño del intento.

### 7.3 Publicación

1. `bus.Publish(ctx, event)`.
2. Si devuelve error:
   - `status='uncertain'`;
   - `last_error`;
   - `available_at = now + backoff(attempts)`;
   - evento `signal_publish_uncertain`.
3. Si devuelve nil:
   - `status='dispatched'`;
   - `dispatched_at=now`;
   - `lease_until=now+ackTimeout`;
   - evento `signal_enqueued`.

El consumidor debe llamar a `AckOutbox(outboxKey)` cuando termine.

### 7.4 Ack

El ack ocurre después de:

- guardar/confirmar la señal;
- notificar, si corresponde;
- ejecutar PAPER, si corresponde;
- confirmar que el recibo PAPER está en estado terminal (`done`, `rejected` o `skipped`).

`AckOutbox`:

```sql
UPDATE ib_outbox
SET status = 'done', acked_at = ?, lease_until = 0, updated_at = ?
WHERE outbox_key = ? AND status IN ('dispatched', 'processing', 'uncertain')
```

El ack es idempotente.

### 7.5 Reintentos y muerte

- Backoff exponencial con jitter, acotado.
- `attempts >= IBOutboxMaxAttempts` → `dead`.
- Un `dead` no se reintenta automáticamente.
- Al pasar a `dead`:
  - actualizar `ib_evaluations.signal_outcome='delivery_failed'`;
  - registrar `signal_dead`;
  - emitir log/alerta operativa.
- Un operador puede reencolar explícitamente un `dead` solo después de revisar el estado.

No se reejecuta la estrategia para recuperar un `dead`.

---

## 8. Interrupciones del publicador

| Punto de interrupción | Estado persistido | Acción al reiniciar | ¿Duplicado? |
|---|---|---|---|
| Antes de reservar | Nada | La vela puede reevaluarse si vuelve a llegar | No hubo efecto |
| Entre reserva y cierre | `running` | `MarkInterrupted`; no se reevalúa | No |
| Dentro de la transacción de cierre, antes de commit | Rollback / `running` | Igual que el anterior | No |
| Después del commit, antes del claim | `accepted` + Outbox `pending` | El worker publica | No |
| Claim hecho, antes de `Publish` | Outbox `processing` | Lease vence, pasa a `uncertain`, reintenta | No |
| `Publish` vuelve error | Outbox `uncertain` | Reintenta con backoff | No |
| `Publish` encola, cae antes de marcar `dispatched` | Outbox `processing` | Lease vence, reintenta | Sí, entrega duplicada |
| Marcado `dispatched`, cae antes del consumidor | Outbox `dispatched` | Ack timeout, reintenta | Sí, entrega duplicada |
| Consumidor guarda señal y cae antes de PAPER | Señal + recibo `pending` | Reintento reanuda PAPER | No |
| Consumidor ejecuta PAPER y cae antes de ack | Recibo `done` | Reintento ve recibo terminal y hace ack | No |
| Ack hecho | Outbox `done` | No se reintenta | No |

La entrega puede ser **al menos una vez**. Los efectos de base de datos son **como máximo una vez** gracias a las claves idempotentes y al recibo.

Queda fuera de la garantía estricta el envío de Telegram: ante una caída después de enviar y antes de marcar `notified_at`, puede repetirse un mensaje. No afecta a señales, órdenes ni posiciones.

---

## 9. Recuperación y clasificación de errores

### 9.1 Arranque

Orden recomendado:

1. Abrir SQLite y ejecutar migraciones.
2. `MarkInterrupted` para filas `running` con `started_at < now - IBTraceRunningTTL`.
3. `RecoverOutboxLeases`:
   - `processing` o `dispatched` con lease vencido → `uncertain`;
   - `available_at` con backoff.
4. Recuperar PAPER (`paper.Recover`) y reconciliar posiciones existentes.
5. Crear `processor` y `notifier`.
6. Arrancar el worker de Outbox.
7. Arrancar la poda.

El worker de Outbox no arranca antes que el consumidor, para no llenar el bus innecesariamente.

### 9.2 Evaluaciones interrumpidas

- `running` vencida → `interrupted`.
- `interrupted` es terminal para el flujo automático.
- No publica, no crea Outbox, no crea órdenes ni posiciones.
- No se reevalúa automáticamente.
- Un reintento manual, si se habilita en el futuro, debe:
  - comprobar que no existe Outbox/señal para `publication_key`;
  - crear una nueva identidad de intento;
  - quedar auditado.

V2 no implementa reintento manual de evaluaciones. Se documenta para evitar que alguien lo confunda con el reintento del Outbox.

### 9.3 Clasificación de errores

| `error_class` | Ejemplo | Política |
|---|---|---|
| `rejected` | Pipeline rechazó la vela | Terminal; no publicar |
| `definitive` | Spec inválido, timeframe vacío, datos insuficientes | Terminal; no publicar |
| `transient` | Error de SQLite, timeout de lectura, contexto cancelado | Terminal para esa evaluación; no hay Outbox. Se audita para intervención |
| `uncertain` (Outbox) | Falló o se desconoce la publicación | Reintento automático del Outbox |
| `dead` (Outbox) | Reintentos agotados | Intervención manual; no reevaluar |

La regla clave: **los reintentos automáticos pertenecen al Outbox, no a la estrategia**.

---

## 10. Deduplicación en capas

| Capa | Clave | Protege |
|---|---|---|
| `LiveEngine.lastBar` | estrategia/símbolo/timeframe + timestamp | Atajo en proceso |
| `ib_evaluations` | `evaluation_key` | No reevaluar misma vela/modelo |
| `ib_outbox` | `UNIQUE(publication_key)` | Una sola intención de publicación por barra |
| `signals` | `UNIQUE(strategy_id, symbol, timeframe, bar_ts)` | Una sola señal por barra |
| `signals.idem_key` | `SignalEvent.IdempotencyKey()` | Repetición exacta de señal |
| `ib_signal_receipts` | `publication_key` | PAPER una sola vez por barra |
| `orders.client_id` | `signal_idem_key` + sufijo | Una sola orden por límite |
| `positions` | índice único parcial | Una sola posición abierta por estrategia/símbolo/timeframe |
| `ib_eval_orders` | `order_client_id` único | Una orden no se enlaza dos veces |
| `ib_eval_positions` | `position_id` único | Una posición no se enlaza dos veces |
| `ib_events` | `event_key` determinista | Eventos repetidos no duplicados |

### 10.1 PAPER no procesa dos veces

La secuencia del consumidor es:

1. Recibir `SignalEvent` con `ib_outbox_key` y `ib_publication_key`.
2. Insertar la señal con `INSERT OR IGNORE` y, si es nueva, crear `ib_signal_receipts` en la misma transacción.
3. Si la señal ya existía y el recibo no existe:
   - no se ejecuta PAPER;
   - se registra anomalía;
   - se hace ack para no dejar el Outbox colgado.
4. Si el recibo existe:
   - si `paper_status` es `done`, `rejected` o `skipped`, no se ejecuta PAPER;
   - si es `pending` o `processing`, se continúa de forma idempotente;
   - si es `error` transitorio, se reintenta.
5. PAPER usa `publication_key` como guardia y las claves existentes (`orders.client_id`, posición abierta única) como segunda barrera.
6. Al terminar PAPER, el recibo pasa a estado terminal y se hace ack del Outbox.

Para la ruta IB, la transición del recibo a `done` y el efecto principal (crear posición o armar límites) deben confirmarse en la misma transacción SQLite. El recibo nunca puede quedar `done` antes del efecto ni después sin que la operación sea reanudable e idempotente.

### 10.2 Notificación

- Solo la primera inserción de señal programa notificación.
- `notified_at` evita reenviar en reintentos normales.
- Una caída entre envío y marca puede producir un duplicado de Telegram; no afecta a PAPER.

---

## 11. Relación uno-a-varios con órdenes y posiciones

### 11.1 Órdenes

- En `paper.OnSignal`, por cada límite armado:
  - persistir/ upsert la orden con su `client_id`;
  - insertar `ib_eval_orders(evaluation_key, order_client_id, slot)`.
- `slot` no es una autoridad de negocio; solo ordena la presentación.
- `UNIQUE(order_client_id)` impide que una orden quede enlazada a dos evaluaciones.

### 11.2 Posiciones inmediatas

- Si la señal abre una posición de mercado:
  - capturar el `position_id` devuelto por `OpenPosition`;
  - insertar `ib_eval_positions(evaluation_key, position_id)`;
  - registrar `paper_position_opened`.

### 11.3 Posiciones desde límites

- `pendingEntry` en memoria añade `EvaluationKey` y `PublicationKey`.
- Al recuperar límites tras reinicio, `recoverPending` resuelve la evaluación por `ib_eval_orders.order_client_id`.
- Al ejecutarse un límite:
  - capturar el `position_id`;
  - insertar `ib_eval_positions` con la evaluación y el `order_client_id`;
  - registrar `paper_order_filled` y `paper_position_opened`.

### 11.4 Cierres

- `paper_position_closed` se registra contra la evaluación de apertura.
- Si el cierre lo provoca una señal contraria, el detalle incluye `closing_evaluation_key` y `closing_publication_key`.
- No se crea un vínculo de posición duplicado.

### 11.5 Consulta

```sql
SELECT e.evaluation_key,
       e.status,
       e.final_reason,
       e.signal_outcome,
       o.order_client_id,
       p.position_id
FROM ib_evaluations e
LEFT JOIN ib_eval_orders o
       ON o.evaluation_key = e.evaluation_key
LEFT JOIN ib_eval_positions p
       ON p.evaluation_key = e.evaluation_key
WHERE e.evaluation_key = ?;
```

---

## 12. Interfaces nuevas

Nuevo paquete neutral: `internal/ibeval`.

```go
type Recorder interface {
    BeginEvaluation(ctx context.Context, meta Meta) (started bool, err error)
    CommitAccepted(ctx context.Context, result AcceptedResult) error
    CommitRejected(ctx context.Context, result RejectedResult) error
    CommitError(ctx context.Context, result ErrorResult) error
    MarkInterrupted(ctx context.Context, staleBefore time.Time) (int64, error)
    Prune(ctx context.Context, before time.Time, maxRows, batch int) (int64, error)
}

type Outbox interface {
    Claim(ctx context.Context, worker string, now time.Time) (OutboxEntry, bool, error)
    MarkDispatched(ctx context.Context, outboxKey string, now time.Time) error
    MarkUncertain(ctx context.Context, outboxKey string, errMsg string, availableAt time.Time) error
    Ack(ctx context.Context, outboxKey string, now time.Time) error
    RecoverLeases(ctx context.Context, now time.Time) (int64, error)
}

type Linker interface {
    LinkOrder(ctx context.Context, link OrderLink) error
    LinkPosition(ctx context.Context, link PositionLink) error
    AppendEvent(ctx context.Context, event Event) error
}

type ReceiptStore interface {
    SaveSignalWithReceipt(ctx context.Context, ev domain.SignalEvent, shouldNotify, shouldPaper bool) (Receipt, bool, error)
    GetReceipt(ctx context.Context, publicationKey string) (Receipt, error)
    ClaimPaper(ctx context.Context, publicationKey string, now time.Time) (Receipt, bool, error)
    CompletePaper(ctx context.Context, publicationKey string, outcome PaperOutcome) error
}
```

`internal/store` implementa estas interfaces y añade las transacciones necesarias.

---

## 13. Archivos y funciones a modificar

| Archivo | Cambio |
|---|---|
| `internal/ibeval/*.go` | Nuevo paquete: tipos, claves, estados, interfaces |
| `internal/store/sqlite.go` | Llamar a `migrateIB` |
| `internal/store/ib_events.go` | Nuevo: migración, evaluaciones, Outbox, enlaces, recibos, eventos, poda |
| `internal/strategymanager/live.go` | Inyectar recorder/Outbox; reservar, evaluar una vez y cerrar transacción |
| `internal/investingbulls/live.go` | `EvaluateLive` se mantiene como wrapper; `LiveEngine` usa `EvaluateLiveWithTrace` |
| `internal/investingbulls/trace.go` | Sin cambios funcionales; se serializa |
| `internal/processor/processor.go` | Recibo duradero, ack opcional y reanudación PAPER |
| `internal/paper/paper.go` | Capturar IDs, enlaces N, `pendingEntry` con evaluación, guardia por recibo |
| `internal/domain/domain.go` | Constantes Meta para Outbox/evaluación/publicación/modelo |
| `main.go` | Wiring de recorder, Outbox worker, recuperación de leases y poda |
| `config/config.go` | Configuración de Outbox, TTL, reintentos y retención |

No se tocan:

- `ResolveLive`.
- `strategyRuntime`, `sharedStrategyFor`, `liveFeedTimeframes`.
- `StrategyNone`.
- Controlador de Telegram.
- Workflows de `.github`.
- Lógica de estrategia, filtros, stops, objetivos, riesgo o ejecución.

---

## 14. Configuración y retención

Añadir a `config.Config`:

```go
IBTraceEnabled        bool
IBTraceRetentionDays  int
IBTraceMaxRows        int
IBTracePruneBatch     int
IBTraceRunningTTL     time.Duration
IBTracePruneInterval  time.Duration

IBOutboxPollInterval  time.Duration
IBOutboxLease         time.Duration
IBOutboxAckTimeout    time.Duration
IBOutboxMaxAttempts   int
IBOutboxBaseBackoff   time.Duration
IBOutboxMaxBackoff    time.Duration
```

Variables propuestas:

```text
IB_TRACE_ENABLED=true
IB_TRACE_RETENTION_DAYS=90
IB_TRACE_MAX_ROWS=200000
IB_TRACE_PRUNE_BATCH=1000
IB_TRACE_RUNNING_TTL_MINUTES=30
IB_TRACE_PRUNE_INTERVAL_HOURS=6

IB_OUTBOX_POLL_SECONDS=1
IB_OUTBOX_LEASE_SECONDS=30
IB_OUTBOX_ACK_TIMEOUT_SECONDS=60
IB_OUTBOX_MAX_ATTEMPTS=20
IB_OUTBOX_BASE_BACKOFF_SECONDS=1
IB_OUTBOX_MAX_BACKOFF_SECONDS=300
```

Reglas:

- `IBTraceEnabled=false`: no se inyecta recorder ni worker; comportamiento actual.
- Poda periódica y al arranque.
- Nunca podar `ib_outbox` en `pending`, `processing`, `dispatched` o `uncertain`.
- Podar `done`/`dead` solo por antigüedad y después de podar sus eventos.
- No podar `ib_signal_receipts` asociados a Outbox no terminal.
- No tocar `signals`, `orders`, `positions`, `candles` ni `strategies`.
- No ejecutar `VACUUM` automático en caliente.

---

## 15. Pruebas necesarias

### Store y migración

- `TestMigrateIBV2Idempotent`
- `TestMigrateIBV2PreservesExistingTables`
- `TestIBCommitAcceptedCreatesOutboxAtomically`
- `TestIBCommitAcceptedRollbackLeavesRunning`
- `TestIBDuplicatePublicationKeyDoesNotCreateSecondOutbox`
- `TestIBEvaluationIdentityIncludesModelFingerprint`
- `TestIBInterruptedDoesNotCreateOutbox`
- `TestIBLinkOrdersAndPositionsN`
- `TestIBOutboxClaimLeaseAndAck`
- `TestIBOutboxLeaseRecovery`
- `TestIBOutboxDeadAfterMaxAttempts`
- `TestIBReceiptIsCreatedWithSignal`
- `TestIBReceiptPreventsSecondPaper`
- `TestIBPruneKeepsNonTerminalOutbox`

### LiveEngine

- `TestLiveEngineAcceptedPersistsTraceAndOutbox`
- `TestLiveEngineRejectedPersistsNoOutbox`
- `TestLiveEngineErrorPersistsNoOutbox`
- `TestLiveEngineDuplicateCandleDoesNotEvaluate`
- `TestLiveEngineDuplicateCandleDoesNotCreateSecondOutbox`
- `TestLiveEngineModelChangeCreatesNewEvaluationButNotNewPublication`
- `TestLiveEngineStrategyNoneDoesNotRecord`
- `TestLiveEngineTraceRoundTrip`

### Outbox

- `TestOutboxPublishErrorSetsUncertain`
- `TestOutboxPublishSuccessWaitsForAck`
- `TestOutboxRetryAfterCrashBeforePublish`
- `TestOutboxRetryAfterCrashAfterPublish`
- `TestOutboxDuplicateDeliveryDoesNotDuplicateSignal`
- `TestOutboxAckIsIdempotent`
- `TestOutboxDeadDoesNotReevaluate`

### Processor y PAPER

- `TestProcessorRetryResumesPaperAfterSignalSaved`
- `TestProcessorRetryDoesNotRepeatPaper`
- `TestPaperReceiptSkipsDoneSignal`
- `TestPaperTwoLimitsLinksAllOrders`
- `TestPaperFillLinksPosition`
- `TestPaperImmediateLinksPosition`
- `TestPaperRiskRejectionMarksReceipt`
- `TestPaperSessionSkipMarksReceipt`
- `TestPaperDoesNotOpenSecondPositionOnReplay`

### Integración

- Flujo completo: vela aceptada → una evaluación, una fila Outbox, una señal, una orden/posición, enlaces consultables.
- Misma vela dos veces → una evaluación, una fila Outbox, una señal, una operación.
- Reinicio simulado después de `accepted` y antes de publicar → la señal se publica una vez.
- Reinicio simulado después de publicar y antes de ack → el consumidor no duplica PAPER.
- Reinicio simulado con `running` → `interrupted`, sin señal ni orden.
- Cambio de spec en la misma vela → nueva evaluación, `signal_outcome='deduplicated'`, sin segunda operación.
- Outbox agotado → `dead`, `delivery_failed`, sin reevaluación.

Comandos previstos:

```powershell
go test ./internal/store ./internal/ibeval ./internal/strategymanager ./internal/investingbulls ./internal/processor ./internal/paper ./config -count=1
go test -race ./internal/store ./internal/ibeval ./internal/strategymanager ./internal/processor ./internal/paper -count=1
```

---

## 16. Impacto estimado

### Memoria

- La traza ya se construye hoy con `EvaluateLiveWithTrace`; V2 solo la serializa.
- El payload del Outbox es el `SignalEvent` serializado, pequeño.
- No se mantiene caché de trazas en memoria.
- El mapa `lastBar` no cambia.

### CPU

- Una reserva y un cierre transaccional por vela de entrada.
- Una fila de Outbox por vela aceptada.
- Polling ligero del Outbox.
- Sin segunda ejecución de la estrategia.
- Sin reevaluación automática de interrumpidas.

### SQLite

- Crecimiento adicional acotado por retención.
- Escrituras serializadas por `MaxOpenConns(1)`.
- El Outbox añade una escritura por publicación y actualizaciones de estado.
- La poda no compacta necesariamente el archivo; el tamaño lógico sí queda acotado.
- `VACUUM` queda fuera del flujo automático.

### Fallos

- Si falla la transacción de cierre, no hay evaluación aceptada ni señal.
- Si falla el Outbox, la evaluación sigue auditada y la señal se reintenta.
- Si se agota el Outbox, se marca `dead` y se alerta; no se reevalúa.
- Si falla un enlace o evento auxiliar, la operación principal continúa.

---

## 17. Orden de implementación propuesto

1. `internal/ibeval` + migración V2 + claves e identidad.
2. `BeginEvaluation` y `CommitAccepted` transaccional con Outbox.
3. `processor`: recibo duradero y ack.
4. `paper`: enlaces N, IDs y guardia por recibo.
5. Outbox worker: claim, lease, publish, retry, ack, dead.
6. Recuperación al arranque, configuración y poda.
7. Pruebas unitarias, de integración, de caída simulada y `-race`.

---

## 18. Estado

- Diseño únicamente.
- No se ha modificado código de producción.
- No se han hecho commits, push ni merge.
- No se han ejecutado workflows.
- Implementación pendiente de aprobación explícita de V2.
