# MEMORIA FASE 12 - Estabilidad entre semillas y reglas de referencia

Fecha: **2026-10-26**. Estado: **evaluado; la NN no es robusta ni supera a una regla simple de
referencia**. No se promociona nada y el holdout ETH sigue intacto.

---

## 1. Objetivo

Antes de cambiar el modelo, comprobar dos cosas:

1. Si el resultado walk-forward de la Fase 11 se mantiene al cambiar la semilla.
2. Si la NN aporta algo frente a reglas simples evaluadas con los mismos costes y el mismo
   simulador.

## 2. Estabilidad entre semillas

Misma configuración que la Fase 11: ETH 30k, 60 épocas, 4 folds, `--calibrate --dev-only`,
`--min-calibration-trades 5 --max-fold-loss 0.02`, externo BTC 8k.

| Semilla | Side/conf | Trades WF | Neto WF | Fold mínimo | Trades BTC | Neto BTC | Gate externo |
|---:|---|---:|---:|---:|---:|---:|---|
| 1 | short/0.40 | 27 | +24.02 % | -1.78 % | 1 | -1.08 % | No |
| 2 | short/0.50 | 11 | +11.46 % | -0.75 % | 0 | 0.00 % | No |
| 3 | both/0.35 | 70 | +30.38 % | -0.91 % | 89 | +7.30 % | Sí |
| 4 | both/0.60 | 22 | +40.61 % | 0.00 % | 0 | 0.00 % | No |
| 5 | both/0.50 | 43 | +20.67 % | -0.44 % | 0 | 0.00 % | No |

- Neto walk-forward: media **+25.43 %**, desviación **10.89 %**, mínimo +11.46 %, máximo +40.61 %.
- Solo **1/5** semillas pasa la validación externa.
- La dirección y la confianza elegidas cambian con la semilla (`short` o `both`, 0.35-0.60).
- La semilla 3 aislada no es evidencia suficiente: parece una coincidencia favorable, no una
  ventaja estable.

## 3. Reglas simples de referencia

Se añadió `ml/baselines.py` y `ml/baseline_check.py`. Todas las reglas usan entrada en la vela
siguiente, salida tras el horizonte, sin solapamiento y con comisión + slippage.

### ETH 30k (24,994 velas, horizonte 24)

| Estrategia | Trades | Acierto | Neto | DD |
|---|---:|---:|---:|---:|
| always_long | 999 | 49.8 % | -64.11 % | 81.43 % |
| always_short | 999 | 45.6 % | -92.00 % | 94.01 % |
| momentum_24 | 998 | 47.6 % | **+12.00 %** | 49.24 % |
| rsi_30_70 | 421 | 54.4 % | -70.76 % | 79.88 % |
| macd_hist | 999 | 47.2 % | -82.74 % | 87.37 % |

### BTC 8k externo (8,000 velas, horizonte 24)

| Estrategia | Trades | Acierto | Neto | DD |
|---|---:|---:|---:|---:|
| always_long | 319 | 48.0 % | -35.13 % | 48.28 % |
| always_short | 319 | 47.3 % | -39.22 % | 53.32 % |
| momentum_24 | 319 | 43.3 % | -41.04 % | 49.74 % |
| rsi_30_70 | 130 | 48.5 % | -24.80 % | 40.45 % |
| macd_hist | 319 | 48.9 % | **+26.09 %** | 23.35 % |

La última vela se recorta en la validación de la NN porque las etiquetas triple-barrier necesitan
el futuro; por eso los valores de referencia en los informes de entrenamiento difieren ligeramente
de la tabla completa.

## 4. Comparación contra una referencia pre-registrada

Se añadió `--reference {none,always_long,momentum,rsi,macd}`. La puerta exige que el modelo supere
esa regla tanto en walk-forward como en externo, además de retorno neto positivo.

- `--reference momentum`: referencia walk-forward **+7.5 %**; las 5 semillas la superan en ETH.
  En BTC la referencia es **-22.2 %**; solo la semilla 3 la supera (+7.3 %).
- `--reference macd`: referencia walk-forward **+153.6 %**; la NN queda en +11.5 % a +40.6 %, así
  que **falla en las 5 semillas**. En BTC la referencia MACD es **+15.1 %** y la semilla 3 hace
  +7.3 %, así que también falla.

Conclusión: la NN no supera a MACD, la regla simple más fuerte en estos datos. El resultado
positivo de la semilla 3 no es un edge del modelo.

## 5. Cambios técnicos

- `ml/baselines.py`: señales y evaluación de `always_long`, `always_short`, `momentum_N`,
  `rsi_30_70` y `macd_hist`.
- `ml/baseline_check.py`: CLI para comparar reglas sobre SQLite/CSV.
- `ml/nn_train.py`: baselines por fold, agregado walk-forward, `--reference` y gate de referencia.
- `ml/nn_promote.py`: rechaza informes con `reference_gate=false` o `external_gate=false`.
- `ml/nn_report.py`: tablas de baselines y referencia en walk-forward y externo.
- `.github/workflows/ml-train.yml`: input `reference`.
- Tests ML: **22 OK**.

## 6. Decisión y siguientes pasos

No se promociona. No se evalúa el holdout ETH. La siguiente línea de trabajo debe ser:

1. Intentar superar explícitamente a `macd_hist` en walk-forward y externo, no solo al baseline
   alcista.
2. Probar multi-activo (ETH+BTC) y features de régimen; el MACD simple ya captura parte del edge
   de tendencia.
3. Añadir más activos externos (SOL, BNB) para que el gate no dependa de un único BTC.
4. Si ninguna variante supera a MACD fuera de muestra, cerrar la línea NN para este par y pivotar.
