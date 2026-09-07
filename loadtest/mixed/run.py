#!/usr/bin/env python3
"""Own a disposable Linux target; drive it from the host and retain evidence."""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import re
import secrets
import shutil
import signal
import subprocess
import threading
import time
import urllib.request
import urllib.error
from datetime import datetime, timezone

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent


def command(args, **kwargs):
    result = subprocess.run(args, text=True, **kwargs)
    if result.returncode:
        raise RuntimeError(f'{args[0]} failed with exit code {result.returncode}; inspect the run logs')
    return result


def output(args):
    return subprocess.check_output(args, text=True).strip()


def request(url, data=None, token=None):
    headers = {'User-Agent': 'shinyhub-loadtest'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    if data is not None:
        headers['Content-Type'] = 'application/json'
        data = json.dumps(data).encode()
    with urllib.request.urlopen(urllib.request.Request(url, data=data, headers=headers), timeout=10) as response:
        return response.read()


def login(host, user, password):
    return json.loads(request(host + '/api/auth/login', {'username': user, 'password': password}))['token']


def parse_metrics(text):
    result = {}
    prefixes = ('process_', 'go_memstats_', 'go_goroutines', 'shinyhub_db_',
                'shinyhub_usage_persistence_', 'shinyhub_sessions_', 'shinyhub_app_state_transitions_')
    for line in text.splitlines():
        if line.startswith(prefixes):
            key, value, *_ = line.split()
            try:
                result[key] = float(value)
            except ValueError:
                pass
    return result


def resources(samples, start, end):
    rows = [row for row in samples if start <= row['time'] <= end and 'metrics' in row]
    if len(rows) < 2:
        return {'samples': len(rows)}
    first, last = rows[0], rows[-1]
    def delta(name):
        return max(0, last['metrics'].get(name, 0) - first['metrics'].get(name, 0))
    elapsed = last['time'] - first['time']
    return {'samples': len(rows), 'server_cpu_cores': delta('process_cpu_seconds_total') / elapsed,
            'server_rss_peak_mb': max(r['metrics'].get('process_resident_memory_bytes', 0) for r in rows) / 1e6,
            'db_wait_count': delta('shinyhub_db_wait_count_total'),
            'db_wait_seconds': delta('shinyhub_db_wait_duration_seconds_total'),
            'db_in_use_peak': max(r['metrics'].get('shinyhub_db_in_use_connections', 0) for r in rows),
            'goroutines_peak': max(r['metrics'].get('go_goroutines', 0) for r in rows),
            'generator_cpu_percent_peak': max((float(r['generator'].split()[0]) for r in rows if r.get('generator')), default=0)}



def container_resources(rows, start, end):
    cpu = []
    unavailable = 0
    for row in rows:
        if not start <= row['time'] <= end:
            continue
        try:
            value = float(row['stats']['CPUPerc'].rstrip('%'))
            if not math.isfinite(value) or value < 0:
                raise ValueError('invalid CPU sample')
        except (KeyError, ValueError, TypeError, AttributeError):
            unavailable += 1
            continue
        cpu.append(value)
    return {'container_samples': len(cpu), 'container_unavailable_samples': unavailable,
            'container_cpu_percent_peak': max(cpu, default=0)}


def verdict(summary):
    metrics = summary.get('metrics', {})
    required = ('page_ms', 'asset_ms', 'report_ms', 'session_establish_ms', 'session_rtt_ms', 'wake_ms')
    missing = [name for name in required if metrics.get(name, {}).get('values', {}).get('count', 0) == 0]
    failed = [name + ': ' + threshold for name, metric in metrics.items()
              for threshold, value in metric.get('thresholds', {}).items() if not value.get('ok', False)]
    if missing:
        return {'status': 'invalid', 'missing': missing, 'failed': failed}
    return {'status': 'saturated' if failed else 'pass', 'missing': [], 'failed': failed}


def summarize(stages, destination, metadata):
    lines = ['# Linux mixed-load results', '',
             'Synthetic gateway workload; these are not browser-rendering or Shiny-engine capacity measurements.', '',
             f"Target: {metadata['cpus']} CPU quota, {metadata['memory']}, Linux {metadata['arch']}, file-backed SQLite.",
             f"Retained sessions: {metadata['seed_sessions']}; stage duration: {metadata['seconds']} seconds.", '',
             '| Clients | Pages/s offered | Page p95 / p99 ms | Report p95 ms | WS RTT p99 ms | Verdict |',
             '|---:|---:|---:|---:|---:|---|']
    for stage in stages:
        data = json.loads(Path(stage['summary']).read_text())
        metrics = data['metrics']
        def value(name, stat):
            return metrics.get(name, {}).get('values', {}).get(stat, float('nan'))
        lines.append(f"| {stage['clients']} | {stage['clients']*2} | {value('page_ms','p(95)'):.1f} / {value('page_ms','p(99)'):.1f} | {value('report_ms','p(95)'):.1f} | {value('session_rtt_ms','p(99)'):.1f} | {stage['verdict']['status']} |")
    lines += ['', '| Clients | Completed pages | Dropped iterations | Reports / wakes sampled |',
              '|---:|---:|---:|---:|']
    for stage in stages:
        metrics = json.loads(Path(stage['summary']).read_text())['metrics']
        def count(name):
            return int(metrics.get(name, {}).get('values', {}).get('count', 0))
        lines.append(f"| {stage['clients']} | {count('page_ms')} | {count('dropped_iterations')} | {count('report_ms')} / {count('wake_ms')} |")
    lines += ['', '| Clients | Server CPU cores | Server peak RSS MB | Pool waits / seconds | Generator peak CPU % |',
              '|---:|---:|---:|---:|---:|']
    for stage in stages:
        r = stage.get('resources', {})
        lines.append(f"| {stage['clients']} | {r.get('server_cpu_cores',0):.2f} | {r.get('server_rss_peak_mb',0):.1f} | {r.get('db_wait_count',0):.0f} / {r.get('db_wait_seconds',0):.2f} | {r.get('generator_cpu_percent_peak',0):.1f} |")
    lines += ['', 'Threshold failures and raw evidence:']
    for stage in stages:
        lines.append(f"- {stage['clients']} clients: " + (', '.join(stage['verdict']['failed'] + stage['verdict']['missing']) or 'all thresholds passed'))
    passed = [s['clients'] for s in stages if s['verdict']['status'] == 'pass']
    failed = [s['clients'] for s in stages if s['verdict']['status'] == 'saturated']
    lines += ['', f'Largest tested passing stage: {max(passed) if passed else "none"}. First failing stage: {min(failed) if failed else "not reached"}.', '',
              'Interpret short runs as a saturation bracket, not a production sizing guarantee. Report and wake percentiles may have few samples; consult their counts. Metrics samples and container statistics include timestamps; CPU profiles cover 10 seconds of each stage. Connection-pool wait time excludes SQLite lock waits and query execution.', '',
              'The load generator runs on the host outside the target CPU quota. Generator CPU and RSS are sampled; dropped iterations invalidate a throughput claim even when successful-request latency looks healthy.']
    destination.write_text('\n'.join(lines) + '\n')


def run(args):
    for tool in ('docker', 'go', 'k6'):
        if not shutil.which(tool):
            raise RuntimeError(f'{tool} is required')
    arch = output(['docker', 'info', '--format', '{{.Architecture}}'])
    arch = {'aarch64': 'arm64', 'x86_64': 'amd64'}.get(arch, arch)
    if arch not in ('arm64', 'amd64'):
        raise RuntimeError(f'unsupported Linux architecture: {arch}')
    image = output(['docker', 'image', 'inspect', args.image, '--format', '{{.Id}}'])
    run_id = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ') + '-' + secrets.token_hex(3)
    result = ROOT / 'loadtest/results' / ('mixed-' + run_id)
    result.mkdir(mode=0o700, parents=True)
    build = ROOT / 'tmp/mixed-build'
    build.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, GOOS='linux', GOARCH=arch, CGO_ENABLED='0')
    for name, package in [('shinyhub', './cmd/shinyhub'), ('fixture', './loadtest/mixed/fixture'), ('seed', './loadtest/mixed/seed')]:
        command(['go', 'build', '-trimpath', '-ldflags=-s -w', '-o', str(build / name), package], cwd=ROOT, env=env)
    inputs = result / 'inputs'
    inputs.mkdir(mode=0o700)
    password = secrets.token_urlsafe(24)
    (inputs / 'password').write_text(password)
    (inputs / 'password').chmod(0o600)
    (inputs / 'server.yaml').write_text(f'''server:
  host: 0.0.0.0
  port: 8080
  shutdown_apps: stop
auth:
  secret: {secrets.token_hex(32)}
database:
  dsn: /state/hub.db
storage:
  apps_dir: /state/apps
  app_data_dir: /state/app-data
metrics:
  enabled: true
  addr: 127.0.0.1:9090
runtime:
  default_max_sessions_per_replica: 0
lifecycle:
  hibernate_timeout: 30m
usage:
  enabled: true
  identity_mode: unattributed
''')
    for slug, startup in [('mixed', '100ms'), ('wake', '750ms')]:
        bundle = inputs / slug
        bundle.mkdir()
        shutil.copy2(build / 'fixture', bundle / 'fixture')
        (bundle / 'shinyhub.toml').write_text(f'''[app]
command = ["./fixture", "-port", "{{port}}", "-startup", "{startup}"]
max_sessions_per_replica = 0
render_seconds = 0
''')
    container = 'shinyhub-mixed-' + run_id.lower()
    volume = container + '-state'
    active = None
    stop = threading.Event()
    stats_process = None
    profile = None
    observer = stats_thread = None
    stages = []
    metadata = {'commit': output(['git', 'rev-parse', 'HEAD']), 'dirty': bool(output(['git', 'status', '--porcelain'])),
                'arch': arch, 'image_id': image, 'cpus': args.cpus, 'memory': args.memory,
                'seed_sessions': args.sessions, 'seconds': args.seconds, 'steps': args.steps, 'report_interval': args.report_interval,
                'go': output(['go', 'version']), 'k6': output(['k6', 'version']), 'driver_platform': platform.platform(), 'driver_cpus': os.cpu_count(),
                'binary_sha256': {name: hashlib.sha256((build/name).read_bytes()).hexdigest() for name in ('shinyhub','fixture','seed')}}
    (result / 'metadata.json').write_text(json.dumps(metadata, indent=2))
    def cleanup():
        stop.set()
        if active and active.poll() is None:
            active.terminate()
            try: active.wait(timeout=10)
            except subprocess.TimeoutExpired: active.kill(); active.wait()
        if profile and profile.poll() is None:
            profile.terminate()
            try: profile.wait(timeout=5)
            except subprocess.TimeoutExpired: profile.kill(); profile.wait()
        if stats_process and stats_process.poll() is None:
            stats_process.terminate(); stats_process.wait(timeout=10)
        if observer: observer.join(timeout=12)
        if stats_thread: stats_thread.join(timeout=5)
        with (result/'container.log').open('w') as log:
            subprocess.run(['docker','logs',container],stdout=log,stderr=subprocess.STDOUT)
        subprocess.run(['docker', 'cp', container + ':/state/server.log', str(result / 'server.log')], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        subprocess.run(['docker', 'rm', '-f', container], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        subprocess.run(['docker', 'volume', 'rm', volume], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        shutil.rmtree(inputs)
        (result / 'auth.json').unlink(missing_ok=True)
    try:
        command(['docker', 'volume', 'create', volume], stdout=subprocess.DEVNULL)
        boot = 'cp /input/server.yaml /state/shinyhub.yaml; cp /input/password /state/password; /rig/shinyhub init --config /state/shinyhub.yaml --admin-user bench-admin --admin-password-file /state/password --output table; /rig/fixture -mode metrics & exec /rig/shinyhub serve --config /state/shinyhub.yaml --no-browser > /state/server.log 2>&1'
        command(['docker', 'run', '-d', '--name', container, '--init', '--cpus', str(args.cpus), '--memory', args.memory,
                 '--pids-limit', '2048', '--ulimit', 'nofile=65536:65536', '-p', '127.0.0.1::8080', '-p', '127.0.0.1::9091',
                 '-v', f'{build}:/rig:ro', '-v', f'{inputs}:/input:ro', '-v', f'{volume}:/state', '-w', '/state', image,
                 '/bin/sh', '-ec', boot], stdout=subprocess.DEVNULL)
        host = 'http://' + output(['docker', 'port', container, '8080/tcp'])
        metrics_url = 'http://' + output(['docker', 'port', container, '9091/tcp']) + '/metrics'
        deadline = time.monotonic() + 60
        while True:
            try: request(host + '/readyz'); break
            except Exception:
                if time.monotonic() > deadline: raise RuntimeError('server readiness timed out')
                time.sleep(0.5)
        admin = login(host, 'bench-admin', password)
        for slug in ('mixed', 'wake'):
            with (result / (slug + '-deploy.log')).open('w') as log:
                command(['docker', 'exec', '-e', 'SHINYHUB_HOST=http://127.0.0.1:8080', '-e', 'SHINYHUB_TOKEN',
                         container, '/rig/shinyhub', 'deploy', '/input/' + slug, '--slug', slug, '--visibility', 'shared', '--output', 'json'], stdout=log, stderr=subprocess.STDOUT, env=dict(os.environ, SHINYHUB_TOKEN=admin))
        command(['docker', 'exec', container, '/rig/seed', '-sessions', str(args.sessions)], stdout=subprocess.DEVNULL)
        viewer = login(host, 'bench-viewer', password)
        auth_file = result / 'auth.json'
        auth_file.write_text(json.dumps({'admin': admin, 'viewer': viewer}))
        auth_file.chmod(0o600)
        # Fail closed: anonymous traffic must not reach the shared fixture.
        try:
            anonymous = request(host + '/app/mixed/')
        except urllib.error.HTTPError as error:
            if error.code not in (401, 403): raise
            anonymous = b''
        if b'id="mixed-fixture"' in anonymous: raise RuntimeError('fixture unexpectedly allows anonymous access')
        stats_process = subprocess.Popen(['docker', 'stats', '--format', '{{json .}}', container], stdout=subprocess.PIPE, text=True)
        def container_stats():
            with (result / 'container.ndjson').open('w') as log:
                for line in stats_process.stdout:
                    line = re.sub(r'\x1b\[[0-9;]*[A-Za-z]', '', line).strip()
                    if not line: continue
                    try: row = {'time': time.time(), 'stats': json.loads(line)}
                    except json.JSONDecodeError: row = {'time': time.time(), 'error': 'invalid Docker stats line'}
                    log.write(json.dumps(row)+'\n'); log.flush()
        stats_thread = threading.Thread(target=container_stats, daemon=True); stats_thread.start()
        def observe():
            with (result / 'metrics.ndjson').open('w') as log:
                while not stop.is_set():
                    sample = {'time': time.time()}
                    try:
                        metrics = parse_metrics(request(metrics_url).decode())
                        required = ('process_cpu_seconds_total', 'process_resident_memory_bytes',
                                    'shinyhub_db_wait_count_total', 'shinyhub_db_wait_duration_seconds_total')
                        if not all(name in metrics for name in required):
                            raise RuntimeError('required server metrics are absent')
                        sample['metrics'] = metrics
                    except Exception as error: sample['error'] = str(error)
                    if active and active.poll() is None:
                        try: sample['generator'] = output(['ps', '-p', str(active.pid), '-o', '%cpu=,rss='])
                        except subprocess.CalledProcessError: pass
                    log.write(json.dumps(sample)+'\n'); log.flush(); stop.wait(2)
        observer = threading.Thread(target=observe, daemon=True); observer.start()
        for clients in args.steps:
            print(f'Running {clients} clients, {clients*2} page loads/s; evidence: {result}', flush=True)
            summary = result / f'{clients}-summary.json'
            before_sessions = json.loads(request(host+'/api/apps/mixed/usage?days=7', token=admin))['summary']['sessions']
            stage = {'clients': clients, 'start': time.time(), 'summary': str(summary), 'usage_sessions_before': before_sessions}
            kenv = dict(os.environ, K6_NO_USAGE_REPORT='true', LT_HOST=host, LT_AUTH_FILE=str(auth_file),
                        LT_CLIENTS=str(clients), LT_DURATION=f'{args.seconds}s', LT_WS_HOLD=str(args.seconds),
                        LT_REPORT_INTERVAL=str(args.report_interval), LT_SUMMARY=str(summary), LT_SEED_SESSIONS=str(args.sessions))
            with (result / f'{clients}-k6.log').open('w') as log:
                active = subprocess.Popen(['k6', 'run', str(HERE / 'mixed.js')], env=kenv, stdout=log, stderr=subprocess.STDOUT)
                # The helper only fetches from container loopback. pprof is not exposed to the host.
                profile = subprocess.Popen(['docker', 'exec', container, '/rig/fixture', '-mode', 'fetch', '-out', f'/state/{clients}-cpu.pprof'], stdout=subprocess.DEVNULL, stderr=log) if args.seconds >= 15 else None
                code = active.wait()
                if profile:
                    profile.wait(timeout=45)
                    command(['docker', 'cp', f'{container}:/state/{clients}-cpu.pprof', str(result / f'{clients}-cpu.pprof')], stdout=subprocess.DEVNULL)
            if code not in (0, 99) or not summary.exists(): raise RuntimeError(f'k6 failed: {code}; inspect {clients}-k6.log')
            summary_data = json.loads(summary.read_text())
            stage.update(end=time.time(), verdict=verdict(summary_data))
            connected = summary_data['metrics'].get('session_established', {}).get('values', {}).get('count', 0)
            deadline = time.monotonic()+15
            while True:
                actual = json.loads(request(host+'/api/apps/mixed/usage?days=7', token=admin))['summary']['sessions']
                if actual >= before_sessions+connected or time.monotonic() >= deadline: break
                time.sleep(1)
            stage['usage_sessions_after'] = actual
            if actual < before_sessions+connected:
                stage['verdict']['status'] = 'invalid'
                stage['verdict']['missing'].append('durable WebSocket usage sessions')
            rows = [json.loads(line) for line in (result/'metrics.ndjson').read_text().splitlines() if line]
            stage['resources'] = resources(rows, stage['start'], stage['end'])
            container_rows = [json.loads(line) for line in (result/'container.ndjson').read_text().splitlines() if line]
            stage['resources'].update(container_resources(container_rows, stage['start'], stage['end']))
            if stage['resources']['samples'] < 2 or not stage['resources']['container_samples']:
                stage['verdict']['status'] = 'invalid'
                stage['verdict']['missing'].append('resource observations')
            stages.append(stage)
            (result / 'stages.json').write_text(json.dumps(stages, indent=2))
            summarize(stages, result / 'REPORT.md', metadata)
            print(json.dumps(stage['verdict']), flush=True)
            if stage['verdict']['status'] == 'invalid': raise RuntimeError('missing required observations')
            if len(stages) == 1 and stage['verdict']['status'] != 'pass' and clients == 1:
                raise RuntimeError('single-client negative control failed; refusing higher load')
            time.sleep(3)
        print(result / 'REPORT.md', flush=True)
    finally:
        cleanup()
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--steps', default='1,10,50,100,200,400')
    parser.add_argument('--seconds', type=int, default=30)
    parser.add_argument('--sessions', type=int, default=100000)
    parser.add_argument('--report-interval', type=int, default=5)
    parser.add_argument('--cpus', type=float, default=2)
    parser.add_argument('--memory', default='2g')
    parser.add_argument('--image', default='debian:bookworm-slim', help='must already exist locally; resolved to immutable image ID')
    args = parser.parse_args()
    args.steps = [int(n) for n in args.steps.split(',')]
    if not args.steps or any(n < 1 or n > 2000 for n in args.steps) or args.seconds < 5 or args.seconds > 600 or args.report_interval < 1 or args.cpus <= 0 or not 0 <= args.sessions <= 1000000 or len(set(args.steps)) != len(args.steps):
        parser.error('steps must be 1..2000, seconds 5..600, interval/CPU quota positive, sessions 0..1000000, and steps unique')
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    return run(args)

if __name__ == '__main__':
    raise SystemExit(main())
