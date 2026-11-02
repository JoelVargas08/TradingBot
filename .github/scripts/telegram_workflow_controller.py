#!/usr/bin/env python3
"""Telegram control plane for the GitHub Actions PAPER runner.

Architecture: a single consumer of ``getUpdates`` per bot token
--------------------------------------------------------------
* While a PAPER run is ``in_progress`` the **runner** owns the Telegram long
  polling.  It answers ``/ping``, ``/help``, ``/session``, ``/session_start``
  and ``/session_stop`` with the live state of the trading session.  The
  controller does **not** call ``getUpdates`` while that is the case, therefore
  two consumers never compete for the same token (no HTTP 409 race).
* While there is no PAPER runner (no run, or a run still ``queued``), the
  **controller** owns the polling and answers the lifecycle commands,
  dispatching or cancelling the PAPER workflow through the GitHub Actions API.

Relay: the controller is a long-running process, not a 5-minute cron
--------------------------------------------------------------------
Measured in this repository, GitHub ran the ``*/5`` cron of the controller only
2-7 times per day (gaps of 4-7 h) and the ``0 8 * * 1-5`` cron of the PAPER
workflow fired between 11:57 and 13:58 Havana.  A cron-only design can neither
start the session at 08:00 nor answer Telegram in reasonable time.

Each controller job therefore runs for ``CONTROLLER_RUNTIME_SECONDS`` (5h30m by
default): it long-polls Telegram, refreshes the PAPER state every
``STATE_REFRESH_SECONDS`` and dispatches the daily session as soon as the
08:00 window opens.  Before ending, the workflow dispatches its own successor
(``telegram-controller.yml``), so the chain covers the whole day.  The
workflow's concurrency group guarantees a single controller at a time and the
cron only acts as a watchdog that restarts a broken chain.

The controller is the only automatic dispatcher of ``paper-session.yml``; it
never dispatches while a run is queued or in progress (see ``_auto_start``).

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

# GitHub run ``status`` values.  A finished run is always ``completed`` and the
# outcome lives in ``conclusion``; GitHub never reports ``status=failed``.
ACTIVE_STATUSES = {"in_progress"}
PENDING_STATUSES = {"queued", "requested", "waiting", "pending"}
COMPLETED_STATUS = "completed"
# Conclusions that mean "the session did not run": the automatic start may
# retry them.  ``cancelled`` is deliberately absent: the operator stopped the
# session and the bot must not restart it behind their back.
RETRYABLE_CONCLUSIONS = {"failure", "timed_out", "startup_failure"}

# Timezone used for the daily session and for the messages sent to the admin.
DEFAULT_TIMEZONE = "America/Havana"
DEFAULT_AUTO_START_HOUR = 8
DEFAULT_AUTO_START_DEADLINE_HOUR = 13
# Monday..Friday (weekday() numbering).
AUTO_START_WEEKDAYS = (0, 1, 2, 3, 4)
# Maximum number of failed automatic attempts per day.
MAX_AUTO_ATTEMPTS = 3

# Runtime of a controller job.  The workflow relays to a new job before the
# 6 h limit of GitHub-hosted runners.  0 = a single pass (tests, diagnostics).
DEFAULT_RUNTIME_SECONDS = 19_800  # 5h30m
MAX_RUNTIME_SECONDS = 20_400  # 5h40m: the job timeout is 345 min
# Cadence of the GitHub state refresh (60 calls/h, well below the 1000/h of
# GITHUB_TOKEN).
STATE_REFRESH_SECONDS = 60
# Long poll timeout per request; the HTTP timeout is always longer.
POLL_TIMEOUT = 25
HTTP_TIMEOUT = 40
# After a dispatch/cancel the controller asks GitHub for confirmation.
CONFIRM_ATTEMPTS = 4
CONFIRM_INTERVAL_SECONDS = 5.0
# Lifecycle commands older than this are not executed (they may have waited
# hours in Telegram while the chain was broken).
COMMAND_MAX_AGE_SECONDS = 15 * 60
# Back-off when GitHub/Telegram fail inside a long-running job.
ERROR_BACKOFF_SECONDS = 30.0
# Minimum time between two admin notifications about the same failure.
ERROR_NOTIFY_INTERVAL_SECONDS = 30 * 60

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
LIFECYCLE_ACTIONS = {"session_start", "session_stop"}

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
            422: "petición rechazada (rama o inputs inválidos)",
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


def _run_id(run) -> str:
    return html.escape(str(run.get("id", "?")))


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

    def window_start(self, now_utc: datetime) -> datetime:
        """UTC instant of today's automatic start (08:00 local by default)."""
        local = self.local_now(now_utc)
        local = local.replace(hour=self.start_hour, minute=0, second=0, microsecond=0)
        return local.astimezone(timezone.utc)

    def is_due(self, now_utc: datetime) -> bool:
        """True when the automatic start must happen right now."""
        local = self.local_now(now_utc)
        if local.weekday() not in AUTO_START_WEEKDAYS:
            return False
        minutes = local.hour * 60 + local.minute
        return self.start_hour * 60 <= minutes < self.deadline_hour * 60

    def next_start(self, now_utc: datetime) -> datetime:
        """Local datetime of the next automatic start after ``now_utc``."""
        local = self.local_now(now_utc)
        day = local.date()
        if local.hour * 60 + local.minute >= self.start_hour * 60:
            day += timedelta(days=1)
        while day.weekday() not in AUTO_START_WEEKDAYS:
            day += timedelta(days=1)
        # Built from the wall clock so a DST change between days is respected.
        return datetime(day.year, day.month, day.day, self.start_hour, tzinfo=self.tz)

    def local_clock(self, now_utc: datetime) -> str:
        return self.local_now(now_utc).strftime("%Y-%m-%d %H:%M")


class PaperState:
    """Snapshot of the PAPER workflow runs relevant to the controller."""

    def __init__(self, active=None, pending=None, runs=None):
        self.active = list(active or [])
        self.pending = list(pending or [])
        self.runs = list(runs or [])

    @classmethod
    def from_runs(cls, runs) -> "PaperState":
        """Classify the runs.  An unknown status counts as pending.

        Interpreting an unknown status as "no session" could dispatch a
        duplicate; treating it as pending only delays the next action.
        """
        active, pending = [], []
        for run in runs:
            status = run.get("status")
            if status in ACTIVE_STATUSES:
                active.append(run)
            elif status != COMPLETED_STATUS:
                pending.append(run)
        return cls(active=active, pending=pending, runs=runs)

    @property
    def has_session(self) -> bool:
        """True when a runner exists (or is about to exist)."""
        return bool(self.active or self.pending)

    def completed(self):
        return [run for run in self.runs if run.get("status") == COMPLETED_STATUS]

    def runs_in_window(self, window_start: datetime):
        """Completed runs that were alive at or after ``window_start``.

        A run that finished before today's window (a night test, yesterday's
        session) is irrelevant for today's automatic start.
        """
        out = []
        for run in self.completed():
            ended = _parse_iso(run.get("updated_at")) or _parse_iso(run.get("created_at"))
            if ended is not None and ended >= window_start:
                out.append(run)
        return out

    def failed_in_window(self, window_start: datetime):
        return [run for run in self.runs_in_window(window_start)
                if run.get("conclusion") in RETRYABLE_CONCLUSIONS]

    def session_in_window(self, window_start: datetime):
        """Runs that already covered (or were stopped during) today's window.

        Successful runs mean the day had its session; cancelled runs mean the
        operator stopped it on purpose.  Neither must be restarted.
        """
        return [run for run in self.runs_in_window(window_start)
                if run.get("conclusion") not in RETRYABLE_CONCLUSIONS]

    def last_finished_at(self) -> datetime | None:
        """Instant at which the most recent completed run ended."""
        out = None
        for run in self.completed():
            ended = _parse_iso(run.get("updated_at"))
            if ended is not None and (out is None or ended > out):
                out = ended
        return out

    def latest_completed(self):
        best, best_ts = None, None
        for run in self.completed():
            ended = _parse_iso(run.get("updated_at"))
            if ended is not None and (best_ts is None or ended > best_ts):
                best, best_ts = run, ended
        return best


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
            f"{PAPER_WORKFLOW_FILE}/runs?per_page=50"
        )
        data = self.request(path) or {}
        return list(data.get("workflow_runs", []))

    def paper_state(self) -> PaperState:
        """Return the PAPER workflow state (active, pending and recent runs)."""
        return PaperState.from_runs(self.list_paper_runs())

    def get_run(self, run_id):
        return self.request(f"/repos/{self.repository}/actions/runs/{run_id}") or {}

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
    """Orchestrates the controller loop (or a single pass when runtime is 0)."""

    def __init__(self, github: GitHubClient, telegram: TelegramClient,
                 admin_chat_id: str, log=print, policy: AutoStartPolicy | None = None,
                 now=None, runtime_seconds: int = 0, sleep=None, monotonic=None,
                 state_refresh_seconds: float = STATE_REFRESH_SECONDS,
                 run_context: dict | None = None):
        self.github = github
        self.telegram = telegram
        self.admin_chat_id = str(admin_chat_id).strip()
        self._log = log
        self.policy = policy or AutoStartPolicy()
        self._now = now or (lambda: datetime.now(timezone.utc))
        self._sleep = sleep or time.sleep
        self._monotonic = monotonic or time.monotonic
        try:
            runtime = int(runtime_seconds)
        except (TypeError, ValueError):
            runtime = 0
        self.runtime_seconds = max(0, min(runtime, MAX_RUNTIME_SECONDS))
        self.state_refresh_seconds = max(1.0, float(state_refresh_seconds))
        self.run_context = dict(run_context or {})
        self.had_errors = False
        # Fatal = relanzar el controlador no lo arregla (token inválido).
        self.fatal = False
        self._exhausted_notified_day = None
        self._last_error_notify = {}
        self._yielding = False

    # ------------------------------------------------------------------
    # Helpers
    # ------------------------------------------------------------------
    def log(self, text: str):
        """Log with correlation fields: UTC, Havana time, run id and event."""
        now = self._now_utc()
        prefix = (
            f"[utc={now.strftime('%Y-%m-%dT%H:%M:%SZ')} "
            f"local={self.policy.local_clock(now)} "
            f"run={self.run_context.get('run_id') or '-'} "
            f"event={self.run_context.get('event') or '-'}]"
        )
        self._log(f"{prefix} {text}")

    def authorized(self, chat_id) -> bool:
        return bool(self.admin_chat_id) and str(chat_id) == self.admin_chat_id

    def _now_utc(self) -> datetime:
        return self._now()

    def _local(self, ts: datetime | None) -> str:
        if ts is None:
            return "-"
        return self.policy.local_clock(ts)

    def _status_text(self, state: PaperState) -> str:
        now = self._now_utc()
        if state.active:
            return (
                "🟢 <b>Sesión PAPER ACTIVA</b>\n\n"
                f"Workflow: en ejecución (run <code>{_run_id(state.active[0])}</code>)\n"
                "Runner: conectado a WEEX y escuchando Telegram\n"
                "Controlador: en espera (el runner es el dueño del polling)\n"
                "Modo: PAPER (sin operaciones reales)"
            )
        if state.pending:
            run = state.pending[0]
            return (
                "🟡 <b>Sesión PAPER EN COLA</b>\n\n"
                f"Workflow: {html.escape(str(run.get('status') or 'en cola'))} "
                f"(run <code>{_run_id(run)}</code>)\n"
                "Runner: arrancando (restaura el estado y conecta WEEX)\n"
                "Controlador: en espera\n"
                "Modo: PAPER (sin operaciones reales)"
            )
        last = state.latest_completed()
        if last is not None:
            ended = _parse_iso(last.get("updated_at"))
            last_text = (
                f"Última sesión: run <code>{_run_id(last)}</code> "
                f"({html.escape(str(last.get('conclusion') or '?'))}, "
                f"fin {self._local(ended)})"
            )
        else:
            last_text = "Última sesión: ninguna registrada"
        next_start = self.policy.next_start(now).strftime("%Y-%m-%d %H:%M")
        due = "ahora (ventana abierta)" if self.policy.is_due(now) else next_start
        return (
            "🔴 <b>Sesión PAPER DETENIDA</b>\n\n"
            "Workflow: sin ejecuciones en marcha\n"
            "Runner: apagado\n"
            "Controlador: escuchando Telegram\n"
            f"{last_text}\n"
            f"Próximo arranque automático: {due} ({self.policy.tz_name})\n"
            "Estrategia y última vela: las informa el runner al arrancar\n"
            "Modo: PAPER (sin operaciones reales)"
        )

    def _wait_for_new_run(self, known_ids, requested_at: datetime):
        """Return the run created by our dispatch, or None if GitHub is silent."""
        floor = requested_at - timedelta(seconds=10)
        for attempt in range(CONFIRM_ATTEMPTS):
            if attempt:
                self._sleep(CONFIRM_INTERVAL_SECONDS)
            try:
                runs = self.github.list_paper_runs()
            except ControllerError as exc:
                self.log(f"aviso: no se pudo confirmar el despacho: {exc}")
                continue
            for run in runs:
                created = _parse_iso(run.get("created_at"))
                if run.get("id") in known_ids or created is None or created < floor:
                    continue
                if run.get("event") not in (None, "workflow_dispatch"):
                    continue
                return run
        return None

    def _dispatch(self, state: PaperState, manual_session: bool):
        """Dispatch the PAPER workflow and look for the run GitHub created.

        Returns ``(run_or_None)`` and marks ``state`` as pending so that a
        second /start in the same batch never dispatches again.
        """
        known = {run.get("id") for run in state.runs}
        requested_at = self._now_utc()
        self.github.dispatch_paper(strategy_id="", restore_state=True,
                                   manual_session=manual_session)
        run = self._wait_for_new_run(known, requested_at)
        placeholder = run or {"id": "solicitado", "status": "requested",
                              "created_at": requested_at.isoformat()}
        state.pending.append(placeholder)
        state.runs.append(placeholder)
        if run is not None:
            self.log(f"despacho confirmado: run={run.get('id')} status={run.get('status')}")
        else:
            self.log("despacho enviado; GitHub aún no confirma la ejecución")
        return run

    def _start(self, chat_id, state: PaperState):
        if state.active:
            self.telegram.send_message(
                chat_id,
                "🟢 Ya hay una sesión PAPER <b>ACTIVA</b> "
                f"(run <code>{_run_id(state.active[0])}</code>); no se lanza otra.\n"
                "Usa /session_stop para detenerla.",
            )
            return
        if state.pending:
            self.telegram.send_message(
                chat_id,
                "🟡 Ya hay una sesión PAPER <b>EN COLA</b> "
                f"(run <code>{_run_id(state.pending[0])}</code>); no se lanza otra.",
            )
            return
        run = self._dispatch(state, manual_session=True)
        if run is not None:
            confirmation = (
                f"GitHub creó la ejecución <code>{_run_id(run)}</code> "
                f"(estado: {html.escape(str(run.get('status') or '?'))}).\n"
            )
        else:
            confirmation = (
                "Solicitud enviada; GitHub aún no confirma la ejecución "
                "(pendiente de confirmación).\n"
            )
        self.telegram.send_message(
            chat_id,
            "✅ <b>Workflow PAPER solicitado</b>\n\n"
            f"{confirmation}"
            "El runner restaurará el estado persistido y conectará WEEX.\n"
            "El bot te avisará cuando empiece a trabajar.\n"
            "Usa /session para comprobar cuándo está ACTIVO.",
        )

    def _wait_for_cancelled(self, run_ids):
        """Return the run ids GitHub reports as completed after the cancel."""
        done = set()
        for attempt in range(CONFIRM_ATTEMPTS):
            if attempt:
                self._sleep(CONFIRM_INTERVAL_SECONDS)
            for run_id in run_ids:
                if run_id in done:
                    continue
                try:
                    run = self.github.get_run(run_id)
                except ControllerError as exc:
                    self.log(f"aviso: no se pudo consultar el run {run_id}: {exc}")
                    continue
                if run.get("status") == COMPLETED_STATUS:
                    done.add(run_id)
            if len(done) == len(run_ids):
                break
        return done

    def _stop(self, chat_id, state: PaperState):
        if any(not isinstance(run.get("id"), int) for run in state.pending):
            # Hay un despacho aún sin confirmar: se pregunta a GitHub por el
            # run real antes de decidir qué cancelar.
            try:
                fresh = self.github.paper_state()
                state.active, state.pending, state.runs = fresh.active, fresh.pending, fresh.runs
            except ControllerError as exc:
                self.log(f"aviso: no se pudo refrescar el estado antes de parar: {exc}")
        targets = [run for run in list(state.pending) + list(state.active)
                   if isinstance(run.get("id"), int)]
        if not targets and state.pending:
            self.telegram.send_message(
                chat_id,
                "🟡 La sesión PAPER se acaba de solicitar y GitHub aún no ha creado la "
                "ejecución.\nVuelve a enviar /session_stop en unos segundos.",
            )
            return
        if not targets:
            self.telegram.send_message(
                chat_id,
                "ℹ️ No hay ninguna sesión PAPER en ejecución ni en cola que cancelar.\n"
                "Si el runner está activo, /session_stop lo detiene desde dentro.",
            )
            return
        ids = []
        for run in targets:
            self.github.cancel_run(run.get("id"))
            ids.append(run.get("id"))
        self.log("cancelación solicitada: runs=%s" % ",".join(str(i) for i in ids))
        done = self._wait_for_cancelled(ids)
        state.pending = [r for r in state.pending if r.get("id") not in ids]
        state.active = [r for r in state.active if r.get("id") not in ids]
        listed = ", ".join(html.escape(str(i)) for i in ids)
        if len(done) == len(ids):
            self.telegram.send_message(
                chat_id,
                "🔴 <b>Parada confirmada</b>\n\n"
                f"Runs cancelados: {listed}\n"
                "GitHub confirma que la ejecución terminó.",
            )
            return
        self.telegram.send_message(
            chat_id,
            "🟠 <b>Cancelación solicitada</b>\n\n"
            f"Runs: {listed}\n"
            "GitHub aceptó la cancelación pero aún no confirma el fin "
            "(es asíncrona). Confirma con /session.",
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

    def _notify_admin(self, text: str, key: str | None = None):
        """Best-effort notification; never raises.

        With ``key`` the same kind of failure is notified at most once every
        ``ERROR_NOTIFY_INTERVAL_SECONDS`` (the loop would otherwise spam the
        admin every minute).
        """
        if not self.admin_chat_id:
            return
        if key is not None:
            now = self._monotonic()
            last = self._last_error_notify.get(key)
            if last is not None and now - last < ERROR_NOTIFY_INTERVAL_SECONDS:
                return
            self._last_error_notify[key] = now
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
            return False
        window_start = self.policy.window_start(now)
        covered = state.session_in_window(window_start)
        if covered:
            run = covered[0]
            self.log(
                "la sesión PAPER de hoy ya existió (run=%s conclusion=%s); no se duplica"
                % (run.get("id"), run.get("conclusion"))
            )
            return False
        failed = state.failed_in_window(window_start)
        if len(failed) >= MAX_AUTO_ATTEMPTS:
            day = self.policy.local_now(now).date()
            if self._exhausted_notified_day != day:
                self._exhausted_notified_day = day
                self.log(
                    f"se alcanzó el máximo de intentos automáticos ({MAX_AUTO_ATTEMPTS}); "
                    "no se reintenta"
                )
                runs = ", ".join(html.escape(str(r.get("id"))) for r in failed)
                self._notify_admin(
                    "❌ <b>La sesión PAPER automática no arrancó</b>\n\n"
                    f"Se agotaron los {MAX_AUTO_ATTEMPTS} intentos automáticos de hoy "
                    f"(runs fallidos: {runs}).\n"
                    "Revisa el workflow <code>TradingBot - Paper Session</code> en Actions.\n"
                    "Usa /session_start para relanzarla manualmente."
                )
            return False
        try:
            # manual_session=false: the runner applies the 08:00-14:00 local
            # window and exits when the window closes.
            run = self._dispatch(state, manual_session=False)
        except ControllerError as exc:
            self.had_errors = True
            self.log(f"ERROR: {exc}")
            self._notify_admin(
                "❌ <b>Falló el inicio automático de la sesión PAPER</b>\n\n"
                f"<code>{html.escape(str(exc))}</code>",
                key="auto_start",
            )
            return False
        attempt = len(failed) + 1
        if run is not None:
            confirmation = (
                f"GitHub creó la ejecución <code>{_run_id(run)}</code> "
                f"(estado: {html.escape(str(run.get('status') or '?'))}).\n"
            )
        else:
            confirmation = "Solicitud enviada; GitHub aún no confirma la ejecución.\n"
        self.log(
            f"inicio automático de la sesión PAPER (intento {attempt}/{MAX_AUTO_ATTEMPTS}); "
            f"run={run.get('id') if run else 'pendiente'}"
        )
        self._notify_admin(
            "🕗 <b>Inicio automático de la sesión PAPER</b>\n\n"
            f"Hora de Cuba: <code>{self.policy.local_clock(now)}</code> "
            f"({self.policy.tz_name})\n"
            f"Intento: {attempt}/{MAX_AUTO_ATTEMPTS}\n"
            f"{confirmation}"
            "El runner restaurará el estado y conectará WEEX.\n"
            "🔒 Modo: PAPER (sin operaciones reales)\n"
            "El bot te avisará cuando empiece a trabajar."
        )
        return True

    # ------------------------------------------------------------------
    # Telegram polling
    # ------------------------------------------------------------------
    def _poll(self, timeout: int = POLL_TIMEOUT):
        """Fetch pending updates with bounded retries for transient errors."""
        last_error = None
        for attempt in range(len(RETRY_DELAYS) + 1):
            try:
                return self.telegram.get_updates(timeout=timeout, limit=100)
            except (TelegramNetworkError, TelegramServerError) as exc:
                last_error = exc
                if attempt >= len(RETRY_DELAYS):
                    break
                delay = RETRY_DELAYS[attempt] + random.uniform(0, 0.5)
                self.log(f"error transitorio de Telegram: {exc}; reintento en {delay:.1f}s")
                self._sleep(delay)
        raise last_error

    def _unknown_hint(self, token: str, state: PaperState) -> str:
        """Respuesta para un comando que este controlador no gestiona.

        Los comandos de trading (/positions, /risk, /performance, ...) los
        atiende el runner, no el controlador.  Si ya hay un run en cola o en
        marcha, decirle al usuario que "falta una sesión PAPER activa" sería
        falso: la sesión ya se está poniendo en marcha y el comando le llegará
        al runner en cuanto esté escuchando.
        """
        escaped = html.escape(token)
        if state.active or state.pending:
            return (
                "ℹ️ El comando <code>" + escaped + "</code> lo atiende el runner "
                "de PAPER, que ya se está poniendo en marcha.\n"
                "Espérate ~1 minuto y vuelve a intentarlo; si no responde, "
                "consulta el estado con /session."
            )
        return (
            "ℹ️ El comando <code>" + escaped + "</code> necesita una sesión PAPER "
            "activa.\nUsa /session_start para arrancarla y /help para ver los "
            "comandos."
        )

    def _message_time(self, message) -> datetime | None:
        try:
            return datetime.fromtimestamp(int(message.get("date")), tz=timezone.utc)
        except (TypeError, ValueError, OSError):
            return None

    def _process(self, updates, state: PaperState):
        """Process a batch of updates.

        Returns ``(failures, commands)`` where ``commands`` counts the
        authorized control commands actually executed.
        """
        handled = 0
        failures = 0
        last_id = None
        runner_until = state.last_finished_at()
        now = self._now_utc()
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
            sent_at = self._message_time(message)
            if sent_at is not None and runner_until is not None and sent_at <= runner_until:
                # Llegó mientras un runner PAPER era el dueño del token; el
                # runner pudo atenderlo sin confirmar el offset antes de
                # terminar.  Reejecutarlo (p. ej. un /stop o un /start) sería
                # una acción duplicada.
                self.log(f"comando de la sesión anterior ignorado: {token} (update_id={update_id})")
                continue
            action = COMMANDS.get(token)
            if action is None:
                if token.startswith("/"):
                    try:
                        self.telegram.send_message(chat_id, self._unknown_hint(token, state))
                    except ControllerError as exc:
                        self.log(f"aviso: no se pudo responder al comando: {exc}")
                continue
            if (action in LIFECYCLE_ACTIONS and sent_at is not None
                    and (now - sent_at).total_seconds() > COMMAND_MAX_AGE_SECONDS):
                age = int((now - sent_at).total_seconds() // 60)
                self.log(f"comando caducado ignorado: {token} ({age} min)")
                try:
                    self.telegram.send_message(
                        chat_id,
                        f"⏱️ Ignoré <code>{html.escape(token)}</code> enviado hace {age} min: "
                        "ha caducado.\nReenvíalo si sigue siendo necesario.",
                    )
                except ControllerError as exc:
                    self.log(f"aviso: no se pudo responder al comando: {exc}")
                continue
            try:
                self.log(f"comando {token} (update_id={update_id})")
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
        if failures:
            self.had_errors = True
        return failures, handled

    # ------------------------------------------------------------------
    # Loop
    # ------------------------------------------------------------------
    def _remaining(self, deadline: float) -> float:
        return deadline - self._monotonic()

    def _pause(self, seconds: float, deadline: float):
        remaining = self._remaining(deadline)
        if remaining <= 0:
            return
        self._sleep(max(0.0, min(seconds, remaining)))

    def run(self) -> int:
        """Run the controller.

        With ``runtime_seconds == 0`` a single pass is executed (state, auto
        start, one poll).  Otherwise the controller keeps working until the
        runtime is over and the workflow relays to the next job.  Returns 0
        on a clean run and 1 when an error happened (the job turns red, which
        keeps failures visible in the Actions tab).
        """
        single_pass = self.runtime_seconds == 0
        deadline = self._monotonic() + self.runtime_seconds
        state = None
        next_refresh = 0.0
        while True:
            if state is None or self._monotonic() >= next_refresh:
                try:
                    state = self.github.paper_state()
                except ControllerError as exc:
                    self.had_errors = True
                    self.log(f"ERROR: {exc}")
                    self._notify_admin(
                        "❌ <b>El controlador no puede consultar GitHub</b>\n\n"
                        f"<code>{html.escape(str(exc))}</code>",
                        key="paper_state",
                    )
                    if single_pass or self._remaining(deadline) <= 0:
                        return 1
                    state = None
                    self._pause(ERROR_BACKOFF_SECONDS, deadline)
                    continue
                next_refresh = self._monotonic() + self.state_refresh_seconds

            if state.active:
                # El runner en ejecución es el dueño del token: consumir updates
                # aquí competiría con él (HTTP 409) y podría perder comandos.
                if not self._yielding:
                    self._yielding = True
                    self.log(
                        "sesión PAPER en ejecución (runs=%s); el runner escucha "
                        "Telegram, el controlador cede"
                        % ",".join(str(r.get("id")) for r in state.active)
                    )
                if single_pass or self._remaining(deadline) <= 0:
                    return 1 if self.had_errors else 0
                self._pause(self.state_refresh_seconds, deadline)
                state = None
                continue
            if self._yielding:
                self._yielding = False
                self.log("no hay runner PAPER activo; el controlador retoma Telegram")

            self._auto_start(state)

            if state.pending and not single_pass:
                # Un run en cola todavía no consume el token: el controlador
                # atiende Telegram (p. ej. para cancelarlo) pero refresca el
                # estado antes para ceder en cuanto el runner arranque.
                next_refresh = min(next_refresh, self._monotonic() + 15)

            if single_pass:
                poll_timeout = POLL_TIMEOUT
            else:
                # El long poll no se extiende más allá del próximo refresco de
                # estado ni del final del job.
                horizon = min(self._remaining(deadline), next_refresh - self._monotonic())
                poll_timeout = int(max(1, min(POLL_TIMEOUT, horizon)))
            try:
                updates = self._poll(timeout=poll_timeout)
            except TelegramConflictError as exc:
                # Otro consumidor (el runner) ya tomó el token: el controlador
                # cede el polling y vuelve a consultar el estado.
                self.log(f"409 de Telegram; otro consumidor está activo: {exc}")
                if single_pass:
                    return 1 if self.had_errors else 0
                state = None
                self._pause(10, deadline)
                if self._remaining(deadline) <= 0:
                    return 1 if self.had_errors else 0
                continue
            except TelegramAuthError as exc:
                self.fatal = True
                self.log(f"ERROR: {exc}")
                return 1
            except ControllerError as exc:
                self.had_errors = True
                self.log(f"ERROR: {exc}")
                if single_pass or self._remaining(deadline) <= 0:
                    return 1
                self._pause(ERROR_BACKOFF_SECONDS, deadline)
                continue

            if updates:
                self._process(updates, state)
            elif single_pass:
                self.log("no hay comandos nuevos")

            if single_pass or self._remaining(deadline) <= 0:
                self.log("ciclo del controlador finalizado")
                return 1 if self.had_errors else 0


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
        "runtime_seconds": _int_env("CONTROLLER_RUNTIME_SECONDS", DEFAULT_RUNTIME_SECONDS),
        "run_id": os.environ.get("GITHUB_RUN_ID", "").strip(),
        "event": os.environ.get("GITHUB_EVENT_NAME", "").strip(),
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
    if not values["admin_chat_id"].lstrip("-").isdigit():
        raise ControllerError("TELEGRAM_ADMIN_CHAT_ID debe ser un chat id numérico")
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


def _write_output(name: str, value: str):
    """Expose a step output (used by the relay step of the workflow)."""
    path = os.environ.get("GITHUB_OUTPUT", "").strip()
    if not path:
        return
    try:
        with open(path, "a", encoding="utf-8") as fh:
            fh.write(f"{name}={value}\n")
    except OSError:  # pragma: no cover - depends on the runner
        pass


def main() -> int:
    try:
        env = _validate_env()
    except ControllerError as exc:
        print(f"controller error: {exc}", file=sys.stderr)
        # Configuración inválida: relanzar el controlador no lo arregla.
        _write_output("relay", "false")
        return 2
    github = GitHubClient(env["gh_token"], env["repository"], ref=env["dispatch_ref"])
    telegram = TelegramClient(env["tg_token"])
    policy = AutoStartPolicy(
        tz_name=env["timezone"],
        start_hour=env["auto_start_hour"],
        deadline_hour=env["auto_start_deadline_hour"],
    )
    controller = Controller(github, telegram, env["admin_chat_id"], policy=policy,
                            runtime_seconds=env["runtime_seconds"],
                            run_context={"run_id": env["run_id"], "event": env["event"]})
    controller.log(
        f"controlador iniciado: repo={env['repository']} ref={env['dispatch_ref']} "
        f"runtime={controller.runtime_seconds}s zona={policy.tz_name} "
        f"ventana={policy.start_hour:02d}:00-{policy.deadline_hour:02d}:00"
    )
    try:
        code = controller.run()
    except ControllerError as exc:
        print(f"controller error: {exc}", file=sys.stderr)
        return 1
    except Exception as exc:  # pragma: no cover - defensive
        print(f"controller error: {_sanitize(str(exc), [env['gh_token'], env['tg_token']])}",
              file=sys.stderr)
        return 1
    if controller.fatal:
        # El token de Telegram es inválido: relanzar el controlador no lo
        # arregla; el cron de vigilancia lo reintentará más tarde.
        _write_output("relay", "false")
        return 2
    return code


if __name__ == "__main__":
    sys.exit(main())
