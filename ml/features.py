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

# Features de la red neuronal. A diferencia de FEATURE_NAMES (legacy XGBoost),
# todas son adimensionales para poder validar en otro símbolo sin reescalar.
NN_FEATURE_NAMES = [
    "ret_1",
    "ret_3",
    "ret_6",
    "ret_12",
    "ret_24",
    "ret_48",
    "logret_1",
    "rsi_7",
    "rsi_14",
    "rsi_28",
    "rsi_diff",
    "macd_line_pct",
    "macd_signal_pct",
    "macd_hist_pct",
    "atr_14_pct",
    "atr_50_pct",
    "atr_ratio",
    "vol_ratio",
    "vol_z_24",
    "volume_change",
    "close_sma20",
    "close_sma50",
    "ema_20_50",
    "ema_50_slope",
    "bb_pos",
    "donchian_pos_20",
    "donchian_pos_55",
    "highlow_range",
    "body_ratio",
    "upper_wick",
    "lower_wick",
    "close_pos",
    "vol_24",
    "vol_48",
    "vol_24_48",
    "range_48",
    "hour_sin",
    "hour_cos",
    "dow_sin",
    "dow_cos",
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

    # --- Features para la red neuronal (adimensionales) ---
    # Momentum extendido y RSI múltiple
    out["ret_48"] = close.pct_change(48)
    out["rsi_7"] = _rsi(close, 7)
    out["rsi_28"] = _rsi(close, 28)
    out["rsi_diff"] = out["rsi_14"] - out["rsi_28"]

    # MACD normalizado por precio (el legacy conserva las columnas absolutas)
    out["macd_line_pct"] = macd_line / close
    out["macd_signal_pct"] = macd_signal / close
    out["macd_hist_pct"] = out["macd_hist"] / close

    # ATR relativo y ratio de volatilidad
    atr50 = _atr(out, 50)
    out["atr_50_pct"] = atr50 / close
    out["atr_ratio"] = atr / atr50.replace(0, np.nan)

    # Volumen: z-score y cambio relativo
    vol_mean = volume.rolling(24, min_periods=2).mean()
    vol_std = volume.rolling(24, min_periods=2).std()
    out["vol_z_24"] = (volume - vol_mean) / vol_std.replace(0, np.nan)
    out["volume_change"] = volume.pct_change().replace([np.inf, -np.inf], np.nan)

    # Tendencia: cruce de EMAs y pendiente
    ema20 = close.ewm(span=20, adjust=False, min_periods=20).mean()
    ema50 = close.ewm(span=50, adjust=False, min_periods=50).mean()
    out["ema_20_50"] = ema20 / ema50 - 1.0
    out["ema_50_slope"] = ema50 / ema50.shift(6) - 1.0

    # Donchian de 55 y rango de 48 velas
    hi55 = out["high"].rolling(55, min_periods=2).max()
    lo55 = out["low"].rolling(55, min_periods=2).min()
    dspan55 = (hi55 - lo55).replace(0, np.nan)
    out["donchian_pos_55"] = (close - lo55) / dspan55
    hi48 = out["high"].rolling(48, min_periods=2).max()
    lo48 = out["low"].rolling(48, min_periods=2).min()
    out["range_48"] = (hi48 - lo48) / close

    # Volatilidad realizada
    logret = out["logret_1"]
    out["vol_24"] = logret.rolling(24, min_periods=2).std()
    out["vol_48"] = logret.rolling(48, min_periods=2).std()
    out["vol_24_48"] = out["vol_24"] / out["vol_48"].replace(0, np.nan)

    # Estructura de la vela
    candle_range = (out["high"] - out["low"]).replace(0, np.nan)
    body_high = np.maximum(out["open"], close)
    body_low = np.minimum(out["open"], close)
    out["body_ratio"] = (close - out["open"]) / candle_range
    out["upper_wick"] = (out["high"] - body_high) / candle_range
    out["lower_wick"] = (body_low - out["low"]) / candle_range
    out["close_pos"] = (close - out["low"]) / candle_range

    # Hora del día y día de la semana (cíclicos)
    if "ts" in out.columns:
        ts = pd.to_datetime(out["ts"], unit="ms", utc=True)
        hour = ts.dt.hour + ts.dt.minute / 60.0
        out["hour_sin"] = np.sin(2 * np.pi * hour / 24.0)
        out["hour_cos"] = np.cos(2 * np.pi * hour / 24.0)
        dow = ts.dt.dayofweek.astype(float)
        out["dow_sin"] = np.sin(2 * np.pi * dow / 7.0)
        out["dow_cos"] = np.cos(2 * np.pi * dow / 7.0)
    else:
        for name in ("hour_sin", "hour_cos", "dow_sin", "dow_cos"):
            out[name] = 0.0

    return out


def feature_matrix(df: pd.DataFrame) -> np.ndarray:
    """Devuelve la matriz X (filas, features) con las features listas."""
    return df[FEATURE_NAMES].to_numpy(dtype=np.float64)


def feature_row(df: pd.DataFrame) -> np.ndarray:
    """Devuelve la última fila de features (para una predicción)."""
    return df[FEATURE_NAMES].iloc[[-1]].to_numpy(dtype=np.float64)


def nn_feature_matrix(df: pd.DataFrame) -> np.ndarray:
    """Matriz X para la red neuronal (features adimensionales)."""
    return df[NN_FEATURE_NAMES].to_numpy(dtype=np.float64)


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
