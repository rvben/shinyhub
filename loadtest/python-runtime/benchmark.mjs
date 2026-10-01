#!/usr/bin/env node
// Compare real Shiny sessions through ShinyHub's native launcher and proxy.
import assert from 'node:assert/strict';
import { spawn, execFile } from 'node:child_process';
import { createHash } from 'node:crypto';
import { once } from 'node:events';
import { cp, mkdir, mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { createRequire } from 'node:module';
import { dirname, join, resolve } from 'node:path';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const require = createRequire(new URL('../render/driver/package.json', import.meta.url));
const { chromium } = require('playwright');
const execute = promisify(execFile);
const options = { python: [], rounds: 3, sessions: 3, samples: 10, binary: 'tmp/python-compat/shinyhub', output: 'tmp/python-runtime', allowSourceBuilds: false };
for (let i = 2; i < process.argv.length; i++) {
  const flag = process.argv[i];
  if (flag === '--allow-source-builds') { options.allowSourceBuilds = true; continue; }
  if (flag === '--help') {
    console.log('Usage: node loadtest/python-runtime/benchmark.mjs [--python VERSION_OR_PATH ...] [--binary PATH] [--output DIRECTORY] [--rounds 3] [--sessions 3] [--samples 10] [--allow-source-builds]');
    process.exit(0);
  }
  const key = flag.slice(2);
  assert.ok(['python', 'binary', 'output', 'rounds', 'sessions', 'samples'].includes(key) && i + 1 < process.argv.length, `unknown or incomplete option: ${flag}`);
  const value = process.argv[++i];
  if (key === 'python') options.python.push(value);
  else options[key] = ['rounds', 'sessions', 'samples'].includes(key) ? Number(value) : value;
}
if (!options.python.length) options.python = ['3.14+gil', '3.15+gil'];
for (const key of ['rounds', 'sessions', 'samples']) assert.ok(Number.isSafeInteger(options[key]) && options[key] > 0 && options[key] <= 100, `invalid ${key}`);
const binary = resolve(root, options.binary);
const output = resolve(root, options.output);
await mkdir(output, { recursive: true, mode: 0o700 });
const work = await mkdtemp(join(output, 'comparison-'));
const app = join(work, 'app');
await mkdir(app, { mode: 0o700 });
for (const name of ['app.py', 'pyproject.toml']) await cp(join(root, 'loadtest/python-runtime', name), join(app, name));
const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('SHINYHUB_')));
const deadline = AbortSignal.timeout(600_000);
const report = { status: 'running', platform: `${process.platform}/${process.arch}`, options, variants: [], failures: [] };
let browser;
let active;

async function command(args, name, extraEnv = {}) {
  try {
    return await execute(args[0], args.slice(1), { cwd: root, env: { ...env, ...extraEnv }, timeout: 180_000, signal: deadline, maxBuffer: 4 * 1024 * 1024 });
  } catch (error) {
    throw new Error(`${name}: ${error.stderr || error.message}`);
  }
}

async function port() {
  const reservation = createServer();
  reservation.listen(0, '127.0.0.1');
  await once(reservation, 'listening');
  const value = reservation.address().port;
  await new Promise((done, reject) => reservation.close(error => error ? reject(error) : done()));
  return value;
}

async function stop(child) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  const ended = once(child, 'exit');
  // The foreground runner tears down its separately grouped app before exiting.
  child.kill('SIGTERM');
  const force = setTimeout(() => child.kill('SIGKILL'), 15_000);
  try { await ended; } finally { clearTimeout(force); }
}

async function memory(pid) {
  if (process.platform === 'linux') {
    const text = await readFile(`/proc/${pid}/smaps_rollup`, 'utf8');
    const kb = name => {
      const match = new RegExp(`^${name}:\\s+(\\d+)`, 'm').exec(text);
      assert.ok(match, `interpreter memory field ${name} is available`);
      return Number(match[1]) * 1024;
    };
    return { rss_bytes: kb('Rss'), pss_bytes: kb('Pss'), private_bytes: kb('Private_Clean') + kb('Private_Dirty'), swap_pss_bytes: kb('SwapPss') };
  }
  const { stdout } = await command(['ps', '-o', 'rss=', '-p', String(pid)], 'memory');
  const rss = Number(stdout.trim()) * 1024;
  assert.ok(Number.isFinite(rss) && rss > 0, 'interpreter memory is available');
  return { rss_bytes: rss, pss_bytes: null, private_bytes: null, swap_pss_bytes: null };
}

function statistics(values) {
  const sorted = [...values].sort((a, b) => a - b);
  const quantile = q => sorted[Math.max(0, Math.ceil(q * sorted.length) - 1)];
  return { count: values.length, median: quantile(0.5), p95: quantile(0.95), p99: quantile(0.99) };
}

try {
  // One universal dependency resolution supplies every variant and round.
  await command(['uv', 'lock', '--project', app, '--python', options.python[0]], 'lock benchmark dependencies');
  const lock = await readFile(join(app, 'uv.lock'));
  report.lock_sha256 = createHash('sha256').update(lock).digest('hex');
  report.binary_sha256 = createHash('sha256').update(await readFile(binary)).digest('hex');
  browser = await chromium.launch({ channel: process.env.SHINYHUB_E2E_BROWSER_CHANNEL || 'chrome', headless: true });
  report.browser = browser.version();
  const prepared = [];
  for (let index = 0; index < options.python.length; index++) {
    const requested = options.python[index];
    const { stdout } = await command(['uv', 'python', 'find', requested], `find Python ${requested}`);
    const python = stdout.trim();
    const state = join(work, `state-${index}`);
    const data = join(work, `data-${index}`);
    const variant = { requested, rounds: [] };
    report.variants.push(variant);
    const variantEnv = { UV_PYTHON: python, UV_PYTHON_DOWNLOADS: 'never', UV_NO_BUILD: options.allowSourceBuilds ? 'false' : 'true' };
    // Build variables must be explicit app variables: the native launcher
    // intentionally filters unapproved ambient variables from child processes.
    const appEnvArgs = Object.entries(variantEnv).flatMap(([key, value]) => ['--env', `${key}=${value}`]);
    await command([binary, 'run', app, '--check', '--state-dir', state, '--data-dir', data, ...appEnvArgs], `prepare Python ${requested}`, variantEnv);
    prepared.push({ requested, python, state, data, variant, variantEnv, appEnvArgs });
  }
  // Alternate order to reduce warm-cache and machine-load bias.
  for (let round = 0; round < options.rounds; round++) {
    const order = round % 2 ? [...prepared].reverse() : prepared;
    for (const { requested, python, state, data, variant, variantEnv, appEnvArgs } of order) {
      console.log(`==> Python ${requested}, round ${round + 1}/${options.rounds}`);
      const bindPort = await port();
      const url = `http://127.0.0.1:${bindPort}/app/runtime-benchmark/`;
      const started = performance.now();
      // Keep host dependency mode enabled: the prepared workspace avoids prep,
      // and this selects the production uv --frozen --no-sync launch command.
      const child = spawn(binary, ['run', app, '--slug', 'runtime-benchmark', '--port', String(bindPort), '--no-reload', '--state-dir', state, '--data-dir', data, ...appEnvArgs], { cwd: root, env: { ...env, ...variantEnv }, signal: deadline });
      active = child;
      let logs = '', startupError;
      child.stdout.on('data', data => { logs += data; });
      child.stderr.on('data', data => { logs += data; });
      child.on('error', error => { startupError = error; });
      const context = await browser.newContext({ userAgent: 'shinyhub-runtime-benchmark' });
      const errors = [];
      try {
        const until = Date.now() + 30_000;
        while (true) {
          deadline.throwIfAborted();
          if (startupError || child.exitCode !== null || child.signalCode !== null) throw new Error(`app failed: ${startupError || logs}`);
          try {
            const response = await fetch(url, { signal: AbortSignal.timeout(500), headers: { 'User-Agent': 'shinyhub-runtime-benchmark' } });
            if (response.ok) { await response.body?.cancel(); break; }
            await response.body?.cancel();
          } catch { /* waiting for readiness */ }
          if (Date.now() > until) throw new Error(`readiness timed out: ${logs}`);
          await delay(25);
        }
        const readiness_ms = performance.now() - started;
        const first_render_ms = [];
        const pages = await Promise.all(Array.from({ length: options.sessions }, async () => {
          const page = await context.newPage();
          page.on('pageerror', error => errors.push(error.message));
          page.setDefaultTimeout(15_000);
          const begin = performance.now();
          await page.goto(url);
          await page.locator('#result').filter({ hasText: 'Calculation: 0;' }).waitFor();
          await page.waitForFunction(() => document.querySelector('#runtime')?.textContent.includes('"pid"'));
          first_render_ms.push(performance.now() - begin);
          return page;
        }));
        const runtime = JSON.parse(await pages[0].locator('#runtime').textContent());
        const { stdout: expected } = await command([python, '-c', 'import sys; print(sys.version.split()[0])'], 'confirm runtime');
        assert.equal(runtime.python, expected.trim(), 'the app uses the requested interpreter');
        variant.runtime = runtime;
        const latency_ms = [];
        const compute_ms = [];
        for (let sample = 1; sample <= options.samples; sample++) {
          await Promise.all(pages.map(async page => {
            const begin = performance.now();
            await page.getByRole('button', { name: 'Calculate', exact: true }).click();
            await page.locator('#result').filter({ hasText: `Calculation: ${sample};` }).waitFor();
            latency_ms.push(performance.now() - begin);
            const text = await page.locator('#result').textContent();
            const result = /value: (\d+); compute_ms: ([\d.]+)/.exec(text);
            assert.ok(result, 'fixed-work render returned timing and result');
            if (report.calculation_value === undefined) report.calculation_value = result[1];
            assert.equal(result[1], report.calculation_value, 'all sessions and interpreters produce the same calculation');
            compute_ms.push(Number(result[2]));
          }));
        }
        assert.deepEqual(errors, [], 'no browser errors');
        const usage = await memory(runtime.pid);
        variant.rounds.push({ readiness_ms, first_render_ms, latency_ms, compute_ms, memory: usage });
      } finally {
        await context.close();
        await stop(child);
        active = undefined;
        await writeFile(join(work, `runtime-${variant.requested.replace(/[^a-zA-Z0-9.-]/g, '_')}-round-${round}.log`), logs, { mode: 0o600 });
      }
    }
  }
  for (const variant of report.variants) {
    variant.summary = {
      readiness_ms: statistics(variant.rounds.map(round => round.readiness_ms)),
      first_render_ms: statistics(variant.rounds.flatMap(round => round.first_render_ms)),
      latency_ms: statistics(variant.rounds.flatMap(round => round.latency_ms)),
      compute_ms: statistics(variant.rounds.flatMap(round => round.compute_ms)),
      rss_bytes: statistics(variant.rounds.map(round => round.memory.rss_bytes)),
    };
  }
  report.status = 'passed';
} catch (error) {
  report.status = 'failed';
  report.failures.push(error.message);
  process.exitCode = 1;
} finally {
  await stop(active);
  await browser?.close();
  await writeFile(join(work, 'results.json'), JSON.stringify(report, null, 2) + '\n', { mode: 0o600 });
  console.log(JSON.stringify({ status: report.status, results: join(work, 'results.json'), variants: report.variants.map(({ requested, runtime, summary }) => ({ requested, runtime, summary })), failures: report.failures }, null, 2));
}
