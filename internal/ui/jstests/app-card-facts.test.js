import { test } from 'node:test';
import assert from 'node:assert/strict';
import { appCardFacts } from '../static/views/app-card-facts.js';

const now = Date.parse('2026-08-14T12:00:00Z');

test('a never-deployed card states the missing prerequisite once', () => {
  assert.deepEqual(appCardFacts({ deploy_count: 0 }, null, now), [
    { text: 'No release deployed', tone: 'attention', title: '' },
  ]);
});

test('a failed first deployment is surfaced as an exception', () => {
  const facts = appCardFacts({
    last_deployment_status: 'failed',
    last_deployed_at: '2026-08-14T11:45:00Z',
  }, null, now);
  assert.equal(facts[0].text,
    'Latest deployment failed');
  assert.equal(facts[0].tone, 'danger');
  assert.equal(facts.some(item => item.text.startsWith('Deployed ')), false);
});

test('a deployed card shows release, recency, and configured instances', () => {
  const facts = appCardFacts({
    release_number: 18,
    released_at: '2026-08-14T11:48:00Z',
    last_deployment_status: 'succeeded',
    replicas: 3,
  }, null, now);
  assert.deepEqual(facts.map(item => item.text), [
    'Release #18', 'Deployed 12 min ago', '3 instances',
  ]);
});

test('live replica readiness replaces the configured instance count', () => {
  const facts = appCardFacts({
    release_number: 4,
    released_at: '2026-08-14T10:00:00Z',
    replicas: 3,
  }, {
    status: 'degraded',
    replicas: [{ status: 'running' }, { status: 'running' }, { status: 'starting' }],
  }, now);
  assert.equal(facts.at(-1).text, '2/3 ready');
});

test('a hibernated scale-to-zero app describes its policy', () => {
  const facts = appCardFacts({
    release_number: 1,
    released_at: '2026-08-13T12:00:00Z',
    status: 'hibernated',
    replicas: 3,
    autoscale_enabled: true,
    autoscale_min_replicas: 0,
  }, null, now);
  assert.equal(facts.at(-1).text, 'Scales to zero');
});

test('fleet governance outranks routine readiness after a metrics poll', () => {
  const facts = appCardFacts({
    release_number: 3,
    released_at: '2026-08-14T11:00:00Z',
    managed_by: 'fleet:production',
    replicas: 3,
  }, {
    status: 'running',
    replicas: [{ status: 'running' }, { status: 'running' }, { status: 'running' }],
  }, now);
  assert.equal(facts.at(-1).text, 'Fleet managed');
  assert.equal(facts.at(-1).title, 'Managed by fleet:production');
});

test('invalid release timestamps do not create misleading recency', () => {
  const facts = appCardFacts({ release_number: 2, released_at: 'not-a-date' }, null, now);
  assert.deepEqual(facts.map(item => item.text), ['Release #2']);
});
