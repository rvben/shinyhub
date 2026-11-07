import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { once } from 'node:events';
import { createWriteStream } from 'node:fs';
import { createServer as createHTTPServer } from 'node:http';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';

const root = fileURLToPath(new URL('../../', import.meta.url));
export const items = body => Array.isArray(body) ? body : body.items;
export const recoverySource = '0.22.0';
const recoveryCommit = '4dcaf6359bb6320431f7cb99ba2c6d2cf4e7f729';

// A fresh hub, private credentials and an ephemeral port for every invocation.
// There is intentionally no existing-host or database override.
export async function createFixture({ recovery = false } = {}) {
  const results = resolve(root, 'loadtest/results');
  await mkdir(results, { recursive: true });
  const output = await mkdtemp(join(results, 'acceptance-'));
  const state = await mkdtemp('/tmp/shinyhub-acceptance-');
  const abort = new AbortController();
  const report = { status: 'failed', checks: [], layouts: [], accessibility: [] };
  const children = new Set();
  let server, collector;
  const onSignal = () => abort.abort(new Error('acceptance run interrupted'));
  const deadline = setTimeout(() => abort.abort(new Error('acceptance run exceeded ten minutes')), 600_000);
  process.once('SIGINT', onSignal);
  process.once('SIGTERM', onSignal);
  const env = {
    ...Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('SHINYHUB_'))),
    GOWORK: 'off', XDG_CONFIG_HOME: join(state, 'config'),
    UV_PYTHON_DOWNLOADS: 'never', UV_PYTHON_PREFERENCE: 'only-system',
  };
  const username = 'acceptance-admin', password = randomBytes(24).toString('hex');
  const accounts = { admin: { username, password } };
  let host, token, python;
  const override = !recovery && process.env.SHINYHUB_E2E_BINARY;
  const binary = override ? resolve(override) : join(state, 'shinyhub');
  const source = recovery ? join(state, 'shinyhub-previous') : binary;
  const storage = join(state, 'storage');
  const config = join(state, 'hub.yaml');

  async function command(args, label, cwd = root) {
    const log = createWriteStream(join(output, label + '.log'), { mode: 0o600 });
    const child = spawn(args[0], args.slice(1), { cwd, env, signal: abort.signal });
    children.add(child);
    let stdout = '';
    child.stdout.on('data', chunk => { stdout += chunk; });
    child.stdout.pipe(log, { end: false });
    child.stderr.pipe(log, { end: false });
    try {
      const [code] = await once(child, 'close');
      assert.equal(code, 0, `${label} failed; see ${label}.log`);
      return stdout.trim();
    } finally { children.delete(child); log.end(); }
  }

  async function poll(fn, label, timeout = 30_000) {
    const end = Date.now() + timeout;
    while (Date.now() < end) {
      abort.signal.throwIfAborted();
      if (server && (server.exitCode !== null || server.signalCode !== null)) throw new Error('isolated hub exited');
      if (await fn()) return;
      await delay(100, undefined, { signal: abort.signal });
    }
    throw new Error(`timed out: ${label}`);
  }

  async function check(name, fn) {
    console.log(`Acceptance: ${name}`);
    const start = performance.now();
    const result = { name, status: 'failed' };
    report.checks.push(result);
    try { await fn(); result.status = 'passed'; }
    finally { result.seconds = +((performance.now() - start) / 1000).toFixed(3); }
  }

  async function response(path, { method = 'GET', body, auth = token, headers = {}, raw = false } = {}) {
    return fetch(host + path, {
      method, headers: { 'User-Agent': 'shinyhub-acceptance-test',
        ...(auth ? { Authorization: `Bearer ${auth}` } : {}),
        ...(body !== undefined && !raw ? { 'Content-Type': 'application/json' } : {}), ...headers },
      body: body === undefined ? undefined : raw ? body : JSON.stringify(body),
      signal: AbortSignal.any([abort.signal, AbortSignal.timeout(120_000)]),
    });
  }

  async function json(path, options, status = 200) {
    const res = await response(path, options);
    // Do not include response bodies: auth/token endpoints return credentials.
    assert.equal(res.status, status, `${options?.method || 'GET'} ${path}`);
    if (res.status === 204) return undefined;
    return res.headers.get('content-type')?.includes('application/json') ? res.json() : res.text();
  }

  async function login(role) {
    const session = await json('/api/auth/login', { method: 'POST', auth: null, body: accounts[role] });
    accounts[role].token = session.token;
    accounts[role].user = session.user;
    return session.token;
  }

  async function stop() {
    if (server?.pid) {
      const exited = once(server, 'close').catch(() => {});
      if (server.exitCode === null && server.signalCode === null) {
        try { process.kill(-server.pid, 'SIGTERM'); } catch { /* already stopped */ }
        await Promise.race([exited, delay(15_000, undefined, { ref: false })]);
      }
      try { process.kill(-server.pid, 'SIGKILL'); } catch { /* group exited */ }
      if (server.exitCode === null && server.signalCode === null) await exited;
    }
    server = undefined;
  }

  async function start(executable, label, readinessTimeout) {
    const log = createWriteStream(join(output, label + '.log'), { mode: 0o600 });
    server = spawn(executable, ['serve', '--config', config, '--no-browser'], { cwd: state, env, detached: true });
    server.stdout.pipe(log, { end: false });
    server.stderr.pipe(log, { end: false });
    let spawnError;
    server.on('error', error => { spawnError = error; });
    server.once('close', () => log.end());
    await poll(async () => {
      if (spawnError) throw spawnError;
      return fetch(host + '/activez', { headers: { 'User-Agent': 'shinyhub-acceptance-test' }, signal: AbortSignal.timeout(1000) }).then(r => r.ok).catch(() => false);
    }, label + ' readiness', readinessTimeout);
    token = await login('admin');
  }

  async function restoreBackup() {
    assert.ok(recovery && server, 'restore requires the isolated recovery fixture');
    const archive = join(state, 'backup.tar.gz');
    await command([source, 'backup', '--config', config, '--out', archive], 'backup');
    await stop();
    // Restore at the original absolute paths, as required by the deployment
    // records, but remove all source state so a partial restore cannot pass.
    await rm(storage, { recursive: true });
    await mkdir(storage, { mode: 0o700 });
    await command([binary, 'restore', archive, '--config', config], 'restore');
    // A live backup can carry the old server's 30-second ownership lease.
    await start(binary, 'restored-server', 60_000);
  }

  async function close() {
    clearTimeout(deadline);
    process.removeListener('SIGINT', onSignal);
    process.removeListener('SIGTERM', onSignal);
    for (const child of children) child.kill('SIGKILL');
    await stop();
    if (collector) {
      collector.closeAllConnections();
      await new Promise(done => collector.close(done));
    }
    await rm(state, { recursive: true, force: true });
    await writeFile(join(output, 'result.json'), JSON.stringify(report, null, 2) + '\n', { mode: 0o600 });
    console.log(`Acceptance diagnostics: ${output}`);
  }

  const fixture = { root, state, output, accounts, report, command, poll, check, response, json, login, close, signal: abort.signal };
  try {
    python = await command(['uv', 'python', 'find', '--python-preference', 'managed',
      process.env.SHINYHUB_E2E_PYTHON || process.env.PYTHON_VERSION || '3.14+gil'], 'python');
    env.UV_PYTHON = python;
    if (!override) await command(['go', 'build', '-o', binary, './cmd/shinyhub'], 'build');
    if (recovery) {
      const tag = 'refs/tags/v' + recoverySource;
      const commit = await command(['git', 'rev-parse', '--verify', tag + '^{commit}'], 'recovery-source-tag')
        .catch(error => { throw new Error(`Fetch the recovery baseline: git fetch --no-tags origin ${tag}:${tag}`, { cause: error }); });
      assert.equal(commit, recoveryCommit, 'recovery baseline tag does not match the pinned release');
      const tree = join(state, 'previous-source'), archive = join(state, 'previous-source.tar');
      await mkdir(tree);
      await command(['git', 'archive', '--format=tar', '--output=' + archive, commit], 'recovery-source-archive');
      await command(['tar', '-xf', archive, '-C', tree], 'recovery-source-extract');
      await command(['go', 'build', '-ldflags', '-X main.version=' + recoverySource, '-o', source, './cmd/shinyhub'], 'recovery-source-build', tree);
    }
    const reservation = createServer();
    reservation.listen(0, '127.0.0.1');
    await once(reservation, 'listening');
    const port = reservation.address().port;
    await new Promise(done => reservation.close(done));
    host = `http://127.0.0.1:${port}`;
    // Disposable OTLP receiver: tracing stays entirely on loopback while the
    // real proxy records the app's spans in its normal trace buffer.
    collector = createHTTPServer((req, res) => {
      req.resume();
      res.writeHead(200, { 'Content-Type': 'application/x-protobuf' });
      res.end();
    });
    collector.listen(0, '127.0.0.1');
    await once(collector, 'listening');
    const passwordFile = join(state, 'password');
    await mkdir(storage, { mode: 0o700 });
    await writeFile(passwordFile, password, { mode: 0o600 });
    await writeFile(config, `server:\n  host: 127.0.0.1\n  port: ${port}\n  base_url: ${host}\n  shutdown_apps: stop\n  drain_timeout: 2s\n  update_capacity:\n    worker_cpu_request_cores: 0.1\n    reserved_cpu_cores: 0.1\nauth:\n  secret: ${randomBytes(32).toString('hex')}\ndatabase:\n  dsn: ${JSON.stringify(join(storage, 'hub.db'))}\nstorage:\n  apps_dir: ${JSON.stringify(join(storage, 'apps'))}\n  app_data_dir: ${JSON.stringify(join(storage, 'app-data'))}\ntracing:\n  enabled: true\n  otlp_endpoint: http://127.0.0.1:${collector.address().port}\n  sample_ratio: 1\n  slow_request_ms: 1\nmetrics:\n  history_window: 15m\n  history_interval: 1s\n`, { mode: 0o600 });
    await command([source, 'init', '--config', config, '--admin-user', username, '--admin-password-file', passwordFile], 'init');
    await rm(passwordFile);
    await start(source, 'server');
    const bundle = join(state, 'bundle.zip'), invalid = join(state, 'invalid.zip');
    await writeFile(invalid, 'not a ZIP');
    await command([python, '-c', 'import sys,zipfile\nfrom pathlib import Path\nwith zipfile.ZipFile(sys.argv[1],"w") as z:\n for p in Path(sys.argv[2]).iterdir():\n  z.write(p,p.name)', bundle, join(root, 'scripts/acceptance/fixtures')], 'bundle');
    const upload = async slug => {
      const form = new FormData();
      form.append('bundle', new Blob([await readFile(bundle)], { type: 'application/zip' }), 'bundle.zip');
      const end = Date.now() + 30_000;
      for (let attempt = 1; ; attempt++) {
        const res = await response(`/api/apps/${slug}/deploy`, { method: 'POST', body: form, raw: true });
        const body = await res.json();
        // Other jobs on a shared host can briefly consume CPU. Retry only the
        // server's explicit transient CPU refusal, never an arbitrary failure.
        if (res.status === 409 && body.failure_kind === 'capacity' && body.capacity_retryable === true
          && body.resource === 'cpu' && attempt < 4 && Date.now() < end) {
          await delay(Math.min(10_000, end - Date.now()), undefined, { signal: abort.signal });
          continue;
        }
        assert.equal(res.status, 200, `deploy ${slug}: ${body.error || res.status}`);
        return body;
      }
    };
    return Object.assign(fixture, { host, bundle, invalid, upload, python, ...(recovery && { restoreBackup }) });
  } catch (error) { report.error = error.message; await close(); throw error; }
}
