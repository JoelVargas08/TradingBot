# Fase 13 - Datos 30k, ensemble de semillas y holdout final (2026-10-26)

## Objetivo

Comprobar si la señal NN de ETH generaliza a otros activos y a un período largo,
sin reutilizar el holdout ETH, y decidir si algún candidato puede promoverse.
Sigue siendo PAPER: no hay trading real ni promoción automática.

## 1. Ingesta: saltar velas corruptas

WEEX devuelve algunas velas históricas con OHLCV inválido (volumen negativo o
campos incompletos). Antes, una sola vela corrupta abortaba toda la descarga.
Se añadió `-skip-invalid` a `cmd/mlhistory` y `SkipInvalidHistory` a
`WeexConfig`:

- El parser estricto sigue fallando ante cualquier fila inválida.
- En modo histórico se descartan esas filas y se informa cuántas se saltaron.
- La paginación avanza usando el timestamp crudo, no el de la última vela válida,
  para no dejar huecos ni entrar en bucles.
- `historyPage` conserva `RawRows`/`RawOldest`; una página con filas descartadas
  no se confunde con el final del histórico.
- Tests nuevos: `TestParseWeexKlinesPageSkipInvalid`,
  `TestWeexBackfillSkipsInvalidHistoryAndPaginates`,
  `TestWeexBackfillStrictRejectsInvalidHistory`.

Esto desbloqueó BTC 30k y permitió descargar SOL/BNB/XRP/ADA con el mismo
método.

## 2. Datos descargados

| Activo | Velas | Desde (UTC) | Hasta (UTC) | Gaps | Inválidas descartadas |
|---|---:|---|---|---:|---:|
| ETH | 24,994 | 2023-08-09 | 2026-10-26 | 3 | 0 |
| BTC | 29,999 | 2023-05-25 | 2026-10-26 | 1 | 1 |
| SOL | 26,328 | 2023-10-25 | 2026-10-26 | 1 | 1 |
| BNB | 25,635 | 2023-11-23 | 2026-10-26 | 2 | 4 |
| XRP | 25,636 | 2023-11-23 | 2026-10-26 | 1 | 3 |
| ADA | 25,636 | 2023-11-23 | 2026-10-26 | 2 | 672 |

ADA tuvo muchas velas corruptas; se descartaron 672. El resto quedó con muy
pocas incidencias. Los ficheros están en `%TEMP%\opencode\*_1h_30k_skip.sqlite`.

## 3. Baselines de 30k (horizonte 24)

Retorno neto compuesto con costes (4 bps fee + 2 bps slippage por lado):

| Activo | always_long | always_short | momentum_24 | rsi_30_70 | macd_hist |
|---|---:|---:|---:|---:|---:|
| BTC | -38.38 % | -95.26 % | -92.84 % | -58.33 % | -61.35 % |
| SOL | -43.42 % | -98.10 % | -91.81 % | -77.09 % | -82.06 % |
| BNB | -7.66 % | -95.87 % | -25.63 % | -50.45 % | -58.01 % |
| XRP | -39.90 % | -97.74 % | **+9.46 %** | -75.24 % | -51.58 % |
| ADA | -79.49 % | -97.46 % | -39.50 % | -69.30 % | -98.52 % |

La única regla simple positiva en 30k fue `momentum_24` en XRP. MACD, que había
dado +153.6 % en los folds de ETH y +15.1 % en BTC 8k, pierde en todos los
activos con 30k. Su ventaja anterior era específica de ese tramo, no robusta.

## 4. Semillas individuales a confianza fija 0.35

La calibración por semilla elegía umbrales distintos y a menudo dejaba 0-5
trades. Para comparar de forma justa se fijó `side=both`, `conf=0.35`, sin
calibración, y se evaluó cada semilla en 30k:

- BTC: **5/5 positivas** (+18.2 %, +20.5 %, +68.6 %, +54.4 %, +22.7 %),
  21-325 trades.
- SOL: **3/5 positivas** (-22.9 %, +5.9 %, +167.3 %, +68.1 %, -21.5 %),
  97-221 trades.
- BNB: **5/5 positivas** (+20.3 %, +20.1 %, +31.4 %, +40.9 %, +45.7 %),
  29-228 trades.

En los últimos 8k la dispersión era mucho mayor: BTC 3/5 positivas pero
pequeñas, SOL 2/5, BNB 1/5. La semilla 3 era la única que hacía muchos trades y
solía destacar. Conclusión: la señal existe en 30k, pero la varianza por semilla
y la selección de umbral son un riesgo. Se pasó a un ensemble.

## 5. Ensemble de 5 semillas

Se añadió `ml/nn_ensemble.py`:

- Carga modelos MLP ya entrenados (JSON) sin reentrenar.
- Combina probabilidades (`probs`), logits (`logits`) o voto mayoritario
  (`vote`, por defecto 3/5).
- Permite evaluar todo el histórico, los últimos N bars (`--last-bars`) o un
  rango exacto (`--start-ts`/`--end-ts`).
- Calcula métricas, baselines, referencia MACD y gate con costes.
- `nn_train.py --dev-only --out ...` ahora guarda el modelo de desarrollo para
  poder ensamblarlo sin evaluar el holdout.

El promedio de probabilidades a 0.35 diluía la confianza y dejaba muy pocos
trades (1-4 en 8k). El voto mayoritario conserva las señales fuertes. Se
exploró confianza 0.10-0.35 sobre BTC/SOL/BNB; **0.25** fue el único umbral
redondo que daba retorno positivo y trades suficientes en el período completo y
en los últimos 8k de BTC y SOL. BNB 8k seguía negativo.

### Resultado fijo: voto 3/5, conf 0.25

| Dataset | Período | Neto | Trades | DD | Baseline | MACD ref | Gate |
|---|---|---:|---:|---:|---:|---:|---|
| BTC | 30k | **+155.3 %** | 134 | 7.5 % | -8.4 % | -79.5 % | sí |
| BTC | últimos 8k | **+15.8 %** | 43 | 5.0 % | -31.9 % | +15.1 % | sí |
| SOL | 30k | **+28.1 %** | 201 | 57.7 % | -23.9 % | -87.9 % | sí |
| SOL | últimos 8k | **+13.6 %** | 32 | 12.3 % | -39.3 % | -73.4 % | sí |
| BNB | 30k | **+60.0 %** | 89 | 11.0 % | +5.5 % | -51.3 % | sí |
| BNB | últimos 8k | **-2.3 %** | 30 | 11.0 % | -34.7 % | -6.8 % | no |
| XRP | 30k | **+30.1 %** | 150 | 76.8 % | -31.1 % | -51.0 % | sí |
| XRP | últimos 8k | **+24.1 %** | 31 | 14.3 % | -50.9 % | -55.6 % | sí |
| ADA | 30k | **+117.1 %** | 167 | 53.7 % | -86.1 % | -97.0 % | sí |
| ADA | últimos 8k | **+23.3 %** | 35 | 12.1 % | -58.5 % | +5.4 % | sí |

XRP y ADA no se usaron para elegir el umbral ni el modo de voto; son el test
externo fijo de la configuración. Ambos pasan tanto en 30k como en los últimos
8k, y superan a MACD.

## 6. Holdout final de ETH

Rango reservado: **2026-03-31 22:00 UTC a 2026-10-25 12:00 UTC** (4,983 filas).
Se evaluó una sola vez el ensemble fijo (voto 3/5, conf 0.25):

| Métrica | Valor |
|---|---:|
| Neto | **-7.4 %** |
| Trades | 17 |
| Max drawdown | 13.3 % |
| Baseline long | -15.9 % |
| MACD ref | +4.0 % |
| Gate | **false** |

El ensemble supera la línea base pasiva, pero pierde dinero y no supera a MACD.
Con la regla pre-registrada (`holdout_net_return > 0`, `reference_gate`,
`external_gate`), **no es elegible para promoción**.

## 7. Lectura

- La ingesta de 30k y el ensemble son una mejora real: la señal deja de depender
  de una sola semilla y generaliza a BTC/SOL/BNB/XRP/ADA en 30k.
- También generaliza en los últimos 8k de BTC/SOL/XRP/ADA, pero **falla en ETH**,
  el activo de entrenamiento, y en BNB reciente.
- El fallo de ETH es decisivo: no se promociona. Los buenos resultados externos
  no compensan un holdout negativo.
- Los drawdowns de 30k en SOL/XRP/ADA (53-77 %) son inaceptables para producción
  aunque el retorno sea positivo. Falta gestión de riesgo (stop, volatilidad,
  tamaño) y una validación nueva.
- El holdout ETH ya está consumido para esta configuración. No se debe reutilizar
  para elegir otro umbral o semilla.

## 8. Decisión y siguientes pasos

1. **No promover** el ensemble actual; mantener PAPER y el modelo XGBoost legacy.
2. No seguir probando semillas/umbrales contra el mismo holdout ETH.
3. Si se continúa, la siguiente hipótesis debe ser estructural:
   - entrenamiento multi-activo agrupado (no solo ETH), o
   - meta-labeling sobre MACD, o
   - gestión de riesgo explícita (stop/target/vol targeting) evaluada en un
     período/activo nuevo.
4. Para cualquier variante, reservar un holdout nuevo y elegir umbral dentro de
   un esquema anidado (entrenamiento/validación/test), sin mirar el test.
5. Commit pendiente de Fase 12+13 cuando el usuario lo autorice.

## 9. Verificación

```powershell
go test ./... -count=1
py -m unittest discover -s tests/ml -p "test_*.py"   # 27 OK
py -m py_compile ml/nn_ensemble.py ml/nn_train.py
```

Descarga con tolerancia a velas corruptas:

```powershell
go run ./cmd/mlhistory -db <db> -symbol BTCUSDT -timeframe 1h -bars 30000 -skip-invalid
```

Ensemble fijo:

```powershell
py ml/nn_ensemble.py --models model1.json ... model5.json `
  --db <db> --symbol BTCUSDT --timeframe 1h `
  --conf 0.25 --side both --combine vote --reference macd
```
