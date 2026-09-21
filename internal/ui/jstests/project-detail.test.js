import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { mountProjectDetail, projectDetailModel } from '../static/views/project-detail.js';

const appSource = readFileSync(new URL('../static/app.js', import.meta.url), 'utf8');

function fixture() {
  const dom = new JSDOM(`<!doctype html><body>
    <section id="project-detail-view" hidden>
      <span id="project-detail-breadcrumb"></span>
      <div id="project-detail-icon"></div>
      <h1 id="project-detail-heading">Project</h1>
      <span id="project-detail-count"></span>
      <code id="project-detail-slug"></code>
      <p id="project-detail-description"></p>
      <button id="project-detail-edit" hidden></button>
      <button id="project-detail-add" hidden></button>
      <button id="project-detail-refresh"></button>
      <p id="project-detail-loading"></p>
      <p id="project-detail-error" hidden></p>
      <div id="project-detail-grid"></div>
      <div id="project-detail-empty" hidden>
        <button id="project-detail-empty-add" hidden></button>
      </div>
    </section>
  </body>`, { url: 'http://localhost/projects/analytics' });
  global.document = dom.window.document;
  global.location = dom.window.location;
  global.CustomEvent = dom.window.CustomEvent;
  return dom;
}

function ok(items) {
  return { ok: true, status: 200, json: async () => ({ items }) };
}

test('projectDetailModel keeps only apps in the requested project', () => {
  const model = projectDetailModel(
    { slug: 'analytics', name: 'Analytics', description: 'Decision dashboards', icon_emoji: '📊' },
    [
      { slug: 'sales', project_slug: 'analytics' },
      { slug: 'finance', project_slug: 'finance' },
      { slug: 'retention', project_slug: ' analytics ' },
    ],
  );
  assert.equal(model.name, 'Analytics');
  assert.equal(model.countLabel, '2 apps');
  assert.deepEqual(model.apps.map((app) => app.slug), ['sales', 'retention']);
});

test('project page renders its metadata, app cards, and project-scoped actions', async () => {
  fixture();
  const rendered = [];
  const targets = [];
  const openedProjects = [];
  const openedCreators = [];
  const project = {
    slug: 'analytics', name: 'Analytics', description: 'Decision dashboards',
    icon_emoji: '📊', app_count: 2,
  };
  const apps = [
    { slug: 'sales', name: 'Sales', project_slug: 'analytics' },
    { slug: 'finance', name: 'Finance', project_slug: 'finance' },
  ];
  const ctx = {
    state: { user: { role: 'admin' }, canCreateApps: true, apps: [] },
    api: async (path) => path === '/api/projects' ? ok([project]) : ok(apps),
    metrics: { setTargets(value) { targets.push(value); } },
    onUnauthorized() {},
    updateActiveNav() {},
    syncSidebar() {},
    renderGridVerbatim(groups) { rendered.push(groups); },
    openProjectEditModal(group) { openedProjects.push(group); },
    openNewAppModal(slug) { openedCreators.push(slug); },
  };

  const view = await mountProjectDetail(ctx)({ slug: 'analytics' });
  assert.equal(view.title, 'Analytics');
  assert.equal(document.getElementById('project-detail-heading').textContent, 'Analytics');
  assert.equal(document.getElementById('project-detail-description').textContent, 'Decision dashboards');
  assert.equal(document.getElementById('project-detail-count').textContent, '1 app');
  assert.equal(document.getElementById('project-detail-edit').hidden, false);
  assert.equal(document.getElementById('project-detail-add').hidden, false);
  assert.equal(rendered.length, 1);
  assert.deepEqual(rendered[0][0].apps.map((app) => app.slug), ['sales']);
  assert.deepEqual(targets.at(-1), ['sales']);

  document.getElementById('project-detail-edit').click();
  document.getElementById('project-detail-add').click();
  assert.equal(openedProjects[0].project, 'analytics');
  assert.deepEqual(openedCreators, ['analytics']);
  view.unmount();
  assert.deepEqual(targets.at(-1), []);
});

test('empty projects get a useful empty state without invoking the card grid', async () => {
  fixture();
  let rendered = false;
  const ctx = {
    state: { user: { role: 'developer' }, canCreateApps: false, apps: [] },
    api: async (path) => path === '/api/projects'
      ? ok([{ slug: 'empty', name: '', description: '', icon_emoji: '', app_count: 0 }])
      : ok([]),
    metrics: { setTargets() {} },
    onUnauthorized() {},
    updateActiveNav() {},
    syncSidebar() {},
    renderGridVerbatim() { rendered = true; },
    openProjectEditModal() {},
    openNewAppModal() {},
  };
  const view = await mountProjectDetail(ctx)({ slug: 'empty' });
  assert.equal(view.title, 'empty');
  assert.equal(document.getElementById('project-detail-empty').hidden, false);
  assert.equal(document.getElementById('project-detail-edit').hidden, true);
  assert.equal(document.getElementById('project-detail-add').hidden, true);
  assert.equal(rendered, false);
  view.unmount();
});

test('project actions stay unavailable when the initial project request fails', async () => {
  fixture();
  const ctx = {
    state: { user: { role: 'admin' }, canCreateApps: true, apps: [] },
    api: async () => { throw new Error('offline'); },
    metrics: { setTargets() {} },
    onUnauthorized() {},
    updateActiveNav() {},
    syncSidebar() {},
    renderGridVerbatim() {},
    openProjectEditModal() {},
    openNewAppModal() {},
  };

  const view = await mountProjectDetail(ctx)({ slug: 'analytics' });
  assert.equal(document.getElementById('project-detail-error').hidden, false);
  assert.equal(document.getElementById('project-detail-edit').hidden, true);
  assert.equal(document.getElementById('project-detail-add').hidden, true);
  assert.equal(document.getElementById('project-detail-empty-add').hidden, true);
  view.unmount();
});

test('a completed app reload refreshes the mounted project cards', async () => {
  fixture();
  let apps = [{ slug: 'sales', name: 'Sales', project_slug: 'analytics', deploy_count: 0 }];
  const rendered = [];
  const ctx = {
    state: { user: { role: 'admin' }, canCreateApps: true, apps: [] },
    api: async (path) => path === '/api/projects'
      ? ok([{ slug: 'analytics', name: 'Analytics' }])
      : ok(apps),
    metrics: { setTargets() {} },
    onUnauthorized() {},
    updateActiveNav() {},
    syncSidebar() {},
    renderGridVerbatim(groups) { rendered.push(groups); },
    openProjectEditModal() {},
    openNewAppModal() {},
  };

  const view = await mountProjectDetail(ctx)({ slug: 'analytics' });
  apps = [{ slug: 'sales', name: 'Sales', project_slug: 'analytics', deploy_count: 1 }];
  document.dispatchEvent(new CustomEvent('shinyhub:apps-reloaded'));
  await new Promise((resolve) => setTimeout(resolve, 0));

  assert.equal(rendered.length, 2);
  assert.equal(rendered.at(-1)[0].apps[0].deploy_count, 1);
  view.unmount();
});

test('global New app triggers do not pass their click event as a project slug', () => {
  assert.match(appSource, /newAppButton\.addEventListener\('click', \(\) => openNewAppModal\(\)\)/);
  assert.match(appSource, /emptyStateCTA\.addEventListener\('click', \(\) => openNewAppModal\(\)\)/);
  assert.match(appSource, /CustomEvent\('shinyhub:apps-reloaded'\)/);
});
