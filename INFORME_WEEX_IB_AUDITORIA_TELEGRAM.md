# Informe final: WEEX + Investing Bulls + auditoría Telegram

**Plan:** `PLAN_MAESTRO_WEEX_INVESTING_BULLS_AUDITORIA_TELEGRAM.md` (10-11-2026)
**Rama de trabajo:** `feat/weex-backfill-ib-audit`, derivada de `integration/ib-shared-engine` (`ba971bb`, PR #25)
**Fecha:** 15-11-2026 · **Entorno:** exclusivamente PAPER

> Resumen: el error `-1142 Parameter 'startTime' is invalid` está confirmado con
> peticiones reales y con la documentación oficial de WEEX. Tenía dos causas en
> el backfill y venía acompañado de otra que provocaba las propias
> desconexiones (los pings del canal público no se respondían). Las tres están
> corregidas y cubiertas por pruebas de regresión que fallan con el código
> anterior. Investing Bulls registra ahora cada fundamento por separado
> (regla, valor observado, umbral, vela, naturaleza del resultado), y 17
> comandos de Telegram de solo lectura lo explican. No se hizo merge, no se
> inició ninguna sesión PAPER y no se tocó `data/bot.db`.

---

## 1. Diagnóstico confirmado de `startTime invalid`

### 1.1 Contrato del endpoint (documentación oficial + API real)

Endpoint usado por el proyecto: `GET https://api-contract.weex.com/capi/v3/market/historyKlines`.
Documentación oficial: [Get Historical Klines](https://www.weex.com/api-doc/contract/Market_API/GetHistoryKlines)
(`startTime`/`endTime` inclusivos en ms, *must not be in the future*, `endTime ≥ startTime`;
`limit` 1–100, por defecto 100; peso IP 5).

Sondeo real del 15-11-2026 (BTCUSDT 1h, `now` = `1794800619000`, vela en formación `cur` = `1794798000000`):

| Petición | Respuesta |
| --- | --- |
| `startTime=cur-5h&endTime=cur-1` | 200, 5 velas (descendente: primera `cur-1h`) |
| `startTime=cur-3h&endTime=now` | 200, 3 velas — **la vela en formación nunca se devuelve** |
| `endTime=now+1h` | 400 `{"code":-1142,"msg":"Parameter 'endTime' is invalid."}` |
| `endTime=now+2s` / `now+30s` | 400 `-1142 'endTime' is invalid` (sin tolerancia de reloj) |
| `startTime=cur&endTime=now` | 200, 0 velas (rango válido vacío) |
| `startTime=cur+1h&endTime=now+1h` **(ruta del código antiguo)** | **400 `{"code":-1142,"msg":"Parameter 'startTime' is invalid."}`** |
| `startTime=endTime` (límite de vela) | 200, 1 vela (ambos extremos inclusivos) |
| `startTime > endTime` | 400 `-1171 startTime cannot be greater than endTime.` |
| `limit=2` | 200, 2 velas |
| `size=2` | 200, 5 velas — **`size` se ignora** (no existe en la API) |
| `limit=100` / `limit=101` / `limit=500` | 200 / 400 `-1142 'limit' is invalid` / 400 |
| segundos en vez de ms | 400 `-1142 'startTime' is invalid` |

Cabeceras de cuota observadas: `X-USED-WEIGHT-10S` / `X-REMAINING-WEIGHT-10S` (500 por 10 s).

### 1.2 Reproducción con el código anterior

La prueba de regresión extremo a extremo (`TestWexWSRecoversClosedBarsAfterDisconnect`)
se ejecutó contra una exportación limpia de `ba971bb` con un servidor que aplica el
contrato anterior. Falla en 5m, 15m y 1h y deja exactamente el mensaje de la sesión PAPER #57:

```text
weex ws: backfill de hueco BTCUSDT 5m falló: weex history 400: {"code":-1142,"msg":"Parameter 'startTime' is invalid."}
weex ws: backfill de hueco BTCUSDT 15m falló: weex history 400: {"code":-1142,"msg":"Parameter 'startTime' is invalid."}
weex ws: backfill de hueco BTCUSDT 1h falló: weex history 400: {"code":-1142,"msg":"Parameter 'startTime' is invalid."}
--- FAIL: TestWexWSRecoversClosedBarsAfterDisconnect (12.00s)
--- FAIL: TestOldIsPingPublicEvent: {"event":"ping"} (formato del canal público WEEX) no se reconoce como ping
```

Con la corrección, las mismas pruebas pasan y el servidor de contrato no registra ninguna petición inválida.

## 2. Causa raíz

1. **Cursor sobre la vela abierta.** `runConnection()` hacía `lastTS = c.Start` con cada mensaje,
   y el canal kline de WEEX empuja sobre todo la vela **en formación**. Tras la desconexión,
   `backfillGap()` pedía `start = lastTS + step`, es decir, el inicio de la vela siguiente
   (futuro): `-1142 startTime`.
2. **`end = now + step`, siempre futuro.** Aunque el cursor hubiera sido una vela cerrada,
   `endTime` era futuro: `-1142 endTime`. **Ningún hueco podía recuperarse nunca** por esta ruta.
3. **Cerradas perdidas.** La versión cerrada de la vela que estaba en curso al desconectar se
   descartaba (`!k.Start.After(lastTS)`), con lo que se saltaban velas cerradas.
4. **Desconexiones "session is inactive".** WEEX envía en el canal público
   `{"event":"ping","time":"…"}` y cierra la sesión tras más de 10 pings sin
   `{"method":"PONG","id":1}` ([WebSocket intro](https://www.weex.com/api-doc/products/futures/streams/websocket-intro.md)).
   `isPing()` solo miraba `method`/`type`/`ping`, no `event`, así que los pings se ignoraban.
   En vivo se observaron 5 pings en ~5 min: unos 10 sin respuesta equivalen a ~10 min, que
   cuadra con las desconexiones recurrentes `close 1000 (normal): session is inactive`.
5. **Parámetro `size`.** No existe en la API (WEEX lo ignoraba); el documentado es `limit`.

## 3. Archivos modificados

| Archivo | Cambio |
| --- | --- |
| `internal/ingest/weex_contract.go` (nuevo) | Contrato verificado, guarda previa al envío (`validateHistoryWindow`), clasificación de errores (negocio/HTTP/429/5xx/timeout/red/parseo), `Retry-After` (s o fecha HTTP, tope 60 s), saneado de mensajes, alineación de velas (incl. semanal desde lunes). |
| `internal/ingest/weex_stream.go` (nuevo, extraído de `weex.go`) | Stream con cursor de vela **cerrada**, reconciliación REST en cambio de vela / reconexión / hueco WS, cola pendiente y huecos explícitos, reintentos acotados, estado irrecuperable, `{"event":"ping"}`, `User-Agent`, cierre inmediato al cancelar, todas las velas del array `d`. |
| `internal/ingest/weex_health.go` (nuevo) | `Health()` por feed, `RecentErrors()`, registros de backfill, eventos persistibles (`IngestEventSink`) con clave estable. |
| `internal/ingest/weex.go` | `limit` en lugar de `size`; `endTime` recortado a `ahora − ClockSkew`; rango futuro sin petición; reintentos clasificados; el backfill inicial siembra el cursor. |
| `internal/ingest/weex_gap_test.go` (nuevo), `weex_test.go` | Regresión completa (§8); fixtures antiguos corregidos a velas alineadas y al contrato verificado. |
| `internal/investingbulls/audit.go` (nuevo) | Modelo de auditoría: resultado/categoría por fundamento, calidad de datos por timeframe, auditoría de confluencia por zona, diagnósticos de confirmación 5m, Fibonacci y plan, `mainClosedAt`, `StaleBarTrace`. |
| `internal/investingbulls/trace.go` | Traza v2 y evaluador LIVE reescrito con la **misma** decisión y explicación concreta de cada rechazo. |
| `internal/investingbulls/confluence.go`, `multitimeframe.go` | Decisión compartida con la auditoría (`componentFlags`, `confirmationDiagnostics`); el learner usa solo velas 1h cerradas. |
| `internal/strategymanager/live.go`, `live_audit.go` (nuevo) | IDs de correlación en la traza, estado del motor, errores registrados y persistidos, protección de vela atrasada. |
| `internal/ibeval/engine_events.go` (nuevo) | Contrato de eventos del motor. |
| `internal/store/ops_events.go` (nuevo), `sqlite.go` | Tabla `ops_events`, búsquedas de auditoría (por vela, por fragmento, motivos, errores de Outbox/PAPER). |
| `ib_audit_format.go`, `ib_audit_reports.go` (nuevos), `ib_diagnostics.go`, `telegram_dispatch.go` | 17 comandos de auditoría; sustituyen a los `/ibstatus`, `/ibevals` y `/ibops` anteriores. |
| `main.go`, `config/config.go` | Wiring de eventos, retención diaria, `IB_MAX_BAR_LAG_MINUTES`, `OPS_EVENTS_*`. |
| `handlers/commands.go`, `.github/scripts/telegram_workflow_controller.py` | Ayuda del runner y del controlador (solo texto). |
| `README.md`, `docs/TELEGRAM_CONTROLLER.md`, `docs/INVESTING_BULLS_STRATEGY.md` | Documentación de comandos, contrato y alineación. |

### Cambios de contrato o arquitectura (justificación)

- **Semántica del cursor WEEX.** Antes: "inicio de la última vela vista". Ahora: "inicio de la última
  vela **cerrada** entregada aguas abajo". Es la única definición que no permite saltar cerradas
  pendientes (requisito 2.2.3).
- **Alineación 1h (learner y LIVE).** Antes se usaba la vela 1h que **contiene** a la vela 15m evaluada.
  En backtest eso era información futura (lookahead) y en LIVE una vela parcial: ambas rutas usaban
  datos distintos. Ahora las dos usan solo velas 1h cerradas en el instante de decisión (cierre de la
  15m). Esto corrige el punto 3.3 de la auditoría ("alineación temporal de los marcos") y no cambia
  el objetivo de la estrategia, que según el material fuente toma la dirección de la 1h. **Los
  modelos ya aprendidos se calcularon con el criterio anterior; ver riesgos.**
- **Motivos de rechazo más concretos.** `confirmacion_5m_fail` pasa a ser
  `confirmacion_5m_quiebre_contrario` / `_sin_quiebre` / `_datos_insuficientes`;
  `fib_fail` → `fib_sin_impulso_compatible` / …; `confluencia_fail` →
  `confluencia_insuficiente` / `confluencia_direccion_contraria`; `trade_plan_fail` → `plan_*`.
  La decisión aceptar/rechazar es idéntica (hay pruebas que lo demuestran con la implementación anterior).
- **Una tendencia 1h sin dirección** se rechaza en su propia etapa (`tendencia_1h_sin_direccion`)
  en lugar de más adelante (`sin_setup_15m`/`setup_no_alineado`). La decisión es la misma.
- **Datos atrasados.** Si falta la última vela cerrada de 1h, 15m o 5m, la evaluación se registra
  como `datos_atrasados_*` (problema de datos, no de la estrategia) y no se evalúa con datos viejos.

## 4. Diseño final del backfill y política de reconexión

- **Cursor:** `lastClosed`, el inicio de la última vela cerrada entregada. Lo siembra el
  backfill inicial y nunca avanza con una vela abierta.
- **Disparadores de reconciliación:** (a) llega la vela en formación X (todas las < X están cerradas);
  (b) reconexión; (c) llega una vela cerrada con un hueco delante; (d) reintento vencido.
- **Rango solicitado:** `[cursor+step, min(objetivo, últimaCerradaSegura)]`, con
  `últimaCerradaSegura = alinear(ahora − 5 s) − step`. El `endTime` enviado es el fin de periodo de la
  última vela pedida (`to + step − 1 ms`), nunca futuro, y el `startTime` va siempre alineado a vela.
  Si ninguna vela está aún cerrada en el servidor, no se envía nada y se reintenta cuando lo esté.
- **Validación:** se descartan, con motivo contado, velas no alineadas, fuera de rango, abiertas o
  duplicadas. El resto se ordena, se entrega en orden cronológico y solo entonces avanza el cursor.
- **Pendiente explícito:** la cola no recibida queda como *pendiente* (degradado, con intentos y
  próximo reintento). Los huecos intermedios que WEEX omite quedan como *huecos* y se reintentan.
  Tras `MaxGapRetries` (6) respuestas sin datos y 2 min después del cierre, el hueco se declara
  *irrecuperable*: evento FAIL y cursor que lo deja constar. Un error de red/HTTP nunca se declara
  irrecuperable; se reintenta con backoff (2 s → 60 s).
- **Reintentos REST:** 429, 5xx, timeout y red con backoff acotado y `Retry-After`; los errores de
  negocio (400 −11xx) no se reintentan. Cada recuperación tiene un límite de 20 s para no bloquear el WS.
- **Reconexión WS:** backoff 1 s → 30 s, que vuelve al mínimo si la conexión duró ≥ 60 s; PONG a
  cualquier ping (`event`/`type`/`method`/`ping`); `User-Agent` genérico.
- **Velas tardías:** se entregan para persistirlas; el motor ignora las anteriores a su última vela.
  Una vela que llega más de `IB_MAX_BAR_LAG_MINUTES` (15) después de su cierre se registra como
  `vela_atrasada` y no genera señal.
- **Observabilidad:** logs con cursor anterior/posterior, rango, esperadas/recibidas/entregadas/
  tardías/descartadas y motivo; eventos en `ops_events`; `Health()` para Telegram.

## 5. Comandos de Telegram

17 comandos, todos de solo lectura y reservados al administrador. Se generan en segundo plano con
un límite de 20 s y recuperación de pánicos, y se dividen en mensajes de ≤ 3500 caracteres con
`(n/m)`. Muestran la hora `America/Havana` y los estados OK/WARN/FAIL/NO DATA. Referencia completa
en `docs/TELEGRAM_CONTROLLER.md` y en `/ibhelp`.

`/ibstatus` `/ibaudit` `/ibwhy` `/ibtrace` `/ibfoundation` `/ibevals` `/ibdata` `/ibhealth`
`/iberrors` `/ibops` `/ibstats` `/ibhelp` · `/weexstatus` `/weexdata` `/weexgaps` `/weexbackfill` `/weexerrors`

### Ejemplos reales

Generados el 15-11-2026 con datos **reales** de WEEX (BTCUSDT): backfill real de 1500 velas por
timeframe, un modelo MTF aprendido sobre esos datos **solo en una base temporal de demostración**,
evaluación LIVE de las últimas 80 velas 15m y suscripción WS real a 1h/15m/5m. No se usó `data/bot.db`
ni se abrió ninguna operación.

Ejecución 2 (1h/15m/5m suscritos, informes generados con el stream conectado):

```text
BACKFILL REAL 1h: 1500 velas en 5.833s; primera 2026-09-14T16:00:00Z última 2026-11-16T03:00:00Z
BACKFILL REAL 15m: 1500 velas en 5.119s; primera 2026-10-31T13:30:00Z última 2026-11-16T04:15:00Z
BACKFILL REAL 5m: 1500 velas en 5.757s; primera 2026-11-10T23:30:00Z última 2026-11-16T04:25:00Z
MODELO aprendido (solo demo): investing-bulls-mtf-BTCUSDT-1794803668 estado=candidate trades=13
EVALUACIONES REALES: 80 velas 15m evaluadas
  46 x rejected confluencia_insuficiente
  32 x rejected confirmacion_5m_quiebre_contrario
  2 x rejected plan_stop_supera_maximo_o_lado_incorrecto
WS REAL 5m: vela CERRADA 2026-11-16T04:30:00Z close=64905.1 (recibida 04:35:00)
SALUD BTCUSDT 1h: conectado=true reconexiones=0 pings=1 pongs=1 cerradasWS=0 cerradasREST=1 repetidas=300 cursor=2026-11-16T03:00:00Z degradado=false 
SALUD BTCUSDT 15m: conectado=true reconexiones=0 pings=1 pongs=1 cerradasWS=0 cerradasREST=1 repetidas=300 cursor=2026-11-16T04:15:00Z degradado=false 
SALUD BTCUSDT 5m: conectado=true reconexiones=0 pings=1 pongs=1 cerradasWS=1 cerradasREST=1 repetidas=300 cursor=2026-11-16T04:30:00Z degradado=false 
WS REAL: 1 velas cerradas entregadas durante la suscripción
```

Ejecución 1 (solo 5m suscrito ~5 min): WEEX envió 5 pings `{"event":"ping"}` y se respondieron 5 PONG, sin reconexiones:

```text
WS REAL 5m: vela CERRADA 2026-11-16T04:25:00Z close=65023.2 (recibida 04:30:00)
SALUD BTCUSDT 5m: conectado=true reconexiones=0 pings=5 pongs=5 cerradasWS=1 cerradasREST=1 cursor=2026-11-16T04:25:00Z degradado=false 
```

`repetidas=300`: al suscribirse, WEEX reenvía ~300 velas históricas en el array `d`. Todas son
anteriores o iguales al cursor sembrado por el backfill, así que se descartan sin reenviarlas aguas abajo
(sin duplicados en el motor). La vela 5m cerrada llegó por WS en el mismo instante del cambio de vela.

**/ibaudit**

```text
AUDITORÍA INVESTING BULLS
2026-11-15 23:35 · hora America/Havana
Evaluación: ht5b3
Símbolo: BTCUSDT · vela 15m 2026-11-15 23:15
Marcos: 1h / 15m / 5m
Motor: ib-mtf-live/2026.11.15 · modelo 9d36420c30107eebab7d6436...
Estado: RECHAZADA

🟢 CONTEXTO 1H: APROBADO
🟢 ESTRUCTURA 15M: APROBADO
🟢 ALINEACIÓN MTF: APROBADO
🟢 CONFIRMACIÓN 5M: APROBADO
🟢 FIBONACCI: APROBADO
🟡 IMBALANCE: NO CONFIRMADO
🟢 ORDER BLOCK: APROBADO
🟢 SUPPLY/DEMAND: APROBADO
🔴 CONFLUENCIA: RECHAZADO
▫️ PLAN DE TRADE: NO EVALUADO

MOTIVO PRINCIPAL
🔴 CONFLUENCIA — RECHAZADO
Ninguna zona Fibonacci reúne la confluencia del modo all4. zona 1: faltan componentes obligatorios: imbalance; zona 2: faltan componentes obligatorios: imbalance.

DETALLE
- Regla evaluada: modo all4: fibonacci+order_block+imbalance+supply_demand
- Valor observado: {"zona1":3,"zona2":3}
- Umbral requerido: 4
- Vela utilizada: 2026-11-15 23:15
- Datos: válidos
- Tipo: 🟡 La estrategia rechaza correctamente la oportunidad (regla)
- Confluencias encontradas: fibonacci, order_block, supply_demand
- Ausentes: imbalance

RESULTADO
No se abrió operación PAPER.

Más: /ibwhy ht5b3 · /ibfoundation ht5b3 · /ibtrace ht5b3
```

**/ibwhy**

```text
¿POR QUÉ NO HUBO SEÑAL?
2026-11-15 23:35 · hora America/Havana
Evaluación ht5b3 · vela 15m 2026-11-15 23:15 · rejected

🟡 La estrategia rechaza correctamente la oportunidad (regla)
Fundamento: CONFLUENCIA — RECHAZADO
Motivo: Ninguna zona Fibonacci reúne la confluencia del modo all4. zona 1: faltan componentes obligatorios: imbalance; zona 2: faltan componentes obligatorios: imbalance.
Regla: modo all4: fibonacci+order_block+imbalance+supply_demand
Observado: {"zona1":3,"zona2":3} · Requerido: 4
· Zona 1 (64951.6–64970.3): faltan componentes obligatorios: imbalance
· Zona 2 (65052.8–65101.5): faltan componentes obligatorios: imbalance
Código: confluencia_insuficiente (rechazo_regla)

Desglose: /ibfoundation ht5b3 · traza: /ibtrace ht5b3
```

**/ibfoundation (extracto: confluencia por zona)**

```text
CONFLUENCIA modo all4 (exige fibonacci+order_block+imbalance+supply_demand, tolerancia 1.00%) · precio 65023.2
Zona 1 64951.615–64970.35 · score 3/4
   ✓ fibonacci dist 0.096% (precio fuera de la zona pero dentro de la tolerancia)
   ~ imbalance dist 2.525% (relleno 0%)
   ✓ order_block dist 0.899%
   ✓ supply_demand dist 0.618%
   → faltan componentes obligatorios: imbalance
Zona 2 65052.784–65101.495 · score 3/4
   ✓ fibonacci dist 0.083% (precio fuera de la zona pero dentro de la tolerancia)
   ~ imbalance dist 2.525% (relleno 0%)
   ✓ order_block dist 0.899%
   ✓ supply_demand dist 0.618%
   → faltan componentes obligatorios: imbalance
Leyenda: ✓ cuenta · ~ existe pero no cuenta · ✗ no existe
```

**/ibevals**

```text
EVALUACIONES INVESTING BULLS
2026-11-15 23:35 · hora America/Havana
Estrategia: investing-bulls-mtf-BTCUSDT-1794803668 · últimas 24h

Procesadas: 80 · señales 0 · rechazos 80 · errores 0 · interrumpidas 0
Por naturaleza: estrategia 80 · datos 0 · infraestructura 0

Rechazos por motivo
· 46 × confluencia_insuficiente [estrategia] última 2026-11-15 23:34
· 32 × confirmacion_5m_quiebre_contrario [estrategia] última 2026-11-15 23:34
· 2 × plan_stop_supera_maximo_o_lado_incorrecto [estrategia] última 2026-11-15 23:34

Últimas evaluaciones
· 2026-11-15 23:15 rejected ht5b3 confluencia_insuficiente
· 2026-11-15 23:00 rejected ht5ao confluencia_insuficiente
· 2026-11-15 22:45 rejected ht5a9 confluencia_insuficiente
· 2026-11-15 22:30 rejected ht59u confirmacion_5m_quiebre_contrario
· 2026-11-15 22:15 rejected ht59f confirmacion_5m_quiebre_contrario

Detalle: /ibaudit <id> · /ibwhy
```

**/ibdata**

```text
DATOS DE MERCADO
2026-11-15 23:35 · hora America/Havana
Símbolo: BTCUSDT

🟢 1h OK
   última cerrada: 2026-11-15 22:00 (cierre hace 35m) · esperada 2026-11-15 22:00
   en formación: 2026-11-15 23:00
   retraso: 0 velas · huecos en las últimas 300: 0 · origen WEEX (rest)

🟢 15m OK
   última cerrada: 2026-11-15 23:15 (cierre hace 5m) · esperada 2026-11-15 23:15
   en formación: 2026-11-15 23:30
   retraso: 0 velas · huecos en las últimas 300: 0 · origen WEEX (rest)

🟢 5m OK
   última cerrada: 2026-11-15 23:30 (cierre hace 50s) · esperada 2026-11-15 23:30
   en formación: 2026-11-15 23:35
   retraso: 0 velas · huecos en las últimas 300: 0 · origen WEEX (ws)

Huecos y recuperación: /weexgaps · /weexbackfill
```

**/ibhealth**

```text
SALUD DEL SISTEMA
2026-11-15 23:35 · hora America/Havana
🟢 Global: OK

🟢 WEEX WebSocket OK — 1h ok · 15m ok · 5m ok
🟢 WEEX REST OK — sin errores en 1h
🟢 Backfill OK — sin huecos pendientes
🟢 Persistencia OK — base accesible
🟢 Motor IB OK — evalúa investing-bulls-mtf-BTCUSDT-1794803668 · última evaluación hace 1m

Sesión: demo (sin sesión PAPER)
```

**/weexdata**

```text
WEEX · ÚLTIMAS VELAS
2026-11-15 23:35 · hora America/Havana

🟢 BTCUSDT 1h — secuencia OK
   cerrada: 2026-11-15 22:00 (fuente rest, recibida hace 1m)
   en formación: 2026-11-15 23:00 · último mensaje hace 0s
   cerradas WS 0 · REST 1 · repetidas (ya entregadas) 300 · desalineadas 0

🟢 BTCUSDT 15m — secuencia OK
   cerrada: 2026-11-15 23:15 (fuente rest, recibida hace 1m)
   en formación: 2026-11-15 23:30 · último mensaje hace 0s
   cerradas WS 0 · REST 1 · repetidas (ya entregadas) 300 · desalineadas 0

🟢 BTCUSDT 5m — secuencia OK
   cerrada: 2026-11-15 23:30 (fuente ws, recibida hace 49s)
   en formación: 2026-11-15 23:35 · último mensaje hace 0s
   cerradas WS 1 · REST 1 · repetidas (ya entregadas) 300 · desalineadas 0
```

**/weexstatus (extracto 5m)**

```text
🟢 BTCUSDT 5m OK
   WS: conectado desde 2026-11-15 23:34 · último mensaje hace 0s
   reconexiones 0 · pings 1 / pongs 1
   recuperaciones 0 · fallidas 0 · velas recuperadas 0 · descartadas 0
```

El resto (`/ibtrace`, `/ibstatus`, `/weexbackfill`, `/weexgaps`, `/weexerrors`, `/iberrors`, `/ibops`, `/ibstats`, `/ibhelp`) se generó en la misma ejecución sin errores. `/ibops` y `/ibstats` muestran NO DATA porque la demostración no abre operaciones.


## 6. Fundamentos de Investing Bulls con trazabilidad

Cada evaluación persiste (`ib_evaluations.trace_json`, traza v2) la versión del motor, la estrategia,
el modelo, `evaluation_key`, `signal_key`, el instante de decisión, la calidad de datos por timeframe
(velas, primera/última, esperada, retraso, huecos) y una etapa por fundamento con
**resultado** (aprobado / rechazado / no evaluable / dato insuficiente), **categoría** (regla cumplida,
rechazo de la regla, no existe en el mercado, existente pero inválido, no confirmado, falta de datos,
datos atrasados, error de cálculo, infraestructura, no requerido, no evaluado), regla, valor observado,
umbral, vela y detalle:

| Fundamento | Qué se registra |
| --- | --- |
| Datos | Velas usadas por timeframe, retraso frente a la esperada, huecos, rechazo por datos atrasados/insuficientes. |
| Contexto 1h | Tendencia, último swing alto/bajo, último BOS/CHOCH, última vela 1h **cerrada** usada. |
| Estructura 15m | Tendencia, swings, quiebres, setup elegido (tipo, dirección, vela) y setups recientes con si están permitidos. |
| Alineación MTF | Dirección del setup frente a la tendencia 1h y setups permitidos por el modelo. |
| Confirmación 5m | Velas 5m usadas, corte, número de quiebres y último quiebre (tipo, dirección, nivel, vela) frente a la dirección requerida. |
| Fibonacci | Swing de origen/destino, niveles 0/0.45/0.5/0.72/0.85/1, zonas, zona del precio; motivo exacto si no se puede trazar. |
| Order block / Imbalance / Oferta-demanda | Detectados, compatibles con la dirección, los 3 más recientes (rango, testeado, % relleno) y veredicto (confluye, existe fuera de tolerancia, testeado/zona quemada, no existe). |
| Confluencia | Para **cada** zona Fibonacci: componentes presentes/contados con distancia y tolerancia, puntuación parcial frente a la exigida, componentes obligatorios ausentes, zona ganadora, encontrados/ausentes. |
| Plan de trade | Entrada, SL, TP, riesgo %, R:R; motivo concreto si se descarta (sin objetivo, stop > máximo o lado incorrecto…). |
| Riesgo / Resultado | Riesgo delegado al risk engine (declarado); señal generada o motivo final y su naturaleza. |

Las etapas no alcanzadas se listan como "no evaluado (pipeline detenido en X)": ninguna traza omite
fundamentos. La explicación usa **las mismas funciones** que la decisión (`componentFlags`,
`confirmationDiagnostics`), y las pruebas comparan con las implementaciones anteriores.

## 7. Persistencia, eventos y migraciones

- **`ops_events`**, nueva y aditiva (`CREATE TABLE IF NOT EXISTS`, índices por componente/severidad/tipo).
  Guarda eventos WEEX (conexión, desconexión, hueco detectado, backfill ok/parcial/fallido, hueco
  irrecuperable) y errores del motor (reserva, evaluación, persistencia, estrategia).
  `event_key` UNIQUE + `INSERT OR IGNORE`: un reintento del mismo suceso no duplica filas.
- **Retención:** `OPS_EVENTS_RETENTION_DAYS=30` y `OPS_EVENTS_MAX_ROWS=50000`, aplicadas cada 24 h.
  `ib_evaluations` conserva su retención (90 días).
- **Correlación:** la traza lleva `evaluation_key` y `signal_key`; Outbox, recibos, `ib_order_links`,
  `ib_position_links` e `ib_paper_events` enlazan con la operación PAPER. El ID corto de los informes
  (vela en base 36) se resuelve por `bar_ts`.
- **Fallos de persistencia observables:** si una traza o un evento no se guarda, el error se propaga
  y queda en memoria (`/iberrors`, `/ibhealth`) y, si es posible, en `ops_events`. Nunca se presenta
  como auditoría completa.
- **Migración probada** sobre una base existente con datos (`TestOpsEventsMigrationPreservesExistingDatabase`):
  conserva las evaluaciones previas y es idempotente. No se borra ni sobrescribe `data/bot.db`.

## 8. Pruebas ejecutadas y resultados

Entorno: Windows 11, Go 1.26.2 portable, `CGO_ENABLED=0`.

| Comando | Resultado |
| --- | --- |
| `gofmt -l` sobre los archivos Go modificados | sin diferencias |
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test ./internal/ingest/...` | PASS |
| `go test -count=1 ./...` | PASS (todos los paquetes) |
| `go test -race` | **No ejecutado:** `-race requires cgo` y no hay gcc en la máquina. Pendiente en CI Linux. |
| `python -m unittest discover -s .github/scripts` | PASS (77) con `tzdata`; sin `tzdata`, `test_start_follows_daylight_saving` falla igual en la base (preexistente, entorno) |

Pruebas nuevas destacadas:

- **WEEX:** reproducción de `-1142` (5m/15m/1h); contrato (`validateHistoryWindow`, `limit`, ms);
  recuperación tras desconexión 5m/15m/1h extremo a extremo; cursor en vela abierta y en vela
  cerrada; reloj junto al cambio de vela con servidor 3 s retrasado; historial vacío (pendiente →
  irrecuperable, sin bucles); velas repetidas/desordenadas; huecos de 1, 2 y 7 velas; hueco intermedio
  recuperado tarde sin mover el cursor atrás; 400/429+`Retry-After` (incl. tope)/5xx/timeout;
  120 casos aleatorios sin ninguna petición fuera de contrato y con continuidad exacta;
  `{"event":"ping"}`; arrays `d` con varias velas; fallo del sink observable; solo endpoints públicos
  de mercado sin credenciales; feeds retirados.
- **Investing Bulls:** confirmación 5m, Fibonacci y zonas de confluencia deciden igual que antes (miles
  de casos); la 1h ya no incluye la vela en formación; todos los fundamentos con resultado/categoría;
  datos atrasados como problema de datos; roundtrip JSON.
- **Motor:** IDs de correlación en la traza; vela atrasada registrada sin señal; fallo de persistencia observable.
- **Store:** migración sobre base existente, deduplicación, filtros, retención, búsqueda por vela/fragmento.
- **Telegram:** contenido real sin valores de ejemplo; datos frente a estrategia; paginación; mensajes
  ≤ 4096; los 17 comandos solo para el administrador y documentados; ninguna consulta altera la base,
  la sesión ni el motor; motor/feed no disponibles.
- **Sin fallback a Chandelier:** se mantienen `TestSharedStrategyNeverFallsBackToChandelierWhenIBIsRequested`
  y similares; `/ibstatus` marca FAIL si el motor no evalúa Investing Bulls.

**Preexistente (no regresión):** `TestIBConcurrentClaimHasSingleOwner` falla de forma intermitente
también en la base (2/30 ejecuciones en `ba971bb`). Causa: la prueba captura `now` antes de sembrar y
`RecordEvaluation` fija `available_at` con un `time.Now()` posterior; si cruzan un milisegundo, la fila
no es reclamable con ese `now`. No afecta a producción (el worker reclama con la hora actual).

## 9. Commits, rama y PR

Rama local `feat/weex-backfill-ib-audit` sobre `ba971bb` (`integration/ib-shared-engine`):

- `866bda8` fix(telegram): IDs de evaluación únicos y detalles hallados con datos reales WEEX
- `5d87756` fix(ingest): no listar como desconectados los feeds retirados por cambio de selección
- `5797ebe` docs: comandos de auditoría, contrato historyKlines y alineación MTF
- `0de88c0` feat(telegram): comandos de auditoría Investing Bulls y WEEX
- `70bdeac` test(ingest): la ingestión WEEX solo usa endpoints públicos de mercado
- `196193a` feat(store,ib): eventos operativos persistidos y auditoría del motor
- `6c76d94` feat(ib): trazabilidad por fundamento y alineación 1h sin lookahead
- `59ef07d` fix(ingest): backfill WEEX conforme al contrato y cursor de velas cerradas
- *(este informe se añade en el commit siguiente)*

**PR:** no se pudo publicar. La cuenta Git de esta máquina (`PedroLaceup`) recibe HTTP 403 al
hacer push a `JoelVargas08/TradingBot` (comprobado con `git push --dry-run`) y no existe un fork. Los
commits están en la rama local y se entregan también como *bundle* y parches (ver la entrega). PR #25
sigue abierta y sin cambios: no se cerró ni se fusionó nada.

## 10. Riesgos y tareas pendientes

1. **Reaprender y revalidar los modelos IB MTF.** La alineación 1h ahora usa solo velas cerradas, y
   los modelos persistidos se aprendieron con el criterio anterior (lookahead de la vela 1h en curso).
   Sus estadísticas OOS pueden cambiar. Recomendado: `/learnibmtf` + `/validateibmtf` antes de volver
   a operar PAPER con conclusiones.
2. **Semántica de la confirmación 5m (decisión pendiente).** Solo se usan velas 5m cerradas **antes
   del inicio** de la vela 15m evaluada (criterio heredado, conservador). Las tres velas 5m dentro de esa
   15m ya están cerradas en la decisión y no se usan. Cambiarlo altera la estrategia y el learner; no se
   hizo sin autorización.
3. **`go test -race`** pendiente en un entorno con CGO (CI Linux).
4. **Push/PR:** requiere acceso de colaborador o un fork.
5. **Validación en sesión real:** la corrección se verificó contra la API real (backfill y WS con pings,
   cambio de vela y vela cerrada), pero no en una sesión PAPER completa, que no se inició por el plan.
   Conviene revisar `/weexstatus`, `/weexgaps` y `/iberrors` en la próxima sesión autorizada.
6. **`IB_MAX_BAR_LAG_MINUTES=15`** evita señales con precios viejos tras una caída; ajustar si se
   prefiere otro margen (`0` lo desactiva).
7. **Material fuente:** el PDF dice "0.72 y 0.86" en un punto y "0.72 – 0.85" en el resto; el código usa
   0.85 (sin cambios). El modelo de la demostración eligió confluencia `all4` (4/4), más estricta que la
   "confluencia de 3 confirmaciones" del texto: esto explica la mayoría de rechazos observados y es una
   decisión del learner, no un fallo.
8. **Flake preexistente** `TestIBConcurrentClaimHasSingleOwner` (§8).

## 11. Confirmación explícita

- El bot **permanece exclusivamente PAPER**. No se añadieron endpoints ni credenciales de trading, y
  la ingestión solo hace `GET /capi/v3/market/*` sin credenciales (con prueba). `MODE=live` sigue
  abortando sin adaptador de broker.
- **No se inició ni se programó ninguna sesión PAPER** y no se modificó ningún workflow. Solo cambió el
  texto de ayuda del script del controlador.
- **No se hizo merge**, no se consolidaron ramas, no se cerró PR #25 ni se reescribió historial.
- **No se eliminaron datos:** `data/bot.db` no existe en esta máquina y no se tocó; la migración es
  aditiva. La demostración usó una base temporal fuera del repositorio.
- No se cambió BTCUSDT por XAUUSD ni se activó fallback a Chandelier.
