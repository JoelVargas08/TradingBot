# TradingView → AquaTrade: configuración y prueba

## 1. Configuración del bot

En `.env` usa:

```env
MODE=paper
WEBHOOK_SECRET=pon_aqui_un_secreto_largo_y_aleatorio
INGEST_ENABLED=false
```

`INGEST_ENABLED=false` evita que AquaTrade intente usar el ingest legado de Binance. La fuente de velas para esta fase es TradingView.

## 2. Webhook de TradingView

La URL pública debe apuntar a:

```text
https://TU-DOMINIO/webhook
```

TradingView requiere un endpoint accesible por HTTPS. No uses `http://localhost:8080/webhook` como URL de alerta.

## 3. Mensaje de la alerta

Usa este JSON como mensaje de la alerta:

```json
{
  "secret": "pon_aqui_un_secreto_largo_y_aleatorio",
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

El valor de `secret` debe coincidir exactamente con `WEBHOOK_SECRET`.

## 4. Configuración de la alerta

Para la primera prueba usa un solo instrumento y una sola temporalidad, por ejemplo:

- Símbolo: `BTCUSDT`
- Temporalidad: `1h`
- Ejecución: al cierre de cada vela (`Once Per Bar Close`)
- Webhook: activado
- Mensaje: JSON anterior

La vela que llega a AquaTrade se considera cerrada cuando no se envía `closed=false`.

## 5. Qué ocurre al recibir una vela

```text
TradingView
    ↓
POST /webhook
    ↓
validación del secret
    ↓
validación OHLCV
    ↓
SQLite: candles
    ↓
LiveEngine
    ↓
estrategia activa
    ↓
SignalEvent
    ↓
Processor
    ↓
sesión de trading
    ↓
Paper Engine
```

Una vela por sí sola no crea una operación. Primero debe producir una señal de la estrategia activa.

## 6. Prueba local del webhook

Con AquaTrade ejecutándose en `localhost:8080`, PowerShell puede probar el endpoint con:

```powershell
$body = @'
{
  "secret": "pon_aqui_un_secreto_largo_y_aleatorio",
  "symbol": "BTCUSDT",
  "exchange": "BINANCE",
  "timeframe": "1h",
  "time": "2026-09-17T14:00:00Z",
  "open": "76000.10",
  "high": "77100.50",
  "low": "75800.00",
  "close": "76900.25",
  "volume": "1234.56"
}
'@

Invoke-RestMethod `
  -Uri "http://localhost:8080/webhook" `
  -Method Post `
  -ContentType "application/json" `
  -Body $body
```

Respuesta esperada:

```json
{"status":"accepted"}
```

En los logs debe aparecer algo similar a:

```text
Vela TradingView guardada: BTCUSDT 1h ...
```

## 7. Validación después de actualizar el código

Desde la carpeta del proyecto:

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

## 8. Importante

Esta fase todavía utiliza `BTCUSDT` + `1h` como selección inicial del `LiveEngine`. La siguiente fase añadirá los comandos de Telegram para cambiar dinámicamente:

```text
/symbol BTCUSDT
/timeframe 1h
/strategy chandelier
```

y posteriormente permitirá mantener varias estrategias aprendidas desde PDF y cambiar entre ellas sin perder sus estadísticas de paper trading.
