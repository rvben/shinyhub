#!/usr/bin/env node
import assert from 'node:assert/strict';
import { createHash, randomBytes } from 'node:crypto';
import { createFixture, items, recoverySource } from './acceptance/fixture.mjs';

const f = await createFixture({ recovery: true });
const { check, json, poll, response } = f;
const slug = 'recovery-forecast', appPath = '/api/apps/' + slug;
const description = 'Restored regional forecast · 東京';
const data = 'region,total\neurope,42\n';
const secret = randomBytes(24).toString('hex');
const digest = createHash('sha256').update(secret).digest('hex');
let original, schedule;

try {
  f.report.scope = 'backup-restore-upgrade';
  await check('deploy and save synthetic state on the previous release', async () => {
    assert.equal((await json('/api/server-info')).version, recoverySource);
    await json('/api/apps', { method: 'POST', body: { slug, name: 'Recovery forecast', access: 'private' } }, 201);
    await json(appPath, { method: 'PATCH', body: { description } });
    for (const [key, value, isSecret] of [['REGION', 'europe · 東京', false], ['RECOVERY_SECRET', secret, true]]) {
      await json(appPath + '/env/' + key, { method: 'PUT', body: { value, secret: isSecret } });
    }
    await f.upload(slug);
    original = (await json(appPath)).app;
    assert.equal(original.status, 'running');
    assert.ok(original.current_version);
    await response(appPath + '/data/regions.csv', { method: 'PUT', body: data, raw: true }).then(r => assert.ok(r.ok));
    schedule = await json(appPath + '/schedules', { method: 'POST', body: {
      name: 'recovery-probe', cron_expr: '0 0 * * *', enabled: false,
      command: [f.python, '-c',
        'import hashlib, os\n' +
        'assert hashlib.sha256(os.environ["RECOVERY_SECRET"].encode()).hexdigest() == ' + JSON.stringify(digest) + '\n' +
        'print("recovered-secret-ok")'],
      timeout_seconds: 30, overlap_policy: 'skip', missed_policy: 'skip',
    } }, 201);
  });
  await check('back up the live source and restore into empty storage using the current build', async () => {
    await f.restoreBackup();
    assert.notEqual((await json('/api/server-info')).version, recoverySource);
  });
  await check('original login, app identity, settings and file survive', async () => {
    const { app } = await json(appPath);
    assert.equal(app.id, original.id);
    assert.equal(app.current_version, original.current_version);
    assert.equal(app.description, description);
    assert.equal(app.access, 'private');
    const env = items(await json(appPath + '/env'));
    assert.equal(env.find(e => e.key === 'REGION')?.value, 'europe · 東京');
    assert.equal(env.find(e => e.key === 'RECOVERY_SECRET')?.secret, true);
    assert.ok(!JSON.stringify(env).includes(secret), 'the environment API must not reveal the secret');
    assert.equal(await json(appPath + '/data/regions.csv'), data);
  });
  await check('serve the original deployed app without uploading it again', async () => {
    const url = '/app/' + slug + '/';
    await json(appPath + '/restart?if_not_running=true', { method: 'POST' });
    const signIn = await response('/api/auth/session', { method: 'POST', auth: null,
      headers: { Origin: f.host }, body: { username: f.accounts.admin.username, password: f.accounts.admin.password } });
    assert.equal(signIn.status, 200, 'restored browser sign-in');
    const cookies = signIn.headers.getSetCookie().map(cookie => cookie.split(';', 1)[0]);
    await signIn.body.cancel();
    assert.ok(cookies.length > 0, 'sign-in issued a session cookie');
    const page = await response(url, { auth: null, headers: { Cookie: cookies.join('; ') } });
    assert.equal(page.status, 200, 'restored app served through the proxy');
    assert.match(await page.text(), /Acceptance forecast/);
    const { app } = await json(appPath);
    assert.equal(app.current_version, original.current_version);
    assert.equal(app.deploy_count, original.deploy_count);
  });
  await check('original schedule runs with the decrypted secret and never logs it', async () => {
    const path = appPath + '/schedules/' + schedule.id;
    const run = await json(path + '/run', { method: 'POST', body: {} }, 202);
    const runPath = path + '/runs/' + run.run_id;
    await poll(async () => {
      const { status } = await json(runPath);
      assert.ok(!['failed', 'timed_out', 'interrupted', 'skipped_overlap'].includes(status), 'restored schedule failed: ' + status);
      return status === 'succeeded';
    }, 'restored schedule succeeds');
    const logs = await json(runPath + '/logs');
    assert.ok(logs.includes('recovered-secret-ok'), 'schedule verified the exact secret');
    assert.ok(!logs.includes(secret), 'schedule logs must not reveal the secret');
  });
  f.report.status = 'passed';
} catch (error) {
  f.report.error = error.message;
  console.error(error);
  process.exitCode = 1;
} finally { await f.close(); }
