#!/usr/bin/env bash
set -euo pipefail
SERVICE_NAME="${SERVICE_NAME:-tradingbot}"
sudo systemctl restart "$SERVICE_NAME"
sleep 3
sudo systemctl is-active --quiet "$SERVICE_NAME" || { sudo systemctl status "$SERVICE_NAME" --no-pager; exit 1; }
if command -v curl >/dev/null 2>&1; then curl -fsS --max-time 10 http://127.0.0.1:8080/health || exit 1; echo; fi
echo "Reinicio completado."
