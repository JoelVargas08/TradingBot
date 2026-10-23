"""
Métricas predictivas y de trading con costes realistas (Fase NN).

La simulación es deliberadamente conservadora:
- señales no solapadas (entrada cada horizon velas como máximo),
- entrada/salida al cierre,
- comisión + slippage en ambos lados,
- se compara contra una línea base alcista con el mismo calendario y costes.
"""
import numpy as np


def classification_metrics(y_true, probs):
    probs = np.asarray(probs, dtype=np.float64)
    y = np.asarray(y_true, dtype=np.int64)
    eps = 1e-12
    p = np.clip(probs, eps, 1.0 - eps)
    logloss = float(-np.mean(np.log(p[np.arange(len(y)), y])))
    acc = float(np.mean(np.argmax(probs, axis=1) == y))
    onehot = np.zeros_like(p)
    onehot[np.arange(len(y)), y] = 1.0
    brier = float(np.mean(np.sum((p - onehot) ** 2, axis=1)))
    return {'accuracy': acc, 'log_loss': logloss, 'brier': brier, 'samples': int(len(y))}


def directional_trades(probs, close, horizon, confidence, fee_rate, slippage_rate):
    probs = np.asarray(probs, dtype=np.float64)
    close = np.asarray(close, dtype=np.float64)
    n = min(len(probs), len(close))
    horizon = max(1, int(horizon))
    if n <= horizon:
        return []

    trades = []
    i = 0
    # La señal se calcula con el cierre de la vela i, pero la entrada se
    # ejecuta en la vela siguiente (i+1): entrar al cierre observado sería
    # lookahead. El coste se paga en entrada y salida.
    while i + 1 + horizon < n:
        prob_down = float(probs[i, 0])
        prob_up = float(probs[i, 2]) if probs.shape[1] > 2 else float(probs[i, 1])
        strength = prob_up - prob_down
        if strength >= confidence:
            direction = 1
        elif strength <= -confidence:
            direction = -1
        else:
            i += 1
            continue

        entry_idx = i + 1
        exit_idx = entry_idx + horizon
        entry = close[entry_idx]
        exit_ = close[exit_idx]
        if entry <= 0 or exit_ <= 0:
            i += 1
            continue
        gross = direction * (exit_ / entry - 1.0)
        cost = 2.0 * (fee_rate + slippage_rate)
        net = gross - cost
        trades.append({
            'entry_idx': int(entry_idx),
            'exit_idx': int(exit_idx),
            'direction': direction,
            'gross_return': float(gross),
            'cost': float(cost),
            'net_return': float(net),
        })
        i = exit_idx
    return trades


def summarize_trades(trades):
    if not trades:
        return {
            'trades': 0, 'win_rate': 0.0, 'net_return': 0.0,
            'gross_return': 0.0, 'total_cost': 0.0,
            'avg_net_return': 0.0, 'max_drawdown': 0.0,
        }
    nets = np.array([t['net_return'] for t in trades], dtype=np.float64)
    gross = np.array([t['gross_return'] for t in trades], dtype=np.float64)
    costs = np.array([t['cost'] for t in trades], dtype=np.float64)
    equity = np.cumprod(1.0 + nets)
    peaks = np.maximum.accumulate(equity)
    drawdown = 1.0 - equity / peaks
    return {
        'trades': int(len(trades)),
        'win_rate': float(np.mean(nets > 0)),
        'net_return': float(equity[-1] - 1.0),
        'gross_return': float(np.sum(gross)),
        'total_cost': float(np.sum(costs)),
        'avg_net_return': float(np.mean(nets)),
        'max_drawdown': float(np.max(drawdown)),
    }


def evaluate_trading(probs, close, horizon, confidence=0.6,
                     fee_rate=0.0004, slippage_rate=0.0002):
    model_trades = directional_trades(probs, close, horizon, confidence, fee_rate, slippage_rate)

    n = min(len(probs), len(close))
    baseline_probs = np.zeros((n, 3), dtype=np.float64)
    baseline_probs[:, 2] = 1.0
    baseline_trades = directional_trades(
        baseline_probs, close, horizon, 0.0, fee_rate, slippage_rate
    )

    model_summary = summarize_trades(model_trades)
    baseline_summary = summarize_trades(baseline_trades)
    return {
        'model': model_summary,
        'baseline_long': baseline_summary,
        'beats_baseline': model_summary['net_return'] > baseline_summary['net_return'],
        'costs': {'fee_rate': fee_rate, 'slippage_rate': slippage_rate},
    }


def aggregate_folds(folds):
    if not folds:
        return {}
    net = 1.0
    trades = 0
    wins = 0.0
    worst_dd = 0.0
    for f in folds:
        m = f.get('model', {})
        net *= 1.0 + float(m.get('net_return', 0.0))
        trades += int(m.get('trades', 0))
        wins += float(m.get('win_rate', 0.0)) * int(m.get('trades', 0))
        worst_dd = max(worst_dd, float(m.get('max_drawdown', 0.0)))
    return {
        'folds': len(folds),
        'trades': trades,
        'win_rate': (wins / trades) if trades else 0.0,
        'compounded_net_return': net - 1.0,
        'worst_fold_drawdown': worst_dd,
    }