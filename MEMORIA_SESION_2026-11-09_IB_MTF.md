# Memoria de sesión — Integración IB MTF V2

Fecha: 2026-11-09
Rama: `integration/ib-shared-engine`
Base: `origin/fix/telegram-controller` (`3d47946`)

## Qué se hizo

- Se completó la V2 de persistencia, Outbox y trazabilidad de Investing Bulls
  MTF sobre el motor compartido de Pedro, sin un segundo motor y sin fallback a
  Chandelier.
- `LiveEngine` reserva la evaluación (`BeginEvaluation`), evalúa una sola vez con
  `EvaluateLiveWithTrace` y cierra de forma atómica con `FinishEvaluation`
  (evaluación + Outbox aceptado en una transacción).
- `OutboxWorker` con lease, reintentos, backoff, ack idempotente y estado `dead`.
- `processor` con recibo duradero, dedupe de notificación y lease PAPER
  recuperable tras caída.
- PAPER enlaza evaluación → N órdenes → N posiciones → cierres mediante
  `ib_paper_events`, sin cambiar riesgo/stops/objetivos.
- Arranque marca evaluaciones `running` antiguas como `interrupted` con
  `IB_RUNNING_TTL_MINUTES`; poda por retención.
- Comandos `/ibstatus`, `/ibevals`, `/ibops`.
- Pruebas nuevas de identidad, Outbox, reserva, concurrencia, recibos, PAPER y
  pipeline completo.

## Validación

- `go build ./...`: PASS.
- `go vet ./...`: PASS.
- `go test -count=1 ./...`: PASS.
- `go test -race`: NO EJECUTADO (entorno sin `gcc`/cgo).
- `gofmt -w` en archivos tocados: PASS. No reformatear el repo completo (CRLF
  preexistente).

## Documentos creados

- `ESTADO_INTEGRACION_IB_MTF.md`
- `AUDITORIA_FINAL_IB_MTF.md`
- `MEMORIA_SESION_2026-11-09_IB_MTF.md` (este archivo)
- Enlaces añadidos en `README.md`.

## Pendiente

- Ejecutar `go test -race` en CI con cgo/gcc.
- Añadir pruebas de caída inyectada tras crear orden y tras abrir posición.
- Prueba PAPER controlada 24–48 h y puerta de 100 operaciones cerradas antes del
  criterio de 75 % WR.
- Revisar PR; no fusionar todavía.
