import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { appAttention, attentionApps, renderAppAttention } from '../static/views/app-attention.js';

test('strict admission includes actionable exceptions and excludes routine state', () => {
  assert.equal(appAttention({ status: 'stopped', deploy_count: 2 }), null);
  assert.equal(appAttention({ status: 'hibernated', deploy_count: 2 }), null);
  assert.equal(appAttention({ status: 'running', deploy_count: 2, last_deployment_status: 'failed' }), null,
    'a failed redeploy does not hide the still-serving successful release');
  assert.equal(appAttention({ status: 'crashed', deploy_count: 2 }).label, 'Crashed');
  assert.equal(appAttention({ status: 'stopped', deploy_count: 0, last_deployment_status: 'failed' }).label,
    'Deployment failed');
});

test('live readiness admits materially under-ready running apps', () => {
  const attention = appAttention({ status: 'running', deploy_count: 2, replicas: 3 }, {
    status: 'running',
    replicas: [{ status: 'running' }, { status: 'starting' }, { status: 'lost' }],
  });
  assert.deepEqual(attention, {
    severity: 'warning',
    label: 'Replica capacity reduced',
    detail: '1 of 3 replicas are ready.',
  });
});

test('deploying suppresses stale exception state', () => {
  assert.equal(appAttention({ status: 'crashed', deploy_count: 2, deploying: true }), null);
  assert.equal(appAttention({ status: 'crashed', deploy_count: 2 }, { deploying: true }), null);
});

test('attentionApps orders danger before warning, then by name', () => {
  const apps = [
    { slug: 'z', name: 'Zulu', status: 'degraded', deploy_count: 1 },
    { slug: 'b', name: 'Beta', status: 'crashed', deploy_count: 1 },
    { slug: 'a', name: 'Alpha', status: 'crashed', deploy_count: 1 },
  ];
  assert.deepEqual(attentionApps(apps).map(item => item.app.slug), ['a', 'b', 'z']);
});

test('renderAppAttention builds a labelled recovery rail with safe links', () => {
  const doc = new JSDOM('<!doctype html><body></body>').window.document;
  const section = renderAppAttention(doc, [{
    slug: 'sales & ops', name: 'Revenue <Pulse>', project_name: 'Commercial',
    status: 'crashed', deploy_count: 3, icon_emoji: '📉',
  }]);
  assert.equal(section.querySelector('h2').textContent, 'Needs attention');
  assert.equal(section.querySelector('.app-attention-count').textContent, '1');
  assert.equal(section.querySelector('.app-attention-name').textContent, 'Revenue <Pulse>');
  assert.equal(section.querySelector('.app-attention-avatar').textContent, '📉');
  assert.equal(section.querySelector('.app-attention-action').getAttribute('href'), '/apps/sales%20%26%20ops');
  assert.match(section.querySelector('.app-attention-meta').textContent, /Commercial/);
  assert.equal(section.querySelector('script'), null);
});

test('renderAppAttention returns null for a healthy fleet', () => {
  const doc = new JSDOM('<!doctype html><body></body>').window.document;
  assert.equal(renderAppAttention(doc, [{ status: 'running', deploy_count: 2 }]), null);
});
