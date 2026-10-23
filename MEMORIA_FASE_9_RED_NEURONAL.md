# MEMORIA FASE 9 - Red neuronal pequeña para señales experimentales

Fecha: **2026-10-23**. Estado: **implementación completa en código; modelo real aún no promovido**.

Esta fase implementa `plan_red_neuronal_tradingbot_opencode.md` con la solución más pequeña
validable: un MLP NumPy (sin PyTorch/TensorFlow), separación estricta entre entrenamiento e
inferencia, validación temporal y promoción manual. No se añadió trading real ni promoción
automática.

---

## 1. Decisiones de arquitectura

- **MLP NumPy**: 17 features -> capas ocultas (32, 16) -> 3 clases (down/flat/up). Adam,
  dropout, weight decay, early stopping y class weights. Se evita una dependencia pesada para
  mantener el entrenamiento dentro del presupuesto de GitHub Actions.
- **Etiqueta triple barrier** existente (`ml/labels.py`) con horizonte 24 y `sigma_mult=1.5`.
  Clases explícitas: `0=down`, `1=flat`, `2=up`.
- **Sin lookahead**:
  - features calculadas solo con la vela actual y anteriores;
  - normalización ajustada solo con el tramo de entrenamiento;
  - holdout final reservado y walk-forward con embargo;
  - la simulación entra en la vela siguiente (`i+1`), no al cierre observado.
- **Inferencia sin entrenamiento**: `ml/api.py` solo carga JSON aprobado y predice. El
  entrenamiento vive en `ml/nn_train.py`.
- **Promoción manual**: `ml-promote.yml` + `ml/nn_promote.py`. El modelo aprobado queda como
  artefacto `ml-approved`; la sesión PAPER restaura el más reciente. Un candidato inconcluso,
  con pocos trades o que no supera la línea base es rechazado.
- **Integración con WEEX/PAPER**: `internal/ml.SignalSink` alimenta el motor ML solo con el
  símbolo/timeframe configurado. La sesión PAPER existente (1h) ejecuta inferencia; no se creó
  un workflow cada 15 minutos.

## 2. Flujo operativo

1. `ML Train (candidate)` (manual) restaura `data/bot.db` del último artefacto PAPER, entrena y
   sube `ml-candidate` (modelo + informe).
2. `ML Promote (manual)` descarga el candidato por `candidate_run_id`, valida el informe y sube
   `ml-approved` solo con `confirm=yes`.
3. `Paper Session` restaura `ml-approved`; si existe, instala dependencias de inferencia, arranca
   el sidecar y activa `ML_ENABLED=true`. Si no existe o el sidecar falla, la sesión sigue sin ML.
4. Las predicciones se registran en `data/ml_predictions.jsonl` y se archivan con el estado.

## 3. Archivos principales

### Python
- `ml/dataset.py`: carga DB/CSV/sintética, validación, gaps, hash de dataset.
- `ml/nn/config.py`: hiperparámetros y límites (RAM/tiempo/filas).
- `ml/nn/model.py`: MLP + Adam en NumPy.
- `ml/nn/metrics.py`: métricas predictivas y simulación con costes, sin lookahead.
- `ml/nn/registry.py`: candidato/aprobado.
- `ml/nn/resources.py`: límites de RAM/tiempo; `psutil` opcional.
- `ml/nn_train.py`: entrenamiento walk-forward + holdout final + informe JSON.
- `ml/nn_promote.py`: puerta de promoción manual.
- `ml/nn_report.py`: resumen Markdown para el job summary.
- `ml/api.py`: sidecar FastAPI (`/health`, `/predict`, `/predict_batch`, `/reload`).
- `ml/requirements-train.txt`, `ml/requirements-inference.txt`.

### Go
- `internal/ml/sink.go`: adaptador feed WEEX -> motor ML, filtrado por símbolo/timeframe y seed
  desde SQLite.
- `internal/ml/engine.go`: deduplicación de la vela seed, rechazo de velas fuera de orden,
  propagación de `model_version`.
- `internal/ml/client.go`, `internal/domain/domain.go`: metadato de versión del modelo.
- `main.go`: el fanout WEEX incluye el sink ML cuando `ML_ENABLED=true`.

### Workflows
- `.github/workflows/ml-train.yml`: entrenamiento manual, límites, artefacto candidato.
- `.github/workflows/ml-promote.yml`: validación y promoción manual con confirmación explícita.
- `.github/workflows/paper-session.yml`: restaura modelo aprobado, arranca sidecar y persiste
  predicciones.
- `.github/workflows/ci.yml`: tests Go + tests ML.

## 4. Pruebas ejecutadas (resultados reales)

```powershell
gofmt -w main.go internal/ml internal/domain
go build ./...
go vet ./...
go test ./...
```

Resultado: build/vet limpios; todos los paquetes Go OK.

```powershell
py -m unittest discover -s tests/ml -p "test_*.py" -v
```

Resultado: **13 tests OK** (config, recursos/límites, registro, aprendizaje del MLP, pesos,
dataset, métricas sin solapamiento/costes, carga del modelo en el API, validación estricta y
puerta de promoción).

Smoke de entrenamiento sintético:

```powershell
py ml/nn_train.py --synthetic --epochs 2 --splits 2 --out $env:TEMP\nn_test.json --report $env:TEMP\nn_test_report.json
```

Resultado: informe `completed`, validación `inconclusive`, holdout con 0 trades y
`beats_baseline=true`; correctamente no elegible para promoción.

Entrenamiento con la DB local real:

```powershell
py ml/nn_train.py --db data/bot.db --symbol BTCUSDT --timeframe 1h --epochs 2 --splits 2
```

Resultado: 1035 filas limpias, holdout de 207 filas, solo 3 trades y validación `inconclusive`.
La DB local (1083 velas 1h) no alcanza el mínimo de 10 trades del holdout, por lo que **no se
promovió**. Es el comportamiento seguro esperado.

Smoke de inferencia con 200 velas reales y el modelo de prueba: `/predict` y `/predict_batch`
respondieron correctamente, con `model_version` propagada. El sidecar arrancó y `/health`
devolvió `model_loaded=true`.

Smoke de promoción con un informe sintético marcado `complete/eligible/beats_baseline` y 20
trades: `nn_promote.py` generó `neural_model.json`, `neural_meta.json`, `training_report.json`
y `promotion.json`. Con `--min-holdout-trades 0` la promoción se rechaza; también se rechaza
un candidato con `validation_status=inconclusive`. El artefacto de prueba se eliminó del
repositorio (solo se usó un directorio temporal).

## 5. Limitaciones y notas

- La RAM no se pudo medir localmente porque `psutil` no estaba instalado; el informe lo marca
  como `unavailable`. Los workflows instalan `psutil` y medirán `peak_ram_mb` real en Actions.
  La CPU sí se mide con `time.process_time()` y se registra como `cpu_sec`/`cpu_pct`.
- La DB local no tiene datos suficientes para una promoción válida. Para promover hace falta más
  histórico (o un CSV) y que el holdout produzca al menos 10 trades no solapados.
- El modelo aprobado se guarda como artefacto (retención 90 días), no en Git. Si expira, se
  reentrena y se vuelve a promover; la sesión PAPER funciona sin ML mientras no exista.
- `ML_ENABLED` sigue siendo una bandera explícita. Sin modelo aprobado o sin sidecar saludable,
  la sesión PAPER no activa el motor ML.
- No se modificaron las reglas de Investing Bulls ni el modo PAPER. `MODE=live` sigue bloqueado.

## 6. Siguientes pasos recomendados

1. Ejecutar `ML Train` en Actions con la DB restaurada y revisar RAM/duración reales.
2. Acumular más velas o usar un CSV amplio para que el holdout alcance el mínimo de trades.
3. Solo si el informe es `complete`, `eligible_for_promotion=true`, supera la línea base y tiene
   trades suficientes, ejecutar `ML Promote` con `confirm=yes`.
4. Observar `data/ml_predictions.jsonl` y comparar las señales PAPER con la línea base antes de
   considerar cualquier automatización futura.