# Auditoría final de integración IB MTF (V2)

Fecha: 2026-11-09
Rama auditada: `integration/ib-shared-engine`
Base: `origin/fix/telegram-controller` (ancestro verificado)
Alcance: fases 2–7 del diseño V2 y condiciones A–H del plan de continuidad.

> Regla aplicada: no se marca PASS una prueba que no se haya ejecutado. La
> garantía real del sistema es **entrega al menos una vez** con **efectos
> persistentes idempotentes**, no exactamente una vez de extremo a extremo.

## 1. Rama y base (A)

| Requisito | Resultado | Evidencia |
| --- | --- | --- |
| Repo `JoelVargas08/TradingBot` | PASS | worktree independiente |
| Rama `integration/ib-shared-engine` | PASS | `git rev-parse --abbrev-ref HEAD` |
| Base `origin/fix/telegram-controller` | PASS | `git merge-base --is-ancestor` → ancestro |
| Commits de Pedro presentes | PASS | `3d47946`, `13f3f08`, `fa3bccb`, `1727c19` en la base |
| No reutilizar `68596cc` | PASS | no aparece en la rama |
| V1 y V2 conservados | PASS | ambos documentos presentes |

## 2. Implementación por fase (B)

| Fase | Resultado | Evidencia |
| --- | --- | --- |
| 2. Persistencia | PASS | `internal/ibeval`, `internal/store/ib_events.go`, migración idempotente, relaciones N órdenes/posiciones |
| 3. Evaluación y Outbox | PASS | `BeginEvaluation`/`FinishEvaluation` transaccionales, `OutboxWorker` con lease/backoff/ack/dead |
| 4. Processor | PASS | `SaveSignalWithReceipt`, dedupe, lease PAPER, recuperación de recibo |
| 5. Paper | PASS | `ib_paper_events`, enlaces a órdenes/posiciones, cierres con evaluación de apertura |
| 6. Runtime y configuración | PASS | wiring en `main.go`, poda/retención, `MarkInterrupted`, variables documentadas |
| 7. Telegram y observabilidad | PASS | `/ibstatus`, `/ibevals`, `/ibops` con restricción admin existente |

## 3. Condiciones técnicas obligatorias (C)

### C.1 Recuperación del recibo PAPER

| Requisito | Resultado | Evidencia |
| --- | --- | --- |
| Lease/timeout en `processing` | PASS | `ClaimReceiptPaper`, `IB_PAPER_LEASE_SECONDS` |
| Ningún recibo bloqueado indefinidamente | PASS | `TestIBReceiptRecoversPaperAfterExpiredLease` |
| Recuperación tras reinicio | PASS | recibo persistido + lease vencido + reintento PAPER |

### C.2 Idempotencia real por punto crítico

| Punto de interrupción | Resultado | Evidencia / garantía |
| --- | --- | --- |
| Antes de publicar | PASS | el mensaje sigue `pending`; lease vencido permite reclamar (`ClaimOutbox`) |
| Después de publicar y antes de actualizar Outbox | PASS | `TestOutboxWorkerCrashAfterPublishRedeliversUntilAck` |
| Después de guardar la señal | PASS | `TestIBConcurrentSaveSignalSingleReceipt`, `TestIBReceiptPreventsDuplicateDeliveryEffects` |
| Durante el procesamiento PAPER | PASS | `TestIBPaperFailureRetriesWithoutRenotifying` |
| Después de crear una orden | PARCIAL | `TestIBPaperTwoLimitsLinkAllOrders` y `persistPending`; no hay prueba de caída exacta entre crear y enlazar |
| Después de abrir una posición | PARCIAL | `TestIBPaperImmediateLinksPosition`, `TestIBPaperCloseLinksOpeningEvaluation`; enlace persistido, falta prueba de caída exacta tras `OpenPosition` |
| Antes de completar el recibo | PASS | lease PAPER vencido → reintento sin duplicar notificación |
| Antes de confirmar el ack | PASS | `TestOutboxAckIsIdempotent`, reentrega hasta ack |

**Limitación documentada:** el bus y SQLite no ofrecen exactamente-una-vez de
extremo a extremo. Se garantiza **al menos una vez** y se demuestra la
idempotencia de los efectos persistibles (evaluación, Outbox, señal, recibo,
órdenes, posiciones, eventos). Los dos puntos marcados PARCIAL quedan como
riesgo residual y se recomienda añadir pruebas de caída inyectada antes de
fusionar.

## 4. Restricciones (D)

| Restricción | Resultado | Evidencia |
| --- | --- | --- |
| Solo PAPER | PASS | no se habilitó ejecución real |
| Sin fallback a Chandelier | PASS | `TestSharedStrategyNeverFallsBackToChandelierWhenIBIsRequested` |
| Estrategia/filtros/stops/targets/riesgo sin cambios | PASS | diff limitado a wiring/persistencia; pruebas PAPER existentes pasan |
| `ResolveLive`/runtime de Pedro sin cambios innecesarios | PASS | cambios solo de conexión |
| Workflows `.github` intactos | PASS | sin diff en `.github` |
| `data/bot.db` intacto | PASS | pruebas usan SQLite temporal/`:memory:` |
| Sin merge a main ni despliegue | PASS | solo rama de integración |
| Sin sesiones PAPER reales en pruebas | PASS | pruebas unitarias/integración |

## 5. Auditoría y pruebas (E)

| Comando / prueba | Resultado real |
| --- | --- |
| `gofmt -w` archivos tocados | PASS |
| `gofmt -l .` | No concluyente: CRLF preexistente en casi todo el repo |
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test -count=1 ./...` | PASS (todas las suites) |
| `go test -race ./internal/...` | **NO EJECUTADO**: sin `gcc`/cgo en el entorno |
| Migración SQLite temporal | PASS (`TestIBMigrationIsIdempotentAndPreservesLegacyTables`) |
| Concurrencia y duplicados | PASS (`TestIBConcurrentClaimHasSingleOwner`, `TestIBConcurrentSaveSignalSingleReceipt`) |
| Recuperación ante interrupciones | PASS/PARCIAL según C.2 |
| Pipeline completo MTF | PASS (`TestPhase1SharedEngine*`, `TestIBPipelineFromCandleToPaperPosition`) |
| Vela → operación PAPER | PASS (`TestIBPipelineFromCandleToPaperPosition`) |
| `StrategyNone` sin señales | PASS (`TestPhase2StrategyNoneDoesNotRecordOrPublish`, `TestPhase1StrategyNoneDoesNotReadOrPublish`) |
| Sin fallback a Chandelier | PASS (prueba citada) |
| Un solo motor IB | PASS: una sola instancia `strategymanager.NewLiveEngine` en `main.go`; `/ibdiag` es diagnóstico bajo demanda, no motor operativo |
| Contador PAPER persistente y puerta de 100/WR 75 % | PASS: `computePaperValidation` + `TestComputePaperValidation`; `TestFormatPaperValidationNeverEnablesLive` |

## 6. Documentación (F)

| Documento | Estado |
| --- | --- |
| `ESTADO_INTEGRACION_IB_MTF.md` | Creado |
| `AUDITORIA_FINAL_IB_MTF.md` | Creado (este documento) |
| `README.md` | Enlaces a operación/diseño V2 |
| `docs/INVESTING_BULLS.md` | Operación, diagnóstico y persistencia |
| `docs/TELEGRAM_CONTROLLER.md` | Comandos y variables |

## 7. Riesgos y limitaciones

1. **`-race` no ejecutado localmente.** Debe correrse en CI con cgo/gcc.
2. **Cobertura PARCIAL de caída después de crear orden/posición.** El diseño es
   idempotente, pero falta una prueba de fallo inyectado exactamente en ese
   intervalo.
3. **Prueba PAPER real de 24–48 h no ejecutada.** No se activaron sesiones
   reales por restricción del plan.
4. **Puerta de 100 operaciones cerradas no alcanzada.** El código y las pruebas
   de formato existen; falta la muestra real.
5. **Entrega al menos una vez.** Los efectos persistentes son idempotentes, pero
   no se afirma exactamente-una-vez extremo a extremo.

## 8. Comparación con la arquitectura original de Pedro

| Aspecto | Arquitectura original | Integración IB V2 |
| --- | --- | --- |
| Motor | Un `LiveEngine` compartido | Se conserva un único `LiveEngine`; IB entra por la misma ruta |
| Selección de estrategia | `ResolveLive`, sin sustitutos silenciosos | Se conserva; IB no cae a Chandelier |
| Ejecución | PAPER por defecto | Se conserva; solo PAPER |
| Persistencia | Señales/posiciones existentes | Se añaden evaluaciones, Outbox, recibos y enlaces sin alterar tablas heredadas |
| Recuperación | Reintentos básicos | Outbox con lease/backoff/dead + recibos con lease |
| Observabilidad | Comandos del controlador | Se añaden `/ibstatus`, `/ibevals`, `/ibops` |

La integración respeta la arquitectura de Pedro: no crea un segundo motor, no
duplica el controlador de sesión y mantiene las restricciones administrativas.

## 9. Qué falta para una prueba PAPER controlada

1. Ejecutar `go test -race` en CI con cgo/gcc.
2. Añadir pruebas de caída inyectada tras crear orden y tras abrir posición.
3. Desplegar la rama en el entorno PAPER controlado y observar 24–48 h:
   `/ibstatus`, `/ibevals`, `/ibops`, ausencia de duplicados y un solo motor.
4. Acumular 100 operaciones cerradas antes de evaluar el criterio de 75 % WR.
5. Revisar y fusionar el PR solo después de 1–4.

## 10. Conclusión

La implementación V2 está completa a nivel de código, compila, pasa `go vet` y
toda la suite `go test -count=1 ./...`. La entrega es al menos una vez con
efectos idempotentes demostrados en los puntos críticos principales. No se
declara validación operativa hasta completar `-race`, las dos pruebas de caída
pendientes y la prueba PAPER controlada.
