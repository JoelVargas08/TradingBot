# TradingBot

Bot de trading **PAPER** (simulado) para BTCUSDT con datos de mercado de WEEX,
estrategia Investing Bulls multi-timeframe (1h → 15m → 5m), control por
Telegram y sesiones diarias en GitHub Actions.

> **Seguridad:** todo el flujo es PAPER. `MODE=live` aborta el arranque y los
> workflows verifican `TRADING_MODE=paper`. Ningún comando, workflow ni
> resultado de validación (incluidas las 100 operaciones PAPER) habilita
> trading real; cualquier paso a dinero real exige una revisión independiente
> y autorización manual explícita.

## Mapa de workflows

### Operación diaria

| Workflow | Responsabilidad | Disparadores |
| --- | --- | --- |
| `telegram-controller.yml` — *TradingBot - Telegram Controller* | Plano de control continuo (relevo de ~5h30m por job): arranca la sesión PAPER a las 08:00 de La Habana (L-V), atiende `/ping`, `/help`, `/session`, `/start`, `/stop` cuando no hay runner, reintenta fallos (máx. 3/día). | Relevo propio (`workflow_dispatch`); cron `*/15` solo como vigilante |
| `paper-session.yml` — *TradingBot - Paper Session* | Única vía de ejecución PAPER: restaura y verifica el estado, tests + build, WEEX, sesión 08:00–14:00, archiva el estado verificado y resume el job. | `workflow_dispatch` (controlador, Telegram o manual) |

### Calidad

| Workflow | Responsabilidad | Disparadores |
| --- | --- | --- |
| `ci.yml` — *CI* | `go build`, `go vet`, `go test`; tests del controlador y del estado; tests ML. | `push` a `main`, PR, manual |

### Laboratorio ML (siempre manual)

| Workflow | Responsabilidad |
| --- | --- |
| `ml-history.yml` | Descarga histórico WEEX paginado y lo publica como artefacto/CSV. |
| `ml-train.yml` | Entrena un candidato MLP con límites de RAM/tiempo, walk-forward, baselines y holdout reservado; nunca activa el modelo. |
| `ml-promote.yml` | Promoción manual con `confirm=yes` y puerta de métricas; deja un informe auditable. |

Detalle del controlador, comandos y persistencia:
[docs/TELEGRAM_CONTROLLER.md](docs/TELEGRAM_CONTROLLER.md).

## Configuración de GitHub

| Tipo | Nombre | Obligatoria | Uso |
| --- | --- | --- | --- |
| Secret | `TELEGRAM_BOT_TOKEN` | sí | Token del bot de Telegram. |
| Secret | `TRADINGBOT_ACTIONS_TOKEN` | no | Solo si una política impide usar `GITHUB_TOKEN` (`actions: write`). |
| Variable | `TELEGRAM_ADMIN_CHAT_ID` | sí | Chat ID numérico; único chat que controla la sesión. |
| Variable | `TRADINGBOT_DISPATCH_REF` | no | Rama desplegada (por defecto `main`). |
| Variable | `INVESTING_BULLS_STRATEGY_ID` | no | Vacía = alias estable `investing-bulls-mtf`. Un ID explícito se valida y, si no es válido, Investing Bulls no arranca. |
| Variable | `TRADINGBOT_CONTROLLER_RELAY` | no | `false` apaga la cadena del controlador. |

Nunca se suben `.env`, tokens, claves WEEX ni bases locales al repositorio.

## Selección de estrategia

- `INVESTING_BULLS_STRATEGY_ID=investing-bulls-mtf` (alias, por defecto en
  `paper-session.yml`): elige la estrategia Investing Bulls MTF `active` más
  reciente de BTCUSDT cuyos timeframes coinciden con 1h/15m/5m y que el feed de
  WEEX entrega. Las demás se listan como descartadas con su motivo.
- Un ID explícito debe existir, estar `active`, ser de la fuente
  `investing_bulls_learn_mtf`, del símbolo y timeframes configurados.
- Si no se resuelve ninguna, Investing Bulls **no** arranca y **nada la
  sustituye**: el motor compartido corre con `LIVE_STRATEGY=none`. Chandelier
  solo opera con `LIVE_STRATEGY=chandelier` explícito.
- El aviso de arranque en Telegram, el log y `/session` muestran la estrategia
  elegida (ID, fuente, símbolo, timeframes) o el motivo del rechazo.

## Operación

1. **Arranque diario:** automático a las 08:00 (America/Havana), lunes a
   viernes. El runner avisa por Telegram con el run, la estrategia y el estado
   del feed WEEX.
2. **Consultar:** `/session` (sin runner: último run y próximo arranque; con
   runner: estrategia, feed, última vela por timeframe y salud del feed).
3. **Arrancar a mano:** `/session_start` (o `/start`). Sesión activa desde el
   arranque hasta el final del job.
4. **Detener:** `/session_stop` (o `/stop`). Cierra el gate de entradas y
   cancela el run; las posiciones abiertas no se cierran automáticamente.
5. **Diagnóstico:** `/ibdiag` (sin argumentos usa la estrategia de la sesión).
6. **Resumen de cada sesión:** pestaña *Summary* del run de *Paper Session*
   (estado restaurado/guardado, velas, estrategias, operaciones cerradas).

## Desarrollo local

```bash
go build ./... && go vet ./... && go test ./...
python3 -m unittest discover -s .github/scripts -p "test_*.py" -v
python3 -m pip install -r ml/requirements-train.txt -r ml/requirements-inference.txt
python3 -m unittest discover -s tests/ml -p "test_*.py" -v
```

Go 1.26.2. En Windows, los tests del controlador necesitan el paquete
`tzdata` de Python para `America/Havana`.

## Documentación

- [docs/TELEGRAM_CONTROLLER.md](docs/TELEGRAM_CONTROLLER.md) — controlador, comandos, persistencia.
- [docs/INVESTING_BULLS_STRATEGY.md](docs/INVESTING_BULLS_STRATEGY.md) — especificación canónica R01–R26.
- [PLAN_EJECUCION_RED_NEURONAL.md](PLAN_EJECUCION_RED_NEURONAL.md) — red neuronal.
- [PROGRESO.md](PROGRESO.md) — progreso por fases.
- [DEPLOYMENT.md](DEPLOYMENT.md) — despliegue fuera de GitHub Actions.
