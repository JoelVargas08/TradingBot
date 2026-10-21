#!/usr/bin/env python3
"""Telegram control plane for the GitHub Actions PAPER runner.

Architecture: a single consumer of ``getUpdates`` per bot token
--------------------------------------------------------------
* While a PAPER run exists (``queued`` **or** ``in_progress``) the **runner**
  owns the Telegram long polling.  It answers ``/ping``, ``/help``,
  ``/session``, ``/session_start`` and ``/session_stop`` with the live state of
  the trading session.  The controller does **not** call ``getUpdates`` while
  that is the case, therefore two consumers never compete for the same token
  (no HTTP 409 race).
* While there is no PAPER run at all, the **controller** owns the polling and
  answers the lifecycle commands, dispatching or cancelling the PAPER workflow
  through the GitHub Actions API.

The daily start is orchestrated by the controller, which makes it the only
dispatcher of ``paper-session.yml``.  That avoids duplicated PAPER sessions
(two concurrent paper runners would double the simulated exposure) and makes
the start self-healing: if the scheduled trigger is skipped or delayed, the
next cycle starts the session inside the catch-up window.

The Telegram user does not need the app open: the Bot API delivers messages
independently of the client, and notifications sent while the user is offline
are queued by Telegram and shown as soon as the client reconnects.

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
import random
import socket
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timedelta, timezone

try:  # Python >= 3.9
    from zoneinfo import ZoneInfo
except ImportError:  # pragma: no cover - defensive
    ZoneInfo = None

API_ROOT = "https://api.github.com"
API_VERSION = "2026-03-10"
PAPER_WORKFLOW_FILE = "paper-session.yml"
PAPER_WORKFLOW_NAME = "TradingBot - Paper Session"
DEFAULT_BRANCH = "main"

ACTIVE_STATUSES = {"in_progress"}
PENDING_STATUSES = {"queued", "requested", "waiting", "pending"}
FAILED_STATUSES = {"failed"}

# Timezone used for the daily session and for the messages sent to the admin.
DEFAULT_TIMEZONE = "America/Havana"
DEFAULT_AUTO_START_HOUR = 8
DEFAULT_AUTO_START_DEADLINE_HOUR = 13
# Monday..Friday (weekday() numbering).
AUTO_START_WEEKDAYS = (0, 1, 2, 3, 4)
# Maximum number of automatic attempts per day (only failed runs are retried).
MAX_AUTO_ATTEMPTS = 3

# Listening window of a controller cycle.  The cron fires every 5 minutes, so
# keeping the job in long polling for 240 s makes the latency of a command
# ~1 min instead of up to 5 min, without overlapping cycles.
DEFAULT_POLL_WINDOW_SECONDS = 240
MAX_POLL_WINDOW_SECONDS = 260
# Long poll timeout per request; the HTTP timeout is always longer.
POLL_TIMEOUT = 15
HTTP_TIMEOUT = 25

# Commands understood by the controller.  ``/start`` without arguments means
# "start the PAPER workflow"; ``/start <chat_id>`` (alerts) is handled by the
# trading bot, not here.
COMMANDS = {
    "/ping": "ping",
    "/help": "help",
    "/session": "session",
    "/start": "session_start",
    "/session_start": "session_start",
    "/stop": "session_stop",
    "/session_stop": "session_stop",
}

HELP_TEXT = (
    "📚 <b>Comandos del controlador</b>\n\n"
    "/ping - Comprobar que el bot procesa comandos\n"
    "/help - Mostrar esta ayuda\n"
    "/session - Estado real de la sesión PAPER\n"
    "/session_start - Arrancar la sesión PAPER (alias: /start)\n"
    "/session_stop - Detener la sesión PAPER (alias: /stop)\n\n"
    "Los comandos de trading (/positions, /risk, /performance, ...) requieren "
    "una sesión PAPER activa; se responden desde el runner."
)

RETRY_DELAYS = (1.0, 3.0, 7.0)


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


class TelegramError(ControllerError):
    """Telegram API failure."""

    def __init__(self, message: str, status=None, method: str = ""):
        self.status = status
        self.method = method
        super().__init__(message)


class TelegramConflictError(TelegramError):
    """HTTP 409: another getUpdates consumer is using the same token."""


class TelegramAuthError(TelegramError):
    """HTTP 401/403: invalid token or missing permissions."""


class TelegramServerError(TelegramError):
    """HTTP 5xx: Telegram server-side failure."""


class TelegramNetworkError(TelegramError):
    """Read timeout or transient network failure."""


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


def _zone(name: str):
    """Return the tzinfo for ``name`` (UTC-5 fallback if tzdata is missing)."""
    if ZoneInfo is not None:
        try:
            return ZoneInfo(name)
        except Exception:  # pragma: no cover - depends on the host
            pass
    # Fallback: Cuba is UTC-5 (UTC-4 during the northern summer).  Only used
    # when the IANA database is unavailable on the runner.
    return timezone(timedelta(hours=-5), name)


def _parse_iso(text) -> datetime | None:
    """Parse an ISO-8601 timestamp from the GitHub API."""
    if not text:
        return None
    value = str(text).strip()
    if value.endswith("Z"):
        value = value[:-1] + "+00:00"
    try:
        parsed = datetime.fromisoformat(value)
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return parsed.astimezone(timezone.utc)


class AutoStartPolicy:
    """Decides when the daily PAPER session must be started automatically.

    The window is expressed in local time (``America/Havana`` by default) so it
    follows the Cuban daylight saving transitions.
    """

    def __init__(self, tz_name: str = DEFAULT_TIMEZONE,
                 start_hour: int = DEFAULT_AUTO_START_HOUR,
                 deadline_hour: int = DEFAULT_AUTO_START_DEADLINE_HOUR):
        self.tz_name = tz_name
        self.tz = _zone(tz_name)
        self.start_hour = int(start_hour)
        self.deadline_hour = int(deadline_hour)

    def local_now(self, now_utc: datetime) -> datetime:
        return now_utc.astimezone(self.tz)

    def day_start(self, now_utc: datetime) -> datetime:
        """UTC instant of today's 00:00 in the local timezone."""
        local = self.local_now(now_utc)
        local = local.replace(hour=0, minute=0, second=0, microsecond=0)
        return local.astimezone(timezone.utc)

    def is_due(self, now_utc: datetime) -> bool:
        """True when the automatic start must happen right now."""
        local = self.local_now(now_utc)
        if local.weekday() not in AUTO_START_WEEKDAYS:
            return False
        minutes = local.hour * 60 + local.minute
        return self.start_hour * 60 <= minutes < self.deadline_hour * 60

    def local_clock(self, now_utc: datetime) -> str:
        return self.local_now(now_utc).strftime("%Y-%m-%d %H:%M")


class PaperState:
    """Snapshot of the PAPER workflow runs relevant to the controller."""

    def __init__(self, active=None, pending=None, runs=None):
        self.active = list(active or [])
        self.pending = list(pending or [])
        self.runs = list(runs or [])

    @property
    def has_session(self) -> bool:
        """True when a runner exists (or is about to exist)."""
        return bool(self.active or self.pending)

    def attempts_today(self, day_start: datetime):
        """Runs created today (local midnight) sorted by creation time."""
        out = []
        for run in self.runs:
            created = _parse_iso(run.get("created_at"))
            if created is not None and created >= day_start:
                out.append((created, run))
        out.sort(key=lambda item: item[0])
        return out

    def started_today(self, day_start: datetime):
        """Runs of today that actually produced (or produced) a session.

        Failed runs do not count: the automatic start may retry them.  Cancelled
        runs do count: the operator stopped the session on purpose and the bot
        must not restart it behind their back.
        """
        return [run for _, run in self.attempts_today(day_start)
                if run.get("status") not in FAILED_STATUSES]


class GitHubClient:
    """Thin, testable wrapper over the GitHub Actions REST endpoints we need."""

    def __init__(self, token: str, repository: str, opener=_default_opener,
                 api_root: str = API_ROOT, timeout: int = 15,
                 ref: str = DEFAULT_BRANCH):
        self.token = token
        self.repository = repository
        self.opener = opener
        self.api_root = api_root
        self.timeout = timeout
        self.ref = ref or DEFAULT_BRANCH

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
        except (TimeoutError, socket.timeout):
            raise ControllerError(f"GitHub API timeout en {path}") from None
        text = _decode(raw)
        return json.loads(text) if text else None

    def list_paper_runs(self):
        """Return the most recent runs of the PAPER workflow (any status)."""
        path = (
            f"/repos/{self.repository}/actions/workflows/"
            f"{PAPER_WORKFLOW_FILE}/runs?per_page=100"
        )
        data = self.request(path) or {}
        return list(data.get("workflow_runs", []))

    def paper_state(self) -> PaperState:
        """Return the PAPER workflow state (active, pending and recent runs)."""
        runs = self.list_paper_runs()
        active, pending = [], []
        for run in runs:
            status = run.get("status")
            if status in ACTIVE_STATUSES:
                active.append(run)
            elif status in PENDING_STATUSES:
                pending.append(run)
        return PaperState(active=active, pending=pending, runs=runs)

    def dispatch_paper(self, strategy_id: str = "", restore_state: bool = True,
                       manual_session: bool = True):
        path = (
            f"/repos/{self.repository}/actions/workflows/"
            f"{PAPER_WORKFLOW_FILE}/dispatches"
        )
        body = {
            "ref": self.ref,
            "inputs": {
                "restore_state": "true" if restore_state else "false",
                "strategy_id": strategy_id,
                "manual_session": "true" if manual_session else "false",
            },
        }
        self.request(path, method="POST", body=body)

    def cancel_run(self, run_id):
        path = f"/repos/{self.repository}/actions/runs/{run_id}/cancel"
        self.request(path, method="POST", body={})


class TelegramClient:
    """Minimal Telegram Bot API client (polling + sendMessage)."""

    def __init__(self, token: str, opener=_default_opener, timeout: int = HTTP_TIMEOUT):
        self.token = token
        self.opener = opener
        self.timeout = timeout

    def _http_error(self, method: str, status: int, detail: str) -> TelegramError:
        if status == 409:
            return TelegramConflictError(
                f"Telegram {method} HTTP 409 (otro consumidor de getUpdates): {detail}",
                status=status, method=method,
            )
        if status in (401, 403):
            return TelegramAuthError(
                f"Telegram {method} HTTP {status} (token o permisos inválidos): {detail}",
                status=status, method=method,
            )
        if status >= 500:
            return TelegramServerError(
                f"Telegram {method} HTTP {status} (error del servidor): {detail}",
                status=status, method=method,
            )
        return TelegramError(
            f"Telegram {method} HTTP {status}: {detail}", status=status, method=method,
        )

    def call(self, method: str, params=None):
        url = f"https://api.telegram.org/bot{self.token}/{method}"
        data = urllib.parse.urlencode(params or {}).encode()
        request = urllib.request.Request(url, data=data, method="POST")
        try:
            _, raw = self.opener(request, self.timeout)
        except urllib.error.HTTPError as exc:
            detail = _sanitize(_decode(exc.read()), [self.token])
            raise self._http_error(method, exc.code, detail) from None
        except urllib.error.URLError as exc:
            reason = exc.reason
            if isinstance(reason, (TimeoutError, socket.timeout)):
                raise TelegramNetworkError(
                    f"Telegram {method}: timeout de red", method=method,
                ) from None
            raise TelegramNetworkError(
                f"Telegram {method} no alcanzable: {reason}", method=method,
            ) from None
        except (TimeoutError, socket.timeout):
            raise TelegramNetworkError(
                f"Telegram {method}: timeout de lectura", method=method,
            ) from None
        payload = json.loads(_decode(raw) or "{}")
        if not payload.get("ok", False):
            description = _sanitize(str(payload.get("description", "error")), [self.token])
            raise TelegramError(f"Telegram {method}: {description}", method=method)
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

    def confirm(self, offset):
        """Acknowledge handled updates so they are not delivered again.

        Telegram removes every update with ``update_id < offset``; ``offset``
        must therefore be the id of the first update that was *not* handled.
        """
        self.get_updates(offset=int(offset), limit=1)

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
                 admin_chat_id: str, log=print, policy: AutoStartPolicy | None = None,
                 now=None, poll_window_seconds: int = DEFAULT_POLL_WINDOW_SECONDS):
        self.github = github
        self.telegram = telegram
        self.admin_chat_id = str(admin_chat_id).strip()
        self.log = log
        self.policy = policy or AutoStartPolicy()
        self._now = now or (lambda: datetime.now(timezone.utc))
        try:
            window = int(poll_window_seconds)
        except (TypeError, ValueError):
            window = DEFAULT_POLL_WINDOW_SECONDS
        self.poll_window_seconds = max(0, min(window, MAX_POLL_WINDOW_SECONDS))

    # ------------------------------------------------------------------
    # Helpers
    # ------------------------------------------------------------------
    def authorized(self, chat_id) -> bool:
        return bool(self.admin_chat_id) and str(chat_id) == self.admin_chat_id

    def _now_utc(self) -> datetime:
        return self._now()

    def _status_text(self, state: PaperState) -> str:
        if state.active:
            run_id = html.escape(str(state.active[0].get("id", "?")))
            return (
                "🟢 <b>Sesión PAPER ACTIVA</b>\n\n"
                f"Workflow: en ejecución (run <code>{run_id}</code>)\n"
                "Runner: conectado a WEEX y escuchando Telegram\n"
                "Controlador: en espera (el runner es el dueño del polling)\n"
                "Modo: PAPER (sin operaciones reales)"
            )
        if state.pending:
            run_id = html.escape(str(state.pending[0].get("id", "?")))
            return (
                "🟡 <b>Sesión PAPER EN COLA</b>\n\n"
                f"Workflow: esperando runner (run <code>{run_id}</code>)\n"
                "Runner: arrancando (restaura el estado y conecta WEEX)\n"
                "Controlador: en espera\n"
                "Modo: PAPER (sin operaciones reales)"
            )
        return (
            "🔴 <b>Sesión PAPER DETENIDA</b>\n\n"
            "Workflow: sin ejecuciones\n"
            "Runner: apagado\n"
            "Controlador: escuchando Telegram\n"
            "Modo: PAPER (sin operaciones reales)"
        )

    def _start(self, chat_id, state: PaperState):
        if state.active:
            run_id = html.escape(str(state.active[0].get("id", "?")))
            self.telegram.send_message(
                chat_id,
                "🟢 Ya hay una sesión PAPER <b>ACTIVA</b> "
                f"(run <code>{run_id}</code>); no se lanza otra.\n"
                "Usa /session_stop para detenerla.",
            )
            return
        if state.pending:
            run_id = html.escape(str(state.pending[0].get("id", "?")))
            self.telegram.send_message(
                chat_id,
                "🟡 Ya hay una sesión PAPER <b>EN COLA</b> "
                f"(run <code>{run_id}</code>); no se lanza otra.",
            )
            return
        self.github.dispatch_paper(strategy_id="", restore_state=True, manual_session=True)
        self.telegram.send_message(
            chat_id,
            "✅ <b>Workflow PAPER solicitado</b>\n\n"
            "El runner arrancará, restaurará el estado persistido y conectará WEEX.\n"
            "El bot te avisará cuando empiece a trabajar.\n"
            "Usa /session para comprobar cuándo está ACTIVO.",
        )

    def _stop(self, chat_id, state: PaperState):
        targets = list(state.pending) + list(state.active)
        if not targets:
            self.telegram.send_message(
                chat_id,
                "ℹ️ No hay ninguna sesión PAPER en ejecución ni en cola que cancelar.\n"
                "Si el runner está activo, /session_stop lo detiene desde dentro.",
            )
            return
        cancelled = []
        for run in targets:
            self.github.cancel_run(run.get("id"))
            cancelled.append(html.escape(str(run.get("id"))))
        self.telegram.send_message(
            chat_id,
            "🔴 <b>Cancelación solicitada</b>\n\n"
            f"Runs cancelados: {', '.join(cancelled)}\n"
            "La cancelación es asíncrona: confirma con /session.",
        )

    def _handle(self, action, chat_id, state: PaperState):
        if action == "ping":
            self.telegram.send_message(
                chat_id,
                "🏓 <b>Pong!</b>\n"
                "Controlador Telegram operativo.\n"
                f"Hora de Cuba: <code>{self.policy.local_clock(self._now_utc())}</code> "
                f"({self.policy.tz_name})",
            )
        elif action == "help":
            self.telegram.send_message(chat_id, HELP_TEXT)
        elif action == "session":
            self.telegram.send_message(chat_id, self._status_text(state))
        elif action == "session_start":
            self._start(chat_id, state)
        elif action == "session_stop":
            self._stop(chat_id, state)

    def _notify_admin(self, text: str):
        """Best-effort notification; never raises."""
        if not self.admin_chat_id:
            return
        try:
            self.telegram.send_message(self.admin_chat_id, text)
        except ControllerError as exc:
            self.log(f"aviso: no se pudo notificar al admin: {exc}")

    # ------------------------------------------------------------------
    # Automatic start
    # ------------------------------------------------------------------
    def _auto_start(self, state: PaperState) -> bool:
        """Start the daily session when due.  Returns True if a run was dispatched."""
        now = self._now_utc()
        if not self.policy.is_due(now):
            return False
        if state.active or state.pending:
            # Ya hay una sesión en marcha o en cola: no se duplica.
            self.log("ya existe una sesión PAPER; no se duplica el inicio automático")
            return False
        day_start = self.policy.day_start(now)
        attempts = state.attempts_today(day_start)
        if state.started_today(day_start):
            self.log(
                "la sesión PAPER de hoy ya existe "
                f"({len(attempts)} intento(s)); no se duplica"
            )
            return False
        if len(attempts) >= MAX_AUTO_ATTEMPTS:
            self.log(
                f"se alcanzó el máximo de intentos automáticos ({MAX_AUTO_ATTEMPTS}); "
                "no se reintenta"
            )
            self._notify_admin(
                "❌ <b>La sesión PAPER automática no arrancó</b>\n\n"
                f"Se agotaron los {MAX_AUTO_ATTEMPTS} intentos automáticos de hoy.\n"
                "Revisa el workflow <code>TradingBot - Paper Session</code> en Actions.\n"
                "Usa /session_start para relanzarla manualmente."
            )
            return False
        try:
            # manual_session=false: the runner applies the 08:00-14:00 local
            # window and exits when the window closes.
            self.github.dispatch_paper(strategy_id="", restore_state=True,
                                       manual_session=False)
        except GitHubError as exc:
            self.log(f"ERROR: {exc}")
            self._notify_admin(
                "❌ <b>Falló el inicio automático de la sesión PAPER</b>\n\n"
                f"<code>{html.escape(str(exc))}</code>"
            )
            return False
        self.log(
            f"inicio automático de la sesión PAPER ({self.policy.local_clock(now)} "
            f"{self.policy.tz_name}); run solicitado"
        )
        self._notify_admin(
            "🕗 <b>Inicio automático de la sesión PAPER</b>\n\n"
            f"Hora de Cuba: <code>{self.policy.local_clock(now)}</code> "
            f"({self.policy.tz_name})\n"
            "Workflow solicitado: el runner restaurará el estado y conectará WEEX.\n"
            "🔒 Modo: PAPER (sin operaciones reales)\n"
            "El bot te avisará cuando empiece a trabajar."
        )
        return True

    # ------------------------------------------------------------------
    # Telegram polling
    # ------------------------------------------------------------------
    def _poll(self):
        """Fetch pending updates with bounded retries for transient errors."""
        delay = 0.0
        last_error = None
        for attempt in range(len(RETRY_DELAYS) + 1):
            try:
                return self.telegram.get_updates(timeout=POLL_TIMEOUT, limit=100)
            except (TelegramNetworkError, TelegramServerError) as exc:
                last_error = exc
                if attempt >= len(RETRY_DELAYS):
                    break
                delay = RETRY_DELAYS[attempt] + random.uniform(0, 0.5)
                self.log(f"error transitorio de Telegram: {exc}; reintento en {delay:.1f}s")
                time.sleep(delay)
        raise last_error

    def _process(self, updates, state: PaperState):
        """Process a batch of updates.

        Returns ``(failures, commands)`` where ``commands`` counts the
        authorized control commands actually executed.
        """
        handled = 0
        failures = 0
        last_id = None
        for update in sorted(updates, key=lambda u: u.get("update_id", 0)):
            update_id = update.get("update_id")
            if update_id is None:
                continue
            # El update queda visto: se confirma aunque no sea un comando
            # (si no, Telegram lo volvería a entregar en cada ciclo).
            last_id = update_id
            message = update.get("message") or {}
            text = (message.get("text") or "").strip()
            chat_id = (message.get("chat") or {}).get("id")
            if not text or chat_id is None:
                continue
            token = text.split()[0].split("@")[0].lower()
            if not self.authorized(chat_id):
                self.log(f"chat no autorizado ignorado (update_id={update_id})")
                continue
            action = COMMANDS.get(token)
            if action is None:
                if token.startswith("/"):
                    try:
                        self.telegram.send_message(
                            chat_id,
                            "ℹ️ El comando <code>" + html.escape(token) + "</code> "
                            "necesita una sesión PAPER activa.\n"
                            "Usa /session_start para arrancarla y /help para ver "
                            "los comandos.",
                        )
                    except ControllerError as exc:
                        self.log(f"aviso: no se pudo responder al comando: {exc}")
                continue
            try:
                self._handle(action, chat_id, state)
            except GitHubError as exc:
                self.log(f"ERROR: {exc}")
                failures += 1
                self._notify_admin(
                    "❌ <b>GitHub rechazó la operación</b>\n\n"
                    f"<code>{html.escape(str(exc))}</code>"
                )
            except ControllerError as exc:
                self.log(f"ERROR: {exc}")
                failures += 1
                self._notify_admin(
                    "❌ <b>Error del controlador</b>\n\n"
                    f"<code>{html.escape(str(exc))}</code>"
                )
            handled += 1
        if last_id is not None:
            try:
                self.telegram.confirm(last_id + 1)
            except ControllerError as exc:
                self.log(f"aviso: no se pudo confirmar el offset: {exc}")
        if not handled:
            self.log("sin comandos de control autorizados")
        return failures, handled

    # ------------------------------------------------------------------
    # Cycle
    # ------------------------------------------------------------------
    def run(self) -> int:
        try:
            state = self.github.paper_state()
        except GitHubError as exc:
            self.log(f"ERROR: {exc}")
            return 1
        except ControllerError as exc:
            self.log(f"ERROR: {exc}")
            return 1

        if state.active:
            # El runner en ejecución es el dueño del token: consumir updates
            # aquí competiría con él (HTTP 409) y podría perder comandos.
            run_ids = [r.get("id") for r in state.active]
            self.log(
                "sesión PAPER en ejecución (runs=%s); el runner escucha "
                "Telegram, el controlador cede" % ",".join(str(i) for i in run_ids)
            )
            return 0

        if state.pending:
            # El run está en cola: todavía no hay nadie consumiendo el token,
            # así que el controlador puede atender /session_stop y cancelar la
            # ejecución en cola por la API de GitHub.
            self.log(
                "sesión PAPER en cola (runs=%s); el controlador atiende Telegram"
                % ",".join(str(r.get("id")) for r in state.pending)
            )

        if self._auto_start(state):
            return 0

        # Ventana de escucha: el cron dispara cada 5 minutos, así que el job
        # se queda en long polling un rato.  La latencia de un comando pasa de
        # "hasta 5 minutos" a "hasta ~1 minuto" sin solapar ciclos.
        deadline = time.monotonic() + self.poll_window_seconds
        while True:
            try:
                updates = self._poll()
            except TelegramConflictError as exc:
                # El runner ya tomó el token: el controlador cede el polling.
                self.log(f"409 de Telegram; otro consumidor está activo: {exc}")
                return 0
            except TelegramAuthError as exc:
                self.log(f"ERROR: {exc}")
                return 1
            except ControllerError as exc:
                self.log(f"ERROR: {exc}")
                return 1

            if updates:
                failures, commands = self._process(updates, state)
                if failures:
                    return 1
                if commands:
                    # Un comando de control ya se atendió: no hace falta
                    # seguir escuchando en este ciclo.
                    return 0
            else:
                self.log("no hay comandos nuevos")

            if self.poll_window_seconds <= 0 or time.monotonic() >= deadline:
                self.log("ventana de escucha finalizada; sin comandos de control")
                return 0


def _int_env(name: str, fallback: int) -> int:
    raw = os.environ.get(name, "").strip()
    if not raw:
        return fallback
    try:
        return int(raw)
    except ValueError:
        return fallback


def _validate_env() -> dict:
    values = {
        "tg_token": os.environ.get("TELEGRAM_BOT_TOKEN", "").strip(),
        "gh_token": os.environ.get("GH_TOKEN", "").strip(),
        "repository": os.environ.get("GITHUB_REPOSITORY", "").strip(),
        "admin_chat_id": os.environ.get("TELEGRAM_ADMIN_CHAT_ID", "").strip(),
        "timezone": os.environ.get("TRADINGBOT_TIMEZONE", DEFAULT_TIMEZONE).strip()
        or DEFAULT_TIMEZONE,
        "auto_start_hour": _int_env("TRADINGBOT_AUTO_START_HOUR", DEFAULT_AUTO_START_HOUR),
        "auto_start_deadline_hour": _int_env(
            "TRADINGBOT_AUTO_START_DEADLINE_HOUR", DEFAULT_AUTO_START_DEADLINE_HOUR
        ),
        "dispatch_ref": os.environ.get("TRADINGBOT_DISPATCH_REF", DEFAULT_BRANCH).strip()
        or DEFAULT_BRANCH,
        "poll_window_seconds": _int_env(
            "CONTROLLER_POLL_WINDOW_SECONDS", DEFAULT_POLL_WINDOW_SECONDS
        ),
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
    if not 0 <= values["auto_start_hour"] <= 23:
        raise ControllerError("TRADINGBOT_AUTO_START_HOUR debe estar entre 0 y 23")
    if not 0 <= values["auto_start_deadline_hour"] <= 24:
        raise ControllerError(
            "TRADINGBOT_AUTO_START_DEADLINE_HOUR debe estar entre 0 y 24"
        )
    if values["auto_start_deadline_hour"] <= values["auto_start_hour"]:
        raise ControllerError(
            "TRADINGBOT_AUTO_START_DEADLINE_HOUR debe ser posterior a "
            "TRADINGBOT_AUTO_START_HOUR"
        )
    return values


def main() -> int:
    try:
        env = _validate_env()
    except ControllerError as exc:
        print(f"controller error: {exc}", file=sys.stderr)
        return 2
    github = GitHubClient(env["gh_token"], env["repository"], ref=env["dispatch_ref"])
    telegram = TelegramClient(env["tg_token"])
    policy = AutoStartPolicy(
        tz_name=env["timezone"],
        start_hour=env["auto_start_hour"],
        deadline_hour=env["auto_start_deadline_hour"],
    )
    controller = Controller(github, telegram, env["admin_chat_id"], policy=policy,
                            poll_window_seconds=env["poll_window_seconds"])
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
