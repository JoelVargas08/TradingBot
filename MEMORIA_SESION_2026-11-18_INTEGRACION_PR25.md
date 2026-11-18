# Memoria de sesión — 2026-11-18 — Integración PR #25 en `main`

## Objetivo
- Extraer del último run de GitHub los datos necesarios.
- Garantizar que la próxima sesión PAPER use el código más reciente, priorizando
  los commits de Pedro (PR #25).
- Dejar `main` lista para fusionarse con el resto de las ramas.
- Corregir: Investing Bulls no se evaluaba (`LIVE_STRATEGY=none`) y el backfill
  pedía 5000 velas por timeframe, agotando el rate limit de WEEX.

## Diagnóstico (run PAPER 44106337274, artefacto 14360825627)
- `bot.db` íntegra (`integrity_check=ok`, WAL); `ib_*` en 0 filas.
- Causa de no operar: `LIVE_STRATEGY=none` tiene prioridad y deshabilita IB.
- Rate limit: 3 reintentos HTTP 429 / code -1003 por backfill de 5000 velas en
  1h/15m/5m (≈150 peticiones en ráfaga).
- Las estrategias `investing-bulls-mtf-BTCUSDT-*` existen y están `active`, por
  lo que el alias resuelve; solo faltaba no bloquearla.

## Topología de ramas
- `origin/main` = `24d0bab` (merge PR #24). No contiene PR #25.
- `origin/fix/telegram-controller` = `3ba5a97` = merge `3d47946` + `7226904`
  (PR #25, commits de Pedro). Contiene la lógica IB MTF V2 y el chequeo de
  prioridad de `LIVE_STRATEGY`.
- PR #25 se fusionó a `fix/telegram-controller`, no a `main`.
- `68596cc` (HEAD local `fix/telegram-controller`) es una línea divergente
  obsoleta; no se fusiona, solo se preserva.

## Cambios realizados (rama `integration/main-pr25-final`)
- Merge `origin/fix/telegram-controller` (`3ba5a97`) sobre `origin/main`
  (`24d0bab`): sin conflictos.
- `.github/workflows/paper-session.yml`:
  - `LIVE_STRATEGY: none` → `LIVE_STRATEGY: investing-bulls-mtf` para activar IB
    en el motor compartido (el alias resuelve la estrategia MTF activa).
  - `WEEX_BACKFILL_BARS: 5000` → `500` (≈5 páginas por timeframe en vez de 50).
- `internal/ingest/service.go`: `feedStartStagger = 300 ms`; el backfill de cada
  timeframe arranca escalonado para no disparar todas las peticiones REST a la
  vez (429 por ráfaga).
- `.env.example`: `WEEX_BACKFILL_BARS=500`.
- `README.md`: documentado el nuevo comportamiento de selección de estrategia.

## Validación
- `go build ./...` OK.
- `go vet ./...` OK.
- `go test -count=1 ./...` OK (todos los paquetes).
- `py -m unittest discover -s .github/scripts -p "test_*.py"`: 77 tests OK.

## Estado y pendientes
- Rama de integración lista y publicada; PR abierto hacia `main` (no fusionado).
- La sesión PAPER del 2026-11-18 arrancó con el código anterior de `main`; el
  próximo arranque usará estos cambios cuando el PR se fusione a `main`
  (`TRADINGBOT_DISPATCH_REF=main`).
- PRs abiertos antiguos (#3, #7, #22) no relacionados; revisar por separado.
