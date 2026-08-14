# MEMORIA FASE 6 — Backtesting + KPIs + despliegue

Estado: **completada** (commit de la Fase 6). La Fase 6 cierra el plan original: el bot ahora puede
medirse contra un benchmark (buy&hold y Chandelier en Go puro), validar el modelo ML de la Fase 5
fuera de muestra y desplegarse con Docker. El paper-trading (4–6 semanas) queda como uso operativo
de `MODE=paper`.

---

## 1. Motor de backtesting (`internal/backtest`)

Event-driven, con costos realistas:

- **Slippage por lado** (`SlippagePct`) aplicado al precio de relleno de entrada y salida.
- **Comisión por lado** (`FeePct`), acumulada en `TotalFees`.
- **Stops y take-profit intrabarra** (`StopPct`/`TakePct`): se evalúan con `Low`/`High` de la vela
  antes de procesar la señal del cierre.
- **Semántica de señal = estado objetivo**: `SignalLong`/`SignalShort` abren o mantienen la posición;
  `SignalNone` cierra a flat. Esto permite que una estrategia (p.ej. Chandelier) salga de la posición.
- **Métricas**: `TotalReturn`, `CAGR`, `Sharpe`, `Sortino`, `MaxDrawdown`, `WinRate`, `ProfitFactor`,
  `Wins`/`Losses`, `Trades`, curvas `Equity`/`Drawdown`, `BuyHoldReturn`/`BuyHoldEquity` (referencia
  sin costos de estrategia) y `TradeLog` con motivo de salida (`signal`/`stop`/`take`/`end`).
- Cuenta **todo-en**: el balance se invierte completo en cada posición (aproximación de un bot que
  arriesga el 100% del capital disponible).

Tests en `backtest_test.go`: buy&hold, long sostenido, short, fees, stop, cierre por señal opuesta,
cierre a flat con `SignalNone`, Sharpe 0 en flat y validación de mínimo 2 velas.

## 2. Estrategias en Go (`internal/strategies`)

Chandelier Exit reimplementado en Go puro (ATR de Wilder propio) para no depender del sidecar en el
benchmark:

- `Chandelier{Period, Multiplier, ATRPeriod}` con `DefaultChandelier()` = 22/3.0/22.
- Señal = estado objetivo: cruce del cierre por encima de `max(high,n) − mult·ATR` → long; cruce por
  debajo de `min(low,n) + mult·ATR` → short; el resto → flat.
- **Inicialización en la primera barra válida**: si al terminar el warmup el precio ya está sobre una
  banda, se entra en esa dirección (evita perder tendencias establecidas).
- Sin lookahead: cada barra solo usa velas `[0..i]`.

Tests: entrada en tendencia alcista, warmup sin señales y backtest completo sobre las señales.

## 3. Señal por barra del modelo ML (`/predict_batch`)

El sidecar (`ml/api.py`) ahora expone `POST /predict_batch`:

- Devuelve **una señal por barra** (`buy`/`sell`/`none`) + `prob_up`/`prob_down`/`confidence`.
- **Sin lookahead**: `compute_features` es causal y la evaluación recorre las barras en orden; las
  primeras 64 y las barras con features NaN devuelven `none`.
- El cliente Go (`internal/ml/client.go`) añade `PredictBatch` → `BatchResult{Signals[]}`.
- En el benchmark se aplica el **umbral de confianza** (default 0.6): las barras con confianza
  menor se tratan como `none` (reduce churn: en la prueba sintética pasó de 956 a 256 operaciones).

Tests: roundtrip contra un sidecar simulado (`client_test.go`) y error HTTP 503.

## 4. Comparativa (`internal/benchmark` + `cmd/backtest`)

- `benchmark.Run(candles, cfg, runner)` ejecuta los tres backtests con **los mismos costos**:
  Buy & Hold (comprar en la primera vela y mantener), Chandelier Exit (Go) y ML XGBoost (sidecar,
  opcional).
- `Report.Text()` imprime una tabla markdown comparable a los KPIs del plan (Sharpe, Sortino,
  MaxDD, PF, win rate, comisiones) + `Best()`.
- CLI `go run ./cmd/backtest`:
  - Fuentes: `--db data/bot.db`, `--csv` (columnas `ts,open,high,low,close,volume`) o `--synthetic`.
  - Opciones: `--fee`, `--slippage`, `--stop`, `--take`, `--balance`, `--ml`, `--ml-url`,
    `--ml-confidence`, `--out report.json`, `--html dashboard.html`.
  - El dashboard HTML es **autocontenido** (SVG inline, sin CDN) con curvas de equity, drawdown,
    métricas y la tabla de trades de Chandelier.

Ejemplo (datos sintéticos, sin sidecar):

```
go run ./cmd/backtest --synthetic --bars 2000 --seed 42 --html reportes/dashboard.html
```

Con sidecar (modelo de la Fase 5):

```
py -m uvicorn api:app --host 127.0.0.1 --port 8099   # en ml/
go run ./cmd/backtest --synthetic --bars 3000 --seed 7 --ml --ml-confidence 0.6
```

## 5. Paper-trading (`MODE=paper|live`)

El bot ya gestionaba posiciones simuladas en SQLite a partir de las señales (libro de posiciones,
stop/take y sizing por riesgo). La Fase 6 lo hace **explícito**:

- `MODE=paper` (default): las señales generan posiciones en `data/bot.db` (sin broker externo).
- `MODE=live`: sin adaptador de broker configurado, el bot avisa por log y continúa en modo
  simulado (seguro).
- `MODE` inválido → error de arranque (`config_test.go`).

## 6. Despliegue con Docker

- `Dockerfile`: build multi-stage Go (imagen `golang:1.26-alpine`, `CGO_ENABLED=0` porque
  `modernc.org/sqlite` es Go puro) + imagen final mínima (`alpine:3.20`, usuario no-root, volumen
  `/app/data`).
- `ml/Dockerfile`: sidecar Python (`python:3.12-slim`) con `requirements.txt` y volumen
  `/ml/models`.
- `docker-compose.yml`: servicios `ml`, `bot` (depends_on ml, `ML_URL=http://ml:8099`) y
  `dashboard` (Caddy sirve `/reports` y hace de proxy del webhook). Variables: `WEBHOOK_SECRET`
  (obligatoria), `MODE`, `SYMBOLS`, `TIMEFRAMES`, `ML_ENABLED`, etc.
- `Caddyfile`: `{$BOT_UPSTREAM}` + rutas `/reports/*` (estático) y `/webhook/*` (proxy al bot).
- `.dockerignore` y `.gitignore` actualizados (`/reports/`).

**Validación pendiente**: Docker no está instalado en el entorno de desarrollo. Los archivos se
escribieron siguiendo buenas prácticas pero deben validarse en un host con Docker
(`docker compose up -d --build`).

## 7. Validación

- `gofmt -l .` → limpio.
- `go vet ./...` → limpio.
- `go build ./...` → OK.
- `go test ./... -count=1` → **25 paquetes OK** (nuevos: `internal/backtest`, `internal/strategies`,
  `internal/benchmark`, `config` MODE).
- Sidecar: `predict_batch` probado en vivo (200 velas → 200 señales, primeras 64 `none`).
- CLI: backtest sintético con y sin ML; dashboard HTML generado correctamente.

## 8. Pendientes / notas

- **CNN-1D** (punto 19 del plan): condicionada a que supere a XGBoost y al Chandelier en Sharpe/MaxDD
  con **datos reales**. Los exchanges siguen bloqueados en el entorno (451/403) → entrenar con
  `py ml/train.py --db data/bot.db` cuando existan velas reales.
- **Benchmark con datos reales**: `go run ./cmd/backtest --db data/bot.db` tras acumular velas del
  backfill.
- **HTTPS/Caddy en producción**: obligatorio para webhooks de TradingView.
- Recomendación de uso: **4–6 semanas de `MODE=paper`** antes de considerar capital real.
