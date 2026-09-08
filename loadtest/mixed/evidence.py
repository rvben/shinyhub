"""Resource evidence and repeated-run comparisons; never average percentiles."""
import json
from pathlib import Path


def target_resources(samples, start, end):
    rows = [r for r in samples if start <= r['time'] <= end and 'target' in r]
    result = {'target_samples': len(rows)}
    if len(rows) < 2:
        return result
    first, last = rows[0], rows[-1]
    elapsed = last['time'] - first['time']
    try:
        cpu = {key: last['target']['cpu_stat'][key] - first['target']['cpu_stat'][key]
               for key in ('usage_usec', 'nr_periods', 'nr_throttled', 'throttled_usec')}
        ticks = [b-a for a, b in zip(first['target']['host_ticks'], last['target']['host_ticks'])]
        if elapsed <= 0 or len(ticks) != 8 or min(ticks + list(cpu.values())) < 0 or sum(ticks) <= 0:
            raise ValueError('resource counters reset or did not advance')
        result.update(target_cpu_cores=cpu['usage_usec']/1e6/elapsed,
                      target_throttled_seconds=cpu['throttled_usec']/1e6,
                      target_cpu_periods=cpu['nr_periods'], target_throttled_periods=cpu['nr_throttled'],
                      target_throttled_period_fraction=cpu['nr_throttled']/cpu['nr_periods'] if cpu['nr_periods'] else 0,
                      target_kernel_busy_fraction=1-(ticks[3]+ticks[4])/sum(ticks),
                      target_kernel_steal_fraction=ticks[7]/sum(ticks),
                      target_kernel_load_peak=max(r['target']['host_load'][0] for r in rows),
                      driver_load_peak=max(r['driver_load_average'][0] for r in rows))
    except (KeyError, ValueError, TypeError, ZeroDivisionError) as error:
        result['target_observation_error'] = str(error)
    return result


def quiet_reasons(samples, driver_cpus, max_load=0.5, max_busy=0.2):
    if len(samples) < 2:
        return ['insufficient quiet-host observations']
    data = target_resources(samples, samples[0]['time'], samples[-1]['time'])
    if 'target_kernel_busy_fraction' not in data:
        return ['incomplete quiet-host observations']
    reasons = []
    if data['driver_load_peak']/driver_cpus > max_load:
        reasons.append('generator host load exceeds quiet limit')
    if max(r['target']['host_load'][0]/r['target']['host_cpus'] for r in samples) > max_load:
        reasons.append('target kernel load exceeds quiet limit')
    if data['target_kernel_busy_fraction'] > max_busy:
        reasons.append('target kernel CPU activity exceeds quiet limit')
    return reasons


def compare_runs(paths, expected):
    runs = []
    for path in paths:
        root = Path(path)
        runs.append((json.loads((root/'metadata.json').read_text()), json.loads((root/'stages.json').read_text())))
    fields = ('arch', 'image_id', 'cpus', 'memory', 'seed_sessions', 'seconds', 'steps',
              'transport', 'target_host', 'source_commit', 'report_interval', 'k6', 'binary_sha256', 'driver_cpus', 'driver_platform', 'go', 'require_quiet')
    compatible = bool(runs) and all(all(k in meta and meta[k] == runs[0][0].get(k) for k in fields) for meta, _ in runs)
    # Older evidence used a single session lasting the whole stage. Never pool
    # it with reconnecting sessions or a different lifecycle/workload mix.
    for key, default in (('lifecycle_interval', 0), ('workload_sha256', None), ('harness_sha256', None)):
        compatible = compatible and all(meta.get(key, default) == runs[0][0].get(key, default) for meta, _ in runs)
    compatible = compatible and all(meta.get('ws_hold', meta['seconds']) == runs[0][0].get('ws_hold', runs[0][0]['seconds']) for meta, _ in runs)
    result = {'expected_runs': expected, 'completed_runs': len(runs), 'compatible': compatible, 'stages': []}
    clients = runs[0][0]['steps'] if runs else []
    for client in clients:
        stages = [next((s for s in stages if s['clients'] == client), None) for _, stages in runs]
        statuses = [s['verdict']['status'] if s else 'missing' for s in stages]
        if not compatible or len(runs) != expected or any(s in ('invalid', 'missing') for s in statuses):
            consistency = 'insufficient evidence'
        elif len(set(statuses)) == 1:
            consistency = 'consistent pass' if statuses[0] == 'pass' else 'consistent saturation'
        else:
            consistency = 'variable'
        row = {'clients': client, 'verdicts': statuses, 'consistency': consistency, 'page_p95_ms': [], 'report_p95_ms': []}
        for stage in stages:
            if stage:
                metrics = json.loads(Path(stage['summary']).read_text())['metrics']
                row['page_p95_ms'].append(metrics['page_ms']['values']['p(95)'])
                row['report_p95_ms'].append(metrics['report_ms']['values']['p(95)'])
        result['stages'].append(row)
    return result


def write_comparison(paths, expected, destination):
    data = compare_runs(paths, expected)
    destination = Path(destination)
    (destination/'comparison.json').write_text(json.dumps(data, indent=2))
    lines = ['# Repeated mixed-load comparison', '',
             f"Completed {data['completed_runs']} of {expected} runs. Matching workload and binaries: {data['compatible']}.", '',
             '| Clients | Verdicts | Page p95 ms per run | Report p95 ms per run | Consistency |',
             '|---:|---|---|---|---|']
    for row in data['stages']:
        pages = ', '.join(f'{v:.1f}' for v in row['page_p95_ms'])
        reports = ', '.join(f'{v:.1f}' for v in row['report_p95_ms'])
        lines.append(f"| {row['clients']} | {', '.join(row['verdicts'])} | {pages} | {reports} | {row['consistency']} |")
    lines += ['', 'Percentiles are shown separately, not averaged or pooled. A consistent pass is an observation under the recorded conditions, not a production capacity guarantee. Local Docker shares physical hardware with the generator. SSH includes network and tunnel overhead; physical hardware separation must be verified independently. Target-kernel metrics cannot reveal a hypervisor’s other workloads.', '', 'Evidence directories:']
    lines += [f'- {p}' for p in paths]
    (destination/'REPORT.md').write_text('\n'.join(lines)+'\n')
