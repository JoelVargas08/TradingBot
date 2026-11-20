# Memoria de sesión — 2026-11-20 — Diagnóstico: señales IB aceptadas sin operación PAPER

> Fecha: 2026-11-20
> Estado: diagnóstico completado (sin cambios de código)

## Objetivo

Interpretar las respuestas del bot en `Respuesta últimos comandos.txt` y explicar por qué, con
todos los filtros (Fibonacci, imbalance, order block, supply/demand) en 🟢 APROBADO, no aparece
ninguna operación PAPER en `/ibops`.

## Entradas leídas

- `rules.md` (6 reglas: optimización, arquitectura limpia, especialización, validación, excelencia, memoria).
- `Respuesta últimos comandos.txt` (344 líneas): `/ibaudit`, `/ibwhy`, `/ibtrace` y `/ibops`
  para `ht946`, `ht93r`, `ht93c` (BTCUSDT, velas 15m 09:00/09:15/09:30 del 2026-11-19).
- Código del pipeline IB: `internal/strategymanager/live.go`, `internal/investingbulls/trace.go`,
  `internal/bus/bus.go`, `internal/processor/processor.go`, `internal/risk/risk.go`,
  `internal/paper/paper.go`, `internal/store/ib_events.go`, `internal/store/ops_events.go`,
  `main.go`, `ib_audit_reports.go`, `ib_diagnostics.go`, `.env.example`, `README.md`.

## Hallazgos validados en código

1. **Los filtros no fallan.** `/ibaudit` muestra Estado `ACEPTADA` y los 11 fundamentos en 🟢;
   `/ibtrace` paso 13 dice «Señal PAPER generada (orden límite pendiente)». El cuello está
   aguas abajo de la señal, no en Fibonacci/OB/imbalance.

2. **`/ibwhy` ya avisa del problema**: «Señal aceptada; orden PAPER pendiente o aún no procesada
   (ver /ibops)» (`ib_audit_reports.go:189-205`).

3. **La señal IB no abre a mercado.** Con `entry_zone_low/high`, `paper.Engine.OnSignal`
   persiste dos órdenes límite con estado `paper_armed` en la zona (`internal/paper/paper.go:355-357`,
   `420-440`; estado en `67`, persistencia en `69-83`). Solo se ejecutan cuando una vela **cerrada**
   posterior del **mismo timeframe** toca el precio (`fillPending`, `paper.go:579-595`) y el riesgo
   vuelve a aprobar al ejecutar (`paper.go:611-623`).

4. **Los precios no alcanzaron la zona.** Señales en 63913 / 63906 / 63919.9; zona 1
   63851.55-63871.895 y zona 2 63709.135-63762.032. El precio tenía que retroceder ~47-68 USD
   (≈0,07-0,11 %) para llenar el límite. Sin ese retroceso no hay posición: comportamiento esperado.

5. **`/ibops ht946` estaba mal invocado.** `ht946` es un ID de *evaluación*, pero el comando
   interpreta el primer argumento como `StrategyID` (`ib_audit_reports.go:1150,1158-1174`) y por
   eso imprime «Sin posiciones PAPER abiertas». El StrategyID real, según el trace, es
   `investing-bulls-mtf-BTCUSDT-1793631122`. Con `|` en el argumento, `/ibops` lista eventos PAPER
   de esa evaluación (`1201-1219`), incluidos los `armed`.

6. **Gates posteriores a la señal** que pueden convertir la orden en `skipped` sin que los filtros
   cambien: sesión inactiva (`internal/processor/processor.go:204-209` → `sesion_detenida`),
   feed stale (`210-215` → `feed_stale`), y `entryGate` de sesión en paper (`paper.go:351-353`).
   Riesgo puede rechazar por kill-switch/drawdown/pérdida diaria/máx. posiciones/R:R
   (`internal/risk/risk.go:157-213`). Por defecto `.env.example:78-79` trae
   `TRADING_SESSION_START_ACTIVE=false` y `TRADING_SESSION_SCHEDULE_ENABLED=false`.

7. **Sin `RISK_ENABLED` no existe paper engine** (`main.go:267-308`); sin `IB_PERSISTENCE_ENABLED`
   no se publica el outbox (`main.go:362-369`). Los límites armados se recuperan al arrancar desde
   `paper_armed` (`paper.go:101-132`).

## Pasos entregados al usuario

1. Repetir `/ibops` con el StrategyID real y con la evaluation key (la consulta anterior no era válida).
2. `/ibstatus` → revisar sesión y contadores «Recibos PAPER: pend/proc/ok/omit».
3. `/iberrors 24` → revisar `paper_error` de outbox/recibos (`ops_events.go:379-387`).
4. Comparar precio de la señal contra la zona de entrada del trace; si no se tocó, la orden sigue
   pendiente y no hay bug de filtros.
5. Confirmar `RISK_ENABLED`, `IB_PERSISTENCE_ENABLED`, sesión iniciada (`/start`) y restauración del
   artefacto `tradingbot-state`.
6. Si aparecen eventos `armed` pero nunca `filled`, revisar logs de `paper: límite ejecutado` /
   `rechazado al ejecutar` y la lógica de `fillPending` (timeframe y `BarTS`).

## Validación y estado final

- No se modificó código; no había base `data/bot.db` en el workspace para consultar órdenes reales,
  así que el diagnóstico se apoya en las respuestas del bot y en el código, no en datos locales.
- No se ejecutaron `go build/vet/test` porque no hubo cambios de código (comandos canónicos en
  `README.md:93` y `.github/workflows/ci.yml`).
- Próxima sesión: partir de este diagnóstico si el usuario aporta `/ibstatus` y `/iberrors`.

## Actualización con `/ibstatus` real del usuario (2026-11-20 08:22)

Salud OK; sesión ACTIVO (08:00→14:00 America/Havana); WEEX conectado y vela fresca;
`RISK_ENABLED=true` en `.github/workflows/paper-session.yml:106`. Persistencia: 24 evaluaciones,
3 acept, 21 rech, Outbox ack 3/dead 0, **Recibos PAPER: ok 0 · omit 3**.

- **Queda descartada la hipótesis de orden límite esperando retroceso**: `ok 0` implica que nunca
  se armó ninguna orden (`paper_armed`); las 3 señales aceptadas fueron omitidas por el processor.
- Motivos posibles de `PaperSkipped`, guardados en `paper_error` (`internal/store/ib_events.go:543-555`)
  y visibles con `/iberrors 24` (`internal/store/ops_events.go:383-387`):
  `sin_cambio_direccion`, `paper_deshabilitado`, `sesion_detenida`, `feed_stale`
  (`internal/processor/processor.go:193-215`).
- Con riesgo habilitado, sesión activa y feed conectado, el candidato principal es
  **`sin_cambio_direccion`**: las 3 señales del 19 son todas `buy` y `StateEvaluator.Evaluate`
  solo emite cuando la dirección difiere de la última señal persistida
  (`internal/evaluator/evaluator.go:18-26`); sin `emit`, el processor marca el recibo como
  `skipped` (`processor.go:193-197`).
- `rejected confluencia_insuficiente` en la evaluación actual es comportamiento normal del filtro
  (21/24 rechazos), no un error de infraestructura.
- Para operar señales aceptadas consecutivas del mismo lado haría falta cambiar la política de
  dedupe por dirección (por ejemplo, dedupe por vela/idempotency o permitir reentrada tras cierre),
  no relajar los filtros de confluencia.

## Confirmación definitiva (`/iberrors 24` → «Sin errores registrados»)

No contradice el diagnóstico: los `skipped` generados por `emit=false` se insertan directamente con
`paper_error=''` (`internal/store/ib_events.go:448-457`) y el processor hace `ack` terminal sin
escribir motivo (`internal/processor/processor.go:189-192`). Por eso `IBPipelineErrors`
(`WHERE paper_error <> ''`, `internal/store/ops_events.go:386`) no los muestra nunca.

Descartados `sesion_detenida`, `feed_stale` y `paper_deshabilitado` porque esos caminos sí escriben
un `paper_error` no vacío (`processor.go:199-215`) y `RISK_ENABLED=true`. Causa confirmada por
eliminación: `emit=false` → `last.Direction == ev.Direction` (`internal/evaluator/evaluator.go:26`)
en tres señales `buy` consecutivas. El paper engine ya evita duplicar misma dirección con posición
abierta (`internal/paper/paper.go:366-372`), por lo que el filtro de dirección del evaluator es
redundante para ese caso y es lo que bloquea las entradas en tendencia alcista.

## Implementación autorizada por el usuario (2026-11-20)

- `internal/evaluator/evaluator.go`: añade `Policy` con `PolicyDirection` (histórico, sigue siendo
  el default de `New`) y `PolicyIdempotency` vía `NewWithPolicy`. La nueva política emite cuando
  cambia `IdempotencyKey()` (vela + setup + dirección), no solo cuando cambia la dirección.
- `main.go:253`: el wiring usa `evaluator.NewWithPolicy(sqliteStore, evaluator.PolicyIdempotency)`.
- `internal/paper/paper.go:389-398`: si ya hay un lote pendiente de la misma
  estrategia/símbolo/timeframe/dirección, `OnSignal` no arma otro (evita sobre-exposición); el
  lote de dirección contraria se sigue cancelando.
- Tests nuevos: 2 en `internal/evaluator/evaluator_test.go`, 1 en
  `internal/processor/ib_receipt_test.go` (dos barras buy consecutivas → 2 PAPER done, 0 skipped)
  y 1 en `internal/paper/paper_test.go` (un solo lote pendiente).
- Validación: `go build ./...`, `go vet ./...` y `go test ./...` pasan sin errores.
  `go test -race` no se pudo ejecutar en local: no hay `gcc` para cgo; CI lo cubre en Linux
  (`.github/workflows/ci.yml:58-77`).
- Commit en `main`: `fix(ib): emitir por idempotency y no duplicar lotes pendientes`
  (autorizado explícitamente por el usuario).

## Nota operativa: cómo aplicar el fix

- El código nuevo solo corre en un **run nuevo** de `paper-session.yml` (checkout en
  `.github/workflows/paper-session.yml:120`).
- Procedimiento: `/stop` → esperar a que se confirme el fin del run → `/start`. El controlador no
  despacha con un run activo o en cola y `concurrency.cancel-in-progress=false`
  (`paper-session.yml:31-33`).
- `/start` manual despacha con `manual_session=true` y `restore_state=true`: sesión activa desde
  el arranque, **sin horario**, hasta el fin del job (~6 h); conserva DB, posiciones y órdenes.
- Para mantener la ventana 08:00-14:00 hay que esperar al despacho automático de las 08:00
  (`manual_session=false`). Un run `cancelled` no se relanza (`docs/TELEGRAM_CONTROLLER.md:92`).
