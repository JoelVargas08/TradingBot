# Controlador de Telegram ↔ GitHub Actions (sesiones PAPER)

Este documento describe cómo la sesión PAPER de TradingBot **arranca sola** a las
08:00 de Cuba (incluso con el usuario sin conectar a Telegram) y cómo se
controla desde el chat. **Todo el flujo permanece en modo PAPER: ningún comando
habilita trading real.**

## Diagnóstico que motivó el diseño actual (29-10-2026)

Medido con la API pública de Actions del repositorio:

| Disparador | Esperado | Real |
| --- | --- | --- |
| Cron `*/5` del controlador | 288 ejecuciones/día | **2–7 ejecuciones/día** (huecos de 4–7 h) |
| Cron `0 8 * * 1-5` de `paper-session.yml` | 08:00 La Habana | **11:57–13:58** La Habana |
| Sesiones reales (`workflow_dispatch`) | 08:00 | 08:33–11:57 (cuando el controlador llegaba a correr) |

Con esos cron ni el arranque de las 08:00 ni la latencia de los comandos de
Telegram eran posibles. Además se encontró que el controlador nunca
reintentaba un run fallido: comparaba `status == "failed"`, pero GitHub
informa un run terminado como `status=completed` y deja el resultado en
`conclusion` (`failure`, `cancelled`, `success`, …).

## Arquitectura: controlador en relevo continuo

```text
   telegram-controller.yml  (job de ~5h30m; al terminar despacha su sucesor)
   ┌─────────────────────────────────────────────────────────────────────┐
   │ telegram_workflow_controller.py                                     │
   │  · cada 60 s: estado de paper-session.yml (API de Actions)           │
   │  · long poll de Telegram (25 s) si no hay runner PAPER in_progress    │
   │  · lunes-viernes ≥ 08:00 (America/Havana): despacha la sesión         │
   │  · /ping /help /session /session_start(/start) /session_stop(/stop)   │
   └──────────────┬──────────────────────────────────────────────────────┘
                  │ workflow_dispatch (ref = TRADINGBOT_DISPATCH_REF)
                  ▼
   paper-session.yml  (runner PAPER · WEEX · estado persistido)
     main.go → internal/telegram.Listener: dueño de getUpdates mientras corre
```

- **Relevo:** cada job trabaja `CONTROLLER_RUNTIME_SECONDS` (19 800 s) y el
  paso final `Relay to the next controller job` despacha
  `telegram-controller.yml` en `TRADINGBOT_DISPATCH_REF`. El grupo de
  concurrencia `tradingbot-telegram-controller` garantiza **un solo
  controlador** a la vez: un disparo nuevo queda en cola y reemplaza a
  cualquier otro pendiente, por lo que nunca hay dos cadenas.
- **Vigilante:** el cron `*/15` solo reanuda la cadena si se rompe (GitHub lo
  ejecuta pocas veces al día; no se depende de él).
- **Sin bucles rápidos:** si un job falla en segundos, el relevo espera hasta
  completar 10 minutos desde el inicio del job. Un error de configuración
  (variables ausentes, token de Telegram inválido) emite `relay=false` y la
  cadena se detiene hasta que el vigilante la reintente.
- **Apagado:** variable de repositorio `TRADINGBOT_CONTROLLER_RELAY=false`
  (el job en curso termina y no despacha sucesor) o cancelar el run.
- **Diagnóstico:** `workflow_dispatch` con `runtime_seconds=0` (una pasada) y
  `relay=false`.

### Un solo consumidor de `getUpdates` por token

| Estado del workflow PAPER | Dueño de `getUpdates` | Quién responde |
| --- | --- | --- |
| Run `in_progress` | **el runner** (Go) | `/ping`, `/help`, `/session`, `/positions`, `/risk`, … |
| Run `queued`/`requested`/`waiting`/desconocido | **el controlador** | `/session_stop` cancela el run en cola |
| Sin runs | **el controlador** | `/ping`, `/help`, `/session`, `/session_start`, `/session_stop` |

- Mientras el runner está `in_progress` el controlador no hace polling: espera
  y vuelve a consultar el estado cada 60 s. Al terminar el runner retoma
  Telegram.
- Si recibe un `409`, cede, espera 10 s y refresca el estado.
- **Comandos de la sesión anterior:** el runner puede atender un comando sin
  llegar a confirmar el offset antes de terminar. El controlador ignora (y
  confirma) los mensajes enviados antes del fin del último run PAPER, de modo
  que un `/stop` o `/start` atendido por el runner nunca se ejecuta dos veces.
- **Comandos caducados:** `/start` y `/stop` con más de 15 minutos no se
  ejecutan; se responde que han caducado.
- Un estado de run desconocido cuenta como "en cola": nunca se interpreta como
  "no hay sesión".

## Arranque automático (08:00 America/Havana)

- `AutoStartPolicy.is_due()`: lunes a viernes, `08:00 ≤ hora local < 13:00`,
  calculado con `zoneinfo` (respeta el horario de verano de Cuba). El
  controlador lo evalúa en cada vuelta (≤ 25 s), así que el despacho ocurre en
  menos de un minuto tras las 08:00.
- **Idempotencia:** no se despacha si hay un run activo o en cola, ni si hoy ya
  hubo una sesión que llegó a la ventana (`conclusion` `success`, `cancelled`,
  …). Un run que terminó antes de las 08:00 (p. ej. una prueba nocturna) no
  bloquea el arranque.
- **Reintentos:** `conclusion` `failure`, `timed_out` o `startup_failure` se
  reintentan hasta `MAX_AUTO_ATTEMPTS = 3` por día. Al agotarlos se avisa una
  vez al administrador.
- **Parada del operador:** un run `cancelled` no se relanza.
- **¿Por qué hasta las 13:00?** Es la ventana de recuperación de reintentos;
  una sesión que arrancase después tendría menos de una hora útil.
- **Confirmación honesta:** tras despachar, el controlador busca el run creado
  (4 consultas, 5 s). El aviso incluye el `run_id` si GitHub lo confirma o
  dice "pendiente de confirmación" si no.

### Variantes de sesión

| Disparador | `manual_session` | Gate interno | Duración |
| --- | --- | --- | --- |
| Automático (08:00) | `false` | horario 08:00–14:00 `America/Havana` | hasta las 14:00 |
| Manual (`/session_start`) | `true` | activo desde el arranque | hasta el final del job (~5 h 56 min) |

El job PAPER tiene el límite duro de 6 h de los runners alojados. El bot recibe
`SIGTERM` 4 minutos antes (paso `Run paper session`) para cerrar limpio y dejar
tiempo al archivado del estado.

## Comandos

En el **controlador** solo atiende `TELEGRAM_ADMIN_CHAT_ID`. En el **runner**,
los comandos de control (`/session_start`, `/start` sin argumento,
`/session_stop`, `/stop`, `/strategy`, `/symbol`, `/timeframe`, `/activate`,
`/backtest`, `/learn*`, `/validate*`, `/ibdiag` y los PDF) también están
reservados al chat administrador; `/start <chat_id>` solo puede suscribir el
propio chat a las alertas.

| Comando | Quién responde | Comportamiento |
| --- | --- | --- |
| `/session` | controlador o runner | Controlador: último run, próximo arranque. Runner: estado, estrategia, feed y última vela por timeframe. |
| `/session_start`, `/start` | controlador | Despacha PAPER si no hay sesión activa ni en cola; responde con el `run_id` o "pendiente de confirmación". |
| `/session_stop`, `/stop` | controlador o runner | Cancela el run; responde "Parada confirmada" o "Cancelación solicitada (asíncrona)". |
| `/ibdiag` | runner | Sin argumentos usa la estrategia Investing Bulls de la sesión. |
| `/ping`, `/help` | ambos | Estado del bot. |

## Persistencia

- El estado (`data/bot.db`, `data/users.json`, logs ML) se archiva como
  `tradingbot-state` al terminar, fallar o cancelar el runner, y se restaura al
  siguiente arranque (búsqueda por nombre del artefacto).
- La restauración exige que el `.tgz` contenga `data/bot.db` y que pase
  `PRAGMA integrity_check`; si un artefacto existente no se puede restaurar, la
  sesión no arranca (nunca se opera con la cuenta reiniciada en silencio).
- Antes de subir, la base se verifica: una base corrupta se sube como
  `tradingbot-state-corrupt` y no sustituye al último estado bueno.
- El resumen del job muestra el run restaurado, si se guardó el estado, velas
  por timeframe, estrategias y operaciones PAPER cerradas por estrategia.

## Configuración en GitHub

| Tipo | Nombre | Uso |
| --- | --- | --- |
| Secret | `TELEGRAM_BOT_TOKEN` | Token del bot (obligatorio). |
| Secret | `TRADINGBOT_ACTIONS_TOKEN` | Opcional. El `GITHUB_TOKEN` con `actions: write` basta para listar, despachar y cancelar; usarlo solo si una política de la organización lo exige. |
| Variable | `TELEGRAM_ADMIN_CHAT_ID` | Chat ID numérico del administrador (obligatoria: sin ella el controlador se bloquea). |
| Variable | `TRADINGBOT_DISPATCH_REF` | Rama desplegada (por defecto `main`). La usan el despacho PAPER y el relevo. |
| Variable | `INVESTING_BULLS_STRATEGY_ID` | Opcional. Vacía = alias estable `investing-bulls-mtf`. Un ID explícito se valida y, si no es válido, IB no arranca. |
| Variable | `TRADINGBOT_CONTROLLER_RELAY` | `false` apaga la cadena del controlador. |

> **Coste:** el relevo mantiene un runner ocupado todo el día. En un
> repositorio público los minutos de runners estándar son gratuitos; si el
> repositorio pasa a privado, ~44 000 min/mes superan la cuota gratuita y la
> alternativa es un servidor propio (`systemd/`, `docker-compose.yml`).

## Tests

```bash
python3 -m unittest discover -s .github/scripts -p "test_*.py"   # controlador + estado
go build ./... && go vet ./... && go test ./...                  # bot y runner
```

Cubren las horas 07:59/08:00/12:59/13:00, fines de semana, horario de verano,
duplicados con runs en cola/activos, reintentos de `failure`/`timed_out`/
`startup_failure` con tope de 3, no relanzar `cancelled`, chats no
autorizados, parada con run activo/en cola/sin run, errores HTTP
401/403/404/409/5xx y timeouts, rama de despacho, cesión del polling al
runner, comandos de la sesión anterior y caducados, y el bucle de relevo.

## Limitaciones conocidas

- El controlador y el runner comparten el token de Telegram. En el instante en
  que el runner empieza a hacer polling puede haber un `409`; ambos lo
  gestionan (el runner reintenta con backoff, el controlador cede).
- Entre dos jobs de la cadena hay un hueco de ~30–60 s sin polling; Telegram
  conserva los mensajes y se atienden al arrancar el sucesor.
- Si GitHub Actions sufre una caída, la cadena se reanuda con el vigilante.
