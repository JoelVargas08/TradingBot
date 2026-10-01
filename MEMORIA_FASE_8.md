# MEMORIA FASE 8 - Ampliar el aprendizaje Investing Bulls sin rebajar sus reglas

Estado: **completada**. Esta fase implementa `TRADINGBOT_CAMBIOS_LEARN_MTF.md`: el learner deja de
devolver siempre "sin candidata", amplía el espacio de búsqueda sin relajar `MinTrades`, y explica
por qué no encuentra nada cuando no lo encuentra.

---

## 1. El diagnóstico que cambió el diseño

Con el learner anterior, `/learnib` terminaba siempre en `LearnDiagnostics` con cero candidatas. Tres
causas concretas, ninguna arreglable subiendo el número de operaciones:

1. **La confluencia 3/3 era casi imposible.** `EvaluateConfluence` exigía que Fibonacci, imbalance y
   order block confluyeran en la misma vela, y además exigía `price ∈ [Low, High]` del imbalance. Un
   imbalance **no rellenado** es un hueco que el precio aún no ha recorrido: si el precio está dentro
   del hueco, el hueco quedaría llenado. La condición era contradictoria y el componente imbalance
   nunca confirmaba.
2. **La escalera de objetivos Fibonacci era asimétrica.** Para largos se elegía el borde de zona más
   cercano por encima de la entrada, pero para cortos el mismo bucle elegía el nivel MÁS ALTO por
   debajo: un setup corto se medía contra un objetivo mucho más lejano que su espejo largo.
3. **Coste O(n³).** `DetectBreaks` recorría todos los swings por cada vela y el learner lo llamaba una
   vez por vela. 1500 velas tardaban minutos.

## 2. Medición y decisión separadas (`confluence.go`)

- **`ConfluenceMode`**: `all` (3/3), `two_of_three` genérico y las tres variantes por pareja
  (`fibonacci_order_block`, `fibonacci_imbalance`, `order_block_imbalance`).
- **`ConfluenceComponent`** y `RequiredScore()`: 3/3 exige 3, cada variante 2/3 exige 2. `Score`
  sigue contando toda la evidencia confluente, no solo la exigida.
- **`MaxConfluenceDistanceLimit = 0.02`**: tope de zona del perfil, aplicado en `Normalize()`. El
  learner puede probar 0.5%, 1%, 1.5% y 2%, nunca más.
- **`ConfluenceEvidence` + `CollectConfluenceEvidence` + `EvaluateConfluenceAt`**: la evidencia se
  **mide una vez por vela** y cada configuración la reinterpreta. `CurrentPrice`, `FibonacciPrice` y
  `DistanceToFibonacci` son magnitudes distintas y ahora se persisten por separado.
- **Imbalance y order block como soporte**: `respectsImbalance` y el filtro de order block ya no
  exigen que el precio esté dentro de la zona; exigen que **no la haya perforado** por el lado
  equivocado. Un imbalance relleno (`IsFilled`) o perforado sigue sin validar. Esto es lo que hace
  que 2/3 y 3/3 tengan setups reales.

## 3. Perfil de riesgo (`tradeplan.go`)

- **`MaxStopPctLimit = 0.02`** con `Valid()`/`Normalize()`: el 10% de antes quedaba fuera del perfil
  de la estrategia fuente (stop ≈2%). `config/config.go` ya usaba `INVESTING_BULLS_MAX_STOP=0.02`.
- **`planInputs` / `resolvePlanInputs` / `buildPlan`**: los niveles (objetivo estructural, ancla de
  order block, anclas de Fibonacci) no dependen del stop, así que se resuelven una vez por vela.
- **`BuildTradePlanAt`**: la entrada del learner es la apertura siguiente, no el cierre de la vela del
  setup.
- **`structuralStop` unificado**: si el ancla estructural queda al lado equivocado o más lejos que
  `MaxStopPct`, el setup se descarta; si no hay ancla utilizable, el stop es el propio límite de
  riesgo. Antes el learner **caía al stop por porcentaje** cuando el order block quedaba lejos,
  mientras que la ejecución estricta rechazaba el mismo setup: dos reglas distintas para la misma
  entrada, y el learner podía medir una operación que en vivo nunca se habría tomado.
- **`fibTarget` simétrico**: largo y corto eligen el nivel Fibonacci más cercano al otro lado.

## 4. Búsqueda reutilizando decisiones (`learn.go`)

- **`SearchSpace`** con `DefaultSearchSpace`: 4 modos × 4 distancias × 3 stops = **48
  configuraciones**, ordenadas de la más estricta a la más laxa (3/3 y distancias/stops más
  ajustados primero). `normalized()` descarta valores fuera de los topes del perfil y cae a la grid
  por defecto si un espacio llega vacío.
- **`decisionPoint` + `collectDecisionPoints` + `simulateDecisions`**: estructura, Fibonacci,
  imbalances y order blocks se miden una vez; cada configuración solo aplica su modo, distancia y
  stop. **1500 velas x 48 configuraciones ≈ 0.5 s** (antes: minutos por O(n³)).
- **`CodeVersion = "investing-bulls/1.1.0"`**, `ConfluenceComponents` y `ConfigsEvaluated` se
  persisten: una candidata 2/3 es indistinguible de una 3/3 sin ese registro.
- **`LearnedTrade.Components`**: cada operación guarda qué evidencia la produjo.
- **`LearnDiagnostics`** describe la configuración **ELEGIDA** (no la que más operaciones tuvo): el
  criterio de selección es retorno ajustado por drawdown, no el conteo.

### Dos bugs reales encontrados por el camino

- **`json: unsupported value: +Inf`**: una configuración sin pérdidas tiene profit factor infinito y
  `json.Marshal` fallaba, tirando abajo un comando Learn que ya había encontrado una candidata
  válida. Ahora `recordedProfitFactor` limita a `MaxProfitFactorRecorded = 99` y
  `LearnedModel.ZeroLosses` deja constancia de por qué. Cualquier métrica no finita pasa por
  `recordedMetric`.
- **Contadores del diagnóstico que no contaban nada**: "velas con Imbalance / Order Block" siempre
  valían 0 porque nunca se incrementaban. Se eliminaron en favor de contadores sobre los puntos de
  decisión realmente guardados (`setups detectados`, `setups con Fibonacci/Order Block/Imbalance`),
  que sí describen la densidad utilizable.

## 5. MTF: 5m opcional y con explicación (`multitimeframe.go`)

- `ConfirmSkipReason` en el modelo: si se pide confirmación de 5m y no hay histórico, el aprendizaje
  **continúa con 1H+15m** y lo registra, en vez de abortar. Telegram lo muestra con un aviso.
- `MultiTimeframeLearnResult.Diagnostics` propaga el diagnóstico del learn base: el error "el
  aprendizaje base no produjo un candidato" ya dice por qué.
- `simulateMultiTimeframe` usa `EvaluateConfluenceAt` y guarda `Components` por operación.
- El `DatasetHash` MTF cubre los timeframes realmente usados, así que un modelo aprendido sin 5m no
  puede confundirse con uno que sí lo usó.

## 6. LIVE y mensajes (`live.go`, `main.go`)

- `EvaluateLive` evalúa **solo la vela actual** con `EvaluateConfluenceAt` (antes recorría todas las
  velas del prefijo para luego filtrar una).
- `/learnib` y `/learnibmtf` informan de la configuración elegida (modo, componentes, zona, stop) y
  del número de configuraciones evaluadas.

## 7. Validación

- `gofmt` limpio en los archivos tocados, `go vet ./...` limpio, `go build ./...` OK.
- `go test ./...` → **31 paquetes OK**.
- Tests nuevos o reescritos:
  - `confluence_test.go`: los cuatro modos, 2/3 con cualquiera de las tres parejas, 3/3 sigue
    exigiendo los tres, tolerancia measures distancia a la zona (no contenido de la vela), imbalance
    como soporte/impermeable, separación precio actual / precio Fibonacci, grid de 48 configuraciones
    recortada a los topes del perfil, orden determinista, `+Inf` serializable.
  - `tradeplan_test.go`: tope del 2% (un stop del 15% es inválido, no se "normaliza" a algo
    admisible), stop estructural descarta el setup en learner y en ejecución por igual, fallback al
    stop por porcentaje solo cuando no hay ancla, simetría de `fibTarget`.
  - `learn_test.go`: candidata con `MinTrades` respetado y configuración persistida coherente con el
    diagnóstico, determinismo, `datasetDigest`, rechazo de velas inválidas, dataset plano **no**
    produce candidata, `MinTrades=400` no produce candidata y `MinTrades=5` sí, y **no-lookahead**
    verificable mutando velas futuras.
  - `multitimeframe_test.go`: seguir aprendiendo sin velas de 5m y registrar el motivo, rechazar
    1H/15m insuficientes o corruptos, y las señales MTF respetando la dirección de 1H sin solaparse.
- El histórico sintético de los tests **no demuestra rentabilidad**: existe para ejercitar el código.
  La validación real sigue siendo walk-forward OOS sobre velas de mercado.

## 8. Lo que sigue pendiente

- `data/bot.db` está **vacía de velas**: no se pudo ejecutar una validación funcional con datos de
  WEEX/Binance. El primer uso real será `/learnib BTCUSDT 15m` con ingesta activa y después
  `/validateib`.
- Sin datos reales no hay forma de saber si 3/3 con zona del 2% encuentra suficientes operaciones en
  BTCUSDT 15m. Si en la práctica 3/3 no alcanza `MinTrades`, el orden de la grid ya garantiza que las
  variantes 2/3 se prueban después; el diagnóstico dirá cuál era la mejor.

## Notas

- Corrección previa de salud incluida en este mismo commit: `internal/health/market.go` ya no inserta
  `LastCandle` al principio de `Recent`, lo que provocaba el retroceso temporal que disparaba el
  alerta "timestamps no monótonos".
- Cambios de formato (`gofmt`) en `persist.go`, `validate.go`, `walkforward.go`, `setup.go`,
  `orderblock.go`: solo alineación, sin cambio de comportamiento.
