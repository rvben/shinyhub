import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
import { items } from './fixture.mjs';

export async function runAPIJourneys(f) {
  const { json, response, check, poll } = f;
  await check('four real roles with isolated accounts', async () => {
    for (const role of ['viewer', 'developer', 'operator']) {
      f.accounts[role] = { username: 'acceptance-' + role, password: randomBytes(24).toString('hex') };
      const user = await json('/api/users', { method: 'POST', body: { ...f.accounts[role], role } }, 201);
      f.accounts[role].user = user;
      await f.login(role);
    }
  });
  await check('project and app creation, deployment, and invalid ZIP validation', async () => {
    await json('/api/projects', { method: 'POST', body: { slug: 'acceptance', name: 'Acceptance workspace' } }, 201);
    await json('/api/apps', { method: 'POST', body: { slug: 'forecast', name: 'Regional forecast · 東京', project_slug: 'acceptance', access: 'private' } }, 201);
    const form = new FormData();
    form.append('bundle', new Blob(['not a ZIP']), 'invalid.zip');
    const bad = await json('/api/apps/forecast/deploy', { method: 'POST', body: form, raw: true }, 422);
    assert.match(bad.error, /valid ZIP bundle/);
    await f.upload('forecast');
    const { app } = await json('/api/apps/forecast');
    assert.equal(app.status, 'running');
  });
  await check('settings and environment round-trip', async () => {
    await json('/api/apps/forecast', { method: 'PATCH', body: { description: 'Verified through the real backend · 東京' } });
    assert.equal((await json('/api/apps/forecast')).app.description, 'Verified through the real backend · 東京');
    await json('/api/apps/forecast/env/REGION', { method: 'PUT', body: { value: 'europe · 東京', secret: false } });
    assert.equal(items(await json('/api/apps/forecast/env')).find(e => e.key === 'REGION').value, 'europe · 東京');
  });
  await check('data round-trip and live shared-data unmount protection', async () => {
    const content = 'region,total\neurope,42\n';
    const uploaded = await response('/api/apps/forecast/data/regions.csv', { method: 'PUT', body: content, raw: true });
    assert.ok(uploaded.ok);
    assert.equal(await (await response('/api/apps/forecast/data/regions.csv')).text(), content);
    await json('/api/apps', { method: 'POST', body: { slug: 'reference', name: 'Shared reference', access: 'private' } }, 201);
    await response('/api/apps/reference/data/source.txt', { method: 'PUT', body: 'keep this source', raw: true }).then(r => assert.ok(r.ok));
    await json('/api/apps/forecast/shared-data', { method: 'POST', body: { source_slug: 'reference' } }, 201);
    const blocked = await json('/api/apps/forecast/shared-data/reference', { method: 'DELETE' }, 409);
    assert.match(blocked.error, /restart/);
    assert.equal(items(await json('/api/apps/forecast/shared-data')).length, 1);
    await json('/api/apps/forecast/shared-data/reference?restart=true', { method: 'DELETE' });
    assert.equal(items(await json('/api/apps/forecast/shared-data')).length, 0);
    assert.equal(await (await response('/api/apps/reference/data/source.txt')).text(), 'keep this source');
  });
  await check('successful and failed scheduled runs reach terminal status and logs', async () => {
    for (const [name, exit, status] of [['refresh', 0, 'succeeded'], ['failure', 3, 'failed']]) {
      const schedule = await json('/api/apps/forecast/schedules', { method: 'POST', body: {
        name, cron_expr: '0 0 * * *', enabled: false, command: ['sh', '-c', `echo acceptance-${name}; exit ${exit}`],
        timeout_seconds: 30, overlap_policy: 'skip', missed_policy: 'skip',
      } }, 201);
      const run = await json(`/api/apps/forecast/schedules/${schedule.id}/run`, { method: 'POST', body: {} }, 202);
      const path = `/api/apps/forecast/schedules/${schedule.id}/runs/${run.run_id}`;
      await poll(async () => (await json(path)).status === status, `${name} terminal run`);
      const logs = await response(path + '/logs');
      assert.equal(logs.status, 200);
      assert.match(await logs.text(), new RegExp('acceptance-' + name));
      if (!exit) f.scheduleID = schedule.id;
    }
  });
  await check('personal tokens authenticate, stay private, and stop working after revocation', async () => {
    const created = await json('/api/tokens', { method: 'POST', body: { name: 'acceptance-token', expires_in_days: 30 } }, 201);
    assert.ok(typeof created.token === 'string' && created.token.startsWith('shk_'), 'a personal token was issued');
    const expiresIn = Date.parse(created.expires_at) - Date.now();
    assert.ok(expiresIn > 29 * 86400_000 && expiresIn <= 30 * 86400_000);
    const credential = { auth: null, headers: { Authorization: `token ${created.token}` } };
    await json('/api/auth/me', credential);
    assert.ok(!JSON.stringify(await json('/api/tokens')).includes(created.token), 'list never reveals token value');
    await json('/api/tokens', { method: 'POST', body: { name: 'acceptance-token' } }, 409);
    await json('/api/tokens/' + created.id, { method: 'DELETE', auth: f.accounts.viewer.token }, 404);
    await json('/api/tokens/' + created.id, { method: 'DELETE' }, 204);
    await json('/api/auth/me', credential, 401);
  });
  await check('announcement retries are idempotent and stale edits conflict', async () => {
    const body = { title: 'Acceptance maintenance', message: 'Disposable end-to-end notice · 東京', severity: 'information', publication: 'draft' };
    const headers = { 'Idempotency-Key': randomBytes(16).toString('hex') };
    const created = await json('/api/announcements', { method: 'POST', body, headers }, 201);
    const replay = await json('/api/announcements', { method: 'POST', body, headers }, 201);
    assert.equal(replay.id, created.id);
    assert.equal(replay.revision, created.revision);
    const path = '/api/announcements/' + created.id;
    const edited = await json(path, { method: 'PATCH', body: { expected_revision: created.revision, message: 'Confirmed saved edit' } });
    await json(path, { method: 'PATCH', body: { expected_revision: created.revision, message: 'Stale edit' } }, 409);
    assert.equal((await json(path)).message, 'Confirmed saved edit');
    assert.equal(edited.revision, created.revision + 1);
    f.announcementID = created.id;
  });
  await check('role boundaries and membership do not grant settings access', async () => {
    for (const path of ['/api/users', '/api/workers', '/api/audit', '/api/announcements']) {
      await json(path);
      for (const role of ['viewer', 'developer', 'operator']) await json(path, { auth: f.accounts[role].token }, 403);
    }
    for (const path of ['/api/apps', '/api/users', '/api/apps/forecast/env']) await json(path, { auth: null }, 401);
    await json('/api/apps/forecast', { auth: f.accounts.viewer.token }, 404);
    await json('/api/apps/forecast/env', { auth: f.accounts.operator.token });
    await json('/api/apps/forecast/env', { auth: f.accounts.developer.token }, 404);
    await json('/api/apps/forecast/members', { method: 'POST', body: { username: f.accounts.viewer.username, role: 'viewer' } }, 204);
    await json('/api/apps/forecast', { auth: f.accounts.viewer.token });
    await json('/api/apps/forecast/env', { auth: f.accounts.viewer.token }, 403);
    await json('/api/apps/forecast', { method: 'PATCH', auth: f.accounts.viewer.token, body: { name: 'Unauthorized change' } }, 403);
    await json('/api/apps', { method: 'POST', auth: f.accounts.viewer.token, body: { slug: 'denied', name: 'Denied' } }, 403);
    await json('/api/apps', { method: 'POST', auth: f.accounts.developer.token, body: { slug: 'developer-app', name: 'Developer owned' } }, 201);
    await json('/api/apps/developer-app', { method: 'DELETE', auth: f.accounts.developer.token });
  });
  await check('identity role changes apply to existing sessions', async () => {
    const id = f.accounts.developer.user.id;
    await json('/api/users/' + id, { method: 'PATCH', body: { role: 'viewer' } });
    assert.equal((await json('/api/auth/me', { auth: f.accounts.developer.token })).user.role, 'viewer');
    await json('/api/apps', { method: 'POST', auth: f.accounts.developer.token, body: { slug: 'denied-role', name: 'Denied role' } }, 403);
    await json('/api/users/' + id, { method: 'PATCH', body: { role: 'developer' } });
  });
  await check('redeploy and audit history retain real results', async () => {
    await f.upload('forecast');
    assert.equal(items(await json('/api/apps/forecast/deployments')).filter(d => d.status === 'succeeded').length, 2);
    const audit = await json('/api/audit?q=forecast&action=update_app');
    assert.ok(audit.events.length > 0);
    assert.ok(audit.events.every(e => e.action === 'update_app' && e.resource_id === 'forecast'));
    f.auditEventID = audit.events[0].id;
  });
}

// Run after browser inspection: these actions intentionally dismantle the app.
export async function runLifecycleJourneys(f) {
  await f.check('rollback, stop, restart, and deletion preserve the expected app state', async () => {
    const deployments = items(await f.json('/api/apps/forecast/deployments')).filter(d => d.status === 'succeeded');
    const previous = deployments.find(d => d.version !== deployments[0].version);
    assert.ok(previous, 'a previous deployment is available');
    const rolledBack = await f.json('/api/apps/forecast/rollback', {
      method: 'POST', body: { deployment_id: previous.id },
    });
    assert.equal(rolledBack.current_version, previous.version);
    assert.equal((await f.json('/api/apps/forecast')).app.status, 'running');
    assert.equal((await f.json('/api/apps/forecast')).app.deploy_count, 2);
    await f.json('/api/apps/forecast/stop', { method: 'POST' });
    assert.equal((await f.json('/api/apps/forecast')).app.status, 'stopped');
    await f.json('/api/apps/forecast/restart', { method: 'POST' });
    const { app } = await f.json('/api/apps/forecast');
    assert.equal(app.status, 'running');
    assert.equal(app.current_version, previous.version);
    await f.json('/api/apps/forecast', { method: 'DELETE' });
    await f.json('/api/apps/forecast', {}, 404);
  });
}
