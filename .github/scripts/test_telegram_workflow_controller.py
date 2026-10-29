#!/usr/bin/env python3
"""Unit tests for telegram_workflow_controller.

No real network, tokens or trading orders are involved: GitHub and Telegram
are replaced by fakes that record the HTTP requests.  The fake runs use the
real GitHub vocabulary: a finished run is ``status=completed`` and its outcome
is in ``conclusion`` (GitHub never reports ``status=failed``).
"""
from __future__ import annotations

import datetime as dt
import io
import json
import os
import socket
import sys
import tempfile
import unittest
import urllib.error
import urllib.parse
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import telegram_workflow_controller as ctl  # noqa: E402

ADMIN = "123456"
UTC = dt.timezone.utc
# 2026-01-15 (jueves) 15:00 en La Habana (UTC-5): fuera de la ventana 08-13.
DEFAULT_NOW = dt.datetime(2026, 1, 15, 20, 0, tzinfo=UTC)


def _utc(year, month, day, hour=0, minute=0) -> dt.datetime:
    return dt.datetime(year, month, day, hour, minute, tzinfo=UTC)


def _iso(ts: dt.datetime) -> str:
    return ts.strftime("%Y-%m-%dT%H:%M:%SZ")


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
    """Fake GitHub API.  ``create_on_dispatch`` simulates the run GitHub creates."""

    def __init__(self, now=DEFAULT_NOW):
        self.requests = []
        self.workflow_runs = []
        self.list_status = 200
        self.dispatch_status = 204
        self.cancel_status = 202
        self.dispatches = []
        self.cancels = []
        self.create_on_dispatch = True
        self.complete_on_cancel = True
        self.now = now
        self._next_id = 1000

    def __call__(self, request, timeout):
        self.requests.append(request)
        url = request.full_url
        if "/dispatches" in url:
            if self.dispatch_status >= 400:
                raise _http_error(url, self.dispatch_status, {"message": "forbidden"})
            self.dispatches.append(json.loads(request.data.decode()))
            if self.create_on_dispatch:
                self._next_id += 1
                self.workflow_runs.insert(0, {
                    "id": self._next_id, "status": "queued", "conclusion": None,
                    "event": "workflow_dispatch", "created_at": _iso(self.now),
                    "updated_at": _iso(self.now),
                })
            return self.dispatch_status, b""
        if "/cancel" in url:
            if self.cancel_status >= 400:
                raise _http_error(url, self.cancel_status, {"message": "not found"})
            self.cancels.append(url)
            run_id = int(url.rsplit("/runs/", 1)[1].split("/")[0])
            if self.complete_on_cancel:
                for run in self.workflow_runs:
                    if run["id"] == run_id:
                        run["status"] = "completed"
                        run["conclusion"] = "cancelled"
            return self.cancel_status, b""
        if "/workflows/" in url and "/runs" in url:
            if self.list_status >= 400:
                raise _http_error(url, self.list_status, {"message": "unauthorized"})
            return 200, _json_body({"workflow_runs": self.workflow_runs})
        if "/actions/runs/" in url:
            run_id = int(url.rsplit("/runs/", 1)[1])
            for run in self.workflow_runs:
                if run["id"] == run_id:
                    return 200, _json_body(run)
            return 200, _json_body({"id": run_id, "status": "in_progress"})
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
        self.poll_timeouts = []
        # Lotes opcionales: cada long poll devuelve el lote siguiente; el
        # último se repite.  Sirve para simular que un comando llega tarde.
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
            self.poll_timeouts.append(int(params["timeout"][0]))
            if self.batches is not None:
                index = min(self.poll_calls - 1, len(self.batches) - 1)
                return 200, _json_body({"ok": True, "result": self.batches[index]})
            return 200, _json_body({"ok": True, "result": self.updates})
        if method == "sendMessage":
            self.sent.append({k: v[0] for k, v in params.items()})
            return 200, _json_body({"ok": True, "result": {"message_id": 1}})
        raise AssertionError("método inesperado: " + method)


def message(update_id, text, chat_id=ADMIN, sent_at=None):
    sent_at = sent_at or (DEFAULT_NOW - dt.timedelta(seconds=10))
    return {"update_id": update_id, "message": {
        "text": text, "chat": {"id": chat_id}, "date": int(sent_at.timestamp()),
    }}


def run(run_id, status, created_at="2026-01-15T13:00:00Z", conclusion=None,
        updated_at=None, event="workflow_dispatch"):
    return {"id": run_id, "status": status, "conclusion": conclusion, "event": event,
            "created_at": created_at, "updated_at": updated_at or created_at}


def done(run_id, conclusion, created_at="2026-01-15T13:00:00Z", updated_at=None):
    return run(run_id, "completed", created_at=created_at, conclusion=conclusion,
               updated_at=updated_at or created_at)


class FakeClock:
    """Monotonic clock driven by sleeps and long polls."""

    def __init__(self, tg=None, poll_cost=25.0):
        self.t = 0.0
        self.tg = tg
        self.poll_cost = poll_cost
        self._polls_seen = 0
        self.sleeps = []

    def monotonic(self):
        if self.tg is not None and self.tg.poll_calls > self._polls_seen:
            self.t += self.poll_cost * (self.tg.poll_calls - self._polls_seen)
            self._polls_seen = self.tg.poll_calls
        return self.t

    def sleep(self, seconds):
        self.sleeps.append(seconds)
        self.t += seconds


def build_controller(gh_opener, tg_opener, admin=ADMIN, now=None, policy=None,
                     runtime_seconds=0, clock=None, ref="main"):
    github = ctl.GitHubClient("ghtoken", "owner/repo", opener=gh_opener, ref=ref)
    telegram = ctl.TelegramClient("tgtoken", opener=tg_opener)
    logs = []
    clock = clock or FakeClock(tg_opener)
    controller = ctl.Controller(
        github, telegram, admin, log=logs.append,
        now=now or (lambda: DEFAULT_NOW),
        policy=policy or ctl.AutoStartPolicy(),
        runtime_seconds=runtime_seconds,
        sleep=clock.sleep, monotonic=clock.monotonic,
        run_context={"run_id": "42", "event": "schedule"},
    )
    return controller, logs


ENV_OK = {"TELEGRAM_BOT_TOKEN": "t", "GH_TOKEN": "g",
          "GITHUB_REPOSITORY": "o/r", "TELEGRAM_ADMIN_CHAT_ID": ADMIN}


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
        env = dict(ENV_OK, GITHUB_REPOSITORY="\\owner/repo")
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

    def test_non_numeric_admin_is_rejected(self):
        env = dict(ENV_OK, TELEGRAM_ADMIN_CHAT_ID="@yo")
        with mock.patch.dict(os.environ, env, clear=True):
            with self.assertRaises(ctl.ControllerError):
                ctl._validate_env()

    def test_valid_env(self):
        with mock.patch.dict(os.environ, ENV_OK, clear=True):
            values = ctl._validate_env()
        self.assertEqual(values["repository"], "o/r")
        self.assertEqual(values["timezone"], ctl.DEFAULT_TIMEZONE)
        self.assertEqual(values["auto_start_hour"], 8)
        self.assertEqual(values["dispatch_ref"], "main")
        self.assertEqual(values["runtime_seconds"], ctl.DEFAULT_RUNTIME_SECONDS)

    def test_invalid_auto_start_window_is_rejected(self):
        env = dict(ENV_OK, TRADINGBOT_AUTO_START_HOUR="10",
                   TRADINGBOT_AUTO_START_DEADLINE_HOUR="9")
        with mock.patch.dict(os.environ, env, clear=True):
            with self.assertRaises(ctl.ControllerError) as ctx:
                ctl._validate_env()
            self.assertIn("DEADLINE", str(ctx.exception))

    def test_runtime_env_is_read_and_invalid_falls_back(self):
        with mock.patch.dict(os.environ, dict(ENV_OK, CONTROLLER_RUNTIME_SECONDS="120"), clear=True):
            self.assertEqual(ctl._validate_env()["runtime_seconds"], 120)
        with mock.patch.dict(os.environ, dict(ENV_OK, CONTROLLER_RUNTIME_SECONDS="x"), clear=True):
            self.assertEqual(ctl._validate_env()["runtime_seconds"], ctl.DEFAULT_RUNTIME_SECONDS)

    def test_runtime_is_clamped(self):
        controller, _ = build_controller(FakeGitHubOpener(), FakeTelegramOpener(),
                                         runtime_seconds=10 ** 6)
        self.assertEqual(controller.runtime_seconds, ctl.MAX_RUNTIME_SECONDS)

    def test_invalid_env_disables_the_relay(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = os.path.join(tmp, "out.txt")
            with mock.patch.dict(os.environ, {"GITHUB_OUTPUT": out}, clear=True):
                with mock.patch("sys.stderr", new=io.StringIO()):
                    self.assertEqual(ctl.main(), 2)
            with open(out, encoding="utf-8") as fh:
                self.assertIn("relay=false", fh.read())


class GitHubClientTests(unittest.TestCase):
    def test_authorization_header_uses_bearer(self):
        opener = FakeGitHubOpener()
        client = ctl.GitHubClient("secret-token", "owner/repo", opener=opener)
        client.list_paper_runs()
        self.assertEqual(_header(opener.requests[0], "Authorization"), "Bearer secret-token")

    def test_401_403_404_raise_github_error(self):
        for status in (401, 403, 404):
            opener = FakeGitHubOpener()
            opener.list_status = status
            client = ctl.GitHubClient("tok", "owner/repo", opener=opener)
            with self.assertRaises(ctl.GitHubError) as ctx:
                client.list_paper_runs()
            self.assertEqual(ctx.exception.status, status)
        self.assertIn("autenticación", str(ctl.GitHubError(401, "/x", "")))

    def test_error_message_never_leaks_token(self):
        opener = FakeGitHubOpener()
        opener.list_status = 401
        client = ctl.GitHubClient("super-secret", "owner/repo", opener=opener)
        with self.assertRaises(ctl.GitHubError) as ctx:
            client.list_paper_runs()
        self.assertNotIn("super-secret", str(ctx.exception))

    def test_network_error_and_timeout_are_controller_errors(self):
        def unreachable(request, timeout):
            raise urllib.error.URLError("dns")

        def slow(request, timeout):
            raise socket.timeout("slow")

        for opener in (unreachable, slow):
            client = ctl.GitHubClient("tok", "owner/repo", opener=opener)
            with self.assertRaises(ctl.ControllerError):
                client.list_paper_runs()

    def test_paper_state_splits_active_pending_and_completed(self):
        state = ctl.PaperState.from_runs([
            run(1, "in_progress"),
            run(2, "queued"),
            run(3, "waiting"),
            done(4, "success"),
        ])
        self.assertEqual([r["id"] for r in state.active], [1])
        self.assertEqual([r["id"] for r in state.pending], [2, 3])
        self.assertTrue(state.has_session)

    def test_unknown_status_is_treated_as_pending(self):
        """Un estado desconocido no se interpreta como "no hay sesión"."""
        state = ctl.PaperState.from_runs([run(5, "estado_nuevo")])
        self.assertEqual([r["id"] for r in state.pending], [5])

    def test_dispatch_uses_the_configured_ref(self):
        opener = FakeGitHubOpener()
        client = ctl.GitHubClient("tok", "owner/repo", opener=opener, ref="release")
        client.dispatch_paper(manual_session=False)
        self.assertEqual(opener.dispatches[0]["ref"], "release")
        self.assertEqual(opener.dispatches[0]["inputs"]["manual_session"], "false")
        self.assertEqual(opener.dispatches[0]["inputs"]["restore_state"], "true")


class ControllerTests(unittest.TestCase):
    def test_active_run_does_not_consume_telegram(self):
        gh = FakeGitHubOpener()
        gh.workflow_runs = [run(7, "in_progress")]
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/session_start")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(tg.get_updates_calls, 0)
        self.assertEqual(tg.sent, [])
        self.assertEqual(gh.dispatches, [])

    def test_pending_run_keeps_controller_owning_telegram(self):
        """Un run en cola todavía no consume el token: el controlador atiende."""
        gh = FakeGitHubOpener()
        gh.workflow_runs = [run(7, "queued")]
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/session_stop")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(len(gh.cancels), 1)
        self.assertIn("/runs/7/cancel", gh.cancels[0])
        self.assertIn("Parada confirmada", tg.sent[0]["text"])

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
        self.assertTrue(any("no hay comandos" in line for line in logs))

    def test_logs_carry_correlation_fields(self):
        controller, logs = build_controller(FakeGitHubOpener(), FakeTelegramOpener())
        controller.run()
        self.assertTrue(logs)
        self.assertIn("utc=2026-01-15T20:00:00Z", logs[0])
        self.assertIn("local=2026-01-15 15:00", logs[0])
        self.assertIn("run=42", logs[0])
        self.assertIn("event=schedule", logs[0])

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
        self.assertIn("/session_start", tg.sent[0]["text"])
        self.assertIn("/session_stop", tg.sent[0]["text"])

    def test_session_reports_stopped_with_last_run_and_next_start(self):
        gh = FakeGitHubOpener()
        gh.workflow_runs = [done(55, "success", updated_at="2026-01-15T19:00:00Z")]
        tg = FakeTelegramOpener()
        tg.updates = [message(42, "/session")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        text = tg.sent[0]["text"]
        self.assertIn("DETENIDA", text)
        self.assertIn("55", text)
        # Jueves 15:00 -> próximo arranque viernes 08:00.
        self.assertIn("2026-01-16 08:00", text)
        self.assertEqual(tg.offsets, [43])

    def test_status_text_reports_active_and_pending(self):
        controller, _ = build_controller(FakeGitHubOpener(), FakeTelegramOpener())
        active = ctl.PaperState(active=[run(11, "in_progress")], pending=[])
        pending = ctl.PaperState(active=[], pending=[run(12, "queued")])
        self.assertIn("ACTIVA", controller._status_text(active))
        self.assertIn("EN COLA", controller._status_text(pending))
        self.assertIn("DETENIDA", controller._status_text(ctl.PaperState()))

    def test_start_dispatches_and_confirms_the_run(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/session_start")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(len(gh.dispatches), 1)
        self.assertEqual(gh.dispatches[0]["inputs"]["manual_session"], "true")
        text = tg.sent[0]["text"]
        self.assertIn("solicitado", text.lower())
        self.assertIn("1001", text)
        self.assertNotIn("pendiente de confirmación", text)

    def test_start_without_github_confirmation_is_honest(self):
        gh = FakeGitHubOpener()
        gh.create_on_dispatch = False
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/start")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(len(gh.dispatches), 1)
        self.assertIn("pendiente de confirmación", tg.sent[0]["text"])

    def test_two_starts_in_one_batch_dispatch_once(self):
        gh = FakeGitHubOpener()
        gh.create_on_dispatch = False
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/start"), message(2, "/session_start")]
        controller, _ = build_controller(gh, tg)
        controller.run()
        self.assertEqual(len(gh.dispatches), 1)
        self.assertIn("cola", tg.sent[1]["text"].lower())
        self.assertEqual(tg.offsets, [3])

    def test_start_with_active_does_not_dispatch_duplicate(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        controller, _ = build_controller(gh, tg)
        controller._start(ADMIN, ctl.PaperState(active=[run(4, "in_progress")]))
        self.assertEqual(gh.dispatches, [])
        self.assertIn("activa", tg.sent[0]["text"].lower())

    def test_stop_cancels_pending_run(self):
        gh = FakeGitHubOpener()
        gh.workflow_runs = [run(11, "queued")]
        tg = FakeTelegramOpener()
        controller, _ = build_controller(gh, tg)
        controller._stop(ADMIN, ctl.PaperState.from_runs(gh.workflow_runs))
        self.assertIn("/runs/11/cancel", gh.cancels[0])
        self.assertIn("Parada confirmada", tg.sent[0]["text"])

    def test_stop_cancels_active_run(self):
        gh = FakeGitHubOpener()
        gh.workflow_runs = [run(12, "in_progress")]
        tg = FakeTelegramOpener()
        controller, _ = build_controller(gh, tg)
        controller._stop(ADMIN, ctl.PaperState.from_runs(gh.workflow_runs))
        self.assertIn("/runs/12/cancel", gh.cancels[0])

    def test_stop_without_confirmation_says_it_is_asynchronous(self):
        gh = FakeGitHubOpener()
        gh.complete_on_cancel = False
        gh.workflow_runs = [run(12, "in_progress")]
        tg = FakeTelegramOpener()
        clock = FakeClock(tg)
        controller, _ = build_controller(gh, tg, clock=clock)
        controller._stop(ADMIN, ctl.PaperState.from_runs(gh.workflow_runs))
        self.assertIn("Cancelación solicitada", tg.sent[0]["text"])
        self.assertIn("asíncrona", tg.sent[0]["text"])
        self.assertEqual(len(clock.sleeps), ctl.CONFIRM_ATTEMPTS - 1)

    def test_stop_without_runs_informs(self):
        tg = FakeTelegramOpener()
        controller, _ = build_controller(FakeGitHubOpener(), tg)
        controller._stop(ADMIN, ctl.PaperState())
        self.assertIn("No hay ninguna sesión", tg.sent[0]["text"])

    def test_cancel_rejected_by_github_notifies(self):
        gh = FakeGitHubOpener()
        gh.cancel_status = 404
        gh.workflow_runs = [run(12, "queued")]
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/stop")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 1)
        self.assertIn("rechazó", tg.sent[0]["text"].lower())
        self.assertEqual(tg.offsets, [2])

    def test_unauthorized_chat_is_ignored(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/session_start", chat_id=999)]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(gh.dispatches, [])
        self.assertEqual(tg.sent, [])
        # El update se confirma igualmente para que no se repita.
        self.assertEqual(tg.offsets, [2])

    def test_unknown_command_gets_hint(self):
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/positions")]
        controller, _ = build_controller(FakeGitHubOpener(), tg)
        self.assertEqual(controller.run(), 0)
        self.assertIn("necesita una sesión PAPER activa", tg.sent[0]["text"])

    def test_unknown_command_with_pending_run_points_to_runner(self):
        gh = FakeGitHubOpener()
        gh.workflow_runs = [run(11, "queued")]
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/positions")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        text = tg.sent[0]["text"]
        self.assertIn("lo atiende el runner", text)
        self.assertNotIn("necesita una sesión PAPER activa", text)
        self.assertEqual(tg.offsets, [2])

    def test_dispatch_rejected_notifies_and_fails(self):
        gh = FakeGitHubOpener()
        gh.dispatch_status = 403
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/session_start")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 1)
        self.assertIn("rechazó", tg.sent[0]["text"].lower())
        self.assertEqual(tg.offsets, [2])

    def test_github_unreachable_fails_the_single_pass(self):
        gh = FakeGitHubOpener()
        gh.list_status = 401
        tg = FakeTelegramOpener()
        controller, logs = build_controller(gh, tg)
        self.assertEqual(controller.run(), 1)
        self.assertTrue(any("401" in line for line in logs))
        self.assertEqual(tg.get_updates_calls, 0)

    def test_telegram_conflict_yields_without_consuming(self):
        tg = FakeTelegramOpener()
        tg.conflict = True
        controller, logs = build_controller(FakeGitHubOpener(), tg)
        self.assertEqual(controller.run(), 0)
        self.assertTrue(any("409" in line for line in logs))
        self.assertEqual(tg.offsets, [])

    def test_telegram_auth_error_is_fatal_and_secret_free(self):
        tg = FakeTelegramOpener()
        tg.auth_error = True
        controller, logs = build_controller(FakeGitHubOpener(), tg)
        self.assertEqual(controller.run(), 1)
        self.assertTrue(controller.fatal)
        self.assertTrue(any("401" in line for line in logs))
        self.assertFalse(any("tgtoken" in line for line in logs))

    def test_transient_timeout_is_retried(self):
        tg = FakeTelegramOpener()
        tg.timeout_calls = 1
        tg.updates = [message(3, "/ping")]
        controller, _ = build_controller(FakeGitHubOpener(), tg)
        self.assertEqual(controller.run(), 0)
        # 1 timeout + 1 poll correcto + 1 confirmación del offset.
        self.assertEqual(tg.get_updates_calls, 3)
        self.assertIn("Pong", tg.sent[0]["text"])

    def test_persistent_telegram_server_error_fails_the_pass(self):
        tg = FakeTelegramOpener()
        tg.server_error = True
        controller, _ = build_controller(FakeGitHubOpener(), tg)
        self.assertEqual(controller.run(), 1)
        self.assertEqual(tg.get_updates_calls, len(ctl.RETRY_DELAYS) + 1)

    def test_html_is_escaped_in_status(self):
        controller, _ = build_controller(FakeGitHubOpener(), FakeTelegramOpener())
        text = controller._status_text(ctl.PaperState(pending=[run("<b>x</b>", "queued")]))
        self.assertIn("&lt;b&gt;x&lt;/b&gt;", text)
        self.assertNotIn("<b>x</b>", text)

    def test_all_commands_in_batch_are_processed_once(self):
        tg = FakeTelegramOpener()
        tg.updates = [message(10, "/ping"), message(11, "/session")]
        controller, _ = build_controller(FakeGitHubOpener(), tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(len(tg.sent), 2)
        self.assertEqual(tg.offsets, [12])

    def test_command_sent_while_a_runner_was_alive_is_not_replayed(self):
        """El runner pudo atender el /stop sin confirmar el offset."""
        gh = FakeGitHubOpener()
        gh.workflow_runs = [done(9, "cancelled", created_at="2026-01-15T13:00:00Z",
                                 updated_at="2026-01-15T19:59:00Z")]
        tg = FakeTelegramOpener()
        tg.updates = [message(5, "/stop", sent_at=_utc(2026, 1, 15, 19, 58)),
                      message(6, "/start", sent_at=_utc(2026, 1, 15, 19, 58, ))]
        controller, logs = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(gh.dispatches, [])
        self.assertEqual(gh.cancels, [])
        self.assertEqual(tg.sent, [])
        self.assertEqual(tg.offsets, [7])
        self.assertTrue(any("sesión anterior" in line for line in logs))

    def test_expired_lifecycle_command_is_not_executed(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        tg.updates = [message(5, "/start", sent_at=DEFAULT_NOW - dt.timedelta(hours=3))]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(gh.dispatches, [])
        self.assertIn("caducado", tg.sent[0]["text"])


class AutoStartTests(unittest.TestCase):
    """Arranque automático de la sesión PAPER a las 08:00 de Cuba."""

    def _run_at(self, now, runs=None, policy=None):
        gh = FakeGitHubOpener(now=now)
        gh.workflow_runs = list(runs or [])
        tg = FakeTelegramOpener()
        controller, logs = build_controller(gh, tg, now=lambda: now, policy=policy)
        code = controller.run()
        return gh, tg, logs, code

    def test_does_not_start_at_0759(self):
        gh, _, _, _ = self._run_at(_utc(2026, 1, 15, 12, 59))  # 07:59 UTC-5
        self.assertEqual(gh.dispatches, [])

    def test_starts_at_0800(self):
        gh, tg, _, code = self._run_at(_utc(2026, 1, 15, 13, 0))  # 08:00 UTC-5
        self.assertEqual(code, 0)
        self.assertEqual(len(gh.dispatches), 1)
        self.assertEqual(gh.dispatches[0]["inputs"]["manual_session"], "false")
        self.assertEqual(gh.dispatches[0]["ref"], "main")
        self.assertIn("automático", tg.sent[0]["text"].lower())
        self.assertIn("1/3", tg.sent[0]["text"])
        self.assertIn("1001", tg.sent[0]["text"])

    def test_starts_at_1259(self):
        gh, _, _, _ = self._run_at(_utc(2026, 1, 15, 17, 59))  # 12:59 UTC-5
        self.assertEqual(len(gh.dispatches), 1)

    def test_does_not_start_at_1300(self):
        gh, _, _, _ = self._run_at(_utc(2026, 1, 15, 18, 0))  # 13:00 UTC-5
        self.assertEqual(gh.dispatches, [])

    def test_start_follows_daylight_saving(self):
        # En julio La Habana es UTC-4: 08:00 local -> 12:00 UTC.
        gh, _, _, _ = self._run_at(_utc(2026, 7, 15, 12, 0))
        self.assertEqual(len(gh.dispatches), 1)
        gh, _, _, _ = self._run_at(_utc(2026, 7, 15, 11, 59))
        self.assertEqual(gh.dispatches, [])

    def test_does_not_start_on_weekend(self):
        for day in (17, 18):  # sábado y domingo
            gh, _, _, _ = self._run_at(_utc(2026, 1, day, 13, 0))
            self.assertEqual(gh.dispatches, [])

    def test_does_not_duplicate_active_or_queued_run(self):
        for status in ("in_progress", "queued", "requested", "waiting"):
            gh, _, _, _ = self._run_at(_utc(2026, 1, 15, 13, 5),
                                       runs=[run(99, status, created_at="2026-01-15T13:00:00Z")])
            self.assertEqual(gh.dispatches, [], status)

    def test_completed_session_of_today_is_not_repeated(self):
        gh, _, logs, _ = self._run_at(
            _utc(2026, 1, 15, 15, 0),
            runs=[done(99, "success", created_at="2026-01-15T13:00:00Z",
                       updated_at="2026-01-15T14:00:00Z")])
        self.assertEqual(gh.dispatches, [])
        self.assertTrue(any("ya existió" in line for line in logs))

    def test_cancelled_session_is_not_restarted(self):
        gh, _, _, _ = self._run_at(
            _utc(2026, 1, 15, 14, 0),
            runs=[done(99, "cancelled", created_at="2026-01-15T13:00:00Z",
                       updated_at="2026-01-15T13:30:00Z")])
        self.assertEqual(gh.dispatches, [])

    def test_failed_run_is_retried(self):
        gh, tg, _, _ = self._run_at(
            _utc(2026, 1, 15, 13, 30),
            runs=[done(99, "failure", created_at="2026-01-15T13:00:00Z",
                       updated_at="2026-01-15T13:05:00Z")])
        self.assertEqual(len(gh.dispatches), 1)
        self.assertIn("2/3", tg.sent[0]["text"])

    def test_timed_out_and_startup_failure_are_retried(self):
        for conclusion in ("timed_out", "startup_failure"):
            gh, _, _, _ = self._run_at(
                _utc(2026, 1, 15, 13, 30),
                runs=[done(99, conclusion, created_at="2026-01-15T13:00:00Z",
                           updated_at="2026-01-15T13:05:00Z")])
            self.assertEqual(len(gh.dispatches), 1, conclusion)

    def test_gives_up_after_max_attempts_and_notifies_once(self):
        now = _utc(2026, 1, 15, 13, 30)
        runs = [done(i, "failure", created_at=f"2026-01-15T13:0{i}:00Z",
                     updated_at=f"2026-01-15T13:0{i}:30Z") for i in (1, 2, 3)]
        gh = FakeGitHubOpener(now=now)
        gh.workflow_runs = runs
        tg = FakeTelegramOpener()
        controller, _ = build_controller(gh, tg, now=lambda: now)
        controller.run()
        controller.run()
        self.assertEqual(gh.dispatches, [])
        self.assertEqual(len(tg.sent), 1)
        self.assertIn("no arrancó", tg.sent[0]["text"])

    def test_night_test_run_does_not_block_the_morning_start(self):
        gh, _, _, _ = self._run_at(
            _utc(2026, 1, 15, 13, 0),
            runs=[done(98, "success", created_at="2026-01-15T07:00:00Z",
                       updated_at="2026-01-15T07:10:00Z")])
        self.assertEqual(len(gh.dispatches), 1)

    def test_yesterdays_failures_do_not_count(self):
        runs = [done(i, "failure", created_at="2026-01-14T13:00:00Z",
                     updated_at="2026-01-14T13:05:00Z") for i in (1, 2, 3)]
        gh, _, _, _ = self._run_at(_utc(2026, 1, 15, 13, 0), runs=runs)
        self.assertEqual(len(gh.dispatches), 1)

    def test_dispatch_error_notifies_and_marks_failure(self):
        now = _utc(2026, 1, 15, 13, 0)
        gh = FakeGitHubOpener(now=now)
        gh.dispatch_status = 403
        tg = FakeTelegramOpener()
        controller, _ = build_controller(gh, tg, now=lambda: now)
        self.assertEqual(controller.run(), 1)
        self.assertIn("Falló el inicio automático", tg.sent[0]["text"])

    def test_uses_the_configured_timezone_and_hours(self):
        policy = ctl.AutoStartPolicy(tz_name="America/Havana", start_hour=9, deadline_hour=14)
        gh, _, _, _ = self._run_at(_utc(2026, 1, 15, 13, 30), policy=policy)  # 08:30
        self.assertEqual(gh.dispatches, [])
        gh, _, _, _ = self._run_at(_utc(2026, 1, 15, 14, 30), policy=policy)  # 09:30
        self.assertEqual(len(gh.dispatches), 1)

    def test_next_start_skips_the_weekend(self):
        policy = ctl.AutoStartPolicy()
        friday_afternoon = _utc(2026, 1, 16, 20, 0)
        self.assertEqual(policy.next_start(friday_afternoon).strftime("%Y-%m-%d %H:%M"),
                         "2026-01-19 08:00")


class LoopTests(unittest.TestCase):
    """Modo relevo: el controlador trabaja durante todo el job."""

    def test_loop_polls_until_the_runtime_ends(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        controller, logs = build_controller(gh, tg, runtime_seconds=300)
        self.assertEqual(controller.run(), 0)
        self.assertGreaterEqual(tg.poll_calls, 5)
        self.assertTrue(all(t <= ctl.POLL_TIMEOUT for t in tg.poll_timeouts))
        self.assertTrue(any("finalizado" in line for line in logs))

    def test_command_arriving_later_is_answered(self):
        tg = FakeTelegramOpener()
        tg.batches = [[], [], [message(7, "/ping")], []]
        controller, _ = build_controller(FakeGitHubOpener(), tg, runtime_seconds=300)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(len(tg.sent), 1)
        self.assertIn("Pong", tg.sent[0]["text"])

    def test_auto_start_happens_when_the_window_opens(self):
        """El reloj cruza las 08:00 durante el job y la sesión se despacha."""
        tg = FakeTelegramOpener()
        clock = FakeClock(tg)
        start = _utc(2026, 1, 15, 12, 58)  # 07:58 La Habana

        def now():
            return start + dt.timedelta(seconds=clock.t)

        gh = FakeGitHubOpener(now=start)
        controller, _ = build_controller(gh, tg, now=now, runtime_seconds=600, clock=clock)
        controller.run()
        self.assertEqual(len(gh.dispatches), 1)
        self.assertEqual(gh.dispatches[0]["inputs"]["manual_session"], "false")

    def test_controller_yields_while_the_runner_is_active_and_resumes(self):
        gh = FakeGitHubOpener()
        gh.workflow_runs = [run(7, "in_progress")]
        tg = FakeTelegramOpener()
        clock = FakeClock(tg)
        controller, logs = build_controller(gh, tg, runtime_seconds=600, clock=clock)
        original_sleep = clock.sleep

        def sleep(seconds):
            original_sleep(seconds)
            if clock.t >= 180:
                gh.workflow_runs = [done(7, "success", updated_at=_iso(DEFAULT_NOW))]

        controller._sleep = sleep
        self.assertEqual(controller.run(), 0)
        self.assertTrue(any("controlador cede" in line for line in logs))
        self.assertTrue(any("retoma Telegram" in line for line in logs))
        self.assertGreater(tg.poll_calls, 0)

    def test_conflict_in_loop_refreshes_and_backs_off(self):
        tg = FakeTelegramOpener()
        tg.conflict = True
        clock = FakeClock(tg)
        controller, logs = build_controller(FakeGitHubOpener(), tg, runtime_seconds=60, clock=clock)
        self.assertEqual(controller.run(), 0)
        self.assertIn(10, clock.sleeps)
        self.assertTrue(any("409" in line for line in logs))

    def test_github_error_in_loop_is_retried_and_notified_once(self):
        gh = FakeGitHubOpener()
        gh.list_status = 503
        tg = FakeTelegramOpener()
        clock = FakeClock(tg)
        controller, _ = build_controller(gh, tg, runtime_seconds=200, clock=clock)
        self.assertEqual(controller.run(), 1)
        self.assertGreaterEqual(clock.sleeps.count(ctl.ERROR_BACKOFF_SECONDS), 2)
        self.assertEqual(len(tg.sent), 1)
        self.assertIn("no puede consultar GitHub", tg.sent[0]["text"])


if __name__ == "__main__":
    unittest.main()
