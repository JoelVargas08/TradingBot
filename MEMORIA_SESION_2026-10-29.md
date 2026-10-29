# MEMORIA_SESION_2026-10-29

Fecha: 2026-10-29
Plan: `PLAN_OPENCODE_CIERRE_TRADINGBOT_2026-10-28` (cierre PAPER).
Rama: `feat/cierre-plan-paper`, creada desde `origin/fix/telegram-controller`
(5 commits por delante de `main`, 0 por detrás; se incluyen en el PR).
Reglas: `rules.md`. Todo permanece en PAPER; no se añadió ninguna vía LIVE.

## 1. Diagnóstico causal del arranque tardío (evidencia)

Fuente: API pública de Actions de `JoelVargas08/TradingBot` (repo público,
rama por defecto `main`), consultada sin credenciales.

- **Controlador:** cron `*/5 * * * *` → 2–7 ejecuciones por día entre el
  12 y el 29 de octubre (debería ser 288). Del 28-10 11:57 al 29-10 09:42
  (La Habana) solo hubo 5 runs, con huecos de 3h50m a 7h08m.
- **PAPER `schedule` `0 8 * * 1-5` (America/Havana):** runs creados entre las
  11:57 y las 13:58 de La Habana; al quedar en cola detrás de la sesión activa
  arrancaban a las 14:00 y se cerraban solos.
- **Sesiones reales:** las creó el controlador (`workflow_dispatch`) cuando
  llegó a ejecutarse: 08:33, 09:42, 09:48, 10:15, 10:28, 11:57. Hoy (29-10):
  run `40884448510` despachado a las 09:42, terminó 14:00:04, setup ~25 s,
  estado subido.
- **Reintentos inexistentes:** el controlador trataba como reintentable
  `status == "failed"`, valor que GitHub nunca devuelve (`status=completed` +
  `conclusion=failure`). Los tests usaban ese vocabulario falso y pasaban.
- No había `401/403` del controlador en los runs recientes (todos `success`);
  la causa es la cadencia del cron, no permisos.

## 2. Workflows

| Workflow | Decisión | Motivo |
| --- | --- | --- |
| `telegram-controller.yml` | Conservado, rediseñado | Relevo continuo (jobs 5h30m que se despachan a sí mismos), cron `*/15` solo vigilante, apagado por `TRADINGBOT_CONTROLLER_RELAY=false`. |
| `paper-session.yml` | Conservado; **se quita su `schedule`** | Disparaba con 4–6 h de retraso y, en cola, podía arrancar tras un `/stop` del operador. El controlador es el único disparador automático. |
| `ci.yml`, `ml-history.yml`, `ml-train.yml`, `ml-promote.yml` | Sin cambios | Responsabilidades distintas y controles propios. |

No se eliminó ningún workflow.

## 3. Archivos modificados

- `.github/scripts/telegram_workflow_controller.py`: bucle de relevo con
  refresco de estado cada 60 s y long poll de 25 s; reintentos por
  `conclusion` (`failure`/`timed_out`/`startup_failure`, máx. 3); estado
  desconocido = en cola; sesión del día = run que llegó a la ventana (una
  prueba nocturna no bloquea); confirmación del run tras despachar y de la
  parada tras cancelar; sin doble despacho en un mismo lote; comandos de la
  sesión anterior y caducados (>15 min) ignorados; logs con
  `utc/local/run/event`; aviso de agotamiento una vez al día; `relay=false`
  ante configuración inválida o token de Telegram inválido.
- `.github/scripts/test_telegram_workflow_controller.py`: reescrito con el
  vocabulario real de GitHub (71 tests).
- `.github/scripts/paper_state.py` + `test_paper_state.py`: verificación
  `integrity_check` y resumen del job (6 tests). Cierra la conexión con
  `contextlib.closing` (un `with sqlite3.connect()` deja el fichero abierto).
- `.github/workflows/telegram-controller.yml`: relevo, vigilante, guardia de
  10 min contra bucles rápidos.
- `.github/workflows/paper-session.yml`: sin `schedule`; alias
  `investing-bulls-mtf` por defecto; `LIVE_STRATEGY=none`; afirmación de modo
  PAPER; restauración por nombre de artefacto con comprobación de
  `data/bot.db` e integridad; `SIGTERM` al bot 4 min antes del límite de 6 h;
  base corrupta → `tradingbot-state-corrupt`; resumen del job.
- `internal/investingbulls/selector.go` (+ tests): `ResolveLive`.
- `internal/strategymanager/live.go` (+ test): `StrategyNone`.
- `internal/store/sqlite.go` (+ `positions_migration_test.go`): migración de
  la unicidad de `positions` (ver §8).
- `main.go`, `strategy_runtime.go` (+ tests), `telegram_dispatch.go`,
  `handlers/commands.go` (+ test), `config/config.go`: selección explícita de
  estrategias, aviso de arranque y `/session` con estrategia y feed,
  autorización de comandos de control, monitor de 100 operaciones.
- `README.md` (estaba vacío), `docs/TELEGRAM_CONTROLLER.md`, `PROGRESO.md`.

## 4. Selección de Investing Bulls

- `INVESTING_BULLS_STRATEGY_ID` = alias `investing-bulls-mtf` (por defecto en
  el workflow) → estrategia `active` de fuente `investing_bulls_learn_mtf`,
  símbolo BTCUSDT, timeframes 1h/15m(/5m si exige confirmación) que el feed
  WEEX entrega; si hay varias, la actualizada más recientemente; las demás se
  registran como descartadas con motivo.
- ID explícito: debe existir, estar activo y ser compatible; si no, error
  explícito y **no** se sustituye por el alias ni por otra estrategia.
- **Chandelier ya no actúa en silencio:** antes `main.go` seleccionaba siempre
  `"chandelier"` en el motor compartido de WEEX, así que cada sesión PAPER
  operaba Chandelier tanto si IB resolvía como si no. Ahora el motor compartido
  usa `LIVE_STRATEGY` (`none` en el workflow) y solo opera Chandelier con
  selección explícita. Evidencia: `TestLiveEngineStrategyNoneNeverEvaluates`,
  `TestSharedStrategyNeverFallsBackToChandelierWhenIBIsRequested`,
  `TestResolveLiveAliasWithoutActiveFailsAndNeverFallsBack`,
  `TestStrategyLinesReportUnresolvedIBWithoutSubstitute`.
- Atención: el ID `investing-bulls-mtf-BTCUSDT-1793190305` existe en la base
  **local**; la base de GitHub es la cadena de artefactos `tradingbot-state` y
  no se pudo inspeccionar sin credenciales. Si allí no hay una IB activa, el
  aviso de arranque lo dirá ("Investing Bulls: NO activo — …") y no operará
  ninguna estrategia hasta ejecutar `/learnibmtf` + `/validateibmtf` en el
  runner.

## 5. Pruebas ejecutadas (resultado real)

| Comando | Resultado |
| --- | --- |
| `go build ./...` | OK |
| `go vet ./...` | OK |
| `go test -count=1 ./...` | 35 paquetes OK, 0 fallos |
| `python -m unittest discover -s .github/scripts -p "test_*.py"` | 77 OK |
| `python -m unittest discover -s tests/ml -p "test_*.py"` | 27 OK (con `ml/requirements-*.txt`) |
| `gofmt` sobre los `.go` tocados | sin diferencias |
| YAML de los 6 workflows | parsea (PyYAML) |
| Lógica bash nueva (timeout/124, listado tar, tar rc=1, array vacío con `set -u`) | probada localmente |

No se pudo ejecutar `actionlint` (descarga bloqueada por la política de
permisos de la sesión). En Windows los tests del controlador requieren
`tzdata`; sin él falla `test_start_follows_daylight_saving` (en Ubuntu no).

## 6. Evidencia E2E por Telegram

**No realizada en esta sesión:** requiere fusionar en `main` (el despacho y el
relevo usan `TRADINGBOT_DISPATCH_REF=main`) y los secretos del repositorio.
Pasos tras fusionar:

1. Actions → *TradingBot - Telegram Controller* → *Run workflow* (relay=true).
2. En Telegram: `/ping`, `/session` (debe mostrar último run y próximo
   arranque), `/session_start` (respuesta con `run_id`), esperar el aviso del
   runner (estrategia + feed), `/session` en el runner, `/session_stop`
   ("Parada confirmada" o "Cancelación solicitada").
3. Verificar en Actions que tras ~5h30m aparece el sucesor del controlador y
   que el día laborable siguiente el run PAPER se crea a las 08:00–08:01.

## 7. Evidencia WEEX (desde esta máquina, endpoint público, solo lectura)

`go run ./cmd/mlhistory` (misma ruta de código que el backfill) a las
19:05 UTC del 29-10: 300 velas por timeframe, **0 huecos**, integridad `ok`.
Última vela: 1h 18:00, 15m 18:45, 5m 19:00 UTC (la vela en curso no se
almacena como cerrada). Histórico 1h de 8 000 velas
(2025-11-30 11:00 → 2026-10-29 18:00 UTC): 0 huecos. El código ya cubría
reconexión con backoff, relleno de huecos tras reconectar, validación OHLCV,
orden/duplicados y gate de feed STALE; `/session` ahora muestra la última vela
por timeframe, su edad y la salud del feed.

## 8. Persistencia y operaciones PAPER cerradas

- **Bug crítico corregido:** la tabla `positions` tenía
  `UNIQUE(strategy_id, symbol, timeframe, status)`. Cerrar una **segunda**
  operación de la misma estrategia/símbolo/timeframe violaba la restricción
  (`UNIQUE constraint failed`), la posición quedaba abierta y bloqueaba nuevas
  entradas: el libro PAPER no podía pasar de **1 operación cerrada** por
  estrategia y la meta de 100 era inalcanzable. Reproducido con un test antes
  de corregir. Migración idempotente: reconstrucción de la tabla conservando
  filas e ids + índice único parcial `WHERE status = 'open'`.
- El monitor de 100 operaciones contaba solo las de la sesión en curso (se
  reiniciaba cada día). Ahora cuenta todas las cerradas de la estrategia (la
  base se restaura en cada run), informa win rate, neto, PF, expectativa,
  costes, DD y long/short, no repite el aviso de muestra superada y nunca
  habilita LIVE. Corregido también `\\n` literal en el aviso.
- Conteo actual de operaciones cerradas en GitHub: **no verificable** sin
  descargar el artefacto (requiere credenciales); desde ahora aparece en el
  resumen de cada run. Base local documentada el 28-10: 0 operaciones.

## 9. Red neuronal (WEEX BTCUSDT 1h, 8 000 velas, comisiones 4 bps + slippage 2 bps)

- Baselines sobre toda la muestra: always_long −46.2 %, always_short −25.2 %,
  momentum_24 −28.7 %, rsi_30_70 −24.1 %, macd_hist −15.5 % (DD 28.5 %).
- `nn_train.py --dev-only --reference macd --calibrate --seed 7`: 4 folds,
  11 trades, retorno compuesto −2.1 % frente a MACD walk-forward +15.2 % →
  `gate=false`; calibración sin candidatos elegibles (mín. 20 trades). Muestra
  insuficiente: resultado **inconcluso y no promocionable**.
- Recursos (medidos por `ResourceLimiter`/psutil): RAM pico 97 MB (límite
  512 MB); el informe registra 0.34 s de entrenamiento.
- Holdout reservado: **no evaluado** (no se reutiliza para iterar).
- Hill climbing / ABC: siguen desactivados (no hay línea base reproducible
  superada que justifique optimizar).

## 10. Limitaciones, riesgos y pasos manuales

1. Fusionar el PR en `main` (el cron, el relevo y el despacho usan `main`).
2. Verificar `TELEGRAM_ADMIN_CHAT_ID`; dejar `INVESTING_BULLS_STRATEGY_ID`
   vacía (alias) o con un ID que exista en la base de GitHub.
3. Lanzar una vez el controlador a mano tras fusionar para iniciar la cadena.
4. Coste/uso: el relevo ocupa un runner 24/7. Gratis en repo público; en
   privado superaría la cuota (alternativa: `systemd/`, `docker-compose.yml`).
   Valorar también la política de uso de GitHub Actions para procesos
   continuos.
5. Ventana efectiva de trading automático ≈ 08:01–14:00 (setup ~25 s); una
   sesión manual dura hasta ~5h56m.
6. Investing Bulls: OOS 0/4, solo observación PAPER. 100 operaciones no
   habilitan LIVE.
7. CRLF/gofmt preexistente en otros archivos: no se reformateó en masa.

## 11. Pendientes

- E2E real por Telegram y primer arranque 08:00 con la cadena activa.
- Confirmar qué estrategia IB resuelve el alias en la base de GitHub.
- Contadores de huecos detectados/reparados por WEEX en `/session` (hoy solo
  en logs y en la salud del feed).
