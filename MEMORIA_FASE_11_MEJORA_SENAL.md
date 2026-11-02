# MEMORIA FASE 11 - Mejora de señal y validación externa

Fecha: **2026-10-26**. Estado: **implementado y evaluado; resultado negativo**. No se promociona
ningún modelo y no se tocó el holdout ETH de este conjunto de features.

---

## 1. Objetivo

La Fase 10 desbloqueó los datos históricos, pero el modelo seguía sin superar la línea base.
La Fase 11 busca una señal con más información y una metodología que evite elegir hiperparámetros
mirando el holdout:

1. Features nuevas, adimensionales y con más contexto de mercado.
2. Selección de `side` y confianza solo con walk-forward.
3. Validación externa en otro símbolo (BTC) antes de gastar el holdout ETH.
4. Promoción solo si el holdout supera la línea base **y** tiene retorno neto positivo.

## 2. Cambios implementados

- `ml/features.py`: `NN_FEATURE_NAMES` con 40 features scale-free (momentum, RSI múltiple, MACD
  normalizado, ATR ratios, volumen z-score, EMAs, Donchian 55, rango 48, volatilidad realizada,
  cuerpo/wicks, hora/día cíclicos). `FEATURE_NAMES` de 17 columnas queda intacto para XGBoost.
- `ml/nn_train.py`: `--side`, `--calibrate`, `--min-calibration-trades`, `--max-fold-loss`,
  `--dev-only`, `--horizon`, `--sigma-mult` y validación externa con
  `--external-db`/`--external-csv`.
- `ml/nn/metrics.py`: simulación por dirección (`both/long/short`), calibración walk-forward con
  mínimo de trades, pérdida máxima por fold y lista de candidatos elegibles.
- `ml/nn/calibration.py`: temperatura ajustada solo con validación interna; se aplica a folds,
  modelo final y validación externa. La temperatura se guarda en el modelo y `ml/api.py` la usa en
  inferencia.
- `ml/nn_promote.py`: rechaza candidatos con `holdout_net_return <= 0`, aunque superen la línea
  base. La validación externa también exige retorno neto positivo.
- `.github/workflows/ml-train.yml`: inputs nuevos para side/calibración/dev-only/horizonte/sigma.
- `ml/nn_report.py` y tests ML actualizados.

## 3. Datos usados

| Dataset | Símbolo | Timeframe | Velas | Rango |
|---|---|---|---|---|
| ETH 30k | ETHUSDT | 1h | 24,994 | 2023-08-09 a 2026-10-26 |
| BTC 8k externo | BTCUSDT | 1h | ~8,000 | 2025-11-27 a 2026-10-26 |

BTC 30k no se pudo descargar por datos históricos corruptos de WEEX (volumen negativo).

## 4. Resultado de la corrida

Configuración: 60 épocas, 4 folds walk-forward, calibración con mínimo 5 trades y pérdida máxima
por fold 2 %. Se ejecutó con `--dev-only` y validación externa BTC.

- Candidato elegido por walk-forward: `side=short`, confianza `0.40`, 27 trades, 62.96 % de
  acierto, retorno compuesto **+15.32 %**, drawdown máximo 9.39 %, 4/4 folds positivos,
  fold mínimo +1.91 %. 15 candidatos elegibles.
- Temperaturas ajustadas: folds `1.75, 1.50, 1.25, 1.20`; modelo final `1.10`.
- Validación externa BTC: 7 trades, 42.86 % de acierto, retorno neto **-7.39 %** frente a
  baseline **-31.86 %**. `passes_gate=false` porque el retorno neto no es positivo y además hay
  7 trades, por debajo del mínimo de 10 usado por la puerta.
- El holdout ETH de este conjunto de features **no se evaluó**. No se generó candidato promovible.

Sin temperature scaling, la corrida anterior eligió `short/0.60` con 15 trades y +13.78 % en
walk-forward, pero el modelo final no generó ningún trade en BTC. La temperatura permitió evaluar
BTC, pero confirmó que la ventaja no se transfiere.

## 5. Interpretación

- El walk-forward de ETH es fuerte, pero no generaliza a BTC. La señal parece específica del
  régimen/activo, no una ventaja estructural.
- En BTC el modelo pierde menos que el baseline alcista, pero sigue perdiendo en absoluto; no es
  apto para promoción.
- No se debe seguir ajustando contra el holdout ETH. La siguiente hipótesis debe ser estructural:
  features de régimen, entrenamiento multi-activo, o una estrategia de selección de exposición
  distinta.

## 6. Verificación

```powershell
py -m unittest discover -s tests/ml -p "test_*.py"   # 20 tests OK
py -c "import yaml; yaml.safe_load(open('.github/workflows/ml-train.yml', encoding='utf-8'))"
```

También se validó el camino no-dev con datos sintéticos y validación externa BTC: el informe
incluye `external`, `external_gate`, `holdout` y `metrics`, y la puerta de promoción exige retorno
neto positivo.
