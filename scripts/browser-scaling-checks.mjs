import assert from 'node:assert/strict';

// Real dashboard edits, real replica processes, and the actual Shiny client.
// Each reactive check pins the session label and the WebSocket incarnation.
export async function checkBrowserScaling({ browser, context, page, host, username, password,
  api, request, check, poll, output, original, sockets }) {
  const appURL = host + '/app/browser/';
  const contexts = [];
  const initialSockets = sockets.get(page).length;
  const initialSocket = sockets.get(page).at(-1);
  let calculations = 2;
  const originalLive = async () => {
    await page.getByRole('button', { name: 'Recalculate' }).click();
    await output(page, 'v1', ++calculations, 500, original);
    assert.equal(sockets.get(page).length, initialSockets, 'original session did not reconnect');
    assert.equal(initialSocket.isClosed(), false, 'original WebSocket remains open');
  };
  const snapshot = async () => {
    const body = await api('');
    return { count: body.app.replicas, rows: body.replicas_status, pending: body.redeploy_in_flight };
  };
  const ready = async count => poll(async () => {
    const current = await snapshot();
    return current.count === count && !current.pending && current.rows.length === count
      && current.rows.every(row => row.status === 'running');
  }, `${count} real replicas ready`, 60_000);
  const pids = current => Object.fromEntries(current.rows.map(row => [row.index, row.pid]));
  const unchanged = (before, after) => {
    for (const [index, pid] of Object.entries(before)) {
      assert.ok(pid > 0, 'real process PID');
      assert.equal(after[index], pid, `replica ${index} process was preserved`);
    }
  };
  try {
    const operatorContext = await browser.newContext({ userAgent: 'shinyhub-scaling-test' });
    contexts.push(operatorContext);
    const operator = await operatorContext.newPage();
    operator.setDefaultTimeout(30_000);
    const dialogs = [];
    operator.on('dialog', async dialog => {
      dialogs.push(dialog.message());
      await dialog.accept();
    });
    await operator.goto(host + '/login?next=' + encodeURIComponent('/apps/browser/configuration'));
    await operator.locator('body[data-auth="out"]').waitFor();
    await operator.locator('#login-form').getByLabel('Username', { exact: true }).fill(username);
    await operator.locator('#login-form').getByLabel('Password', { exact: true }).fill(password);
    await operator.getByRole('button', { name: 'Sign in', exact: true }).click();
    await operator.waitForURL(host + '/apps/browser/configuration');
    await poll(async () => await operator.locator('#scaling-replicas').inputValue().catch(() => '') === '1',
      'scaling form populated');
    const save = async (field, value, expected) => {
      await operator.locator(field).fill(String(value));
      const response = operator.waitForResponse(response => response.request().method() === 'PATCH'
        && new URL(response.url()).pathname === '/api/apps/browser');
      await operator.locator('#scaling-save-btn').click();
      const result = await response;
      assert.equal(result.status(), 200, 'dashboard settings saved');
      assert.deepEqual(result.request().postDataJSON(), expected, 'dashboard sends only edited settings');
      await operator.locator('#scaling-status').waitFor({ state: 'visible' });
    };
    const initialPids = pids(await snapshot());
    await check('dashboard cap increase preserves a real Shiny session and process', async () => {
      await save('#scaling-cap', 20, { max_sessions_per_replica: 20 });
      await originalLive();
      unchanged(initialPids, pids(await snapshot()));
      assert.deepEqual(dialogs, [], 'cap increase requires no restart confirmation');
    });
    await check('dashboard replica growth starts only added processes', async () => {
      await save('#scaling-replicas', 3, { replicas: 3 });
      await ready(3);
      unchanged(initialPids, pids(await snapshot()));
      await originalLive();
      assert.deepEqual(dialogs, [], 'replica growth requires no restart confirmation');
    });
    const extraSession = async () => {
      const storage = await context.storageState();
      storage.cookies = storage.cookies.filter(cookie => cookie.name !== 'shinyhub_rep_browser');
      const isolated = await browser.newContext({ storageState: storage, userAgent: 'shinyhub-scaling-test' });
      contexts.push(isolated);
      const tab = await isolated.newPage();
      const connections = [];
      tab.on('websocket', socket => connections.push(socket));
      await tab.goto(appURL);
      const session = await output(tab, 'v1', 0);
      assert.notEqual(session, original, 'new client has an independent Shiny session');
      const sticky = (await isolated.cookies(appURL)).find(cookie => cookie.name === 'shinyhub_rep_browser');
      assert.ok(sticky, 'new client received a replica cookie');
      return { isolated, tab, connections, session, index: Number(sticky.value.split('.')[0]) };
    };
    let first, second;
    await check('new browser sessions reach both added real replicas', async () => {
      first = await extraSession();
      second = await extraSession();
      assert.deepEqual([first.index, second.index].sort(), [1, 2]);
      for (const client of [first, second]) {
        await client.tab.getByRole('button', { name: 'Recalculate' }).click();
        await output(client.tab, 'v1', 1, 1000, client.session);
        assert.equal(client.connections.length, 1);
        assert.equal(client.connections[0].isClosed(), false);
      }
    });
    await check('cap-only save from a stale form preserves the newer replica count and all sessions', async () => {
      const before = pids(await snapshot());
      await request('PATCH', '/api/apps/browser', { replicas: 4 });
      await ready(4);
      assert.equal(await operator.locator('#scaling-replicas').inputValue(), '3', 'form deliberately remains stale');
      await save('#scaling-cap', 1, { max_sessions_per_replica: 1 });
      await ready(4);
      unchanged(before, pids(await snapshot()));
      await originalLive();
      for (const client of [first, second]) {
        await client.tab.getByRole('button', { name: 'Recalculate' }).click();
        await output(client.tab, 'v1', 2, 1000, client.session);
        assert.equal(client.connections.length, 1);
        assert.equal(client.connections[0].isClosed(), false);
      }
      assert.deepEqual(dialogs, []);
    });
    await check('manual shrink drains removed replicas and preserves the surviving Shiny session', async () => {
      const before = pids(await snapshot());
      const highest = first.index === 2 ? first : second;
      const remaining = first.index === 1 ? first : second;
      await save('#scaling-replicas', 1, { replicas: 1 });
      assert.equal(dialogs.length, 1, 'only shrinking requires a disconnection confirmation');
      assert.match(dialogs[0], /drain/i);
      await poll(async () => (await snapshot()).rows.some(row => row.index === 2 && row.desired_state === 'draining'),
        'replica 2 draining');
      await originalLive();
      await highest.tab.getByRole('button', { name: 'Recalculate' }).click();
      await output(highest.tab, 'v1', 3, 1000, highest.session);
      assert.equal(highest.connections[0].isClosed(), false, 'removed replica serves its session while draining');
      // Closing replica 2's session allows early completion; keep replica 1's
      // session open to exercise the configured forced-disconnection deadline.
      await highest.isolated.close();
      await poll(async () => (await snapshot()).rows.some(row => row.index === 1 && row.desired_state === 'draining'),
        'replica 1 draining');
      await remaining.tab.getByRole('button', { name: 'Recalculate' }).click();
      await output(remaining.tab, 'v1', 3, 1000, remaining.session);
      const drainStarted = Date.now();
      await ready(1);
      assert.ok(Date.now() - drainStarted >= 1000, 'open removed session was given a drain grace');
      await poll(() => remaining.connections[0].isClosed(), 'only removed session disconnected');
      await originalLive();
      unchanged({ 0: before[0] }, pids(await snapshot()));
      await remaining.isolated.close();
    });
    // Leave the fixture ready for the existing rolling/restart lifecycle checks.
    await save('#scaling-cap', 20, { max_sessions_per_replica: 20 });
    return calculations;
  } finally {
    await Promise.all(contexts.map(context => context.close().catch(() => {})));
  }
}
