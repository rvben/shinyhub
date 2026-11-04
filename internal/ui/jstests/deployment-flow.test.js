// Exercise the production event bindings with an isolated transport. No files
// leave this process; protocol parsing is tested in deployment-upload.test.js.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';
import { deploymentOutcome } from '../static/views/deployment-upload.js';
import { renderDeployResult, formatManifestSummary } from '../static/deploy-summary.js';
const source = readFileSync(new URL('../static/app.js', import.meta.url), 'utf8');
const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
const slice = (start, end) => source.slice(source.indexOf(start), source.indexOf(end, source.indexOf(start)));
const flush = () => new Promise(r => setImmediate(r));
function fixture(t) {
  const dom = new JSDOM(html, { url: 'http://localhost/apps/example' }); t.after(() => dom.window.close());
  const doc = dom.window.document; let resolve, reject, aborted = 0, options, route, opened;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  const context = vm.createContext({ document: doc, window: dom.window, console,
    modalTrap: () => ({ activate() {}, release() {} }),
    setError: (el, message) => { el.textContent = message; el.hidden = !message; },
    flashToast() {}, readCookie: () => '', handleUnauthorized() {},
    uploadDeployment: value => { options = value; return { promise, xhr: { abort() { aborted++; } } }; },
    deploymentOutcome, renderDeployResult, formatManifestSummary, loadApps: async () => {},
    router: { navigate: value => { route = value; } }, location: { assign: value => { opened = value; } },
  });
  vm.runInContext('let deployState = null;\n' + slice('  const deployModal        =', '\n  const deployCliCopyStatus') + '\n' +
    slice('  function updateDeployMonitor(', '  function renderDeploySummary(') + '\n' +
    slice("  deploySubmit.addEventListener('click'", '\n  deployModalClose.addEventListener'), context);
  const run = code => vm.runInContext(code, context);
  run("openDeployModal({slug:'example',name:'Example'}); deployState.blob = {}; deploySubmit.disabled = false;");
  return { doc, run, resolve, reject, aborted: () => aborted, options: () => options, route: () => route, opened: () => opened };
}
test('hiding an active deployment preserves its request and progress can be reopened', async t => {
  const f = fixture(t); f.doc.querySelector('#deploy-submit').click();
  f.options().onPhase({ phase: 'dependencies', status: 'started', message: 'Installing dependencies' });
  assert.equal(f.doc.querySelector('progress#deploy-progress-bar').hasAttribute('value'), false);
  f.run('closeDeployModal()'); assert.equal(f.aborted(), 0); assert.equal(f.doc.querySelector('#deploy-modal').hidden, true);
  assert.equal(f.doc.querySelector('#deploy-monitor').hidden, false);
  f.doc.querySelector('#deploy-monitor-open').click(); assert.equal(f.doc.querySelector('#deploy-modal').hidden, false);
  f.resolve({ status: 'running' }); await flush(); assert.equal(f.doc.querySelector('#deploy-submit').textContent, 'Open app');
});
test('stopped deployments finish with View app and never auto-start the app', async t => {
  const f = fixture(t); f.doc.querySelector('#deploy-submit').click();
  f.resolve({ status: 'stopped', kept_stopped: true, warning: 'Setup requires attention' }); await flush();
  assert.match(f.doc.querySelector('#deploy-stage').textContent, /remains stopped/);
  assert.match(f.doc.querySelector('#deploy-result').textContent, /Setup requires attention/);
  f.doc.querySelector('#deploy-submit').click(); assert.equal(f.route(), '/apps/example'); assert.equal(f.opened(), undefined);
});
test('uncertain outcomes offer history, and selecting a new bundle clears the old outcome', async t => {
  const f = fixture(t); f.doc.querySelector('#deploy-submit').click();
  f.reject(Object.assign(Error('Check deployment history before deploying again'), { uncertain: true })); await flush();
  assert.equal(f.doc.querySelector('#deploy-submit').textContent, 'Check deployment');
  f.run('beginDeploySelection()');
  assert.equal(f.run('deployState.uncertain'), false); assert.equal(f.run('deployState.outcome'), null);
  assert.equal(f.doc.querySelector('#deploy-submit').textContent, 'Deploy');
});
test('history navigation hides a pending modal without aborting its request', async t => {
  const f = fixture(t); f.doc.querySelector('#deploy-submit').click();
  f.doc.querySelector('#deploy-view-history').addEventListener('click', event => event.preventDefault());
  f.doc.querySelector('#deploy-view-history').click();
  assert.equal(f.doc.querySelector('#deploy-modal').hidden, true); assert.equal(f.aborted(), 0);
  f.resolve({ status: 'running' }); await flush();
  assert.match(f.doc.querySelector('#deploy-monitor-text').textContent, /Now serving/);
});
