# Estado de integración IB MTF (V2)

Fecha: 2026-11-09
Rama: `integration/ib-shared-engine`
Base: `origin/fix/telegram-controller` (commit `3d47946`, verificado como ancestro)
Último commit de partida: `9ffaed7 docs: agregar diseño V2 de persistencia IB MTF`

Este documento resume qué quedó implementado de la V2 de persistencia, Outbox y
trazabilidad de Investing Bulls MTF, qué se validó y qué falta antes de fusionar.

## 1. Resumen

La integración mantiene **un solo motor** (`strategymanager.LiveEngine`) evaluando
Investing Bulls. Ese motor consume las velas multi-timeframe, evalúa la estrategia
compartida, reserva la identidad de la evaluación y publica la señal a través de
un **Outbox transaccional**. El consumidor (`processor`) aplica un **recibo
duradero** antes de notificar y abrir operaciones PAPER, de modo que los
reintentos no duplican efectos. La ruta PAPER enlaza evaluación → señal → orden →
posición → evento de cierre.

No se modificó la lógica de estrategia, filtros, stops, targets, riesgo ni
ejecución. No se añadió fallback a Chandelier. `StrategyNone` no produce señales
ni escribe persistencia.

## 2. Flujo implementado

1. `LiveEngine.OnCandle` detecta la vela de entrada/confirmación según la
   configuración MTF.
2. Antes de evaluar, reserva la evaluación con `BeginEvaluation`
   (`ib_evaluations.status = running`). Si la clave ya existe y no está
   interrumpida, el bar se omite.
3. Ejecuta `investingbulls.EvaluateLiveWithTrace` (única evaluación).
4. `FinishEvaluation` cierra la evaluación y, solo si fue aceptada, inserta el
   mensaje de Outbox **en la misma transacción**. Si la evaluación fue rechazada
   o falló, no hay Outbox.
5. `OutboxWorker` reclama mensajes con lease, publica en el bus y confirma
   (`ack`). Ante caída después de publicar, el mensaje se reentrega hasta el ack.
   Tras `max_attempts` pasa a `dead`.
6. `processor.handleIB` valida la evaluación y el Outbox, crea el recibo
   (`ib_signal_receipts`) de forma atómica con la señal y reclama el lease PAPER.
   Si el lease expira sin completar, se reintenta PAPER sin renotificar.
7. PAPER registra `armed`/`filled`/`opened`/`closed` en `ib_paper_events` y
   enlaza N órdenes y N posiciones con la evaluación de apertura.
8. Al arrancar, `MarkInterrupted` marca como `interrupted` las evaluaciones
   `running` más antiguas que `IB_RUNNING_TTL`. No se reevalúan ni se publican
   automáticamente: quedan visibles para diagnóstico.

## 3. Archivos principales

| Área | Archivos |
| --- | --- |
| Identidad y estados | `internal/ibeval/ibeval.go` |
| Outbox worker | `internal/ibeval/worker.go` |
| Esquema y transacciones | `internal/store/ib_events.go` |
| Motor compartido | `internal/strategymanager/live.go` |
| Consumo duradero | `internal/processor/processor.go` |
| Trazabilidad PAPER | `internal/paper/paper.go` |
| Arranque y wiring | `main.go`, `config/config.go` |
| Diagnóstico | `ib_diagnostics.go`, `handlers/commands.go`, `telegram_dispatch.go` |
| Documentación | `docs/INVESTING_BULLS.md`, `docs/TELEGRAM_CONTROLLER.md`, `README.md` |

## 4. Esquema SQLite

Tablas creadas de forma idempotente y sin tocar tablas heredadas:

- `ib_evaluations`: identidad versionada, estado, fingerprint, traza y tiempos.
- `ib_outbox`: mensaje transaccional, estado, intentos, lease y último error.
- `ib_signal_receipts`: recibo de consumo, estado de notificación y de PAPER.
- `ib_order_links`: relación evaluación → orden (N órdenes).
- `ib_position_links`: relación evaluación → posición (N posiciones).
- `ib_paper_events`: bitácora `armed`/`filled`/`opened`/`closed`.

La migración es idempotente y conserva las tablas existentes (probado en
`TestIBMigrationIsIdempotentAndPreservesLegacyTables`).

## 5. Configuración

| Variable | Default | Uso |
| --- | --- | --- |
| `IB_PERSISTENCE_ENABLED` | `true` | Activa persistencia, Outbox, recibos y worker |
| `IB_OUTBOX_LEASE_SECONDS` | `30` | Lease de publicación |
| `IB_OUTBOX_VISIBILITY_SECONDS` | `60` | Visibilidad antes de reclamar |
| `IB_OUTBOX_MAX_ATTEMPTS` | `10` | Intentos antes de `dead` |
| `IB_PAPER_LEASE_SECONDS` | `120` | Lease de consumo PAPER |
| `IB_RUNNING_TTL_MINUTES` | `30` | Antigüedad para marcar `interrupted` |
| `IB_OUTBOX_RETENTION_DAYS` | `7` | Retención de Outbox confirmado |
| `IB_EVALUATION_RETENTION_DAYS` | `90` | Retención de evaluaciones |
| `IB_OUTBOX_WORKER_SECONDS` | `1` | Intervalo de sondeo del worker |

## 6. Diagnóstico

- `/ibstatus`: resumen de evaluaciones, Outbox, recibos y enlaces.
- `/ibevals`: últimas evaluaciones con estado y fingerprint.
- `/ibops`: operaciones y eventos PAPER enlazados.

Los comandos son de solo lectura y no reevalúan ni publican.

## 7. Validación ejecutada

| Comando | Resultado |
| --- | --- |
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test -count=1 ./...` | PASS (todas las suites) |
| `gofmt -w` en archivos tocados | PASS (sin reformateo masivo; el repo tiene CRLF preexistente) |
| `go test -race ...` | NO EJECUTADO: el entorno no tiene `gcc`/cgo |

Pruebas nuevas relevantes:

- Identidad: `TestFingerprintIgnoresVolatileFieldsAndOrder`,
  `TestPublicationKeyStableAndEvaluationIncludesModel`.
- Outbox: `TestOutboxWorkerCrashAfterPublishRedeliversUntilAck`,
  `TestOutboxWorkerFailureRetriesAndDies`, `TestOutboxAckIsIdempotent`.
- Reserva/atomicidad: `TestIBReservationInterruptionNeverCreatesOutbox`,
  `TestIBReservationAcceptedIsAtomic`, `TestLiveEngineSkipsReservedBar`.
- Concurrencia: `TestIBConcurrentClaimHasSingleOwner`,
  `TestIBConcurrentSaveSignalSingleReceipt`.
- Consumo: `TestIBReceiptPreventsDuplicateDeliveryEffects`,
  `TestIBReceiptRecoversPaperAfterExpiredLease`,
  `TestIBPaperFailureRetriesWithoutRenotifying`.
- PAPER: `TestIBPaperTwoLimitsLinkAllOrders`, `TestIBPaperImmediateLinksPosition`,
  `TestIBPaperCloseLinksOpeningEvaluation`.
- Pipeline completo: `TestIBPipelineFromCandleToPaperPosition`.
- Aislamiento: `TestPhase2StrategyNoneDoesNotRecordOrPublish`,
  `TestSharedStrategyNeverFallsBackToChandelierWhenIBIsRequested`.

## 8. Pendiente antes de fusionar

1. **Ejecutar `go test -race` en CI o en una máquina con cgo/gcc.** No se pudo
   correr localmente.
2. **Prueba PAPER controlada de 24–48 h** con la rama desplegada, verificando
   `/ibstatus`, `/ibevals` y `/ibops`, y que no haya dos motores evaluando.
3. **Criterio de 100 operaciones cerradas** antes de aplicar la comparación de
   75 % de acierto (puerta existente en `main.go`). No se ha alcanzado todavía.
4. Revisar el PR y fusionar solo tras los puntos anteriores.

## 9. Compatibilidad

- No se eliminó el diseño V1; queda como historial.
- La V2 es la referencia operativa.
- No se modificaron workflows, Telegram runtime, `StrategyNone` ni la lógica de
  Pedro más allá del wiring estrictamente necesario.
