#!/usr/bin/env python3
"""Unit tests for telegram_workflow_controller.

No real network, tokens or trading orders are involved: GitHub and Telegram
are replaced by fakes that record the HTTP requests.
"""
from __future__ import annotations

import io
import json
import os
import sys
import unittest
import urllib.error
import urllib.parse
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import telegram_workflow_controller as ctl  # noqa: E402

ADMIN = "123456"


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

    def __call__(self, request, timeout):
        params = urllib.parse.parse_qs(request.data.decode())
        method = request.full_url.rsplit("/", 1)[-1]
        if method == "getUpdates":
            if self.conflict:
                raise _http_error(request.full_url, 409,
                                  {"ok": False, "description": "Conflict: terminated by other getUpdates request"})
            if "offset" in params:
                self.offsets.append(int(params["offset"][0]))
                return 200, _json_body({"ok": True, "result": []})
            return 200, _json_body({"ok": True, "result": self.updates})
        if method == "sendMessage":
            self.sent.append({k: v[0] for k, v in params.items()})
            return 200, _json_body({"ok": True, "result": {"message_id": 1}})
        raise AssertionError("método inesperado: " + method)


def message(update_id, text, chat_id=ADMIN):
    return {"update_id": update_id, "message": {"text": text, "chat": {"id": chat_id}}}


def build_controller(gh_opener, tg_opener, admin=ADMIN):
    github = ctl.GitHubClient("ghtoken", "owner/repo", opener=gh_opener)
    telegram = ctl.TelegramClient("tgtoken", opener=tg_opener)
    logs = []
    controller = ctl.Controller(github, telegram, admin, log=logs.append)
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
            {"id": 1, "status": "in_progress"},
            {"id": 2, "status": "queued"},
            {"id": 3, "status": "completed"},
        ]
        client = ctl.GitHubClient("tok", "owner/repo", opener=opener)
        active, pending = client.paper_state()
        self.assertEqual([r["id"] for r in active], [1])
        self.assertEqual([r["id"] for r in pending], [2])


class ControllerTests(unittest.TestCase):
    def test_active_run_does_not_consume_telegram(self):
        gh = FakeGitHubOpener()
        gh.workflow_runs = [{"id": 7, "status": "in_progress"}]
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/session_start")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(tg.sent, [])
        self.assertEqual(tg.offsets, [])
        self.assertEqual(gh.dispatches, [])

    def test_no_runs_no_updates_exits_clean(self):
        controller, logs = build_controller(FakeGitHubOpener(), FakeTelegramOpener())
        self.assertEqual(controller.run(), 0)
        self.assertTrue(any("no hay comandos" in log for log in logs))

    def test_idle_controller_can_wait_for_telegram_command(self):
        controller, logs = build_controller(FakeGitHubOpener(), FakeTelegramOpener())
        controller.poll_window_seconds = 1
        with mock.patch.object(ctl.time, "monotonic", side_effect=[10.0, 12.0]):
            self.assertEqual(controller.run(), 0)
        self.assertTrue(any("ventana de escucha finalizada" in log for log in logs))

    def test_session_reports_stopped(self):
        tg = FakeTelegramOpener()
        tg.updates = [message(42, "/session")]
        controller, _ = build_controller(FakeGitHubOpener(), tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(len(tg.sent), 1)
        self.assertIn("DETENIDO", tg.sent[0]["text"])
        self.assertEqual(tg.offsets, [43])

    def test_session_reports_queued(self):
        gh = FakeGitHubOpener()
        gh.workflow_runs = [{"id": 9, "status": "queued"}]
        tg = FakeTelegramOpener()
        tg.updates = [message(5, "/session")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertIn("EN COLA", tg.sent[0]["text"])

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
        gh.workflow_runs = [{"id": 3, "status": "queued"}]
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/start")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(gh.dispatches, [])
        self.assertIn("cola", tg.sent[0]["text"].lower())

    def test_stop_cancels_pending_run(self):
        gh = FakeGitHubOpener()
        gh.workflow_runs = [{"id": 11, "status": "queued"}]
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/session_stop")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(len(gh.cancels), 1)
        self.assertIn("/runs/11/cancel", gh.cancels[0])

    def test_stop_alias_cancels_pending_run(self):
        gh = FakeGitHubOpener()
        gh.workflow_runs = [{"id": 11, "status": "queued"}]
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/stop")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(len(gh.cancels), 1)

    def test_stop_without_pending_informs(self):
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/session_stop")]
        controller, _ = build_controller(FakeGitHubOpener(), tg)
        self.assertEqual(controller.run(), 0)
        self.assertIn("No hay ninguna sesión", tg.sent[0]["text"])

    def test_unauthorized_chat_is_ignored(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/session_start", chat_id=999)]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(gh.dispatches, [])
        self.assertEqual(tg.sent, [])

    def test_unknown_command_is_ignored(self):
        gh = FakeGitHubOpener()
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/deploy")]
        controller, _ = build_controller(gh, tg)
        self.assertEqual(controller.run(), 0)
        self.assertEqual(gh.dispatches, [])
        self.assertEqual(tg.sent, [])

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
        self.assertTrue(any("cede" in log for log in logs))

    def test_html_is_escaped_in_status(self):
        gh = FakeGitHubOpener()
        gh.workflow_runs = [{"id": "<b>x</b>", "status": "queued"}]
        tg = FakeTelegramOpener()
        tg.updates = [message(1, "/session")]
        controller, _ = build_controller(gh, tg)
        controller.run()
        self.assertIn("&lt;b&gt;x&lt;/b&gt;", tg.sent[0]["text"])
        self.assertNotIn("<b>x</b>", tg.sent[0]["text"])

    def test_confirmation_uses_next_offset(self):
        tg = FakeTelegramOpener()
        tg.updates = [message(100, "/session")]
        controller, _ = build_controller(FakeGitHubOpener(), tg)
        controller.run()
        self.assertEqual(tg.offsets, [101])


if __name__ == "__main__":
    unittest.main()
