import assert from 'node:assert/strict';
import { readFile, readdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

// Runs inside the real lifecycle harness with a disposable native app and
// extension-free browser. All sessions carry visible random server identities.
export async function checkSessionRelays({ context, page, original, appURL, host, state, app, work,
  binary, config, command, request, deploy, check, poll, output, assertDisconnected, sockets, viewerID, report }) {
  const pidFile = join(state, 'hub.pid');
  const currentPID = async () => Number((await readFile(pidFile, 'utf8')).trim());
  const retiredPIDs = new Set();
  const exited = pid => {
    try { process.kill(pid, 0); return false; }
    catch (error) { if (error.code === 'ESRCH') return true; throw error; }
  };
  const ready = async () => fetch(host + '/activez', { signal: AbortSignal.timeout(2000),
    headers: { 'User-Agent': 'shinyhub-session-relay-test' } }).then(r => r.ok).catch(() => false);
  const reload = async () => {
    const previous = await currentPID();
    const result = JSON.parse(await command([binary, 'reload', '--config', config, '--output', 'json'], 'acknowledged-reload'));
    assert.equal(result.previous_pid, previous);
    assert.notEqual(result.pid, previous);
    retiredPIDs.add(previous);
    await poll(async () => (await currentPID()) !== previous && await ready(), 'successor owns control plane', 30_000);
  };
  let probing = true;
  const failures = [];
  const result = { http_probes: 0, http_failures: failures };
  report.session_relays = result;
  const originalSockets = [...(sockets.get(page) ?? [])];
  const sameSockets = () => sockets.get(page)?.length === originalSockets.length
    && sockets.get(page).every((socket, index) => socket === originalSockets[index]);
  const probes = (async () => {
    while (probing) {
      try {
        const response = await context.request.get(appURL, { timeout: 5000 });
        result.http_probes++;
        if (response.status() !== 200) failures.push(`page status ${response.status()}`);
        await response.dispose();
      } catch (error) { failures.push(String(error)); }
      await delay(200);
    }
  })();
  let originalPID;
  let second, third, secondIdentity;
  try {
    await check('two reloads preserve original Shiny session past the reload deadline', async () => {
      originalPID = await currentPID();
      assert.ok(originalSockets.length > 0, 'original session opened a WebSocket');
      assert.ok(originalSockets.every(socket => !socket.isClosed()), 'original sockets start open');
      await reload();
      await delay(6000);
      await page.getByRole('button', { name: 'Recalculate' }).click();
      await output(page, 'v1', 3, 500, original);
      assert.ok(sameSockets(), 'first reload must not reconnect the original page');
      assert.ok(originalSockets.every(socket => !socket.isClosed()), 'first reload preserves the original sockets');
      second = await context.newPage();
      await second.goto(appURL);
      const identity = await output(second, 'v1', 0);
      secondIdentity=identity;
      assert.notEqual(identity, original, 'successor accepts a distinct Shiny session');
      await second.getByRole('button', { name: 'Recalculate' }).click();
      await output(second, 'v1', 1, 1000, identity);
      await reload();
      await delay(6000);
      await page.getByRole('button', { name: 'Recalculate' }).click();
      await output(page, 'v1', 4, 500, original);
      await second.getByRole('button', { name: 'Recalculate' }).click();
      await output(second, 'v1', 2, 1000, identity);
      assert.ok(sameSockets(), 'second reload must not reconnect the original page');
      assert.ok(originalSockets.every(socket => !socket.isClosed()), 'original websocket remains open');
    });
    await check('relay limit refuses an upgrade without closing a working session', async () => {
      third = await context.newPage();
      await third.goto(appURL);
      await output(third, 'v1', 0);
      const previous = await currentPID();
      await command([binary, 'reload', '--config', config], 'reload-refused', { expectedExit: 'nonzero' });
      assert.match(await readFile(join(work, 'reload-refused.log'), 'utf8'), /reload relay limit reached/);
      await poll(async () => (await readFile(join(work, 'server.log'), 'utf8')).includes('reload relay limit reached'), 'explicit relay-limit refusal');
      assert.equal(await currentPID(), previous);
      await page.getByRole('button', { name: 'Recalculate' }).click();
      await output(page, 'v1', 5, 500, original);
      await page.close();
      const recovered = JSON.parse(await command([binary, 'reload', '--config', config, '--wait', '15s', '--output', 'json'], 'reload-waits-for-relay-capacity'));
      assert.notEqual(recovered.pid, previous);
      retiredPIDs.add(previous);
      await poll(ready, 'third successor ready');
      await output(second, 'v1', 2);
      await output(third, 'v1', 0);
      // Once its sessions close, the retired controller must exit. The
      // independent output pump must keep collecting the adopted app's logs.
      await poll(() => {
        try { process.kill(originalPID, 0); return false; }
        catch (error) {
          if (error.code === 'ESRCH') return true;
          throw error;
        }
      }, 'retired controller exits after its last session closes');
      const retirementLogged = (await readFile(join(work, 'server.log'), 'utf8')).split('\n').some(line => {
        try {
          const entry = JSON.parse(line);
          return entry.msg === 'retired process exiting' && entry.pid === originalPID;
        } catch { return false; }
      });
      assert.ok(retirementLogged, 'retired controller logs its exit');
      await second.getByRole('button', { name: 'Recalculate' }).click();
      await output(second, 'v1', 3, 1000, secondIdentity);
      await poll(async () => {
        const dir=join(state, 'apps/browser/logs');
        const texts=await Promise.all((await readdir(dir)).filter(name=>name.endsWith('.log')).map(name=>readFile(join(dir,name),'utf8')));
        return texts.some(text=>text.includes(`RELAYLOG:${secondIdentity}:3`));
      }, 'native log output survives session-relay retirement');
    });
    await check('deployment after repeated reloads protects relayed backend until app deadline', async () => {
      await writeFile(join(app, 'version.txt'), 'v2\n');
      await deploy();
      await second.getByRole('button', { name: 'Recalculate' }).click();
      await output(second, 'v1', 4);
      const fresh = await context.newPage();
      await fresh.goto(appURL);
      await output(fresh, 'v2', 0);
      await command([binary, 'reload', '--config', config, '--wait', '100ms'], 'reload-refused-during-drain', { expectedExit: 'nonzero' });
      assert.match(await readFile(join(work, 'reload-refused-during-drain.log'), 'utf8'), /browser: draining generation/);
      await command([binary, 'reload', '--config', config, '--wait', '15s'], 'reload-waits-for-generation-drain');
      await assertDisconnected(second);
      await assertDisconnected(third);
      await fresh.getByRole('button', { name: 'Recalculate' }).click();
      await output(fresh, 'v2', 1);
      await fresh.close();
    });
    await check('nine retired sessions reconnect together without deferred page loads', async () => {
      const burst = [];
      try {
        // The server has sixteen render cores: nine tabs fit, even while the
        // old generation drains. Do not hide deliberate pacing behind retries.
        for (let i = 0; i < 9; i++) {
          const tab = await context.newPage(); burst.push(tab);
          assert.equal((await tab.goto(appURL)).status(), 200);
          await output(tab, 'v2', 0);
        }
        await writeFile(join(app, 'version.txt'), 'v3\n');
        await deploy();
        await Promise.all(burst.map(assertDisconnected));
        const responses = await Promise.all(burst.map(tab => tab.reload()));
        assert.deepEqual(responses.map(response => response.status()), Array(9).fill(200));
        await Promise.all(burst.map(tab => output(tab, 'v3', 0)));
      } finally { await Promise.all(burst.map(tab => tab.close())); }
    });
    await check('closing the final browser sessions retires all relays and preserves the active server', async () => {
      const retained = await context.newPage();
      assert.equal((await retained.goto(appURL)).status(), 200);
      await output(retained, 'v3', 0);
      await reload();
      const activePID = await currentPID();
      const before = JSON.parse(await command([binary, 'status', '--config', config,
        '--output', 'json'], 'before-final-relay-status'));
      assert.ok(before.retired.count > 0 && before.retired.sessions > 0, 'retirement check starts with live relays');
      for (const generation of before.retired.generations ?? []) {
        assert.ok(Number.isInteger(generation.pid) && generation.pid > 1, 'retired generation reports its PID');
        retiredPIDs.add(generation.pid);
      }
      await Promise.all(context.pages().map(tab => tab.close()));
      let finalStatus;
      await poll(async () => {
        const status = JSON.parse(await command([binary, 'status', '--config', config,
          '--output', 'json'], 'final-relay-status'));
        finalStatus = status;
        return status.status === 'observed' && status.pid === activePID
          && status.retired?.unknown === false && status.retired.count === 0
          && status.retired.sessions === 0;
      }, 'all relays retire after their final session closes');
      await poll(() => [...retiredPIDs].every(exited), 'retired processes exit');
      assert.equal(await currentPID(), activePID, 'normal retirement preserves the active PID');
      assert.ok(await ready(), 'active server serves after normal retirement');
      result.active_pid = activePID;
      result.retired_count = finalStatus.retired.count;
    });
    // The revocation check deliberately makes this viewer unauthorized. Keep
    // availability probes running through normal retirement, then stop before
    // that intentional access refusal.
    probing = false;
    await probes;
    assert.deepEqual(failures, [], 'all page probes succeed throughout reloads and retirement');
    await check('access revocation reaches a session held by a retiring process', async () => {
      const retained = await context.newPage();
      await retained.goto(appURL);
      await output(retained, 'v3', 0);
      await reload();
      await request('DELETE', `/api/apps/browser/members/${viewerID}`, undefined, 204);
      await assertDisconnected(retained);
      await retained.close();
    });
  } finally {
    probing = false;
    await probes;
  }
  assert.deepEqual(failures, [], 'all page probes succeed throughout reloads and deployment');
  assert.ok(result.http_probes >= 10, 'sampled app traffic throughout reloads and retirement');
  const logs = await readFile(join(work, 'server.log'), 'utf8');
  assert.ok(!logs.includes('no WebSocket has connected'), 'relayed sessions suppress false connectivity errors');
  assert.ok(!/orphan dir sweep[^\n]*\.reload-relays/.test(logs), 'relay lease directory is not reported as orphan');
}
