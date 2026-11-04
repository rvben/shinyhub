import { test } from 'node:test';
import assert from 'node:assert/strict';
import { DEPLOY_EVENTS, uploadDeployment, deploymentOutcome } from '../static/views/deployment-upload.js';
class Target {
  listeners = {};
  addEventListener(name, fn) { (this.listeners[name] ||= []).push(fn); }
  emit(name, event = {}) { for (const fn of this.listeners[name] || []) fn(event); }
}
class XHR extends Target {
  upload = new Target(); headers = {}; status = 200; responseText = ''; type = DEPLOY_EVENTS;
  open(method, url) { this.method = method; this.url = url; }
  setRequestHeader(name, value) { this.headers[name] = value; }
  getResponseHeader() { return this.type; }
  send(form) { this.form = form; }
  abort() { this.aborted = true; this.emit('abort'); }
  chunk(text) { this.responseText += text; this.emit('progress'); }
}
function transfer(options = {}) {
  const xhr = new XHR(); const phases = [], uploads = [];
  const task = uploadDeployment({ slug: 'test', blob: new Blob(['app']), csrf: 'test-token', createXHR: () => xhr,
    onPhase: event => phases.push(event), onUpload: value => uploads.push(value), ...options });
  return { xhr, phases, uploads, ...task };
}
const record = value => JSON.stringify(value) + '\n';
test('upload completion transitions into server phases, including fragmented records', async () => {
  const t = transfer();
  t.xhr.upload.emit('progress', { lengthComputable: true, loaded: 10, total: 10 });
  t.xhr.upload.emit('load');
  const phase = record({ type: 'phase', phase: 'dependencies', status: 'started', message: 'Installing packages' });
  t.xhr.chunk(phase.slice(0, 15)); t.xhr.chunk(phase.slice(15));
  t.xhr.chunk(record({ type: 'result', result: { status: 'running' } })); t.xhr.emit('load');
  assert.equal((await t.promise).status, 'running');
  assert.deepEqual(t.uploads, [100]); assert.equal(t.phases.at(-1).phase, 'dependencies');
  assert.equal(t.xhr.headers.Accept, DEPLOY_EVENTS);
});
test('legacy JSON remains compatible', async () => {
  const t = transfer(); t.xhr.type = 'application/json'; t.xhr.responseText = '{"status":"stopped","kept_stopped":true}'; t.xhr.emit('load');
  assert.equal(deploymentOutcome(await t.promise).openable, false);
});
for (const event of ['error', 'timeout', 'abort', 'load']) test(`interrupted stream (${event}) never invites a blind redeploy`, async () => {
  const t = transfer(); t.xhr.chunk(record({ type: 'phase', phase: 'commit', status: 'completed' })); t.xhr.emit(event);
  await assert.rejects(t.promise, error => error.uncertain && /history before deploying again/.test(error.message));
});
test('a terminal result remains confirmed when transport subsequently fails', async () => {
  const t = transfer(); t.xhr.chunk(record({ type: 'result', result: { status: 'running' } })); t.xhr.emit('error');
  assert.equal((await t.promise).status, 'running');
});
test('idle deadline preserves a terminal result and releases the connection', async () => {
  const t = transfer({ idleMs: 5 }); t.xhr.chunk(record({ type: 'result', result: { status: 'running' } }));
  assert.equal((await t.promise).status, 'running'); assert.equal(t.xhr.aborted, true);
});
test('idle deadline without a result reports uncertainty', async () => {
  const t = transfer({ idleMs: 5 }); await assert.rejects(t.promise, error => error.uncertain); assert.equal(t.xhr.aborted, true);
});
test('server failure after publication names the error and requires checking history', async () => {
  const t = transfer(); t.xhr.chunk(record({ type: 'phase', phase: 'commit', status: 'completed' }));
  t.xhr.chunk(record({ type: 'error', phase: 'configuration', message: 'Could not save settings', status_code: 500 })); t.xhr.emit('load');
  await assert.rejects(t.promise, error => error.uncertain && /Could not save settings/.test(error.message));
});
test('invalid terminal results are not reported as success', async () => {
  const t = transfer(); t.xhr.chunk('{"type":"result","result":null}\n');
  await assert.rejects(t.promise, error => error.uncertain);
});
test('HTTP authentication failures remain distinguishable', async () => {
  const t = transfer(); t.xhr.status = 401; t.xhr.responseText = '{"error":"Sign in again"}'; t.xhr.emit('load');
  await assert.rejects(t.promise, error => error.status === 401 && !error.uncertain);
});
test('outcomes disclose stopped, sleeping, skipped setup and session handoff', () => {
  assert.equal(deploymentOutcome({ status: 'running', kept_stopped: true }).openable, false);
  assert.match(deploymentOutcome({ kept_hibernated: true }).title, /on demand/);
  const outcome = deploymentOutcome({ status: 'running', hooks_skipped: 2, warning: 'Check configuration', handoff: true, drain_timeout_seconds: 30,
    schedule_convergence: [{ schedule: 'prepare', status: 'pending' }] });
  assert.match(outcome.details.join(' '), /2 post-deploy hooks/);
  assert.match(outcome.details.join(' '), /30 seconds/);
  assert.match(outcome.details.join(' '), /prepare: scheduled setup is pending/);
});


test('streamed capacity errors preserve structured failure details for recoverable UI feedback', async () => {
  const t = transfer();
  t.xhr.chunk(record({type:'error',phase:'capacity',failure_kind:'capacity',status_code:409,message:'Not enough capacity'}));t.xhr.emit('load');
  await assert.rejects(t.promise,error=>error.status===409 && error.deployment.failure_kind==='capacity' && !error.uncertain);
});
