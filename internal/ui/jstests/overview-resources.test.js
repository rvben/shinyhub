import { afterEach, beforeEach, test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { installPopoverStub } from './popover-stub.js';
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
  installPopoverStub(dom.window);
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

test('2 GB of app limits remains distinct from a 64 GB host and offers a coverage action', () => {
  const resources = resourcesFor([{ slug: 'limited', status: 'running' }, { slug: 'free', name: 'Free app', status: 'running' }], {
    limited: { replicas: [replica(0, 10, 512, 2048)] },
    free: { replicas: [unlimited(0, 10, 128)] },
  }, { memory_mb: 65536, memory_source: 'host-total' });
  const node = renderResourcePressure(resources, { canManageApp: () => true });
  assert.match(node.textContent, /512 MB \/ 2\.0 GB/);
  assert.match(node.textContent, /25% of combined app limits/);
  assert.equal(node.querySelectorAll('.ov-resource-values')[1].querySelector('dd').textContent, '640 MB');
  assert.match(node.querySelectorAll('.ov-resource-values')[1].textContent, /Host capacity · OS total64\.0 GB/);
  const details = node.querySelector('.ov-measurement-details');
  assert.equal(node.querySelector('[data-disclosure-key="capacity:help"]').getAttribute('aria-expanded'), 'false');
  assert.doesNotMatch(details.textContent, /combined app limits/);
  assert.match(node.querySelectorAll('.ov-app-limit-reading')[1].textContent, /25% of combined app limits/);
  assert.equal(node.querySelector('.ov-app-limit-reading').closest('[popover]'), null);
  node.querySelector('[data-disclosure-key="capacity:help"]').click();
  assert.equal(details.dataset.testPopoverOpen, 'true');
  const gap = node.querySelector('.ov-coverage-row');
  assert.match(gap.textContent, /Free app.*No limit set.*CPU: 1 replica · Memory: 1 replica/);
  assert.equal(gap.querySelector('a').getAttribute('href'), '/apps/free/configuration');
  assert.match(gap.querySelector('a').textContent, /Review settings/);
});

test('container and configured capacity identify the source and the setting to check', () => {
  for (const source of ['cgroup-limit', 'config']) {
    const resources = resourcesFor([{ slug: 'a', status: 'running' }], {
      a: { replicas: [unlimited(0, 10, 128)] },
    }, { memory_mb: 2048, memory_source: source });
    const node = renderResourcePressure(resources);
    assert.match(node.querySelectorAll('.ov-resource-values')[1].textContent, source === 'config'
      ? /Configured capacity2\.0 GB/
      : /Container \/ service capacity2\.0 GB/);
    assert.match(node.querySelector('[popover]').textContent, source === 'config'
      ? /server.host_capacity_memory_mb/
      : /restricted by a container or service/);
    assert.doesNotMatch(node.textContent, /OS total/);
  }
});

test('idle measurement help pairs every label with content without empty metric sections or app-limit claims', () => {
  const node = renderResourcePressure(resourcesFor([], {}, {
    cores: 12, cores_source: 'affinity', memory_mb: 16384, memory_source: 'host-total',
  }));
  const toggle = node.querySelector('[data-disclosure-key="capacity:help"]');
  assert.equal(toggle.getAttribute('aria-label'), 'About these metrics');
  toggle.click();
  const details = node.querySelector('.ov-measurement-details');
  assert.equal(details.dataset.testPopoverOpen, 'true');
  assert.equal(details.querySelector('h3').textContent, 'About these metrics');
  assert.deepEqual([...details.querySelectorAll('dt')].map((term) => term.textContent),
    ['Measured usage', 'CPU capacity', 'Memory capacity', 'Memory (RSS)']);
  for (const row of details.querySelector('dl').children) {
    assert.equal(row.firstElementChild.tagName, 'DT');
    assert.equal(row.lastElementChild.tagName, 'DD');
    assert.ok(row.lastElementChild.textContent.trim());
  }
  assert.match(details.textContent, /not free memory or idle CPU/);
  assert.match(details.textContent, /Shared memory pages may be counted more than once/);
  assert.doesNotMatch(details.textContent, /replicas checked|Trend unavailable/);
});

test('missing measurements lead to app inspection without suggesting a limit change', () => {
  const resources = resourcesFor([{ slug: 'a', status: 'running' }], {
    a: { replicas: [{ ...replica(0, 10, 128), rss_bytes: null }] },
  });
  const node = renderResourcePressure(resources, { canManageApp: () => true });
  const gap = node.querySelector('.ov-coverage-row');
  assert.match(gap.textContent, /No measurement.*Memory: 1 replica/);
  assert.doesNotMatch(node.textContent, /App limits: 0/);
  assert.equal(gap.querySelector('a').getAttribute('href'), '/apps/a');
  assert.doesNotMatch(gap.textContent, /Set a limit|Review limits/);
});

test('coverage actions respect app management permissions and escape app text', () => {
  const resources = resourcesFor([{ slug: 'limited', status: 'running' }, { slug: 'free', name: '<script>bad</script>', status: 'running' }], {
    limited: { replicas: [replica(0, 10, 128)] }, free: { replicas: [unlimited(0, 10, 128)] },
  });
  const node = renderResourcePressure(resources);
  assert.equal(node.querySelector('.ov-coverage-row a').getAttribute('href'), '/apps/free');
  assert.equal(node.querySelector('script'), null);
});

test('expanded coverage and focused actions survive a metrics poll', () => {
  const apps = Array.from({ length: 7 }, (_, i) => ({ slug: `a${i}`, status: 'running' }));
  const resources = resourcesFor(apps, Object.fromEntries(apps.map((app, i) => [app.slug, {
    replicas: [i === 0 ? replica(0, 10, 128) : unlimited(0, 10, 128)],
  }])));
  const body = document.createElement('div');
  document.body.appendChild(body);
  replaceOverviewContent(body, renderResourcePressure(resources));
  body.querySelector('[data-disclosure-key="coverage-gaps"]').click();
  const link = body.querySelector('.ov-coverage-row:last-child a');
  link.focus();
  const key = link.dataset.focusKey;
  replaceOverviewContent(body, renderResourcePressure(resources));
  assert.equal(body.querySelectorAll('.ov-coverage-row:not([hidden])').length, 6);
  assert.equal(document.activeElement.dataset.focusKey, key);
});

test('coverage focus and expanded advice survive changing counts and new priority gaps', () => {
  const apps = ['limited', 'a', 'b', 'c', 'free'].map((slug) => ({ slug, status: 'running' }));
  const metrics = Object.fromEntries(apps.map((app) => [app.slug, { replicas: [app.slug === 'limited' ? replica(0, 10, 128) : unlimited(0, 10, 128)] }]));
  const body = document.createElement('div');
  document.body.appendChild(body);
  replaceOverviewContent(body, renderResourcePressure(resourcesFor(apps, metrics)));
  const key = 'coverage:free:unlimited';
  const advice = body.querySelector('[data-disclosure-key="coverage-help:free:unlimited"]');
  advice.click();
  body.querySelector(`[data-focus-key="${key}"]`).focus();
  metrics.free.replicas.push(unlimited(1, 10, 128));
  apps.push({ slug: 'missing', status: 'running' });
  metrics.missing = { replicas: [{ ...unlimited(0, 10, 128), metrics_available: false }] };
  replaceOverviewContent(body, renderResourcePressure(resourcesFor(apps, metrics)));
  assert.equal(document.activeElement.dataset.focusKey, key);
  assert.equal(document.activeElement.closest('li').hidden, false);
  assert.match(document.activeElement.closest('li').textContent, /CPU: 2 replicas · Memory: 2 replicas/);
  assert.equal(body.querySelector('[data-disclosure-key="coverage-help:free:unlimited"]').getAttribute('aria-expanded'), 'true');
});

test('different missing replicas retain per-metric counts without implying shared identity', () => {
  const resources = resourcesFor([{ slug: 'a', status: 'running' }], {
    a: { replicas: [{ ...replica(0, 10, 128), cpu_percent: null }, { ...replica(1, 10, 128), rss_bytes: null }] },
  });
  const node = renderResourcePressure(resources);
  assert.match(node.querySelector('.ov-coverage-counts').textContent, /^CPU: 1 replica · Memory: 1 replica$/);
  assert.equal(node.querySelectorAll('.ov-coverage-row').length, 1);
});

test('unenforced limits offer host recovery guidance without a misleading settings action', () => {
  const resources = resourcesFor([{ slug: 'a', status: 'running' }, { slug: 'b', status: 'running' }], {
    a: { replicas: [replica(0, 10, 128)] }, b: { replicas: [{ ...replica(0, 10, 128), memory_limit_enforced: false }] },
  });
  const node = renderResourcePressure(resources, { canManageApp: () => true });
  const row = node.querySelector('.ov-coverage-row');
  assert.match(row.textContent, /Limit not enforced · host action/);
  assert.equal(row.querySelector('[data-nav]'), null);
  assert.equal(row.querySelector('.ov-resource-help').hidden, true);
  row.querySelector('button').click();
  assert.equal(row.querySelector('.ov-resource-help').hidden, false);
  assert.match(row.textContent, /Delegate=cpu memory pids/);
  assert.equal(row.querySelector('a').hostname, 'shinyhub.dev');
});

test('documentation link focus survives a resource poll', () => {
  const resources = resourcesFor([{ slug: 'a', status: 'running' }, { slug: 'b', status: 'running' }], {
    a: { replicas: [replica(0, 10, 128)] }, b: { replicas: [{ ...replica(0, 10, 128), memory_limit_enforced: false }] },
  });
  const body = document.createElement('div');
  document.body.appendChild(body);
  for (const [disclosureKey, focusKey] of [['capacity:help', 'capacity:guide'], ['coverage-help:b:unenforced', 'coverage-guide:b:unenforced']]) {
    replaceOverviewContent(body, renderResourcePressure(resources));
    body.querySelector(`[data-disclosure-key="${disclosureKey}"]`).click();
    body.querySelector(`[data-focus-key="${focusKey}"]`).focus();
    replaceOverviewContent(body, renderResourcePressure(resources));
    assert.equal(document.activeElement.dataset.focusKey, focusKey);
    assert.equal(document.activeElement.closest('.ov-resource-help').hidden, false);
    assert.equal(document.activeElement.closest('[hidden]'), null);
  }
});

test('focused pressure alert remains visible when a hotter alert pushes it into overflow', () => {
  const apps = Array.from({ length: 5 }, (_, i) => ({ slug: `app-${i}`, status: 'running' }));
  const metrics = Object.fromEntries(apps.map((app, i) => [app.slug, { replicas: [replica(0, i < 4 ? 90 : 10, 64)] }]));
  const body = document.createElement('div');
  document.body.appendChild(body);
  replaceOverviewContent(body, renderResourcePressure(resourcesFor(apps, metrics)));
  body.querySelector('[data-focus-key="pressure:app-3:cpu"]').focus();
  metrics['app-4'].replicas[0].cpu_percent = 99;
  replaceOverviewContent(body, renderResourcePressure(resourcesFor(apps, metrics)));
  assert.equal(document.activeElement.dataset.focusKey, 'pressure:app-3:cpu');
  assert.equal(document.activeElement.closest('li').hidden, false);
  assert.equal(body.querySelector('[data-disclosure-key="pressure-alerts"]').getAttribute('aria-expanded'), 'true');
});

test('host recovery remains available when every configured limit is unenforced', () => {
  const resources = resourcesFor([{ slug: 'a', status: 'running' }], {
    a: { replicas: [{ ...replica(0, 10, 128), memory_limit_enforced: false, cpu_quota_enforced: false }] },
  }, { cores: 4, memory_mb: 8192 });
  const node = renderResourcePressure(resources);
  assert.equal(resources.state, 'partial');
  assert.equal(resources.memory.usage.state, 'ready');
  assert.match(node.querySelector('.ov-resource-values').textContent, /Measured app usage0\.1 cores/);
  assert.match(node.querySelector('.ov-coverage-row').textContent, /Limit not enforced/);
  assert.equal(node.querySelector('.ov-coverage-row [data-nav]'), null);
});

test('host capacity remains visible during idle and unavailable states without invented usage', () => {
  for (const state of ['idle', 'unavailable']) {
    const apps = state === 'idle' ? [] : [{ slug: 'a', status: 'running' }];
    const resources = buildOverviewModel(apps, { state: state === 'idle' ? 'ready' : state, metrics: {}, host: { cores: 16, memory_mb: 65536 } }).resources;
    const node = renderResourcePressure(resources);
    assert.match(node.textContent, /64\.0 GB/);
    assert.equal(node.querySelector('meter'), null);
    assert.doesNotMatch(node.textContent, /0 KB|0%/);
    const values = [...node.querySelectorAll('.ov-resource-values dd')].map((el) => el.textContent);
    assert.equal(values[0], state === 'idle' ? '—' : 'Unavailable');
  }
});

test('resource renderer shows stable capacity ratios, semantic meters, labels, and coverage', () => {
  const resources = resourcesFor([{ slug: 'forecast', name: 'Forecast', status: 'running' }], {
    forecast: { status: 'running', replicas: [replica(0, 43, 256)] },
  }, { cores: 4, memory_mb: 8192 });
  const node = renderResourcePressure(resources);
  document.body.appendChild(node);

  assert.equal(node.getAttribute('aria-labelledby'), 'ov-resources-title');
  assert.match(node.textContent, /0\.43 \/ 1 cores/);
  assert.match(node.textContent, /256 MB \/ 512 MB/);
  assert.match(node.textContent, /1 of 1 replicas checked/);
  const meters = node.querySelectorAll('meter');
  assert.equal(meters.length, 2);
  assert.equal(meters[0].high, 0.9);
  assert.match(meters[0].getAttribute('aria-label'), /CPU reported app usage, 11%/);
});

test('small real CPU loads retain enough precision to avoid a false zero', () => {
  const resources = resourcesFor([{ slug: 'small', status: 'running' }], {
    small: { status: 'running', replicas: [replica(0, 4, 64)] },
  });
  const node = renderResourcePressure(resources);
  assert.match(node.textContent, /0\.04 \/ 1 cores/);
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

test('hotspot rows name severity, metric, replica, and inspection target', () => {
  const resources = resourcesFor([{ slug: 'forecast', name: 'Forecast', status: 'running' }], {
    forecast: { status: 'running', replicas: [replica(1, 96, 500)] },
  });
  const node = renderResourcePressure(resources);
  assert.match(node.textContent, /Critical/);
  assert.match(node.textContent, /Replica 2/);
  const link = node.querySelector('.ov-hotspot-name');
  assert.equal(link.getAttribute('href'), '/apps/forecast/overview');
  assert.equal(link.dataset.focusKey, 'pressure:forecast:memory');
  assert.match(node.querySelector('.ov-hotspot-row meter').getAttribute('aria-label'), /98 percent, critical/);
});

test('overflow alerts are disclosed with an operable view-all control', () => {
  const apps = Array.from({ length: 6 }, (_, i) => ({ slug: `app-${i}`, status: 'running' }));
  const metrics = Object.fromEntries(apps.map((app) => [app.slug, {
    status: 'running', replicas: [replica(0, 90, 100, 110, 100)],
  }]));
  const node = renderResourcePressure(resourcesFor(apps, metrics));
  const toggle = node.querySelector('[data-disclosure-key="pressure-alerts"]');
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
  assert.match(node.textContent, /1 of 2 replicas checked/);
  assert.match(node.textContent, /No limit set · optional/);
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
  assert.match(node.textContent, /App resource usage/);
  assert.match(node.querySelector('.ov-res-scope').textContent, /Reporting apps only. Other host processes are excluded/);
  assert.match(node.textContent, /1\.5 coresCPU capacity · Allowed cores4\.0 cores/);
  assert.match(node.textContent, /2\.0 GBHost capacity · OS total8\.0 GB/);
  assert.match(node.textContent, /1 of 1 replicas reporting/);
  assert.doesNotMatch(node.textContent, /Capacity unavailable/);
  assert.doesNotMatch(node.textContent, /Trend unavailable/);
  assert.doesNotMatch(node.textContent, /unlimited|not enforced/);

  const meters = node.querySelectorAll('meter');
  assert.equal(meters.length, 2);
  assert.match(meters[0].getAttribute('aria-label'), /CPU reported app usage, 38% of cpu capacity · allowed cores/);
  assert.equal(Number(meters[1].value.toFixed(2)), 0.25);
});

test('an unmeasurable host reports live usage rather than an invented percentage', () => {
  const resources = resourcesFor([{ slug: 'forecast', name: 'Forecast', status: 'running' }], {
    forecast: { status: 'running', replicas: [unlimited(0, 150, 2048)] },
  });
  const node = renderResourcePressure(resources);
  assert.match(node.textContent, /Host capacityUnknown/);
  assert.match(node.textContent, /Measured app usage1\.5 cores/);
  assert.match(node.textContent, /Measured app usage2\.0 GB/);
  assert.match(node.querySelector('.ov-res-updated').textContent, /Updated just now/);
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
  assert.equal(rows[0].querySelector('a').getAttribute('href'), '/apps/alpha/overview');
  assert.match(rows[0].textContent, /400 MB/);
  assert.match(rows[0].textContent, /40% of measured usage/);
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
  assert.match(memoryRow.textContent, /Near capacity/);
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
  assert.match(resourceLiveSummary(second), /Beta, cpu, replica 2, critical/);
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
  assert.match(summary, /Gamma, cpu, replica 2, critical/);
  assert.doesNotMatch(summary, /Alpha/);
});


test('metrics help close returns focus and native dismissal keeps the trigger state in sync', () => {
  const node = renderResourcePressure(resourcesFor([], {}));
  document.body.appendChild(node);
  const toggle = node.querySelector('[data-focus-key="capacity:help"]');
  const help = node.querySelector('[popover]');
  toggle.click();
  const close = help.querySelector('button');
  close.focus();
  close.click();
  assert.equal(toggle.getAttribute('aria-expanded'), 'false');
  assert.equal(document.activeElement, toggle);
  toggle.click();
  help.hidePopover(); // Native Escape or light-dismiss fires the same event.
  assert.equal(toggle.getAttribute('aria-expanded'), 'false');
});

test('browsers without popovers get a working documentation link', () => {
  delete dom.window.HTMLElement.prototype.showPopover;
  const node = renderResourcePressure(resourcesFor([], {}));
  const link = node.querySelector('.ov-metrics-help-link');
  assert.equal(link.textContent, 'About these metrics');
  assert.equal(link.href, 'https://shinyhub.dev/configuration/#host-capacity');
  assert.equal(node.querySelector('[popover]'), null);
});


test('tiny measured CPU stays nonzero and units remain secondary', () => {
  const resources = resourcesFor([{ slug: 'small', status: 'running' }], {
    small: { status: 'running', replicas: [unlimited(0, 0.138, 14)] },
  }, { cores: 12, memory_mb: 16384 });
  const node = renderResourcePressure(resources);
  const value = node.querySelector('.ov-capacity-value');
  assert.equal(value.textContent, '<0.01 cores');
  assert.equal(value.querySelector('.metric-number').textContent, '<0.01');
  assert.equal(value.querySelector('.metric-unit').textContent, ' cores');
});
