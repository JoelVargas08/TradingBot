"""Ingeniería de características para la Fase 5.

Todas las features se calculan SOLO con datos de la vela actual y anteriores
(cajón cerrado): ninguna mira hacia el futuro. Así se evita lookahead tanto en
entrenamiento como en inferencia.
"""

import numpy as np
import pandas as pd

REQUIRED_COLS = ["open", "high", "low", "close", "volume"]

FEATURE_NAMES = [
    "ret_1",
    "ret_3",
    "ret_6",
    "ret_12",
    "ret_24",
    "logret_1",
    "rsi_14",
    "macd_line",
    "macd_signal",
    "macd_hist",
    "atr_14_pct",
    "vol_ratio",
    "close_sma20",
    "close_sma50",
    "bb_pos",
    "donchian_pos_20",
    "highlow_range",
]


def compute_features(df: pd.DataFrame) -> pd.DataFrame:
    """Devuelve el DataFrame con las columnas de features añadidas.

    Se espera un DataFrame con columnas [ts, open, high, low, close, volume]
    ordenado cronológicamente (ascendente).
    """
    for col in REQUIRED_COLS:
        if col not in df.columns:
            raise ValueError(f"falta la columna requerida: {col}")
    out = df.copy()
    close = out["close"]
    volume = out["volume"]

    # Momentum
    for n in (1, 3, 6, 12, 24):
        out[f"ret_{n}"] = close.pct_change(n)
    out["logret_1"] = np.log(close / close.shift(1))

    # RSI 14 (Wilder)
    out["rsi_14"] = _rsi(close, 14)

    # MACD 12/26/9
    ema12 = close.ewm(span=12, adjust=False).mean()
    ema26 = close.ewm(span=26, adjust=False).mean()
    out["macd_line"] = macd_line = ema12 - ema26
    out["macd_signal"] = macd_signal = macd_line.ewm(span=9, adjust=False).mean()
    out["macd_hist"] = macd_line - macd_signal

    # ATR 14 normalizado por precio
    atr = _atr(out, 14)
    out["atr_14_pct"] = atr / close

    # Volumen relativo a su SMA 20
    vol_sma = volume.rolling(20, min_periods=1).mean()
    out["vol_ratio"] = volume / vol_sma

    # Distancia relativa a medias móviles
    out["close_sma20"] = close / close.rolling(20, min_periods=1).mean()
    out["close_sma50"] = close / close.rolling(50, min_periods=1).mean()

    # Banda de Bollinger (20, 2)
    mid = close.rolling(20, min_periods=1).mean()
    std = close.rolling(20, min_periods=1).std()
    upper = mid + 2 * std
    lower = mid - 2 * std
    span = (upper - lower).replace(0, np.nan)
    out["bb_pos"] = (close - lower) / span

    # Posición dentro del rango de Donchian (20)
    hi = out["high"].rolling(20, min_periods=1).max()
    lo = out["low"].rolling(20, min_periods=1).min()
    dspan = (hi - lo).replace(0, np.nan)
    out["donchian_pos_20"] = (close - lo) / dspan

    # Rango intrabarra relativo
    out["highlow_range"] = (out["high"] - out["low"]) / close

    return out


def feature_matrix(df: pd.DataFrame) -> np.ndarray:
    """Devuelve la matriz X (filas, features) con las features listas."""
    return df[FEATURE_NAMES].to_numpy(dtype=np.float64)


def feature_row(df: pd.DataFrame) -> np.ndarray:
    """Devuelve la última fila de features (para una predicción)."""
    return df[FEATURE_NAMES].iloc[[-1]].to_numpy(dtype=np.float64)


def _rsi(close: pd.Series, period: int = 14) -> pd.Series:
    delta = close.diff()
    gain = delta.clip(lower=0)
    loss = -delta.clip(upper=0)
    avg_gain = gain.ewm(alpha=1 / period, adjust=False, min_periods=period).mean()
    avg_loss = loss.ewm(alpha=1 / period, adjust=False, min_periods=period).mean()
    rs = avg_gain / avg_loss
    return 100 - 100 / (1 + rs)


def _atr(df: pd.DataFrame, period: int = 14) -> pd.Series:
    prev_close = df["close"].shift(1)
    tr = pd.concat(
        [
            df["high"] - df["low"],
            (df["high"] - prev_close).abs(),
            (df["low"] - prev_close).abs(),
        ],
        axis=1,
    ).max(axis=1)
    return tr.ewm(alpha=1 / period, adjust=False, min_periods=period).mean()


def load_candles(candles: list[dict]) -> pd.DataFrame:
    """Convierte la lista JSON de velas (de la API /predict o CSV/DB) a DataFrame."""
    df = pd.DataFrame(candles)
    for col in REQUIRED_COLS:
        df[col] = pd.to_numeric(df[col], errors="coerce")
    if "ts" in df.columns:
        df["ts"] = pd.to_numeric(df["ts"], errors="coerce")
    return df.sort_values("ts").reset_index(drop=True)
