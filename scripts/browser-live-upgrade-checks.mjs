import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFile, rename } from 'node:fs/promises';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const previousRelease = '0.22.0';

export async function installPreviousRelease({ root, state, command }) {
  assert.ok(['darwin', 'linux'].includes(process.platform), 'the live upgrade fixture requires macOS or Linux');
  const arch = { x64: 'amd64', arm64: 'arm64' }[process.arch];
  assert.ok(arch, 'the live upgrade fixture requires amd64 or arm64');
  const asset = `shinyhub_${process.platform}_${arch}.tar.gz`;
  const manifest = await readFile(join(root, 'testdata/compatibility', `v${previousRelease}-checksums.txt`), 'utf8');
  const expected = manifest.split('\n').map(line => line.trim().split(/\s+/)).find(([, name]) => name === asset)?.[0];
  assert.match(expected ?? '', /^[0-9a-f]{64}$/, `missing pinned checksum for ${asset}`);
  const archive = join(state, asset);
  await command(['curl', '-fsSL', '--retry', '3', '--max-time', '90', '-o', archive,
    `https://github.com/rvben/shinyhub/releases/download/v${previousRelease}/${asset}`], 'previous-release-download');
  const actual = createHash('sha256').update(await readFile(archive)).digest('hex');
  assert.equal(actual, expected, `checksum mismatch for v${previousRelease}/${asset}`);
  await command(['tar', '-xzf', archive, '-C', state, 'shinyhub'], 'previous-release-extract');
}

export async function checkLiveUpgrade({ context, page, original, appURL, state, binary, successor,
  targetVersion, config, command, request, check, output, sockets, report }) {
  const pid = async () => Number((await readFile(join(state, 'hub.pid'), 'utf8')).trim());
  const version = async () => (await request('GET', '/api/server-info')).version;
  const before = await version();
  assert.equal(before, previousRelease);
  assert.notEqual(targetVersion, before, 'the upgrade must cross server versions');
  const previousPID = await pid();
  assert.ok(Number.isInteger(previousPID) && previousPID > 1, 'the original server published its PID');
  const originalSocket = sockets.get(page).at(-1);
  assert.ok(originalSocket && !originalSocket.isClosed(), 'the original Shiny session has an open WebSocket');
  const originalSocketCount = sockets.get(page).length;

  const result = { before, expected_after: targetVersion, previous_pid: previousPID,
    http_probes: 0, http_failures: [] };
  report.live_upgrade = result;
  async function probe() {
    // Open a fresh connection each time to exercise the inherited listener.
    const response = await context.request.get(appURL, { headers: { Connection: 'close' }, maxRedirects: 0, timeout: 5000 });
    try {
      assert.equal(response.status(), 200, 'authenticated app HTTP probe');
      result.http_probes++;
    } finally { await response.dispose(); }
  }

  await probe();
  let probing = true;
  const traffic = (async () => {
    while (probing) {
      try { await probe(); }
      catch (error) { result.http_failures.push(error.message.split('\n', 1)[0]); }
      if (probing) await delay(50);
    }
  })();
  try {
    await check('replace the server binary and acknowledge the new version', async () => {
      await rename(successor, binary);
      const handoff = JSON.parse(await command([binary, 'reload', '--config', config,
        '--wait', '30s', '--output', 'json'], 'live-upgrade'));
      result.pid = handoff.pid;
      assert.equal(handoff.previous_pid, previousPID);
      assert.notEqual(handoff.pid, previousPID);
      assert.equal(await pid(), handoff.pid, 'the successor owns the configured PID file');
      result.after = await version();
      assert.equal(result.after, targetVersion);
      assert.equal((await request('GET', '/activez')).active, true);
    });

    await check('new Shiny sessions can open and recalculate on the successor', async () => {
      const fresh = await context.newPage();
      try {
        assert.equal((await fresh.goto(appURL)).status(), 200);
        const identity = await output(fresh, 'v1', 0);
        assert.notEqual(identity, original);
        await fresh.getByRole('button', { name: 'Recalculate' }).click();
        await output(fresh, 'v1', 1, 1000, identity);
      } finally { await fresh.close(); }
    });

    await check('the original Shiny session stays reactive on the same WebSocket', async () => {
      // Wait past the fixture's five-second drain budget to distinguish a
      // preserved session from one that is about to be disconnected.
      await delay(6000);
      await page.getByRole('button', { name: 'Recalculate' }).click();
      await output(page, 'v1', 3, 500, original);
      assert.equal(originalSocket.isClosed(), false, 'the original WebSocket survived');
      assert.equal(sockets.get(page).length, originalSocketCount, 'the original page did not reconnect');
    });
  } finally {
    probing = false;
    await traffic;
  }
  await check('authenticated app requests succeed throughout the handoff', async () => {
    assert.ok(result.http_probes > 2, 'sampled app traffic across the upgrade');
    assert.deepEqual(result.http_failures, []);
  });
}
