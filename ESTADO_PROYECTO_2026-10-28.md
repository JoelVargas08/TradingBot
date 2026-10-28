# Estado actual del proyecto — 2026-10-28

- **Repositorio:** `JoelVargas08/TradingBot`
- **Rama:** `fix/telegram-controller` (aún no fusionada en `main`)
- **Último commit:** `5ef7e37` — `fix(ib): diagnostico de arranque sin fallback silencioso y linea base NN`
- **Módulo Go:** `tradingview-bot` (Go 1.26.2)
- **Modo:** PAPER. Trading real y apalancamiento real desactivados por defecto.
- **Validación de esta fecha:** `go build ./...` OK, `go vet ./...` OK, `go test ./...` OK.

---

## 1. ¿Se solucionó la identificación del ID de Investing Bulls?

**Respuesta corta:** sí para el entorno local actual; **no está confirmado todavía para GitHub Actions**.

### Lo que sí está resuelto (local)

| Evidencia | Estado |
| --- | --- |
| `INVESTING_BULLS_STRATEGY_ID` en `.env` | `investing-bulls-mtf-BTCUSDT-1793190305` |
| Estrategia en `data/bot.db` | existe, `status=active`, `source=investing_bulls_learn_mtf` |
| Fecha de creación (UTC) | 2026-10-28 12:25:05 (08:25 America/Havana) |
| Gate de `main.go` | exige ID no vacío + estrategia existente + `status=active` |
| Arranque de IB | se activa sin fallback silencioso a Chandelier |
| Trazabilidad | `/ibdiag` usa el pipeline real (`PipelineTrace`) |

La lectura completa del registro activo es:

```text
id=investing-bulls-mtf-BTCUSDT-1793190305
status=active
error_msg="PAPER en observación: 100 operaciones cerradas requeridas; OOS histórico no superado (0/4 folds positivos)"
```

### Lo que falta confirmar (CI/PAPER en GitHub Actions)

El workflow `.github/workflows/paper-session.yml` resuelve el ID así:

```yaml
INVESTING_BULLS_STRATEGY_ID: ${{ inputs.strategy_id || vars.INVESTING_BULLS_STRATEGY_ID }}
```

- El controlador de Telegram despacha con `strategy_id=""`, por lo que en la práctica manda la **variable de repositorio** `vars.INVESTING_BULLS_STRATEGY_ID`.
- Esa variable **no se pudo verificar desde este entorno**: no hay `gh` CLI ni credenciales de GitHub disponibles.
- Si la variable está vacía o apunta a un ID distinto, la sesión PAPER arrancará con Investing Bulls deshabilitado aunque la DB tenga una estrategia `active`.

### Fragilidad estructural del mecanismo

El ID contiene un timestamp (`...-1793190305`) y **cambia en cada aprendizaje**. Por tanto:

1. Un ID fijado en `.env` o en la variable del repo queda obsoleto tras el siguiente `learn`/promoción.
2. La solución actual sirve para este ID concreto, no es una identificación estable en el tiempo.
3. Cada cambio de ID exige actualizar `.env` (local) y la variable del repositorio (CI).

---

## 2. Estado de datos y ejecución (local)

| Tabla | Filas |
| --- | --- |
| `candles` | 4.486 |
| `strategies` | 3 |
| `trades` | 0 |
| `positions` | 0 |
| `signals` | 0 |
| `orders` | 0 |
| `model_versions` | 0 |

- Hay **3 estrategias**: 1 `active` (creada hoy) y 2 `rejected` (2026-10-06, OOS 0/4).
- **No hay evidencia de operaciones PAPER** en esta base local: aunque IB figure activa, no hay trades/posiciones/señales registradas.
- La observación PAPER requerida es de **100 operaciones cerradas**; está pendiente.

---

## 3. Estrategia Investing Bulls

### 3.1 Implementación

| Área | Estado |
| --- | --- |
| Especificación canónica R01–R26 | `docs/INVESTING_BULLS_STRATEGY.md` |
| Estructura, CHOCH/BOS, 4 setups | implementado y probado |
| Fibonacci 0.45–0.50 / 0.72–0.85, 1.34/1.53 | implementado y probado |
| Order Block, Imbalance, Supply & Demand | implementados como componentes independientes |
| Confluencia 3/4 y 4/4 | implementada |
| MTF 1H → 15m → 5m (roles 2H/4H/30m validados) | implementado |
| Plan de trade (entrada, SL, TP) | implementado |
| Perfil de riesgo IB | `IBStopDefaultPct=1.5%`, `IBStopMaxPct=2%`, `IBMaxLeverage=20`, riesgo diario 1% |
| TP parcial 50% + breakeven | implementado y con paridad research ↔ PAPER |
| Trazabilidad por etapas | `PipelineTrace` + `/ibdiag` |

### 3.2 Validación

- **Puerta A (código):** build, vet y tests en verde.
- **Puerta E (walk-forward real):** candidato in-sample rechazado (27 trades, WR 3.7%, PF 0.044, ret −6.78%); OOS 4 folds con **0/4 folds positivos**.
- La estrategia se activa para **observación PAPER**, no para LIVE.
- **WEEX:** no sirve `2h`; el feed en vivo usa 1h/15m/5m. El histórico profundo se pagina con `/historyKlines`.

### 3.3 Correcciones aplicadas hoy

Durante la revisión se detectaron regresiones en el working tree y se revirtieron antes del commit:

- `EvaluateLive` había quedado como stub que **nunca emitía señal**; se restauró el pipeline MTF real y `trace.go`/`trace_test.go`.
- `supplydemand.go` reescrito rompía `TestDetectSupplyZone`; se restauró la versión canónica.
- `docs/INVESTING_BULLS_STRATEGY.md` había perdido la matriz R01–R26; se restauró.
- `fibzoneselect.go` y `secrets_reaction.go` duplicaban lógica existente y no se usaban; se eliminaron para evitar rutas paralelas.

---

## 4. Red neuronal (ML)

### 4.1 Estado

- MLP NumPy propio + sidecar (`ml/api.py`), sin TensorFlow/PyTorch.
- Entrenamiento y promoción **manuales**; PAPER forzado; holdout reservado.
- Datos históricos profundos disponibles (BTC/SOL/BNB/XRP/ADA ~25k–30k velas 1h).
- **No hay modelo neuronal aprobado** en `ml/models/neural_model.json`; solo hay candidatos sin promocionar.
- La sesión PAPER restaura un artefacto `ml-approved` si existe; localmente no está aprobado.

### 4.2 Resultados relevantes

| Fase | Resultado |
| --- | --- |
| 9–10 | MLP experimental; no supera baseline; no se promociona |
| 11 | ETH 30k: +15.32% walk-forward, pero validación externa BTC −7.39% → `external_gate=false` |
| 12 | 5 semillas: solo 1/5 pasó validación externa; MACD supera a la NN |
| 13 | Ensemble 3/5 positivo en varios activos, pero holdout ETH reservado −7.4% → `gate=false`; DD 53–77% en SOL/XRP/ADA |
| Línea base 2026-10-28 | Informe sintético en `ml/INFORME_LINEA_BASE.md` + `ml/linea_base_sintetica.json` |

**Conclusión ML:** no hay modelo promocionable. El holdout ETH negativo es decisivo y los drawdowns son inaceptables.

---

## 5. Bloqueos y riesgos abiertos

1. **ID volátil:** el ID cambia con cada aprendizaje; `.env` y variable del repo se desincronizan.
2. **Variable de CI no verificada:** `vars.INVESTING_BULLS_STRATEGY_ID` podría estar vacía o desactualizada.
3. **OOS 0/4:** la estrategia activa no superó el histórico; solo está en observación.
4. **0 operaciones PAPER:** faltan las 100 operaciones cerradas para validar.
5. **Sin modelo NN aprobado:** inferencia neuronal no operativa por defecto.
6. **Rama sin fusionar:** `fix/telegram-controller` no está en `main`.
7. **WEEX sin `2h`:** configurar 2H como principal no es operable con esta fuente.
8. **CRLF/gofmt:** el repo usa `core.autocrlf=true`; `gofmt -l` marca archivos preexistentes. No reformatear en masa.

---

## 6. Próximos pasos recomendados

1. **Confirmar en GitHub** la variable `INVESTING_BULLS_STRATEGY_ID` (o pasarla explícitamente al despachar la sesión).
2. **Diseñar identificación estable:** seleccionar automáticamente la estrategia `active` más reciente cuando el ID no esté fijado, o un alias estable por símbolo/timeframe.
3. **Acumular 100 operaciones PAPER** y revisar el reporte de win-rate.
4. **Revisar el criterio OOS** antes de cualquier paso a LIVE (hoy 0/4).
5. **No promocionar NN** hasta superar baseline y holdout con DD aceptable.
6. **Fusionar/ordenar la rama** y limpiar la deuda de formato CRLF.

---

## 7. Verificación reproducible

```powershell
go build ./...
go vet ./...
go test ./...
go run ./cmd/ibvalidate -db <db-temporal> -symbol BTCUSDT -timeframes 1h,15m,5m -backfill
```

Consulta del ID activo (solo lectura):

```sql
SELECT id, status, source, error_msg, created_at, updated_at
FROM strategies
WHERE status = 'active'
ORDER BY updated_at DESC;
```

---

## 8. Referencias

- `docs/INVESTING_BULLS_STRATEGY.md` — especificación canónica R01–R26.
- `docs/DIAGNOSTICO_ID_INVESTING_BULLS.md` — diagnóstico del ID.
- `PLAN_DIAGNOSTICO_INVESTING_BULLS_MTF.md` — plan de verificación del arranque.
- `MEMORIA_SESION_2026-10-28.md` — memoria de la sesión.
- `PROGRESO.md` — panorama consolidado de fases.
- `PLAN_EJECUCION_RED_NEURONAL.md` — plan de la red neuronal.
- `.github/workflows/paper-session.yml` — resolución del ID en CI.
