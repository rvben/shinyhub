import {test} from 'node:test';
import assert from 'node:assert/strict';
import {JSDOM} from 'jsdom';
import {createOverviewActivity, overviewActivityRows} from '../static/views/app-overview-activity.js';

const flush = () => new Promise(resolve => setTimeout(resolve, 0));
const response = (items, status = 200) => ({ok:status === 200, status, json:async () => ({items})});
const release = {id:1, release_number:2, status:'succeeded', created_at:'2026-11-03T10:00:00Z'};
function harness(api, canReadSchedules = true) {
  const dom = new JSDOM('<section></section>', {url:'https://hub.example.com'});
  const panel = dom.window.document.querySelector('section');
  let unauthorized = 0;
  const dispose = createOverviewActivity({panel, app:{slug:'app/a'}, api, canReadSchedules, onUnauthorized:() => unauthorized++});
  return {panel, dispose, dom, unauthorized:() => unauthorized, close:() => {dispose();dom.window.close();}};
}

test('activity interleaves latest scheduled runs and deployments, with accurate release labels', () => {
  const rows = overviewActivityRows([release, {id:2,status:'failed',version:'3',failure_reason:'Dependency missing',created_at:'2026-11-03T12:00:00Z'}],
    [{id:9,name:'Refresh',last_run_status:'failed',last_run_at:'2026-11-03T13:00:00Z'}, {id:10,retired:true,last_run_status:'failed',last_run_at:'2026-11-03T14:00:00Z'}], 'app/a');
  assert.deepEqual(rows.map(row => row.title), ['Refresh','Deployment attempt','Release v2']);
  assert.equal(rows[0].href, '/apps/app%2Fa/schedules/9');
  assert.equal(rows[1].detail, 'Dependency missing');
  assert.equal(rows[1].status, 'failed');
});

test('activity omits malformed identities and never invents a successful outcome or date', () => {
  const rows = overviewActivityRows([{}, {id:1,created_at:'invalid'}], [], 'app');
  assert.equal(rows.length, 1);
  assert.equal(rows[0].status, 'unknown');
  assert.equal(rows[0].time, null);
});

test('transient source failures retain last results; access revocation clears only that source', async () => {
  let status = 200;
  const h = harness(async path => path.includes('deployments') ? response([release],status)
    : response([{id:9,name:'<img src=x>',last_run_status:'failed',last_run_at:'2026-11-03T13:00:00Z'}]));
  try {
    await flush(); assert.equal(h.panel.querySelectorAll('li').length,2); assert.equal(h.panel.querySelector('img'),null);
    status = 503; assert.equal(await h.dispose.refresh(),false);
    assert.equal(h.panel.querySelectorAll('li').length,2);
    assert.match(h.panel.textContent,/Showing the last results/);
    status = 403; await h.dispose.refresh();
    assert.equal(h.panel.querySelectorAll('li').length,1);
    assert.doesNotMatch(h.panel.textContent,/Release v2/);
    assert.match(h.panel.textContent,/no longer have access to deployments/);
    status = 200; assert.equal(await h.dispose.refresh(true),true);
    assert.equal(h.panel.querySelector('button').hidden,true);
    assert.match(h.panel.textContent,/Activity updated/);
  } finally {h.close();}
});

test('read-only activity requests only authorized sources and handles an empty history', async () => {
  const paths=[];
  const h=harness(async path => {paths.push(path);return response([]);},false);
  try {await flush();assert.deepEqual(paths,['/api/apps/app%2Fa/deployments?limit=4']);
    assert.match(h.panel.textContent,/No deployments/);assert.equal(h.panel.querySelectorAll('.app-activity-footer a').length,1);
  } finally {h.close();}
});

test('superseded body responses cannot replace newer results, and disposal aborts outstanding requests', async () => {
  let complete, signal, calls=0;
  const h=harness(async (_path,options) => {
    signal=options.signal;
    if (++calls===1) return {ok:true,status:200,json:() => new Promise(resolve => {complete=resolve;})};
    return response([{...release,release_number:4}]);
  },false);
  try {
    await flush(); await h.dispose.refresh();complete({items:[release]});await flush();
    assert.match(h.panel.textContent,/Release v4/);assert.doesNotMatch(h.panel.textContent,/Release v2/);
    h.dispose();assert.equal(signal.aborted,true);
  } finally {h.close();}
});

test('session expiry clears all previous results and signs out once', async () => {
  let expired=false;
  const h=harness(async () => response([release],expired ? 401 : 200));
  try {await flush();expired=true;await h.dispose.refresh();
    assert.equal(h.unauthorized(),1);assert.equal(h.panel.querySelectorAll('li').length,0);
    assert.match(h.panel.textContent,/session expired/);assert.equal(h.panel.hasAttribute('aria-busy'),false);
  } finally {h.close();}
});

test('leaving the page ignores a late response without touching the activity list', async () => {
  let resolve, signal;
  const h=harness((_path,options) => {signal=options.signal;return new Promise(r=>{resolve=r;});},false);
  h.dispose();resolve(response([release]));await flush();
  assert.equal(signal.aborted,true);assert.equal(h.panel.querySelectorAll('li').length,0);h.close();
});
