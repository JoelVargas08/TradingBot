#!/usr/bin/env python3
"""Telegram control plane for the GitHub Actions PAPER runner.

This process is intentionally tiny: when the paper workflow is running, the
trading bot itself owns Telegram polling. When no paper runner is active, this
controller polls Telegram only long enough to accept /start or /session_start.
"""
import json
import os
import sys
import urllib.parse
import urllib.request

API_VERSION = "2026-03-10"
REPO = os.environ["GITHUB_REPOSITORY"]
GH_TOKEN = os.environ["GH_TOKEN"].strip()
TG_TOKEN = os.environ["TELEGRAM_BOT_TOKEN"].strip()
ADMIN_CHAT_ID = os.environ.get("TELEGRAM_ADMIN_CHAT_ID", "").strip()

def gh(path, method="GET", body=None):
    req = urllib.request.Request(
        "https://api.github.com" + path,
        method=method,
        data=None if body is None else json.dumps(body).encode(),
        headers={
            "Accept": "application/vnd.github+json",
            "Authorization": "Bearer " + GH_TOKEN,
            "X-GitHub-Api-Version": API_VERSION,
            "Content-Type": "application/json",
        },
    )
    with urllib.request.urlopen(req, timeout=15) as r:
        raw = r.read()
        return json.loads(raw) if raw else None

def telegram(method, params=None):
    query = urllib.parse.urlencode(params or {})
    url = f"https://api.telegram.org/bot{TG_TOKEN}/{method}"
    if query:
        url += "?" + query
    with urllib.request.urlopen(url, timeout=20) as r:
        return json.loads(r.read())

def send(chat_id, text):
    telegram("sendMessage", {"chat_id": str(chat_id), "text": text, "parse_mode": "HTML"})

def active_paper_runs():
    data = gh(f"/repos/{REPO}/actions/runs?event=workflow_dispatch&status=in_progress&per_page=20")
    runs = []
    for run in data.get("workflow_runs", []):
        if run.get("path") == ".github/workflows/paper-session.yml" or run.get("name") == "TradingBot - Paper Session":
            runs.append(run)
    queued = gh(f"/repos/{REPO}/actions/runs?event=workflow_dispatch&status=queued&per_page=20")
    for run in queued.get("workflow_runs", []):
        if run.get("path") == ".github/workflows/paper-session.yml" or run.get("name") == "TradingBot - Paper Session":
            runs.append(run)
    return runs

def authorized(chat_id):
    return not ADMIN_CHAT_ID or str(chat_id) == ADMIN_CHAT_ID

def main():
    if not TG_TOKEN or not GH_TOKEN:
        raise SystemExit("TELEGRAM_BOT_TOKEN/GH_TOKEN faltan")

    runs = active_paper_runs()
    if runs:
        # The trading runner owns Telegram polling while it is alive.
        print(f"paper workflow activo: run={runs[0].get('id')}; no se consume Telegram")
        return

    updates = telegram("getUpdates", {"timeout": "1", "limit": "100", "allowed_updates": json.dumps(["message"])}).get("result", [])
    if not updates:
        print("paper workflow detenido; no hay comandos nuevos")
        return

    # Process only the first recognized lifecycle command. Older unrelated
    # messages are intentionally left for the trading bot after dispatch.
    for u in updates:
        msg = u.get("message") or {}
        text = (msg.get("text") or "").strip()
        chat_id = (msg.get("chat") or {}).get("id")
        if not chat_id or not authorized(chat_id):
            continue
        cmd = text.split()[0].split("@")[0].lower() if text else ""
        if cmd not in ("/start", "/session_start", "/session"):
            continue

        if cmd == "/session":
            send(chat_id, "🔴 <b>Workflow PAPER DETENIDO</b>\\n\\nRunner: apagado\\nWEEX: desconectado\\nUsa /session_start o /start para arrancar.")
            telegram("getUpdates", {"offset": str(int(u["update_id"]) + 1), "limit": "1"})
            return

        # /start without arguments is the new workflow-level start command.
        if cmd == "/start" and len(text.split()) > 1:
            send(chat_id, "ℹ️ /start ahora arranca el workflow PAPER. Para activar alertas usa /myid y la gestión de alertas existente.")
            telegram("getUpdates", {"offset": str(int(u["update_id"]) + 1), "limit": "1"})
            return

        send(chat_id, "🟡 <b>Arrancando workflow PAPER...</b>\\n\\nGitHub Actions iniciará el runner, restaurará el estado persistido y conectará WEEX.")
        gh(
            f"/repos/{REPO}/actions/workflows/paper-session.yml/dispatches",
            method="POST",
            body={"ref": "main", "inputs": {"restore_state": "true", "strategy_id": "", "manual_session": "true"}},
        )
        telegram("getUpdates", {"offset": str(int(u["update_id"]) + 1), "limit": "1"})
        send(chat_id, "✅ <b>Workflow PAPER solicitado</b>\\n\\nEl runner comenzará a arrancar. Usa /session para comprobar cuándo está ACTIVO.")
        return

    print("no se encontró un comando de control autorizado")

if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(f"controller error: {exc}", file=sys.stderr)
        raise
