import { afterEach, beforeEach, test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { buildOverviewModel } from '../static/views/overview-model.js';
import {
  renderResourcePressure,
  replaceOverviewContent,
  resourceLiveSignature,
  resourceLiveSummary,
} from '../static/views/overview.js';

const MIB = 1024 * 1024;
let dom;

beforeEach(() => {
  dom = new JSDOM('<!doctype html><body></body>');
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
});

afterEach(() => {
  dom.window.close();
  delete globalThis.window;
  delete globalThis.document;
});

function replica(index, cpu, rssMB, memMB = 512, cpuQuota = 100) {
  return {
    index,
    status: 'running',
    metrics_available: true,
    cpu_percent: cpu,
    rss_bytes: rssMB * MIB,
    effective_memory_limit_mb: memMB,
    effective_cpu_quota_percent: cpuQuota,
    memory_limit_enforced: true,
    cpu_quota_enforced: true,
    resource_enforcement_known: true,
  };
}

function resourcesFor(apps, metrics, host = null) {
  return buildOverviewModel(apps, {
    state: 'ready', metrics, generatedAt: new Date().toISOString(), host,
  }).resources;
}

// An app with neither a limit nor an enforcement flag: the shape prod carries
// when no operator has ever set one.
function unlimited(index, cpu, rssMB) {
  return { ...replica(index, cpu, rssMB, 0, 0), memory_limit_enforced: false, cpu_quota_enforced: false };
}

test('resource renderer shows stable capacity ratios, semantic meters, labels, and coverage', () => {
  const resources = resourcesFor([{ slug: 'forecast', name: 'Forecast', status: 'running' }], {
    forecast: { status: 'running', replicas: [replica(0, 43, 256)] },
  });
  const node = renderResourcePressure(resources);
  document.body.appendChild(node);

  assert.equal(node.getAttribute('aria-labelledby'), 'ov-resources-title');
  assert.match(node.textContent, /0\.4 cores \/ 1\.0 cores/);
  assert.match(node.textContent, /256 MB \/ 512 MB/);
  assert.match(node.textContent, /1 of 1 running replicas covered/);
  const meters = node.querySelectorAll('meter');
  assert.equal(meters.length, 2);
  assert.equal(meters[0].high, 0.95);
  assert.match(meters[0].getAttribute('aria-label'), /CPU allocation, 43 percent/);
});

test('small real CPU loads retain enough precision to avoid a false zero', () => {
  const resources = resourcesFor([{ slug: 'small', status: 'running' }], {
    small: { status: 'running', replicas: [replica(0, 4, 64)] },
  });
  const node = renderResourcePressure(resources);
  assert.match(node.textContent, /0\.04 cores \/ 1\.0 cores/);
  assert.match(node.textContent, /4%/);
});

test('resource renderer never turns unavailable metrics into plausible zeros', () => {
  const resources = buildOverviewModel([{ slug: 'a', status: 'running', replicas: 1 }], {
    state: 'unavailable', metrics: {}, generatedAt: null,
  }).resources;
  const node = renderResourcePressure(resources);
  assert.match(node.textContent, /Metrics unavailable/);
  assert.doesNotMatch(node.textContent, /0%|0 KB/);
  assert.equal(node.querySelector('meter'), null);
});

test('hotspot rows name severity, metric, replica, and configuration target', () => {
  const resources = resourcesFor([{ slug: 'forecast', name: 'Forecast', status: 'running' }], {
    forecast: { status: 'running', replicas: [replica(1, 96, 500)] },
  });
  const node = renderResourcePressure(resources);
  assert.match(node.textContent, /Critical/);
  assert.match(node.textContent, /Replica 2/);
  const link = node.querySelector('.ov-hotspot-name');
  assert.equal(link.getAttribute('href'), '/apps/forecast/configuration');
  assert.equal(link.dataset.focusKey, 'pressure:forecast:memory');
  assert.match(node.querySelector('.ov-hotspot-row meter').getAttribute('aria-label'), /98 percent, critical/);
});

test('overflow alerts are disclosed with an operable view-all control', () => {
  const apps = Array.from({ length: 6 }, (_, i) => ({ slug: `app-${i}`, status: 'running' }));
  const metrics = Object.fromEntries(apps.map((app) => [app.slug, {
    status: 'running', replicas: [replica(0, 90, 100, 110, 100)],
  }]));
  const node = renderResourcePressure(resourcesFor(apps, metrics));
  const toggle = node.querySelector('.ov-res-toggle');
  assert.match(toggle.textContent, /View all 12 pressure alerts/);
  assert.equal(node.querySelectorAll('.ov-hotspot-row:not([hidden])').length, 4);
  toggle.click();
  assert.equal(toggle.getAttribute('aria-expanded'), 'true');
  assert.equal(node.querySelectorAll('.ov-hotspot-row:not([hidden])').length, 12);
});

test('partial coverage uses explicit copy instead of an all-clear', () => {
  const resources = resourcesFor([{ slug: 'limited', status: 'running' }, { slug: 'free', status: 'running' }], {
    limited: { status: 'running', replicas: [replica(0, 20, 100)] },
    free: { status: 'running', replicas: [unlimited(0, 20, 100)] },
  });
  const node = renderResourcePressure(resources);
  assert.match(node.textContent, /1 of 2 running replicas covered/);
  assert.match(node.textContent, /1 unlimited/);
  assert.doesNotMatch(node.textContent, /All covered replicas/);
});

// The panel a fleet with no limits set actually gets: usage against the box,
// never a row that reports its own capacity as unavailable.
test('a fleet with no limits is measured against the host, not reported as immeasurable', () => {
  const resources = resourcesFor(
    [{ slug: 'forecast', name: 'Forecast', status: 'running' }],
    { forecast: { status: 'running', replicas: [unlimited(0, 150, 2048)] } },
    { cores: 4, cores_source: 'affinity', memory_mb: 8192, memory_source: 'host-total' },
  );
  const node = renderResourcePressure(resources);
  assert.match(node.textContent, /Fleet resource usage/);
  assert.match(node.textContent, /No per-app limits set · measured against host capacity/);
  assert.match(node.textContent, /1\.5 cores \/ 4\.0 cores/);
  assert.match(node.textContent, /2\.0 GB \/ 8\.0 GB/);
  assert.match(node.textContent, /Across 1 running replica/);
  assert.doesNotMatch(node.textContent, /Capacity unavailable/);
  assert.doesNotMatch(node.textContent, /Trend unavailable/);
  assert.doesNotMatch(node.textContent, /unlimited|not enforced/);

  const meters = node.querySelectorAll('meter');
  assert.equal(meters.length, 2);
  assert.match(meters[0].getAttribute('aria-label'), /CPU allocation, 38 percent of host capacity/);
  assert.equal(Number(meters[1].value.toFixed(2)), 0.25);
});

test('an unmeasurable host reports live usage rather than an invented percentage', () => {
  const resources = resourcesFor([{ slug: 'forecast', name: 'Forecast', status: 'running' }], {
    forecast: { status: 'running', replicas: [unlimited(0, 150, 2048)] },
  });
  const node = renderResourcePressure(resources);
  assert.match(node.textContent, /No per-app limits set · host capacity unknown/);
  assert.match(node.textContent, /1\.5 cores in use/);
  assert.match(node.textContent, /2\.0 GB in use/);
  assert.match(node.textContent, /Live/);
  assert.doesNotMatch(node.textContent, /Capacity unavailable/);
  // No denominator means no meter and no percentage: the value is the whole
  // truth, and a bar would need a full length it cannot justify.
  assert.equal(node.querySelector('meter'), null);
  assert.doesNotMatch(node.textContent, /\d+%/);
});

test('a no-limit fleet attributes its usage to the apps behind it', () => {
  const sizes = [['alpha', 400], ['beta', 300], ['gamma', 200], ['delta', 100]];
  const resources = resourcesFor(
    sizes.map(([slug]) => ({ slug, name: slug, status: 'running' })),
    Object.fromEntries(sizes.map(([slug, mb]) => [slug, { status: 'running', replicas: [unlimited(0, 10, mb)] }])),
    { cores: 4, cores_source: 'affinity', memory_mb: 8192, memory_source: 'host-total' },
  );
  const node = renderResourcePressure(resources);
  assert.match(node.textContent, /Top memory consumers/);
  const rows = [...node.querySelectorAll('.ov-consumer-row')];
  assert.equal(rows.length, 3);
  assert.equal(rows[0].querySelector('a').getAttribute('href'), '/apps/alpha/configuration');
  assert.match(rows[0].textContent, /400 MB/);
  assert.match(rows[0].textContent, /40% of fleet/);
  // Attribution, not alarm: no pressure alerts and no all-clear about limits
  // that were never set.
  assert.equal(node.querySelector('.ov-hotspot-row'), null);
  assert.doesNotMatch(node.textContent, /All covered replicas/);
});

test('a single app is not ranked against itself', () => {
  const resources = resourcesFor([{ slug: 'only', name: 'Only', status: 'running' }], {
    only: { status: 'running', replicas: [unlimited(0, 10, 100)] },
  });
  const node = renderResourcePressure(resources);
  assert.equal(node.querySelector('.ov-consumer-row'), null);
  assert.doesNotMatch(node.textContent, /Top memory consumers/);
});

test('host-scale pressure keeps the severity treatment a full box deserves', () => {
  const resources = resourcesFor(
    [{ slug: 'hog', name: 'Hog', status: 'running' }],
    { hog: { status: 'running', replicas: [unlimited(0, 10, 7900)] } },
    { cores: 4, cores_source: 'affinity', memory_mb: 8192, memory_source: 'host-total' },
  );
  const node = renderResourcePressure(resources);
  const memoryRow = node.querySelectorAll('.ov-capacity-row')[1];
  assert.ok(memoryRow.classList.contains('ov-capacity-row--critical'));
  assert.match(memoryRow.textContent, /Critical/);
  assert.ok(memoryRow.querySelector('.ov-capacity-meter--critical'));
});

test('poll replacement preserves focus on a stable pressure link', () => {
  const apps = [{ slug: 'forecast', name: 'Forecast', status: 'running' }];
  const metrics = { forecast: { status: 'running', replicas: [replica(0, 96, 500)] } };
  const body = document.createElement('div');
  document.body.appendChild(body);
  replaceOverviewContent(body, renderResourcePressure(resourcesFor(apps, metrics)));
  const link = body.querySelector('[data-focus-key="pressure:forecast:memory"]');
  link.focus();
  assert.equal(document.activeElement, link);

  replaceOverviewContent(body, renderResourcePressure(resourcesFor(apps, metrics)));
  assert.equal(document.activeElement.dataset.focusKey, 'pressure:forecast:memory');
});

test('poll replacement preserves an expanded alert disclosure and its focus', () => {
  const apps = Array.from({ length: 6 }, (_, i) => ({ slug: `app-${i}`, status: 'running' }));
  const metrics = Object.fromEntries(apps.map((app) => [app.slug, {
    status: 'running', replicas: [replica(0, 90, 100, 110, 100)],
  }]));
  const body = document.createElement('div');
  document.body.appendChild(body);
  replaceOverviewContent(body, renderResourcePressure(resourcesFor(apps, metrics)));
  const toggle = body.querySelector('[data-disclosure-key="pressure-alerts"]');
  toggle.click();
  toggle.focus();

  replaceOverviewContent(body, renderResourcePressure(resourcesFor(apps, metrics)));
  const restored = body.querySelector('[data-disclosure-key="pressure-alerts"]');
  assert.equal(restored.getAttribute('aria-expanded'), 'true');
  assert.equal(body.querySelectorAll('.ov-hotspot-row:not([hidden])').length, 12);
  assert.equal(document.activeElement, restored);
});

test('live signature and announcement identify a warning that moves between apps', () => {
  const first = resourcesFor([{ slug: 'alpha', name: 'Alpha', status: 'running' }], {
    alpha: { status: 'running', replicas: [replica(0, 90, 64)] },
  });
  const second = resourcesFor([{ slug: 'beta', name: 'Beta', status: 'running' }], {
    beta: { status: 'running', replicas: [replica(1, 90, 64)] },
  });
  assert.notEqual(resourceLiveSignature(first), resourceLiveSignature(second));
  assert.match(resourceLiveSummary(second), /Beta, cpu, replica 2, warning/);
});

test('live announcement identifies a changed secondary hotspot, not the unchanged hottest one', () => {
  const apps = [
    { slug: 'alpha', name: 'Alpha', status: 'running' },
    { slug: 'beta', name: 'Beta', status: 'running' },
    { slug: 'gamma', name: 'Gamma', status: 'running' },
  ];
  const first = resourcesFor(apps, {
    alpha: { status: 'running', replicas: [replica(0, 96, 64)] },
    beta: { status: 'running', replicas: [replica(0, 90, 64)] },
    gamma: { status: 'running', replicas: [replica(0, 10, 64)] },
  });
  const second = resourcesFor(apps, {
    alpha: { status: 'running', replicas: [replica(0, 96, 64)] },
    beta: { status: 'running', replicas: [replica(0, 10, 64)] },
    gamma: { status: 'running', replicas: [replica(1, 90, 64)] },
  });
  const summary = resourceLiveSummary(second, first);
  assert.match(summary, /Gamma, cpu, replica 2, warning/);
  assert.doesNotMatch(summary, /Alpha/);
});
