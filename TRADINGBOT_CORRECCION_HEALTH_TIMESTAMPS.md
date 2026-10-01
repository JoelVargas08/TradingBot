# TRADINGBOT --- Corrección del monitor de salud del feed WEEX

## Objetivo

Corregir el falso estado:

``` json
"market_health": {
  "healthy": false,
  "reasons": ["timestamps no monótonos"],
  "stale": true
}
```

observado mientras el bot está conectado correctamente a WEEX y ha
completado el backfill de BTCUSDT.

La corrección debe limitarse al monitor de salud. **No se debe modificar
WEEX, el backfill, SQLite ni la validación de datos de Investing
Bulls**, porque esas partes ya validan correctamente el orden temporal.

------------------------------------------------------------------------

## 1. Diagnóstico

El bot consigue actualmente:

-   conexión con WEEX;
-   backfill histórico;
-   5000 velas de BTCUSDT 1h almacenadas;
-   feed de mercado activo;
-   `market_data.connected = true`.

Sin embargo, `/health` devuelve `market_health.healthy = false` con la
razón `timestamps no monótonos`.

La causa está en `internal/health/market.go`.

`RecentCandles()` devuelve las velas en orden cronológico ascendente. El
monitor construye actualmente la secuencia como:

``` go
all := make([]Candle, 0, len(s.Recent)+1)
if !s.LastCandle.Start.IsZero() {
    all = append(all, s.LastCandle)
}
all = append(all, s.Recent...)
```

Esto puede producir:

``` text
LastCandle: 11:00

Recent:
08:00
09:00
10:00
11:00
```

El monitor comprueba entonces:

``` text
11:00 → 08:00
```

y detecta un timestamp no monótono.

------------------------------------------------------------------------

## 2. Archivo a modificar

Modificar únicamente:

``` text
internal/health/market.go
```

No modificar:

``` text
internal/ingest/weex.go
internal/store/sqlite.go
internal/investingbulls/learn.go
```

Estas áreas ya contienen validaciones importantes de orden temporal.

------------------------------------------------------------------------

## 3. Cambio requerido

### Código actual

Dentro de `Monitor.Check()`:

``` go
all := make([]Candle, 0, len(s.Recent)+1)
if !s.LastCandle.Start.IsZero() {
    all = append(all, s.LastCandle)
}
all = append(all, s.Recent...)
```

### Código propuesto

Sustituirlo por:

``` go
all := make([]Candle, 0, len(s.Recent)+1)
all = append(all, s.Recent...)

if !s.LastCandle.Start.IsZero() {
    if len(all) == 0 || s.LastCandle.Start.After(all[len(all)-1].Start) {
        all = append(all, s.LastCandle)
    }
}
```

Con esto:

-   `Recent` permanece en orden cronológico;
-   `LastCandle` se añade solo si realmente es posterior;
-   no se duplica la última vela;
-   el monitor no genera un retroceso temporal artificial.

------------------------------------------------------------------------

## 4. Comportamiento esperado

### Caso A --- LastCandle ya está en Recent

Entrada:

``` text
Recent:
08:00
09:00
10:00
11:00

LastCandle:
11:00
```

Resultado:

``` text
08:00
09:00
10:00
11:00
```

### Caso B --- LastCandle es nueva

Entrada:

``` text
Recent:
08:00
09:00
10:00

LastCandle:
11:00
```

Resultado:

``` text
08:00
09:00
10:00
11:00
```

### Caso C --- No existe LastCandle

Resultado:

``` text
08:00
09:00
10:00
11:00
```

------------------------------------------------------------------------

## 5. Por qué no modificar SQLite

`internal/store/sqlite.go` implementa `RecentCandles()` con:

``` sql
ORDER BY ts DESC
```

y posteriormente invierte el resultado para devolver las velas en orden
ascendente.

Por tanto, SQLite está entregando correctamente:

``` text
más antigua → más reciente
```

No cambiar el orden de `RecentCandles()`.

------------------------------------------------------------------------

## 6. Por qué no modificar WEEX

`internal/ingest/weex.go` ya contiene validaciones para:

-   timestamps desordenados;
-   velas duplicadas;
-   OHLC incoherente.

También existen tests específicos para esos casos.

Por ejemplo, una serie:

``` text
t1
t2
t3
```

es válida, mientras que:

``` text
t2
t1
t3
```

debe fallar.

También debe fallar:

``` text
t1
t1
t2
```

Estas validaciones deben conservarse.

------------------------------------------------------------------------

## 7. Por qué no modificar Investing Bulls

`internal/investingbulls/learn.go` valida que las velas estén
estrictamente ordenadas:

``` go
if i > 0 && !ks[i-1].Start.Before(k.Start) {
    return fmt.Errorf(
        "timestamps desordenados o duplicados en la vela %d (%v)",
        i,
        k.Start.Format(time.RFC3339),
    )
}
```

Esta protección debe mantenerse porque el aprendizaje no debe ejecutarse
sobre datos desordenados o duplicados.

------------------------------------------------------------------------

## 8. Pruebas después del cambio

Ejecutar:

``` powershell
gofmt -w internal\health\market.go
```

Después:

``` powershell
go test ./...
```

``` powershell
go vet ./...
```

``` powershell
go build ./...
```

Los tres comandos deben terminar sin errores.

------------------------------------------------------------------------

## 9. Prueba funcional

Mantener PAPER:

``` powershell
$env:TRADING_MODE="paper"
$env:MARKET_DATA_PROVIDER="weex"
```

Arrancar:

``` powershell
go run .
```

Esperar a que aparezcan eventos similares a:

``` text
event=BACKFILL_STARTED
event=BACKFILL_COMPLETED
market data: feed activo
event=MARKET_CONNECTED provider=weex
```

Desde otra PowerShell ejecutar:

``` powershell
Invoke-RestMethod http://localhost:8080/health | ConvertTo-Json -Depth 5
```

------------------------------------------------------------------------

## 10. Resultado esperado

Lo importante es obtener:

``` json
"market_data": {
    "connected": true,
    "provider": "weex",
    "symbol": "BTCUSDT",
    "timeframe": "1h"
}
```

y:

``` json
"market_health": {
    "healthy": true,
    "stale": false
}
```

Además debe desaparecer:

``` text
timestamps no monótonos
```

------------------------------------------------------------------------

## 11. Criterio para continuar con Learn

**No ejecutar `/learnibmtf` hasta que esta prueba haya pasado.**

Una vez que tengamos:

``` text
WEEX conectado       ✅
Backfill completado  ✅
5000 velas           ✅
Health saludable     ✅
Paper mode           ✅
```

el siguiente bloque será:

``` text
WEEX
  ↓
histórico almacenado
  ↓
1H
  ↓
15M
  ↓
5M
  ↓
Investing Bulls MTF
  ↓
clasificación de setups
  ↓
aprendizaje
  ↓
modelo persistido
  ↓
validación OOS
```

------------------------------------------------------------------------

## 12. Resumen

  Área                                 Acción
  ------------------------------------ ---------------
  `internal/health/market.go`          **Modificar**
  `internal/store/sqlite.go`           No modificar
  `internal/ingest/weex.go`            No modificar
  `internal/investingbulls/learn.go`   No modificar
  WEEX WebSocket                       No modificar
  Backfill                             No modificar
  Estrategia Investing Bulls           No modificar
  Telegram                             No modificar
  TradingView/Webhooks                 No modificar
  Modo LIVE                            No activar

### Cambio central

De:

``` text
LastCandle + Recent
```

a:

``` text
Recent + LastCandle solo si es posterior a la última Recent
```

Esto evita duplicar la última vela y evita que el monitor compruebe una
secuencia temporal artificialmente desordenada.
