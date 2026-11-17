// A setup failure must never count as a successful negative control.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

assert.ok(!process.env.SHINYHUB_E2E_ROLE_DIR, 'restart control requires the reference unit');
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const child = spawnSync('flock', ['--no-fork', '--timeout', '600', '/tmp/shinyhub-systemd-upgrade.lock',
  process.execPath, 'scripts/browser-lifecycle-e2e.mjs', '--systemd-upgrade'], {
  cwd: root, encoding: 'utf8', timeout: 1_320_000, maxBuffer: 4 * 1024 * 1024,
  env: { ...process.env, SHINYHUB_E2E_UPGRADE_MUTATION: 'restart' },
});
try {
  assert.ifError(child.error);
  assert.equal(child.status, 1, 'disruptive deployment must fail');
  const directory = /^Local diagnostics: (.+)$/m.exec(child.stdout)?.[1];
  assert.ok(directory && resolve(directory).startsWith(join(root, 'loadtest/results/browser-live-upgrade-')));
  const result = JSON.parse(readFileSync(join(directory, 'result.json'), 'utf8'));
  assert.equal(result.status, 'failed');
  assert.equal(result.cleanup_error, undefined, 'negative control cleans up successfully');
  assert.equal(result.first_failure?.check, 'the original Shiny session stays reactive on the same WebSocket');
  assert.match(result.first_failure.error, /the original WebSocket survived/);
  assert.equal(typeof result.live_upgrade?.after, 'string', 'candidate version was observed');
  assert.ok(result.live_upgrade.after.length > 0);
  assert.equal(result.live_upgrade.after, result.live_upgrade.expected_after, 'candidate really served');
  assert.ok(result.live_upgrade.http_failures.length > 0, 'HTTPS probes observe the restart outage');
  const summary = { status: 'passed', control: 'restart interrupts the original WebSocket',
    first_failure: result.first_failure, http_failures: result.live_upgrade.http_failures.length };
  writeFileSync(join(directory, 'negative-control.json'), JSON.stringify(summary, null, 2) + '\n');
  console.log('PASS: stop/start was rejected by the WebSocket continuity assertion');
  console.log(`Local diagnostics: ${directory}`);
} catch (error) {
  process.stderr.write(child.stdout || '');
  process.stderr.write(child.stderr || '');
  throw error;
}
