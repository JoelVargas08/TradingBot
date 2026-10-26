"""
Entrenamiento de una red neuronal pequeña (MLP) para señales experimentales.

Diseño seguro:
- separa desarrollo (walk-forward) del test final reservado,
- normaliza solo con datos de entrenamiento,
- early stopping con validación interna (nunca con el test final),
- compara contra una línea base alcista con los mismos costes,
- guarda un candidato y metadatos; nunca promueve automáticamente,
- aplica límites de tiempo, RAM, filas y épocas.

Uso:
  py ml/nn_train.py --synthetic
  py ml/nn_train.py --db data/bot.db --symbol BTCUSDT --timeframe 1h
  py ml/nn_train.py --csv velas.csv --out ml/models/candidates/candidate.json
"""
import argparse
import json
import os
import sys
import time
import traceback
from datetime import datetime, timezone
from pathlib import Path

import numpy as np

_HERE = os.path.dirname(os.path.abspath(__file__))
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

from baselines import evaluate_baselines  # noqa: E402
from dataset import (  # noqa: E402
    dataset_hash,
    load_from_csv,
    load_from_db,
    load_synthetic,
    prepare_candles,
)
from features import NN_FEATURE_NAMES, compute_features, nn_feature_matrix  # noqa: E402
from labels import add_labels, walk_forward_splits  # noqa: E402
from nn.calibration import apply_temperature, fit_temperature  # noqa: E402
from nn.config import load_config  # noqa: E402
from nn.metrics import (  # noqa: E402
    aggregate_folds,
    calibrate_walk_forward,
    classification_metrics,
    evaluate_trading,
)
from nn.model import Adam, MLP  # noqa: E402
from nn.registry import ModelRegistry  # noqa: E402
from nn.resources import ResourceLimiter  # noqa: E402

DEFAULT_REPORT_DIR = Path(_HERE) / 'reports'
DEFAULT_CANDIDATE_DIR = Path(_HERE) / 'models' / 'candidates'


def utc_now():
    return datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')


def write_json(path, payload):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + '.tmp')
    with open(tmp, 'w', encoding='utf-8') as fh:
        json.dump(payload, fh, ensure_ascii=False, indent=2, default=_json_default)
    os.replace(tmp, path)


def _json_default(obj):
    if isinstance(obj, (np.integer,)):
        return int(obj)
    if isinstance(obj, (np.floating,)):
        return float(obj)
    if isinstance(obj, (np.ndarray,)):
        return obj.tolist()
    raise TypeError(f'not JSON serializable: {type(obj)}')


def class_weights(y):
    counts = np.bincount(y, minlength=3).astype(np.float64)
    counts[counts == 0] = 1.0
    weights = counts.sum() / (len(counts) * counts)
    return weights[y]


def carve_validation(train_idx, gap, val_frac, min_train):
    train_idx = np.asarray(train_idx, dtype=np.int64)
    val_size = max(30, int(len(train_idx) * val_frac))
    gap = max(0, int(gap))
    cut = len(train_idx) - val_size - gap
    if cut < min_train:
        return train_idx, np.array([], dtype=np.int64)
    return train_idx[:cut], train_idx[len(train_idx) - val_size:]


def fit_mlp(X_train, y_train, X_val, y_val, cfg, seed, guard, epochs, patience,
            sample_weight=None, verbose=False):
    model = MLP(X_train.shape[1], hidden_sizes=cfg.hidden_sizes, seed=seed)
    opt = Adam(
        model,
        lr=cfg.lr,
        weight_decay=cfg.weight_decay,
        max_grad_norm=cfg.max_grad_norm,
    )
    rng = np.random.RandomState(seed)
    best_state = None
    best_val = float('inf')
    best_epoch = 0
    wait = 0
    history = []

    for epoch in range(1, epochs + 1):
        guard.check()
        order = rng.permutation(len(X_train))
        epoch_loss = 0.0
        batches = 0
        for start in range(0, len(order), cfg.batch_size):
            idx = order[start:start + cfg.batch_size]
            sw = sample_weight[idx] if sample_weight is not None else None
            loss, grads, _ = model.loss_and_grad(
                X_train[idx], y_train[idx], dropout=cfg.dropout,
                weight_decay=0.0, sample_weight=sw, rng=rng,
            )
            opt.step(model, grads)
            epoch_loss += loss
            batches += 1
        epoch_loss /= max(1, batches)

        if len(X_val) > 0:
            p_val = model.predict_proba(X_val)
            eps = 1e-12
            p_val = np.clip(p_val, eps, 1.0 - eps)
            val_loss = float(-np.mean(np.log(p_val[np.arange(len(y_val)), y_val])))
            monitored = val_loss
        else:
            val_loss = None
            monitored = epoch_loss

        history.append({
            'epoch': epoch,
            'train_loss': float(epoch_loss),
            'val_loss': None if val_loss is None else float(val_loss),
        })
        if verbose and (epoch == 1 or epoch % 5 == 0):
            print(f'[epoch {epoch}] train={epoch_loss:.6f} val={val_loss}', flush=True)

        if monitored < best_val - 1e-6:
            best_val = monitored
            best_epoch = epoch
            best_state = [
                {'w': layer['w'].copy(), 'b': layer['b'].copy()}
                for layer in model.layers
            ]
            wait = 0
        else:
            wait += 1
            if wait >= patience:
                if verbose:
                    print(f'[train] early stopping en época {epoch}', flush=True)
                break

    if best_state is not None:
        model.layers = best_state
    return model, {
        'epochs_run': len(history),
        'best_epoch': best_epoch,
        'best_val_loss': None if best_val == float('inf') else float(best_val),
        'stopped_early': len(history) < epochs,
        'history': history,
    }


def scaler_from(X):
    mu = X.mean(axis=0)
    sigma = X.std(axis=0)
    sigma = np.where(sigma < 1e-12, 1.0, sigma)
    return mu, sigma


def transform(X, mu, sigma):
    return (X - mu) / sigma


def _map_labels(raw_label):
    """Mapea las etiquetas de labels.py a clases 0=down, 1=flat, 2=up."""
    y = np.zeros(len(raw_label), dtype=np.int64)
    y[raw_label == 1] = 0
    y[raw_label == 0] = 1
    y[raw_label == 2] = 2
    return y


def _reference_key(name, horizon):
    return {
        'always_long': 'always_long',
        'momentum': f'momentum_{horizon}',
        'rsi': 'rsi_30_70',
        'macd': 'macd_hist',
    }.get(name, '')


def _evaluate_external(model, mu, sigma, args, cfg, conf, side, report,
                       temperature=1.0, reference_name=''):
    """Evalúa el modelo final en otro símbolo/dataset sin tocar el holdout."""
    source = args.external_db or args.external_csv
    if not source:
        return True
    ext_tf = args.external_timeframe or args.timeframe
    try:
        ext_df = (
            load_from_db(
                args.external_db, args.external_symbol, ext_tf,
                limit=cfg.max_data_rows,
            )
            if args.external_db else load_from_csv(args.external_csv)
        )
        if len(ext_df) > cfg.max_data_rows:
            ext_df = ext_df.iloc[-cfg.max_data_rows:].copy()
        ext_raw, ext_q = prepare_candles(ext_df, ext_tf)
        ext_feat = add_labels(
            compute_features(ext_raw),
            horizon=cfg.horizon, sigma_mult=cfg.sigma_mult,
        )
        ext_clean = ext_feat.dropna(
            subset=NN_FEATURE_NAMES + ['label']
        ).reset_index(drop=True)
        if len(ext_clean) < max(50, cfg.horizon * 2):
            raise ValueError(f'filas externas insuficientes: {len(ext_clean)}')
        ext_X = nn_feature_matrix(ext_clean)
        ext_close = ext_clean['close'].to_numpy(dtype=np.float64)
        ext_y = _map_labels(ext_clean['label'].to_numpy(dtype=np.int64))
        ext_finite = np.isfinite(ext_X).all(axis=1) & np.isfinite(ext_close)
        ext_X = ext_X[ext_finite]
        ext_close = ext_close[ext_finite]
        ext_y = ext_y[ext_finite]
        ext_clean = ext_clean.loc[ext_finite].reset_index(drop=True)
        ext_probs = apply_temperature(
            model.predict_proba(transform(ext_X, mu, sigma)), temperature
        )
        ext_pred = classification_metrics(ext_y, ext_probs)
        ext_trade = evaluate_trading(
            ext_probs, ext_close, cfg.horizon, confidence=conf,
            fee_rate=args.fee_bps / 10000.0,
            slippage_rate=args.slippage_bps / 10000.0,
            side=side,
        )
        ext_baselines = {
            name: trade['model']
            for name, trade in evaluate_baselines(
                ext_close, ext_clean, cfg.horizon,
                args.fee_bps / 10000.0, args.slippage_bps / 10000.0,
            ).items()
        }
        ext_reference = ext_baselines.get(reference_name) if reference_name else None
        reference_ok = bool(
            ext_reference is None
            or ext_trade['model']['net_return'] > ext_reference['net_return']
        )
        external_ok = bool(
            ext_trade['model']['trades'] >= args.min_test_trades
            and ext_trade.get('beats_baseline') is True
            and ext_trade['model']['net_return'] > 0.0
            and reference_ok
        )
        report['external'] = {
            'symbol': args.external_symbol,
            'timeframe': ext_tf,
            'data': ext_q.to_dict(),
            'dataset_hash': dataset_hash(ext_raw) if len(ext_raw) else '',
            'predictive': ext_pred,
            'trading': ext_trade,
            'baselines': ext_baselines,
            'passes_gate': external_ok,
        }
        if reference_name:
            report['external']['reference'] = {
                'name': reference_name,
                'summary': ext_reference,
                'model_beats': reference_ok,
            }
        print(
            f'[externo] {args.external_symbol} {ext_tf} '
            f'acc={ext_pred["accuracy"]:.3f} '
            f'net={ext_trade["model"]["net_return"]:.3f} '
            f'baseline={ext_trade["baseline_long"]["net_return"]:.3f} '
            + (f'ref={ext_reference["net_return"]:.3f} ' if ext_reference else '')
            + f'trades={ext_trade["model"]["trades"]} gate={external_ok}',
            flush=True,
        )
        return external_ok
    except Exception as exc:  # noqa: BLE001
        report['external'] = {'status': 'failed', 'error': str(exc)}
        print(f'[aviso] validación externa falló: {exc}', flush=True)
        return False


def model_json(model, mu, sigma, meta):
    return {
        'type': 'mlp_numpy',
        'schema_version': 1,
        'input_dim': int(model.layers[0]['w'].shape[0]),
        'hidden_sizes': [int(l['w'].shape[1]) for l in model.layers[:-1]],
        'layers': [
            {'w': layer['w'].tolist(), 'b': layer['b'].tolist()}
            for layer in model.layers
        ],
        'mu': mu.tolist(),
        'sigma': sigma.tolist(),
        'temperature': float(meta.get('config', {}).get('temperature', 1.0) or 1.0),
        'feature_names': list(NN_FEATURE_NAMES),
        'meta': meta,
    }


def main():
    parser = argparse.ArgumentParser(
        description='Entrena un MLP NumPy para trading (candidato, sin promoción automática)'
    )
    src = parser.add_mutually_exclusive_group(required=True)
    src.add_argument('--db', help='ruta a bot.db (SQLite)')
    src.add_argument('--csv', help='CSV con ts,open,high,low,close,volume')
    src.add_argument('--synthetic', action='store_true', help='datos sintéticos')
    parser.add_argument('--symbol', default='BTCUSDT')
    parser.add_argument('--timeframe', default='1h')
    parser.add_argument('--splits', type=int, default=4)
    parser.add_argument('--conf', type=float, default=0.6,
                        help='umbral de confianza para métricas de trading')
    parser.add_argument('--side', choices=('both', 'long', 'short'), default='both',
                        help='dirección permitida en la simulación de trading')
    parser.add_argument('--reference',
                        choices=('none', 'always_long', 'momentum', 'rsi', 'macd'),
                        default='none',
                        help='regla simple que el modelo debe superar')
    parser.add_argument('--calibrate', action='store_true',
                        help='elige side/confianza solo con walk-forward')
    parser.add_argument('--min-calibration-trades', type=int, default=20,
                        help='mínimo de trades walk-forward para calibrar')
    parser.add_argument('--max-fold-loss', type=float, default=0.02,
                        help='pérdida máxima permitida en un fold de calibración')
    parser.add_argument('--dev-only', action='store_true',
                        help='no evalúa el holdout (solo desarrollo/walk-forward)')
    parser.add_argument('--horizon', type=int, default=0,
                        help='0 usa el horizonte de la configuración')
    parser.add_argument('--sigma-mult', type=float, default=0.0,
                        help='0 usa sigma_mult de la configuración')
    parser.add_argument('--fee-bps', type=float, default=4.0)
    parser.add_argument('--slippage-bps', type=float, default=2.0)
    parser.add_argument('--min-test-trades', type=int, default=10)
    ext = parser.add_mutually_exclusive_group()
    ext.add_argument('--external-db', help='SQLite con datos para validación externa')
    ext.add_argument('--external-csv', help='CSV para validación externa')
    parser.add_argument('--external-symbol', default='BTCUSDT')
    parser.add_argument('--external-timeframe', default='')
    parser.add_argument('--max-seconds', type=int, default=0,
                        help='0 usa NN_MAX_TIME_SEC del entorno')
    parser.add_argument('--max-ram-mb', type=int, default=0,
                        help='0 usa NN_MAX_RAM_MB del entorno')
    parser.add_argument('--max-bars', type=int, default=0,
                        help='0 usa NN_MAX_DATA_ROWS del entorno')
    parser.add_argument('--min-rows', type=int, default=0,
                        help='0 usa NN_MIN_TRAIN_ROWS del entorno')
    parser.add_argument('--epochs', type=int, default=0,
                        help='0 usa NN_EPOCHS del entorno')
    parser.add_argument('--batch-size', type=int, default=0)
    parser.add_argument('--seed', type=int, default=0)
    parser.add_argument('--out', default='', help='ruta del modelo candidato')
    parser.add_argument('--report', default='')
    parser.add_argument('--verbose', action='store_true')
    args = parser.parse_args()

    cfg = load_config()
    if args.max_seconds > 0:
        cfg.max_time_sec = args.max_seconds
    if args.max_ram_mb > 0:
        cfg.max_ram_mb = args.max_ram_mb
    if args.max_bars > 0:
        cfg.max_data_rows = args.max_bars
    if args.min_rows > 0:
        cfg.min_train_rows = args.min_rows
    if args.epochs > 0:
        cfg.epochs = args.epochs
    if args.batch_size > 0:
        cfg.batch_size = args.batch_size
    if args.seed > 0:
        cfg.seed = args.seed
    if args.horizon > 0:
        cfg.horizon = args.horizon
    if args.sigma_mult > 0:
        cfg.sigma_mult = args.sigma_mult
    if args.min_calibration_trades < 1:
        parser.error('--min-calibration-trades debe ser >= 1')
    if not 0.0 <= args.max_fold_loss <= 1.0:
        parser.error('--max-fold-loss debe estar entre 0 y 1')
    if not 0.0 < args.conf <= 1.0:
        parser.error('--conf debe estar entre 0 y 1')

    stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
    report_path = Path(args.report) if args.report else DEFAULT_REPORT_DIR / f'nn_train_{stamp}.json'
    started = time.monotonic()
    guard = ResourceLimiter(max_ram_mb=cfg.max_ram_mb, max_time_sec=cfg.max_time_sec)
    report = {
        'status': 'running',
        'started_at': utc_now(),
        'code_version': os.environ.get('GITHUB_SHA', ''),
        'source': 'synthetic' if args.synthetic else ('csv' if args.csv else 'db'),
        'symbol': args.symbol,
        'timeframe': args.timeframe,
        'config': {
            'hidden_sizes': list(cfg.hidden_sizes),
            'lr': cfg.lr,
            'batch_size': cfg.batch_size,
            'epochs': cfg.epochs,
            'patience': cfg.patience,
            'dropout': cfg.dropout,
            'weight_decay': cfg.weight_decay,
            'seed': cfg.seed,
            'horizon': cfg.horizon,
            'sigma_mult': cfg.sigma_mult,
            'side': args.side,
            'confidence': args.conf,
            'reference': args.reference,
            'calibrate': bool(args.calibrate),
            'dev_only': bool(args.dev_only),
        },
        'limits': {
            'max_ram_mb': cfg.max_ram_mb,
            'max_time_sec': cfg.max_time_sec,
            'max_data_rows': cfg.max_data_rows,
            'epochs': cfg.epochs,
            'batch_size': cfg.batch_size,
        },
    }

    model_out = None
    try:
        if args.synthetic:
            df = load_synthetic()
        elif args.csv:
            df = load_from_csv(args.csv)
        else:
            df = load_from_db(args.db, args.symbol, args.timeframe, limit=cfg.max_data_rows)

        if len(df) > cfg.max_data_rows:
            df = df.iloc[-cfg.max_data_rows:].copy()

        raw, quality = prepare_candles(df, args.timeframe)
        report['data'] = quality.to_dict()
        report['data']['dataset_hash'] = dataset_hash(raw) if len(raw) else ''
        if len(raw) < cfg.min_train_rows:
            raise ValueError(
                f'datos insuficientes: {len(raw)} velas < {cfg.min_train_rows}'
            )

        feat = add_labels(compute_features(raw), horizon=cfg.horizon, sigma_mult=cfg.sigma_mult)
        clean = feat.dropna(subset=NN_FEATURE_NAMES + ['label']).reset_index(drop=True)
        if len(clean) < cfg.min_train_rows:
            raise ValueError(
                f'filas útiles insuficientes tras features/labels: {len(clean)}'
            )

        X = nn_feature_matrix(clean)
        close = clean['close'].to_numpy(dtype=np.float64)
        raw_label = clean['label'].to_numpy(dtype=np.int64)
        finite = np.isfinite(X).all(axis=1) & np.isfinite(close)
        X = X[finite]
        close = close[finite]
        raw_label = raw_label[finite]
        clean = clean.loc[finite].reset_index(drop=True)
        if len(X) < cfg.min_train_rows:
            raise ValueError(f'filas finitas insuficientes: {len(X)}')

        # Clases explícitas: 0=down, 1=flat, 2=up.
        y = _map_labels(raw_label)

        report['labels'] = {
            'type': 'triple_barrier',
            'horizon': cfg.horizon,
            'sigma_mult': cfg.sigma_mult,
            'classes': {'0': 'down', '1': 'flat', '2': 'up'},
            'distribution': {str(k): int(v) for k, v in zip(*np.unique(y, return_counts=True))},
            'clean_rows': int(len(y)),
        }

        n = len(X)
        holdout = max(int(n * cfg.test_frac), cfg.horizon * 2, 50)
        gap = max(cfg.embargo, cfg.horizon)
        dev_end = n - holdout - gap
        if dev_end < max(100, cfg.horizon * 3):
            raise ValueError(f'desarrollo insuficiente: {dev_end} filas')

        dev_idx = np.arange(dev_end)
        test_idx = np.arange(n - holdout, n)
        folds = walk_forward_splits(
            dev_end, n_splits=args.splits, train_frac=0.65, embargo=gap
        )
        report['splits'] = {
            'total_clean': int(n),
            'development': int(dev_end),
            'embargo_gap': int(gap),
            'holdout': {'start': int(test_idx[0]), 'end': int(test_idx[-1]), 'rows': int(len(test_idx))},
            'walk_forward_folds': len(folds),
        }
        if not folds:
            raise ValueError('no se pudo construir ningún fold walk-forward')

        fold_reports = []
        fold_preds = []
        fold_baselines = {}
        for fold_no, (tr_rel, te_rel) in enumerate(folds, start=1):
            guard.check()
            tr = dev_idx[tr_rel]
            te = dev_idx[te_rel]
            tr_fit, tr_val = carve_validation(tr, gap, cfg.val_frac, min_train=80)
            mu, sigma = scaler_from(X[tr_fit])
            Xtr = transform(X[tr_fit], mu, sigma)
            Xval = transform(X[tr_val], mu, sigma) if len(tr_val) else np.empty((0, X.shape[1]))
            Xte = transform(X[te], mu, sigma)

            sw = class_weights(y[tr_fit])
            model, fit_info = fit_mlp(
                Xtr, y[tr_fit], Xval, y[tr_val], cfg,
                seed=cfg.seed + fold_no, guard=guard, epochs=cfg.epochs,
                patience=cfg.patience, sample_weight=sw, verbose=args.verbose,
            )
            probs = model.predict_proba(Xte)
            val_probs = (
                model.predict_proba(Xval) if len(tr_val) else np.empty((0, 3))
            )
            temperature = fit_temperature(val_probs, y[tr_val])
            probs = apply_temperature(probs, temperature)
            fold_preds.append({'probs': probs, 'close': close[te]})
            pred_metrics = classification_metrics(y[te], probs)
            trade_metrics = evaluate_trading(
                probs, close[te], cfg.horizon, confidence=args.conf,
                fee_rate=args.fee_bps / 10000.0,
                slippage_rate=args.slippage_bps / 10000.0,
                side=args.side,
            )
            fold_base = evaluate_baselines(
                close[te], clean.iloc[te], cfg.horizon,
                args.fee_bps / 10000.0, args.slippage_bps / 10000.0,
            )
            for name, result in fold_base.items():
                fold_baselines.setdefault(name, []).append(result)
            fold_reports.append({
                'fold': fold_no,
                'train': int(len(tr_fit)),
                'validation': int(len(tr_val)),
                'test': int(len(te)),
                'temperature': float(temperature),
                'fit': fit_info,
                'predictive': pred_metrics,
                'baselines': {name: result['model'] for name, result in fold_base.items()},
                **trade_metrics,
            })
            print(
                f'[fold {fold_no}] test={len(te)} acc={pred_metrics["accuracy"]:.3f} '
                f'net={trade_metrics["model"]["net_return"]:.3f} '
                f'baseline={trade_metrics["baseline_long"]["net_return"]:.3f}',
                flush=True,
            )

        side = args.side
        conf = args.conf
        if args.calibrate:
            calibration = calibrate_walk_forward(
                fold_preds, cfg.horizon,
                fee_rate=args.fee_bps / 10000.0,
                slippage_rate=args.slippage_bps / 10000.0,
                min_trades=args.min_calibration_trades,
                max_fold_loss=args.max_fold_loss,
            )
            report['calibration'] = calibration
            selected = calibration.get('selected', {})
            if calibration.get('status') == 'ok':
                side = selected['side']
                conf = float(selected['confidence'])
                for fr, fp in zip(fold_reports, fold_preds):
                    fr.update(evaluate_trading(
                        fp['probs'], fp['close'], cfg.horizon, confidence=conf,
                        fee_rate=args.fee_bps / 10000.0,
                        slippage_rate=args.slippage_bps / 10000.0,
                        side=side,
                    ))
            elif args.dev_only:
                print(
                    '[aviso] calibración sin candidatos elegibles; se mantiene '
                    f'side={side} conf={conf:.2f}',
                    flush=True,
                )
            else:
                raise ValueError(
                    'calibración sin candidatos elegibles; no se evalúa el holdout'
                )
            print(
                f'[calibración] side={side} conf={conf:.2f} '
                f"trades={calibration.get('selected', {}).get('trades', 0)} "
                f"net={calibration.get('selected', {}).get('compounded_net_return', 0.0):.3f} "
                f"eligible={calibration.get('eligible_candidates', 0)} "
                f"status={calibration.get('status')}",
                flush=True,
            )
        report['config']['side'] = side
        report['config']['confidence'] = conf
        report['config']['max_fold_loss'] = args.max_fold_loss
        report['walk_forward'] = {
            'folds': fold_reports,
            'aggregate': aggregate_folds(fold_reports),
            'baselines': {
                name: aggregate_folds(items)
                for name, items in fold_baselines.items()
            },
        }
        reference_name = _reference_key(args.reference, cfg.horizon)
        reference_ok = True
        if reference_name:
            ref_summary = report['walk_forward']['baselines'].get(reference_name, {})
            model_summary = report['walk_forward']['aggregate']
            reference_ok = float(
                model_summary.get('compounded_net_return', -1.0)
            ) > float(ref_summary.get('compounded_net_return', 0.0))
            report['walk_forward']['reference'] = {
                'name': reference_name,
                'summary': ref_summary,
                'model_beats': reference_ok,
            }
        report['reference_gate'] = reference_ok
        if reference_name:
            print(
                f'[referencia] {reference_name} '
                f"model={report['walk_forward']['aggregate'].get('compounded_net_return', 0.0):.3f} "
                f"ref={report['walk_forward']['reference']['summary'].get('compounded_net_return', 0.0):.3f} "
                f"gate={reference_ok}",
                flush=True,
            )

        if args.dev_only and not (args.external_db or args.external_csv):
            report['validation_status'] = 'dev_only'
            report['eligible_for_promotion'] = False
            report['status'] = 'completed'
            print('[ok] dev-only: holdout reservado (sin evaluación final)', flush=True)
            return 0

        # Modelo final: entrena solo con desarrollo; el holdout sigue intacto.
        tr_fit, tr_val = carve_validation(dev_idx, gap, cfg.val_frac, min_train=80)
        mu, sigma = scaler_from(X[tr_fit])
        Xtr = transform(X[tr_fit], mu, sigma)
        Xval = transform(X[tr_val], mu, sigma) if len(tr_val) else np.empty((0, X.shape[1]))
        sw = class_weights(y[tr_fit])
        final_model, final_fit = fit_mlp(
            Xtr, y[tr_fit], Xval, y[tr_val], cfg,
            seed=cfg.seed + 10_000, guard=guard, epochs=cfg.epochs,
            patience=cfg.patience, sample_weight=sw, verbose=args.verbose,
        )
        final_val_probs = (
            final_model.predict_proba(Xval) if len(tr_val) else np.empty((0, 3))
        )
        temperature = fit_temperature(final_val_probs, y[tr_val])
        report['config']['temperature'] = float(temperature)
        external_ok = _evaluate_external(
            final_model, mu, sigma, args, cfg, conf, side, report,
            temperature, reference_name,
        )

        if args.dev_only:
            if args.out:
                dev_meta = {
                    'model': 'mlp-numpy',
                    'schema_version': 1,
                    'trained_at': utc_now(),
                    'status': 'dev_candidate',
                    'validation_status': 'dev_only',
                    'eligible_for_promotion': False,
                    'feature_names': list(NN_FEATURE_NAMES),
                    'label': report['labels'],
                    'config': report['config'],
                    'splits': report['splits'],
                    'external': report.get('external'),
                    'reference_gate': reference_ok,
                    'walk_forward_aggregate': report['walk_forward']['aggregate'],
                    'data': report['data'],
                    'code_version': report['code_version'],
                }
                write_json(Path(args.out), model_json(final_model, mu, sigma, dev_meta))
                report['candidate_path'] = str(args.out)
                print(f'[ok] candidato dev: {args.out}', flush=True)
            report['validation_status'] = 'dev_only'
            report['eligible_for_promotion'] = False
            report['status'] = 'completed'
            print('[ok] dev-only: holdout reservado (validación externa sí)', flush=True)
            return 0

        test_probs = apply_temperature(
            final_model.predict_proba(transform(X[test_idx], mu, sigma)),
            temperature,
        )
        test_pred = classification_metrics(y[test_idx], test_probs)
        test_trade = evaluate_trading(
            test_probs, close[test_idx], cfg.horizon, confidence=conf,
            fee_rate=args.fee_bps / 10000.0,
            slippage_rate=args.slippage_bps / 10000.0,
            side=side,
        )
        report['holdout'] = {
            'predictive': test_pred,
            'trading': test_trade,
            'fit': final_fit,
        }

        complete = (
            len(folds) >= 2
            and test_pred['samples'] >= max(50, cfg.horizon * 2)
            and test_trade['model']['trades'] >= args.min_test_trades
        )
        report['validation_status'] = 'complete' if complete else 'inconclusive'
        report['external_gate'] = external_ok
        report['eligible_for_promotion'] = bool(
            complete
            and test_trade.get('beats_baseline') is True
            and test_trade['model']['net_return'] > 0.0
            and reference_ok
            and external_ok
            and not guard.timed_out
            and not guard.oom_risk
        )
        if not complete:
            print(
                '[aviso] validación inconclusa: no promover sin revisar los datos/métricas',
                flush=True,
            )

        meta = {
            'model': 'mlp-numpy',
            'schema_version': 1,
            'trained_at': utc_now(),
            'status': 'candidate',
            'validation_status': report['validation_status'],
            'eligible_for_promotion': report['eligible_for_promotion'],
            'feature_names': list(NN_FEATURE_NAMES),
            'label': report['labels'],
            'config': report['config'],
            'splits': report['splits'],
            'holdout': report['holdout'],
            'external': report.get('external'),
            'reference_gate': reference_ok,
            'walk_forward_aggregate': report['walk_forward']['aggregate'],
            'data': report['data'],
            'code_version': report['code_version'],
            'metrics': {
                'holdout_accuracy': test_pred['accuracy'],
                'holdout_log_loss': test_pred['log_loss'],
                'holdout_net_return': test_trade['model']['net_return'],
                'holdout_max_drawdown': test_trade['model']['max_drawdown'],
                'holdout_trades': test_trade['model']['trades'],
                'baseline_net_return': test_trade['baseline_long']['net_return'],
                'beats_baseline': test_trade['beats_baseline'],
            },
            'resources': {
                'peak_ram_mb': float(guard.peak_ram_mb),
                'ram_measurement': 'psutil' if guard.available else 'unavailable',
                'duration_sec': float(time.monotonic() - started),
                'cpu_sec': float(guard.cpu_sec()),
                'cpu_pct': float(guard.cpu_sec() / max(1e-9, time.monotonic() - started) * 100),
                'max_ram_mb': cfg.max_ram_mb,
                'max_time_sec': cfg.max_time_sec,
            },
        }
        payload = model_json(final_model, mu, sigma, meta)

        if args.out:
            model_out = Path(args.out)
            write_json(model_out, payload)
        else:
            registry = ModelRegistry(models_dir=str(DEFAULT_CANDIDATE_DIR.parent))
            cand_dir = registry.save_candidate(payload, meta)
            model_out = Path(cand_dir) / 'model.json'

        report['candidate_path'] = str(model_out)
        report['metrics'] = meta['metrics']
        report['resources'] = meta['resources']
        report['status'] = 'completed'
        print(f'[ok] candidato: {model_out}', flush=True)
        print(
            f'[ok] holdout acc={test_pred["accuracy"]:.3f} '
            f'net={test_trade["model"]["net_return"]:.3f} '
            f'baseline={test_trade["baseline_long"]["net_return"]:.3f} '
            f'beats_baseline={test_trade["beats_baseline"]}',
            flush=True,
        )
        return 0
    except (TimeoutError, MemoryError) as exc:
        report['status'] = 'resource_limit'
        report['error'] = str(exc)
        print(f'[limit] {exc}', flush=True)
        return 3
    except Exception as exc:  # noqa: BLE001
        report['status'] = 'failed'
        report['error'] = str(exc)
        report['traceback'] = traceback.format_exc()
        print(f'[error] {exc}', flush=True)
        return 1
    finally:
        report['duration_sec'] = float(time.monotonic() - started)
        report['finished_at'] = utc_now()
        report.setdefault('resources', {})['peak_ram_mb'] = float(guard.peak_ram_mb)
        report.setdefault('resources', {})['ram_measurement'] = (
            'psutil' if guard.available else 'unavailable'
        )
        report.setdefault('resources', {})['cpu_sec'] = float(guard.cpu_sec())
        report.setdefault('resources', {})['cpu_pct'] = float(
            guard.cpu_sec() / max(1e-9, time.monotonic() - started) * 100
        )
        try:
            write_json(report_path, report)
            print(f'[ok] informe: {report_path}', flush=True)
        except Exception as exc:  # noqa: BLE001
            print(f'[error] no se pudo escribir el informe: {exc}', flush=True)


if __name__ == '__main__':
    sys.exit(main())