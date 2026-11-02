"""Evalúa un ensemble de modelos MLP ya entrenados sin reentrenar."""
import argparse
import json
import os
import sys
from pathlib import Path

import numpy as np

_HERE = os.path.dirname(os.path.abspath(__file__))
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

from baselines import evaluate_baselines  # noqa: E402
from dataset import load_from_csv, load_from_db, prepare_candles  # noqa: E402
from features import NN_FEATURE_NAMES, compute_features, nn_feature_matrix  # noqa: E402
from labels import add_labels  # noqa: E402
from nn.calibration import apply_temperature  # noqa: E402
from nn.metrics import classification_metrics, evaluate_trading  # noqa: E402


def _softmax(z):
    z = z - np.max(z, axis=1, keepdims=True)
    e = np.exp(z)
    return e / np.sum(e, axis=1, keepdims=True)


def combine_probs(member_probs, mode='probs', conf=0.35, min_votes=0):
    member_probs = [np.asarray(p, dtype=np.float64) for p in member_probs]
    probs = np.mean(member_probs, axis=0)
    trade_probs = probs
    min_votes = int(min_votes)
    if mode == 'logits':
        logits = np.mean(
            [np.log(np.clip(p, 1e-12, 1.0)) for p in member_probs], axis=0
        )
        probs = _softmax(logits)
        trade_probs = probs
    elif mode == 'vote':
        min_votes = min_votes or (len(member_probs) // 2 + 1)
        votes = np.zeros(len(probs), dtype=np.int64)
        for p in member_probs:
            strength = p[:, 2] - p[:, 0]
            votes += (strength >= conf).astype(np.int64)
            votes -= (strength <= -conf).astype(np.int64)
        direction = np.sign(votes) * (np.abs(votes) >= min_votes)
        trade_probs = np.zeros_like(probs)
        trade_probs[:, 1] = 1.0
        trade_probs[direction > 0, 2] = 1.0
        trade_probs[direction > 0, 1] = 0.0
        trade_probs[direction < 0, 0] = 1.0
        trade_probs[direction < 0, 1] = 0.0
    return probs, trade_probs, min_votes


def _forward(model, X):
    a = (X - np.asarray(model['mu'])) / np.asarray(model['sigma'])
    for layer in model['layers'][:-1]:
        a = np.maximum(0.0, a @ np.asarray(layer['w']) + np.asarray(layer['b']))
    layer = model['layers'][-1]
    probs = _softmax(a @ np.asarray(layer['w']) + np.asarray(layer['b']))
    return apply_temperature(probs, model.get('temperature', 1.0))


def _map_labels(raw):
    y = np.zeros(len(raw), dtype=np.int64)
    y[raw == 1] = 0
    y[raw == 0] = 1
    y[raw == 2] = 2
    return y


def _reference_key(name, horizon):
    return {
        'always_long': 'always_long',
        'momentum': f'momentum_{horizon}',
        'rsi': 'rsi_30_70',
        'macd': 'macd_hist',
    }.get(name, '')


def load_models(paths):
    models = []
    for path in paths:
        with open(path, 'r', encoding='utf-8') as fh:
            model = json.load(fh)
        if model.get('feature_names') != list(NN_FEATURE_NAMES):
            raise ValueError(f'{path}: feature_names incompatibles')
        models.append(model)
    if not models:
        raise ValueError('no hay modelos')
    dim = models[0]['input_dim']
    if any(m['input_dim'] != dim for m in models):
        raise ValueError('modelos con input_dim distinto')
    return models


def main():
    parser = argparse.ArgumentParser(description='Ensemble de modelos MLP NumPy')
    parser.add_argument('--models', nargs='+', required=True)
    parser.add_argument('--db')
    parser.add_argument('--csv')
    parser.add_argument('--symbol', default='BTCUSDT')
    parser.add_argument('--timeframe', default='1h')
    parser.add_argument('--last-bars', type=int, default=0)
    parser.add_argument('--start-ts', type=int, default=0)
    parser.add_argument('--end-ts', type=int, default=0)
    parser.add_argument('--horizon', type=int, default=24)
    parser.add_argument('--conf', type=float, default=0.35)
    parser.add_argument('--side', choices=('both', 'long', 'short'), default='both')
    parser.add_argument('--combine', choices=('probs', 'logits', 'vote'), default='probs')
    parser.add_argument('--min-votes', type=int, default=0)
    parser.add_argument('--reference',
                        choices=('none', 'always_long', 'momentum', 'rsi', 'macd'),
                        default='none')
    parser.add_argument('--fee-bps', type=float, default=4.0)
    parser.add_argument('--slippage-bps', type=float, default=2.0)
    parser.add_argument('--min-trades', type=int, default=10)
    parser.add_argument('--out')
    args = parser.parse_args()

    if not args.db and not args.csv:
        parser.error('se requiere --db o --csv')

    models = load_models(args.models)
    if args.db:
        df = load_from_db(args.db, args.symbol, args.timeframe)
    else:
        df = load_from_csv(args.csv)
    if args.last_bars > 0:
        df = df.iloc[-args.last_bars:].copy()
    raw, quality = prepare_candles(df, args.timeframe)
    cfg = models[0].get('meta', {}).get('config', {})
    sigma_mult = float(cfg.get('sigma_mult', 1.5))
    feat = add_labels(
        compute_features(raw), horizon=args.horizon, sigma_mult=sigma_mult,
    )
    clean = feat.dropna(subset=NN_FEATURE_NAMES + ['label']).reset_index(drop=True)
    if args.start_ts > 0:
        clean = clean.loc[clean['ts'] >= args.start_ts].reset_index(drop=True)
    if args.end_ts > 0:
        clean = clean.loc[clean['ts'] <= args.end_ts].reset_index(drop=True)
    if len(clean) < max(50, args.horizon * 2):
        raise SystemExit(f'filas insuficientes: {len(clean)}')
    X = nn_feature_matrix(clean)
    close = clean['close'].to_numpy(dtype=np.float64)
    y = _map_labels(clean['label'].to_numpy(dtype=np.int64))
    finite = np.isfinite(X).all(axis=1) & np.isfinite(close)
    X = X[finite]
    close = close[finite]
    y = y[finite]
    clean = clean.loc[finite].reset_index(drop=True)

    member_probs = [_forward(m, X) for m in models]
    probs, trade_probs, min_votes = combine_probs(
        member_probs, mode=args.combine, conf=args.conf, min_votes=args.min_votes
    )
    pred = classification_metrics(y, probs)
    trade = evaluate_trading(
        trade_probs, close, args.horizon, confidence=args.conf,
        fee_rate=args.fee_bps / 10000.0,
        slippage_rate=args.slippage_bps / 10000.0,
        side=args.side,
    )
    baselines = {
        name: item['model']
        for name, item in evaluate_baselines(
            close, clean, args.horizon,
            args.fee_bps / 10000.0, args.slippage_bps / 10000.0,
        ).items()
    }
    ref_name = _reference_key(args.reference, args.horizon)
    ref = baselines.get(ref_name) if ref_name else None
    model_net = trade['model']['net_return']
    ref_ok = bool(ref is None or model_net > ref['net_return'])
    gate = bool(
        trade['model']['trades'] >= args.min_trades
        and trade.get('beats_baseline') is True
        and model_net > 0.0
        and ref_ok
    )

    result = {
        'models': [str(Path(p)) for p in args.models],
        'symbol': args.symbol,
        'timeframe': args.timeframe,
        'last_bars': args.last_bars,
        'start_ts': args.start_ts,
        'end_ts': args.end_ts,
        'combine': args.combine,
        'min_votes': min_votes,
        'data': quality.to_dict(),
        'samples': int(len(y)),
        'predictive': pred,
        'trading': trade,
        'baselines': baselines,
        'reference': {'name': ref_name, 'summary': ref, 'model_beats': ref_ok},
        'gate': gate,
    }
    print(
        f"ensemble {args.symbol} {args.timeframe} samples={len(y)} "
        f"acc={pred['accuracy']:.3f} net={model_net:.3f} "
        f"trades={trade['model']['trades']} dd={trade['model']['max_drawdown']:.3f} "
        f"baseline={trade['baseline_long']['net_return']:.3f} "
        + (f"ref={ref['net_return']:.3f} " if ref else '')
        + f"gate={gate}"
    )
    if args.out:
        out = Path(args.out)
        out.parent.mkdir(parents=True, exist_ok=True)
        with open(out, 'w', encoding='utf-8') as fh:
            json.dump(result, fh, ensure_ascii=False, indent=2)
        print(f'[ok] informe: {out}')


if __name__ == '__main__':
    main()
