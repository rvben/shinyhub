#!/usr/bin/env node
// A real, isolated Shiny server and extension-free browser. No existing server,
// browser profile, credentials, or checked-in app files are modified.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { once } from 'node:events';
import { createWriteStream } from 'node:fs';
import { cp, mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { createRequire } from 'node:module';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const require = createRequire(new URL('../loadtest/render/driver/package.json', import.meta.url));
const { chromium } = require('playwright');
const results = join(root, 'loadtest/results');
await mkdir(results, { recursive: true });
const work = await mkdtemp(join(results, 'browser-lifecycle-'));
const state = join(work, 'state');
await mkdir(state, { mode: 0o700 });
const binary = process.env.SHINYHUB_E2E_BINARY ? resolve(process.env.SHINYHUB_E2E_BINARY) : join(work, 'shinyhub');
const config = join(state, 'shinyhub.yaml');
const clientConfig = join(state, 'client.json');
const passwordFile = join(state, 'password');
const password = randomBytes(24).toString('hex');
const username = 'lifecycle-admin';
const viewer = 'lifecycle-viewer';
const viewerPassword = randomBytes(24).toString('hex');
const sweepSeconds = 1;
const abort = new AbortController();
const deadline = setTimeout(() => abort.abort(new Error('lifecycle check exceeded ten minutes')), 600_000);
const stop = () => abort.abort(new Error('lifecycle check interrupted'));
process.once('SIGINT', stop);
process.once('SIGTERM', stop);
let server, browser, context;
const children = new Set();
const sockets = new WeakMap();
const report = { checks: [], status: 'failed', shiny: '1.6.3', readiness: [], browserErrors: [] };
// Operator overrides must never redirect this test to an existing database/server.
const inherited = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('SHINYHUB_')));
const env = { ...inherited, XDG_CONFIG_HOME: join(state, 'config'), GOWORK: 'off', UV_PYTHON_DOWNLOADS: 'never', UV_PYTHON_PREFERENCE: 'only-system' };

async function command(args, name, extra = {}) {
  const log = createWriteStream(join(work, `${name}.log`), { mode: 0o600 });
  const child = spawn(args[0], args.slice(1), { cwd: root, env, signal: abort.signal, ...extra });
  children.add(child);
  child.stdout.pipe(log, { end: false });
  child.stderr.pipe(log, { end: false });
  try {
    const [code] = await once(child, 'exit');
    assert.equal(code, 0, `${name} failed; inspect ${name}.log`);
  } finally { children.delete(child); log.end(); }
}

async function check(name, fn) {
  console.log(`==> ${name}`);
  const started = performance.now();
  await fn();
  report.checks.push({ name, seconds: +( (performance.now() - started) / 1000).toFixed(3) });
}

async function poll(fn, description, timeout = 30_000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    abort.signal.throwIfAborted();
    if (server && (server.exitCode !== null || server.signalCode !== null)) throw new Error('isolated server exited');
    if (await fn()) return;
    await delay(100, undefined, { signal: abort.signal });
  }
  throw new Error(`timed out: ${description}`);
}

async function output(page, version, calculation, samples = 1000, session) {
  const expected = [`Version: ${version}`, `Calculation: ${calculation}`, `Samples: ${samples}`,
    `Sum: ${samples * (samples + 1) / 2}`, `Mean: ${((samples + 1) / 2).toFixed(1)}`];
  await poll(async () => {
    const text = await page.locator('#result').textContent({ timeout: 1000 }).catch(() => '');
    return expected.every(line => text.split('\n').includes(line));
  }, 'reactive output');
  const text = await page.locator('#result').textContent();
  const label = /Session: ([a-f0-9]{8})/.exec(text)?.[1];
  assert.ok(label, 'visible session label');
  if (session) assert.equal(label, session, 'session continuity');
  return label;
}

async function exportResult(page, status, count) {
  await poll(async () => (await page.locator('#export_result').textContent({ timeout: 1000 }).catch(() => ''))
    === `Result: ${status}\nExports: ${count}`, `export ${status}, count ${count}`);
}

async function recoverSession(page, context) {
  const link = page.getByRole('link', { name: 'Start a new app session in a new tab', exact: true });
  await link.waitFor();
  const opened = context.waitForEvent('page');
  await link.click();
  const next = await opened;
  await next.waitForLoadState('domcontentloaded');
  return next;
}

async function assertDisconnected(page) {
  const connections = sockets.get(page);
  assert.ok(connections?.length, 'the app opened a real WebSocket');
  await poll(() => connections.every(socket => socket.isClosed()), 'entitlement sweep closes all old sockets', 10_000);
  await page.getByRole('link', { name: 'Start a new app session in a new tab', exact: true }).waitFor();
}

try {
  if (!process.env.SHINYHUB_E2E_BINARY) await command(['go', 'build', '-o', binary, './cmd/shinyhub'], 'build');
  report.binary_sha256 = createHash('sha256').update(await readFile(binary)).digest('hex');
  // Reserve an ephemeral loopback port; process readiness below detects a bind race.
  const reservation = createServer();
  reservation.listen(0, '127.0.0.1');
  await once(reservation, 'listening');
  const port = reservation.address().port;
  await new Promise(resolve => reservation.close(resolve));
  const host = `http://127.0.0.1:${port}`;
  await writeFile(passwordFile, password, { mode: 0o600 });
  await writeFile(config, `server:\n  host: 127.0.0.1\n  port: ${port}\n  shutdown_apps: stop\n  session_recheck_interval: ${sweepSeconds}s\nauth:\n  secret: ${randomBytes(32).toString('hex')}\ndatabase:\n  dsn: ${JSON.stringify(join(state, 'hub.db'))}\nstorage:\n  apps_dir: ${JSON.stringify(join(state, 'apps'))}\n  app_data_dir: ${JSON.stringify(join(state, 'app-data'))}\nlifecycle:\n  hibernate_timeout: 30m\n`, { mode: 0o600 });
  await command([binary, 'init', '--config', config, '--admin-user', username, '--admin-password-file', passwordFile], 'init', { cwd: state });
  const log = createWriteStream(join(work, 'server.log'), { mode: 0o600 });
  server = spawn(binary, ['serve', '--config', config, '--no-browser'], { cwd: state, env, detached: true });
  server.stdout.pipe(log, { end: false });
  server.stderr.pipe(log, { end: false });
  server.once('close', () => log.end());
  let serverError;
  server.on('error', error => { serverError = error; });
  await poll(async () => {
    if (serverError) throw serverError;
    return fetch(host + '/activez', { signal: AbortSignal.timeout(2000), headers: { 'User-Agent': 'shinyhub-lifecycle-test' } }).then(r => r.ok).catch(() => false);
  }, 'server readiness');
  const login = await fetch(host + '/api/auth/login', { method: 'POST', signal: abort.signal,
    headers: { 'Content-Type': 'application/json', 'User-Agent': 'shinyhub-lifecycle-test' },
    body: JSON.stringify({ username, password }) });
  assert.equal(login.status, 200);
  const { token } = await login.json();
  const request = async (method, path, data, status = 200) => {
    const response = await fetch(host + path, {
      method, signal: AbortSignal.any([abort.signal, AbortSignal.timeout(60_000)]),
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json', 'User-Agent': 'shinyhub-lifecycle-test' },
      body: data === undefined ? undefined : JSON.stringify(data),
    });
    assert.equal(response.status, status, `${method} ${path}`);
    return response.status === 204 ? undefined : response.json();
  };
  const api = (path, data) => request(data === undefined ? 'GET' : 'POST', '/api/apps/browser' + path, data);
  const app = join(state, 'input');
  await cp(join(root, 'loadtest/browser/app'), app, { recursive: true, filter: source => !source.includes('__pycache__') });
  // Exercise the helper built from this exact tree, including packaging. The
  // release gate runs before PyPI publication, so never resolve the helper there.
  const wheels = join(state, 'wheels');
  await command(['uv', 'build', '--wheel', '--out-dir', wheels, join(root, 'packaging/python-identity')], 'identity-wheel');
  const wheelNames = (await readdir(wheels)).filter(name => name.endsWith('.whl'));
  assert.equal(wheelNames.length, 1, 'one identity helper wheel');
  const wheel = wheelNames[0];
  await cp(join(wheels, wheel), join(app, wheel));
  report.identity_wheel = { name: wheel, sha256: createHash('sha256').update(await readFile(join(wheels, wheel))).digest('hex') };
  const requirements = await readFile(join(app, 'requirements.txt'), 'utf8');
  assert.match(requirements, /^shinyhub-identity==[^\n]+$/m);
  await writeFile(join(app, 'requirements.txt'), requirements.replace(/^shinyhub-identity==[^\n]+$/m, `./${wheel}`));
  // The CLI uses its own config resolution; XDG_CONFIG_HOME alone does not
  // isolate it from an existing operator config or an inaccessible home.
  const deploy = () => command([binary, 'deploy', app, '--config', clientConfig, '--slug', 'browser', '--visibility', 'private', '--output', 'json'], 'deploy',
    { env: { ...env, SHINYHUB_HOST: host, SHINYHUB_TOKEN: token } });
  await check('deploy real Shiny v1', deploy);
  await request('POST', '/api/users', { username: viewer, password: viewerPassword, role: 'viewer' }, 201);
  await request('POST', '/api/apps/browser/members', { username: viewer, role: 'viewer' }, 204);

  browser = await chromium.launch({ headless: true });
  abort.signal.addEventListener('abort', () => { browser?.close().catch(() => {}); }, { once: true });
  report.browser = browser.version();
  context = await browser.newContext({ userAgent: 'shinyhub-lifecycle-test' });
  context.setDefaultTimeout(30_000);
  context.on('response', response => {
    if (response.url().endsWith('/.shinyhub/ready')) report.readiness.push({ status: response.status() });
  });
  context.on('requestfailed', request => {
    if (request.url().endsWith('/.shinyhub/ready')) report.readiness.push({ error: request.failure()?.errorText });
  });
  context.on('page', page => {
    page.on('pageerror', error => report.browserErrors.push(error.message));
    sockets.set(page, []);
    page.on('websocket', socket => sockets.get(page).push(socket));
  });
  const page = await context.newPage();
  const appURL = host + '/app/browser/';
  let original;
  await check('private app sign-in preserves the requested URL', async () => {
    await page.goto(appURL + '?check=return');
    await page.getByRole('link', { name: 'Log in', exact: true }).click();
    // The initial session check focuses Username when it finishes. Wait for
    // that signed-out state before filling so it cannot steal the input focus.
    await page.locator('body[data-auth="out"]').waitFor();
    await page.locator('#login-form').getByLabel('Username', { exact: true }).fill(viewer);
    await page.locator('#login-form').getByLabel('Password', { exact: true }).fill(viewerPassword);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.waitForURL(appURL + '?check=return');
    original = await output(page, 'v1', 0);
    await page.getByRole('spinbutton', { name: 'Samples' }).fill('500');
    await page.getByRole('button', { name: 'Recalculate' }).click();
    await output(page, 'v1', 1, 500, original);
    await page.getByRole('button', { name: 'Recalculate' }).click();
    await output(page, 'v1', 2, 500, original);
  });
  let fresh, second;
  await check('rolling deployment preserves old sessions and serves v2 to new sessions', async () => {
    await writeFile(join(app, 'version.txt'), 'v2\n');
    await deploy();
    await page.getByRole('button', { name: 'Recalculate' }).click();
    await output(page, 'v1', 3, 500, original);
    fresh = await context.newPage();
    await fresh.goto(appURL);
    second = await output(fresh, 'v2', 0);
    assert.notEqual(second, original);
    await fresh.getByRole('button', { name: 'Recalculate' }).click();
    await output(fresh, 'v2', 1, 1000, second);
    await page.close();
  });
  let recovered;
  await check('restart offers a new tab and preserves previous results as a snapshot', async () => {
    await api('/restart', {});
    await fresh.getByRole('link', { name: 'Start a new app session in a new tab', exact: true }).waitFor();
    await fresh.screenshot({ path: join(work, 'recovery.png') });
    const opened = context.waitForEvent('page');
    await fresh.getByRole('link', { name: 'Start a new app session in a new tab', exact: true }).click();
    recovered = await opened;
    await recovered.waitForLoadState('domcontentloaded');
    const next = await output(recovered, 'v2', 0);
    assert.notEqual(next, second);
    await recovered.getByRole('button', { name: 'Recalculate' }).click();
    await output(recovered, 'v2', 1, 1000, next);
    await output(fresh, 'v2', 1, 1000, second);
    // The switcher deliberately uses a closed shadow root. Check the native
    // accessibility tree, which is what assistive technology actually receives.
    const accessibility = await context.newCDPSession(fresh);
    try {
      await poll(async () => {
        const { nodes } = await accessibility.send('Accessibility.getFullAXTree');
        return nodes.some(node => !node.ignored && node.role?.value === 'button'
          && node.name?.value === 'Offline snapshot options');
      }, 'accessible snapshot status');
    } finally { await accessibility.detach(); }
    assert.equal(await fresh.locator('#shinyhub-status-overlay').getAttribute('aria-modal'), null);
    await fresh.screenshot({ path: join(work, 'snapshot.png') });
    assert.equal((await context.request.get(appURL + '.shinyhub/ready')).status(), 200);
    await fresh.close();
    await recovered.close();
  });
  await check('sleep and cold wake return a reactive v2 session', async () => {
    const sleeping = await api('/sleep', {});
    assert.equal(sleeping.status, 'hibernated');
    const woke = await context.newPage();
    await woke.goto(appURL);
    const label = await output(woke, 'v2', 0);
    assert.notEqual(label, second);
    await woke.getByRole('button', { name: 'Recalculate' }).click();
    await output(woke, 'v2', 1, 1000, label);
    await woke.close();
  });
  let denied, allowed, otherTab, privilegedSession;
  await check('a viewer without an entitlement is denied by the app', async () => {
    denied = await context.newPage();
    await denied.goto(appURL);
    await output(denied, 'v2', 0);
    const identity = await denied.locator('#result').textContent();
    assert.ok(identity.split('\n').includes(`User: ${viewer}`), 'real viewer identity');
    assert.ok(identity.split('\n').includes('Role: viewer'), 'no administrator shortcut');
    assert.ok(identity.split('\n').includes('Entitlements: '), 'no initial business permissions');
    await denied.getByRole('button', { name: 'Export report', exact: true }).click();
    await exportResult(denied, 'Denied', 0);
  });
  await check('a live grant requires recovery and enables a server-side operation without redeploy', async () => {
    await request('PUT', '/api/apps/browser/entitlements/report_export', { description: 'Export the test report' }, 204);
    await request('POST', '/api/apps/browser/entitlements/report_export/grants', { username: viewer }, 204);
    await assertDisconnected(denied);
    allowed = await recoverSession(denied, context);
    privilegedSession = await output(allowed, 'v2', 0);
    assert.notEqual(privilegedSession, /Session: ([a-f0-9]{8})/.exec(await denied.locator('#result').textContent())[1]);
    assert.ok((await allowed.locator('#result').textContent()).split('\n').includes('Entitlements: report_export'));
    await allowed.getByRole('button', { name: 'Export report', exact: true }).click();
    await exportResult(allowed, 'Allowed', 1);
    await exportResult(denied, 'Denied', 0);
    await denied.close();
    otherTab = await context.newPage();
    await otherTab.goto(appURL);
    await output(otherTab, 'v2', 0);
    await otherTab.getByRole('button', { name: 'Export report', exact: true }).click();
    await exportResult(otherTab, 'Allowed', 1);
  });
  await check('revocation disconnects every privileged tab and recovery denies the protected operation', async () => {
    const started = performance.now();
    await request('DELETE', '/api/apps/browser/entitlements/report_export/grants', { username: viewer }, 204);
    await Promise.all([assertDisconnected(allowed), assertDisconnected(otherTab)]);
    report.entitlement_revocation = { sweep_seconds: sweepSeconds, observed_seconds: +( (performance.now() - started) / 1000).toFixed(3) };
    const reduced = await recoverSession(allowed, context);
    const session = await output(reduced, 'v2', 0);
    assert.notEqual(session, privilegedSession, 'fresh server session after revocation');
    assert.ok((await reduced.locator('#result').textContent()).split('\n').includes('Entitlements: '));
    await reduced.getByRole('button', { name: 'Export report', exact: true }).click();
    await exportResult(reduced, 'Denied', 0);
    // Admission and ordinary reactive work still succeed. Previously delivered
    // data remains visible in disconnected tabs; revocation cannot erase it.
    await reduced.getByRole('button', { name: 'Recalculate' }).click();
    await output(reduced, 'v2', 1, 1000, session);
    await exportResult(allowed, 'Allowed', 1);
    await exportResult(otherTab, 'Allowed', 1);
    await allowed.close();
    await otherTab.close();
    await reduced.close();
  });
  report.status = 'passed';
  console.log('BROWSER LIFECYCLE E2E PASS');
} catch (error) {
  report.error = String(error);
  if (context) {
    report.pages = await Promise.all(context.pages().map(async page => ({
      path: new URL(page.url()).pathname,
      text: await page.locator('body').innerText({ timeout: 1000 }).catch(() => ''),
    })));
  }
  console.error(error);
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  await context?.close().catch(() => {});
  await browser?.close().catch(() => {});
  for (const child of children) child.kill('SIGKILL');
  if (server?.pid) {
    // Graceful shutdown reaps app replicas; kill only our process group if it stalls.
    const exited = once(server, 'close').catch(() => {});
    try { process.kill(-server.pid, 'SIGTERM'); } catch { /* already stopped */ }
    await Promise.race([exited, delay(20_000, undefined, { ref: false })]);
    try { process.kill(-server.pid, 'SIGKILL'); } catch { /* group is gone */ }
  }
  await rm(state, { recursive: true, force: true });
  await writeFile(join(work, 'result.json'), JSON.stringify(report, null, 2) + '\n', { mode: 0o600 });
  console.log(`Local diagnostics: ${work}`);
}
