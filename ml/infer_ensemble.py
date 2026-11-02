#!/usr/bin/env python3
"""
Inferencia con ensemble (sin entrenar).
"""
import argparse
import json
import os
import sys
from datetime import datetime, timezone

import numpy as np

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)

from dataset import load_from_db, load_from_csv, prepare_candles
from features import compute_features, NN_FEATURE_NAMES
from nn_ensemble import load_models, combine_probs, _forward

def utc_now():
    return datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')

def main():
    p = argparse.ArgumentParser(description='Inferencia ensemble (sin entrenar)')
    p.add_argument('--db', default='')
    p.add_argument('--csv', default='')
    p.add_argument('--symbol', default='BTCUSDT')
    p.add_argument('--timeframe', default='1h')
    p.add_argument('--limit', type=int, default=200)
    p.add_argument('--models', nargs='+', default=[
        'ml/models/candidates/cand_1793104547/model.json',
        'ml/models/candidates/cand_1793105216/model.json',
        'ml/models/candidates/cand_1793105236/model.json',
    ])
    p.add_argument('--conf', type=float, default=0.25)
    p.add_argument('--combine', default='vote', choices=['probs','logits','vote'])
    p.add_argument('--min-votes', type=int, default=2)
    p.add_argument('--out', default='')
    args = p.parse_args()

    if args.csv:
        df = load_from_csv(args.csv)
    elif args.db:
        df = load_from_db(args.db, args.symbol, args.timeframe, limit=args.limit)
    else:
        raise SystemExit('Especifica --db o --csv')

    raw, q = prepare_candles(df, args.timeframe)
    if len(raw) < 64:
        raise SystemExit(f'Insuficientes velas: {len(raw)} < 64')

    feat = compute_features(raw)
    for fn in NN_FEATURE_NAMES:
        if fn not in feat.columns:
            raise SystemExit(f'Falta feature: {fn}')
    X = feat[NN_FEATURE_NAMES].to_numpy(dtype=np.float64)
    if np.isnan(X).any():
        col_mean = np.nanmean(X, axis=0)
        inds = np.where(np.isnan(X))
        X[inds] = np.take(col_mean, inds[1])

    models = load_models(args.models)
    if not models:
        raise SystemExit('Sin modelos')

    member_probs = []
    for m in models:
        member_probs.append(_forward(m, X))
    probs, signal_last, conf_last = combine_probs(member_probs, mode=args.combine, conf=args.conf, min_votes=args.min_votes)
    prob_down, prob_flat, prob_up = float(probs[-1,0]), float(probs[-1,1]), float(probs[-1,2])
    strength = prob_up - prob_down
    if strength >= args.conf:
        signal = 'buy'
    elif strength <= -args.conf:
        signal = 'sell'
    else:
        signal = 'none'
    conf = abs(strength)

    res = {
        'ts': utc_now(),
        'symbol': args.symbol,
        'timeframe': args.timeframe,
        'bars': len(raw),
        'last_ts': int(raw['ts'].iloc[-1]) if 'ts' in raw.columns else None,
        'signal': signal,
        'confidence': round(conf, 4),
        'prob_up': round(prob_up, 4),
        'prob_down': round(prob_down, 4),
        'prob_flat': round(prob_flat, 4),
        'price': float(raw['close'].iloc[-1]),
        'models': len(models),
        'combine': args.combine,
    }
    print(json.dumps(res, ensure_ascii=False, indent=2))
    if args.out:
        os.makedirs(os.path.dirname(args.out) or '.', exist_ok=True)
        with open(args.out,'w',encoding='utf-8') as fh:
            json.dump(res, fh, ensure_ascii=False, indent=2)

if __name__ == '__main__':
    main()
