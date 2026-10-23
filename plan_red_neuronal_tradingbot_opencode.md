# Plan para OpenCode: red neuronal de trading eficiente en GitHub Actions

**Repositorio:** `JoelVargas08/TradingBot`  
**Modo obligatorio:** PAPER; sin operaciones reales.  
**Documentos base:** `rules.md` y `tener en cuenta para crear la red neuronal.txt`.

## 1. Reglas de trabajo

Leer `rules.md` antes de empezar y respetar sus seis principios: optimización, arquitectura limpia, especialización, validación, excelencia y guardar memoria/documentación del trabajo al terminar. Inspeccionar el estado real del repositorio antes de editar; no asumir que el código, las rutas o los workflows anteriores siguen iguales.

## 2. Objetivo y distinción fundamental

Crear gradualmente una **red neuronal pequeña para señales experimentales de trading**, con consumo controlado en GitHub Actions. Separar:

- **Inferencia:** aplicar un modelo ya entrenado a las velas nuevas.
- **Entrenamiento:** ajustar los pesos con datos históricos y nuevos suficientes.
- **Validación:** evaluar la generalización fuera de muestra.

No entrenar cada 15 minutos. Esa frecuencia, si resulta viable en la arquitectura actual, será para inferencia ligera.

**Hill Climbing (HC) y Artificial Bee Colony (ABC) no son redes neuronales**; son métodos de optimización. Dejarlos como experimentos opcionales posteriores para optimizar hiperparámetros o parámetros, una vez exista una línea base neuronal. No incorporarlos a la vez que la primera red sin evidencia de utilidad.

## 3. Frecuencia inicial

| Tarea | Frecuencia propuesta | Reglas |
|---|---|---|
| Inferencia | Cada 15 minutos si puede hacerse sin crear jobs excesivos | Leer solo velas nuevas, preparar características y registrar la predicción |
| Entrenamiento inicial | Manual mediante `workflow_dispatch` | Medir RAM, CPU y duración antes de automatizar |
| Entrenamiento programado | Como máximo una vez al día, solo tras validar coste y estabilidad | Omitirlo si no hay suficientes datos nuevos |
| Evaluación más profunda | Semanal o cuando haya datos suficientes | Comparar con línea base fuera de muestra |
| Promoción del modelo | Manual, nunca automática al principio | Revisión y aprobación explícitas |

No lanzar un workflow de entrenamiento cada 15 minutos. Cada job de Actions puede iniciar un runner nuevo y consumir minutos. Inspeccionar primero la arquitectura actual y escoger la alternativa menos costosa. No depender de un job de Actions como servicio permanente ni prometer ejecución exacta al minuto: los eventos `schedule` pueden retrasarse.

## 4. Presupuesto de recursos

Los siguientes son objetivos iniciales que deben comprobarse midiendo; no representan el consumo universal de una red neuronal:

- Objetivo de RAM del proceso experimental: **menos de 512 MiB**.
- Límite de seguridad configurable: **1 GiB**. Si se supera, cancelar de forma controlada, guardar un informe parcial y no publicar el modelo como válido.
- Un proceso de entrenamiento a la vez; pocos hilos de CPU al principio.
- Modelo pequeño, lotes reducidos y máximos configurables de duración, datos, épocas y tamaño de lote.
- Leer datos por ventanas/lotes cuando corresponda y reutilizar características calculadas sin permitir fugas temporales.
- Registrar RAM pico, duración, CPU aproximada, tamaño de datos/modelo, épocas y estado.
- Si no puede medirse RAM de forma fiable, documentarlo; no afirmar que el límite está garantizado.
- No aumentar tamaño, hilos ni paralelismo sin mediciones.

### Técnicas de eficiencia

1. Elegir una arquitectura pequeña según los datos reales.
2. No añadir TensorFlow/PyTorch u otras dependencias pesadas sin justificar que son necesarias.
3. Usar early stopping basado en validación, nunca en el conjunto de test.
4. Usar gradient checkpointing solo si la arquitectura lo admite y las mediciones justifican el coste extra de cómputo.
5. Considerar cuantización INT8 principalmente para inferencia si el framework y las métricas lo justifican; no asumir que abarata el entrenamiento.
6. Liberar recursos y finalizar cada job limpiamente.
7. Evitar copias completas del histórico por cada candidato o lote.

## 5. Arquitectura desacoplada

Adaptar los nombres a la arquitectura real; no duplicar componentes existentes.

1. **Datos de mercado:** reutilizar el adaptador/proveedor y la persistencia actuales; validar huecos, orden temporal, velas incompletas y frescura.
2. **Características:** transformación determinista compartida entre entrenamiento e inferencia; versionar el orden y evitar normalización con datos futuros.
3. **Modelo:** interfaz para entrenar, cargar, versionar y predecir; registrar semilla, hiperparámetros, versión de código/datos y fecha.
4. **Entrenamiento:** proceso independiente de Telegram; límites, cancelación y artefactos candidatos.
5. **Inferencia:** modelo ya entrenado; ejecutar solo si hay datos nuevos válidos; registrar predicción, timestamp y versión del modelo.
6. **Validación:** splits cronológicos, walk-forward cuando haya suficientes datos, línea base y costes realistas.
7. **Persistencia:** no sobrescribir el modelo aprobado si falla el entrenamiento; evitar guardar secretos o archivos grandes en Git.
8. **Orquestación:** workflow de entrenamiento separado; limitar concurrencia; no bloquear Telegram ni PAPER.
9. **Observabilidad:** resumen legible y artefacto JSON/Markdown con métricas y consumo.

## 6. Entrenamiento y prevención de sobreajuste

Antes de codificar, definir qué se predice, la etiqueta, el horizonte, las características y cómo la predicción se traduce en una decisión PAPER. No inventar un objetivo de aprendizaje sin revisar los datos reales.

- Separar cronológicamente entrenamiento, validación y test.
- Ajustar normalizadores solo con entrenamiento.
- Evitar fuga de información futura en características, etiquetas e indicadores.
- Incluir comisiones y slippage realistas en la evaluación de trading.
- Comparar contra una línea base simple.
- Reportar métricas del modelo y métricas de trading por separado: número de ejemplos/operaciones, retorno neto, drawdown, costes y estabilidad.
- No elegir solo por win rate ni beneficio in-sample.
- Reservar el test final hasta cerrar decisiones de desarrollo.
- Registrar todos los experimentos, incluidos los fallidos.
- Si los datos son insuficientes o los resultados inconclusos, no promover el modelo.
- Los resultados históricos no garantizan rentabilidad futura.

La búsqueda reiterada sobre los mismos datos puede sobreajustar. HC y ABC solo se compararán más adelante, con presupuestos equivalentes y los mismos splits temporales.

## 7. GitHub Actions

1. Inspeccionar tipo de repositorio, workflows, límites de concurrencia y cuota vigente.
2. Crear primero un workflow manual (`workflow_dispatch`).
3. Definir `timeout-minutes`, límites internos y cancelación segura.
4. Evitar dos entrenamientos simultáneos; no cancelar accidentalmente una ejecución necesaria.
5. Guardar informes como artefactos y resumir resultados en el job summary.
6. No lanzar un workflow por candidato.
7. No programar entrenamiento cada 15 minutos.
8. Habilitar entrenamiento diario solo tras medir la primera ejecución y confirmar datos nuevos suficientes; considerar semanal si el coste diario no se justifica.
9. Consultar la documentación vigente antes de afirmar límites de runner o cuotas.

Fuentes oficiales:
- https://docs.github.com/en/actions/reference/runners/github-hosted-runners
- https://docs.github.com/en/actions/how-tos/write-workflows/choose-where-workflows-run/choose-the-runner-for-a-job

## 8. Fases de implementación para OpenCode

### Fase 0 — Inspección sin cambios
- Leer `rules.md` y este plan.
- Revisar rama, cambios locales y estado Git.
- Inspeccionar datos, indicadores, backtest, Investing Bulls MTF, persistencia, Telegram y workflows.
- Confirmar cómo se fuerza el modo PAPER y que el nuevo módulo no puede invocar trading real.
- Identificar dependencias disponibles y puntos de integración reales.

### Fase 1 — Línea base y medición
- Definir datos, características y objetivo con base en el código real.
- Crear una línea base reproducible.
- Medir tiempo y RAM; implementar límites, cancelación y errores controlados.
- Ejecutar las pruebas existentes y guardar resultados iniciales.

### Fase 2 — Red neuronal mínima
- Elegir una arquitectura pequeña y justificarla.
- Separar entrenamiento e inferencia.
- Hacer reproducibles las ejecuciones con semillas/versiones.
- Guardar modelo candidato y metadatos; no activarlo automáticamente.
- Probar entradas inválidas, datos faltantes, orden temporal, guardado/carga y predicciones.

### Fase 3 — Validación temporal
- Evaluar fuera de muestra y comparar con la línea base.
- Comprobar ausencia de fuga de datos.
- Medir métricas predictivas, retorno neto, drawdown, costes y tamaño de muestra.
- Marcar como inconclusos los resultados con datos insuficientes.

### Fase 4 — Inferencia cada 15 minutos
- Decidir si puede reutilizarse el proceso existente o hace falta una ejecución independiente.
- No entrenar dentro del flujo de inferencia.
- Leer solo velas nuevas y evitar duplicados.
- Si el modelo falta, está corrupto/obsoleto o los datos no son fiables, registrar el motivo y abstenerse de producir una señal operable.
- Medir coste y comprobar que no hay solapamientos.

### Fase 5 — Automatización del entrenamiento
- Mantener `workflow_dispatch` al inicio.
- Tras medir el coste, habilitar como máximo una ejecución diaria si se justifica.
- Saltar el entrenamiento si no hay datos nuevos suficientes.
- Guardar informes/modelos candidatos como artefactos.
- Mantener el modelo aprobado intacto ante errores.

### Fase 6 — HC y ABC opcionales
- Solo después de estabilizar la línea base neuronal.
- Confirmar que optimizar hiperparámetros es necesario.
- Igualar presupuesto, datos, costes y splits de ambos métodos.
- Limitar población, evaluaciones, tiempo y RAM.
- Dejarlos desactivados por defecto hasta demostrar utilidad.

### Fase 7 — Pruebas y documentación
Ejecutar lo que corresponda al repositorio:
- `gofmt` para Go modificado.
- `go test ./...`, `go vet ./...`, `go build ./...`.
- Tests Python si se modifica Python.
- Validación YAML/configuración.
- Entrenamiento pequeño e inferencia reproducible.
- Pruebas de límites/cancelación y revisión de secretos.
- Confirmar que no se ha añadido trading real ni promoción automática.

No afirmar que una prueba pasó si no se ejecutó. Corregir los fallos y repetir las pruebas pertinentes.

## 9. Criterios de aceptación

- [ ] Se leyó y respetó `rules.md`.
- [ ] Se implementa una red neuronal real; HC/ABC se describen como optimizadores, no redes neuronales.
- [ ] Entrenamiento e inferencia están separados.
- [ ] La inferencia no lanza entrenamiento.
- [ ] La frecuencia de 15 minutos no provoca jobs innecesarios o solapados.
- [ ] Entrenamiento manual inicialmente y programado solo tras medir coste.
- [ ] Se registran RAM pico, duración y cantidad de datos.
- [ ] Límites configurables de RAM, tiempo, datos y entrenamiento.
- [ ] Validación temporal sin fuga y comparación contra línea base.
- [ ] El modelo nuevo es candidato y no reemplaza automáticamente el aprobado.
- [ ] TradingBot y Telegram siguen funcionando.
- [ ] PAPER sigue siendo obligatorio y no se añade trading real.
- [ ] Las pruebas reales y sus resultados están documentados.
- [ ] El informe final enumera archivos modificados, configuración, resultados, RAM observada, limitaciones y próximos pasos.

## 10. Instrucción final

Trabaja directamente sobre el repositorio actual. Inspecciona, implementa por fases, ejecuta pruebas y mide recursos reales. No te limites a escribir un plan ni a aumentar los recursos de GitHub.

Prioriza la solución más pequeña validable. No prometas rentabilidad, no cambies las reglas fundamentales de Investing Bulls MTF, no actives operaciones reales y no promociones automáticamente modelos. Si una fase no puede completarse, deja el proyecto en estado seguro y explica el bloqueo.

Al terminar, informa:
1. Archivos modificados.
2. Arquitectura y motivo de elección.
3. Comandos y pruebas ejecutados con resultados reales.
4. RAM pico, CPU aproximada y duración observadas.
5. Frecuencias y límites configurados.
6. Limitaciones y siguientes pasos.

## 11. Fuentes de investigación

- GitHub-hosted runners: https://docs.github.com/en/actions/reference/runners/github-hosted-runners
- Elegir runners: https://docs.github.com/en/actions/how-tos/write-workflows/choose-where-workflows-run/choose-the-runner-for-a-job
- Bailey et al., *The Probability of Backtest Overfitting*: https://escholarship.org/uc/item/4w1110bb
- Bailey et al., *Stock Portfolio Design and Backtest Overfitting*: https://escholarship.org/uc/item/2np9b5g9

**Nota:** 512 MiB como objetivo, 1 GiB como límite de seguridad y las frecuencias propuestas son valores iniciales para medir en el runner real, no benchmarks universales.
