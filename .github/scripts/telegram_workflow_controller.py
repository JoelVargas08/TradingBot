#!/usr/bin/env python3
"""Telegram control plane for the GitHub Actions PAPER runner.

Behaviour
---------
* When a PAPER run is ``in_progress`` the trading bot itself owns Telegram
  long-polling, so this controller exits without touching ``getUpdates``.
* When there is no PAPER run at all, this controller answers ``/session`` and
  accepts ``/start`` (alias) or ``/session_start`` to dispatch the workflow.
* When a PAPER run is queued but not started yet, the controller can report
  ``EN COLA`` and cancel it with ``/stop`` or ``/session_stop``.

Security
--------
* Only ``TELEGRAM_ADMIN_CHAT_ID`` can drive the workflow.
* No command can ever enable live trading: the dispatched workflow always runs
  ``TRADING_MODE=paper``.
* Tokens are never logged nor echoed; GitHub/Telegram error bodies are
  sanitized before printing.
"""
from __future__ import annotations

import html
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

API_ROOT = "https://api.github.com"
API_VERSION = "2026-03-10"
PAPER_WORKFLOW_FILE = "paper-session.yml"
PAPER_WORKFLOW_NAME = "TradingBot - Paper Session"
DEFAULT_BRANCH = "main"

ACTIVE_STATUSES = {"in_progress"}
PENDING_STATUSES = {"queued", "requested", "waiting", "pending"}

# Command aliases. /start without arguments means "start the PAPER workflow";
# /start <chat_id> (alerts) is handled by the trading bot, not here.
COMMANDS = {
    "/start": "session_start",
    "/session_start": "session_start",
    "/session": "session",
    "/help": "help",
    "/stop": "session_stop",
    "/session_stop": "session_stop",
}


class ControllerError(RuntimeError):
    """Recoverable controller failure with a secret-free message."""


class GitHubError(ControllerError):
    """GitHub API rejected a request."""

    def __init__(self, status: int, endpoint: str, detail: str):
        self.status = status
        self.endpoint = endpoint
        self.detail = detail
        kind = {
            401: "autenticación rechazada",
            403: "permisos insuficientes o límite alcanzado",
            404: "ruta o recurso no encontrado",
        }.get(status, "respuesta inesperada")
        super().__init__(f"GitHub API HTTP {status} ({kind}) en {endpoint}: {detail}")


def _sanitize(text: str, secrets) -> str:
    """Return at most 500 chars of ``text`` with secrets replaced by ***."""
    out = text or ""
    for secret in secrets:
        if secret:
            out = out.replace(secret, "***")
    out = out.replace("\n", " ").strip()
    return out[:500]


def _default_opener(request: urllib.request.Request, timeout: int):
    """Open ``request`` and return ``(status, body_bytes)``."""
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return response.status, response.read()


def _decode(raw) -> str:
    if raw is None:
        return ""
    if isinstance(raw, bytes):
        return raw.decode("utf-8", errors="replace")
    return str(raw)


class GitHubClient:
    """Thin, testable wrapper over the GitHub Actions REST endpoints we need."""

    def __init__(self, token: str, repository: str, opener=_default_opener,
                 api_root: str = API_ROOT, timeout: int = 15):
        self.token = token
        self.repository = repository
        self.opener = opener
        self.api_root = api_root
        self.timeout = timeout

    def request(self, path: str, method: str = "GET", body=None):
        url = self.api_root + path
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(
            url,
            method=method,
            data=data,
            headers={
                "Accept": "application/vnd.github+json",
                "Authorization": "Bearer " + self.token,
                "X-GitHub-Api-Version": API_VERSION,
                "Content-Type": "application/json",
            },
        )
        try:
            _, raw = self.opener(request, self.timeout)
        except urllib.error.HTTPError as exc:
            detail = _sanitize(_decode(exc.read()), [self.token])
            raise GitHubError(exc.code, path, detail) from None
        except urllib.error.URLError as exc:
            raise ControllerError(f"GitHub API no alcanzable en {path}: {exc.reason}") from None
        text = _decode(raw)
        return json.loads(text) if text else None

    def list_paper_runs(self):
        """Return active or pending runs of the PAPER workflow only."""
        path = (
            f"/repos/{self.repository}/actions/workflows/"
            f"{PAPER_WORKFLOW_FILE}/runs?per_page=50"
        )
        data = self.request(path) or {}
        runs = []
        for run in data.get("workflow_runs", []):
            if run.get("status") in ACTIVE_STATUSES or run.get("status") in PENDING_STATUSES:
                runs.append(run)
        return runs

    def paper_state(self):
        """Return ``(active_runs, pending_runs)`` for the PAPER workflow."""
        active, pending = [], []
        for run in self.list_paper_runs():
            if run.get("status") in ACTIVE_STATUSES:
                active.append(run)
            else:
                pending.append(run)
        return active, pending

    def dispatch_paper(self, strategy_id: str = "", restore_state: bool = True):
        path = (
            f"/repos/{self.repository}/actions/workflows/"
            f"{PAPER_WORKFLOW_FILE}/dispatches"
        )
        body = {
            "ref": DEFAULT_BRANCH,
            "inputs": {
                "restore_state": "true" if restore_state else "false",
                "strategy_id": strategy_id,
                "manual_session": "true",
            },
        }
        self.request(path, method="POST", body=body)

    def cancel_run(self, run_id):
        path = f"/repos/{self.repository}/actions/runs/{run_id}/cancel"
        self.request(path, method="POST", body={})


class TelegramClient:
    """Minimal Telegram Bot API client (polling + sendMessage)."""

    def __init__(self, token: str, opener=_default_opener, timeout: int = 20):
        self.token = token
        self.opener = opener
        self.timeout = timeout

    def call(self, method: str, params=None):
        url = f"https://api.telegram.org/bot{self.token}/{method}"
        data = urllib.parse.urlencode(params or {}).encode()
        request = urllib.request.Request(url, data=data, method="POST")
        try:
            _, raw = self.opener(request, self.timeout)
        except urllib.error.HTTPError as exc:
            detail = _sanitize(_decode(exc.read()), [self.token])
            raise ControllerError(f"Telegram {method} HTTP {exc.code}: {detail}") from None
        except urllib.error.URLError as exc:
            raise ControllerError(f"Telegram no alcanzable ({method}): {exc.reason}") from None
        payload = json.loads(_decode(raw) or "{}")
        if not payload.get("ok", False):
            description = _sanitize(str(payload.get("description", "error")), [self.token])
            raise ControllerError(f"Telegram {method}: {description}")
        return payload.get("result")

    def get_updates(self, offset=None, timeout: int = 0, limit: int = 100):
        params = {
            "timeout": str(timeout),
            "limit": str(limit),
            "allowed_updates": json.dumps(["message"]),
        }
        if offset is not None:
            params["offset"] = str(offset)
        return self.call("getUpdates", params) or []

    def confirm(self, update_id):
        """Acknowledge a handled update so it is not delivered again."""
        self.get_updates(offset=int(update_id) + 1, limit=1)

    def send_message(self, chat_id, text: str):
        self.call("sendMessage", {
            "chat_id": str(chat_id),
            "text": text,
            "parse_mode": "HTML",
            "disable_web_page_preview": "true",
        })


class Controller:
    """Orchestrates one controller cycle."""

    def __init__(self, github: GitHubClient, telegram: TelegramClient,
                 admin_chat_id: str, log=print, poll_window_seconds: int = 0):
        self.github = github
        self.telegram = telegram
        self.admin_chat_id = str(admin_chat_id).strip()
        self.log = log
        self.poll_window_seconds = max(0, min(int(poll_window_seconds), 260))

    def authorized(self, chat_id) -> bool:
        return bool(self.admin_chat_id) and str(chat_id) == self.admin_chat_id

    def _select_command(self, updates):
        """Return ``(update_id, chat_id, action)`` for the first admin command."""
        for update in sorted(updates, key=lambda u: u.get("update_id", 0)):
            message = update.get("message") or {}
            text = (message.get("text") or "").strip()
            chat_id = (message.get("chat") or {}).get("id")
            if not text or chat_id is None:
                continue
            token = text.split()[0].split("@")[0].lower()
            action = COMMANDS.get(token)
            if action is None or not self.authorized(chat_id):
                continue
            return update.get("update_id"), chat_id, action
        return None

    def _status_text(self, pending) -> str:
        if pending:
            run_id = html.escape(str(pending[0].get("id", "?")))
            return (
                "🟡 <b>Workflow PAPER EN COLA</b>\n\n"
                f"Run: <code>{run_id}</code>\n"
                "Runner: esperando recursos de GitHub Actions\n"
                "WEEX: aún sin conexión\n"
                "Usa /session_stop o /stop para cancelar."
            )
        return (
            "🔴 <b>Workflow PAPER DETENIDO</b>\n\n"
            "Runner: apagado\n"
            "WEEX: desconectado\n"
            "Usa /session_start o /start para arrancar."
        )

    def _start(self, chat_id, pending):
        if pending:
            self.telegram.send_message(
                chat_id,
                "🟡 Ya hay una sesión PAPER en cola; no se lanza otra.",
            )
            return
        self.github.dispatch_paper(strategy_id="", restore_state=True)
        self.telegram.send_message(
            chat_id,
            "✅ <b>Workflow PAPER solicitado</b>\n\n"
            "El runner arrancará, restaurará el estado persistido y conectará WEEX.\n"
            "Usa /session para comprobar cuándo está ACTIVO.",
        )

    def _stop(self, chat_id, pending):
        if not pending:
            self.telegram.send_message(
                chat_id,
                "ℹ️ No hay ninguna sesión PAPER en cola que cancelar. "
                "Si está ACTIVA, el propio runner procesa /session_stop.",
            )
            return
        cancelled = []
        for run in pending:
            self.github.cancel_run(run.get("id"))
            cancelled.append(html.escape(str(run.get("id"))))
        self.telegram.send_message(
            chat_id,
            "🔴 <b>Cancelación solicitada</b>\n\n"
            f"Runs en cola cancelados: {', '.join(cancelled)}\n"
            "Confirma con /session.",
        )

    def _handle(self, action, chat_id, pending):
        if action == "session":
            self.telegram.send_message(chat_id, self._status_text(pending))
        elif action == "help":
            self.telegram.send_message(
                chat_id,
                "<b>🤖 AquaTrade — comandos</b>\\n\\n"
                "/session — estado del workflow PAPER\\n"
                "/session_start — iniciar sesión PAPER y conectar WEEX\\n"
                "/session_stop — detener la sesión PAPER\\n"
                "/help — mostrar esta ayuda\\n\\n"
                "La sesión es simulada (PAPER); no envía órdenes reales. "
                "Mientras PAPER está activo, usa los comandos del runner.",
            )
        elif action == "session_start":
            self._start(chat_id, pending)
        elif action == "session_stop":
            self._stop(chat_id, pending)

    def run(self) -> int:
        active, pending = self.github.paper_state()
        if active:
            # Mientras el runner está activo, el proceso Go es el único
            # consumidor de Telegram y puede cancelar su propio workflow.
            self.log(
                "paper workflow activo: run="
                f"{active[0].get('id')}; el runner gestiona Telegram"
            )
            return 0

        # En reposo, mantener este job escuchando en long-poll. Un único
        # getUpdates inmediato cada cinco minutos hacía que /session_start
        # dependiera demasiado del instante exacto del cron y de carreras.
        deadline = time.monotonic() + self.poll_window_seconds
        first_poll = True
        while True:
            try:
                updates = self.telegram.get_updates(
                    limit=100,
                    timeout=25 if self.poll_window_seconds > 0 else 0,
                )
            except ControllerError as exc:
                if "Conflict" in str(exc):
                    self.log("otro consumidor de Telegram está activo; se cede")
                    return 0
                raise

            if updates:
                selection = self._select_command(updates)
                if selection is not None:
                    update_id, chat_id, action = selection
                    try:
                        # Recheck before dispatch/cancel to avoid duplicate
                        # sessions if another run was queued meanwhile.
                        _, pending = self.github.paper_state()
                        self._handle(action, chat_id, pending)
                    except GitHubError as exc:
                        self.log(f"ERROR: {exc}")
                        self.telegram.send_message(
                            chat_id,
                            "❌ <b>GitHub rechazó la operación</b>\n\n"
                            f"<code>{html.escape(str(exc))}</code>",
                        )
                        self.telegram.confirm(update_id)
                        return 1
                    self.telegram.confirm(update_id)
                    return 0

                # Acknowledge unrelated/unauthorized updates so they do not
                # remain queued and get reconsidered on every scheduled run.
                latest_id = max(int(u.get("update_id", -1)) for u in updates)
                if latest_id >= 0:
                    self.telegram.confirm(latest_id)
                self.log("actualizaciones sin comandos de control autorizados")

            elif first_poll:
                self.log("sin sesión activa; escuchando comandos de Telegram")
            first_poll = False

            if time.monotonic() >= deadline:
                self.log("ventana de escucha finalizada; no hay comandos de control")
                return 0
            if self.poll_window_seconds <= 0:
                return 0

def _validate_env() -> dict:
    values = {
        "tg_token": os.environ.get("TELEGRAM_BOT_TOKEN", "").strip(),
        "gh_token": os.environ.get("GH_TOKEN", "").strip(),
        "repository": os.environ.get("GITHUB_REPOSITORY", "").strip(),
        "admin_chat_id": os.environ.get("TELEGRAM_ADMIN_CHAT_ID", "").strip(),
    }
    if not values["tg_token"]:
        raise ControllerError("TELEGRAM_BOT_TOKEN no configurado")
    if not values["gh_token"]:
        raise ControllerError("GH_TOKEN no configurado")
    repository = values["repository"]
    if "\\" in repository or "/" not in repository:
        raise ControllerError(
            f"GITHUB_REPOSITORY inválido ({repository!r}); debe ser owner/repo"
        )
    if not values["admin_chat_id"]:
        raise ControllerError(
            "TELEGRAM_ADMIN_CHAT_ID no configurado; se bloquea el control de workflow"
        )
    return values


def main() -> int:
    try:
        env = _validate_env()
    except ControllerError as exc:
        print(f"controller error: {exc}", file=sys.stderr)
        return 2
    github = GitHubClient(env["gh_token"], env["repository"])
    telegram = TelegramClient(env["tg_token"])
    controller = Controller(
        github, telegram, env["admin_chat_id"],
        poll_window_seconds=int(os.environ.get("CONTROLLER_POLL_WINDOW_SECONDS", "240")),
    )
    try:
        return controller.run()
    except ControllerError as exc:
        print(f"controller error: {exc}", file=sys.stderr)
        return 1
    except Exception as exc:  # pragma: no cover - defensive
        print(f"controller error: {_sanitize(str(exc), [env['gh_token'], env['tg_token']])}",
              file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
