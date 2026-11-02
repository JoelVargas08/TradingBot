# MEMORIA_SESION_2026-10-28
Fecha: 2026-10-28
Objetivo: seguir rules.md (validación, arquitectura limpia, excelencia), diagnosticar el arranque de Investing Bulls MTF y cerrar la sesión con commit.

## Realizado
- Leídos rules.md, planes NN e Investing Bulls y DEPLOYMENT.
- Diagnóstico del ID de estrategia: `investing-bulls-mtf-BTCUSDT-1793190305` existe en `data/bot.db` con `status=active` (paper_observation). Añadido `INVESTING_BULLS_STRATEGY_ID` a `.env` (ignorado por git).
- Documentado en `docs/DIAGNOSTICO_ID_INVESTING_BULLS.md` y `PLAN_DIAGNOSTICO_INVESTING_BULLS_MTF.md`.
- `main.go`: logging explícito cuando la estrategia no existe, no está activa o falla la selección; en esos casos se detiene Investing Bulls sin fallback silencioso a Chandelier. Se registra la fuente de la estrategia al activarse.
- NN Fase 1: línea base con datos sintéticos en `ml/INFORME_LINEA_BASE.md` y `ml/linea_base_sintetica.json`.

## Correcciones de calidad (rules.md)
Durante la revisión se detectaron regresiones en el working tree y se revirtieron:
- `EvaluateLive` había quedado como stub que nunca emitía señal; `trace.go`/`trace_test.go` se habían sustituido por `trace_min.go`. Restaurados la implementación MTF real y su traza; eliminado el stub.
- `supplydemand.go` reescrito rompía `TestDetectSupplyZone` y perdía la semántica direccional/`CreatedAt`. Restaurada la versión canónica y probada.
- `docs/INVESTING_BULLS_STRATEGY.md` se había reducido perdiendo la matriz de trazabilidad R01–R26. Restaurado el documento canónico.
- `fibzoneselect.go` y `secrets_reaction.go` duplicaban lógica ya existente y probada (`zoneselect.go`, `secretzone.go`) y no se usaban; eliminados para no abrir rutas paralelas.

## Validación
- `go build ./...` OK
- `go vet ./...` OK
- `go test ./...` OK (todas las suites)

## Estado
- IB ID configurado; Investing Bulls arranca solo si la estrategia está activa y se puede seleccionar.
- Trazabilidad `/ibdiag` intacta sobre el pipeline real.
- Línea base NN registrada.

## Pendientes
- Completar asimilación fiel del PDF (golden tests y PipelineTrace más fino).
- Aplicar riesgo IB (SL<=1.5%, riesgo por trade, 1% diario, 20x validado).
- NN fases 2–7 según plan.
- Validación E2E en PAPER.
