# Actualización PC — Integración TradingView → AquaTrade

## Objetivo

Actualizar la copia local de AquaTrade con los cambios realizados en GitHub para que:

TradingView → `/webhook` → SQLite → LiveEngine → estrategia activa → SignalEvent → Processor → Paper Engine

Binance deja de ser la fuente de datos operativa por defecto.

---

## 1. Cambios en `handlers/webhook.go`

Actualizar el webhook existente para aceptar velas OHLCV de TradingView.

Debe aceptar este formato:

```json
{
  "secret": "TU_WEBHOOK_SECRET",
  "symbol": "{{ticker}}",
  "exchange": "{{exchange}}",
  "timeframe": "{{interval}}",
  "time": "{{time}}",
  "open": "{{open}}",
  "high": "{{high}}",
  "low": "{{low}}",
  "close": "{{close}}",
  "volume": "{{volume}}"
}
```

Características:

- `open`, `high`, `low`, `close` y `volume` pueden llegar como número o string.
- `time` puede llegar como Unix timestamp o RFC3339.
- Se valida que OHLC sea coherente.
- Se aceptan únicamente velas cerradas.
- Si llega una vela OHLCV sin `action`, se guarda y no se genera una operación directamente.
- Si llega una vela junto con `action=buy/sell`, se conserva el flujo anterior de señales.
- Se mantiene la validación de `WEBHOOK_SECRET`.
- La respuesta correcta es HTTP `202 Accepted`.

La vela se almacena mediante el `CandleStore` existente.

---

## 2. Nuevo archivo `internal/strategymanager/live.go`

Crear el motor de evaluación en vivo:

```text
internal/strategymanager/live.go
```

Responsabilidad:

- recibir velas cerradas;
- ignorar símbolos que no coincidan con el seleccionado;
- ignorar temporalidades que no coincidan;
- evitar procesar dos veces la misma vela;
- cargar las velas recientes necesarias para la estrategia;
- evaluar la estrategia activa;
- publicar un `domain.SignalEvent` cuando exista señal.

Configuración inicial:

```text
strategy = chandelier
symbol   = BTCUSDT
timeframe = 1h
```

La estructura debe permitir posteriormente cambiar:

```text
estrategia
par
temporalidad
```

mediante:

```go
SetSelection(strategyID, symbol, timeframe)
Selection()
```

Para `chandelier` se utiliza la estrategia existente `DefaultChandelier()`.

Para estrategias aprendidas desde PDF se utiliza el `StrategyStore` y el `Spec` almacenado.

---

## 3. Conexión del webhook con el LiveEngine

El flujo debe quedar:

```text
TradingView
    |
    | POST /webhook
    v
WebhookHandler
    |
    +--> validar secret
    |
    +--> validar OHLCV
    |
    +--> guardar vela
    |
    v
LiveEngine.OnCandle()
    |
    v
Estrategia activa
    |
    +--> sin señal
    |
    +--> BUY
    |
    +--> SELL
    |
    v
SignalPublisher
    |
    v
Processor
    |
    v
Session Manager
    |
    +--> sesión detenida: no abre posición
    |
    +--> sesión activa: Paper Engine
```

No se debe crear una segunda lógica de paper trading.

El LiveEngine debe reutilizar el bus de señales y el processor existentes.

---

## 4. Binance

La ingestión automática de Binance debe quedar desactivada por defecto.

Configurar:

```env
INGEST_ENABLED=false
```

Si el `.env` local contiene:

```env
INGEST_ENABLED=true
```

cambiarlo manualmente a:

```env
INGEST_ENABLED=false
```

Esto evita que AquaTrade intente utilizar Binance como fuente principal durante esta fase.

---

## 5. TradingView

Crear una alerta en TradingView con el webhook público:

```text
https://TU-DOMINIO/webhook
```

Mensaje:

```json
{
  "secret": "TU_WEBHOOK_SECRET",
  "symbol": "{{ticker}}",
  "exchange": "{{exchange}}",
  "timeframe": "{{interval}}",
  "time": "{{time}}",
  "open": "{{open}}",
  "high": "{{high}}",
  "low": "{{low}}",
  "close": "{{close}}",
  "volume": "{{volume}}"
}
```

La alerta debe ejecutarse al cierre de la vela.

---

## 6. Variables locales

Comprobar que `.env` contenga como mínimo:

```env
MODE=paper
WEBHOOK_SECRET=tu_secreto
INGEST_ENABLED=false
```

No publicar `WEBHOOK_SECRET` en GitHub.

---

## 7. Validación obligatoria en la PC

Desde:

```text
D:\ETECSA\Programación\GO\Proyectos\Trading algoritmico
```

ejecutar:

```powershell
gofmt -w .
go build ./...
go test ./...
go vet ./...
```

Después:

```powershell
go run .
```

---

## 8. Prueba del webhook

Si el servidor está disponible localmente en el puerto 8080, se puede probar con PowerShell.

Sustituir `TU_SECRETO` por el valor real:

```powershell
$body = @{
    secret = "TU_SECRETO"
    symbol = "BTCUSDT"
    exchange = "BINANCE"
    timeframe = "1h"
    time = "2026-09-17T15:00:00Z"
    open = 100000
    high = 101000
    low = 99500
    close = 100500
    volume = 123.45
} | ConvertTo-Json

Invoke-WebRequest `
    -Uri "http://localhost:8080/webhook" `
    -Method POST `
    -ContentType "application/json" `
    -Body $body
```

Respuesta esperada:

```text
HTTP 202
```

con:

```json
{
  "status": "accepted"
}
```

---

## 9. Qué NO implementar todavía

No implementar todavía:

- backtesting histórico;
- conexión con dinero real;
- Weex;
- cambio automático de estrategia;
- selección automática por win rate.

El objetivo inmediato es conseguir una cadena estable de:

```text
TradingView
→ webhook
→ vela
→ estrategia
→ señal
→ paper trade
```

---

## 10. Próxima fase después de esta actualización

Una vez que compile y las velas de TradingView lleguen correctamente:

### A. Configuración del mercado

Implementar:

```text
/symbol
/symbol BTCUSDT

/timeframe
/timeframe 1h
```

### B. Gestión de estrategias

Implementar:

```text
/strategies
/strategy
/strategy chandelier
```

y permitir que `/learn` añada nuevas estrategias sin eliminar las existentes.

### C. Estadísticas

Implementar:

```text
/winrate
/performance
```

con estadísticas separadas por:

```text
estrategia + par + temporalidad
```

Ejemplo:

```text
Chandelier | BTCUSDT | 1h
PDF_01     | BTCUSDT | 1h
PDF_02     | ETHUSDT | 15m
```

### D. Paper trading 4–6 semanas

Registrar:

- operaciones;
- ganadoras;
- perdedoras;
- win rate;
- PnL;
- drawdown;
- profit factor;
- promedio ganador;
- promedio perdedor;
- número de operaciones.

Weex queda para una fase posterior, después de validar el comportamiento del sistema en paper trading.

---

## Nota sobre GitHub

Estos cambios ya fueron realizados en el repositorio remoto. Este documento sirve como guía para reproducir/actualizar la copia local de la PC.

No sustituir archivos del proyecto a ciegas: primero hacer una copia/commit de cualquier cambio local que todavía no esté en GitHub.
