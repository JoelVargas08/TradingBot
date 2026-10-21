#!/usr/bin/env python3
"""Unit tests for telegram_workflow_controller.

No real network, tokens or trading orders are involved: GitHub and Telegram
are replaced by fakes that record the HTTP requests.
"""
from __future__ import annotations

import datetime as dt
import io
import json
import os
import socket
import sys
import unittest
import urllib.error
import urllib.parse
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import telegram_workflow_controller as ctl  # noqa: E402

ADMIN = "123456"
UTC = dt.timezone.utc


def _utc(year, month, day, hour=0, minute=0) -> dt.datetime:
    return dt.datetime(year, month, day, hour, minute, tzinfo=UTC)


def _json_body(payload) -> bytes:
    return json.dumps(payload).encode()


def _http_error(url: str, code: int, payload) -> urllib.error.HTTPError:
    return urllib.error.HTTPError(url, code, "err", {}, io.BytesIO(_json_body(payload)))


def _header(request, name: str) -> str:
    for key, value in request.headers.items():
        if key.lower() == name.lower():
            return value
    return ""


class FakeGitHubOpener:
    def __init__(self):
        self.requests = []
        self.workflow_runs = []
        self.list_status = 200
        self.dispatch_status = 204
        self.cancel_status = 202
        self.dispatches = []
        self.cancels = []

    def __call__(self, request, timeout):
        self.requests.append(request)
        url = request.full_url
        if "/dispatches" in url:
            if self.dispatch_status >= 400:
                raise _http_error(url, self.dispatch_status, {"message": "forbidden"})
            self.dispatches.append(json.loads(request.data.decode()))
            return self.dispatch_status, b""
        if "/cancel" in url:
            if self.cancel_status >= 400:
                raise _http_error(url, self.cancel_status, {"message": "not found"})
            self.cancels.append(url)
            return self.cancel_status, b""
        if "/workflows/" in url and "/runs" in url:
            if self.list_status >= 400:
                raise _http_error(url, self.list_status, {"message": "unauthorized"})
            return 200, _json_body({"workflow_runs": self.workflow_runs})
        raise AssertionError("URL inesperada: " + url)


class FakeTelegramOpener:
    def __init__(self):
        self.updates = []
        self.sent = []
        self.offsets = []
        self.conflict = False
        self.auth_error = False
        self.server_error = False
        self.timeout_calls = 0
        self.get_updates_calls = 0
        self.poll_calls = 0
        # Lotes opcionales: cada long poll devuelve el lote siguiente; el
        # último se repite.  Sirve para simular que un comando llega tarde
        # dentro de la ventana de escucha.
        self.batches = None

    def __call__(self, request, timeout):
        params = urllib.parse.parse_qs(request.data.decode())
        method = request.full_url.rsplit("/", 1)[-1]
        if method == "getUpdates":
            self.get_updates_calls += 1
            if self.timeout_calls > 0:
                self.timeout_calls -= 1
                raise socket.timeout("timed out")
            if self.conflict:
                raise _http_error(request.full_url, 409,
                                  {"ok": False, "description": "Conflict: terminated by other getUpdates request"})
            if self.auth_error:
                raise _http_error(request.full_url, 401,
                                  {"ok": False, "description": "Unauthorized"})
            if self.server_error:
                raise _http_error(request.full_url, 502,
                                  {"ok": False, "description": "Bad Gateway"})
            if "offset" in params:
                self.offsets.append(int(params["offset"][0]))
                return 200, _json_body({"ok": True, "result": []})
            self.poll_calls += 1
            if self.batches is not None:
                index = min(self.poll_calls - 1, len(self.batches) - 1)
                return 200, _json_body({"ok": True, "result": self.batches[index]})
            return 200, _json_body({"ok": True, "result": self.updates})
        if method == "sendMessage":
            self.sent.append({k: v[0] for k, v in params.items()})
            return 200, _json_body({"ok": True, "result": {"message_id": 1}})
        raise AssertionError("método inesperado: " + method)


def message(update_id, text, chat_id=ADMIN):
    return {"update_id": update_id, "message": {"text": text, "chat": {"id": chat_id}}}


def run(run_id, status, created_at="2026-01-15T13:00:00Z"):
    return {"id": run_id, "status": status, "created_at": created_at}


def build_controller(gh_opener, tg_opener, admin=ADMIN, now=None, policy=None,
                    poll_window_seconds=0):
    github = ctl.GitHubClient("ghtoken", "owner/repo", opener=gh_opener)
    telegram = ctl.TelegramClient("tgtoken", opener=tg_opener)
    logs = []
    # La hora por defecto queda fuera de la ventana de arranque automático para
    # que los tests no despachen sesiones PAPER por accidente.
    # poll_window_seconds=0: los tests terminan el ciclo en una vuelta; la
    # ventana continua se cubre en PollWindowTests.
    controller = ctl.Controller(
        github, telegram, admin, log=logs.append,
        now=now or (lambda: _utc(2026, 1, 15, 20, 0)),
        policy=policy or ctl.AutoStartPolicy(),
        poll_window_seconds=poll_window_seconds,
    )
    return controller, logs


class ValidateEnvTests(unittest.TestCase):
    def test_missing_telegram_token(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            with self.assertRaises(ctl.ControllerError) as ctx:
                ctl._validate_env()
            self.assertIn("TELEGRAM_BOT_TOKEN", str(ctx.exception))

    def test_missing_gh_token(self):
        env = {"TELEGRAM_BOT_TOKEN": "t", "GITHUB_REPOSITORY": "o/r", "TELEGRAM_ADMIN_CHAT_ID": ADMIN}
        with mock.patch.dict(os.environ, env, clear=True):
            with self.assertRaises(ctl.ControllerError) as ctx:
                ctl._validate_env()
            self.assertIn("GH_TOKEN", str(ctx.exception))

    def test_escaped_repository_is_rejected(self):
        env = {"TELEGRAM_BOT_TOKEN": "t", "GH_TOKEN": "g",
               "GITHUB_REPOSITORY": "\\owner/repo", "TELEGRAM_ADMIN_CHAT_ID": ADMIN}
        with mock.patch.dict(os.environ, env, clear=True):
            with self.assertRaises(ctl.ControllerError) as ctx:
                ctl._validate_env()
            self.assertIn("GITHUB_REPOSITORY", str(ctx.exception))

    def test_missing_admin_blocks_control(self):
        env = {"TELEGRAM_BOT_TOKEN": "t", "GH_TOKEN": "g", "GITHUB_REPOSITORY": "o/r"}
        with mock.patch.dict(os.environ, env, clear=True):
            with self.assertRaises(ctl.ControllerError) as ctx:
                ctl._validate_env()
            self.assertIn("TELEGRAM_ADMIN_CHAT_ID", str(ctx.exception))

    def test_valid_env(self):
        env = {"TELEGRAM_BOT_TOKEN": "t", "GH_TOKEN": "g",
               "GITHUB_REPOSITORY": "o/r", "TELEGRAM_ADMIN_CHAT_ID": ADMIN}
        with mock.patch.dict(os.environ, env, clear=True):
            values = ctl._validate_env()
        self.assertEqual(values["repository"], "o/r")
        self.assertEqual(values["timezone"], ctl.DEFAULT_TIMEZONE)
        self.assertEqual(values["auto_start_hour"], 8)

    def test_invalid_auto_start_window_is_rejected(self):
        env = {"TELEGRAM_BOT_TOKEN": "t", "GH_TOKEN": "g", "GITHUB_REPOSITORY": "o/r",
               "TELEGRAM_ADMIN_CHAT_ID": ADMIN,
               "TRADINGBOT_AUTO_START_HOUR": "10", "TRADINGBOT_AUTO_START_DEADLINE_HOUR": "9"}
        with mock.patch.dict(os.environ, env, clear=True):
            with self.assertRaises(ctl.ControllerError) as ctx:
                ctl._validate_env()
            self.assertIn("DEADLINE", str(ctx.exception))


class GitHubClientTests(unittest.TestCase):
    def test_authorization_header_uses_bearer(self):
        opener = FakeGitHubOpener()
        client = ctl.GitHubClient("secret-token", "owner/repo", opener=opener)
        client.list_paper_runs()
        self.assertTrue(opener.requests)
        self.assertEqual(_header(opener.requests[0], "Authorization"), "Bearer secret-token")

    def test_401_raises_github_error(self):
        opener = FakeGitHubOpener()
        opener.list_status = 401
        client = ctl.GitHubClient("tok", "owner/repo", opener=opener)
        with self.assertRaises(ctl.GitHubError) as ctx:
            client.list_paper_runs()
        self.assertEqual(ctx.exception.status, 401)
        self.assertIn("autenticación", str(ctx.exception))

    def test_403_raises_github_error(self):
        opener = FakeGitHubOpener()
        opener.list_status = 403
        client = ctl.GitHubClient("tok", "owner/repo", opener=opener)
        with self.assertRaises(ctl.GitHubError) as ctx:
            client.list_paper_runs()
        self.assertEqual(ctx.exception.status, 403)

    def test_error_message_never_leaks_token(self):
        opener = FakeGitHubOpener()
        opener.list_status = 401
        client = ctl.GitHubClient("super-secret", "owner/repo", opener=opener)
        with self.assertRaises(ctl.GitHubError) as ctx:
            client.list_paper_runs()
        self.assertNotIn("super-secret", str(ctx.exception))

    def test_paper_state_splits_active_and_pending(self):
        opener = FakeGitHubOpener()
        opener.workflow_runs = [
            run(1, "in_progress"),
            run(2, "queued"),
            run(3, "completed"),
        ]
        client = ctl.GitHubClient("tok", "owner/repo", opener=opener)
        state = client.paper_state()
        self.assertEqual([r["id"] for r in state.active], [1])
        self.assertEqual([r["id"] for r in state.pending], [2])
        self.assertTrue(state.has_session)


class ControllerTests(unittest.TestCase):
    def test_active_run_does_not_consume_telegram(self):
        gh = FakeGitHubOpener()
        gh.workflow_runs = [run(7, "in_progress")]
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/session_start")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(tg.sent, [])
        self.assertEqual(tg.offsets, [])
        self.assertEqual(gh.dispatches, [])

    def test_pending_run_keeps_controller_owning_telegram(self):
        """Un run en cola todavía no consume el token: el controlador atiende.

        Es lo que permite cancelar por Telegram una ejecución que aún no ha
        arrancado (el runner no puede recibir el comando).
        """
        gh = FakeGitHubOpener()
        gh.workflow_runs = [run(7, "queued")]
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/session_stop")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        # 1 poll + 1 confirmación del offset.
        self.assertEqual(tg.get_updates_calls, 2)
        self.assertEqual(len(gh.cancels), 1)
        self.assertIn("/runs/7/cancel", gh.cancels[0])

    def test_pending_run_does_not_duplicate_the_start(self):
        gh = FakeGitHubOpener()
        gh.workflow_runs = [run(7, "queued")]
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/session_start")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(gh.dispatches, [])
        self.assertIn("cola", tg.sent[0]["text"].lower())

    def test_no_runs_no_updates_exits_clean(self):
        controller, logs = build_controller(FakeGitHubOpener(), FakeTelegramOpener())
        self.assertEqual(controller.run(), 0)
        self.assertTrue(any("no hay comandos" in log for log in logs))

    def test_ping_is_answered(self):
        tg = FakeTelegramOpener()
        tg.updates = [message(11, "/ping")]
        controller, _ = build_controller(FakeGitHubOpener(), tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(len(tg.sent), 1)
        self.assertIn("Pong", tg.sent[0]["text"])
        self.assertEqual(tg.offsets, [12])

    def test_help_is_answered(self):
        tg = FakeTelegramOpener()
        tg.updates = [message(11, "/help")]
        controller, _ = build_controller(FakeGitHubOpener(), tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(len(tg.sent), 1)
        self.assertIn("/session_start", tg.sent[0]["text"])
        self.assertIn("/session_stop", tg.sent[0]["text"])

    def test_session_reports_stopped(self):
        tg = FakeTelegramOpener()
        tg.updates = [message(42, "/session")]
        controller, _ = build_controller(FakeGitHubOpener(), tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(len(tg.sent), 1)
        self.assertIn("DETENIDA", tg.sent[0]["text"])
        self.assertEqual(tg.offsets, [43])

    def test_status_text_reports_active_and_pending(self):
        controller, _ = build_controller(FakeGitHubOpener(), FakeTelegramOpener())
        active = ctl.PaperState(active=[run(11, "in_progress")], pending=[])
        pending = ctl.PaperState(active=[], pending=[run(12, "queued")])
        stopped = ctl.PaperState()
        self.assertIn("ACTIVA", controller._status_text(active))
        self.assertIn("EN COLA", controller._status_text(pending))
        self.assertIn("DETENIDA", controller._status_text(stopped))

    def test_start_dispatches_workflow(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/session_start")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(len(gh.dispatches), 1)
        self.assertEqual(gh.dispatches[0]["inputs"]["manual_session"], "true")
        self.assertIn("solicitado", tg.sent[0]["text"].lower())

    def test_start_alias_dispatches_workflow(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/start")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(len(gh.dispatches), 1)

    def test_start_with_pending_does_not_dispatch_duplicate(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        controller, _ = build_controller(gh, tg)
        state = ctl.PaperState(pending=[run(3, "queued")])
        controller._start(ADMIN, state)
        self.assertEqual(gh.dispatches, [])
        self.assertIn("cola", tg.sent[0]["text"].lower())

    def test_start_with_active_does_not_dispatch_duplicate(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        controller, _ = build_controller(gh, tg)
        state = ctl.PaperState(active=[run(4, "in_progress")])
        controller._start(ADMIN, state)
        self.assertEqual(gh.dispatches, [])
        self.assertIn("activa", tg.sent[0]["text"].lower())

    def test_stop_cancels_pending_run(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        controller, _ = build_controller(gh, tg)
        state = ctl.PaperState(pending=[run(11, "queued")])
        controller._stop(ADMIN, state)
        self.assertEqual(len(gh.cancels), 1)
        self.assertIn("/runs/11/cancel", gh.cancels[0])

    def test_stop_cancels_active_run(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        controller, _ = build_controller(gh, tg)
        state = ctl.PaperState(active=[run(12, "in_progress")])
        controller._stop(ADMIN, state)
        self.assertEqual(len(gh.cancels), 1)
        self.assertIn("/runs/12/cancel", gh.cancels[0])

    def test_stop_without_pending_informs(self):
        tg = FakeTelegramOpener()
        controller, _ = build_controller(FakeGitHubOpener(), tg)
        state = ctl.PaperState()
        controller._stop(ADMIN, state)
        self.assertIn("No hay ninguna sesión", tg.sent[0]["text"])

    def test_unauthorized_chat_is_ignored(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/session_start", chat_id=999)]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(gh.dispatches, [])
        self.assertEqual(tg.sent, [])

    def test_unknown_command_gets_hint(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/positions")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(gh.dispatches, [])
        self.assertEqual(len(tg.sent), 1)
        self.assertIn("/positions", tg.sent[0]["text"])

    def test_dispatch_rejected_notifies_and_fails(self):
        gh = FakeGitHubOpener()
        gh.dispatch_status = 403
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/session_start")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 1)
        self.assertIn("rechazó", tg.sent[0]["text"].lower())
        self.assertEqual(tg.offsets, [2])

    def test_telegram_conflict_yields_without_consuming(self):
        tg = FakeTelegramOpener()
        tg.conflict = True
        controller, logs = build_controller(FakeGitHubOpener(), tg)
        self.assertEqual(controller.run(), 0)
        self.assertTrue(any("409" in log for log in logs))
        self.assertEqual(tg.offsets, [])

    def test_telegram_auth_error_is_reported_without_secret(self):
        tg = FakeTelegramOpener()
        tg.auth_error = True
        controller, logs = build_controller(FakeGitHubOpener(), tg)
        self.assertEqual(controller.run(), 1)
        self.assertTrue(any("401" in log for log in logs))
        self.assertFalse(any("tgtoken" in log for log in logs))

    def test_transient_timeout_is_retried(self):
        tg = FakeTelegramOpener()
        tg.timeout_calls = 1
        tg.updates = [message(3, "/ping")]
        controller, logs = build_controller(FakeGitHubOpener(), tg)
        with mock.patch("telegram_workflow_controller.time.sleep"):
            self.assertEqual(controller.run(), 0)
        # 1 timeout + 1 poll correcto + 1 confirmación del offset.
        self.assertEqual(tg.get_updates_calls, 3)
        self.assertEqual(len(tg.sent), 1)
        self.assertIn("Pong", tg.sent[0]["text"])

    def test_html_is_escaped_in_status(self):
        controller, _ = build_controller(FakeGitHubOpener(), FakeTelegramOpener())
        state = ctl.PaperState(pending=[run("<b>x</b>", "queued")])
        text = controller._status_text(state)
        self.assertIn("&lt;b&gt;x&lt;/b&gt;", text)
        self.assertNotIn("<b>x</b>", text)

    def test_confirmation_uses_next_offset(self):
        tg = FakeTelegramOpener()
        tg.updates = [message(100, "/session")]
        controller, _ = build_controller(FakeGitHubOpener(), tg)
        controller.run()
        self.assertEqual(tg.offsets, [101])

    def test_all_commands_in_batch_are_processed_once(self):
        tg = FakeTelegramOpener()
        tg.updates = [message(10, "/ping"), message(11, "/session")]
        controller, _ = build_controller(FakeGitHubOpener(), tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(len(tg.sent), 2)
        # Un único offset: el último update atendido + 1.
        self.assertEqual(tg.offsets, [12])


class AutoStartTests(unittest.TestCase):
    """Arranque automático de la sesión PAPER a las 08:00 de Cuba."""

    def _controller(self, gh, tg, now, policy=None):
        return build_controller(gh, tg, now=now, policy=policy)[0]

    def test_does_not_start_before_eight_havana(self):
        # 07:59 en La Habana (enero, UTC-5) -> 12:59 UTC.
        now = _utc(2026, 1, 15, 12, 59)
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        controller = self._controller(gh, tg, lambda: now)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(gh.dispatches, [])

    def test_starts_at_eight_havana(self):
        # 08:00 en La Habana (enero, UTC-5) -> 13:00 UTC.
        now = _utc(2026, 1, 15, 13, 0)
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        controller = self._controller(gh, tg, lambda: now)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(len(gh.dispatches), 1)
        self.assertEqual(gh.dispatches[0]["inputs"]["manual_session"], "false")
        self.assertEqual(len(tg.sent), 1)
        self.assertIn("automático", tg.sent[0]["text"].lower())
        # Tras despachar, el controlador no consume Telegram: el runner es el dueño.
        self.assertEqual(tg.get_updates_calls, 0)

    def test_start_follows_daylight_saving(self):
        # En julio La Habana es UTC-4: 08:00 local -> 12:00 UTC.
        now = _utc(2026, 7, 15, 12, 0)
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        controller = self._controller(gh, tg, lambda: now)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(len(gh.dispatches), 1)

    def test_does_not_start_on_weekend(self):
        # 2026-01-17 es sábado.
        now = _utc(2026, 1, 17, 13, 0)
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        controller = self._controller(gh, tg, lambda: now)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(gh.dispatches, [])

    def test_is_idempotent_within_the_day(self):
        now = _utc(2026, 1, 15, 13, 0)
        gh = FakeGitHubOpener()
        gh.workflow_runs = [run(99, "in_progress", created_at="2026-01-15T13:00:00Z")]
        tg = FakeTelegramOpener()
        controller = self._controller(gh, tg, lambda: now)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(gh.dispatches, [])

    def test_cancelled_session_is_not_restarted(self):
        now = _utc(2026, 1, 15, 14, 0)
        gh = FakeGitHubOpener()
        gh.workflow_runs = [run(99, "cancelled", created_at="2026-01-15T13:00:00Z")]
        tg = FakeTelegramOpener()
        controller = self._controller(gh, tg, lambda: now)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(gh.dispatches, [])

    def test_failed_run_is_retried(self):
        now = _utc(2026, 1, 15, 13, 30)
        gh = FakeGitHubOpener()
        gh.workflow_runs = [run(99, "failed", created_at="2026-01-15T13:00:00Z")]
        tg = FakeTelegramOpener()
        controller = self._controller(gh, tg, lambda: now)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(len(gh.dispatches), 1)

    def test_gives_up_after_max_attempts(self):
        now = _utc(2026, 1, 15, 13, 30)
        gh = FakeGitHubOpener()
        gh.workflow_runs = [
            run(1, "failed", created_at="2026-01-15T13:00:00Z"),
            run(2, "failed", created_at="2026-01-15T13:10:00Z"),
            run(3, "failed", created_at="2026-01-15T13:20:00Z"),
        ]
        tg = FakeTelegramOpener()
        controller = self._controller(gh, tg, lambda: now)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(gh.dispatches, [])
        self.assertEqual(len(tg.sent), 1)
        self.assertIn("no arrancó", tg.sent[0]["text"])

    def test_does_not_start_after_the_catch_up_window(self):
        # 13:30 en La Habana está fuera de la ventana (08:00-13:00).
        now = _utc(2026, 1, 15, 18, 30)
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        controller = self._controller(gh, tg, lambda: now)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(gh.dispatches, [])

    def test_uses_the_configured_timezone(self):
        policy = ctl.AutoStartPolicy(tz_name="America/Havana", start_hour=9,
                                     deadline_hour=14)
        now = _utc(2026, 1, 15, 13, 30)  # 08:30 en La Habana -> aún no
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        controller = self._controller(gh, tg, lambda: now, policy=policy)
        controller.run()
        self.assertEqual(gh.dispatches, [])
        now = _utc(2026, 1, 15, 14, 30)  # 09:30 en La Habana -> sí
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        controller = self._controller(gh, tg, lambda: now, policy=policy)
        controller.run()
        self.assertEqual(len(gh.dispatches), 1)


class PollWindowTests(unittest.TestCase):
    """Ventana de escucha continua dentro de un ciclo del controlador."""

    @staticmethod
    def _clock(step=100.0):
        state = {"t": 0.0}

        def monotonic():
            state["t"] += step
            return state["t"]

        return monotonic

    def test_window_keeps_polling_until_the_deadline(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        controller, logs = build_controller(gh, tg, poll_window_seconds=240)
        with mock.patch.object(ctl.time, "monotonic", self._clock()):
            self.assertEqual(controller.run(), 0)
        # Varias vueltas de long poll dentro de la misma ventana.
        self.assertGreaterEqual(tg.poll_calls, 3)
        self.assertTrue(any("ventana de escucha" in line for line in logs))

    def test_command_arriving_later_in_the_window_is_answered(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        tg.batches = [[], [], [message(7, "/ping")]]
        controller, logs = build_controller(gh, tg, poll_window_seconds=240)
        with mock.patch.object(ctl.time, "monotonic", self._clock()):
            self.assertEqual(controller.run(), 0)
        self.assertEqual(len(tg.sent), 1)
        self.assertIn("Pong", tg.sent[0]["text"])

    def test_window_is_clamped(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        controller, _ = build_controller(gh, tg, poll_window_seconds=10_000)
        self.assertEqual(controller.poll_window_seconds, ctl.MAX_POLL_WINDOW_SECONDS)

    def test_window_env_is_read(self):
        env = {"TELEGRAM_BOT_TOKEN": "t", "GH_TOKEN": "g",
               "GITHUB_REPOSITORY": "o/r", "TELEGRAM_ADMIN_CHAT_ID": ADMIN,
               "CONTROLLER_POLL_WINDOW_SECONDS": "120"}
        with mock.patch.dict(os.environ, env, clear=True):
            values = ctl._validate_env()
        self.assertEqual(values["poll_window_seconds"], 120)

    def test_invalid_window_falls_back_to_the_default(self):
        env = {"TELEGRAM_BOT_TOKEN": "t", "GH_TOKEN": "g",
               "GITHUB_REPOSITORY": "o/r", "TELEGRAM_ADMIN_CHAT_ID": ADMIN,
               "CONTROLLER_POLL_WINDOW_SECONDS": "no-es-un-numero"}
        with mock.patch.dict(os.environ, env, clear=True):
            values = ctl._validate_env()
        self.assertEqual(values["poll_window_seconds"], ctl.DEFAULT_POLL_WINDOW_SECONDS)


if __name__ == "__main__":
    unittest.main()
