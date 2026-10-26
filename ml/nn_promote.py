"""Promoción manual de un candidato neuronal (nunca automática).

Uso:
  python ml/nn_promote.py \
    --model ml/models/candidates/candidate.json \
    --report ml/models/candidates/report.json \
    --out ml-approved \
    --candidate-run-id 123456 \
    --reason "revisión manual"

El script se niega a promover si el informe no está completo, si la validación
fue inconclusa, si no supera la línea base alcista con costes o si hay pocos
trades fuera de muestra. No entrena ni modifica el candidato.
"""

import argparse
import hashlib
import json
import math
import os
import shutil
from datetime import datetime, timezone
from pathlib import Path

import numpy as np

HERE = Path(__file__).resolve().parent
import sys

if str(HERE) not in sys.path:
    sys.path.insert(0, str(HERE))

from features import NN_FEATURE_NAMES  # noqa: E402


def sha256(path):
    h = hashlib.sha256()
    with open(path, 'rb') as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b''):
            h.update(chunk)
    return h.hexdigest()


def load_json(path):
    with open(path, 'r', encoding='utf-8') as fh:
        return json.load(fh)


def finite_tree(value, path='root'):
    if isinstance(value, dict):
        for key, child in value.items():
            finite_tree(child, f'{path}.{key}')
    elif isinstance(value, list):
        for idx, child in enumerate(value):
            finite_tree(child, f'{path}[{idx}]')
    elif isinstance(value, (int, float)) and not isinstance(value, bool):
        if not math.isfinite(float(value)):
            raise ValueError(f'valor no finito en {path}')


def validate_candidate(model, report, min_trades):
    errors = []
    if report.get('status') != 'completed':
        errors.append(f"estado del entrenamiento: {report.get('status')!r}")
    if report.get('validation_status') != 'complete':
        errors.append(f"validación no concluyente: {report.get('validation_status')!r}")
    if report.get('eligible_for_promotion') is not True:
        errors.append('el informe no marca eligible_for_promotion=true')
    if report.get('reference_gate') is False:
        errors.append('el modelo no supera la regla de referencia')
    if report.get('external_gate') is False:
        errors.append('no supera la validación externa')
    metrics = report.get('metrics', {})
    beats = metrics.get('beats_baseline')
    if beats is None:
        beats = report.get('holdout', {}).get('trading', {}).get('beats_baseline')
    if beats is not True:
        errors.append('no supera la línea base alcista con costes en holdout')
    net = metrics.get('holdout_net_return')
    if net is None:
        net = report.get('holdout', {}).get('trading', {}).get('model', {}).get('net_return')
    try:
        net = float(net)
    except (TypeError, ValueError):
        net = None
    if net is None or not math.isfinite(net):
        errors.append('holdout_net_return ausente o inválido')
    elif net <= 0.0:
        errors.append('holdout_net_return no es positivo')
    trades = int(metrics.get('holdout_trades', 0) or 0)
    if trades < min_trades:
        errors.append(f'trades de holdout {trades} < mínimo {min_trades}')
    dataset_hash = str(report.get('data', {}).get('dataset_hash', '') or '')
    if len(dataset_hash) < 16:
        errors.append('dataset_hash ausente o inválido')

    if model.get('type') != 'mlp_numpy':
        errors.append(f"tipo de modelo inesperado: {model.get('type')!r}")
    if model.get('feature_names') != list(NN_FEATURE_NAMES):
        errors.append('feature_names no coinciden con ml/features.py')
    layers = model.get('layers') or []
    if not layers:
        errors.append('modelo sin capas')
    input_dim = int(model.get('input_dim', 0) or 0)
    prev_dim = input_dim
    for idx, layer in enumerate(layers):
        w = layer.get('w') or []
        b = layer.get('b') or []
        if not w or not b:
            errors.append(f'capa {idx} inválida (sin pesos o sesgos)')
            continue
        row_dim = len(w[0]) if isinstance(w[0], list) else 0
        if row_dim <= 0 or any(not isinstance(row, list) or len(row) != row_dim for row in w):
            errors.append(f'capa {idx} inválida (matriz w irregular)')
            continue
        if prev_dim and len(w) != prev_dim:
            errors.append(f'capa {idx}: filas {len(w)} != entrada esperada {prev_dim}')
        if np.asarray(b).size != row_dim:
            errors.append(f'capa {idx}: sesgos {np.asarray(b).size} != salida {row_dim}')
        prev_dim = row_dim
    if errors:
        raise SystemExit('PROMOCIÓN RECHAZADA:\n- ' + '\n- '.join(errors))


def main():
    parser = argparse.ArgumentParser(description='Promueve manualmente un candidato NN validado')
    parser.add_argument('--model', required=True)
    parser.add_argument('--report', required=True)
    parser.add_argument('--out', default='ml-approved')
    parser.add_argument('--candidate-run-id', default='')
    parser.add_argument('--reason', default='manual')
    parser.add_argument('--min-holdout-trades', type=int, default=10)
    args = parser.parse_args()
    if args.min_holdout_trades < 1:
        parser.error('--min-holdout-trades debe ser >= 1')

    model_path = Path(args.model)
    report_path = Path(args.report)
    model = load_json(model_path)
    report = load_json(report_path)
    finite_tree(model)
    validate_candidate(model, report, args.min_holdout_trades)

    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)
    approved_model = out_dir / 'neural_model.json'
    approved_meta = out_dir / 'neural_meta.json'
    approved_report = out_dir / 'training_report.json'

    meta = dict(model.get('meta') or {})
    if not meta:
        raise SystemExit('PROMOCIÓN RECHAZADA: el candidato no contiene metadatos embebidos')
    meta['status'] = 'approved'
    meta['promoted_at'] = datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
    meta['promotion_reason'] = args.reason
    meta['candidate_run_id'] = str(args.candidate_run_id)
    meta['dataset_hash'] = report['data']['dataset_hash']
    meta['validation_status'] = report['validation_status']
    meta['holdout_metrics'] = report.get('metrics', {})
    meta['resources'] = report.get('resources', {})
    meta['source_report_sha256'] = sha256(report_path)
    meta['approved_model_sha256'] = None

    model['meta'] = meta
    tmp = out_dir / '.neural_model.json.tmp'
    with open(tmp, 'w', encoding='utf-8') as fh:
        json.dump(model, fh, ensure_ascii=False, separators=(',', ':'))
    os.replace(tmp, approved_model)
    meta['approved_model_sha256'] = sha256(approved_model)

    with open(approved_meta, 'w', encoding='utf-8') as fh:
        json.dump(meta, fh, ensure_ascii=False, indent=2)
    shutil.copy2(report_path, approved_report)

    summary = {
        'status': 'approved',
        'promoted_at': meta['promoted_at'],
        'candidate_run_id': meta['candidate_run_id'],
        'reason': args.reason,
        'dataset_hash': meta['dataset_hash'],
        'model_sha256': meta['approved_model_sha256'],
        'report_sha256': meta['source_report_sha256'],
        'metrics': meta.get('holdout_metrics', {}),
    }
    with open(out_dir / 'promotion.json', 'w', encoding='utf-8') as fh:
        json.dump(summary, fh, ensure_ascii=False, indent=2)

    print(json.dumps(summary, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()