# MEMORIA CONTROLADOR TELEGRAM - Arranque automático de la sesión PAPER (2026-10-21)

Estado: **implementado y validado en la rama `fix/telegram-controller`**. Pendiente
de despliegue: fusionar en `main` y crear la variable de Actions
`TELEGRAM_ADMIN_CHAT_ID`.

---

## 1. El encargo y la causa raíz real

El encargo: que la sesión PAPER **arranque sola a las 08:00 (America/Havana)**
aunque el usuario no esté conectado a Telegram, y que el bot avise por
Telegram que ya está trabajando. Todo en modo PAPER.

La primera hipótesis (timeouts de red) era **falsa**: `tgbotapi.NewBotAPI` usa
`&http.Client{}` sin timeout y `GetUpdatesChan` reintenta cada 3 s sin morir.
La causa raíz eran varias, todas estructurales:

1. **`\${{ ... }}` literal en el YAML de `main`** → `GH_TOKEN` inválido → **401**
   de la API de GitHub. Afectaba a `telegram-controller.yml` y a
   `paper-session.yml`.
2. **El controlador no implementaba `/ping` ni `/help`** y solo consume Telegram
   cuando no hay runner: sin sesión activa nadie respondía nada.
3. **`confirm(offset=update_id+1)`** descartaba updates pendientes no
   gestionados (se confirmaban comandos ajenos al controlador).
4. **Dos consumidores de `getUpdates`**: el controlador seguía consultando
   Telegram con el run PAPER *en cola*; solo `in_progress` cedía el polling →
   carreras `409` y Updates perdidos.
5. **El runner nunca notificaba su arranque** y `TELEGRAM_ADMIN_CHAT_ID` no
   llegaba a `paper-session.yml`.
6. **`HandleSessionStart` respondía siempre "workflow ya activo"**, porque
   `workflow.Available()` solo comprueba variables de GitHub, no el gate de
   sesión. Y `HandleSessionStop` no detenía el gate interno.
7. **Zona horaria `America/New_York`** en lugar de `America/Havana`.
8. **`cancel-in-progress: true`** en el controlador: podía cortar un ciclo
   entre el despacho del workflow y la notificación al usuario.

## 2. Decisiones de diseño

### 2.1 Un único dispatcher

Se **eliminó `on.schedule` de `paper-session.yml`**: el controlador pasa a ser
el único que despacha el workflow PAPER. Con `schedule` en el runner, el grupo
de concurrencia `tradingbot-paper-session` (`cancel-in-progress: false`) no
impedía una segunda sesión PAPER: la dejaba **en cola**, con dos runners
vivos y el doble de exposición simulada. Con un solo dispatcher esa situación
es imposible por construcción.

### 2.2 Propiedad del polling por estado

| Estado | Dueño de `getUpdates` |
| --- | --- |
| Run `in_progress` | el runner (Go) |
| Run `queued` | el controlador (Python) |
| Sin runs | el controlador (Python) |

El caso `queued` es la clave: el runner todavía no existe, así que nadie
contesta; el controlador atiende `/session_stop` y cancela la ejecución en cola
por la API de GitHub. En cuanto el runner entra en `in_progress`, el
controlador cede el token.

### 2.3 Ventana de arranque y zona horaria

`AutoStartPolicy` calcula la hora local con `zoneinfo` (`America/Havana`), de
modo que el DST cubano se respeta sinIntervention manual. La ventana es
**08:00–13:00** (no 08:00–08:05) para que un trigger retrasado —GitHub no
garantiza la hora exacta— todavía encuentre la sesión dentro del rango, y para
que el runner siempre arranque antes de las 14:00 (fin de su propia ventana).

Idempotencia: si hoy ya existe un run creado desde la medianoche local que no
esté `failed`, no se despacha otro. Los `failed` sí se reintentan (máximo 3 por
día) y los `cancelled` **no** se relanzan: el operador los paró a propósito.

## 3. Cambios implementados

### 3.1 Controlador (`.github/scripts/telegram_workflow_controller.py`)

Reescrito como una clase `Controller` con dependencias inyectables
(`GitHubClient`, `TelegramClient`, `AutoStartPolicy`, reloj). Piezas nuevas:
`PaperState`, `GitHubError`, `TelegramConflictError`/`TelegramAuthError`/
`TelegramServerError`/`TelegramNetworkError`, `_auto_start()`, `_status_text()`
y `_poll()` con reintentos acotados. Los secretos nunca se registran y los
cuerpos de error se sanean. `TRADINGBOT_DISPATCH_REF` permite despachar desde
otra rama.

### 3.2 Runner Go

- **`internal/telegram/listener.go`** (nuevo): listener de long polling con
  `Message`, `Fetcher`, `Handler`, clasificación de errores de Telegram
  (409/401/429/5xx/red) y backoff exponencial con jitter. **El offset solo
  avanza cuando el mensaje se ha gestionado**: un comando fallido se reintenta
  y no se pierde; tras `MaxAttempts` se descarta para no bloquear la cola.
- **`internal/session.Manager.RemainingWindow()`**: tiempo restante de la
  ventana, para que el bot informe de cuánto queda.
- **`handlers/commands.go`**: `HandleSession`/`HandleSessionStart`/
  `HandleSessionStop` reescritos. El estado lo gobierna el gate interno
  (`session.IsActive()`), no `workflow.Available()`; `/session_stop` cierra el
  gate **antes** de cancelar el workflow.
- **`telegram_dispatch.go`** (nuevo): el `switch` de comandos sale de `main.go`
  a un despachador propio, testeable y desacoplado.
- **`telegram_notify.go`** (nuevo): `🟢 Bot PAPER en marcha` al arrancar y
  `🔴 Bot PAPER detenido` al terminar, enviados a `TELEGRAM_ADMIN_CHAT_ID`.
- **`config`**: `TelegramAdminChatID` (validado: un chat id corrupto es error,
  no un silencio) y zona por defecto `America/Havana`.
- **`main.go`**: `NewBotAPIWithClient` con **timeout HTTP de 90 s** y long poll
  de 50 s; el listener sustituye a `GetUpdatesChan`.

### 3.3 Workflows

- `telegram-controller.yml`: `ubuntu-latest` (ubuntu-slim puede no existir en
  repos privados), `cancel-in-progress: false`, `timeout-minutes: 6` (el job
  escucha 240 s y debe terminar antes del siguiente ciclo de 5 min), tests
  solo en `workflow_dispatch` y variables de zona/ventana.
- `paper-session.yml`: sin `schedule`, `manual_session` gobierna el gate
  (horario 08:00–14:00 o sesión manual de 6 h), `America/Havana` y
  `TELEGRAM_ADMIN_CHAT_ID`.

### 3.4 Merge con `main` y ventana de escucha

`main` había avanzado con tres commits del 15 de octubre (PR #21 y dos
commits posteriores) que tocaban el mismo terreno:

| Commit de `main` | Qué aportaba | Resolución |
| --- | --- | --- |
| `a0173a0` | Escucha continua en reposo (`CONTROLLER_POLL_WINDOW_SECONDS`) | **Adoptado**: el controlador ahora escucha 240 s por ciclo |
| `497cdbd` | `on.schedule` a las 08:00 Cuba en `paper-session.yml`, con la sesión **detenida** hasta `/session_start` | **Descartado**: el arranque diario lo hace el controlador y la sesión **opera** sola |
| `67eed89` | `/help` con el workflow en reposo | **Ya cubierto**: el controlador responde `/help` sin runner |

Motivo de descartar el `schedule` del runner en el merge: con él el workflow
arrancaba a las 08:00 pero el motor de trading quedaba **detenido** hasta que
el usuario mandaba `/session_start`, lo que contradice el encargo (la sesión
arranca sola, trabaja sola y avisa).

La ventana de escucha sí se adopta porque mejora la latencia sin tocar la
arquitectura: el job pasa de terminar en ~10 s a escuchar 240 s, con lo que un
comando espera como mucho ~1 minuto en vez de hasta 5. El ciclo sigue
terminando antes si atiende un comando, si el runner toma el token (409) o si
GitHub cancela el workflow.

### 3.5 Hallazgo de campo: GitHub Actions ejecuta los cron con retraso de horas

Al revisar las ejecuciones reales del repositorio (72 runs del controlador
entre el 8 y el 21 de octubre) se midió que el cron `*/5` **no** se ejecutaba
cada 5 minutos: los huecos reales fueron de 76 a 587 minutos (media ~4–5 h).
Los `schedule` de las sesiones PAPER tampoco arrancaban a las 08:00: se
observaron inicios entre las 12:02 y las 13:58 de La Habana.

Consecuencia directa: **un solo disparador no garantiza que el día tenga
sesión**. Por eso se mantendrán **dos disparadores con la misma semántica**
(el controlador y el `schedule` del runner), con la condición de que el
segundo **opere** la sesión automáticamente (la variante de `main` la dejaba
detenida hasta `/session_start`).

Es seguro porque:

- el grupo de concurrencia `tradingbot-paper-session` con
  `cancel-in-progress: false` impide dos runners simultáneos;
- un run que arranque fuera de la ventana 08:00–14:00 se cierra solo
  (`TRADING_SESSION_EXIT_AFTER_WINDOW`);
- el controlador no duplica: antes de despachar mira los runs del día y los
  estados activo/en cola.

El último fallo real del controlador en `main` (15:38 UTC del 21/10) fue
`controller error: The read operation timed out`: el código anterior hacía
long poll de 25 s con timeout HTTP de 20 s. La versión nueva usa long poll de
15 s con timeout HTTP de 25 s, y la ventana de escucha añade hasta 240 s de
long poll por ciclo.

## 4. Validación

- `python3 -m unittest discover -s .github/scripts -p "test_*.py"` → **50
  casos OK** (arranque a las 08:00, DST, fin de semana, idempotencia,
  reintentos, 409/401/red, ownership del polling, `/ping`, `/help`, ventana de
  escucha).
- `go build ./... && go vet ./... && go test ./...` → **OK** (incluye
  `internal/telegram` y los handlers reescritos).

## 5. Pendiente de despliegue

1. Fusionar la rama en `main`: el controlador despacha con `ref: main`, así
   que hasta la fusión el runner ejecuta el código anterior.
2. Crear la variable de Actions `TELEGRAM_ADMIN_CHAT_ID` (obligatoria; sin ella
   el controlador se bloquea por seguridad).
3. Opcional: `TRADINGBOT_DISPATCH_REF` si se quiere despachar desde otra rama.
4. Comprobar en la primera sesión que llegan los dos avisos (controlador:
   "inicio automático"; runner: "Bot PAPER en marcha").

## 6. Límites reales que conviene recordar

- GitHub Actions no garantiza la hora exacta del `schedule`; la ventana
  08:00–13:00 lo absorbe, no lo elimina.
- Un solo `getUpdates` por token: la regla de propiedad lo resuelve, pero en el
  instante en que arranca el runner puede aparecer un `409`; el listener lo
  reintenta con backoff. Un bot exclusivo para el controlador lo elimina.
- Los cron de GitHub Actions se interpretan en **UTC**: el horario de la
  sesión diaria no se implementa con cron, se calcula con `zoneinfo`.
