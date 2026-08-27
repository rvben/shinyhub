import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  relativeTime,
  deploymentRowModel,
  deploymentListModels,
  deploymentTimelineModels,
  deploymentProviderIcon,
  provenanceModel,
} from '../static/views/deployment-row.js';

// The Deployments tab marks the LIVE deployment and suppresses its Roll back
// button. "Live" is the newest *succeeded* deployment — not the newest row and
// not current_version (which is newest-regardless-of-status) — because ShinyHub
// auto-reverts a failed deploy, so a failed/pending latest attempt does not
// change the running bundle.

const NOW = Date.UTC(2026, 5, 14, 12, 0, 0); // fixed clock for relativeTime

test('relativeTime renders compact buckets and tolerates bad input', () => {
  assert.equal(relativeTime(new Date(NOW - 30 * 1000), NOW), '30s ago');
  assert.equal(relativeTime(new Date(NOW - 5 * 60 * 1000), NOW), '5m ago');
  assert.equal(relativeTime(new Date(NOW - 3 * 3600 * 1000), NOW), '3h ago');
  assert.equal(relativeTime(new Date(NOW - 2 * 86400 * 1000), NOW), '2d ago');
  assert.equal(relativeTime(null, NOW), '');
  assert.equal(relativeTime('not-a-date', NOW), '');
});

test('provenance presents pipeline first with durable revision and optional MR', () => {
  const p = provenanceModel({
    run_id: '0123456789abcdef0123456789abcdef',
    fleet_id: 'prod-eu',
    metadata: {
      provider: 'gitlab',
      source: { label: 'GitLab pipeline #412', url: 'https://gitlab.example/pipelines/412' },
      revision: { sha: 'abcdef1234567890', ref: 'main', url: 'https://gitlab.example/commit/abcdef' },
      change: { label: 'MR !87', url: 'https://gitlab.example/mr/87' },
    },
  });
  assert.equal(p.available, true);
  assert.equal(p.label, 'GitLab pipeline #412');
  assert.equal(p.detail, 'abcdef12 · main');
  assert.deepEqual(p.change, { label: 'MR !87', url: 'https://gitlab.example/mr/87' });
  assert.equal(p.mark, '');
  assert.equal(p.providerIcon, 'gitlab');
});

test('deployment provider marks recognize brand aliases and keep a neutral fallback', () => {
  assert.equal(deploymentProviderIcon('gitlab'), 'gitlab');
  assert.equal(deploymentProviderIcon('GITLAB_CI'), 'gitlab');
  assert.equal(deploymentProviderIcon('github'), 'github');
  assert.equal(deploymentProviderIcon('github-actions'), 'github');
  assert.equal(deploymentProviderIcon('buildkite'), 'ci');
  assert.equal(deploymentProviderIcon(''), 'ci');
});

test('legacy deployments expose an explicit provenance fallback', () => {
  assert.deepEqual(provenanceModel(null), {
    available: false,
    label: 'Source not recorded',
    detail: 'No deployment source was captured',
    url: '',
    change: null,
    provider: '',
    mark: '',
    markIcon: '',
    providerIcon: '',
    headerText: '',
    headerDetail: '',
  });
});

test('direct deployment provenance distinguishes dashboard, CLI, watch, and API channels', () => {
  const dashboard = provenanceModel({ origin: { kind: 'direct', channel: 'dashboard', actor: 'admin' } });
  assert.equal(dashboard.label, 'Manual deployment');
  assert.equal(dashboard.detail, 'admin · Dashboard');
  assert.equal(dashboard.headerText, 'Deployed manually by admin');
  assert.equal(dashboard.headerDetail, 'Dashboard');
  assert.equal(dashboard.mark, '');
  assert.equal(dashboard.markIcon, 'manual');

  const cli = provenanceModel({ origin: { kind: 'direct', channel: 'cli', actor: 'release-bot' } });
  assert.equal(cli.label, 'CLI deployment');
  assert.equal(cli.headerText, 'Deployed via ShinyHub CLI by release-bot');
  assert.equal(cli.mark, 'CLI');

  const watch = provenanceModel({ origin: { kind: 'direct', channel: 'watch', actor: 'dev' } });
  assert.equal(watch.label, 'Remote development');
  assert.equal(watch.detail, 'dev · Remote development');
  assert.equal(watch.headerText, 'Deployed from a live development session by dev');
  assert.equal(watch.headerDetail, 'Remote development');
  assert.equal(watch.mark, 'DEV');

  const api = provenanceModel({ origin: { kind: 'direct', channel: 'api' } });
  assert.equal(api.label, 'Direct API deployment');
  assert.equal(api.detail, 'API');
  assert.equal(api.headerText, 'Deployed via API');
});

test('rollback provenance names the action and authenticated actor', () => {
  const p = provenanceModel({ origin: { kind: 'rollback', channel: 'dashboard', actor: 'admin' } });
  assert.equal(p.label, 'Rollback');
  assert.equal(p.detail, 'admin · Dashboard');
  assert.equal(p.headerText, 'Rolled back by admin');
  assert.equal(p.mark, '');
  assert.equal(p.markIcon, 'rollback');
});

test('a current succeeded deployment shows its release label and blocks its own rollback', () => {
  const m = deploymentRowModel(
    { id: 9, version: '300', release_number: 3, created_at: NOW - 60000, status: 'succeeded' },
    { isCurrent: true, now: NOW },
  );
  assert.equal(m.isCurrent, true);
  assert.equal(m.canRollback, false);
  assert.equal(m.releaseNumber, 3);
  assert.equal(m.releaseLabel, 'v3');
  assert.equal(m.version, '300'); // epoch kept for the hover/title
});

test('a non-current succeeded deployment offers rollback', () => {
  const m = deploymentRowModel(
    { id: 8, version: '200', release_number: 2, created_at: NOW - 120000, status: 'succeeded' },
    { isCurrent: false, now: NOW },
  );
  assert.equal(m.canRollback, true);
  assert.equal(m.releaseLabel, 'v2');
});

test('failed and pending deployments have no release label and never offer rollback', () => {
  const failed = deploymentRowModel(
    { id: 7, version: '150', release_number: null, created_at: NOW, status: 'failed', failure_reason: 'boom' },
    { isCurrent: false, now: NOW },
  );
  assert.equal(failed.status, 'failed');
  assert.equal(failed.failureReason, 'boom');
  assert.equal(failed.canRollback, false);
  assert.equal(failed.releaseLabel, ''); // no number for a failed attempt

  const pending = deploymentRowModel(
    { id: 6, version: '140', created_at: NOW, status: 'pending' },
    { isCurrent: false, now: NOW },
  );
  assert.equal(pending.canRollback, false);
  assert.equal(pending.releaseLabel, '');
});

test('deploymentListModels marks the newest SUCCEEDED row live, not a failed newest', () => {
  // Newest-first (id DESC); a failed latest attempt sits above the succeeded live
  // bundle. release_number comes from the server (succeeded only; null for failed).
  const rows = [
    { id: 3, version: '300', release_number: null, created_at: NOW - 1000, status: 'failed', failure_reason: 'bad deps' },
    { id: 2, version: '200', release_number: 2, created_at: NOW - 2000, status: 'succeeded' },
    { id: 1, version: '100', release_number: 1, created_at: NOW - 3000, status: 'succeeded' },
  ];
  const models = deploymentListModels(rows, NOW);
  assert.deepEqual(models.map(m => m.releaseLabel), ['', 'v2', 'v1']);
  // The failed newest row is NOT current; the newest succeeded one (v2) is.
  assert.deepEqual(models.map(m => m.isCurrent), [false, true, false]);
  // Rollback: not on the failed row, not on the live row, yes on the older succeeded.
  assert.deepEqual(models.map(m => m.canRollback), [false, false, true]);
});

test('deploymentListModels marks newest row live when all succeeded', () => {
  const rows = [
    { id: 2, version: '200', release_number: 2, created_at: NOW - 1000, status: 'succeeded' },
    { id: 1, version: '100', release_number: 1, created_at: NOW - 2000, status: 'succeeded' },
  ];
  const models = deploymentListModels(rows, NOW);
  assert.deepEqual(models.map(m => m.releaseLabel), ['v2', 'v1']);
  assert.deepEqual(models.map(m => m.isCurrent), [true, false]);
  assert.deepEqual(models.map(m => m.canRollback), [false, true]);
});

test('deploymentTimelineModels groups a development session without hiding attempts', () => {
  const session = { id: 'a'.repeat(32), target_kind: 'existing', status: 'ended', created_at: NOW - 9000, updated_at: NOW - 1000 };
  const rows = [
    { id: 4, version: '400', release_number: null, created_at: NOW - 1000, status: 'failed', provenance: { origin: { kind: 'direct', channel: 'cli', actor: 'dev', development_session_id: session.id }, development_session: session } },
    { id: 3, version: '300', release_number: 3, created_at: NOW - 2000, status: 'succeeded', provenance: { origin: { kind: 'direct', channel: 'cli', actor: 'dev', development_session_id: session.id }, development_session: session } },
    { id: 2, version: '200', release_number: 2, created_at: NOW - 3000, status: 'succeeded', provenance: { origin: { kind: 'direct', channel: 'cli' } } },
  ];
  const timeline = deploymentTimelineModels(rows, NOW);
  assert.equal(timeline.length, 2);
  assert.equal(timeline[0].kind, 'development-session');
  assert.equal(timeline[0].attemptCount, 2);
  assert.equal(timeline[0].failedCount, 1);
  assert.equal(timeline[0].latestReleaseLabel, 'v3');
  assert.equal(timeline[0].isCurrent, true);
  assert.deepEqual(timeline[0].attempts.map(a => a.id), [4, 3]);
  assert.equal(timeline[1].kind, 'deployment');
});

test('session provenance wins over its backward-compatible CLI channel', () => {
  const p = provenanceModel({
    origin: { kind: 'direct', channel: 'cli', actor: 'dev', development_session_id: 'b'.repeat(32) },
    development_session: { id: 'b'.repeat(32), target_kind: 'created', status: 'active' },
  });
  assert.equal(p.label, 'Remote development');
  assert.equal(p.mark, 'DEV');
  assert.equal(p.detail, 'dev · Remote development');
});
