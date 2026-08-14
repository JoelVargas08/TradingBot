# Memoria Fase 5 — Motor de señales con IA (baseline XGBoost)

## Objetivo

Completar la Fase 5 del plan: sidecar de ML que predice dirección de mercado (up/down) a partir de
velas 1h, integrado con el bot Go. Las predicciones se filtran por confianza + cooldown y entran en
el mismo pipeline de Telegram que el Chandelier (que sigue como fallback).

## Lo que se implementó

### Sidecar Python (`ml/`)

- `features.py` — ingeniería de características **sin lookahead** (solo velas cerradas): momentum
  (retornos 1/3/6/12/24), RSI(14) Wilder, MACD(12/26/9) + histograma, ATR(14) normalizado, volumen
  relativo a SMA20, distancia a SMA20/SMA50, posición en Bollinger(20,2) y Donchian(20), rango
  intrabarra. `FEATURE_NAMES` compartido entre entrenamiento e inferencia.
- `labels.py` — **triple-barrier**: barreras ±1.5σ (σ = std de retornos logarítmicos de la ventana
  anterior, sin lookahead) con horizonte de 24 velas; etiquetas up(2)/down(1)/flat(0). Splits
  **walk-forward** con embargo (purga de etiquetas solapadas).
- `train.py` — carga datos desde el SQLite del bot (`--db`, leído con sqlite3), un CSV (`--csv`) o
  sintéticos (`--synthetic`). Clasificación **binaria up vs down** (se descartan flats; la señal
  "none" se resuelve en inferencia con el umbral de confianza). Walk-forward en N folds, métricas por
  fold (acc, trades, PnL direccional, Sharpe anualizado 24×365) y se guarda el mejor modelo por
  Sharpe OOS en `ml/models/xgboost.joblib` + `meta.json`.
- `api.py` — FastAPI: `GET /health`, `POST /predict`, `POST /reload`. `/predict` recibe ≥64 velas
  OHLCV, calcula features, `predict_proba`, y devuelve `signal` (buy/sell/none), `confidence`
  (prob_up − prob_down), probas, RSI, volumen relativo y stop/target como múltiplos de ATR
  (`ML_STOP_ATR`, `ML_TARGET_ATR`, defaults 1.5× y 3.0×).

### Integración Go (`internal/ml/`)

- `client.go` — `Client` HTTP con `Predict(ctx, symbol, timeframe, ks)` y `Health(ctx)`. `Predictor`
  es una interfaz para poder testear el motor con un stub.
- `engine.go` — `Engine` mantiene un buffer de `Window` (default 64) velas cerradas por
  `(symbol|timeframe)` con `Seed` (precarga desde SQLite al arrancar) y `Reset`. `OnCandle` solo
  predice con velas **cerradas**, respeta el **cooldown** (default 4h), filtra por **confianza**
  (default 0.60) y construye un `SignalEvent` con meta: `confidence`, `rsi`, `volume_ratio`,
  `stop_loss`/`take_profit` (precios derivados de los % de ATR) y probabilidades.
- `config`: `ML_ENABLED` (default `false`), `ML_URL`, `ML_STRATEGY_ID` (default `ml-xgboost`),
  `ML_WINDOW`, `ML_CONFIDENCE`, `ML_COOLDOWN_HOURS`, `ML_TIMEOUT_SECONDS`.
- `main.go`: si `ML_ENABLED=true`, chequea `/health` (sin fallar si está caído), precarga buffers y,
  en el loop de velas, publica las señales en el **mismo bus** → processor → evaluador → notifier →
  risk. Chandelier queda intacto como fallback.
- Playbook: muestra "Confianza modelo" y probabilidades al alza/a la baja. Constantes nuevas
  `MetaKeyConfidence` y `MetaKeyProbability` en `domain`.

## Decisiones clave

- **Clasificación binaria** en vez de 3 clases: evita el fallo de XGBoost con clases ausentes en
  folds pequeños; la decisión "no operar" se toma en el bot con el umbral de confianza + cooldown.
- **Features en Python, no en Go:** el sidecar computa features desde las velas crudas, así
  entrenamiento e inferencia comparten exactamente el mismo código (`features.py`).
- **Splits con embargo = `horizon`:** elimina fuga por solapamiento de ventanas de triple-barrier.
- **Sidecar tolerante a caídas:** el bot arranca igual si el sidecar no responde; los fallos de
  predicción solo se loguean.
- **Datos:** en este entorno están bloqueados Binance/Bybit/Coinbase/Kraken (451/403), así que el
  pipeline se validó con datos sintéticos. `train.py` acepta el DB del bot (`--db`) y CSV para
  entrenar con datos reales cuando el bot corra en un entorno con conectividad.

## Validación

- Sidecar: `ml/train.py --synthetic` entrena y guarda el modelo; `/health` y `/predict` responden
  correctamente; roundtrip real Go → FastAPI verificado (señal + confianza + stop/target).
- Go: `gofmt -l .` limpio, `go vet ./...` OK, `go build ./...` OK.
- `go test ./... -count=1` → todos los paquetes en verde (21 con tests).
- Tests nuevos en `internal/ml`: emisión con confianza, cooldown, confianza baja, velas abiertas,
  precarga con Seed, parsing del cliente (httptest) y cálculo de stop/target.

## Pendientes / siguiente paso

- **CNN-1D (opcional):** el plan pide escalar a convoluciones dilatadas **solo si** el XGBoost
  supera al Chandelier en Sharpe/maxDD entre folds con datos reales. La arquitectura actual permite
  exportar el modelo a ONNX y servirla en `api.py` sin tocar el bot.
- Probar en vivo: entrenar con `py ml/train.py --db data/bot.db --symbol BTCUSDT --timeframe 1h`,
  arrancar `ml/api.py`, activar `ML_ENABLED=true` y observar señales en Telegram.
