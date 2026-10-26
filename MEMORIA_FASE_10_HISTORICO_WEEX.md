# MEMORIA FASE 10 - Histórico profundo de WEEX para entrenar la red neuronal

Fecha: **2026-10-26**. Estado: **implementado, probado y validado en vivo**.

Esta fase resuelve el bloqueo de datos que impedía entrenar/promover la red neuronal de la Fase 9:
la base local solo tenía ~1000 velas y el holdout producía pocos trades. La causa no era el modelo,
sino el endpoint REST usado para el backfill.

---

## 1. El problema

La Fase 9 documentó que el backfill obtenía ~1000 velas/1h y que el holdout tenía solo 3 trades.
La investigación previa (`MEMORIA_FASE_IB_ASIMILACION.md`, `PROGRESO.md`) concluyó que WEEX
ignoraba `startTime`/`endTime`, pero se había probado solo `/capi/v3/market/klines` y una variante
`historyKlines` con el parámetro `size`.

Verificación en vivo del 2026-10-26:

| Prueba | Resultado |
|---|---|
| `/klines?startTime=2021-01-01&endTime=2021-01-03&limit=5` | 5 velas, pero **las más recientes de 2026**. Ignora el rango. |
| `/klines?limit=1000` | 1000 velas recientes como máximo. |
| `/historyKlines?...&startTime=2021-01-01&endTime=2021-01-03&size=100` | **49 velas** de 2021 (2 días). Sí honra el rango. |
| `/historyKlines` con rango > 90 días | HTTP 400 `{"code":-1170,"msg":"Time range cannot exceed 90 days."}` |
| `/historyKlines?size=5` / `size=100` / `size=1000` | Siempre devuelve hasta **100 velas**; `size` no amplía el tope. |
| `/historyKlines?startTime=2021-01-10&endTime=...` y siguiente página con `endTime = ts_más_antiguo - 1ms` | Paginación correcta: 100 + 100 + 19 velas sin huecos. |

**Causa raíz:** el backfill usaba `/klines`, que solo entrega una ventana móvil de 1000 velas e
ignora los rangos. Por eso la DB no crecía hacia atrás y el entrenamiento no tenía muestra
suficiente. La API sí ofrece histórico paginable en `/historyKlines`; solo hay que respetar sus
límites (90 días por consulta, 100 velas por respuesta) y paginar por `endTime`.

## 2. La solución

Se sustituyó la descarga de `/klines` por paginación real sobre `/historyKlines`:

1. Se pide una ventana de como máximo **90 días** (`HistoryMaxRange`).
2. La API devuelve hasta **100 velas** (`HistoryMaxBars`) dentro de `[startTime, endTime]`.
3. Si la página vino completa, se avanza hacia atrás con
   `endTime = timestamp_de_la_vela_más_antigua - 1 ms`.
4. Cuando la ventana se agota, se salta a la ventana anterior de 90 días.
5. Se deduplica, se ordena ascendente y se recorta al `limit` pedido.
6. Se reintentan 429/5xx con backoff y se separan las páginas con `HistoryPageDelay`
   (50 ms por defecto) para no saturar el rate limit.

Esto permite descargar años de 1h o semanas de 5m sin depender de Binance (bloqueado con HTTP 451
desde este entorno) ni de acumular velas por streaming durante meses.

### 2.1 Arquitectura

- `internal/ingest/weex.go` es la única capa que conoce WEEX. `GetKlines` y `Backfill` mantienen su
  contrato (`domain.Kline`, `domain.CandleStore`); el resto del bot no cambia.
- `cmd/mlhistory` es un adaptador de datos para ML: descarga histórico, lo persiste en la tabla
  `candles` y exporta CSV. No entrena ni predice.
- `ml/` sigue siendo el dominio de entrenamiento/inferencia. El entrenamiento no descarga datos por
  sí mismo; consume SQLite/CSV, igual que antes.
- `.github/workflows/ml-history.yml` produce el artefacto `ml-history`; `ml-train.yml` lo restaura
  con `source=history`. Sin acoplar el workflow de PAPER ni el feed en vivo.

## 3. Archivos modificados

### Go
- `internal/ingest/weex.go`: paginación con `/historyKlines`, ventanas de 90 días, 100 velas/página,
  reintentos y `HistoryPageDelay`; `Backfill` deja de estar limitado a 1000 velas.
- `internal/ingest/weex_test.go`: tests de paginación, ventanas de 90 días, deduplicación y rango.
- `cmd/mlhistory/main.go`: comando para sembrar histórico en SQLite y exportar CSV.
- `cmd/mlhistory/main_test.go`: tests del comando (incluye descarga end-to-end contra un servidor
  HTTP falso y exportación CSV).
- `cmd/ibvalidate/main.go`: ayuda de `-limit` actualizada.

### Workflows
- `.github/workflows/ml-history.yml` (nuevo): descarga manual de histórico profundo; artefacto
  `ml-history` con `bot.db`, CSV y `history_report.json`.
- `.github/workflows/ml-train.yml`: nueva opción `source=history` que restaura el artefacto
  `ml-history` y entrena contra esa DB.

### ML
- `ml/nn_train.py`: `eligible_for_promotion` ahora exige también `beats_baseline=true`. La puerta
  de promoción ya lo validaba, pero el informe ahora no puede marcar como elegible un modelo que no
  supera la línea base.

### Documentación
- `PROGRESO.md` §6.1 y backlog: corregido el hallazgo de WEEX.
- `docs/INVESTING_BULLS_STRATEGY.md` §6.1: misma corrección.
- `MEMORIA_FASE_IB_ASIMILACION.md`: nota de actualización al inicio.

## 4. Comandos y resultados reales

### Tests Go

```powershell
gofmt -w internal/ingest cmd/mlhistory cmd/ibvalidate
go build ./...
go vet ./...
go test ./... -count=1
```

Resultado: build y vet limpios; **todos los paquetes Go OK**. `internal/ingest` tardó ~6.1 s
(los tests de reintentos esperan el backoff) y `cmd/mlhistory` ~0.6 s.

### Tests Python

```powershell
py -m unittest discover -s tests/ml -p "test_*.py"
```

Resultado: **13 tests OK** (2.05 s).

### Descarga real de WEEX

```powershell
go run ./cmd/mlhistory -db $env:TEMP\mlhistory_smoke.db -symbol BTCUSDT -timeframe 1h -bars 300 -csv $env:TEMP\mlhistory_smoke.csv
```

Resultado: **300 velas**, 0 huecos, CSV de 301 líneas (cabecera + 300), desde
`2026-10-14T00:00:00Z` hasta `2026-10-26T11:00:00Z`.

```powershell
go run ./cmd/mlhistory -db $env:TEMP\mlhistory_deep.db -symbol BTCUSDT -timeframe 1h -bars 5000
```

Resultado: **5000 velas**, 0 huecos, desde `2026-04-01T04:00:00Z` hasta
`2026-10-26T11:00:00Z` (~208 días, cruzando tres ventanas de 90 días). Duración observada:
**32.5 s** (incluye ~50 páginas y el delay de cortesía).

### Entrenamiento con la DB profunda

```powershell
py ml/nn_train.py --db $env:TEMP\mlhistory_deep.db --symbol BTCUSDT --timeframe 1h `
  --epochs 2 --splits 2 --max-bars 5000 `
  --out $env:TEMP\nn_deep.json --report $env:TEMP\nn_deep_report.json
```

Resultado del smoke (2 épocas, solo validación de que el dato desbloquea el pipeline):

- 5000 filas de entrada, 0 inválidas, 0 duplicadas, 0 huecos.
- Holdout: **990 filas**, **12 trades** (antes eran 3 y `inconclusive`).
- `validation_status=complete`.
- `beats_baseline=false` → `eligible_for_promotion=false` (la puerta rechaza el modelo).
- Walk-forward: 30 trades, win rate 46.7 %, retorno compuesto -0.43 %.
- Holdout: retorno del modelo -5.04 % vs baseline +0.67 %.

Conclusión: el problema de datos está resuelto y el mecanismo de seguridad funciona; el modelo
actual **no debe promoverse** porque no supera la línea base. Esto es el resultado esperado y no un
fallo del pipeline.

## 5. Cómo usarlo en GitHub Actions

1. Ejecutar manualmente **`TradingBot - ML History (WEEX)`** con `symbol=BTCUSDT`,
   `timeframe=1h`, `bars=8000` y `csv=true`. Genera el artefacto `ml-history`.
2. Ejecutar **`TradingBot - ML Train (candidate)`** con `source=history`, `max_bars=8000` y las
   épocas deseadas. Restaura la DB histórica y entrena.
3. Revisar el job summary y el informe. Solo si `validation_status=complete`,
   `eligible_for_promotion=true` y `beats_baseline=true`, ejecutar **`ML Promote`** con
   `confirm=yes`.
4. Para más historia, subir `bars` (máx 200000). Para 5m, 8000 velas ≈ 28 días; para 1h,
   8000 velas ≈ 333 días. El comando respeta el tope de 90 días por consulta automáticamente.

## 6. Límites y notas

- **Rate limit:** no se conoce el límite exacto publicado de `/historyKlines`; se usa un delay de
  50 ms entre páginas y backoff ante 429/5xx. Para descargas muy grandes conviene subir
  `HistoryPageDelay` si aparecen muchos 429.
- **Tope por llamada:** 100 velas; no se puede pedir más en una sola respuesta. El tiempo de
  descarga crece linealmente con las velas pedidas.
- **Rango máximo:** 90 días por consulta. El código divide el rango automáticamente.
- **Fin del histórico:** si una ventana completa de 90 días no devuelve velas, la descarga se
  detiene (asume que no hay datos más antiguos). No se han observado huecos de 90 días en WEEX;
  si existieran, habría que revisar ese corte.
- **Datos por exchange:** se usa WEEX como fuente, así que el histórico coincide con el mercado de
  ejecución. No se introdujo una dependencia de Binance (además, Binance respondió HTTP 451 desde
  este entorno).
- **`2h` sigue sin existir en WEEX:** el defecto pendiente de rechazarlo en la configuración MTF no
  se tocó en esta fase.
- **Sin trading real:** no se modificaron reglas de Investing Bulls, PAPER ni el bloqueo de LIVE.
- **Caché de Python:** se generaron `__pycache__` al ejecutar tests; no son cambios de código.

## 7. Siguientes pasos

1. Ejecutar `ML History` en Actions y verificar RAM/duración reales del runner.
2. Entrenar con más épocas (`80`) y 4 splits usando `source=history` y 8000+ velas.
3. Mejorar la señal (features/objetivo/arquitectura) hasta que el holdout supere la línea base con
   costes; no promocionar solo por win rate.
4. Solo entonces ejecutar `ML Promote` y observar `data/ml_predictions.jsonl` en PAPER.
5. Mantener el modelo aprobado intacto si un candidato falla o es rechazado.
