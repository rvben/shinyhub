import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { appAttention, renderAppAttention } from '../static/views/app-attention.js';

test('producer-quarantined existing release requires operator attention', () => {
  assert.deepEqual(appAttention({
    status: 'failed', deploy_count: 1, last_deployment_status: 'succeeded',
    name: 'Dashboard', last_error: 'Producer failed after a data write',
  }), {
    severity: 'danger',
    label: 'Unavailable',
    detail: 'A producer or deployment failed. Review the app details and repair the failure.',
  });
});

test('live producer failure and repair update attention independently of release success', () => {
  const app = { status: 'running', deploy_count: 2, last_deployment_status: 'succeeded' };
  assert.equal(appAttention(app, { status: 'failed' }).severity, 'danger');
  assert.equal(appAttention({ ...app, status: 'failed' }, { status: 'running' }), null);
});

test('failed apps render a recovery entry in the attention list', () => {
  const doc = new JSDOM('<!doctype html><body></body>').window.document;
  const section = renderAppAttention(doc, [{
    slug: 'dashboard', name: 'Dashboard', status: 'failed', deploy_count: 2,
  }]);
  assert.equal(section.tagName, 'DETAILS');
  assert.equal(section.open, false);
  assert.equal(section.querySelector('summary').textContent, '1 app needs attention');
  assert.equal(section.querySelector('.app-attention-problem strong').textContent, 'Unavailable');
  assert.equal(section.querySelector('.app-attention-action').getAttribute('href'), '/apps/dashboard');
});

test('serving app with deployment fence needs attention before a restart', () => {
  const app = { slug: 'fenced', name: 'Fenced', status: 'running', deploy_count: 2,
    last_deployment_status: 'failed', deployment_repair_required: true };
  const attention = appAttention(app);
  assert.equal(attention.severity, 'warning');
  assert.equal(attention.label, 'Deployment repair required');
  assert.match(attention.detail, /cannot recover after a restart/);
  const doc = new JSDOM('<!doctype html><body></body>').window.document;
  const section = renderAppAttention(doc, [app]);
  assert.equal(section.querySelector('.app-attention-problem strong').textContent, 'Deployment repair required');
  assert.equal(section.querySelector('.app-attention-action').getAttribute('href'), '/apps/fenced');
  assert.equal(appAttention({ ...app, deployment_repair_required: false }), null);
});

test('routine running producer does not look like a durable deployment repair', () => {
  assert.equal(appAttention({ status: 'running', deploy_count: 1,
    compatibility_quarantined: true, deployment_repair_required: false }), null);
});
