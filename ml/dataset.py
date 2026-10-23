"""
Carga y validación de velas para entrenamiento e inferencia (Fase NN).

No introduce dependencias pesadas: solo numpy/pandas. El objetivo es que
entrenamiento e inferencia compartan la misma validación temporal y que los
problemas de datos se reporten en lugar de ocultarse.
"""
import hashlib
import sqlite3
from dataclasses import dataclass, asdict

import numpy as np
import pandas as pd

REQUIRED_COLS = ('ts', 'open', 'high', 'low', 'close', 'volume')


@dataclass
class DataQuality:
    input_rows: int = 0
    output_rows: int = 0
    invalid_rows: int = 0
    duplicate_ts: int = 0
    gaps: int = 0
    timeframe_ms: int = 0
    first_ts: int = 0
    last_ts: int = 0

    def to_dict(self):
        return asdict(self)


def timeframe_ms(tf: str) -> int:
    tf = (tf or '').strip()
    if len(tf) < 2:
        raise ValueError(f'timeframe inválido: {tf!r}')
    number = int(tf[:-1])
    unit = tf[-1].lower()
    if number <= 0:
        raise ValueError(f'timeframe inválido: {tf!r}')
    factors = {'m': 60_000, 'h': 3_600_000, 'd': 86_400_000, 'w': 604_800_000}
    if unit not in factors:
        raise ValueError(f'timeframe inválido: {tf!r}')
    return number * factors[unit]


def prepare_candles(df: pd.DataFrame, timeframe: str = '') -> tuple[pd.DataFrame, DataQuality]:
    missing = [c for c in REQUIRED_COLS if c not in df.columns]
    if missing:
        raise ValueError(f'faltan columnas requeridas: {missing}')

    out = df.loc[:, REQUIRED_COLS].copy()
    q = DataQuality(input_rows=len(out))
    for col in REQUIRED_COLS:
        out[col] = pd.to_numeric(out[col], errors='coerce')

    numeric = out[list(REQUIRED_COLS)].to_numpy(dtype=np.float64)
    invalid = ~np.isfinite(numeric).all(axis=1)
    invalid |= (out['open'] <= 0) | (out['high'] <= 0) | (out['low'] <= 0) | (out['close'] <= 0)
    invalid |= out['high'] < out['low']
    invalid |= out['volume'] < 0
    q.invalid_rows = int(invalid.sum())
    out = out.loc[~invalid].copy()

    out['ts'] = out['ts'].astype(np.int64)
    before = len(out)
    out = out.sort_values('ts').drop_duplicates(subset=['ts'], keep='last').reset_index(drop=True)
    q.duplicate_ts = int(before - len(out))
    q.output_rows = len(out)
    if len(out):
        q.first_ts = int(out['ts'].iloc[0])
        q.last_ts = int(out['ts'].iloc[-1])
    if timeframe:
        q.timeframe_ms = timeframe_ms(timeframe)
        if q.timeframe_ms > 0 and len(out) > 1:
            diffs = np.diff(out['ts'].to_numpy(dtype=np.int64))
            q.gaps = int(np.sum(diffs > int(q.timeframe_ms * 1.5)))
    return out, q


def dataset_hash(df: pd.DataFrame) -> str:
    h = hashlib.sha256()
    for row in df.loc[:, REQUIRED_COLS].itertuples(index=False, name=None):
        h.update(('%s|%.10g|%.10g|%.10g|%.10g|%.10g;' % row).encode('ascii'))
    return h.hexdigest()


def load_from_db(db_path: str, symbol: str, timeframe: str, limit: int = 0) -> pd.DataFrame:
    conn = sqlite3.connect(f'file:{db_path}?mode=ro', uri=True)
    try:
        if limit and limit > 0:
            q = """
                SELECT ts, open, high, low, close, volume FROM (
                    SELECT ts, open, high, low, close, volume
                    FROM candles
                    WHERE symbol = ? AND timeframe = ?
                    ORDER BY ts DESC
                    LIMIT ?
                ) ORDER BY ts ASC
            """
            return pd.read_sql_query(q, conn, params=(symbol, timeframe, int(limit)))
        q = """
            SELECT ts, open, high, low, close, volume
            FROM candles
            WHERE symbol = ? AND timeframe = ?
            ORDER BY ts ASC
        """
        return pd.read_sql_query(q, conn, params=(symbol, timeframe))
    finally:
        conn.close()


def load_from_csv(path: str) -> pd.DataFrame:
    return pd.read_csv(path)


def load_synthetic(n_bars: int = 12000, seed: int = 42) -> pd.DataFrame:
    rng = np.random.default_rng(seed)
    logret = rng.normal(0.00005, 0.004, n_bars)
    regime = np.where(rng.random(n_bars) < 0.03, 0.0006, 0.00001)
    logret += regime
    price = 30000 * np.exp(np.cumsum(logret))
    close = price
    open_ = np.concatenate([[price[0]], price[:-1]])
    high = np.maximum(open_, close) * (1 + np.abs(rng.normal(0, 0.0015, n_bars)))
    low = np.minimum(open_, close) * (1 - np.abs(rng.normal(0, 0.0015, n_bars)))
    base_vol = 20 + 5 * (high - low) / close * 1000
    volume = base_vol * (1 + np.abs(rng.normal(0, 0.5, n_bars)))
    ts = pd.date_range('2023-01-01', periods=n_bars, freq='h')
    return pd.DataFrame({
        'ts': ts.astype('int64') // 10 ** 6,
        'open': open_,
        'high': high,
        'low': low,
        'close': close,
        'volume': volume,
    })