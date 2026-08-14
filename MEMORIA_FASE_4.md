# Memoria Fase 4 — Aprendizaje de estrategias desde PDF

## Objetivo

Completar la Fase 4 del plan: el bot aprende una estrategia de trading a partir de un documento PDF
adjunto en Telegram, genera Pine Script v6 + spec ejecutable con un LLM, y valida la estrategia con un
backtest out-of-sample antes de activarla.

## Lo que se implementó

### Extracción de PDF (`internal/pdf`)

- `pdf.New()` → `ExtractText(path)` / `ExtractTextFromReader(io.Reader)`. Implementación en Go puro
  (solo stdlib): detecta el encabezado `%PDF-`, localiza los bloques `stream ... endstream` con su
  diccionario, descomprime `FlateDecode` con `compress/zlib` y extrae el texto de los operadores
  `Tj`, `TJ`, `'`, `"` (literales con escapes, arrays con ajustes de posición y strings hex).
- Límites: 64MB por stream y 64MB total; streams ilegibles se omiten.
- **Decisión:** `github.com/pdfcpu/pdfcpu` se descartó por descargas fallidas en una red inestable
  (timeouts en proxy.golang.org, zip corrupto, git clone colgado). El extractor propio cubre los PDFs
  basados en texto sin dependencias externas.

### Cola de trabajos (`internal/jobqueue`)

- `Queue` con pool de workers (`Config{Workers, QueueSize, MaxAttempts, BaseBackoff, MaxBackoff}`).
- Estados: `queued / running / done / failed / canceled`. `Get`, `List`, `Count`, `InFlight`.
- Reintentos: solo errores marcados con `RetryableError(err, delay)` se reencolan con backoff
  exponencial; cualquier otro error es final. `OnDone(cb)` notifica el `Result{JobID, Payload}`.

### Cliente LLM (`internal/llm`)

- `Client` agnóstico al proveedor: `openai` (default), `deepseek`, `anthropic`, `openai_compat`.
- `GeneratePineScript(ctx, texto)` → `PineScriptResult{Name, Description, PineScript, Spec}`.
  El prompt (`SystemPromptPine`) pide JSON estricto con Pine Script v6 compilable y un `spec` JSON
  ejecutable (esquema de indicadores/entradas/salidas). Tolerante a cercos de markdown.

### Motor de estrategias (`internal/strategymanager`)

- `spec.go`: `StrategySpec{Symbol, Timeframe, Indicators, Entries, Exits, StopPct, TakeProfitPct}`,
  `ParseSpec`, validación (tipos `ema/sma/rsi/atr/volume_sma/donchian`, ops
  `> < >= <= = cross_above cross_below`, operandos `close/open/high/low/volume`/id/número).
- `indicators.go`: `sma`, `ema`, `rsi`, `atr`, `donchianMid` + series helper.
- `backtest.go`: simulación de equity sin dependencias. Tren 60% (primeros) / test 40% (finales),
  fee round-trip 0.001, stop/take intrabarra, Sharpe proxy (sqrt de barras anuales), `Thresholds`
  con defaults (`MinTrades=8`, `MinWinRate=0.45`, `MinProfitFactor=1.2`, `MinSharpe=0.5`,
  `MaxDrawdown=0.30`).
- `manager.go`: `Manager` con `SubmitFromText` (LLM → spec → estrategia `draft`), `Backtest`
  (→ `backtesting` → `active` si pasa / `rejected` con motivo si falla), `Activate`, `Reject`,
  `List`, `Get`. `strategyID()` genera ids estables desde el nombre.

### Persistencia (`internal/domain`, `internal/store`)

- `Strategy` ampliada: `Source`, `PineScript`, `Spec`, `Error`, `UpdatedAt`. Estados
  `draft/backtesting/active/rejected`. `BacktestResult` con métricas OOS. Interfaces
  `StrategyStore` (ampliada), `BacktestStore`, `StrategyManager`.
- SQLite: migración idempotente (`ALTER TABLE ... ADD COLUMN`, detecta `duplicate column`),
  columnas nuevas en `strategies` y tabla `strategy_backtests` + índice. Métodos
  `UpsertStrategy` completo, `GetStrategy`, `ListStrategies`, `SaveBacktest`, `LastBacktest`.

### Telegram y wiring (`handlers/strategies.go`, `main.go`, `config`)

- Comandos: `/learn` (PDF adjunto, nombre opcional en caption), `/strategies`, `/strategy <id>`,
  `/backtest <id>`. El loop de updates de `main.go` maneja documentos `Document` → descarga a
  `UPLOAD_DIR` → encola job `learn`.
- Jobs `learn` y `backtest`; errores LLM/backtest reintentables; `OnDone` notifica por Telegram el
  resultado con métricas y estado final.
- Config: `LLM_ENABLED` (default `false`), `LLM_PROVIDER`, `LLM_API_KEY`, `LLM_MODEL`,
  `LLM_BASE_URL`, `UPLOAD_DIR`, `STRATEGY_*` (umbrales y `BACKTEST_BARS`).
- Wiring en `main.go` solo si `LLM_ENABLED=true` (el resto del bot sigue igual sin la Fase 4).

## Decisiones clave

- **Doble artefacto del LLM:** Pine Script v6 para TradingView + spec JSON ejecutable para el
  backtest local. El backtest nunca depende del código Pine; valida el spec directamente.
- **Activación bloqueada por validación OOS:** una estrategia solo pasa a `active` si el backtest
  sobre el 40% final supera los umbrales; si no, queda `rejected` con el motivo concreto.
- **Job queue para el flujo completo:** descarga no bloquea el poller de Telegram; la extracción y
  el LLM (lentos y fallibles) viven en el worker con reintentos.
- **Extractor PDF propio** en vez de `pdfcpu` por la red inestable del entorno.

## Validación

- `gofmt -l .` limpio, `go build ./...` OK, `go vet ./...` OK.
- `go test ./... -count=1` → todos los paquetes en verde (20 con tests).
- Tests nuevos: `internal/pdf` (literales/arrays/hex/flate), `internal/jobqueue` (retries,
  cancelación, backoff), `internal/llm` (parsing de artefactos, endpoints), `internal/store`
  (migraciones, upsert, backtests), `internal/strategymanager` (spec, indicadores, backtest,
  cruces, ciclo del manager).

## Pendientes / siguiente paso

- **Fase 5:** motor de señales con IA (sidecar Python, XGBoost baseline, walk-forward, CNN-1D si
  supera). Ver `PLAN_DE_ACCION.md`.
- Probar en vivo: activar `LLM_ENABLED=true` con `LLM_API_KEY`, `LLM_PROVIDER` y `LLM_MODEL`;
  enviar al bot un PDF con una estrategia. El bot responde con el resultado del backtest OOS.
