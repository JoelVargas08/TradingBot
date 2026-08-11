# Memoria de sesión — Fase 1 (desacoplar señales + multi-estrategia)

> Fecha: 2026-08-11
> Proyecto: Trading algorítmico (bot Go TradingView → Telegram)
> Estado: ✅ Fase 1 completa — compila, `go vet` limpio, tests verdes (9 paquetes)

---

## Contexto del plan

El plan maestro (`PLAN_DE_ACCION.md`) tiene 7 fases (0–6). Esta sesión ejecutó y
cerró la **Fase 1**. Queda validado el camino crítico 0 → 1 → 3.

## Arquitectura nueva (desacoplamiento de dominios)

El flujo ya no es síncrono dentro del webhook. Ahora es un **pipeline**:

```
TradingView → /webhook → Bus interno (canal acotado) → Processor (single consumer)
   → dedupe por barra → evaluador de cambio de dirección → SQLite → Notifier (cola + rate-limit)
```

### Paquetes nuevos (todo bajo `internal/`)

1. **`internal/domain`** — núcleo sin dependencias:
   - `SignalEvent{StrategyID, Symbol, Timeframe, Direction, Price, BarTS, ReceivedAt, Meta}` con
     `Valid()`, `Key()` → `(strategyID|symbol|timeframe)` y `BarKey()` (incluye ms de barra).
   - Interfaces: `Notifier`, `SignalStore`, `StrategyStore`, `Evaluator`, `UserRegistry`.
   - Errores canónicos: `ErrNotFound`, `ErrDuplicate`. Estados de estrategia
     `draft | backtesting | active` (usados en Fase 4).

2. **`internal/bus`** — `Bus` con canal acotado (512), `Publish(ctx)` respeta el contexto
   y `Subscribe()` expone el canal. El webhook publica y responde **202 Accepted**;
   si el bus está lleno → 503.

3. **`internal/dedupe`** — `Deduplicator` con mapa acotado (10 000 entradas) que se
   resetea al superar el límite. Clave = `BarKey()`.

4. **`internal/evaluator`** — `StateEvaluator`: consulta la última señal en el store por
   `(strategyID, symbol, timeframe)`; emite **solo si la dirección cambió** (o si no hay
   historial). Reemplaza el viejo toggle por símbolo de `models/user.go`.

5. **`internal/notifier`** — cola acotada (4096) + N workers (4) + **token-bucket
   ~30 msg/s** (burst 60) + retry con backoff exponencial (base 250 ms, máx 5 s,
   3 reintentos). Respeta `retry_after` de Telegram vía interfaz `RetryAware`
   (el emisor la implementa estructuralmente, sin acoplar servicios↔notifier).
   Formatea la alerta con contexto (estrategia, timeframe, precio según magnitud, meta).

6. **`internal/store`** — SQLite con `modernc.org/sqlite` (puro Go, sin CGO):
   - Esquema completo: `strategies`, `signals`, `candles`, `trades`, `performance`.
   - WAL + `busy_timeout` + `foreign_keys` + `synchronous=NORMAL`, `MaxOpenConns(1)`.
   - `SaveSignal` usa `INSERT OR IGNORE` y devuelve `ErrDuplicate` si ya existía.
   - `LastSignal` por `(strategy, symbol, timeframe)` ordenado por `bar_ts DESC`.
   - `UpsertStrategy` / `GetStrategy`.

7. **`internal/processor`** — consumidor **único y ordenado** (garantiza orden por clave,
   evita carreras entre evaluar/guardar). Pipeline: dedupe de barra → evaluar → persistir
   → notificar. Los duplicados o cambios sin dirección se descartan con log.

### Cambios en paquetes existentes

- **`handlers/webhook.go`** — ahora depende solo de `Publisher`. Payload añade `strategy`
  y `timeframe` (defaults `chandelier`/`1h`). Valida secret (tiempo constante), symbol,
  action ∈ {buy,sell}, price > 0. Responde **202**; 503 si el bus está lleno.
  `FlexPrice` (número o string) se mantiene como preocupación de transporte.
- **`models/user.go`** — `UserState` simplificado a `{ChatID, Active}`. Se eliminó
  `LastSignals` por símbolo (la lógica vive en `evaluator` + SQLite). Nuevos:
  `ActiveUserIDs()` y `SnapshotUsers()` (copia bajo RLock).
- **`storage/storage.go`** — `Save([]models.UserState)` (ya no hay `Snapshot()` en el modelo).
- **`services/telegram.go`** — `SendMessage` como `Sender`; detecta `tgbotapi.Error` con
  `RetryAfter` y devuelve un error con `RetryDelay()`. Se eliminó `SendAlert` (el
  formato vive en `notifier`).
- **`config/config.go`** — nueva variable `DB_FILE` (default `data/bot.db`).
- **`main.go`** — cablea bus → evaluador → notifier → processor; `context` cancelado en
  shutdown para drenar workers; `defer sqliteStore.Close()`.

## Tests (37 en 9 paquetes)

- `handlers`: 202, 401/400/503, defaults, FlexPrice, secureEqual.
- `internal/bus`: publish/subscribe, respeto del contexto con canal lleno.
- `internal/dedupe`: seen/unseen y reset por límite.
- `internal/domain`: validación y claves.
- `internal/evaluator`: primera señal, cambio de dirección, misma dirección, errores.
- `internal/notifier`: fan-out a activos, omisión de inactivos, retry, formato + meta,
  token-bucket (3 tokens @ 10/s ≥ 200 ms).
- `internal/processor`: end-to-end con SQLite en memoria — cambio de dirección notifica,
  misma dirección no, misma barra dedupe, separación por estrategia y por timeframe,
  persistencia verificada.
- `internal/store`: roundtrip Save/LastSignal, not-found, duplicado, estrategia upsert/get.
- `storage`: roundtrip, archivo inexistente, corrupto, overwrite atómico.

## Resultado de validación

```
gofmt -l .   → limpio
go build ./... ✅
go vet ./...  ✅
go test ./... ✅  (9 paquetes)
```

Nota: `go test -race` no está disponible en Windows sin cgo (el driver SQLite es puro Go);
la concurrencia está protegida por mutex y el processor es single-consumer.

## Notas de operación

- Variables de entorno requeridas: `TELEGRAM_BOT_TOKEN`, `WEBHOOK_SECRET`
  (opcionales: `PORT=8080`, `STORAGE_FILE=data/users.json`, `DB_FILE=data/bot.db`).
- El JSON de la alerta de TradingView debe incluir: `secret`, `symbol`, `action`, `price`,
  `time`; opcionalmente `strategy` y `timeframe`.
- La señal solo se notifica al usuario cuando **cambia la dirección** para la clave
  `(strategy, symbol, timeframe)`, y cada barra se procesa una sola vez.

## Siguiente paso

Fase 2 — Ingesta de datos: paquete `internal/ingest` (WebSocket de Binance/Bybit para
`kline_1m/1h`, backfill REST, throttler token-bucket, reconexión con backoff) + detector
de eventos (vela grande, spike de volumen, quiebre S/R, whale trades) + sentimiento
(Fear&Greed, CoinGecko trending, RSS).
