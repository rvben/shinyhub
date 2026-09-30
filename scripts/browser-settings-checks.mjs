import assert from 'node:assert/strict';
import { setTimeout as delay } from 'node:timers/promises';

export async function checkBrowserSettings({ browser, page, host, username, password, api, request,
  check, poll, output, original, sockets, calculations, deployFixture }) {
  const operatorContext = await browser.newContext({ userAgent: 'shinyhub-settings-test' });
  try {
    const operator = await operatorContext.newPage();
    operator.setDefaultTimeout(30_000);
    operator.on('dialog', dialog => dialog.accept());
    const initialSocket = sockets.get(page).at(-1);
    const initialConnections = sockets.get(page).length;
    const live = async () => {
      await page.getByRole('button', { name: 'Recalculate' }).click();
      await output(page, 'v1', ++calculations, 500, original);
      assert.equal(sockets.get(page).length, initialConnections, 'original session did not reconnect');
      assert.equal(initialSocket.isClosed(), false);
    };
    await check('dashboard environment deletion defers application and preserves the live Shiny session', async () => {
      await request('PUT', '/api/apps/browser/env/LIVE_EDIT_TEST', { value: 'saved', secret: false });
      const before = await api('');
      await operator.goto(host + '/login?next=' + encodeURIComponent('/apps/browser/configuration'));
      await operator.locator('body[data-auth="out"]').waitFor();
      await operator.locator('#login-form').getByLabel('Username', { exact: true }).fill(username);
      await operator.locator('#login-form').getByLabel('Password', { exact: true }).fill(password);
      await operator.getByRole('button', { name: 'Sign in', exact: true }).click();
      await operator.waitForURL(host + '/apps/browser/configuration');
      const row = operator.locator('#env-list tbody tr').filter({ hasText: 'LIVE_EDIT_TEST' });
      await row.waitFor();
      const response = operator.waitForResponse(response => response.request().method() === 'DELETE'
        && new URL(response.url()).pathname === '/api/apps/browser/env/LIVE_EDIT_TEST');
      await row.getByRole('button', { name: 'Delete', exact: true }).click();
      const result = await response;
      assert.equal(result.status(), 204);
      assert.equal(new URL(result.request().url()).search, '', 'deletion does not request an implicit restart');
      await poll(async () => await row.count() === 0, 'environment row removed');
      await page.getByRole('button', { name: 'Recalculate' }).click();
      await output(page, 'v1', ++calculations, 500, original);
      assert.equal(sockets.get(page).length, initialConnections);
      assert.equal(initialSocket.isClosed(), false);
      const after = await api('');
      assert.equal(after.replicas_status[0].pid, before.replicas_status[0].pid);
    });
    await check('dashboard environment save defaults to deferred application and resets that choice', async () => {
      const before = await api('');
      await operator.locator('#env-add-btn').click();
      assert.equal(await operator.locator('#env-form-restart').isChecked(), false);
      await operator.locator('#env-form-key').fill('DEFERRED_EDIT_TEST');
      await operator.locator('#env-form-value').fill('saved');
      const response = operator.waitForResponse(r => r.request().method() === 'PUT'
        && new URL(r.url()).pathname === '/api/apps/browser/env/DEFERRED_EDIT_TEST');
      await operator.locator('#env-form-save').click();
      const result = await response;
      assert.equal(result.status(), 200);
      assert.equal(new URL(result.url()).search, '');
      assert.equal((await result.json()).restart_required, true);
      await poll(() => operator.locator('#env-form').isHidden(), 'environment form saved');
      await live();
      assert.equal((await api('')).replicas_status[0].pid, before.replicas_status[0].pid);
      await operator.locator('#env-add-btn').click();
      await operator.locator('#env-form-restart').check();
      await operator.locator('#env-form-cancel').click();
      await operator.locator('#env-add-btn').click();
      assert.equal(await operator.locator('#env-form-restart').isChecked(), false, 'previous opt-in does not carry into a later edit');
      await operator.locator('#env-form-cancel').click();
    });
    await check('equivalent inherited isolation and inactive worker limits preserve a real Shiny session', async () => {
      const before = await api('');
      await request('PATCH', '/api/apps/browser', { worker_isolation: null });
      await request('PATCH', '/api/apps/browser', { worker_isolation: 'multiplex', worker_max_workers: 4 });
      await live();
      assert.equal((await api('')).replicas_status[0].pid, before.replicas_status[0].pid);
    });
    await check('placement growth and shrink preserve the unchanged real replica', async () => {
      const before = await api('');
      await request('PATCH', '/api/apps/browser', { placement: { local: 2 } });
      await poll(async () => { const state = await api(''); return !state.redeploy_in_flight && state.replicas_status.length === 2
        && state.replicas_status.every(row => row.status === 'running'); }, 'placement growth converges', 60_000);
      await live();
      assert.equal((await api('')).replicas_status[0].pid, before.replicas_status[0].pid);
      await request('PATCH', '/api/apps/browser', { placement: { local: 1 } });
      await poll(async () => { const state = await api(''); return !state.redeploy_in_flight && state.replicas_status.length === 1; }, 'placement shrink converges');
      await request('PATCH', '/api/apps/browser', { placement: null });
      await poll(async () => !(await api('')).redeploy_in_flight, 'placement inheritance restored');
      await live();
      assert.equal((await api('')).replicas_status[0].pid, before.replicas_status[0].pid);
    });
    await check('dashboard resource save preserves concurrent sibling settings and the Shiny session', async () => {
      await operator.reload();
      await poll(() => operator.locator('#resources-memory').isVisible(), 'resource form loaded');
      const before = await api('');
      const memory = before.app.effective_memory_limit_mb;
      await request('PATCH', '/api/apps/browser', { memory_limit_mb: memory });
      const cpu = before.resource_enforcement?.cpu ? before.app.effective_cpu_quota_percent : 150;
      await operator.locator('#resources-cpu').fill(String(cpu));
      const response = operator.waitForResponse(r => r.request().method() === 'PATCH' && new URL(r.url()).pathname === '/api/apps/browser');
      await operator.locator('#resources-save-btn').click();
      const result = await response;
      assert.equal(result.status(), 200);
      assert.deepEqual(result.request().postDataJSON(), { cpu_quota_percent: cpu });
      const state = await api('');
      assert.equal(state.app.memory_limit_mb, memory, 'stale sibling value was not restored');
      assert.equal(state.replicas_status[0].pid, before.replicas_status[0].pid);
      await live();
    });

    await check('dashboard environment application failures remain visible and preserve the live session', async () => {
      await operator.route('**/api/apps/browser/env/apply', route => route.fulfill({
        status: 500, contentType: 'application/json',
        body: JSON.stringify({ error: 'Applying saved environment failed', restart_error: 'runtime refused stop' }),
      }));
      try {
        assert.equal(await operator.locator('#env-form').isHidden(), true);
        await operator.locator('#env-apply-btn').click();
        await operator.getByText('runtime refused stop', { exact: true }).waitFor({ state: 'visible', timeout: 5000 });
        assert.equal(await operator.locator('#env-apply-btn').isEnabled(), true, 'failed application can be retried');
        await live();
        await operator.unroute('**/api/apps/browser/env/apply');
        await operator.route('**/api/apps/browser/env/apply', route => route.fulfill({
          status: 200, contentType: 'application/json', body: JSON.stringify({ restarted: false }),
        }));
        await operator.locator('#env-apply-btn').click();
        await poll(() => operator.locator('#env-apply-btn').isEnabled(), 'application retry completes');
        assert.equal(await operator.locator('#env-form-error').isHidden(), true, 'retry clears the previous failure');
      } finally {
        await operator.unroute('**/api/apps/browser/env/apply');
      }
    });

    // A second actual Shiny app exercises demand-driven grouped workers and
    // the lifetime callback wired by the production server entry point.
    await request('POST', '/api/apps', { slug: 'settings-grouped', name: 'Settings grouped', access: 'private' }, 201);
    await request('PATCH', '/api/apps/settings-grouped', { worker_isolation: 'grouped', worker_grouped_size: 2,
      worker_max_workers: 3, worker_max_session_lifetime_secs: 6 });
    await deployFixture('settings-grouped');
    const grouped = await operatorContext.newPage();
    const groupedSockets = [];
    grouped.on('websocket', socket => groupedSockets.push(socket));
    await grouped.goto(host + '/app/settings-grouped/');
    const groupedSession = await output(grouped, 'v1', 0);
    const groupedState = () => request('GET', '/api/apps/settings-grouped');
    const worker = (await groupedState()).worker_pool.workers.find(worker => worker.sessions > 0);
    assert.ok(worker?.pid > 0, 'real assigned grouped worker');
    await check('grouped lifetime increase and disable preserve a session past its previous deadline', async () => {
      await request('PATCH', '/api/apps/settings-grouped', { worker_max_session_lifetime_secs: 8 });
      await request('PATCH', '/api/apps/settings-grouped', { worker_max_session_lifetime_secs: 0 });
      await delay(8500);
      await grouped.getByRole('button', { name: 'Recalculate' }).click();
      await output(grouped, 'v1', 1, 1000, groupedSession);
      assert.equal(groupedSockets.length, 1);
      assert.equal(groupedSockets[0].isClosed(), false);
      assert.equal((await groupedState()).worker_pool.workers.find(w => w.slot_id === worker.slot_id)?.pid, worker.pid);
    });
    await check('dashboard grouped capacity increases and decreases preserve assigned workers', async () => {
      await operator.goto(host + '/apps/settings-grouped/configuration');
      await poll(async () => await operator.locator('#worker-isolation').inputValue().catch(() => '') === 'grouped', 'grouped controls populated');
      let calculation = 1;
      for (const [field, value, payload] of [
        ['#worker-grouped-size', 3, { worker_grouped_size: 3 }],
        ['#worker-max-workers', 4, { worker_max_workers: 4 }],
        ['#worker-grouped-size', 1, { worker_grouped_size: 1 }],
        ['#worker-max-workers', 1, { worker_max_workers: 1 }],
      ]) {
        await operator.locator(field).fill(String(value));
        const response = operator.waitForResponse(r => r.request().method() === 'PATCH' && new URL(r.url()).pathname === '/api/apps/settings-grouped');
        await operator.locator('#scaling-save-btn').click();
        const result = await response;
        assert.equal(result.status(), 200);
        assert.deepEqual(result.request().postDataJSON(), payload);
        await grouped.getByRole('button', { name: 'Recalculate' }).click();
        await output(grouped, 'v1', ++calculation, 1000, groupedSession);
        assert.equal(groupedSockets.length, 1);
        assert.equal(groupedSockets[0].isClosed(), false);
        const state = await groupedState();
        assert.equal(state.redeploy_in_flight, false);
        assert.equal(state.worker_pool.workers.find(w => w.slot_id === worker.slot_id)?.pid, worker.pid);
      }
    });
    await check('explicit dashboard environment application drains a grouped Shiny session before restarting', async () => {
      await request('PUT', '/api/apps/settings-grouped/env/EXPLICIT_EDIT_TEST', { value: 'saved', secret: false });
      const response = operator.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/apps/settings-grouped/env/apply');
      await operator.locator('#env-apply-btn').click();
      await poll(async () => (await groupedState()).worker_pool?.workers.some(w => w.status === 'draining'), 'grouped worker draining');
      assert.equal(groupedSockets[0].isClosed(), false, 'session survives the start of draining');
      await grouped.getByRole('button', { name: 'Recalculate' }).click();
      await output(grouped, 'v1', 6, 1000, groupedSession);
      await grouped.close();
      assert.equal((await response).status(), 200);
      assert.equal((await response).request().postData(), null);
    });
    await request('POST', '/api/apps/settings-grouped/stop');
    return calculations;
  } finally {
    await operatorContext.close();
  }
}
