import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { createPageHeader, enhancePageHeaders, createFilterBar, bindFilterBar, setFeedback, createListState } from '../static/views/page-components.js';

function page(t, html = '') {
  const dom = new JSDOM(html, {pretendToBeVisual:true});
  t.after(() => dom.window.close());
  return {doc:dom.window.document, win:dom.window};
}

test('page headers keep supplied controls, permissions and literal text', t => {
  const {doc} = page(t);
  const actions = doc.createElement('div');
  const button = doc.createElement('button'); button.hidden = true;
  let clicks = 0; button.addEventListener('click', () => clicks++); actions.append(button);
  const header = createPageHeader(doc, {title:'<b>Identity</b>',headingId:'title',description:'People & <groups>',actions});
  doc.body.append(header);
  assert.equal(header.querySelector('h1').textContent, '<b>Identity</b>');
  assert.equal(header.querySelector('b'), null);
  assert.equal(header.querySelector('.page-description').textContent, 'People & <groups>');
  assert.equal(header.querySelector('h1').id, 'title');
  assert.equal(header.querySelector('button'), button);
  assert.equal(button.hidden, true);
  button.hidden = false; button.click(); assert.equal(clicks, 1);
  assert.equal(createPageHeader(doc, {title:'No actions'}).children.length, 1);
});

test('the real shell shares header composition without replacing identities or actions', t => {
  const source = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
  const {doc} = page(t, source);
  const hosts = [...doc.querySelectorAll('[data-page-header]')];
  assert.equal(hosts.length, 8);
  const originals = hosts.map(host => ({
    host, heading:host.querySelector('h1'), identity:host.querySelector('[data-page-identity]'),
    actions:host.querySelector(':scope > .page-actions'),
    controls:[...host.querySelectorAll('button,a,input')].map(node => ({node,hidden:node.hidden})),
  }));
  enhancePageHeaders(doc);
  for (const {host,heading,identity,actions,controls} of originals) {
    assert.equal(host.querySelector('h1'), heading);
    assert.equal(host.querySelectorAll(':scope > .page-header-copy').length, 1);
    if (identity) assert.equal(host.firstElementChild, identity);
    if (actions) assert.equal(host.lastElementChild, actions);
    for (const {node,hidden} of controls) { assert.equal(host.contains(node), true); assert.equal(node.hidden, hidden); }
  }
  const contents = hosts.map(host => host.innerHTML);
  enhancePageHeaders(doc);
  assert.deepEqual(hosts.map(host => host.innerHTML), contents);
  originals[0].heading.textContent = 'Updated title';
  enhancePageHeaders(doc);
  assert.equal(hosts[0].querySelector('h1').textContent, 'Updated title');
});

test('enhancing a mounted header preserves keyboard focus and action listeners', t => {
  const {doc} = page(t, '<div data-page-header><h1>Apps</h1><div class="page-actions"><button>New app</button></div></div>');
  const button = doc.querySelector('button'); let clicks = 0;
  button.addEventListener('click', () => clicks++); button.focus();
  enhancePageHeaders(doc);
  assert.equal(doc.activeElement, button); button.click(); assert.equal(clicks, 1);
});

function filters(t) {
  const env = page(t, '<form><input type="search" value=""><select><option value="all">All</option><option value="active" selected>Active</option></select><input type="date"><button type="button" id="clear">Clear</button></form><button id="external">Clear results</button>');
  const {doc} = env;
  return {...env,root:doc.querySelector('form'),fields:{search:doc.querySelector('input'),status:doc.querySelector('select'),since:doc.querySelector('[type=date]')},clearButtons:[doc.querySelector('#clear'),doc.querySelector('#external')]};
}

test('filter bars retain native labels and controls and announce their purpose', t => {
  const {doc} = page(t, '<label for="search">Search</label><input id="search" type="search">');
  const controls = [...doc.body.children];
  const bar = createFilterBar(doc, {label:'Filter apps',controls,className:'apps-filters'});
  doc.body.append(bar);
  assert.equal(bar.getAttribute('role'), 'search');
  assert.equal(bar.getAttribute('aria-label'), 'Filter apps');
  assert.equal(bar.querySelector('label').control, controls[1]);
  assert.equal(bar.classList.contains('apps-filters'), true);
});

test('filters distinguish editing from applying a server query and prevent native navigation', t => {
  const f = filters(t); const changes = [], applies = [];
  const binding = bindFilterBar({...f,onChange:values=>changes.push(values),onApply:values=>applies.push(values)});
  f.fields.search.value = '東京'; f.fields.search.dispatchEvent(new f.win.Event('input'));
  f.fields.since.value = '2026-11-03'; f.fields.since.dispatchEvent(new f.win.Event('change'));
  assert.equal(changes.length, 2); assert.equal(applies.length, 0);
  const submit = new f.win.Event('submit', {cancelable:true}); f.root.dispatchEvent(submit);
  assert.equal(submit.defaultPrevented, true);
  assert.deepEqual(applies, [{search:'東京',status:'active',since:'2026-11-03'}]);
  assert.deepEqual(binding.read(), applies[0]);
});

test('clear restores declared defaults once and returns focus from an empty-state action', t => {
  const f = filters(t); let changes = 0;
  bindFilterBar({...f,onChange:()=>changes++});
  f.fields.search.value = 'none'; f.fields.status.value = 'all'; f.fields.since.value = '2026-11-03';
  f.clearButtons[1].focus(); f.clearButtons[1].click();
  assert.equal(changes, 1);
  assert.equal(f.fields.search.value, ''); assert.equal(f.fields.status.value, 'active'); assert.equal(f.fields.since.value, '');
  assert.equal(f.doc.activeElement, f.fields.search);
});

test('partial reset preserves sorting and disposed bindings do not duplicate on remount', t => {
  const f = filters(t); let changes = 0;
  let binding = bindFilterBar({...f,resetFields:['search'],onChange:()=>changes++});
  f.fields.search.value = 'none'; f.fields.status.value = 'all'; binding.reset();
  assert.equal(f.fields.status.value, 'all'); assert.equal(changes, 1);
  binding.dispose();
  f.fields.search.dispatchEvent(new f.win.Event('input')); f.clearButtons[0].click();
  assert.equal(changes, 1);
  binding = bindFilterBar({...f,onChange:()=>changes++});
  f.fields.search.dispatchEvent(new f.win.Event('input')); assert.equal(changes, 2);
  binding.dispose();
});

test('feedback is safe text with appropriate inline announcements and clearing', t => {
  const {doc} = page(t, '<p hidden></p>'); const node = doc.querySelector('p');
  setFeedback(node, '<img src=x>', {error:true});
  assert.equal(node.hidden, false); assert.equal(node.children.length, 0);
  assert.equal(node.textContent, '<img src=x>'); assert.equal(node.getAttribute('role'), 'alert');
  assert.equal(node.getAttribute('aria-atomic'), 'true'); assert.equal(node.classList.contains('is-error'), true);
  setFeedback(node, 'Copied');
  assert.equal(node.getAttribute('role'), 'status'); assert.equal(node.classList.contains('is-error'), false);
  setFeedback(node, 'Copy failed', {error:true,role:'status'});
  assert.equal(node.getAttribute('role'), 'status');
  setFeedback(node, ''); assert.equal(node.hidden, true); assert.equal(node.textContent, '');
  setFeedback(node, '', {collapse:false}); assert.equal(node.hidden, false);
});

function inventory(t) {
  const {doc} = page(t, '<main><div id="loading"><span>Loading…</span></div><p id="error" hidden></p><div id="content" hidden><details open><summary>Connection</summary></details></div><p id="empty" hidden>Nothing yet</p><p id="noMatches" hidden>No matches</p></main>');
  const nodes = Object.fromEntries(['loading','error','content','empty','noMatches'].map(id => [id,doc.getElementById(id)]));
  const busy = doc.querySelector('main');
  return {doc,...nodes,busy,list:createListState({...nodes,busy})};
}

test('list states distinguish loading, an empty inventory, no matches and initial failure', t => {
  const f = inventory(t); const skeleton = f.loading.firstElementChild;
  f.list.pending(); assert.equal(f.loading.hidden, false); assert.equal(f.empty.hidden, true);
  assert.equal(f.loading.firstElementChild, skeleton); assert.equal(f.busy.getAttribute('aria-busy'), 'true');
  f.list.finish(); f.list.results({total:0});
  assert.equal(f.loading.hidden, true); assert.equal(f.empty.hidden, false); assert.equal(f.noMatches.hidden, true);
  f.list.results({total:4,visible:0});
  assert.equal(f.empty.hidden, true); assert.equal(f.noMatches.hidden, false); assert.equal(f.content.hidden, true);
  f.list.pending(); f.list.fail('Unavailable');
  assert.equal(f.empty.hidden, true); assert.equal(f.noMatches.hidden, true); assert.equal(f.error.hidden, false);
  assert.equal(f.busy.hasAttribute('aria-busy'), false);
});

test('retained failures and retries preserve content, open details, focus and the stale warning', t => {
  const f = inventory(t); f.list.finish(); f.list.results({total:1});
  const details = f.content.querySelector('details'), summary = details.querySelector('summary'); summary.focus();
  f.list.pending({retain:true}); f.list.fail('Showing last loaded data.', {retain:true});
  assert.equal(f.content.hidden, false); assert.equal(f.content.querySelector('details'), details);
  assert.equal(details.open, true); assert.equal(f.doc.activeElement, summary);
  f.list.pending({retain:true});
  assert.equal(f.error.hidden, false); assert.equal(f.error.textContent, 'Showing last loaded data.');
  assert.equal(f.loading.hidden, true);
  f.list.finish(); setFeedback(f.error, '', {error:true}); f.list.results({total:1});
  assert.equal(f.error.hidden, true); assert.equal(f.content.hidden, false);
});

test('retained empty results stay truthful and explicit reset clears private list states', t => {
  const f = inventory(t); f.list.finish(); f.list.results({total:0});
  f.list.pending({retain:true}); f.list.fail('Refresh failed', {retain:true});
  assert.equal(f.empty.hidden, false); assert.equal(f.content.hidden, true);
  f.list.pending({retain:true,clearError:true,message:'Refreshing…'});
  assert.equal(f.error.hidden, true); assert.equal(f.loading.textContent, 'Refreshing…');
  f.list.clear();
  for (const node of [f.content,f.loading,f.error,f.empty,f.noMatches]) assert.equal(node.hidden, true);
  assert.equal(f.busy.hasAttribute('aria-busy'), false);
});
