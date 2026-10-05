# TradingBot — Deployment

## Arquitectura recomendada

Servidor Linux 24/7:

```text
/opt/tradingbot/
├── código (Git)
├── bot
├── .env              # solo servidor
└── data/
    ├── bot.db
    └── users.json
```

La fase actual debe operar en PAPER con datos públicos de WEEX. Las credenciales privadas de WEEX no son necesarias hasta autorizar LIVE.

## Requisitos

- Linux 64-bit, preferentemente Ubuntu/Debian.
- Git.
- Go 1.26.x; el repositorio declara Go 1.26.2.
- systemd.
- Salida HTTPS/WSS hacia WEEX y Telegram.
- Docker es opcional; el repositorio ya incluye Dockerfile y docker-compose.yml.

## Primera instalación

```bash
sudo mkdir -p /opt/tradingbot
sudo chown "$USER":"$USER" /opt/tradingbot
cd /opt
git clone https://github.com/JoelVargas08/TradingBot.git tradingbot
cd /opt/tradingbot
go version
go mod download
go test ./...
go build -trimpath -ldflags="-s -w" -o bot
mkdir -p data
chmod 700 data
cp .env.example .env
chmod 600 .env
nano .env
```

Nunca subir `.env` a GitHub.

## PAPER + WEEX

Configuración base:

```dotenv
TRADING_MODE=paper
MODE=paper
MARKET_DATA_PROVIDER=weex
TRADING_SYMBOL=BTCUSDT
TRADING_TIMEFRAME=1h
WEEX_PRICE_TYPE=LAST_PRICE
WEEX_BACKFILL_BARS=5000
INGEST_ENABLED=false
INVESTING_BULLS_MAIN_TIMEFRAME=1h
INVESTING_BULLS_ENTRY_TIMEFRAME=15m
INVESTING_BULLS_CONFIRM_TIMEFRAME=5m
INVESTING_BULLS_CONFIRM_5M=true
RISK_ENABLED=true
RISK_PCT=0.01
RISK_MIN_RR=2.0
RISK_MAX_OPEN_POSITIONS=3
RISK_KILL_SWITCH_PCT=0.15
RISK_STARTING_BALANCE=10000
DB_FILE=data/bot.db
STORAGE_FILE=data/users.json
PORT=8080
```

`TELEGRAM_BOT_TOKEN` es obligatorio para el arranque actual. Tras obtener el ID de estrategia aprendida, configurar `INVESTING_BULLS_STRATEGY_ID`.

## Telegram

El bot usa long polling; no necesita webhook de Telegram. `WEBHOOK_SECRET` solo es necesario para `POST /webhook` externo.

## GitHub Actions — PAPER por sesión

También existe `.github/workflows/paper-session.yml` para ejecutar una sesión de PAPER sobre WEEX en un runner efímero.

El diseño actual:

```text
GitHub Actions
    ↓
restaura tradingbot-state
    ↓
TradingBot + WEEX público
    ↓
08:00–14:00 America/New_York
    ↓
PaperBroker + SQLite
    ↓
shutdown limpio + checkpoint WAL
    ↓
sube tradingbot-state
```

El workflow usa una programación con zona horaria de Nueva York y evita ejecuciones simultáneas mediante `concurrency`. GitHub permite horarios con zona IANA; los runners hospedados tienen un límite de 6 horas por job.

### Estado persistente

Como el runner se destruye al terminar, `data/bot.db` y `data/users.json` se empaquetan como el artefacto `tradingbot-state` y se restauran en la siguiente ejecución. GitHub permite descargar artefactos de ejecuciones anteriores mediante un token y el identificador de ejecución; los artefactos tienen retención configurable.

Esto sirve para la fase de PAPER, pero **no debe considerarse una base de datos de producción**. Si el objetivo es LIVE con dinero real, el servidor persistente con SQLite local sigue siendo la opción preferida.

### Preparación

1. En **Settings → Secrets and variables → Actions**, crear el secret `TELEGRAM_BOT_TOKEN`.
2. Crear la variable de repositorio `INVESTING_BULLS_STRATEGY_ID` con el ID de la estrategia MTF validada/promovida a ACTIVE.
3. Mantener PAPER: `TRADING_MODE=paper`.
4. Ejecutar primero el workflow manualmente desde **Actions → TradingBot - Paper Session → Run workflow** para comprobar restauración, WEEX, sesión y persistencia. Los workflows con `workflow_dispatch` pueden lanzarse manualmente desde Actions.

**Importante:** 08:00–14:00 es una configuración operativa inicial para esta prueba de 6 horas; no es un horario que el PDF de Investing Bulls establezca como regla. Debemos medir los resultados antes de fijarlo como horario definitivo.

## LIVE WEEX

Solo después de la validación y autorización explícita:

```dotenv
TRADING_MODE=live
WEEX_API_KEY=...
WEEX_API_SECRET=...
WEEX_TESTNET=false
LIVE_TRADING_CONFIRM=true
```

Nunca guardar credenciales privadas en GitHub, README, issues o logs.

## systemd

Se incluye `systemd/tradingbot.service.example`.

```bash
sudo useradd --system --home /opt/tradingbot --shell /usr/sbin/nologin tradingbot || true
sudo chown -R tradingbot:tradingbot /opt/tradingbot
sudo cp systemd/tradingbot.service.example /etc/systemd/system/tradingbot.service
sudo systemctl daemon-reload
sudo systemctl enable tradingbot
sudo systemctl start tradingbot
```

Comprobar:

```bash
sudo systemctl status tradingbot
sudo journalctl -u tradingbot -f
curl http://127.0.0.1:8080/health
```

Si no se usa `POST /webhook`, no es necesario exponer 8080 públicamente.

## Actualización

Flujo: PC → `git push` → GitHub → servidor → `scripts/update.sh`.

```bash
cd /opt/tradingbot
./scripts/update.sh
```

El script comprueba cambios locales, hace `git pull --ff-only`, ejecuta `go test ./...`, compila antes de detener el servicio, conserva `bot.previous`, reinicia y verifica `/health`. `.env` y `data/` no se reemplazan.

## Reinicio

```bash
sudo systemctl restart tradingbot
sudo systemctl status tradingbot --no-pager
sudo journalctl -u tradingbot -n 100 --no-pager
```

Después de cambiar solo `.env`, basta reiniciar.

## Rollback

```bash
sudo systemctl stop tradingbot
sudo cp /opt/tradingbot/bot.previous /opt/tradingbot/bot
sudo systemctl start tradingbot
```

## Persistencia

No borrar `data/` durante una actualización. Antes de LIVE:

```bash
mkdir -p backups
cp data/bot.db "backups/bot.db.$(date +%Y%m%d-%H%M%S)"
cp data/users.json "backups/users.json.$(date +%Y%m%d-%H%M%S)" 2>/dev/null || true
```

## Desarrollo mientras corre en servidor

Sí. Desarrolla en el PC, prueba, haz `git commit`, `git push` y ejecuta `scripts/update.sh` en el servidor. No modificar manualmente el código de producción salvo emergencia.

## Gate PAPER → LIVE

Mantener `TRADING_MODE=paper` durante la validación. Evaluar al menos 100 operaciones: win rate, PnL, profit factor, drawdown, setups y salud del feed. El >75% de win rate es un criterio definido por el usuario, no una garantía. LIVE requiere activación explícita y controles de riesgo.
