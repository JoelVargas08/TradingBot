#!/usr/bin/env python3
"""Unit tests for paper_state (state verification and job summary)."""
from __future__ import annotations

import io
import os
import sqlite3
import sys
import tempfile
import unittest
from contextlib import redirect_stdout, redirect_stderr

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import paper_state  # noqa: E402


def _db(path: str):
    conn = sqlite3.connect(path)
    conn.executescript("""
        CREATE TABLE candles (symbol TEXT, timeframe TEXT, ts INTEGER);
        CREATE TABLE strategies (id TEXT, status TEXT, source TEXT, updated_at INTEGER);
        CREATE TABLE positions (strategy_id TEXT, status TEXT, net_pnl REAL);
        INSERT INTO candles VALUES ('BTCUSDT', '15m', 1793190000000), ('BTCUSDT', '15m', 1793190900000);
        INSERT INTO strategies VALUES ('ib-1', 'active', 'investing_bulls_learn_mtf', 1793190305000);
        INSERT INTO positions VALUES ('ib-1', 'closed', 12.5), ('ib-1', 'closed', -4), ('ib-1', 'open', 0);
    """)
    conn.commit()
    conn.close()


class PaperStateTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.path = os.path.join(self.tmp.name, "bot.db")
        _db(self.path)

    def tearDown(self):
        self.tmp.cleanup()

    def test_verify_ok(self):
        with redirect_stdout(io.StringIO()):
            self.assertEqual(paper_state.main(["x", "verify", self.path]), 0)

    def test_verify_rejects_a_corrupt_file(self):
        bad = os.path.join(self.tmp.name, "bad.db")
        with open(bad, "wb") as fh:
            fh.write(b"SQLite format 3\x00" + b"\xff" * 2048)
        with redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()):
            self.assertNotEqual(paper_state.main(["x", "verify", bad]), 0)

    def test_summary_reports_candles_strategies_and_trades(self):
        text = paper_state.summary(self.path)
        self.assertIn("| BTCUSDT | 15m | 2 |", text)
        self.assertIn("`ib-1` | active", text)
        self.assertIn("| `ib-1` | 2 | 1 | 8.5 |", text)
        self.assertIn("Posiciones abiertas: 1", text)

    def test_summary_tolerates_missing_tables(self):
        empty = os.path.join(self.tmp.name, "empty.db")
        sqlite3.connect(empty).close()
        text = paper_state.summary(empty)
        self.assertIn("Sin velas.", text)

    def test_summary_is_read_only(self):
        before = os.path.getmtime(self.path)
        paper_state.summary(self.path)
        self.assertEqual(before, os.path.getmtime(self.path))

    def test_usage(self):
        with redirect_stderr(io.StringIO()):
            self.assertEqual(paper_state.main(["x"]), 2)


if __name__ == "__main__":
    unittest.main()
