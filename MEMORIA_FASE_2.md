# MEMORIA_FASE_2.md — Ingesta de datos y detección de movimientos

**Fecha:** 2026-08-11
**Estado:** ✅ Completada y validada (build + vet + `go test ./... -count=1` en verde).
**Rama:** main — remoto `origin` (https://github.com/JoelVargas08/TradingBot.git)

---

## Objetivo

Alimentar el bot con datos de mercado en vivo (Binance WS), recuperar histórico (REST backfill),
detectar movimientos relevantes (vela grande, spike de volumen, quiebre S/R, whale trades) y emitir
un digest periódico de sentimiento (Fear&Greed + CoinGecko trending), todo conectado al pipeline
existente de Telegram.

## Decisiones de arquitectura

- **`internal/ingest`**: cliente WebSocket multistream de Binance (`wss://stream.binance.com:9443`),
  streams combinados `symbol@kline_TF` y `symbol@aggTrade`, reconexión con backoff exponencial
  (base 1s → máx 30s), ping/pong keepalive, read limit 2MB. Backfill REST con paginación de 1000
  velas y throttler token-bucket (10 req/s) reutilizado de `internal/throttle`.
- **`internal/detect`**: motor de eventos con ventana de 50 velas (mín 20 para emitir).
  - `big_candle`: rango de la vela ≥ 2.5× el rango medio de la ventana.
  - `volume_spike`: volumen con z ≥ 3σ sobre la ventana.
  - `breakout`: quiebre alcista/bajista sobre máximo/mínimo de la ventana S/R (50 velas), con
    rearme tras pullback (cierre retorna por debajo/encima del nivel del último quiebre).
  - `whale_trade`: trades con notional ≥ umbral (100k USD default) con cooldown por símbolo (5s).
- **`internal/sentiment`**: índice Fear&Greed (alternative.me) + trending de CoinGecko;
  `BuildDigest` produce el resumen para Telegram. Deshabilitado por defecto (`SENTIMENT_ENABLED=false`).
- **Persistencia**: velas en SQLite vía `SaveCandle`/`RecentCandles` (INSERT OR IGNORE, dedupe por
  `(symbol, timeframe, ts)`), tabla `candles`.
- **Configuración por env** (con defaults en `config/config.go` y `internal/ingest/config.go`):
  `SYMBOLS`, `TIMEFRAMES`, `INGEST_ENABLED`, `WATCH_TRADES`, `WHALE_USD`, `BINANCE_WS_URL`,
  `BINANCE_REST_URL`, `SENTIMENT_ENABLED`, `SENTIMENT_INTERVAL_HOURS`, `COINGECKO_API_KEY`.

## Detalle de implementación

- `internal/ingest/config.go`: defaults y `withDefaults()`, `streamNames()` construye los streams
  combinados, `wsStreamsURL()` arma `url?streams=sym@kline_1m/...`.
- `internal/ingest/binance.go`: `NewBinance`, `Start` (idempotente vía `started`), `Candles()`/`Trades()`
  canales con buffer (1024/512), `handleMessage` despacha kline/trade, parseo de campos string de
  Binance a float64. Se añadieron campos extra (`End`, `LastID`, `Type`) para evitar colisiones
  case-insensitive de `encoding/json` (`"T"`/`"L"`/`"e"` vs `"t"`/`"l"`/`"E"`).
- `internal/ingest/backfill.go`: `NewBackfiller` con `CandleStore` y URL; `Backfill` pagina hasta
  agotar velas o tope; aplica throttle.
- `main.go`: si `INGEST_ENABLED`, arranca Binance, crea el Detector, siembra el historial desde el
  store, corre backfill en goroutine y consume los canales; el detección usa `TextNotifier`.
  Si `SENTIMENT_ENABLED`, ticker periódico con digest.

## Bugs encontrados y resueltos

1. **Colisiones case-insensitive de `encoding/json`**: los mensajes reales de Binance llevan campos
   `t/T` (open/close time), `l/L` (low/last trade id) y `e/E` (event type/time). Sin campos con tag
   exacto, `"T"` sobreescribía `"t"` (Start quedaba con el close time), `"L"` chocaba con `"l"`
   (error "cannot unmarshal number into string") y `"e"` con `"E"` (error int64). Se declararon
   campos explícitos (`End json:"T"`, `LastID json:"L"`, `Type json:"e"`) para emparejar exacto.
2. **Tests con expectativas incorrectas**:
   - `TestDetectBigCandle`: la vela de prueba rompía S/R y emitía 2 eventos; se ajustó close para
     emitir solo `big_candle`.
   - `TestDetectBreakoutWithRearm`: el rearme ocurre en la vela de pullback y el quiebre dispara en
     la **siguiente** vela que supera el máximo; se corrigió el test para reflejarlo.
   - `TestSaveAndRecentCandles`: con `limit 2` el resultado ascendente es `[ts2, ts3]`, no `[ts2, ts1]`.
   - `TestBinanceStartValidates`: se verificó la guarda de doble `Start` (con defaults ya no hay
     "sin streams").

## Validación

- `go build ./...` OK, `go vet ./...` OK, `gofmt -l .` limpio.
- `go test ./... -count=1` → todos los paquetes en verde (handlers, bus, dedupe, detect, domain,
  evaluator, ingest, notifier, processor, sentiment, store, throttle, storage).
- Nota: `go test -race` no disponible (requiere cgo, bloqueado en Windows).

## Pendientes / siguiente paso

- Fase 3: estrategia "Chandelier Multi-Confirm" (Pine Script v6 + playbook enriquecido + reglas de
  riesgo). Ver `PLAN_DE_ACCION.md`.
- Para probar en vivo: definir `WEBHOOK_SECRET`, `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`, y activar
  `INGEST_ENABLED=true` (env vars, ver README de configuración).
