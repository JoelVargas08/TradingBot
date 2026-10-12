# PROGRESO DEL PROYECTO - TradingBot (`tradingview-bot`)

> Documento consolidado del avance del proyecto. Reúne, en un solo lugar, el estado de
> todas las fases (plan original + integración Telegram/PAPER + asimilación de la
> estrategia *Investing Bulls*). El detalle de cada fase vive en los `MEMORIA_*.md`
> enlazados al final.

- **Fecha:** 2026-10-12
- **Módulo Go:** `tradingview-bot` (Go 1.26.2)
- **Repositorio:** https://github.com/JoelVargas08/TradingBot
- **Rama de trabajo:** `main`
- **Verificación (última):** `go build ./...`, `go vet ./...` limpios y
  `go test ./... -count=1` → **31 paquetes OK**, 0 fallos.
- **Trading real:** **deshabilitado** por defecto (LIVE off); todo corre en PAPER.

---

## 1. Visión general

Bot de trading algorítmico en Go con:

- Ingesta multi-fuente (Binance/WEEX) por **WebSocket** y **REST (backfill)**.
- Detección de movimientos y **4 componentes de confluencia** (Fibonacci, Order Block,
  Imbalance/desbalance, Supply & Demand).
- Aprendizaje de estrategias **desde PDF** y desde **datos históricos** (learner + walk-forward).
- Motor de señales con **IA (baseline XGBoost, sidecar Python)**.
- Ejecución **PAPER** (simulada) con contabilidad, TP parcial, recuperación de órdenes y
  reconciliación; puente a **Telegram** para control y notificaciones.
- Estrategia insignia: **Investing Bulls** (asimilación fiel desde el material fuente).

---

## 2. Arquitectura (paquetes)

| Área | Paquete / carpeta | Rol |
| --- | --- | --- |
| Servidor y wiring | `main.go`, `handlers/`, `services/` | HTTP, webhook, comandos Telegram |
| Dominio | `internal/domain` | Tipos: velas, posiciones, órdenes, cuenta, planes |
| Persistencia | `internal/store` | SQLite + migraciones idempotentes |
| Bus / eventos | `internal/bus` | Desacoplamiento de señales |
| Ingesta | `internal/ingest` | WEEX/Binance: WS, REST, backfill, validación |
| Detección | `internal/detect`, `internal/evaluator` | Movimientos, features |
| Estrategias | `internal/investingbulls`, `internal/strategies` | IB (learner/LIVE/MTF), Chandelier |
| Riesgo | `internal/risk` | Riesgo genérico + `IBRiskProfile` |
| PAPER | `internal/paper` | Motor simulado (TP parcial, reconcile) |
| Ejecución | `internal/execution` | Órdenes, recuperación, reconciliación |
| IA / ML | `internal/ml`, `ml/` | Cliente Go + sidecar Python (XGBoost) |
| Aprendizaje PDF | `internal/pdf`, `internal/jobqueue`, `internal/llm`, `internal/strategymanager` | Extracción → cola → LLM → estrategia |
| Backtesting | `internal/backtest`, `internal/benchmark` | Comparativas buy&hold / Chandelier / IA |
| Observabilidad | `internal/health`, `internal/observability`, `internal/notifier` | Salud, métricas, avisos |
| Validación offline | `cmd/ibvalidate` | Harness de validación end-to-end de IB |
| Despliegue | `.github/`, `systemd/`, `scripts/`, `docs/` | CI/CD, servicio, documentación |

---

## 3. Plan original (Fases 0-8) - COMPLETADO

| Fase | Tema | Estado |
| --- | --- | --- |
| **0** | Endurecer el bot (config *fail-fast*, webhook, storage, servidor HTTP) | ✅ |
| **1** | Desacoplar señales + multi-estrategia (bus, dominio) | ✅ |
| **2** | Ingesta de datos y detección de movimientos | ✅ |
| **3** | Estrategia *Chandelier Multi-Confirm* + gestión de riesgo | ✅ |
| **4** | Aprendizaje de estrategias desde PDF (pdf/llm/jobqueue/strategymanager) | ✅ |
| **5** | Motor de señales con IA (baseline XGBoost, sidecar Python) | ✅ |
| **6** | Backtesting + KPIs + despliegue | ✅ |
| **7** | Bot de Telegram listo para PDF + reparación de errores del pipeline | ✅ |
| **8** | Ampliar el aprendizaje *Investing Bulls* sin rebajar sus reglas (learner MTF) | ✅ |

Detalle por fase en `MEMORIA_FASE_0.md` … `MEMORIA_FASE_8.md`.

---

## 4. Integración Telegram / PAPER y control de sesiones - COMPLETADO

Trabajo posterior sobre el flujo de operación y control (documentado en los
`TradingBot_Fase_*`, `TRADINGBOT_*.md`):

- Control de sesiones PAPER a nivel de **workflow de GitHub Actions** y desde Telegram.
- Selección manual de la estrategia IB para sesiones PAPER.
- Reset manual y validación de sesiones PAPER "clean".
- Validación de candidatos IB sobre **100 operaciones PAPER**.
- Reporte de win-rate final por Telegram.
- Persistencia de entradas PAPER *staged* entre reinicios del runner.
- Diagnósticos LIVE de Investing Bulls (`/ibdiag`, `PipelineTrace`).
- Seguridad: se exige el **chat ID de admin** de Telegram para el control de workflows.

---

## 5. Asimilación de la estrategia *Investing Bulls* (p34)

El trabajo sigue `PLAN_IMPLEMENTACION_INVESTING_BULLS.md` y su plan origen
`PLAN_ASIMILAR_ESTRATEGIA_INVESTING_BULLS.md`, leyendo la fuente
`docs/source/estrategia_investing_bulls_p34.txt`.

| Fase IB | Tema | Estado |
| --- | --- | --- |
| **0/1** | Especificación canónica + matriz de trazabilidad (`docs/INVESTING_BULLS_STRATEGY.md`) | ✅ |
| **2** | Detección e integración (4 componentes de confluencia) | ✅ |
| **3.5** | Detector de reacción en niveles 1.34/1.53 (`secretzone.go`) | ✅ |
| **3.6** | Roles MTF 2H/4H/30m (validación, defaults sin cambiar) | ✅ |
| **4/3** | Perfil de riesgo `IBRiskProfile` | ✅ |
| **6** | Migración/versionado + SearchSpace persistido + diagnóstico | ✅ |
| **5** | TP parcial 50 % + paridad `tradeSim` research↔PAPER | ✅ |
| **7** | Validación end-to-end con datos reales (WEEX) + corrección de backfill | ✅ (PAPER 100 ops pendiente) |

### 5.1 Especificación canónica (Fase 0/1)
`docs/INVESTING_BULLS_STRATEGY.md`: **26 reglas (R01-R26)** con ID, fuente, definición
operativa, tipo (`texto fuente` 📄 vs `decisión de ingeniería` ⚙️), archivo/función,
prueba y estado; **8 preguntas abiertas (Q1-Q8)** y mapa del pipeline (13 etapas de
`PipelineTrace`).

### 5.2 Detección (Fase 2)
Supply&Demand como **cuarto componente independiente** (no alias de Order Block),
integrado igual en learner, walk-forward y LIVE (`confluence.go`, `zoneselect.go`,
`learn.go`, `multitimeframe.go`, `trace.go`). Modos fieles `all4`, `three_of_four`,
`fibonacci_supply_demand`; `DefaultSearchSpace` → **72 configuraciones**.

### 5.3 Detector 1.34/1.53 (Fase 3.5)
`secretzone.go`: `SecretZone` + `EvaluateSecretZone` (`none`/`touch`/`confirmed`/
`invalidated`). Toque sin confirmación no habilita nada; con confirmación (OB/
imbalance) es reacción válida. **No** se auto-operan entradas de reversión (la fuente
las llama "más arriesgadas").

### 5.4 Roles MTF (Fase 3.6)
`multitimeframe_config.go`: `ValidateMultiTimeframeConfig` acepta 2H/4H (principal) y
30m (entrada) **sin cambiar** los defaults `1h/15m/5m`; rechaza explícitamente roles
fuera de rango y temporalidades repetidas.

### 5.5 Perfil de riesgo (Fase 4/3)
`internal/risk/ibprofile.go`: `IBStopDefaultPct = 0.015`, `IBStopMaxPct = 0.02`,
`IBMaxLeverage = 20`, `IBRiskDefaultPct = 0.01`, `IBDailyLossDefaultPct = 0.01`.
Separación de tamaño/exposición/distancia-stop/apalancamiento; límite diario sobre
balance inicial con **fallo seguro**.

### 5.6 Migración, versionado y diagnóstico (Fase 6)
`migrate.go`: carga migrada en LIVE/OOS/MTF/`main.go`, rechazo explícito de versiones
incompatibles (línea `investing-bulls/1.`). `CodeVersion = "investing-bulls/1.4.0"`.
`SearchSpace` persistido; `SetupStats` (por setup) y `ComponentStats` (por componentes,
con `ConfluentCount`).

### 5.7 TP parcial 50 % (Fase 5)
Cierre del **50 %** en el objetivo parcial (= máximo/mínimo previo) protegiendo el resto
a **breakeven** (Choice A: el objetivo parcial **es** el `TakeProfit` actual). Stop tiene
prioridad; vela ambigua → cierre total al stop (`AmbiguousBar`). Idempotente
(`PartialQty > 0`). Contabilidad: la posición se cuenta **una vez** (win rate) con
`NetPnL = neto resto + PartialPnL`; la cuenta no duplica. Paridad research↔PAPER
mediante un único `tradeSim` compartido.

---

## 6. Fase 7 - Validación end-to-end con datos reales (WEEX)

**Harness nuevo:** `cmd/ibvalidate` (equivalente no-Telegram de `/learnibmtf` +
`/validateibmtf`): backfill WEEX → diagnóstico de datos → `LearnMultiTimeframeAndPersist`
→ `ValidateMultiTimeframeCandidate` → reporte JSON. No envía órdenes.

- **Puerta A (código):** build/vet/test en verde.
- **Puerta B (fidelidad):** golden/parity tests del pipeline en verde.
- **Puerta C (datos):** calidad BTCUSDT 1h/15m/5m correcta (monotónico, sin duplicados/
  huecos, alineado, no stale). **Hallazgos WEEX** (ver §6.1).
- **Puerta D (riesgo/PAPER):** cubierto por tests; ninguna orden real.
- **Puerta E (walk-forward):** sobre datos reales, candidato in-sample **rechazado**
  (27 trades, WR 3.7 %, PF 0.044, ret −6.78 %); OOS 4 folds: 15 trades, WR 0 %, PF 0,
  ret −4.55 %, **0/4 folds positivos**. Por diseño el OOS es diagnóstico y el candidato
  se activa para observación PAPER; LIVE sigue off.
- **Observación PAPER de 100 operaciones:** pendiente (requiere runtime en vivo).

### 6.1 Limitaciones de la fuente WEEX y corrección aplicada

Verificado contra `api-contract.weex.com`:

1. **Timeframes servidos:** `1m,5m,15m,30m,1h,4h,12h,1d,1w`. **No hay `2h`** (HTTP 400).
2. **Rangos ignorados:** `/capi/v3/market/klines` ignora `startTime`/`endTime`.
3. **Tope de 1000 velas** por llamada (las más recientes).

**Corrección aplicada:**
- `WeexBackfillBars` por defecto `5000` → **`1000`** (`config/config.go`).
- `WeexProvider.Backfill` acota `limit` a `RestMaxBars` con *warning* y avisa si
  `seen < limit`.
- `WeexProvider.GetKlines` ya **no pagina**: una sola llamada acotada a `RestMaxBars`
  (refleja que la API ignora rangos).
- Tests actualizados: `TestWeexConfigWithDefaults`,
  `TestWeexGetKlinesSingleRequestCappedAtRestMaxBars`,
  `TestWeexGetKlinesDeduplicatesWithinResponse`.

---

## 7. Pendientes (backlog)

1. **Observación PAPER de 100 operaciones** de IB (requiere runtime en vivo).
2. **Defecto WEEX (2):** rechazar `2h` en la configuración MTF (la fuente no lo sirve).
3. **Q3:** decidir si la reacción en 1.34/1.53 debe generar órdenes propias.
4. **Q5:** definición exacta de *Order Block testeado* (`ExcludeTestedOrderBlock`,
   hoy configurable con default `false`).
5. **Fase 6 no bloqueante:** diagnóstico por zona Fibonacci individual (zona 1 vs 2) y
   perfil de riesgo como dimensión del `SearchSpace`.
6. **Historia profunda:** solo se acumula por streaming WS (la API REST no permite
   backfill largo en WEEX).

---

## 8. Cómo verificar

```powershell
go build ./...
go vet ./...
go test ./... -count=1        # 31 paquetes OK
gofmt -l <archivos nuevos>    # limpio (varios .go pre-existentes son CRLF: no reformatear)
```

Validación IB end-to-end sin Telegram:

```powershell
go run ./cmd/ibvalidate -db <db-temporal> -symbol BTCUSDT -timeframes 1h,15m,5m -backfill
```

---

## 9. Índice de documentación

- **Este archivo:** `PROGRESO.md` (panorama consolidado).
- **Especificación IB:** `docs/INVESTING_BULLS_STRATEGY.md` (R01-R26, §6.1 WEEX).
- **Flujo/uso IB:** `docs/INVESTING_BULLS.md`.
- **Memoria IB (detallada):** `MEMORIA_FASE_IB_ASIMILACION.md`.
- **Planes IB:** `PLAN_IMPLEMENTACION_INVESTING_BULLS.md`,
  `PLAN_ASIMILAR_ESTRATEGIA_INVESTING_BULLS.md`.
- **Fuente IB:** `docs/source/estrategia_investing_bulls_p34.txt`.
- **Plan original y memorias:** `PLAN_DE_ACCION.md`, `MEMORIA_FASE_0.md` … `MEMORIA_FASE_8.md`.
- **Cambios Telegram/PAPER:** `TradingBot_Fase_A_Cambios.md`, `TradingBot_Fase_B_C_Plan_Exacto.md`,
  `TradingBot_Fase_D_E_F_Cambios.md`, `TradingBot_Fase_A1_Telegram_Cambios.md`,
  `TRADINGBOT_CAMBIOS_LEARN_MTF.md`, `TRADINGBOT_CORRECCION_HEALTH_TIMESTAMPS.md`,
  `TRADINGBOT_PLAN_COMPLETO_CAMBIOS.md`.
- **Despliegue:** `DEPLOYMENT.md`, `TRADINGVIEW_WEBHOOK_SETUP.md`.
- **Bitácoras de sesión:** `Bitacora_Sesion_*.md`.
