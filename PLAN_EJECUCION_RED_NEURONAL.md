# Plan completo: Red neuronal para TradingBot (Fases 0-7)

**Repositorio:** `JoelVargas08/TradingBot`  
**Rama actual:** `fix/telegram-controller`  
**Modo:** PAPER únicamente. Sin trading real. Sin promoción automática.  
**Base:** `rules.md`, `plan_red_neuronal_tradingbot_opencode.md`, `tener en cuenta para crear la red neuronal.txt`

## Estado actual (Fase 0 - Completada)

### Infraestructura
- **Go:** v1.26.2, `go vet` OK, `go build ./...` OK, `go test ./...` OK. 25 archivos con formato pendiente (`gofmt`).
- **Base de datos:** `data/bot.db` con tablas `candles`, `trades`, `positions`, `signals`, `strategies`, `orders`, etc.
- **ML existente:** MLP NumPy puro en `ml/nn/`, `ml/nn_train.py`, `ml/api.py` (sidecar), `ml/nn_promote.py`, `ml/nn_ensemble.py`. Sin TensorFlow/PyTorch.
- **Workflows:** `ml-train.yml` (workflow_dispatch manual), `ml-promote.yml` (manual), `ml-history.yml`, `paper-session.yml`, `telegram-controller.yml`, `ci.yml`.
- **Protecciones:** PAPER forzado en Go, promoción estrictamente manual, holdout reservado, walk-forward con embargo.

### Dependencias Python
- **Disponibles:** `numpy`, `pandas`
- **Faltantes:** `psutil` (train), `fastapi`, `uvicorn`, `joblib` (inferencia)

## Fases de implementación

### Fase 1 - Línea base y medición (PRIORITARIA)
**Objetivo:** Establecer línea base reproducible con costes reales y medir recursos.

#### Pasos
1. **Analizar datos reales**
   - Revisar `data/bot.db` (símbolos/timeframes disponibles, rango temporal, nº velas)
   - Usar `ml/dataset.py:prepare_candles()` para validar OHLCV, duplicados, huecos, orden cronológico
   - Si datos insuficientes → usar `ml/dataset.py:load_synthetic()` solo para medición

2. **Definir objetivo y etiquetas**
   - Usar `ml/labels.py:triple_barrier()` (horizon=24, sigma_mult=1.5 por defecto)
   - Verificar que no hay fuga temporal (uso de volatilidad con shift 1)
   - Confirmar mapeo a decisión PAPER

3. **Establecer línea base**
   - Ejecutar `ml/baselines.py:evaluate_baselines()` con mismos costes que NN
   - Comparar: `always_long`, `always_short`, `momentum_24`, `rsi_30_70`, `macd_hist`
   - Registrar métricas: trades, win rate, retorno neto, drawdown, costes

4. **Medir recursos**
   - Usar `ml/nn/resources.py:ResourceLimiter` (objetivo <512MB, límite seguridad 1GB)
   - Registrar: RAM pico, tiempo total, CPU aproximado, nº barras procesadas
   - Si psutil no disponible → documentarlo explícitamente

**Entregable:** Informe con línea base + mediciones de recursos + calidad de datos

### Fase 2 - Red neuronal mínima (Verificación)
**Estado:** Ya implementado. Solo verificar, ajustar mínimo si falta algo.

#### Verificaciones
- [ ] Separación entrenamiento/inferencia clara (`nn_train.py` vs `api.py`)
- [ ] Guarda candidato en `ml/models/candidates/` sin activar
- [ ] Normalización solo con datos de entrenamiento
- [ ] Walk-forward con embargo (`labels.walk_forward_splits`)
- [ ] Early stopping solo en validación interna (nunca test/holdout)
- [ ] Semillas para reproducibilidad
- [ ] Validación de features, forma de capas, normalización en API
- [ ] Sin dependencias pesadas

**Entregable:** Checklist de verificación + pruebas de guardado/carga

### Fase 3 - Validación temporal (Fortalecimiento)
**Objetivo:** Asegurar robustez sin fuga temporal.

#### Acciones
- [ ] Reportar métricas predictivas y métricas de trading por separado
- [ ] Mantener holdout final reservado (no usar en desarrollo)
- [ ] Verificar embargo entre train/test en walk-forward
- [ ] Marcar resultados como inconclusos si datos insuficientes o trades < umbral
- [ ] Comparación obligatoria con línea base
- [ ] Incluir comisiones + slippage realistas

**Entregable:** Reporte de validación con métricas separadas y estado (conclusivo/inconcluso)

### Fase 4 - Inferencia cada 15 minutos
**Objetivo:** Definir estrategia de inferencia sin entrenar.

#### Decisión
**Recomendado:** Usar sidecar API (`ml/api.py`) como servicio independiente.
- Ejecutar solo si hay modelo aprobado en `ml/models/neural_model.json` + meta
- Leer solo velas nuevas (sin reentrenar)
- Validar frescura de datos y modelo
- Si modelo ausente/corrupto/obsoleto o datos no fiables → **abstenerse** de señal operable
- Medir solapamientos y coste
- No crear workflow de 15min si puede evitarse (minutos de Actions)

#### Acciones
- [ ] Documentar integración con sistema actual (separado de Go core)
- [ ] Añadir validaciones de staleness si no existen
- [ ] Medir coste de inferencia
- [ ] Verificar que nunca dispara entrenamiento

**Entregable:** Diseño de inferencia 15min + medición coste

### Fase 5 - Automatización del entrenamiento
**Objetivo:** Automatizar solo tras mediciones.

#### Acciones
- [ ] Mantener `ml-train.yml` como `workflow_dispatch` (ya está)
- [ ] Tras Fase 1, evaluar cron **diario** o **semanal** solo si justificado
- [ ] Saltar entrenamiento si no hay datos nuevos suficientes
- [ ] Proteger modelo aprobado (nunca sobrescribir)
- [ ] Mantener concurrencia (`tradingbot-ml-train`, cancel-in-progress false)
- [ ] Guardar informes y modelos candidatos como artefactos
- [ ] Resumir en job summary

**Entregable:** Workflow ajustado (si procede) con justificación + mediciones

### Fase 6 - HC y ABC opcionales
**Objetivo:** Solo si aporta valor tras estabilizar NN.

#### Reglas
- [ ] Desactivados por defecto
- [ ] Solo tras línea base neuronal estable
- [ ] Mismos splits temporales, costes, presupuestos (RAM/tiempo)
- [ ] Población/evaluaciones limitadas
- [ ] Documentar como **optimizadores**, NO como redes neuronales
- [ ] Comparación justa con NN

**Entregable:** Documento justificativo (activar solo con evidencia)

### Fase 7 - Pruebas y documentación
**Objetivo:** Validación completa y documentación final.

#### Go
```bash
gofmt -l .                    # 25 archivos pendientes
go vet ./...                  # OK
go build ./...                # OK  
go test ./...                 # OK (cached)
```

#### Python
```bash
py -3 -c "import numpy,pandas; print('ok')"
# Instalar deps si necesario: psutil, fastapi, uvicorn, joblib
```

#### Validaciones
- [ ] Validar workflows YAML
- [ ] Entrenamiento pequeño reproducible (synthetic o subset)
- [ ] Inferencia reproducible con modelo candidato
- [ ] Pruebas de límites/cancelación
- [ ] Revisión de secretos (no commitear)
- [ ] Verificar PAPER obligatorio, sin trading real, sin promoción automática
- [ ] Ejecutar tests Python si existen en `tests/`

**Entregable:** Documentación completa con comandos, resultados reales, métricas

## Comandos propuestos (ejecución)

### Mediciones iniciales
```bash
# Go (verificación)
cd "D:\ETECSA\Programación\GO\Proyectos\Trading algoritmico"
gofmt -l . | wc -l  # o contar
go vet ./...
go build ./...
go test ./...

# Python deps
py -3 -c "import numpy,pandas,psutil,fastapi,uvicorn,joblib" 2>&1
```

### Medición línea base (Python)
```bash
cd ml
py -3 -c "
# Medición con datos sintéticos
from dataset import load_synthetic, prepare_candles
from features import compute_features, nn_feature_matrix
from labels import add_labels
from baselines import evaluate_baselines
from nn.resources import ResourceLimiter
import numpy as np

lim = ResourceLimiter(max_ram_mb=512, max_time_sec=600)
df = load_synthetic(n_bars=8000, seed=123)
df2, q = prepare_candles(df, '1h')
df2 = compute_features(df2)
df2 = add_labels(df2, horizon=24, sigma_mult=1.5)
df2 = df2.dropna(subset=['label'])
close = df2['close'].to_numpy(dtype=float)
feat = df2
res = evaluate_baselines(close, feat, 24, fee_rate=0.0004, slippage_rate=0.0002)
for k,v in res.items():
    print(k, v.get('net_return'), v.get('num_trades'))
print('peak_ram_mb', lim.peak_ram_mb)
"
```

## Criterios de aceptación

- [ ] Fase 0 completada (inspección)
- [ ] Fase 1: Línea base + medición recursos documentados
- [ ] Fase 2: NN verificada, separación correcta
- [ ] Fase 3: Validación temporal con holdout, sin fuga
- [ ] Fase 4: Estrategia inferencia 15min definida (sin entrenamiento)
- [ ] Fase 5: Entrenamiento sigue manual o cron justificado tras medición
- [ ] Fase 6: HC/ABC desactivados o justificados
- [ ] Fase 7: Todas las pruebas ejecutadas con resultados reales
- [ ] PAPER obligatorio mantenido
- [ ] Sin trading real añadido
- [ ] Promoción sigue manual
- [ ] Documentación completa (archivos, arquitectura, comandos, métricas, RAM/CPU/duración, limitaciones)

## Riesgos y mitigaciones

| Riesgo | Mitigación |
|---|---|
| Coste GitHub Actions | Entrenar manual primero, medir antes de cron, no entrenar cada 15min |
| Overfitting | Walk-forward+embargo, holdout reservado, comparación baseline, promoción manual estricta |
| Exceder RAM/tiempo | Límites configurables, early stopping, modelo pequeño (MLP NumPy), sin deps pesadas |
| Fuga temporal | Normalización solo train, features sin lookahead, validación estricta |
| Promoción accidental | `nn_promote.py` con validaciones estrictas, nunca automático |

## Informe final requerido

Al completar todas las fases:
1. **Archivos modificados** (lista con ruta)
2. **Arquitectura y motivo de elección** (explicar separación train/infer, por qué)
3. **Comandos y pruebas ejecutados** con resultados reales
4. **RAM pico, CPU aproximada, duración observadas**
5. **Frecuencias y límites configurados**
6. **Limitaciones y siguientes pasos**
