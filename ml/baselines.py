"""Reglas simples de referencia con los mismos costes que la red neuronal."""
import numpy as np

from nn.metrics import evaluate_trading


def signal_probs(long_signal, short_signal):
    long_signal = np.asarray(long_signal, dtype=bool)
    short_signal = np.asarray(short_signal, dtype=bool)
    if len(long_signal) != len(short_signal):
        raise ValueError('señales de distinta longitud')
    probs = np.tile(np.array([0.5, 0.0, 0.5], dtype=np.float64), (len(long_signal), 1))
    probs[long_signal] = np.array([0.0, 0.0, 1.0])
    probs[short_signal] = np.array([1.0, 0.0, 0.0])
    return probs


def strategy_set(close, feat, horizon):
    close = np.asarray(close, dtype=np.float64)
    n = len(close)
    flat_long = np.zeros(n, dtype=bool)
    flat_short = np.zeros(n, dtype=bool)
    all_true = np.ones(n, dtype=bool)

    mom = np.full(n, np.nan, dtype=np.float64)
    if n > horizon:
        mom[horizon:] = close[horizon:] / close[:-horizon] - 1.0
    mom_long = np.nan_to_num(mom, nan=0.0) > 0.0
    mom_short = np.nan_to_num(mom, nan=0.0) < 0.0

    rsi = feat['rsi_14'].to_numpy(dtype=np.float64)
    rsi_long = np.nan_to_num(rsi, nan=50.0) < 30.0
    rsi_short = np.nan_to_num(rsi, nan=50.0) > 70.0

    macd = feat['macd_hist_pct'].to_numpy(dtype=np.float64)
    macd_long = np.nan_to_num(macd, nan=0.0) > 0.0
    macd_short = np.nan_to_num(macd, nan=0.0) < 0.0

    return {
        'always_long': (signal_probs(all_true, flat_short), 'long', 0.0),
        'always_short': (signal_probs(flat_long, all_true), 'short', 0.0),
        f'momentum_{horizon}': (signal_probs(mom_long, mom_short), 'both', 0.5),
        'rsi_30_70': (signal_probs(rsi_long, rsi_short), 'both', 0.5),
        'macd_hist': (signal_probs(macd_long, macd_short), 'both', 0.5),
    }


def evaluate_baselines(close, feat, horizon, fee_rate, slippage_rate):
    results = {}
    for name, (probs, side, confidence) in strategy_set(close, feat, horizon).items():
        results[name] = evaluate_trading(
            probs, close, horizon, confidence=confidence,
            fee_rate=fee_rate, slippage_rate=slippage_rate, side=side,
        )
    return results
