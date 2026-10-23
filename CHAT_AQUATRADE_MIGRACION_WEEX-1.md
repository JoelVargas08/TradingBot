# Chat — AquaTrade / TradingBot — Migración de TradingView a WEEX

## Resumen

Este archivo conserva el contenido relevante de la conversación sobre el desarrollo de AquaTrade/TradingBot, las pruebas realizadas, el estado de la integración con TradingView y la decisión posterior de migrar a WEEX como fuente principal de mercado.

## Proyecto

- Repositorio: `JoelVargas08/TradingBot`
- Ruta local: `D:\ETECSA\Programación\GO\Proyectos\Trading algoritmico`
- Telegram: `@cubantradebot`
- Bot ID: `8941711038`
- Base de datos: `data/bot.db`
- Modo: `paper`

## Estado validado

Se ejecutaron correctamente:

```powershell
gofmt -w .
go test ./...
go build ./...
go vet ./...
```

Todos los tests pasaron; build y vet terminaron sin errores.

AquaTrade arrancó con Telegram, polling, servidor HTTP en puerto 8080, paper trading y endpoint `/webhook`.

## Prueba de webhook

Se comprobó el health check mediante el túnel de Cloudflare y posteriormente:

```powershell
Invoke-RestMethod -Uri "https://cheaper-pose-minute-pads.trycloudflare.com/webhook" -Method Post -ContentType "application/json" -Body $body
```

Resultado:

```text
status
------
accepted
```

Esto confirmó que el túnel y `/webhook` funcionaban.

## Prueba de SQLite

Se consultó `data/bot.db` mediante un programa temporal `check_candles.go`.

Entre las velas encontradas apareció:

```text
2026-09-18 09:49:00 | O=79000.00 H=79500.00 L=78800.00 C=79300.00 V=125.50 Closed=true
2026-09-18 11:03:18 | O=79000.00 H=79500.00 L=78800.00 C=79300.00 V=125.50 Closed=true
```

Esto confirmó el flujo:

```text
POST /webhook → WebhookHandler → CandleStore → SQLite → RecentCandles
```

## Conexión comprobada en main.go

Se ejecutó:

```powershell
Select-String -Path .\main.go -Pattern "NewWebhookHandler|LiveEngine|sqliteStore|eventBus|processorSvc"
```

La salida relevante incluyó:

```text
main.go:157: eventBus := bus.New(512)
main.go:203: processorSvc := processor.New(eventBus, sqliteStore, evaluatorSvc, notifierSvc,
main.go:204: processorSvc.Start(ctx)
main.go:427: sm := strategymanager.New(sqliteStore, sqliteStore, llmClient, thresholds)
main.go:456: webhookHandler := handlers.NewWebhookHandler(eventBus, cfg.WebhookSecret, sqliteStore, sqliteStore)
```

Esto confirmó que el webhook recibe el `eventBus`, `sqliteStore` como `CandleStore` y `sqliteStore` como `StrategyStore`.

## LiveEngine

`internal/strategymanager/live.go` contiene `LiveEngine`, que:

- ignora velas abiertas;
- comprueba símbolo/timeframe;
- evita duplicados con `lastBar`;
- carga hasta 300 velas;
- evalúa `chandelier` con `strategies.DefaultChandelier().Signal`;
- carga estrategias aprendidas mediante `StrategyStore`;
- genera `domain.SignalEvent`;
- publica mediante `SignalPublisher`.

Actualmente el metadato de la señal usa `source: tradingview`, que deberá cambiarse a WEEX o hacerse configurable.

## Processor

`internal/processor/processor.go` recibe `domain.SignalEvent`, deduplica, evalúa, persiste, notifica y termina llamando a:

```go
p.position.OnSignal(ctx, ev)
```

Por tanto, el flujo esperado es:

```text
LiveEngine → SignalEvent → EventBus → Processor → Paper Engine
```

## Problema que motivó la migración

Una sola vela enviada por webhook no garantiza una señal de Chandelier. Aunque la infraestructura estaba conectada, todavía faltaba demostrar de forma fiable el circuito completo con datos de mercado reales.

Se decidió evitar depender de una cuenta de TradingView, alertas y webhooks, y buscar una fuente directa de mercado.

# Migración a WEEX

La arquitectura propuesta es:

```text
WEEX REST + WebSocket
        ↓
MarketDataService
        ↓
CandleStore (SQLite)
        ↓
LiveEngine
        ↓
Estrategia activa
        ↓
SignalEvent → EventBus → Processor → Paper Engine
        ↓
SQLite + Telegram
```

## WEEX REST

Endpoint propuesto:

```text
GET https://api-contract.weex.com/capi/v3/market/klines
```

Usarlo para cargar aproximadamente 300 velas al iniciar.

Ejemplo:

```text
symbol=BTCUSDT
interval=1h
limit=300
```

Mapeo conceptual:

```text
WEEX[0] → Start
WEEX[1] → Open
WEEX[2] → High
WEEX[3] → Low
WEEX[4] → Close
WEEX[5] → Volume
```

## WEEX WebSocket

Endpoint:

```text
wss://ws-contract.weex.com/v3/ws/public
```

Canal de ejemplo:

```text
BTCUSDT@kline_1h_LAST_PRICE
```

Debe soportar:

- velas en tiempo real;
- detección de cierre;
- deduplicación;
- reconexión;
- ping/pong.

## No usar API key todavía

La primera fase debe utilizar datos públicos de WEEX sin credenciales de trading.

No añadir todavía:

```env
WEEX_API_KEY=
WEEX_SECRET_KEY=
WEEX_PASSPHRASE=
```

La cuenta/API de WEEX queda para una fase posterior, después de validar el paper trading.

## MarketDataProvider

Crear:

```text
internal/ingest/provider.go
```

con una interfaz equivalente a:

```go
type MarketDataProvider interface {
    Backfill(ctx context.Context, symbol, timeframe string, limit int) error
    Subscribe(ctx context.Context, symbol, timeframe string) (<-chan domain.Kline, error)
}
```

Implementación inicial:

```text
internal/ingest/weex.go
```

Esto permite posteriormente soportar otros proveedores sin cambiar LiveEngine ni las estrategias.

## MarketDataService

Crear:

```text
internal/ingest/service.go
```

Responsabilidad:

```text
backfill → CandleStore → WebSocket → CandleStore → LiveEngine
```

Estructura conceptual:

```go
type MarketDataService struct {
    provider    MarketDataProvider
    candleStore domain.CandleStore
    liveEngine  *strategymanager.LiveEngine
}
```

## Configuración propuesta

```env
MODE=paper
MARKET_DATA_PROVIDER=weex
WEEX_CONTRACT_REST_URL=https://api-contract.weex.com
WEEX_CONTRACT_WS_URL=wss://ws-contract.weex.com/v3/ws/public
WEEX_PRICE_TYPE=LAST_PRICE
TRADING_SYMBOL=BTCUSDT
TRADING_TIMEFRAME=1h
WEBHOOK_SECRET=
INGEST_ENABLED=false
```

Reutilizar variables existentes si ya cumplen la misma función.

## LiveEngine

Mantenerlo independiente del proveedor. Debe continuar recibiendo:

```go
OnCandle(ctx, k domain.Kline)
```

Cambiar o parametrizar el metadato:

```go
"source": "tradingview"
```

para que refleje WEEX.

## Paper trading

Mantener la arquitectura actual:

```text
WEEX → estrategia → señal → paper position → PnL/WinRate
```

Objetivo: validar durante aproximadamente 4–6 semanas antes de trading real.

Métricas:

- operaciones;
- WinRate;
- PnL;
- drawdown;
- SL;
- TP;
- estrategia;
- símbolo;
- timeframe.

## Telegram

Mantener comandos existentes y ajustar/añadir:

```text
/symbol BTCUSDT
/timeframe 1h
/strategy chandelier
/market
```

`/market` debería mostrar proveedor, símbolo, timeframe, tipo de precio, estado WebSocket y última vela.

## Health check

Ampliar `/health` para informar estado del proveedor, símbolo, timeframe y modo paper sin exponer secretos.

## TradingView

Mantener temporalmente `POST /webhook` por compatibilidad, pero WEEX debe convertirse en el flujo principal. TradingView no debe ser requisito para recibir mercado ni hacer paper trading.

## Binance

No eliminar inmediatamente el código existente. Primero validar WEEX completamente; luego retirar el uso operativo de Binance si ya no es necesario.

## Tests

Crear pruebas para:

- REST: parseo, timestamps, OHLC, volumen, errores;
- WebSocket: ACK, parseo, velas abiertas/cerradas, ping/pong, reconexión, duplicados;
- MarketDataService: backfill, SQLite y LiveEngine;
- integración: WEEX → MarketDataService → CandleStore → LiveEngine → EventBus → Processor → PaperEngine.

## Orden de implementación

1. Crear `internal/ingest/provider.go`.
2. Crear `internal/ingest/weex.go`.
3. Implementar REST Klines.
4. Tests REST.
5. WebSocket V3.
6. Ping/pong y reconexión.
7. `MarketDataService`.
8. WEEX → CandleStore.
9. CandleStore → LiveEngine.
10. LiveEngine → EventBus → Processor → PaperEngine.
11. `/market`.
12. `/symbol`, `/timeframe`, `/strategy`.
13. `/health`.
14. Tests de integración.
15. Validar:

```powershell
gofmt -w .
go test ./...
go build ./...
go vet ./...
```

16. Ejecutar:

```powershell
go run .
```

## Checklist final

```text
[ ] WEEX REST responde
[ ] 300 velas cargadas
[ ] velas guardadas en SQLite
[ ] WebSocket conectado
[ ] ping/pong funcionando
[ ] nueva vela recibida
[ ] vela abierta NO genera señal
[ ] vela cerrada genera evaluación
[ ] LiveEngine funciona
[ ] SignalEvent publicado
[ ] Processor recibe señal
[ ] Paper Engine abre posición
[ ] posición queda en SQLite
[ ] Telegram muestra posición
[ ] /performance funciona
[ ] /market muestra WEEX conectado
[ ] /health muestra estado WEEX
[ ] go test ./... pasa
[ ] go build ./... pasa
[ ] go vet ./... pasa
```

## Archivo técnico separado creado durante la conversación

También se creó el documento:

```text
ACTUALIZACION_AQUATRADE_WEEX.md
```

con el plan técnico detallado para implementar la migración a WEEX.
