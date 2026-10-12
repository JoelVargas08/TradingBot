# The Investing Bulls — Especificación canónica y matriz de trazabilidad

Este documento es la **especificación canónica** de la estrategia *The Investing Bulls* tal como
la implementa el bot. Cada regla enlaza **fuente → definición operativa → código → prueba**. El
objetivo es fidelidad y trazabilidad, no maximizar el número de entradas (ver
`PLAN_IMPLEMENTACION_INVESTING_BULLS.md`, Fases 0 y 1).

- **Fuente primaria (extracción):** `docs/source/estrategia_investing_bulls_p34.txt`
  (extraído del PDF del autor, páginas **34–94**).
- **Documento funcional relacionado:** `docs/INVESTING_BULLS.md` (flujo y comandos).
- **Memoria de ejecución:** `MEMORIA_FASE_IB_ASIMILACION.md`.
- **Versión de código de referencia:** `investingbulls.CodeVersion = "investing-bulls/1.4.0"`.

> Las referencias `L##` apuntan a líneas del archivo de extracción. `pdf` indica la sección del
> documento fuente. El `.txt` no conserva los números de página; se indexa por línea y se cita la
> sección para poder cotejar contra el PDF.

## 1. Leyenda

**Tipo**

| Marca | Significado |
|---|---|
| 📄 `texto fuente` | Regla descrita explícitamente en el PDF. |
| ⚙️ `decisión de ingeniería` | No es texto fuente: parámetro o interpretación del sistema, documentado aquí y en `MEMORIA_FASE_IB_ASIMILACION.md`. |

**Estado**

| Marca | Significado |
|---|---|
| ✅ `cubierta` | Fuente + definición operativa + código + test que la verifica. |
| 🟡 `parcial` | Implementada con alcance limitado, o parte de la regla sigue siendo decisión abierta. |
| ❓ `abierta` | La fuente no resuelve la definición; **no se inventó** la regla. |
| ➖ `n/a` | Fuera del alcance del motor (material psicológico del autor, etc.). |

## 2. Mapa del pipeline (orden de evaluación)

Coincide con el orden de `PipelineTrace` (`internal/investingbulls/trace.go`), reutilizado por
PAPER/LIVE y por el diagnóstico `/ibdiag`:

```
datos → tendencia principal → setup/estructura (1H/15m) → alineación MTF → Fibonacci →
imbalance → Order Block → oferta/demanda → confluencia → confirmación 5m (opcional) →
plan de trade (entrada/stop/targets) → riesgo → señal aceptada / motivo de rechazo
```

## 3. Especificación numerada de reglas

### 3.1 Estructura y tendencia

#### R01 — Identificación de tendencia por máximos y mínimos 📄 ✅
- **Fuente:** L32–40, L86–92, L397–417 (tendencia = máximos y mínimos cada vez más altos o más
  bajos).
- **Definición operativa:** se detectan swings confirmados (pivotes con `left`/`right` velas a cada
  lado) y se clasifica la tendencia como alcista, bajista o rango según la secuencia de máximos y
  mínimos.
- **Código:** `internal/investingbulls/structure.go` → `DetectSwings`, `Analyze` (tipo `Trend`).
- **Prueba:** `structure_test.go` → `TestDetectSwings`, `TestAnalyzeTrend`.
- **Estado:** ✅ cubierta.

#### R02 — CHOCH (cambio de estructura) 📄 ✅
- **Fuente:** L41–48, L177–182, L526–531 (cambio de tendencia cuando el último retroceso rompe el
  mínimo/alto anterior).
- **Definición operativa:** un `StructureBreak` contra la estructura previa marca CHOCH; cambia la
  dirección operativa (de alcista a bajista y viceversa).
- **Código:** `structure.go` → `DetectBreaks`; `setup.go` → `ClassifyBreak`.
- **Prueba:** `structure_test.go` → `TestCHOCHAfterOppositeStructure`; `setup_test.go` →
  `TestClassifyBreakFamilies`.
- **Estado:** ✅ cubierta.

#### R03 — Confirmación por **cierre de vela** 📄 ✅
- **Fuente:** L529–531 ("para que sea un cambio de tendencia debe marcar un nuevo máximo o mínimo
  **con cierre de vela**, no basta que sea solo por mecha").
- **Definición operativa:** la ruptura solo es válida si el **cierre** de la vela supera el nivel;
  mechas no cuentan. Se evalúa solo sobre velas cerradas.
- **Código:** `structure.go` → `DetectBreaks`; `multitimeframe.go` → `closedCandlesBefore`.
- **Prueba:** `structure_test.go` → `TestDetectBreakRequiresClose`;
  `multitimeframe_test.go` → `TestClosedCandlesBeforeExcludesCandleOpeningAtDecisionTime`.
- **Estado:** ✅ cubierta.

#### R04 — BOS / continuación de tendencia 📄 ✅
- **Fuente:** L93–109, L223–229.
- **Definición operativa:** un nuevo máximo (tendencia alcista) o mínimo (bajista) con cierre más
  allá del extremo anterior indica continuación.
- **Código:** `structure.go` → `DetectBreaks`; `setup.go` → `ClassifyBreak`.
- **Prueba:** `setup_test.go` → `TestClassifyBreakRejectsCounterTrendBOS`.
- **Estado:** ✅ cubierta.

#### R05 — Los cuatro setups 📄 ✅
- **Fuente:** L22–24, L76–77, L161–162, L211–214 (venta/compra por cambio y por continuación).
- **Definición operativa:** familias operativas `choch_long`, `choch_short`, `continuation_long`,
  `continuation_short`. Se conservan y se miden por separado.
- **Código:** `setup.go` → `SetupType`, `ClassifySetups`.
- **Prueba:** `setup_test.go` → `TestClassifyBreakFamilies`.
- **Estado:** ✅ cubierta.

### 3.2 Fibonacci y selección de zona

#### R06 — Trazado de Fibonacci direccional 📄 ✅
- **Fuente:** L50–52, L115–116, L184–186, L230–233 (ventas: del último máximo al último mínimo;
  compras: del mínimo al máximo).
- **Definición operativa:** el `Fibonacci` se construye desde los swings confirmados
  (`Origin`→`Destination`) y expone zonas y proyecciones.
- **Código:** `fibonacci.go` → `NewFibonacci`, `NewFibonacciFromSwings`.
- **Prueba:** `fibonacci_test.go` → `TestNewFibonacciBullish`, `TestNewFibonacciBearish`.
- **Estado:** ✅ cubierta.

#### R07 — Dos zonas de retroceso (0.45–0.50 y 0.72–0.85) 📄 ✅
- **Fuente:** L55–56 (zonas claves 0.45–0.5 y 0.72–0.85).
- **Definición operativa:** `FibConfig.Zone1Min/Max = 0.45/0.50`, `Zone2Min/Max = 0.72/0.85`.
- **Código:** `fibonacci.go` → `DefaultFibConfig`, `InZone`, `Zone`.
- **Prueba:** `fibonacci_test.go` → `TestFibonacciCustomConfig`.
- **Estado:** ✅ cubierta.

#### R08 — Analizar **ambas** zonas y elegir la de más confluencias 📄 ✅
- **Fuente:** L117–128, L158–160, L187–199, L466–470, L493–506.
- **Definición operativa:** se evalúan las dos zonas y se elige por regla determinista
  (más componentes → más puntuación → menos distancia → orden de zona). La elección queda
  registrada en la evidencia.
- **Código:** `zoneselect.go` → `SelectSetupFromEvidence`, `SelectConfluenceZone`,
  `collectEvidenceForZones`; `confluence.go` → `EvaluateConfluenceAtZones`.
- **Prueba:** `zoneselect_test.go` → `TestSelectSetupFromEvidencePrefersHigherScore`,
  `TestSelectSetupFromEvidenceBreaksTiesByDistance`; `learn_test.go` →
  `TestLearnDecisionPointsMeasureBothZones`.
- **Estado:** ✅ cubierta.

### 3.3 Confirmaciones (Order Block, Imbalance, Oferta/Demanda)

#### R09 — Order Block 📄 ✅
- **Fuente:** L27–28, L62–63, L122–135, L144–146.
- **Definición operativa:** última vela opuesta antes de un impulso direccional; se detecta y se
  actualiza su estado (fresco / testeado / invalidado).
- **Código:** `orderblock.go` → `DetectOrderBlocks`, `UpdateOrderBlocks`.
- **Prueba:** `orderblock_test.go` → `TestDetectBullishOrderBlock`, `TestDetectBearishOrderBlock`,
  `TestUpdateBullishOrderBlock`, `TestUpdateBearishOrderBlockInvalidation`.
- **Estado:** ✅ cubierta.

#### R10 — Descartar Order Block ya testeado 📄 🟡
- **Fuente:** L122–128 (OB ya tocado por una vela toma liquidez → se descarta esa zona).
- **Definición operativa:** un OB testeado/consumido por liquidez no cuenta como confirmación
  fresca. Comportamiento **configurable** vía `ConfluenceConfig.ExcludeTestedOrderBlock`
  (default `false`, para no alterar modelos previos).
- **Código:** `orderblock.go` → `UpdateOrderBlocks`; `confluence.go` → `ExcludeTestedOrderBlock`.
- **Prueba:** `confluence4_test.go` → `TestConfluenceExcludeTestedOrderBlock`.
- **Estado:** 🟡 parcial — la **definición exacta** de "OB testeado" (umbral de penetración,
  número de velas) no está fijada por la fuente; es pregunta abierta §5.Q5.

#### R11 — Imbalance / FVG no rellenado 📄 ✅
- **Fuente:** L64–67, L136–137, L158, L191–193, L202–203.
- **Definición operativa:** un imbalance puede confluir solo si **aún no ha sido rellenado**; el
  estado del gap se actualiza vela a vela. Entrar hacia la zona que cierra el imbalance previo.
- **Código:** `imbalance.go` → `DetectImbalances`, `UpdateImbalances`.
- **Prueba:** `imbalance_test.go` → `TestUpdateBullishImbalanceFill`,
  `TestUpdateBearishImbalancePartialFill`; `confluence_test.go` →
  `TestEvaluateConfluenceRejectsFilledImbalance`, `TestConfluenceTreatsUnfilledImbalanceAsSupport`.
- **Estado:** ✅ cubierta.

#### R12 — Zona de oferta/demanda (cuarto componente) 📄 ✅
- **Fuente:** L13, L217, L243–248, L472–475, L599–603.
- **Definición operativa:** zona de oferta/demanda como componente **independiente** (no alias de
  Order Block), con creación, invalidación y condición de "zona testeada".
- **Código:** `supplydemand.go` → `DetectSupplyDemandZones`, `UpdateSupplyDemandZones`.
- **Prueba:** `supplydemand_test.go` → `TestDetectDemandZone`, `TestDetectSupplyZone`,
  `TestUpdateDemandZoneTestedAndInvalidated`.
- **Estado:** ✅ cubierta.

#### R13 — Confluencia = unión de dos o más confirmaciones 📄 ✅
- **Fuente:** L7–13 ("confluencia de estas 3 confirmaciones": OB, Imbalance, Nivel Fibo, Zona de
  oferta/demanda), L61, L139–141, L597–603.
- **Definición operativa:** al menos **dos** componentes en la misma dirección y proximidad
  (tolerancia configurable). Los modos aprobados: 3/4, 4/4 y parejas con Fibonacci obligatorio.
  La fuente lista **cuatro** componentes aunque los rotula como "3"; se modelan los cuatro.
- **Código:** `confluence.go` → `EvaluateConfluence`, `ConfluenceMode`, `FaithfulConfluenceModes`.
- **Prueba:** `confluence_test.go` → `TestEvaluateConfluenceRequiresThreeFactors`,
  `TestConfluenceTwoOfThreeRequiresFibonacci`; `confluence4_test.go` → `TestFaithfulConfluenceModes`,
  `TestConfluenceAll4RequiresSupplyDemand`, `TestConfluenceThreeOfFourRequiresFibonacciAndThree`,
  `TestConfluenceFibonacciSupplyDemandPair`.
- **Estado:** ✅ cubierta.

### 3.4 Plan de entrada, stop y objetivos

#### R14 — Entrada en la zona fibo con confluencia (orden límite) 📄 ✅
- **Fuente:** L57–59, L143–146, L200–203.
- **Definición operativa:** la entrada se sitúa en el borde de la zona/OB que cierra el imbalance y
  coincide con la zona fibo elegida.
- **Código:** `tradeplan.go` → `BuildTradePlanAt`, `resolvePlanInputs`.
- **Prueba:** `tradeplan_test.go` → `TestBuildTradePlanAtUsesExplicitEntry`.
- **Estado:** ✅ cubierta.

#### R15 — Stop loss estructural detrás del OB, antes de la otra zona 📄 ✅
- **Fuente:** L68–70 (stop por encima del OB y antes de la otra zona fibo), L205–206, L250–251,
  L312–332 (fuera de fuente: mecánica estructural).
- **Definición operativa:** stop por encima del OB (ventas) / por debajo (compras); acotado al
  perfil (clamp a `IBStopMaxPct`). Si el stop estructural es demasiado ancho, se descarta el setup.
- **Código:** `tradeplan.go` → `BuildTradePlanAt`, `structuralStop`, `fibStopAnchor`;
  `risk/ibprofile.go` → `ClampStopPct`.
- **Prueba:** `tradeplan_test.go` → `TestTradePlanRejectsStopAboveMaximum`,
  `TestTradePlanDropsSetupWhenStructuralStopTooWide`,
  `TestTradePlanConfigClampsStopToProfileLimit`.
- **Estado:** ✅ cubierta.

#### R16 — Take Profit en el extremo **anterior** 📄 ✅
- **Fuente:** L70–72 (TP en el mínimo o por debajo, ventas), L205–206 y L250–251 (TP en el último
  alto o pasando, compras), L513–517 (cerrar la mayoría al tocar el alto anterior).
- **Definición operativa:** objetivo = máximo/mínimo previo confirmado (`previousSwingTarget`).
- **Código:** `tradeplan.go` → `previousSwingTarget`, `fibTarget`.
- **Prueba:** `tradeplan_test.go` → `TestBuildLongTradePlan`, `TestBuildShortTradePlan`,
  `TestTradePlanRequiresTargetBeyondEntry`, `TestFibTargetSymmetry`.
- **Estado:** ✅ cubierta.

#### R17 — Niveles secretos 1.34 / 1.53 📄 🟡
- **Fuente:** L255–380 (zona de reacción para cambio de tendencia **y** objetivo si estás en
  posición).
- **Definición operativa:** dos usos explícitos en la fuente:
  1. **Objetivo:** `FibConfig.Target1 = 1.34`, `Target2 = 1.53`, usados como proyecciones de
     objetivo (`fibTarget`).
  2. **Zona de reacción:** `SecretZone` (banda `[min,max]` de ambos niveles) con evaluación de
     reacción sobre velas cerradas:
     - `touch` — toca la banda **sin** confirmación → **no** habilita entrada (la fuente lo dice
       explícitamente).
     - `confirmed` — toca, no rompe la banda y hay confirmación (Order Block / imbalance).
     - `invalidated` — cierra cruzando toda la banda (la reacción no se sostuvo).
- **Código:** `fibonacci.go` → `DefaultFibConfig`, `Price`; `secretzone.go` → `NewSecretZone`,
  `EvaluateSecretZone`, `SecretZoneReaction`.
- **Prueba:** `tradeplan_test.go` → `TestFibTargetSymmetry`; `secretzone_test.go` →
  `TestSecretZoneTouchWithoutConfirmation`, `TestSecretZoneConfirmedWithConfluence`,
  `TestSecretZoneInvalidatedWhenClosedThroughBand`.
- **Estado:** 🟡 parcial — el detector de reacción y confirmación está implementado y probado, pero
  **no se auto-operan entradas de reversión** en la zona (la fuente lo llama "más arriesgado" y deja
  la confirmación sin precisar). Ver §5.Q3.

### 3.5 Riesgo y gestión

#### R18 — Stop loss máximo 1.5 % 📄 ✅
- **Fuente:** L638–639, L767 ("usa stop loss máximo hasta 1.5%").
- **Definición operativa:** distancia de stop por defecto `IBStopDefaultPct = 0.015`; cualquier
  valor se recorta al perfil.
- **Código:** `risk/ibprofile.go` → `DefaultIBRiskProfile`, `ClampStopPct`.
- **Prueba:** `risk/ibprofile_test.go` → `TestDefaultIBRiskProfileValid`,
  `TestIBRiskProfileClampStopPct`.
- **Estado:** ✅ cubierta.

#### R19 — Stop del 2 % como tope histórico 📄 🟡
- **Fuente:** L70 ("el stop loss debe ser menor a 2%"), L250–251 ("si no pasa de 2%").
- **Definición operativa:** la fuente menciona 1.5 % y 2 %; se fija 1.5 % como **default** y 2 %
  como **tope absoluto** (`IBStopMaxPct = 0.02`).
- **Código:** `risk/ibprofile.go` → `IBStopDefaultPct`, `IBStopMaxPct`, `Validate`.
- **Prueba:** `risk/ibprofile_test.go` → `TestIBRiskProfileValidateRejectsInvalid`.
- **Estado:** 🟡 parcial — interpretación de dos cifras que la fuente no concilia (§5.Q2).

#### R20 — Riesgo por operación 3–4 % y pérdida diaria 1 % ⚙️ 🟡
- **Fuente:** L765–779 ("no pierdas más de 1 % por día", "3 % al 4 % de tu capital por trade").
- **Definición operativa (ingeniería):** el "3–4 %" se interpreta como **distancia proyectada de
  recompensa**, no como capital arriesgado. El capital arriesgado por operación `IBRiskDefaultPct
  = 0.01`; la pérdida diaria realizada se limita a `IBDailyLossDefaultPct = 0.01` del balance
  inicial (`DailyLossLimit`, `DailyLossExceeded`).
- **Código:** `risk/ibprofile.go` → `RiskAmount`, `DailyLossLimit`, `DailyLossExceeded`;
  `risk/risk.go` → `Evaluate`.
- **Prueba:** `risk/ibprofile_test.go` → `TestIBRiskProfileSizingSeparatesComponents`,
  `TestIBRiskProfileDailyLossLimitBoundaries`; `risk/risk_test.go` → `TestEvaluateRejectsBadRR`,
  `TestEvaluateRespectsMaxOpenPositions`.
- **Estado:** 🟡 parcial — decisión de ingeniería documentada; la ambigüedad 3–4 % vs 1 % es
  pregunta abierta §5.Q1.

#### R21 — Apalancamiento máximo 20x (validado, **no habilitado**) 📄 🟡
- **Fuente:** L691–702, L768 ("si tu stop loss es de 1.5 % apaláncate máximo a 20x").
- **Definición operativa:** `IBMaxLeverage = 20.0` se **valida** (`WithinLeverage`), pero el
  apalancamiento real y el trading real permanecen **desactivados** (LIVE off por defecto).
- **Código:** `risk/ibprofile.go` → `IBMaxLeverage`, `WithinLeverage`; `main.go` (gate LIVE).
- **Prueba:** `risk/ibprofile_test.go` → `TestIBRiskProfileSizingSeparatesComponents`,
  `TestIBRiskProfileZeroNegativeSafety`.
- **Estado:** 🟡 parcial — límite validado pero nunca habilitado (decisión explícita).

### 3.6 Multi-temporalidad (MTF)

#### R22 — Tendencia principal 1H/2H/4H y micro 30/15m; confirmación 5m 📄 ✅
- **Fuente:** L384–386, L409–417, L591–594.
- **Definición operativa:** 1H/2H/4H fijan la dirección; 30m/15m para entradas; 5m confirmación
  opcional. Defaults sin cambios: 1H/15m/5m. Roles no soportados se **rechazan** (no se sustituyen
  en silencio).
- **Código:** `multitimeframe_config.go` → `ValidateMultiTimeframeConfig`,
  `DefaultMultiTimeframeConfig`; `multitimeframe.go` → `LearnMultiTimeframe`.
- **Prueba:** `multitimeframe_config_test.go` → `TestValidateMultiTimeframeConfigAcceptsSourceRoles`,
  `TestValidateMultiTimeframeConfigRejectsUnsupported`; `multitimeframe_test.go` →
  `TestDefaultMultiTimeframeConfig`, `TestSimulateMTFRespectsMainTrendDirection`.
- **Estado:** ✅ cubierta. **Limitación de datos:** la fuente de mercado WEEX
  (`/capi/v3/market/klines`) solo sirve `1m,5m,15m,30m,1h,4h,12h,1d,1w` — **no ofrece `2h`** — y
  además **ignora los rangos** y limita a **1000 velas** por timeframe (ver §6.1). Los
  roles `2h` siguen siendo válidos de forma abstracta, pero no son operables con esta fuente.

#### R23 — Operar solo a favor de la tendencia principal 📄 ✅
- **Fuente:** L435–448 ("si en 1H es alcista, en 15m busca compras; no operes los retrocesos").
- **Definición operativa:** la dirección de la entrada debe coincidir con la tendencia macro;
  setups contra-tendencia se rechazan.
- **Código:** `setup.go` → `ClassifyBreak`; `multitimeframe.go` → `collectMTFDecisionPoints`.
- **Prueba:** `setup_test.go` → `TestClassifyBreakRejectsCounterTrendBOS`;
  `multitimeframe_test.go` → `TestSimulateMTFRespectsMainTrendDirection`.
- **Estado:** ✅ cubierta.

### 3.7 Ejecución y cierre

#### R24 — Cerrar la mayoría en el extremo anterior y proteger el resto 📄 ⚙️ ✅
- **Fuente:** L513–517 ("cerrar la mayoría cuando toca el alto anterior y proteger").
- **Definición operativa (ingeniería):** TP parcial = `PartialCloseFraction` (default **50 %**) en
  el objetivo (extremo anterior); el resto pasa a **breakeven** y conserva el TP. Si la vela es
  ambigua (stop y objetivo a la vez) prevalece el stop.
- **Código:** `domain.go` (`Position.PartialTP/PartialQty/PartialPnL`),
  `store/sqlite.go` (`PartialClosePosition`), `paper/paper.go` (`checkPosition`),
  `investingbulls/learn.go` (`tradeSim`).
- **Prueba:** `paper_test.go` (half+breakeven, no re-aplicación, ambigüedad, short, reconcile);
  `store/sqlite_test.go` (`TestPartialClosePositionAccountingAndRoundtrip`);
  `learn_test.go` (`TestSimPartialTPClosesFractionAndProtectsRest`,
  `TestSimAmbiguousBarPrefersStopWithoutPartial`).
- **Estado:** ✅ cubierta — el **porcentaje exacto** sigue siendo decisión de ingeniería (§5.Q6).

#### R25 — Evaluar solo velas cerradas (sin mirar al futuro) ⚙️ ✅
- **Fuente:** (implícito; R03 exige cierre de vela).
- **Definición operativa:** cada decisión en la vela `i` usa solo datos `[0..i]`; las velas
  posteriores solo resuelven el resultado de una decisión ya tomada.
- **Código:** `multitimeframe.go` → `closedCandlesBefore`; `learn.go` → `collectDecisionPoints`,
  `Learn`.
- **Prueba:** `learn_test.go` → `TestDecisionPointsDoNotLookAhead`;
  `multitimeframe_test.go` → `TestFutureCandleDoesNotChangeEarlierDecisions`.
- **Estado:** ✅ cubierta.

#### R26 — Diagnosticar cada rechazo por etapa ⚙️ ✅
- **Fuente:** no aplica (requisito de ingeniería del plan, Fase 2).
- **Definición operativa:** `PipelineTrace` de solo lectura con estado `pass/fail`, `reason code` y
  valores por etapa; comando `/ibdiag` que no altera PAPER/LIVE.
- **Código:** `trace.go` → `PipelineTrace`, `EvaluateLiveWithTrace`; `handlers/commands.go`
  (`/ibdiag`).
- **Prueba:** `trace_test.go` → `TestEvaluateLiveWithTraceRecordsInsufficientData`,
  `TestEvaluateLiveWithTraceAlwaysExplainsRejection`, `TestPipelineTraceSummaryMentionsStages`.
- **Estado:** ✅ cubierta.

## 4. Matriz de trazabilidad (resumen)

| ID | Regla | Fuente (txt) | Tipo | Operacional | Código | Test | Estado |
|---|---|---|---|---|---|---|---|
| R01 | Tendencia por máx/mín | L32–40, L397–417 | 📄 | Swings + `Trend` | `structure.go` `DetectSwings`,`Analyze` | `structure_test` `TestAnalyzeTrend` | ✅ |
| R02 | CHOCH | L41–48, L526–531 | 📄 | Ruptura contra estructura | `structure.go`,`setup.go` | `TestCHOCHAfterOppositeStructure` | ✅ |
| R03 | Cierre de vela | L529–531 | 📄 | Ruptura por cierre | `structure.go`,`multitimeframe.go` | `TestDetectBreakRequiresClose` | ✅ |
| R04 | BOS/continuación | L93–109, L223–229 | 📄 | Extremo con cierre | `structure.go`,`setup.go` | `TestClassifyBreakRejectsCounterTrendBOS` | ✅ |
| R05 | Cuatro setups | L22–77 | 📄 | `SetupType` (4) | `setup.go` | `TestClassifyBreakFamilies` | ✅ |
| R06 | Fibo direccional | L50–116 | 📄 | `Fibonacci` origin→dest | `fibonacci.go` | `TestNewFibonacciBullish/Bearish` | ✅ |
| R07 | Zonas 0.45–0.50 / 0.72–0.85 | L55–56 | 📄 | `FibConfig` | `fibonacci.go` | `TestFibonacciCustomConfig` | ✅ |
| R08 | Ambas zonas, más confluencias | L158–160, L493–506 | 📄 | Selección determinista | `zoneselect.go` | `TestSelectSetupFromEvidencePrefersHigherScore` | ✅ |
| R09 | Order Block | L27–146 | 📄 | Detección/estado | `orderblock.go` | `TestUpdateBearishOrderBlockInvalidation` | ✅ |
| R10 | OB testeado se descarta | L122–128 | 📄 | `ExcludeTestedOrderBlock` | `orderblock.go`,`confluence.go` | `TestConfluenceExcludeTestedOrderBlock` | 🟡 |
| R11 | Imbalance no rellenado | L64–67, L136–137 | 📄 | Estado del gap | `imbalance.go` | `TestUpdateBullishImbalanceFill` | ✅ |
| R12 | Oferta/demanda (4º comp.) | L13, L217, L472–475 | 📄 | Zona independiente | `supplydemand.go` | `TestUpdateDemandZoneTestedAndInvalidated` | ✅ |
| R13 | Confluencia 2+ | L7–13, L139–141 | 📄 | Modos 3/4, 4/4, parejas | `confluence.go` | `TestFaithfulConfluenceModes` | ✅ |
| R14 | Entrada en zona/OB | L57–59, L143–146 | 📄 | Orden límite en zona | `tradeplan.go` | `TestBuildTradePlanAtUsesExplicitEntry` | ✅ |
| R15 | Stop tras el OB, antes de otra zona | L68–70, L205–206 | 📄 | `structuralStop` | `tradeplan.go`,`risk` | `TestTradePlanDropsSetupWhenStructuralStopTooWide` | ✅ |
| R16 | TP en extremo anterior | L70–72, L250–251 | 📄 | `previousSwingTarget` | `tradeplan.go` | `TestBuildLongTradePlan` | ✅ |
| R17 | Niveles 1.34/1.53 | L255–380 | 📄 | `Target1/2` (objetivo) + `SecretZone` (reacción) | `fibonacci.go`,`secretzone.go` | `TestFibTargetSymmetry`; `TestSecretZone*` | 🟡 |
| R18 | SL máx 1.5 % | L638–639, L767 | 📄 | `IBStopDefaultPct` | `risk/ibprofile.go` | `TestIBRiskProfileClampStopPct` | ✅ |
| R19 | SL tope 2 % | L70, L250–251 | 📄 | `IBStopMaxPct` | `risk/ibprofile.go` | `TestIBRiskProfileValidateRejectsInvalid` | 🟡 |
| R20 | 3–4 %/trade; 1 %/día | L765–779 | ⚙️ | `RiskPct`,`DailyLossLimit` | `risk/ibprofile.go`,`risk/risk.go` | `TestIBRiskProfileDailyLossLimitBoundaries` | 🟡 |
| R21 | Apalancamiento 20x (no habilitado) | L691–768 | 📄 | `IBMaxLeverage` | `risk/ibprofile.go`,`main.go` | `TestIBRiskProfileSizingSeparatesComponents` | 🟡 |
| R22 | MTF 1H/2H/4H + 30/15m + 5m | L384–417, L591–594 | 📄 | Roles validados | `multitimeframe_config.go` | `TestValidateMultiTimeframeConfigAcceptsSourceRoles` | ✅ |
| R23 | Solo a favor de la tendencia | L435–448 | 📄 | Filtro de dirección | `setup.go`,`multitimeframe.go` | `TestSimulateMTFRespectsMainTrendDirection` | ✅ |
| R24 | Cerrar mayoría + proteger | L513–517 | 📄⚙️ | TP parcial 50 % + breakeven | `paper.go`,`store`,`learn.go` | `TestPartialTPClosesHalfAndProtectsRest` | ✅ |
| R25 | Velas cerradas, sin look-ahead | (R03) | ⚙️ | `[0..i]` | `multitimeframe.go`,`learn.go` | `TestDecisionPointsDoNotLookAhead` | ✅ |
| R26 | Diagnóstico por etapa | n/a | ⚙️ | `PipelineTrace`/`/ibdiag` | `trace.go`,`handlers` | `TestPipelineTraceSummaryMentionsStages` | ✅ |

Material del PDF fuera del alcance del motor (psicología, no reglas operativas): "Los 10
mandamientos" (L605–761), ejemplos de configuración de gráficos, anécdotas. Estado ➖ n/a.

## 5. Preguntas abiertas (no inventar regla)

Las siguientes son **decisiones bloqueantes** del plan (§14) que la fuente **no resuelve** de forma
inequívoca. Se dejan explícitas; la implementación usa la interpretación indicada y marcada como
decisión de ingeniería.

- **Q1 — Riesgo 3–4 % vs pérdida diaria 1 %.** La fuente mezcla "3–4 % por trade" con "no perder
  más de 1 % al día". Implementado: 3–4 % = recompensa proyectada; riesgo de capital = 1 %.
  Confirmar con el autor. (R20)
- **Q2 — SL 1.5 % vs 2 %.** La fuente da ambas cifras. Implementado: 1.5 % default, 2 % tope.
  (R19)
- **Q3 — Papel exacto de 1.34/1.53.** La fuente lo describe como objetivo y como zona de
  reacción/reversión. Implementado: objetivo (`Target1/Target2`) **y** detector de reacción
  (`SecretZone`: touch/confirmed/invalidated, `secretzone.go`). **No** se auto-operan entradas de
  reversión: la fuente exige confirmaciones (OB/imbalance) sin precisar el disparo y lo llama "más
  arriesgado". Decidir si la reacción debe generar órdenes propias.
- **Q4 — Rol/obligatoriedad de 2H/4H/30m.** La fuente menciona 1H/2H/4H como principal y 30/15m
  como micro. Implementado: roles aceptados sin cambiar defaults. ¿Son obligatorios en algún caso?
  (R22)
- **Q5 — Definición exacta de "Order Block testeado".** El PDF describe el concepto ("una vela roja
  ya tomó liquidez") pero no fija umbral. `ExcludeTestedOrderBlock` es configurable (default
  `false`). (R10)
- **Q6 — Porcentaje y condiciones del TP parcial.** La fuente dice "cerrar la mayoría"; el 50 % es
  decisión de ingeniería. (R24)
- **Q7 — Cálculo del win rate con cierres parciales.** Resuelto: la posición se cuenta **una vez**
  usando el `NetPnL` total (parcial + resto). (R24)
- **Q8 — Recuperación si faltan campos críticos de riesgo.** Falla de forma segura: sin base de
  balance el límite diario bloquea nuevas entradas. (R20)

## 6. Decisiones de ingeniería (resumen)

1. Umbrales de aprendizaje, selección de setups y criterios OOS son del sistema, no del PDF.
2. Interpretación de 3–4 % como recompensa (no riesgo de capital) — Q1.
3. SL 1.5 % default / 2 % tope — Q2.
4. TP parcial 50 % + breakeven — Q6.
5. Cardinalidad de confluencia: la fuente lista 4 componentes rotulados "3"; se modelan los 4.
6. Live/Lev real desactivados por defecto (`INVESTING_BULLS_STRATEGY_ID` vacío).
7. Optimizador no puede desactivar reglas obligatorias ni ampliar límites de perfil sin control.
8. "Invalidación" de la zona secreta = cierre que cruza toda la banda; prioridad sobre el toque
   (R17).

### 6.1 Limitaciones de la fuente de datos (WEEX)

Verificadas contra la API real (`api-contract.weex.com`, Fase 7):

1. **Timeframes servidos:** `1m,5m,15m,30m,1h,4h,12h,1d,1w`. **`2h` no existe** (HTTP 400).
2. **Rangos ignorados:** `/capi/v3/market/klines` ignora `startTime`/`endTime` (probado en ms y s, y
   con `start`/`end`/`from`/`to`); `historyKlines` (param `size`) también ignora rangos.
3. **Tope de 1000 velas:** el endpoint devuelve como máximo las **1000 velas más recientes** por
   llamada. `WeexProvider.GetKlines` hace una única llamada acotada a `RestMaxBars` (1000) — no pagina.
   El backfill entrega ~1000 velas/timeframe; `WeexBackfillBars` se acota a 1000 (default corregido).
4. **Consecuencia:** con `RequireConfirm` (5m), 1000 velas ≈ 3.5 días de historia común, insuficiente
   para el walk-forward MTF. La historia profunda solo se acumula por streaming WS.

## 7. Mantenimiento

- Al cambiar la semántica de una regla o modelo, incrementar
  `investingbulls.CodeVersion` y actualizar esta matriz (plan §9/§11).
- Toda regla nueva debe añadir fila aquí **con** su test; sin test, no se marca ✅.
- Este documento es la referencia de la Puerta B (fidelidad) del plan.
