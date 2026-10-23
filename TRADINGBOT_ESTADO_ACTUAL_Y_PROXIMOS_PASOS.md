# TRADINGBOT — ESTADO ACTUAL Y PRÓXIMOS PASOS

**Repositorio:** `JoelVargas08/TradingBot`  
**Fecha:** 2026-10-07  
**Objetivo:** validar y operar la estrategia Investing Bulls con datos de WEEX, primero en paper trading y solo después pasar a dinero real.

## 1. Estado general

El proyecto ya cuenta con:

- WEEX como fuente de mercado.
- Backfill 1H, 15m y 5m.
- WebSocket y recuperación de huecos.
- Health check corregido.
- SQLite para persistencia.
- Paper broker.
- Recuperación y reconciliación tras reinicio.
- Gestión de riesgo y kill switch.
- Sesión temporal.
- Learner determinista de Investing Bulls.
- Aprendizaje MTF exacto.
- Persistencia de estrategias/modelos.
- Walk-forward OOS real.
- Bloqueo de estrategias que no estén `ACTIVE`.
- Diagnóstico individual por fold.
- GitHub Actions para sesiones PAPER.
- Scripts/documentación de despliegue.
- Fallback de Telegram cuando falla el HTML.

**Situación actual:** el sistema está en fase de **validación de la estrategia**, no en fase de operar dinero real.

---

## 2. Estrategia Investing Bulls implementada

Fuente principal:

`ESTRATEGIA THE INVESTING BULLS.pdf` (94 páginas).

Elementos implementados:

- estructura de mercado;
- BOS/CHOCH confirmados por cierre;
- Fibonacci 0, 0.45, 0.50, 0.72, 0.85, 1;
- zonas 0.45–0.50 y 0.72–0.85/0.86;
- extensiones 1.34 y 1.53;
- imbalance;
- order blocks;
- supply/demand;
- liquidez/manipulación;
- confluencia Fibonacci + imbalance + OB;
- 1H para dirección;
- 15m para entrada;
- 5m como confirmación opcional;
- stop máximo del 2%;
- objetivos basados en estructura/Fibonacci.

Las definiciones computacionales exactas, grids y criterios estadísticos son decisiones de implementación; no deben confundirse con reglas literalmente expresadas en el PDF.

---

## 3. Arquitectura `internal/investingbulls`

Componentes principales:

- `structure.go`: tendencia, swings, BOS/CHOCH.
- `fibonacci.go`: niveles y zonas Fibonacci.
- `imbalance.go`: detección/fill de imbalance.
- `orderblock.go`: order blocks e invalidación.
- `confluence.go`: confluencia y evidencia.
- `tradeplan.go`: entrada, stop, RR y límites.
- `setup.go`: `choch_long`, `choch_short`, `continuation_long`, `continuation_short`.
- `learn.go`: aprendizaje determinista.
- `persist.go`: persistencia.
- `walkforward.go`: walk-forward.
- `validate.go`: promoción/rechazo.
- `multitimeframe.go`: aprendizaje MTF y validación OOS.
- `live.go`: evaluación live.

El learner no es una red neuronal; es optimización determinista de parámetros sobre las reglas implementadas.

---

## 4. Corrección crítica del aprendizaje MTF

Inicialmente el aprendizaje encontraba candidatos, pero la simulación MTF real podía producir cero operaciones porque el learner no aplicaba desde el principio todos los filtros del ejecutor.

Se corrigió para que `LearnMultiTimeframe` utilice exactamente el mismo pipeline que la ejecución:

1. tendencia 1H;
2. setup 15m;
3. alineación de dirección;
4. Fibonacci;
5. imbalance;
6. order block;
7. confluencia;
8. confirmación 5m si está habilitada;
9. trade plan;
10. simulación.

Esto evita que el learner optimice señales que luego el motor live descarta.

---

## 5. Grid de aprendizaje

La búsqueda actual contempla hasta:

- 4 modos de confluencia;
- 4 distancias;
- 3 niveles de stop.

Total:

**48 configuraciones.**

Incluye confluencia 3/3 y variantes 2/3.

`MinTrades` de producción permanece en **8**. No se redujo para fabricar candidatos. Los tests sintéticos pueden utilizar valores menores únicamente para probar la arquitectura.

---

## 6. Timing y look-ahead

La entrada del learner MTF quedó alineada con `EvaluateLive`:

- entrada basada en `entry[i].Close`;
- `entryBar = i`;
- confirmaciones 5m solo con velas cerradas;
- sin look-ahead.

Esto es importante para que los resultados del aprendizaje representen la misma lógica que se ejecutará posteriormente.

---

## 7. WEEX

Sí funciona como fuente principal.

Configuración usada:

- `BTCUSDT`
- 1H
- 15m
- 5m
- backfill de 5000 velas por timeframe.

Se observaron logs exitosos de:

```text
BACKFILL_COMPLETED ... 1h bars=5000
BACKFILL_COMPLETED ... 15m bars=5000
BACKFILL_COMPLETED ... 5m bars=5000
```

Existe WebSocket con reconexión y backfill de huecos.

Los cierres normales:

```text
websocket: close 1000 (normal): session is inactive
```

han sido recuperados mediante backfill. Esto no debe confundirse automáticamente con pérdida permanente de datos.

---

## 8. Health check

Se corrigió un problema de timestamps no monótonos causado por el orden incorrecto de `LastCandle` y `Recent`.

El health llegó a mostrar:

```json
"market_health": {
  "healthy": true,
  "reasons": null,
  "stale": false
}
```

---

## 9. Paper trading y persistencia

Paper:

- balance inicial por defecto: `10000`;
- máximo observado: 3 posiciones;
- RR: 2:1;
- kill switch: 15%;
- sin broker real.

Las posiciones se guardan en:

`data/bot.db`

También se mantiene:

`data/users.json`

Se implementó:

- `IntegrityCheck`;
- `Checkpoint`;
- recuperación de cuenta;
- recuperación de posiciones;
- reconciliación de posiciones abiertas contra velas persistidas.

---

## 10. Sesión

Configuración utilizada:

```text
08:00 → 14:00
America/New_York
```

Existe:

```text
TRADING_SESSION_EXIT_AFTER_WINDOW=true
```

para finalizar el proceso al terminar la ventana.

La ventana horaria es una configuración operativa del proyecto, no una afirmación literal del PDF.

---

## 11. GitHub Actions

Archivo:

`.github/workflows/paper-session.yml`

Diseñado para sesiones PAPER de 6 horas:

- WEEX;
- BTCUSDT;
- 1H/15m/5m;
- `workflow_dispatch`;
- scheduler;
- timeout de 360 min;
- `go test ./...`;
- `go build`;
- ejecución;
- persistencia de `data/bot.db*` y `data/users.json` mediante artifact.

**Limitación:** GitHub Actions es efímero. Es razonable para PAPER, pero para LIVE se recomienda el servidor/VPS disponible con systemd.

---

## 12. `INVESTING_BULLS_STRATEGY_ID`

La variable debe contener solo una estrategia cuyo estado sea:

```text
ACTIVE
```

IDs probados y rechazados:

```text
investing-bulls-mtf-BTCUSDT-1791292373
investing-bulls-mtf-BTCUSDT-1791296891
```

**No utilizar ninguno de los dos.**

El workflow ya tiene:

```yaml
INVESTING_BULLS_STRATEGY_ID: ${{ vars.INVESTING_BULLS_STRATEGY_ID }}
```

En GitHub se configura en:

**Settings → Secrets and variables → Actions → Variables → New repository variable**

Nombre:

```text
INVESTING_BULLS_STRATEGY_ID
```

Valor:

```text
<ID_NUEVO_QUE_HAYA_QUEDADO_ACTIVE>
```

Actualmente **no hay que poner un ID rechazado**.

---

## 13. Protección contra estrategias rechazadas

`main.go` fue modificado para consultar SQLite al arrancar.

Ahora:

- si el ID no existe → live deshabilitado;
- si el estado no es `ACTIVE` → live bloqueado;
- si es `ACTIVE` → se crea el `LiveEngine`.

Además, `EvaluateLive` verifica el estado, por lo que una estrategia que posteriormente pase a `REJECTED` no debe generar operaciones.

---

## 14. Última validación walk-forward

La estrategia:

```text
investing-bulls-mtf-BTCUSDT-1791296891
```

fue rechazada.

Resultado:

```text
folds=4
positive=1
passed=false
```

Logs relevantes:

```text
event=OOS_STARTED
mode=mtf_walk_forward

event=LEARN_COMPLETED
scope=learn_mtf_exact
candles=802
configs=48
configs_ok=12
trades=15
profit_factor=0

event=LEARN_COMPLETED
scope=learn_mtf_exact
candles=902
configs=48
configs_ok=48
trades=19
profit_factor=0.0595...

event=STRATEGY_REJECTED
reason="OOS walk-forward MTF rechazado: 1/4 folds positivos"

event=OOS_COMPLETED
passed=false
folds=4
positive=1
```

### Interpretación

La estrategia actual **no está suficientemente validada**.

No se deben rebajar los criterios para conseguir un `ACTIVE`.

---

## 15. Diagnóstico por fold

Se añadió logging:

```text
mode=mtf_walk_forward_fold
```

para registrar:

- fold;
- barras train;
- barras OOS;
- trades;
- win rate;
- profit factor;
- retorno;
- drawdown;
- si fue positivo;
- motivo de folds no entrenables.

Esto debe utilizarse para descubrir por qué falla la validación.

---

## 16. Telegram

Se observaron errores de red de `getUpdates`, pero no son el foco principal.

También apareció:

```text
can't parse entities:
Unsupported start tag "strategyid"
```

Se añadió fallback automático:

1. intenta HTML;
2. si Telegram rechaza las entidades;
3. reintenta como texto plano.

---

## 17. Commits importantes recientes

- `fb7228b...` — learner Investing Bulls sin rebajar reglas.
- `2702cc7...` — backfill MTF antes de learning.
- `90b8083...` — aprendizaje MTF exacto + walk-forward.
- `6114974...` — reconciliación paper usando `CandleStore`.
- `7e1ac3a...` — sanitización de métricas `+Inf` para JSON.
- `e51b9c6...` — bloquear estrategias no ACTIVE al arranque.
- `80b4960...` — diagnóstico por fold.
- `b1b8d52...` — fallback Telegram a texto plano.

---

# 18. Estado actual: checklist

### Funciona

- [x] WEEX.
- [x] Backfill 1H.
- [x] Backfill 15m.
- [x] Backfill 5m.
- [x] WebSocket.
- [x] Backfill de huecos.
- [x] Health check.
- [x] SQLite.
- [x] Paper broker.
- [x] Recuperación/reconciliación.
- [x] Gestión de riesgo.
- [x] Kill switch.
- [x] Sesión temporal.
- [x] Learner Investing Bulls.
- [x] Learner MTF exacto.
- [x] Persistencia.
- [x] Walk-forward OOS.
- [x] Bloqueo de estrategias no ACTIVE.
- [x] Diagnóstico por fold.
- [x] GitHub Actions PAPER.

### Aún no demostrado

- [ ] Estrategia ACTIVE robustamente validada.
- [ ] 100 operaciones paper.
- [ ] Win rate >75% sostenido.
- [ ] Profit factor robusto.
- [ ] Drawdown aceptable.
- [ ] Consistencia por setup.
- [ ] Rendimiento neto con fees/slippage realistas.
- [ ] Operación real en WEEX.

---

# 19. Siguientes pasos

## Paso 1 — Verificar los últimos cambios

Ejecutar:

```powershell
go test ./...
go vet ./...
go build .
```

Los cambios más recientes de live/walk-forward/Telegram deben considerarse pendientes de verificación si todavía no existe un resultado CI posterior a esos commits.

---

## Paso 2 — Nuevo aprendizaje

Con WEEX funcionando:

```text
/learnibmtf BTCUSDT 5000 true
```

Obtener el nuevo ID.

No reutilizar IDs rechazados.

---

## Paso 3 — Validación OOS

```text
/validateibmtf <NUEVO_ID> BTCUSDT 5000
```

Revisar:

- folds;
- trades;
- win rate;
- PF;
- retorno;
- drawdown;
- setups;
- consistencia.

---

## Paso 4 — Analizar cada fold

Buscar:

```text
mode=mtf_walk_forward_fold
```

Determinar si el problema está en:

- falta de operaciones;
- win rate;
- profit factor;
- drawdown;
- determinados setups;
- cobertura de mercado;
- asociación Fibonacci/OB/imbalance;
- régimen de mercado.

---

## Paso 5 — Si sigue fallando

**No** hacer automáticamente:

- bajar `MinTrades`;
- aumentar drawdown permitido;
- eliminar confirmaciones;
- quitar setups;
- cambiar reglas solo para mejorar el resultado;
- optimizar sobre OOS;
- seleccionar manualmente el mejor fold.

En su lugar:

1. estudiar los diagnósticos;
2. revisar cobertura de setups;
3. revisar cantidad/calidad de eventos;
4. revisar asociaciones Fibonacci/OB/imbalance;
5. revisar distribución temporal;
6. estudiar sensibilidad de parámetros;
7. considerar más histórico;
8. considerar otros símbolos más adelante;
9. mantener separación estricta train/OOS.

---

## Paso 6 — Si aparece una estrategia `ACTIVE`

Configurar:

```text
INVESTING_BULLS_STRATEGY_ID=<ID_ACTIVE>
```

Ejecutar PAPER y confirmar:

```text
Investing Bulls live activo
```

Después medir las operaciones reales generadas por el motor.

---

# 20. Gate de 100 operaciones

La meta operativa del usuario es:

- al menos 100 operaciones;
- win rate >75%;
- antes de considerar dinero real.

Debe ser un **gate adicional**, no el único criterio.

También revisar:

- Profit Factor;
- drawdown;
- retorno;
- estabilidad temporal;
- consistencia por setup;
- costos;
- calidad del feed.

Un win rate alto por sí solo no demuestra rentabilidad.

---

# 21. Preparación LIVE

Solo después de una fase PAPER convincente:

- WEEX API key/secret;
- permisos mínimos;
- nunca guardar secretos en Git;
- `.env` solo en servidor;
- verificar permisos de retiro;
- validar `WeexBroker`;
- client order IDs;
- idempotencia;
- reconciliación de órdenes/posiciones;
- recuperación después de reinicio;
- órdenes parcialmente ejecutadas;
- errores de red;
- stale-feed gate;
- límites de pérdida;
- kill switch;
- bloqueo si no existe estrategia ACTIVE.

---

# 22. Despliegue recomendado

### PAPER

GitHub Actions puede utilizarse para sesiones de unas 6 horas.

### LIVE

Usar el servidor/VPS disponible:

```text
GitHub
  ↓
Servidor Linux
  ↓
/opt/tradingbot
  ↓
systemd
  ↓
TradingBot
  ├── WEEX market data
  ├── Investing Bulls
  ├── Risk Engine
  ├── Broker
  ├── SQLite
  └── Telegram
```

El código puede seguir modificándose mientras el bot está desplegado. El flujo recomendado es:

```text
desarrollo local
→ git commit
→ git push
→ scripts/update.sh
→ tests
→ build
→ backup
→ restart
→ health check
```

`.env` y `data/bot.db` no deben entrar al repositorio.

---

# 23. Mejora futura posible

Para PAPER podría ser útil que:

```text
INVESTING_BULLS_STRATEGY_ID=""
```

hiciera que el sistema seleccionara automáticamente la última estrategia `ACTIVE` compatible.

Para LIVE es más seguro mantener activación explícita por ID.

**Esta mejora todavía no está implementada.**

---

# 24. Regla principal del proyecto

```text
DATOS CORRECTOS
      ↓
ESTRATEGIA CORRECTA
      ↓
APRENDIZAJE SIN LOOK-AHEAD
      ↓
WALK-FORWARD OOS REAL
      ↓
PAPER TRADING
      ↓
ESTADÍSTICA SUFICIENTE
      ↓
RISK GATES
      ↓
LIVE
```

No invertir este orden solo para conseguir que el sistema produzca una señal.

---

# 25. Resumen para retomar el proyecto

**TradingBot está en fase de validación de Investing Bulls.**

WEEX ya proporciona los datos 1H/15m/5m. El learner MTF utiliza el mismo pipeline que live. La validación es walk-forward OOS real. Las estrategias rechazadas están bloqueadas. Se añadió diagnóstico por fold.

La última estrategia probada:

```text
investing-bulls-mtf-BTCUSDT-1791296891
```

fue:

```text
REJECTED
4 folds
1 positivo
```

Por tanto:

**Actualmente no existe un ID ACTIVE que deba ponerse en `INVESTING_BULLS_STRATEGY_ID`.**

### Prioridad inmediata

1. `go test ./...`
2. `go vet ./...`
3. `go build .`
4. `/learnibmtf BTCUSDT 5000 true`
5. `/validateibmtf <ID> BTCUSDT 5000`
6. estudiar `mtf_walk_forward_fold`;
7. corregir problemas reales sin relajar criterios;
8. repetir validación;
9. cuando exista `ACTIVE`, pasar a PAPER prolongado;
10. alcanzar y analizar el gate de 100 operaciones;
11. preparar LIVE solo después de una validación robusta.
