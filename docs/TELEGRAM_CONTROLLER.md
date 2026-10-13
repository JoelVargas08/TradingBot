# Controlador de Telegram ↔ GitHub Actions (sesiones PAPER)

Este documento describe cómo iniciar, consultar y detener la sesión PAPER de
TradingBot desde Telegram sin depender de que el ordenador personal esté
encendido. **Todo el flujo permanece en modo PAPER: ningún comando habilita
trading real.**

## Arquitectura

```text
Telegram (chat admin)
        │
        ▼
.github/workflows/telegram-controller.yml   (cada 5 min / manual)
        │  .github/scripts/telegram_workflow_controller.py
        │
        ├─ si hay un run PAPER in_progress → NO consume Telegram (el runner es el dueño del polling)
        ├─ si no hay run            → responde /session y arranca con /start | /session_start
        └─ si hay un run en cola     → informa "EN COLA" y cancela con /stop | /session_stop
        │
        ▼
.github/workflows/paper-session.yml         (runner PAPER, WEEX, estado persistido)
        │  el propio bot procesa Telegram mientras está activo
        └─ /session_stop o /stop cancela su propia ejecución (GITHUB_TOKEN)
```

El traspaso de `getUpdates` es estricto: mientras el runner PAPER está
`in_progress`, el controlador no llama a `getUpdates`; cuando el runner no
existe, solo el controlador consume Telegram para comandos de ciclo de vida.

## Causa raíz del error 401

El workflow usaba, de forma literal en el YAML:

```yaml
GH_TOKEN: \${{ secrets.GITHUB_TOKEN }}
```

La barra invertida no escapa expresiones en GitHub Actions: el valor de
`GH_TOKEN` quedaba como `\<token>`, por lo que el encabezado
`Authorization: Bearer \<token>` era inválido y la API respondía **401**.
El mismo defecto afectaba a `GITHUB_REPOSITORY` y, en `paper-session.yml`, a
`TRADING_SESSION_START_ACTIVE` (una sesión arrancada desde Telegram no abría el
gate interno). Se eliminaron las barras invertidas.

`GITHUB_TOKEN` integrado **sí** autentica los endpoints usados
(`listar runs`, `dispatches`, `cancel`) gracias a `permissions: actions: write`.
No es obligatorio un PAT. Opcionalmente puede definirse el secreto
`TRADINGBOT_ACTIONS_TOKEN` y el workflow lo preferirá.

## Comandos (solo el chat admin `TELEGRAM_ADMIN_CHAT_ID`)

| Comando | Comportamiento |
| --- | --- |
| `/session` | Estado real de GitHub Actions: `🔴 DETENIDO`, `🟡 EN COLA` o `🟢 ACTIVO`. |
| `/session_start` | Lanza el workflow PAPER si no hay sesión activa ni en cola. |
| `/start` (sin args) | Alias de `/session_start`. |
| `/session_stop` | Cancela el workflow PAPER (en cola o activo). |
| `/stop` | Alias de `/session_stop`. |
| `/start <chat_id>` | Activar alertas para ese chat (función original conservada). |

Otros comandos del bot (`/ping`, `/positions`, `/risk`, `/performance`, etc.)
siguen operando igual dentro del runner.

## Iniciar / detener

- **Iniciar** (`/session_start` o `/start`): despacha `paper-session.yml` con
  `manual_session=true` y `restore_state=true`. El runner restaura
  `data/bot.db` + `data/users.json`, conecta WEEX y arranca el gate interno.
- **Detener** (`/session_stop` o `/stop`):
  - si la sesión está **en cola**, el controlador llama a
    `POST /repos/{repo}/actions/runs/{id}/cancel`;
  - si está **activa**, el propio runner cancela su ejecución
    (`GITHUB_RUN_ID`) usando `internal/githubactions`.
  - La confirmación se envía solo cuando GitHub aceptó la operación; la
    cancelación es asíncrona y se comprueba con `/session`.

## Persistencia y detención segura

- El estado (SQLite `data/bot.db`, `data/users.json`) se archiva como artefacto
  `tradingbot-state` al terminar/cancelar el runner y se restaura en el
  siguiente arranque. No se borra la base ni se reinicia el balance.
- El límite de 6 horas (`timeout-minutes: 360`) y `TRADING_SESSION_END`
  finalizan la sesión automáticamente.
- Solo se cancela el workflow PAPER; nunca al controlador ni a otros workflows.
- No pueden coexistir dos sesiones PAPER: el controlador no despacha si hay
  runs activos o en cola, y el grupo de concurrencia `tradingbot-paper-session`
  evita solapamientos.

## Configuración manual en GitHub (una sola vez)

1. **Settings → Secrets and variables → Actions → Variables**, crear
   `TELEGRAM_ADMIN_CHAT_ID` con el chat ID del administrador. Sin esta variable
   el controlador se bloquea por seguridad.
2. **Secrets**: `TELEGRAM_BOT_TOKEN` (ya existente). Opcional
   `TRADINGBOT_ACTIONS_TOKEN` (PAT con permiso `Actions: read and write` sobre
   el repositorio) si se quiere usar un token dedicado en lugar del integrado.
3. Asegurar que **Actions** está habilitado y que el workflow
   `TradingBot - Telegram Workflow Controller` puede ejecutarse (schedule y
   `workflow_dispatch`).

## Tests

```bash
python -m unittest discover -s .github/scripts -p "test_*.py"   # controlador
go build ./... && go vet ./... && go test ./...                  # bot y runner
```

El workflow `CI` ejecuta ambos conjuntos. No requieren tokens reales ni envían
órdenes.

## Limitaciones conocidas

- El controlador y el bot comparten un único token de Telegram. Si ambos
  hicieran polling a la vez habría conflicto; por diseño el controlador cede
  cuando detecta un runner activo o un `409 Conflict`. Usar un bot de Telegram
  exclusivo para el controlador eliminaría cualquier carrera residual.
- Para reconocer un comando, `getUpdates` con `offset` confirma también los
  updates anteriores (semántica de Telegram). El controlador confirma
  exactamente hasta el comando atendido y no avanza más, de modo que los
  updates posteriores quedan disponibles para el runner.
