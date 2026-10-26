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
    ]
    wf_ref = report.get('walk_forward', {}).get('reference')
    if wf_ref:
        lines += [
            f"- Referencia: **{wf_ref.get('name')}** "
            f"(modelo {fmt(report.get('walk_forward', {}).get('aggregate', {}).get('compounded_net_return'))} "
            f"vs {fmt(wf_ref.get('summary', {}).get('compounded_net_return'))})",
            f"- Supera referencia: **{wf_ref.get('model_beats')}**",
            '',
        ]
    wf_baselines = report.get('walk_forward', {}).get('baselines', {})
    if wf_baselines:
        lines += [
            '## Baselines walk-forward',
            '',
            '| Estrategia | Trades | Retorno | Peor DD |',
            '|---|---:|---:|---:|',
        ]
        for name, b in wf_baselines.items():
            lines.append(
                f"| {name} | {b.get('trades')} | {fmt(b.get('compounded_net_return'))} | "
                f"{fmt(b.get('worst_fold_drawdown'))} |"
            )
        lines.append('')
    cal = report.get('calibration')
    if cal:
        sel = cal.get('selected', {})
        lines += [
            '## Calibración walk-forward',
            '',
            f"- Estado: **{cal.get('status')}** "
            f"(mín. {cal.get('min_trades')} trades, "
            f"pérdida/fold <= {fmt(cal.get('max_fold_loss'), 2)})",
            f"- Candidatos elegibles: {cal.get('eligible_candidates')}",
            f"- Side/confianza: **{sel.get('side')}** / **{fmt(sel.get('confidence'), 2)}**",
            f"- Trades: {sel.get('trades')}",
            f"- Retorno compuesto: {fmt(sel.get('compounded_net_return'))}",
            f"- Peor drawdown de fold: {fmt(sel.get('worst_fold_drawdown'))}",
            '',
        ]
    metrics = report.get('metrics', {})
    if report.get('holdout'):
        lines += [
            '## Holdout final',
            '',
            f"- Accuracy: {fmt(metrics.get('holdout_accuracy'))}",
            f"- Log loss: {fmt(metrics.get('holdout_log_loss'))}",
            f"- Trades: {metrics.get('holdout_trades')}",
            f"- Retorno neto: {fmt(metrics.get('holdout_net_return'))}",
            f"- Baseline alcista: {fmt(metrics.get('baseline_net_return'))}",
            f"- Supera baseline: **{metrics.get('beats_baseline')}**",
            '',
        ]
    else:
        lines += ['## Holdout final', '', '- No evaluado (dev-only / selección).', '']
    ext = report.get('external')
    if ext:
        lines += ['## Validación externa', '']
        if ext.get('status') == 'failed':
            lines += [f"- Falló: {ext.get('error')}", '']
        else:
            et = ext.get('trading', {})
            em = et.get('model', {})
            ebl = et.get('baseline_long', {})
            ep = ext.get('predictive', {})
            lines += [
                f"- Símbolo/timeframe: {ext.get('symbol')} {ext.get('timeframe')}",
                f"- Accuracy: {fmt(ep.get('accuracy'))}",
                f"- Trades: {em.get('trades')}",
                f"- Retorno neto: {fmt(em.get('net_return'))}",
                f"- Baseline: {fmt(ebl.get('net_return'))}",
                f"- Pasa puerta: **{ext.get('passes_gate')}**",
                '',
            ]
            ext_ref = ext.get('reference')
            if ext_ref:
                lines += [
                    f"- Referencia: **{ext_ref.get('name')}** "
                    f"(modelo {fmt(em.get('net_return'))} vs "
                    f"{fmt(ext_ref.get('summary', {}).get('net_return'))})",
                    f"- Supera referencia: **{ext_ref.get('model_beats')}**",
                    '',
                ]
            ext_baselines = ext.get('baselines', {})
            if ext_baselines:
                lines += [
                    '| Estrategia | Trades | Retorno | DD |',
                    '|---|---:|---:|---:|',
                ]
                for name, b in ext_baselines.items():
                    lines.append(
                        f"| {name} | {b.get('trades')} | {fmt(b.get('net_return'))} | "
                        f"{fmt(b.get('max_drawdown'))} |"
                    )
                lines.append('')
    if report.get('error'):
        lines += ['## Error', '', str(report['error']), '']
    text = '\n'.join(lines)
    print(text)
    if args.out:
        with open(args.out, 'w', encoding='utf-8') as fh:
            fh.write(text + '\n')


if __name__ == '__main__':
    main()