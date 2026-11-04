import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { createDataViewer } from '../static/views/data-ui.js';

const file = (path, size = 1024) => ({path, size, modified_at: 1793707200});
const envelope = (items = [], extra = {}) => ({items, total: items.length, data_bytes: 1024, used_bytes: 10485760, quota_mb: 20, ...extra});
const response = (body, status = 200) => ({ok: status >= 200 && status < 300, status, json: async () => body});
function deferred() {
  let resolve, reject;
  const promise = new Promise((done, fail) => { resolve = done; reject = fail; });
  return {promise, resolve, reject};
}
async function settle() { for (let i = 0; i < 20; i += 1) await Promise.resolve(); }

class Upload {
  constructor() { this.handlers = new Map(); }
  addEventListener(type, fn) { this.handlers.set(type, fn); }
  progress(loaded, total) { this.handlers.get('progress')?.({loaded, total, lengthComputable: total > 0}); }
}
class FakeXHR {
  constructor() { this.upload = new Upload(); this.headers = {}; this.status = 0; this.responseText = ''; }
  open(method, url) { this.method = method; this.url = url; }
  setRequestHeader(name, value) { this.headers[name] = value; }
  send(body) { this.body = body; }
  abort() { this.aborted = true; this.onabort?.(); }
  complete(status, body) { this.status = status; this.responseText = JSON.stringify(body); this.onload?.(); }
}
function harness(t, {api, canManage = true, app = {slug: 'forecast app'}, runtimeMode = 'native', confirm = () => true} = {}) {
  const dom = new JSDOM('<button id="outside">Outside</button><main id="panel"></main>', {url: 'https://example.test/'});
  const panel = dom.window.document.querySelector('main');
  const calls = [], uploads = [];
  let unauthorized = 0;
  const fetcher = (url, options = {}) => {
    calls.push({url, options});
    return api ? api(url, options, calls) : Promise.resolve(response(url.endsWith('/shared-data') ? {items: []} : envelope()));
  };
  const dispose = createDataViewer({panel, app, api: fetcher, canManage, runtimeMode, confirm, onUnauthorized: () => { unauthorized += 1; }, readCookie: name => name === 'csrf_token' ? 'test-token' : '', xhrFactory: () => { const xhr = new FakeXHR(); uploads.push(xhr); return xhr; }});
  t.after(() => { dispose(); dom.window.close(); });
  const el = id => panel.querySelector(`#${id}`);
  const submit = id => el(id).dispatchEvent(new dom.window.Event('submit', {bubbles: true, cancelable: true}));
  const input = (id, value) => { el(id).value = value; el(id).dispatchEvent(new dom.window.Event('input', {bubbles: true})); };
  const choose = (name = 'values.csv', contents = 'a,b\n1,2') => {
    const selected = new dom.window.File([contents], name, {type: 'text/csv'});
    Object.defineProperty(el('data-file-input'), 'files', {configurable: true, value: [selected]});
    el('data-file-input').dispatchEvent(new dom.window.Event('change', {bubbles: true}));
    return selected;
  };
  return {dom, panel, el, calls, uploads, dispose, submit, input, choose, unauthorized: () => unauthorized};
}

test('manager rows expose encoded downloads and deletion while storage separates data from the whole app', async t => {
  const h = harness(t, {api: async url => response(url.endsWith('/shared-data') ? {items: []} : envelope([file('nested/a #.csv')]))});
  await settle();
  assert.equal(h.el('data-file-total').textContent, '1');
  assert.equal(h.el('data-file-size').textContent, '1 KiB');
  assert.equal(h.el('data-app-storage').textContent, '10 MiB / 20 MiB');
  const link = h.el('data-list').querySelector('a');
  assert.equal(link.getAttribute('href'), '/api/apps/forecast%20app/data/nested/a%20%23.csv');
  assert.ok(link.hasAttribute('download'));
  assert.equal(h.el('data-list').querySelector('button').getAttribute('aria-label'), 'Delete nested/a #.csv');
  assert.match(h.el('data-list').querySelector('time').dateTime, /^2026-/);
  assert.equal(h.calls[0].url, '/api/apps/forecast%20app/data');
});

test('metadata readers see filenames and mounts without download or write controls', async t => {
  const h = harness(t, {canManage: false, api: async url => response(url.endsWith('/shared-data') ? {items: [{source_slug: '<warehouse>'}]} : [file('<private>.csv')])});
  await settle();
  assert.equal(h.el('data-list').textContent.includes('<private>.csv'), true);
  assert.equal(h.el('data-list').querySelector('a,button'), null);
  assert.equal(h.el('shared-data-list').querySelector('button'), null);
  assert.equal(h.el('shared-data-list').querySelector('strong').textContent, '<warehouse>');
  assert.equal(h.el('data-upload-toggle').hidden, true);
  assert.equal(h.el('data-mount-toggle').hidden, true);
  assert.equal(h.el('data-file-size').textContent, 'Unavailable');
  assert.equal(h.el('data-app-storage').textContent, 'Unavailable');
  assert.equal(h.panel.querySelector('private,warehouse'), null);
});

test('pagination bounds mounted rows and filtering searches the entire loaded list', async t => {
  const items = Array.from({length: 123}, (_, i) => file(`file-${String(i).padStart(3, '0')}.csv`));
  const h = harness(t, {api: async url => response(url.endsWith('/shared-data') ? [] : envelope(items, {quota_mb: 0}))});
  await settle();
  assert.equal(h.el('data-list').children.length, 50);
  assert.equal(h.el('data-file-count').textContent, '50 of 123 files');
  assert.match(h.el('data-app-storage').textContent, /No limit/);
  h.el('data-more').click();
  assert.equal(h.el('data-list').children.length, 100);
  assert.equal(h.el('data-more').textContent, 'Show 23 more');
  h.input('data-filter', 'FILE-122');
  assert.equal(h.el('data-list').children.length, 1);
  assert.match(h.el('data-list').textContent, /file-122.csv/);
  assert.equal(h.el('data-file-count').textContent, '1 matching · 123 total');
  h.input('data-filter', '');
  assert.equal(h.el('data-list').children.length, 50);
  h.input('data-filter', 'missing');
  assert.equal(h.el('data-list-table').hidden, true);
  assert.match(h.el('data-list-status').textContent, /No files match/);
});

test('transient file and mount failures retain each previous list and retry independently', async t => {
  let broken = false;
  const h = harness(t, {api: async url => {
    if (broken) return response({error: 'Temporarily unavailable'}, 503);
    return response(url.endsWith('/shared-data') ? {items: [{source_slug: 'warehouse'}]} : envelope([file('kept.csv')]));
  }});
  await settle(); broken = true; h.el('data-refresh').click(); await settle();
  assert.match(h.el('data-list').textContent, /kept.csv/);
  assert.match(h.el('shared-data-list').textContent, /warehouse/);
  assert.equal(h.el('data-error').hidden, false);
  assert.equal(h.el('data-mount-error').hidden, false);
  broken = false; h.el('data-retry').click(); await settle();
  assert.equal(h.el('data-error').hidden, true);
  assert.equal(h.el('data-mount-error').hidden, false);
  h.el('data-mount-retry').click(); await settle();
  assert.equal(h.el('data-mount-error').hidden, true);
});

test('malformed successful reports produce errors instead of a false empty state', async t => {
  const h = harness(t, {api: async () => response({unexpected: []})});
  await settle();
  assert.match(h.el('data-error').textContent, /invalid file list/);
  assert.match(h.el('data-mount-error').textContent, /invalid mount list/);
  assert.doesNotMatch(h.el('data-list-status').textContent, /No data files yet/);
  assert.doesNotMatch(h.el('data-mount-status').textContent, /No shared mounts/);
});

test('network errors explain missing or stale lists and recover independently', async t => {
  let offline = true;
  const h = harness(t, {api: async url => {
    if (offline) throw new TypeError('Failed to fetch');
    return response(url.endsWith('/shared-data') ? [{source_slug:'warehouse'}] : envelope([file('kept.csv')]));
  }});
  await settle();
  for (const id of ['data-error', 'data-mount-error']) {
    assert.match(h.el(id).textContent, /Could not load.*Check your connection/);
    assert.doesNotMatch(h.el(id).textContent, /last loaded|Failed to fetch/);
  }
  offline = false; h.el('data-refresh').click(); await settle();
  offline = true; h.el('data-refresh').click(); await settle();
  assert.match(h.el('data-list').textContent, /kept.csv/);
  assert.match(h.el('shared-data-list').textContent, /warehouse/);
  for (const id of ['data-error', 'data-mount-error']) assert.match(h.el(id).textContent, /Could not refresh.*Showing the last loaded list/);
  offline = false; h.el('data-retry').click(); await settle();
  assert.equal(h.el('data-error').hidden, true);
  assert.equal(h.el('data-mount-error').hidden, false);
  h.el('data-mount-retry').click(); await settle();
  assert.equal(h.el('data-mount-error').hidden, true);
});

test('superseded file and mount fetches cannot replace newer reports', async t => {
  const oldFiles = deferred(), oldMounts = deferred();
  let count = 0;
  const h = harness(t, {api: url => {
    count += 1;
    if (count <= 2) return url.endsWith('/shared-data') ? oldMounts.promise : oldFiles.promise;
    return Promise.resolve(response(url.endsWith('/shared-data') ? [{source_slug: 'new-source'}] : envelope([file('new.csv')])));
  }});
  h.el('data-refresh').click(); await settle();
  assert.equal(h.calls[0].options.signal.aborted, true);
  assert.equal(h.calls[1].options.signal.aborted, true);
  oldFiles.resolve(response(envelope([file('old.csv')]))); oldMounts.resolve(response([{source_slug: 'old-source'}])); await settle();
  assert.match(h.el('data-list').textContent, /new.csv/);
  assert.doesNotMatch(h.panel.textContent, /old.csv|old-source/);
});

test('superseded JSON parsing cannot replace newer reports', async t => {
  const oldFiles = deferred(), oldMounts = deferred();
  let count = 0;
  const h = harness(t, {api: async url => {
    count += 1;
    if (count <= 2) return {ok: true, status: 200, json: () => url.endsWith('/shared-data') ? oldMounts.promise : oldFiles.promise};
    return response(url.endsWith('/shared-data') ? [] : envelope([file('fresh.csv')]));
  }});
  await settle(); h.el('data-refresh').click(); await settle();
  oldFiles.resolve(envelope([file('stale.csv')])); oldMounts.resolve([{source_slug: 'stale-source'}]); await settle();
  assert.match(h.el('data-list').textContent, /fresh.csv/);
  assert.doesNotMatch(h.panel.textContent, /stale.csv|stale-source/);
});

test('teardown aborts reads and ignores their late JSON on a replacement panel', async t => {
  const body = deferred();
  const h = harness(t, {api: async () => ({ok: true, status: 200, json: () => body.promise})});
  await settle(); h.dispose(); h.panel.innerHTML = '<p>Replacement app</p>';
  body.resolve(envelope([file('old-private.csv')])); await settle();
  assert.equal(h.panel.textContent, 'Replacement app');
  assert.ok(h.calls.every(call => call.options.signal.aborted));
});

test('upload progress waits for server confirmation, preserves restart failure as partial success and refreshes', async t => {
  const app = {slug: 'original app'};
  const h = harness(t, {app}); await settle();
  const events = []; h.dom.window.document.addEventListener('shinyhub:mutated', event => events.push(event.detail));
  const selected = h.choose(); h.input('data-dest-input', 'nested/a #.csv'); h.el('data-restart-input').checked = true;
  h.submit('data-upload-form');
  const xhr = h.uploads[0];
  assert.equal(xhr.url, '/api/apps/original%20app/data/nested/a%20%23.csv?restart=true');
  assert.equal(xhr.headers['X-CSRF-Token'], 'test-token');
  assert.equal(xhr.body, selected);
  assert.equal(xhr.withCredentials, true);
  xhr.upload.progress(100, 100);
  assert.match(h.el('data-upload-status').textContent, /Waiting for the server/);
  assert.equal(h.el('data-upload-btn').disabled, true);
  app.slug = 'replacement';
  xhr.complete(200, {path: 'nested/a #.csv', size: selected.size, restarted: false, restart_error: 'activation in progress'}); await settle();
  assert.match(h.el('data-upload-status').textContent, /File saved. App restart failed: activation in progress/);
  assert.equal(h.el('data-upload-btn').disabled, false);
  assert.equal(h.el('data-dest-input').value, '');
  assert.deepEqual(events, [{slug: 'original app'}]);
  assert.equal(h.calls.filter(call => call.url === '/api/apps/original%20app/data').length, 2);
});

test('413 uses backend quota keys and keeps the chosen destination for correction', async t => {
  const h = harness(t); await settle(); h.choose(); h.input('data-dest-input', 'replace.csv'); h.submit('data-upload-form');
  h.uploads[0].complete(413, {QuotaBytes: 1048576, UsedBytes: 1000000, WouldBeBytes: 2097152, RemainingBytes: 48576});
  assert.match(h.el('data-upload-status').textContent, /1 MiB app storage limit by 1 MiB/);
  assert.match(h.el('data-upload-status').textContent, /existing file was kept/);
  assert.equal(h.el('data-dest-input').value, 'replace.csv');
  assert.equal(h.calls.length, 2);
});

for (const failure of ['onerror', 'ontimeout', 'onabort', 'status0', 'invalid-success']) {
  test(`upload ${failure} keeps the draft and asks for a refresh before retry`, async t => {
    const h = harness(t); await settle(); h.choose(); h.input('data-dest-input', 'keep.csv'); h.submit('data-upload-form');
    const xhr = h.uploads[0];
    if (failure === 'status0') xhr.complete(0, {});
    else if (failure === 'invalid-success') xhr.complete(200, {});
    else xhr[failure]();
    assert.match(h.el('data-upload-status').textContent, /Refresh files before retrying/);
    assert.equal(h.el('data-dest-input').value, 'keep.csv');
    assert.equal(h.el('data-upload-btn').disabled, false);
    assert.equal(h.calls.length, 2);
  });
}

test('invalid paths and empty files never start an upload; a replacement is visibly named', async t => {
  const h = harness(t, {api: async url => response(url.endsWith('/shared-data') ? [] : envelope([file('values.csv')]))}); await settle();
  h.choose();
  assert.equal(h.el('data-upload-btn').textContent, 'Replace file');
  assert.equal(h.el('data-destination').textContent, 'Replaces data/values.csv');
  for (const path of ['/absolute', '../secret', 'a/../b', 'folder/', '.shinyhub-private', '.', './.', './.shinyhub-private', './/.shinyhub-private', 'é'.repeat(257)]) {
    h.input('data-dest-input', path); h.submit('data-upload-form');
    assert.match(h.el('data-upload-status').textContent, /relative file path/);
  }
  h.choose('empty.csv', ''); h.input('data-dest-input', 'empty.csv'); h.submit('data-upload-form');
  assert.match(h.el('data-upload-status').textContent, /not empty/);
  assert.equal(h.uploads.length, 0);
});

for (const deniedList of ['files', 'mounts']) {
  test(`${deniedList} 403 clears both private lists and storage, suppresses pending opposite JSON, and permits retry`, async t => {
    const oppositeBody = deferred();
    let phase = 'initial';
    const h = harness(t, {api: async url => {
      const isMounts = url.endsWith('/shared-data');
      const target = deniedList === 'mounts' ? isMounts : !isMounts;
      if (phase === 'deny' && target) return response({error: 'App access was revoked'}, 403);
      if (phase === 'deny') return {ok: true, status: 200, json: () => oppositeBody.promise};
      return response(isMounts ? [{source_slug: 'private-source'}] : envelope([file('private-file.csv')]));
    }}); await settle();
    assert.match(h.panel.textContent, /private-file.csv/);
    assert.match(h.panel.textContent, /private-source/);
    phase = 'deny'; h.el('data-refresh').click(); await settle();
    assert.equal(h.el('data-list').children.length, 0);
    assert.equal(h.el('shared-data-list').children.length, 0);
    for (const id of ['data-file-total', 'data-file-size', 'data-app-storage']) assert.equal(h.el(id).textContent, 'Unavailable');
    assert.equal(h.el('data-upload-toggle').hidden, true);
    assert.equal(h.el('data-mount-toggle').hidden, true);
    oppositeBody.resolve(deniedList === 'files' ? [{source_slug: 'leaked-source'}] : envelope([file('leaked-file.csv')])); await settle();
    assert.doesNotMatch(h.panel.textContent, /leaked-source|leaked-file.csv/);
    assert.equal(h.el('data-retry').disabled, false);
    assert.equal(h.el('data-mount-retry').disabled, false);
    phase = 'recovered';
    h.el('data-retry').click(); h.el('data-mount-retry').click(); await settle();
    assert.match(h.el('data-list').textContent, /private-file.csv/);
    assert.match(h.el('shared-data-list').textContent, /private-source/);
    // Existing can_manage data may be stale after a revocation; recovery of read
    // access does not restore download or write authority without a fresh app.
    assert.equal(h.el('data-list').querySelector('a,button'), null);
  });
}

test('teardown aborts upload and ignores late progress, success and failure callbacks', async t => {
  const h = harness(t); await settle(); h.choose(); h.submit('data-upload-form');
  const xhr = h.uploads[0]; h.dispose(); h.panel.innerHTML = '<p>New app</p>';
  assert.equal(xhr.aborted, true);
  xhr.upload.progress(5, 10); xhr.complete(200, {path: 'old.csv', size: 7}); xhr.onerror(); await settle();
  assert.equal(h.panel.textContent, 'New app');
  assert.equal(h.calls.length, 2);
});

test('mount form blocks self-mounts, posts the captured app slug and preserves server warning', async t => {
  const app = {slug: 'consumer'};
  const h = harness(t, {app, api: async (url, options) => options.method === 'POST' ? response({source_slug: 'warehouse', warning: 'Native writes are not blocked.'}) : response(url.endsWith('/shared-data') ? [] : envelope())}); await settle();
  h.el('data-mount-toggle').click();
  assert.equal(h.dom.window.document.activeElement, h.el('data-source-input'));
  h.input('data-source-input', 'consumer'); h.submit('data-mount-form');
  assert.match(h.el('data-mount-form-status').textContent, /another app/);
  assert.equal(h.calls.length, 2);
  app.slug = 'new-app'; h.input('data-source-input', ' warehouse '); h.submit('data-mount-form'); await settle();
  const post = h.calls.find(call => call.options.method === 'POST');
  assert.equal(post.url, '/api/apps/consumer/shared-data');
  assert.deepEqual(JSON.parse(post.options.body), {source_slug: 'warehouse'});
  assert.match(h.el('data-mount-form-status').textContent, /configuration saved. Running apps are not restarted/);
  assert.match(h.el('data-mount-form-status').textContent, /Native writes are not blocked/);
  assert.match(h.el('data-runtime-note').textContent, /convention/);
});

test('teardown aborts an outstanding deletion and ignores its late success', async t => {
  const pending = deferred();
  const h = harness(t, {api: async (url, options) => options.method === 'DELETE' ? pending.promise : response(url.endsWith('/shared-data') ? [] : envelope([file('keep.csv')]))}); await settle();
  h.el('data-list').querySelector('button').click();
  const write = h.calls.find(call => call.options.method === 'DELETE');
  assert.equal(write.url, '/api/apps/forecast%20app/data/keep.csv');
  h.dispose(); h.panel.innerHTML = '<p>Another app</p>';
  assert.equal(write.options.signal.aborted, true);
  pending.resolve(response({}, 204)); await settle();
  assert.equal(h.panel.textContent, 'Another app');
  assert.equal(h.calls.length, 3);
});

test('an unconfirmed deletion keeps the file visible and tells the user to refresh before retrying', async t => {
  const h = harness(t, {api: async (url, options) => {
    if (options.method === 'DELETE') throw new Error('Connection lost after sending');
    return response(url.endsWith('/shared-data') ? [] : envelope([file('keep.csv')]));
  }}); await settle();
  h.el('data-list').querySelector('button').click(); await settle();
  assert.match(h.el('data-list').textContent, /keep.csv/);
  assert.match(h.el('data-error').textContent, /could not be confirmed. Refresh before trying again/);
  assert.equal(h.el('data-list').querySelector('button').disabled, false);
});

test('unmount confirms disruption, sends restart consent, and accepts an empty 204 response', async t => {
  let removed = false, confirmation = '';
  const h = harness(t, {confirm: message => { confirmation = message; return true; }, api: async (url, options) => {
    if (options.method === 'DELETE') { removed = true; return {ok: true, status: 204, json: async () => { throw new SyntaxError('Empty body'); }}; }
    return response(url.endsWith('/shared-data') ? removed ? [] : [{source_slug: 'warehouse'}] : envelope());
  }}); await settle();
  h.el('shared-data-list').querySelector('button').click(); await settle();
  assert.match(confirmation, /running app will restart and active sessions will disconnect/);
  assert.match(h.calls.find(call => call.options.method === 'DELETE').url, /warehouse\?restart=true$/);
  assert.equal(h.el('shared-data-list').children.length, 0);
  assert.equal(h.el('data-mount-status').textContent, 'Unmounted warehouse.');
});

test('a source access failure keeps the mount error visible and leaves consumer management available', async t => {
  const h = harness(t, {api: async (url, options) => options.method === 'POST' ? response({error: 'Source app access is required'}, 403) : response(url.endsWith('/shared-data') ? [] : envelope([file('metadata.csv')]))}); await settle();
  h.el('data-mount-toggle').click();
  h.input('data-source-input', 'warehouse'); h.submit('data-mount-form'); await settle();
  assert.equal(h.el('data-source-input').value, 'warehouse');
  assert.match(h.el('data-mount-form-status').textContent, /Source app access is required/);
  assert.equal(h.el('data-mount-form').hidden, false);
  assert.equal(h.el('data-upload-toggle').hidden, false);
  assert.equal(h.el('data-mount-toggle').hidden, false);
  assert.ok(h.el('data-list').querySelector('a'));
  assert.equal(h.el('data-mount-btn').disabled, false);
});

test('401 from a list or upload invokes the session handler without reporting upload success', async t => {
  const h = harness(t, {api: async url => response(url.endsWith('/shared-data') ? [] : {}, url.endsWith('/shared-data') ? 200 : 401)}); await settle();
  assert.equal(h.unauthorized(), 1);
  h.choose(); h.submit('data-upload-form'); h.uploads[0].complete(401, {error: 'Unauthorized'});
  assert.equal(h.unauthorized(), 2);
  assert.match(h.el('data-upload-status').textContent, /session expired/);
  assert.doesNotMatch(h.el('data-upload-status').textContent, /File saved/);
});


for (const [name, result, expected, error] of [
  ['running app', {unmounted: true, restarted: true}, 'Unmounted warehouse. App restarted.', false],
  ['failed restart', {unmounted: true, restarted: false, restart_error: 'Worker unavailable'}, 'Unmounted warehouse. App restart failed: Worker unavailable. The app is stopped; start it when ready.', true],
]) {
  test(`unmount reports the confirmed outcome for a ${name}`, async t => {
    let removed = false;
    const h = harness(t, {api: async (url, options) => {
      if (options.method === 'DELETE') { removed = true; return response(result); }
      return response(url.endsWith('/shared-data') ? removed ? [] : [{source_slug: 'warehouse'}] : envelope());
    }}); await settle();
    h.el('shared-data-list').querySelector('button').click(); await settle();
    assert.equal(h.el('shared-data-list').children.length, 0);
    assert.equal(h.el('data-mount-status').textContent, expected);
    assert.equal(h.el('data-mount-status').classList.contains('error'), error);
    assert.equal(h.el('data-mount-status').hidden, false);
  });
}

for (const reason of ['Finish or cancel active scheduled jobs before changing shared mounts', 'App processes could not be confirmed stopped']) {
  test(`blocked unmount retains access and shows the reason: ${reason}`, async t => {
    const h = harness(t, {api: async (url, options) => options.method === 'DELETE'
      ? response({error: reason}, 409)
      : response(url.endsWith('/shared-data') ? [{source_slug: 'warehouse'}] : envelope())}); await settle();
    h.el('shared-data-list').querySelector('button').click(); await settle();
    assert.equal(h.el('shared-data-list').children.length, 1);
    assert.equal(h.el('shared-data-list').querySelector('button').disabled, false);
    assert.equal(h.el('data-mount-error').hidden, false);
    assert.match(h.el('data-mount-error').textContent, new RegExp(reason));
    assert.doesNotMatch(h.el('data-mount-status').textContent, /Unmounted/);
  });
}

test('refresh during unmount cannot submit a duplicate restart and failed requests can be retried', async t => {
  const pending = deferred();
  const h = harness(t, {api: async (url, options) => options.method === 'DELETE' ? pending.promise
    : response(url.endsWith('/shared-data') ? [{source_slug: 'warehouse'}] : envelope())}); await settle();
  h.el('shared-data-list').querySelector('button').click(); await settle();
  h.el('data-refresh').click(); await settle();
  const replacement = h.el('shared-data-list').querySelector('button');
  assert.equal(replacement.disabled, true);
  replacement.click(); await settle();
  assert.equal(h.calls.filter(call => call.options.method === 'DELETE').length, 1);
  pending.resolve(response({error: 'Busy; retry shortly'}, 409)); await settle();
  assert.equal(replacement.disabled, false);
});

test('a successful HTTP response without an unmount receipt cannot hide the mount', async t => {
  const h = harness(t, {api: async (url, options) => options.method === 'DELETE' ? response({})
    : response(url.endsWith('/shared-data') ? [{source_slug: 'warehouse'}] : envelope())}); await settle();
  h.el('shared-data-list').querySelector('button').click(); await settle();
  assert.equal(h.el('shared-data-list').children.length, 1);
  assert.match(h.el('data-mount-error').textContent, /could not be confirmed/);
});
