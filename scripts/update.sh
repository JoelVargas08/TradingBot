#!/usr/bin/env bash
set -euo pipefail
APP_DIR="${APP_DIR:-/opt/tradingbot}"
SERVICE_NAME="${SERVICE_NAME:-tradingbot}"
cd "$APP_DIR"
[[ -f go.mod ]] || { echo "ERROR: go.mod no encontrado"; exit 1; }
if [[ -n "$(git status --porcelain)" ]]; then echo "ERROR: hay cambios locales"; git status --short; exit 1; fi
git fetch origin main
LOCAL="$(git rev-parse HEAD)"; REMOTE="$(git rev-parse origin/main)"
if [[ "$LOCAL" == "$REMOTE" ]]; then echo "Ya está actualizado: $LOCAL"; exit 0; fi
git pull --ff-only origin main
go test ./...
go build -trimpath -ldflags="-s -w" -o .bot.new .
[[ -x .bot.new ]] || { echo "ERROR: build falló"; exit 1; }
sudo systemctl stop "$SERVICE_NAME"
[[ -f bot ]] && cp bot bot.previous
mv .bot.new bot
chmod 755 bot
if ! sudo systemctl start "$SERVICE_NAME"; then
  sudo systemctl stop "$SERVICE_NAME" || true
  [[ -f bot.previous ]] && cp bot.previous bot
  sudo systemctl start "$SERVICE_NAME" || true
  echo "ERROR: rollback aplicado"; exit 1
fi
sleep 3
sudo systemctl is-active --quiet "$SERVICE_NAME"
if command -v curl >/dev/null 2>&1; then curl -fsS --max-time 10 http://127.0.0.1:8080/health >/dev/null || { echo "ADVERTENCIA: /health falló"; exit 1; }; fi
echo "Actualización completada: $(git rev-parse HEAD)"
sudo systemctl status "$SERVICE_NAME" --no-pager
