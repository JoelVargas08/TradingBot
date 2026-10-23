# Controlador de Telegram ↔ GitHub Actions (sesiones PAPER)

Este documento describe cómo la sesión PAPER de TradingBot **arranca sola** a las
08:00 de Cuba (incluso con el usuario sin conectar a Telegram) y cómo se
controla desde el chat. **Todo el flujo permanece en modo PAPER: ningún comando
habilita trading real.**

## Arquitectura

```text
                    ┌──────────────────────────────────────────────┐
                    │  .github/workflows/telegram-controller.yml   │
                    │  (cada 5 min · cron sin zona horaria)        │
                    │  .github/scripts/telegram_workflow_controller.py
                    └───────────────┬──────────────────────────────┘
                                    │  1. consulta los runs de paper-session.yml
                                    │  2. arranca la sesión del día si toca
                                    │     (08:00–13:00, America/Havana)
                                    │  3. si NO hay runner activo: escucha
                                    │     Telegram hasta 240 s (ventana de
                                    │     escucha) y atiende /ping, /help,
                                    │     /session, /session_start, /session_stop
                                    ▼
                    ┌──────────────────────────────────────────────┐
                    │  .github/workflows/paper-session.yml         │
                    │  (runner PAPER · WEEX · estado persistido)   │
                    │  main.go → internal/telegram.Listener         │
                    │  el runner es el dueño de getUpdates y avisa │
                    │  por Telegram que ya está trabajando          │
                    └──────────────────────────────────────────────┘
```

### Ventana de escucha y latencia

El cron del controlador dispara cada 5 minutos, así que el job **no termina
inmediatamente**: se queda en long polling durante
`CONTROLLER_POLL_WINDOW_SECONDS` (240 s por defecto, máximo 260 s). Con eso la
latencia de un comando baja de "hasta 5 minutos" a "hasta ~1 minuto" sin
solapar ciclos (el job dura ~260 s y el siguiente arranca a los 300 s).

El ciclo termina antes si atende un comando de control, si el runner toma el
token (HTTP 409) o si el propio workflow se cancela.

### Un solo consumidor de `getUpdates` por token

Telegram permite un único `getUpdates` activo por token: dos consumidores
provocan `HTTP 409 Conflict` y Updates se pierden. La regla del diseño es:

| Estado de GitHub Actions | Dueño de `getUpdates` | Quién responde |
| --- | --- | --- |
| Run `in_progress` | **el runner** (Go) | `/ping`, `/help`, `/session`, `/positions`, `/risk`, … |
| Run `queued` (sin runner aún) | **el controlador** (Python) | `/session_stop` cancela la ejecución en cola por API |
| Sin runs | **el controlador** (Python) | `/ping`, `/help`, `/session`, `/session_start`, `/session_stop` |

El runner, al arrancar, puede encontrar un `409` si el controlador tenía el
token en ese instante: el listener lo clasifica y reintenta con backoff
exponencial, sin perder ningún mensaje.

## Arranque automático a las 08:00 (America/Havana)

Hay **dos disparadores** con la misma semántica (la sesión arranca y **opera**
sola dentro de la ventana 08:00–14:00):

1. **El controlador** (`.github/workflows/telegram-controller.yml`): cada ciclo
   comprueba `AutoStartPolicy.is_due()` y despacha `paper-session.yml` si
   toca. Es el dispatcher principal porque además **vigila** el estado, avisa
   al admin y reintenta.
2. **El `schedule` del propio runner** (`cron: "0 8 * * 1-5"`,
   `timezone: America/Havana`): disparo redundante.

El segundo existe por una razón medida: **los cron de GitHub Actions se
ejecutan con retraso**. En este repositorio el cron `*/5` del controlador se
ha ejecutado en la práctica **cada 4–6 horas**, con huecos de hasta ~10 h. Un
único disparador no garantiza que el día tenga sesión, así que hay dos
oportunidades.

Es seguro que convivan:

- el gate usa la misma ventana 08:00–14:00 y el grupo de concurrencia
  `tradingbot-paper-session` (`cancel-in-progress: false`) impide **dos runners
  a la vez**;
- un run que arranque fuera de la ventana se cierra solo
  (`TRADING_SESSION_EXIT_AFTER_WINDOW`);
- el controlador no duplica: antes de despachar comprueba los runs del día y
  los estados activo/en cola.

- Cada ciclo (5 min) comprueba `AutoStartPolicy.is_due()`: día laborable
  (lunes–viernes) y hora local entre las 08:00 y las 13:00. La zona se calcula
  con `zoneinfo`, de modo que el cambio de horario de verano de Cuba se respeta
  sin ajustar nada a mano.
- **Ventana de recuperación:** si el trigger se retrasa o se pierde, el ciclo
  siguiente arranca la sesión dentro de la ventana. Por eso la ventana acaba a
  las 13:00 y no a las 08:05.
- **Idempotencia:** si hoy ya existe un run creado desde la medianoche local
  que no esté `failed`, no se despacha otro.
- **Reintentos:** los runs `failed` sí cuentan como intento fallido y se
  reintentan hasta `MAX_AUTO_ATTEMPTS = 3` por día. Al agotarlos, el
  controlador avisa al administrador y no insiste más.
- Un run `cancelled` **no** se relanza: el operador lo detuvo a propósito.

### Variantes de sesión

| Disparador | `manual_session` | Gate interno | Duración |
| --- | --- | --- | --- |
| Automático (08:00) | `false` | horario 08:00–14:00 `America/Havana` | hasta las 14:00 |
| Manual (`/session_start`) | `true` | activo desde el arranque | 6 h (`timeout-minutes: 360`) |

## Avisos al administrador

- **Controlador**: avisa cuando el arranque automático dispara el workflow y
  cuando un fallo impide hacerlo.
- **Runner**: al arrancar envía el aviso con el estado real, sin adornos:
  - `🟢 Bot PAPER en marcha` cuando la sesión está ACTIVA;
  - `⚠️ Bot PAPER en marcha (sesión detenida)` cuando arrancó fuera de la
    ventana 08:00–14:00 (el propio runner se cerrará al terminar);
  - en todos los casos: horario, modo PAPER, `GITHUB_RUN_ID`, `@usuario` del
    bot y el estado del feed de mercado (`weex conectado (BTCUSDT 1h)` o
    `weex conectando…`).
- **Feed de mercado**: si WEEX aún no está conectado al arrancar, el runner
  espera un máximo de 2 minutos y envía `✅ Feed de mercado conectado` (es la
  prueba real de que el bot puede analizar el mercado) o, si no conecta,
  `⚠️ El feed de mercado no ha conectado` con el tiempo esperado.
- Telegram entrega el mensaje aunque el usuario esté desconectado: queda en
  cola y se ve al volver a abrir el cliente.

## Comandos (solo el chat admin `TELEGRAM_ADMIN_CHAT_ID`)

| Comando | Quién responde | Comportamiento |
| --- | --- | --- |
| `/session` | controlador o runner | Estado real: `🔴 DETENIDA`, `🟡 EN COLA` o `🟢 ACTIVA`. |
| `/session_start` | controlador | Lanza el workflow PAPER si no hay sesión activa ni en cola. |
| `/start` (sin args) | controlador | Alias de `/session_start`. |
| `/session_stop` | controlador o runner | Cancela el workflow PAPER (en cola o activo). |
| `/stop` | controlador o runner | Alias de `/session_stop`. |
| `/ping`, `/help` | controlador o runner | Estado del bot. |
| `/start <chat_id>` | runner | Activar alertas para ese chat (función original conservada). |

## Iniciar / detener

- **Iniciar** (`/session_start` o `/start`): despacha `paper-session.yml` con
  `manual_session=true` y `restore_state=true`. El runner restaura
  `data/bot.db` + `data/users.json`, conecta WEEX y arranca el gate interno.
- **Detener** (`/session_stop` o `/stop`):
  - si la sesión está **en cola**, el controlador llama a
    `POST /repos/{repo}/actions/runs/{id}/cancel`;
  - si está **activa**, el propio runner cancela su ejecución
    (`GITHUB_RUN_ID`) usando `internal/githubactions` y cierra antes el gate
    interno, de modo que ninguna operación nueva se abre aunque la cancelación
    falle.
  - La confirmación se envía solo cuando GitHub aceptó la operación; la
    cancelación es asíncrona y se comprueba con `/session`.

## Persistencia y detención segura

- El estado (SQLite `data/bot.db`, `data/users.json`) se archiva como artefacto
  `tradingbot-state` al terminar/cancelar el runner y se restaura en el
  siguiente arranque. No se borra la base ni se reinicia el balance.
  - **Corregido (bug confirmado en producción):** el paso
    `Restore persisted state` usaba `if: inputs.restore_state != false`. En el
    disparo `schedule` el contexto `inputs` es `null` y la comparación laxa de
    GitHub (`null != false`, ambos a `0`) lo evalúa como **falso**: el paso se
    omitía, la sesión arrancaba con la base vacía y, al archivar, publicaba un
    artefacto vacío que el siguiente arranque sí restauraba (historial y
    balance perdidos). Comprobado en la API pública del repo: en los 8 runs
    `schedule` de `paper-session.yml` el paso aparece `skipped`, mientras que
    en los `workflow_dispatch` aparece `success`. La condición es ahora
    `github.event_name != 'workflow_dispatch' || github.event.inputs.restore_state != 'false'`.
    Los mismos disparos controlan `TRADING_SESSION_START_ACTIVE` y
    `TRADING_SESSION_SCHEDULE_ENABLED`.
- El límite de 6 horas (`timeout-minutes: 360`) y `TRADING_SESSION_END`
  finalizan la sesión automáticamente.
- Solo se cancela el workflow PAPER; nunca al controlador ni a otros workflows.
- No pueden coexistir dos sesiones PAPER: el controlador no despacha si hay
  runs activos o en cola, y el grupo de concurrencia `tradingbot-paper-session`
  evita solapamientos.

## Configuración en GitHub (una sola vez)

1. **Settings → Secrets and variables → Actions → Variables**:
   - `TELEGRAM_ADMIN_CHAT_ID` (obligatoria): chat ID del administrador. Sin
     esta variable el controlador se bloquea por seguridad.
   - `TRADINGBOT_DISPATCH_REF` (opcional): rama desde la que se despacha el
     workflow PAPER. Por defecto `main`.
2. **Secrets**: `TELEGRAM_BOT_TOKEN` (ya existente). Opcional
   `TRADINGBOT_ACTIONS_TOKEN` (PAT con permiso `Actions: read and write` sobre
   el repositorio) si se quiere usar un token dedicado en lugar del integrado.
3. **Variables de entorno del controlador** (editables en el workflow):
   `TRADINGBOT_TIMEZONE` (por defecto `America/Havana`),
   `TRADINGBOT_AUTO_START_HOUR` (`8`) y
   `TRADINGBOT_AUTO_START_DEADLINE_HOUR` (`13`).
4. **Variables del runner**: `TELEGRAM_ADMIN_CHAT_ID` (misma variable de
   Actions) para que el bot pueda avisar de su arranque.

> El workflow se despacha con `ref: main`. Hasta que los cambios no estén
> fusionados en `main`, el runner seguirá ejecutando el código anterior.

## Variables de entorno (resumen)

| Variable | Dónde | Para qué |
| --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | ambos | Token del bot. |
| `TELEGRAM_ADMIN_CHAT_ID` | ambos | Único chat autorizado a controlar. |
| `GH_TOKEN` / `GITHUB_TOKEN` | controlador / runner | Permisos `actions: write`. |
| `TRADINGBOT_TIMEZONE` | controlador | Zona de la sesión y de los avisos. |
| `TRADINGBOT_AUTO_START_HOUR` | controlador | Hora local del arranque automático. |
| `TRADINGBOT_AUTO_START_DEADLINE_HOUR` | controlador | Fin de la ventana de recuperación. |
| `TRADINGBOT_DISPATCH_REF` | controlador | Rama desde la que se despacha. |
| `CONTROLLER_POLL_WINDOW_SECONDS` | controlador | Segundos de escucha continua por ciclo (240 por defecto). |
| `TRADING_SESSION_TIMEZONE` | runner | Zona de la ventana del gate (`America/Havana`). |
| `TRADING_SESSION_START` / `_END` | runner | Ventana de la sesión (08:00 / 14:00). |
| `TRADING_SESSION_EXIT_AFTER_WINDOW` | runner | El runner termina al cerrar la ventana. |

## Tests

```bash
python3 -m unittest discover -s .github/scripts -p "test_*.py"   # controlador
go build ./... && go vet ./... && go test ./...                  # bot y runner
```

El workflow `CI` ejecuta ambos conjuntos (50 casos del controlador y la suite
Go, incluido `internal/telegram`). No requieren tokens reales ni envían
órdenes.

## Limitaciones conocidas

- El controlador y el runner comparten un único token de Telegram. La regla de
  propiedad del polling elimina la carrera habitual, pero en el instante en que
  el runner arranca puede heredar un `409`; el listener lo reintenta con
  backoff. Usar un bot de Telegram exclusivo para el controlador eliminaría
  cualquier carrera residual.
- El runner es efímero: si arranca fuera de la ventana 08:00–14:00 con
  `manual_session=false` se apaga limpiamente. El arranque automático está
  limitado a la ventana 08:00–13:00 para que siempre haya margen.
- GitHub Actions no garantiza la hora exacta de los `schedule`: en este repo
  el cron `*/5` del controlador se ha ejecutado en la práctica cada 4–6 horas
  (huecos de hasta ~10 h). Por eso hay **dos disparadores** (controlador +
  `schedule` del runner) y la ventana de arranque es de 5 horas. Aun así, un
  retraso mayor puede dejar el día sin sesión: es un límite de la
  infraestructura, no del bot.
- Los cron de GitHub Actions se interpretan en **UTC**, salvo que se declare
  `timezone:` (el `schedule` del runner la declara; el controlador no usa cron
  para la sesión diaria: calcula la hora local con `zoneinfo`).
- **Latencia:** con la ventana de escucha de 240 s, un comando que llega
  entre ciclos espera como mucho ~1 minuto más el arranque del runner de
  GitHub Actions (que puede tardar 30–60 s en llegar a `in_progress`).
  No hay respuesta inmediata garantizada: si el ciclo de 5 min se retrasa o
  GitHub no agenda el cron, la latencia sube.
- El `long poll` de Telegram entrega los mensajes aunque el usuario esté
  desconectado, pero si el usuario los reenvía desde otro cliente se genera
  **otro** `update_id`: el comando se ejecutaría dos veces. El controlador es
  idempotente (no duplica sesiones), pero `/session_start` manual se despacha
  una vez y el runner lo confirma.
