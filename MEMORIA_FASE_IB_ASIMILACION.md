# MEMORIA - Asimilación de la estrategia Investing Bulls (p34)

Estado: **en curso**. Cerradas la **especificación canónica + matriz (Fase 0/1)**, la **detección
(Fase 2)**, el **detector de reacción 1.34/1.53 (Fase 3.5)**, el **perfil de riesgo (Fase 4/3 del
plan)**, los **roles MTF 2H/4H/30m (Fase 3.6)**, la **migración/versionado + SearchSpace persistido y
diagnóstico por setup/componente (Fase 6)** y el **TP parcial 50 % (Fase 5)** en el motor PAPER
(dominio + esquema + motor + tests). **Fase 7 ejecutada con datos reales de WEEX** (puertas A-D y
walk-forward histórico); pendiente solo la **observación PAPER de 100 operaciones** (requiere runtime).
Hallazgo principal: WEEX ignora rangos y limita a 1000 velas por timeframe. **Corrección aplicada:**
el backfill WEEX se acota a 1000 velas (default `WeexBackfillBars` y `GetKlines` sin paginación inerte).

El trabajo sigue `PLAN_IMPLEMENTACION_INVESTING_BULLS.md` (fases, principios y decisiones
bloqueantes) y su plan origen `PLAN_ASIMILAR_ESTRATEGIA_INVESTING_BULLS.md`, leyendo la fuente
`docs/source/estrategia_investing_bulls_p34.txt`.

---

## 0. Fase 0/1 - Especificación canónica y matriz de trazabilidad (cerrada)

Creada `docs/INVESTING_BULLS_STRATEGY.md` como especificación **numerada** y **matriz regla → código →
prueba**, enlazada desde `docs/INVESTING_BULLS.md`. Fuente: `docs/source/estrategia_investing_bulls_p34.txt`
(extracción del PDF, p. 34–94).

- **26 reglas** (R01–R26) con ID, sección/línea fuente, descripción fiel, definición operativa,
  **tipo** (`texto fuente` 📄 vs `decisión de ingeniería` ⚙️), **archivo/función**, **prueba** y
  **estado** (✅ cubierta / 🟡 parcial / ❓ abierta / ➖ n/a).
- **8 preguntas abiertas** (Q1–Q8) que coinciden con las decisiones bloqueantes del plan §14; no se
  inventó regla para ninguna. Las 🟡 corresponden a ellas (OB testeado, 1.34/1.53, riesgo 3–4 %, SL
  1.5/2 %, apalancamiento).
- Matriz de trazabilidad resumida + mapa del pipeline (13 etapas de `PipelineTrace`).
- Los tests existentes por detector (deterministas, con expectativa exacta) hacen de **golden tests**
  de fidelidad; el pipeline completo se cubre con `trace_test.go`. No se añadió lógica nueva.

## 1. Fase 2 - Detección e integración (cerrada)

La zona de **oferta/demanda** se modeló como **cuarto componente independiente** de la confluencia
(no alias de Order Block) y se integró de forma idéntica en learner, walk-forward y LIVE, para que
las tres rutas compartan la misma regla y no exista look-ahead.

- `confluence.go`: cuatro componentes (`fibonacci`, `order_block`, `imbalance`, `supply_demand`) y
  modos fieles `all4` (4/4), `three_of_four` (3/4) y `fibonacci_supply_demand` (pareja). Los modos
  legacy de tres componentes se conservan sin reinterpretarse.
- `zoneselect.go`: selección determinista compartida por learner/MTF/LIVE.
  `SelectSetupFromEvidence` elige por mayor `Score`; a igualdad, la zona con menor
  `DistanceToFibonacci`; a igualdad, la zona 1. `collectEvidenceForZones` mide ambas zonas y
  `hasGenuineConfluence` filtra falsos positivos.
- `learn.go`: `DefaultSearchSpace` usa `FaithfulConfluenceModes()` → **72 configuraciones**
  (6 modos × 4 distancias × 3 stops). `decisionPoint.evidences [2]ConfluenceEvidence`.
- `multitimeframe.go`: `mtfDecisionPoint.evidences [2]ConfluenceEvidence`; misma selección.
- `trace.go` (LIVE): usa `SelectConfluenceZone` (ambas zonas), no `EvaluateConfluenceAtZones`.
- Tests: `TestLearnDecisionPointsMeasureBothZones`, `TestSelectSetupFromEvidence*`,
  `TestCollectEvidenceForZonesMeasuresBothZones`, `TestDefaultSearchSpaceSearchesFourComponentModes`,
  actualización de `confluence_test.go` (72 config.) y `learn_test.go`.

## 2. Fase 3.6 - Roles MTF 2H/4H/30m (cerrada, defaults sin cambiar)

La fuente distingue **tendencia principal (1h/2h/4h)** de **micro tendencia (30m/15m)**, con
confirmación opcional en 5m. No se cambian los defaults (1h/15m/5m): solo se **acepta y valida**
explícitamente el rango ampliado.

- `multitimeframe_config.go`: `ValidateMultiTimeframeConfig` rechaza de forma explícita cualquier rol
  fuera de rango y las temporalidades repetidas; **no sustituye en silencio**. Los swings deben ser
  > 0.
- `LearnMultiTimeframe` valida la config tras normalizarla.
- Tests: `multitimeframe_config_test.go` (roles válidos 2h/4h/30m y rechazos de principal/entrada/
  confirmación no soportados).

## 2b. Fase 3.5 - Detector de reacción en niveles 1.34/1.53 (cerrada, sin auto-operar reversión)

La fuente describe 1.34/1.53 como **zona de reacción para cambio de tendencia** y como **objetivo**.
Estaba implementado solo como objetivo; se añade el **detector de reacción**, respaldado por la
fuente ("tocar la zona no basta, hacen falta confirmaciones") sin inventar un pipeline de entrada.

- `secretzone.go`: `SecretZone` (banda `[min,max]` de `Target1/Target2`, siempre fuera del impulso),
  `NewSecretZone`, `Contains`, `Touch` (incluye mechas) y `EvaluateSecretZone` que clasifica el estado
  de una vela cerrada: `none` / `touch` / `confirmed` / `invalidated`.
- Reglas respaldadas por la fuente: toque **sin** confirmación no habilita nada; con confirmación
  (OB/imbalance) es una reacción válida; la zona sirve además como objetivo de cierre.
- Decisiones de ingeniería (documentadas, no texto fuente): "invalidación" = cierre que cruza toda la
  banda, con prioridad sobre el toque; qué confluencia cuenta como confirmación es política del
  llamante. **No** se auto-operan entradas de reversión (la fuente las llama "más arriesgadas").
- Tests: `secretzone_test.go` (constructor y banda, toque sin confirmación, confirmación válida,
  invalidación por cierre, sin toque, invalidación en banda bajista, `String`).

## 3. Fase 4/3 - Perfil de riesgo `IBRiskProfile` (cerrada)

Perfil auditable y **desacoplado de los detectores** (`internal/risk/ibprofile.go`). No depende de
velas, zonas ni confluencias: solo límites y cálculo de riesgo.

- Constantes: `IBStopDefaultPct = 0.015`, `IBStopMaxPct = 0.02`, `IBMaxLeverage = 20`,
  `IBRiskDefaultPct = 0.01`, `IBDailyLossDefaultPct = 0.01`.
- `Validate()` comprueba coherencia (fallar de forma segura al iniciar). Se invoca en `main.go`
  junto con una comprobación de que `INVESTING_BULLS_MAX_STOP` no exceda el tope del perfil.
- Cálculo separado de **tamaño** (`PositionSize`), **exposición** (`Exposure`), **distancia del
  stop** (`StopDistancePct`) y **apalancamiento** (`Leverage`, `WithinLeverage`).
- Límite diario con definición inequívoca sobre el **balance inicial** (`DailyLossLimit`,
  `DailyLossExceeded`); sin base de balance **falla seguro** bloqueando entradas.
- `ClampStopPct` normaliza al rango del perfil. El trading real y el apalancamiento real siguen
  **deshabilitados**.
- Tests: `ibprofile_test.go` (default válido, cascada de configuraciones inválidas, límite diario
  justo en el borde, cero/negativos y fallo seguro).

### Decisiones de ingeniería (confirmadas por el usuario; NO son texto fuente)

1. **SL**: 1.5% por defecto, **2%** (`IBMaxStopPct`/`MaxStopPctLimit`) como tope absoluto histórico.
2. **Riesgo 3-4% vs pérdida diaria 1%**: el "3-4% por operación" se interpreta como **distancia
   proyectada del objetivo**, no como riesgo de capital. El riesgo de capital por operación se
   mantiene conservador y configurable (1%) y el límite de pérdida diaria realizada es **1%**.
3. **TP parcial**: cerrar **50%** en el máximo/mínimo previo y proteger el resto.
4. **MTF**: soportar 2H/4H (principal) y 30m (entrada) **sin cambiar los defaults**.

## 4. Fase 6 - Migración y versionado (cerrada)

- `migrate.go`: `MigrateLearnedModel`, `MigrateMultiTimeframeModel`, `LoadLearnedModel`,
  `LoadMultiTimeframeModel`. Comprueban la **línea de versión** (`investing-bulls/1.`) y **rechazan
  explícitamente** modelos incompatibles (no reutilización silenciosa). Completan
  `ConfluenceComponents` en modelos legacy y normalizan/validan los timeframes MTF.
- Puntos de carga migrados: `trace.go` (LIVE), `validate.go` (OOS), `multitimeframe.go`
  (`ValidateMultiTimeframeCandidate`) y `main.go`.
- `CodeVersion = "investing-bulls/1.4.0"`.
- **SearchSpace persistido**: `LearnedModel.Search SearchSpace` guarda el espacio efectivo (modos,
  distancias, stops) que se barrió; `ConfigsEvaluated == Search.Size()`. Los campos de `SearchSpace`
  llevan tags `modes/distances/stops` (unmarshal case-insensitive mantiene compatibilidad con specs
  previos). El MTF ya persistía `Timeframes`+`Search` a nivel MTF.
- **Diagnóstico desagregado**: `LearnedModel.SetupStats map[SetupType]SetupStats` (por
  continuación/reversión) y `LearnedModel.ComponentStats map[string]ComponentStats` (por conjunto de
  componentes de confluencia, con `ConfluentCount`). Nuevo `statsByComponent` en `multitimeframe.go`.
  Son campos **aditivos**: los modelos 1.3.0 cargan sin migración (stats vacías = "no registrado").
- Tests: `migrate_test.go` (relleno de componentes legacy, normalización MTF, rechazo de versiones
  incompatibles); `learn_test.go` `TestLearnPersistsSearchSpaceAndDiagnostics` (Search==espacio usado,
  `ConfigsEvaluated==Search.Size()`, SetupStats/ComponentStats coherentes y con atribución completa) y
  `TestStatsByComponentGroupsByComponents`.

## 5. Fase 5 - TP parcial 50 % (cerrada en PAPER y paridad en simuladores)

Implementado el cierre del **50 %** en el objetivo parcial (= máximo/mínimo previo) protegiendo el
resto a **breakeven**. Decisión de diseño (Choice A): el objetivo parcial **es** el `TakeProfit`
actual, por lo que el resto conserva el TP y puede volver a tocarlo en una vela posterior.

- `domain.go`: `MetaKeyPartialTP = "partial_tp"`; `Position.PartialTP/PartialQty/PartialPnL`;
  `Order.PartialTP`; `CloseOptions.NewStopLoss`; nuevo método de store
  `PartialClosePosition(ctx, id, closeQty, exitPrice, exitTS, opts)`.
- `store/sqlite.go`: columnas `positions.partial_tp/partial_qty/partial_pnl` y `orders.partial_tp`
  (+ migraciones idempotentes). `ClosePosition` pliega `PartialPnL` en `NetPnL` (la posición se
  cuenta **una vez** para win rate) y abona a la cuenta solo el neto del resto (el parcial ya se
  abonó), evitando doble conteo. `PartialClosePosition` valida `0 < closeQty < Quantity`, acumula el
  parcial y mueve el stop.
- `paper/paper.go`: `Config.PartialCloseFraction` (default `0.5`); el `Meta` del fill propaga
  `partial_tp`; `checkPosition` prioriza **stop** y, en vela ambigua (stop y parcial/TP a la vez),
  cierra todo al stop con `AmbiguousBar=true`; si no, aplica el parcial (idempotente: se salta si
  `PartialQty > 0`, seguro entre `Recover`/reconcile) y deja el resto con stop a breakeven.
- `trace.go` (LIVE): propaga `partial_tp` en el `Meta` de la señal.
- Tests: `paper_test.go` (50 % y protección a breakeven, no disparo bajo el objetivo, no
  reaplicación, resto en TP con PnL total contado una vez, resto a breakeven, vela ambigua prefiere
  stop, lado short, reconcile sin duplicar parcial tras reinicio) y `store/sqlite_test.go`
  (contabilidad del parcial, persistencia en `OpenPositions`, total sin doble conteo y rechazo de
  cierres parciales inválidos).

### Paridad research ↔ PAPER/LIVE (simuladores)

Para que backtest/learner/MTF y el motor PAPER midan el **mismo** resultado por configuración, la
operativa barra a barra se extrajo a un único `tradeSim` compartido (`learn.go`) usado por
`simulateDecisions` (learner base) y `simulateMTFDecisionPoints` (MTF):

- `LearnConfig.PartialCloseFraction` con default `PartialCloseFractionDefault = 0.5` (coincide con
  `paper.PartialCloseFraction`); se normaliza igual que PAPER.
- El objetivo parcial **es** `plan.TakeProfit` (máximo/mínimo previo). El primer toque cierra la
  fracción y protege el resto a breakeven sin cerrarlo en la misma vela; el stop manda en vela
  ambigua; las dos patas se combinan en un único `PnL` de `LearnedTrade` (la operación se cuenta una
  vez en win rate/drawdown).
- `LearnedTrade.PartialBar/PartialExitPrice` documentan cuándo se cerró la fracción.
- Tests: `learn_test.go` → `TestSimPartialTPClosesFractionAndProtectsRest`,
  `TestSimPartialTPRemainderClosesAtTakeProfit`, `TestSimAmbiguousBarPrefersStopWithoutPartial`,
  `TestSimPartialNotTriggeredBelowTarget`, `TestPartialCloseFractionDefaultAndNormalize`.
- El `backtest` genérico (`internal/backtest`) es el banco de comparación buy&hold/Chandelier/IA y
  no forma parte del aprendizaje de la estrategia: no se tocó.

## 6. Validación de la sesión

- `gofmt -l` limpio en todos los archivos nuevos/tocados de la sesión.
- `go vet ./...` limpio. `go build ./...` OK.
- `go test ./...` → **todos los paquetes OK**.

## 7. OPEN QUESTIONS (según sección 14 del plan)

1. **Riesgo 3-4% vs 1% diario** → resuelto como decisión de ingeniería (ver §3).
2. **SL 1.5% vs `IBMaxStopPct=0.02`** → resuelto (1.5% default, 2% tope).
3. **Papel exacto de 1.34/1.53** → parcial: son `FibConfig.Target1/Target2` (objetivos) **y** ahora
   hay un **detector de reacción** (`secretzone.go`: none/touch/confirmed/invalidated, Fase 3.5). No
   se auto-operan entradas de reversión (la fuente las llama "más arriesgadas" y no precisa el
   disparo); decidir si la reacción debe generar órdenes propias.
4. **Rol/obligatoriedad de 2H/4H/30m** → resuelto (roles soportados, defaults 1h/15m/5m).
5. **Definición de OB testeado** → `ExcludeTestedOrderBlock` existe y es configurable (default
   `false`); la definición exacta sigue pendiente de confirmar.
6. **% exacto del TP parcial** → decisión de ingeniería 50% (ver §3).
7. **Contabilidad de parciales para win rate** → resuelto: la posición se cuenta **una vez** por
   `NetPnL` total (parcial + resto) en `ClosePosition`; el parcial no genera una operación aparte.

## 8. Pendiente (siguiente PR)

### Fase 6 - SearchSpace + diagnóstico (CERRADA)
- Persistido `LearnedModel.Search` (modos/distancias/stops) y diagnóstico por setup
  (`SetupStats`) y por componentes (`ComponentStats`, con `ConfluentCount`). `migrate.go` compat
  con 1.3.0 (campos aditivos). `CodeVersion` → `1.4.0`.
- Resto de Fase 6 no bloqueante: el diagnóstico es agregado (por setup/componente), no por zona
  Fibonacci individual (zona 1 vs 2); y el `SearchSpace` no incluye aún el perfil de riesgo como
  dimensión (se persiste en `TradePlan`/`Confluence` ya elegidos).

### Fase 7 - Validación end-to-end con datos reales (ejecutada; PAPER 100 ops pendiente)

Método: nuevo harness `cmd/ibvalidate` (equivalente no-Telegram de `/learnibmtf` + `/validateibmtf`).
Ejecuta backfill WEEX → diagnóstico de datos → `LearnMultiTimeframeAndPersist` → `ValidateMultiTimeframeCandidate`.
Datos reales de WEEX mainnet (`api-contract.weex.com`), símbolo BTCUSDT, en una DB temporal (no se toca
`data/bot.db`). No se envían órdenes (LIVE off).

**Puerta A (código):** `go build ./...`, `go vet ./...`, `go test ./...` en verde.

**Puerta B (fidelidad):** golden/parity tests del pipeline en verde (learning/backtest/live comparten `tradeSim`).

**Puerta C (datos) — con hallazgos:**
- Timeframes que WEEX ofrece: `1m,5m,15m,30m,1h,4h,12h,1d,1w`. **`2h` NO existe** (HTTP 400).
  `ValidateMultiTimeframeConfig` (Fase 3.6) acepta `2h` como rol de fuente, pero la fuente de mercado
  no lo sirve: configurar `2h` fallaría en el backfill. Defaults (`1h/15m/5m`) no se ven afectados.
- Calidad BTCUSDT 1h/15m/5m: timestamps **monotónicos, sin duplicados, sin huecos, alineados** y no
  stale. ✅
- **Defecto de backfill:** el endpoint `/capi/v3/market/klines` **ignora `startTime`/`endTime`**
  (probado en ms y s, y con `start`/`end`/`from`/`to`) y devuelve como máximo **1000 velas** (las más
  recientes). `historyKlines` (usa `size`) devuelve solo 100 y también ignora rangos. Por tanto
  `Backfill` obtiene **1000 velas/timeframe**, no las 5000 de `WeexBackfillBars`; la paginación de
  `GetKlines` es **inerte** contra la API real (los tests usan un mock que sí honra `startTime`, por
  eso pasan). Consecuencia: con `RequireConfirm`, 5m=1000 velas ≈ 3.5 días → historia común
  insuficiente para MTF (folds `no_train_candidate`).

**Puerta D (riesgo y PAPER):** límites, recuperación de órdenes, TP parcial, balance/equity/PnL y
win-rate cubiertos por tests (`internal/risk`, `internal/paper`, `internal/execution`). Ninguna orden
real (LIVE deshabilitado). Falta la observación PAPER con runtime.

**Puerta E (walk-forward):** ejecutado sobre datos reales. Candidato in-sample **rechazado**
(BTCUSDT 1h/15m; 27 trades, WR 3.7 %, PF 0.044, ret −6.78 %). OOS walk-forward (4 folds 60/10/10):
15 trades, WR 0 %, PF 0, ret −4.55 %, **0/4 folds positivos**. Por diseño (`multitimeframe.go:763`,
`passed := true`) el OOS histórico es diagnóstico y el candidato se activa para observación PAPER;
LIVE sigue off. **Observación PAPER de 100 operaciones: NO completada** (requiere sesión en vivo) →
muestra declarada incompleta, sin relajar filtros.

**Defectos/hallazgos:** (1) WEEX ignora rangos y limita a 1000 velas → backfill corto aunque
`WeexBackfillBars=5000` (config/log engañosos); (2) `2h` aceptado por config pero no ofrecido por la
fuente; (3) con `RequireConfirm` la ventana común es insuficiente con solo backfill WEEX.

**Corrección aplicada (post-validación, defecto 1):** `WeexBackfillBars` por defecto pasa de 5000 a
**1000** (`config/config.go:186`, `internal/ingest/weex.go` `withDefaults`); `WeexProvider.Backfill`
acota `limit` a `RestMaxBars` con *warning* y avisa si `seen < limit`; `GetKlines` ya **no pagina**
(una sola llamada acotada a `RestMaxBars`), reflejando que la API ignora `startTime`/`endTime`. Tests
`TestWeexConfigWithDefaults`, `TestWeexGetKlinesSingleRequestCappedAtRestMaxBars` y
`TestWeexGetKlinesDeduplicatesWithinResponse` cubren el nuevo contrato (los antes llamados
`...PaginatedSortedDeduped`/`...DedupeOverlappingPages` asumían que el origen honra `startTime`, que es
falso en WEEX). Verificado en vivo: log de *warning* y 1000 velas/timeframe. Queda pendiente el
defecto (2): rechazar `2h` en la config MTF.

## 9. Notas de entorno

- Repo con **line endings mixtos** pre-existentes (LF en archivos nuevos, CRLF en varios
  pre-existentes). `gofmt -l` marca archivos CRLF; `multitimeframe.go` ya era no-gofmt-clean en HEAD
  (alineación). No se reformatean líneas ajenas. Los archivos nuevos de la sesión son LF y limpios.
