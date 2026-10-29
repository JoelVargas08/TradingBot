#!/usr/bin/env python3
"""Checks and summarizes the persisted PAPER state (``data/bot.db``).

Used by ``paper-session.yml``:

* ``verify <db>``  -> exit 0 only if ``PRAGMA integrity_check`` is ``ok``.  The
  workflow refuses to trade on a restored database that fails, and refuses to
  overwrite the last good ``tradingbot-state`` artifact with a corrupt one.
* ``summary <db>`` -> Markdown for ``$GITHUB_STEP_SUMMARY``: candles per
  timeframe (count, first, last), strategies, closed PAPER trades per
  strategy and open positions.

Only the standard library is used so it runs on the bare runner Python.
"""
from __future__ import annotations

import sqlite3
import sys
from contextlib import closing
from datetime import datetime, timezone


def _connect(path: str):
    # Read-only: the summary must never modify the state being archived.
    # ``closing``: a bare sqlite3 connection used as context manager only
    # ends the transaction and leaves the file open (and locked on Windows).
    return closing(sqlite3.connect(f"file:{path}?mode=ro", uri=True))


def integrity(path: str) -> str:
    with _connect(path) as conn:
        row = conn.execute("PRAGMA integrity_check").fetchone()
    return str(row[0]) if row else "sin resultado"


def _utc(ms) -> str:
    if not ms:
        return "-"
    return datetime.fromtimestamp(int(ms) / 1000, tz=timezone.utc).strftime("%Y-%m-%d %H:%M")


def _rows(conn, sql: str):
    try:
        return conn.execute(sql).fetchall()
    except sqlite3.OperationalError:
        return None


def summary(path: str) -> str:
    out = ["### Estado PAPER persistido", ""]
    out.append(f"- Integridad SQLite: `{integrity(path)}`")
    with _connect(path) as conn:
        candles = _rows(conn, """
            SELECT symbol, timeframe, COUNT(*), MIN(ts), MAX(ts)
            FROM candles GROUP BY symbol, timeframe ORDER BY symbol, timeframe""")
        strategies = _rows(conn, """
            SELECT id, status, source, updated_at FROM strategies
            ORDER BY updated_at DESC LIMIT 10""")
        closed = _rows(conn, """
            SELECT strategy_id, COUNT(*), SUM(CASE WHEN net_pnl > 0 THEN 1 ELSE 0 END),
                   ROUND(SUM(net_pnl), 2)
            FROM positions WHERE status = 'closed' GROUP BY strategy_id ORDER BY 2 DESC""")
        open_rows = _rows(conn, "SELECT COUNT(*) FROM positions WHERE status = 'open'")
    out.append("")
    out.append("**Velas**")
    out.append("")
    if candles:
        out.append("| Símbolo | TF | Velas | Primera (UTC) | Última (UTC) |")
        out.append("| --- | --- | ---: | --- | --- |")
        for symbol, tf, count, first, last in candles:
            out.append(f"| {symbol} | {tf} | {count} | {_utc(first)} | {_utc(last)} |")
    else:
        out.append("Sin velas.")
    out.append("")
    out.append("**Estrategias (10 más recientes)**")
    out.append("")
    if strategies:
        out.append("| ID | Estado | Fuente | Actualizada (UTC) |")
        out.append("| --- | --- | --- | --- |")
        for sid, status, source, updated in strategies:
            out.append(f"| `{sid}` | {status} | {source} | {_utc(updated)} |")
    else:
        out.append("Sin estrategias.")
    out.append("")
    out.append("**Operaciones PAPER cerradas (todas las sesiones)**")
    out.append("")
    if closed:
        out.append("| Estrategia | Cerradas | Ganadoras | Neto (USDT) |")
        out.append("| --- | ---: | ---: | ---: |")
        for sid, count, wins, net in closed:
            out.append(f"| `{sid}` | {count} | {wins} | {net} |")
    else:
        out.append("Ninguna operación cerrada todavía.")
    if open_rows is not None:
        out.append("")
        out.append(f"Posiciones abiertas: {open_rows[0][0]}")
    return "\n".join(out) + "\n"


def main(argv) -> int:
    if len(argv) != 3 or argv[1] not in ("verify", "summary"):
        print("uso: paper_state.py verify|summary <db>", file=sys.stderr)
        return 2
    path = argv[2]
    try:
        if argv[1] == "verify":
            result = integrity(path)
            print(f"integrity_check: {result}")
            return 0 if result == "ok" else 1
        sys.stdout.write(summary(path))
        return 0
    except sqlite3.Error as exc:
        print(f"error SQLite en {path}: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
