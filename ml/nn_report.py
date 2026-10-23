"""Resumen legible del informe de entrenamiento (job summary de Actions)."""

import argparse
import json


def fmt(value, digits=3):
    if isinstance(value, (int, float)):
        return f'{value:.{digits}f}'
    return str(value)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--report', required=True)
    parser.add_argument('--out', default='')
    args = parser.parse_args()

    with open(args.report, 'r', encoding='utf-8') as fh:
        report = json.load(fh)

    lines = [
        '# Entrenamiento NN (candidato)',
        '',
        f"- Estado: **{report.get('status')}**",
        f"- Validación: **{report.get('validation_status')}**",
        f"- Elegible para promoción: **{report.get('eligible_for_promotion')}**",
        f"- Símbolo/timeframe: {report.get('symbol')} / {report.get('timeframe')}",
        f"- Filas: {report.get('data', {}).get('output_rows')} "
        f"(hash {report.get('data', {}).get('dataset_hash', '')})",
        f"- Duración: {fmt(report.get('duration_sec'), 2)} s",
        f"- CPU: {fmt(report.get('resources', {}).get('cpu_sec'), 2)} s "
        f"({fmt(report.get('resources', {}).get('cpu_pct'), 1)}% de un núcleo)",
        f"- RAM pico: {fmt(report.get('resources', {}).get('peak_ram_mb'), 1)} MB "
        f"({report.get('resources', {}).get('ram_measurement', 'n/d')})",
        '',
        '## Walk-forward',
        '',
        f"- Folds: {report.get('walk_forward', {}).get('aggregate', {}).get('folds')}",
        f"- Trades: {report.get('walk_forward', {}).get('aggregate', {}).get('trades')}",
        f"- Retorno compuesto: "
        f"{fmt(report.get('walk_forward', {}).get('aggregate', {}).get('compounded_net_return'))}",
        '',
        '## Holdout final',
        '',
    ]
    metrics = report.get('metrics', {})
    lines += [
        f"- Accuracy: {fmt(metrics.get('holdout_accuracy'))}",
        f"- Log loss: {fmt(metrics.get('holdout_log_loss'))}",
        f"- Trades: {metrics.get('holdout_trades')}",
        f"- Retorno neto: {fmt(metrics.get('holdout_net_return'))}",
        f"- Baseline alcista: {fmt(metrics.get('baseline_net_return'))}",
        f"- Supera baseline: **{metrics.get('beats_baseline')}**",
        '',
    ]
    if report.get('error'):
        lines += ['## Error', '', str(report['error']), '']
    text = '\n'.join(lines)
    print(text)
    if args.out:
        with open(args.out, 'w', encoding='utf-8') as fh:
            fh.write(text + '\n')


if __name__ == '__main__':
    main()