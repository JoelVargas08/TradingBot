"""
Métricas predictivas y de trading con costes realistas (Fase NN).

La simulación es deliberadamente conservadora:
- señales no solapadas (entrada cada horizon velas como máximo),
- entrada/salida al cierre,
- comisión + slippage en ambos lados,
- se compara contra una línea base alcista con el mismo calendario y costes.
"""
import numpy as np

TRADE_SIDES = ('both', 'long', 'short')


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


def directional_trades(probs, close, horizon, confidence, fee_rate, slippage_rate, side='both'):
    if side not in TRADE_SIDES:
        raise ValueError(f'side inválido: {side!r}')
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
        direction = 0
        if side in ('both', 'long') and strength >= confidence:
            direction = 1
        elif side in ('both', 'short') and strength <= -confidence:
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
                     fee_rate=0.0004, slippage_rate=0.0002, side='both'):
    model_trades = directional_trades(
        probs, close, horizon, confidence, fee_rate, slippage_rate, side=side
    )

    n = min(len(probs), len(close))
    baseline_probs = np.zeros((n, 3), dtype=np.float64)
    baseline_probs[:, 2] = 1.0
    baseline_trades = directional_trades(
        baseline_probs, close, horizon, 0.0, fee_rate, slippage_rate, side='long'
    )

    model_summary = summarize_trades(model_trades)
    baseline_summary = summarize_trades(baseline_trades)
    return {
        'model': model_summary,
        'baseline_long': baseline_summary,
        'beats_baseline': model_summary['net_return'] > baseline_summary['net_return'],
        'side': side,
        'confidence': confidence,
        'costs': {'fee_rate': fee_rate, 'slippage_rate': slippage_rate},
    }


def calibrate_walk_forward(fold_preds, horizon, fee_rate, slippage_rate,
                           min_trades=20, max_fold_loss=0.02,
                           side_values=TRADE_SIDES, confidence_values=None):
    """Elige side/confianza solo con las predicciones walk-forward.

    `fold_preds` es una lista de dicts con `probs` y `close` por fold. Se
    maximiza el retorno compuesto agregado sujeto a un mínimo de trades y a
    que ningún fold pierda más de `max_fold_loss`. El holdout no participa.
    """
    if confidence_values is None:
        confidence_values = [round(x, 2) for x in np.arange(0.05, 0.65, 0.05)]
    if not fold_preds:
        return {'status': 'no_folds', 'candidates': []}

    candidates = []
    for side in side_values:
        for conf in confidence_values:
            folds = [
                evaluate_trading(
                    fp['probs'], fp['close'], horizon, confidence=float(conf),
                    fee_rate=fee_rate, slippage_rate=slippage_rate, side=side,
                )
                for fp in fold_preds
            ]
            agg = aggregate_folds(folds)
            fold_nets = [float(f['model']['net_return']) for f in folds]
            worst_fold = float(min(fold_nets)) if fold_nets else 0.0
            trades = int(agg.get('trades', 0))
            candidates.append({
                'side': side,
                'confidence': float(conf),
                'trades': trades,
                'win_rate': float(agg.get('win_rate', 0.0)),
                'compounded_net_return': float(agg.get('compounded_net_return', 0.0)),
                'worst_fold_drawdown': float(agg.get('worst_fold_drawdown', 0.0)),
                'positive_folds': int(sum(1 for n in fold_nets if n > 0)),
                'min_fold_net_return': worst_fold,
                'fold_net_returns': fold_nets,
                'eligible': bool(
                    trades >= int(min_trades) and worst_fold >= -abs(max_fold_loss)
                ),
            })

    eligible = [c for c in candidates if c['eligible']]
    best = max(
        eligible,
        key=lambda c: (c['compounded_net_return'], c['trades'], c['win_rate']),
    ) if eligible else {}
    candidates.sort(
        key=lambda c: (
            c['eligible'], c['compounded_net_return'], c['trades'], c['win_rate'],
        ),
        reverse=True,
    )
    return {
        'status': 'ok' if eligible else 'no_candidate',
        'min_trades': int(min_trades),
        'max_fold_loss': float(max_fold_loss),
        'eligible_candidates': len(eligible),
        'selected': dict(best),
        'candidates': candidates[:10],
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