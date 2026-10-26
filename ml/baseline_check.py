"""Compara reglas simples con los mismos costes que la red neuronal."""
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
from features import compute_features  # noqa: E402


def main():
    parser = argparse.ArgumentParser(description='Evalúa reglas simples con costes de trading')
    src = parser.add_mutually_exclusive_group(required=True)
    src.add_argument('--db')
    src.add_argument('--csv')
    parser.add_argument('--symbol', default='BTCUSDT')
    parser.add_argument('--timeframe', default='1h')
    parser.add_argument('--horizon', type=int, default=24)
    parser.add_argument('--fee-bps', type=float, default=4.0)
    parser.add_argument('--slippage-bps', type=float, default=2.0)
    parser.add_argument('--out', default='')
    args = parser.parse_args()

    if args.horizon < 1:
        parser.error('--horizon debe ser >= 1')

    if args.db:
        df = load_from_db(args.db, args.symbol, args.timeframe)
    else:
        df = load_from_csv(args.csv)
    raw, quality = prepare_candles(df, args.timeframe)
    if len(raw) < args.horizon * 3:
        raise SystemExit(f'datos insuficientes: {len(raw)} velas')
    feat = compute_features(raw)
    close = raw['close'].to_numpy(dtype=np.float64)
    fee_rate = args.fee_bps / 10000.0
    slippage_rate = args.slippage_bps / 10000.0

    trades = evaluate_baselines(
        close, feat, args.horizon, fee_rate, slippage_rate
    )
    results = {
        name: {
            'side': trade['side'],
            'confidence': trade['confidence'],
            'trades': trade['model']['trades'],
            'win_rate': trade['model']['win_rate'],
            'net_return': trade['model']['net_return'],
            'max_drawdown': trade['model']['max_drawdown'],
        }
        for name, trade in trades.items()
    }
    baseline = next(iter(trades.values()))['baseline_long']

    payload = {
        'symbol': args.symbol,
        'timeframe': args.timeframe,
        'rows': int(len(raw)),
        'horizon': args.horizon,
        'costs': {'fee_rate': fee_rate, 'slippage_rate': slippage_rate},
        'data': quality.to_dict(),
        'baseline_long': baseline,
        'strategies': results,
    }

    print(f"{'strategy':<20} {'side':<6} {'trades':>7} {'win':>7} {'net':>9} {'dd':>9}")
    for name, r in results.items():
        print(
            f"{name:<20} {r['side']:<6} {r['trades']:>7} "
            f"{r['win_rate']:>7.3f} {r['net_return']:>9.4f} {r['max_drawdown']:>9.4f}"
        )
    print(
        f"{'baseline_long':<20} {'long':<6} {baseline['trades']:>7} "
        f"{baseline['win_rate']:>7.3f} {baseline['net_return']:>9.4f} "
        f"{baseline['max_drawdown']:>9.4f}"
    )

    if args.out:
        path = Path(args.out)
        path.parent.mkdir(parents=True, exist_ok=True)
        tmp = path.with_suffix(path.suffix + '.tmp')
        with open(tmp, 'w', encoding='utf-8') as fh:
            json.dump(payload, fh, ensure_ascii=False, indent=2)
        os.replace(tmp, path)
        print(f'[ok] informe: {path}')
    return 0


if __name__ == '__main__':
    sys.exit(main())
