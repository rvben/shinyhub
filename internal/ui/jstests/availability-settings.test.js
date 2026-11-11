import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
const app = readFileSync(new URL('../static/app.js', import.meta.url), 'utf8');
const start = app.indexOf('  function populateAvailabilitySetting(');
const end = app.indexOf('  async function saveUsagePrivacy(', start);

function editor(doc, api) {
  return new Function('document', 'api', 'setError', 'setHidden', `
    let settingsSlug = 'sales', detailApp = null;
    const state = {user:{}};
    const canManageApp = () => true;
    const recomputeDirty = () => {};
    const snapshotSettingsSection = () => {};
    const loadApps = async () => {};
    const refreshDetailFleetState = async () => {};
    ${app.slice(start, end)}
    return {populate:populateAvailabilitySetting, save:saveGeneralInfo};
  `)(doc, api, (el, text) => {el.textContent=text;}, (el, hidden) => {el.hidden=hidden;});
}

test('availability editor explains authoritative overrides and policy suppression', () => {
  const doc = new JSDOM(html).window.document;
  const ui = editor(doc, () => {});
  ui.populate({availability:'off', availability_exposure:{exposed:true,source:'allowlist',policy_suppressed:false}}, true);
  assert.equal(doc.getElementById('general-availability').value, 'off');
  assert.match(doc.getElementById('general-availability-effective').textContent, /Currently public.*operator allowlist/);
  ui.populate({availability:'inherit', availability_exposure:{exposed:false,source:'none',policy_suppressed:true,app_allowed:false}}, false);
  assert.match(doc.getElementById('general-availability-effective').textContent, /Currently off.*suppressed by server policy/);
  assert.equal(doc.getElementById('general-availability').disabled, true);
  assert.equal(doc.querySelector('#general-availability [value=public]').disabled, true);
});

test('general saves preserve omitted intent and unwrap the effective exposure after a deliberate reset', async () => {
  const doc = new JSDOM(html).window.document;
  doc.getElementById('general-name').value='Sales';
  const bodies=[];
  let intent='off';
  const ui=editor(doc, async (_path, req) => {
    const body=JSON.parse(req.body);bodies.push(body);
    intent=body.availability || intent;
    return {ok:true,status:200,json:async()=>({app:{availability:intent,availability_exposure:{exposed:intent==='inherit',source:intent==='inherit'?'default':'app',policy_suppressed:false}}})};
  });
  ui.populate({availability:'off',availability_exposure:{exposed:false,source:'app'}}, true);
  await ui.save();
  assert.equal(Object.hasOwn(bodies[0],'availability'), false);
  doc.getElementById('general-availability').value='inherit';
  await ui.save();
  assert.equal(bodies[1].availability,'inherit');
  assert.match(doc.getElementById('general-availability-effective').textContent, /Currently public.*server default/);
  await ui.save();
  assert.equal(Object.hasOwn(bodies[2],'availability'), false);
});

test('a malformed success response gives recovery guidance without an unhandled rejection', async () => {
  const doc = new JSDOM(html).window.document;
  doc.getElementById('general-name').value = 'Sales';
  const ui = editor(doc, async () => ({ok:true,status:200,json:async()=>{throw new Error('malformed');}}));
  ui.populate({availability:'off',availability_exposure:{exposed:false,source:'app'}}, true);
  await ui.save();
  assert.match(doc.getElementById('general-error').textContent, /Settings were saved.*Refresh to check/);
});
