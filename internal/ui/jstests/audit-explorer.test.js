import {afterEach,beforeEach,test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {JSDOM} from 'jsdom';
import axe from 'axe-core';
import {createAuditExplorer} from '../static/views/audit-explorer.js';
import {auditSelection,auditViewPath,auditPage,auditValidation} from '../static/views/audit-log.js';
const html=readFileSync(new URL('../static/index.html',import.meta.url),'utf8').match(/<section id="audit-view"[\s\S]*?(?=\n    <section)/)[0];
const event={id:42,user_id:1,username:'admin',action:'update_app',resource_type:'app',resource_id:'forecast',created_at:'2026-11-03T12:30:45Z',detail:'{"memory":{"old":"256 MiB","new":"512 MiB"}}',ip_address:'127.0.0.1',run_id:'run/42'};
const payload=(events=[event],total=events.length,has_more=false)=>({events,total,has_more});
const response=(data,status=200)=>({ok:status>=200&&status<300,status,json:async()=>data});
const empty=()=>auditSelection('');
function deferred(){let resolve;return {promise:new Promise(r=>resolve=r),resolve};}
let dom,explorer,state,calls,paths,expired,copied;
const get=id=>dom.window.document.getElementById(id);
function setup(api=async()=>response(payload()),options={}){
 state={user:{id:1,role:'admin'},canReadAudit:true,sessionIdentity:{}};calls=[];paths=[];expired=0;copied=[];
 explorer=createAuditExplorer({document:dom.window.document,state,api:async(path,opts)=>{calls.push({path,opts});return api(path,opts);},
 navigate:(...args)=>paths.push(args),onUnauthorized:()=>expired++,copy:async value=>copied.push(value),origin:'http://localhost',...options});
 return explorer;
}
beforeEach(()=>{dom=new JSDOM('<!DOCTYPE html><html lang="en"><head><title>Audit</title></head><body><main>'+html+'</main></body></html>',{url:'http://localhost/audit-log',pretendToBeVisual:true,runScripts:'outside-only'});get('audit-view').hidden=false;});
afterEach(()=>{explorer?.stop();explorer=null;dom.window.close();});
test('recorded changes are readable and inspection expands one full-width detail panel',async()=>{
 setup();await explorer.load(0,empty());
 assert.equal(get('audit-body').querySelector('.audit-change').textContent,'1 field changed · Memory');
 const button=get('audit-body').querySelector('.audit-event-title');button.focus();button.click();
 assert.equal(dom.window.document.activeElement.getAttribute('aria-expanded'),'true');
 const detail=get('audit-detail-42');assert.equal(detail.closest('tr').hidden,false);assert.match(detail.textContent,/IP address127.0.0.1/);
 assert.match(detail.textContent,/Memory256 MiB → 512 MiB/);
 assert.match(detail.textContent,/12:30:45/);assert.equal(detail.querySelector('td'),null);
 assert.equal(detail.querySelector('.audit-related').getAttribute('href'),'/audit-log?run=run%2F42');
 detail.querySelector('button').click();await new Promise(r=>setImmediate(r));assert.deepEqual(copied,['http://localhost/audit-log?event=42']);
 dom.window.document.activeElement.click();assert.equal(get('audit-detail-42').closest('tr').hidden,true);
});

test('multi-field summaries name every changed field and count changes without clipping values into the row',async()=>{
 const detail={description:{old:'Old description',new:'New description'},name:{old:'Forecast',new:'Operations'},memory_limit_mb:{old:256,new:512},note:'Do not count → this'};
 setup(async()=>response(payload([{...event,detail:JSON.stringify(detail)}])));await explorer.load(0,empty());
 const row=get('audit-event-42');assert.equal(row.querySelector('.audit-change').textContent,'3 fields changed · Description, Name, Memory limit');
 assert.doesNotMatch(row.textContent,/Old description|New description|Do not count/);
 row.querySelector('button').click();
 assert.match(get('audit-detail-42').textContent,/DescriptionOld description → New description/);
 assert.match(get('audit-detail-42').textContent,/NameForecast → Operations/);
 assert.match(get('audit-detail-42').textContent,/Memory limit256 MiB → 512 MiB/);
});

test('a saved form counts actual changes and keeps unchanged submitted fields out of the summary',async()=>{
 const detail={description:{old:'Before',new:'After'},project_slug:{old:'acceptance',new:'acceptance'},replicas:{old:1,new:1}};
 setup(async()=>response(payload([{...event,detail:JSON.stringify(detail)}])));await explorer.load(0,empty());
 assert.equal(get('audit-event-42').querySelector('.audit-change').textContent,'1 field changed · Description');
});

for(const action of ['schedule_activation_outcome','support_session.start','support_session.stop'])test(`${action} recording failure stays visible in both row and details`,async()=>{
 setup(async()=>response(payload([{...event,action,detail:'{"detail_error":"Encoding failed"}'}])));await explorer.load(0,empty());
 assert.match(get('audit-event-42').textContent,/Some event details could not be recorded/);
 get('audit-event-42').querySelector('button').click();
 assert.match(get('audit-detail-42').textContent,/Detail unavailableEncoding failed/);
 assert.doesNotMatch(get('audit-detail-42').textContent,/No additional details were recorded/);
});

test('copy shows inline pending and success feedback, prevents duplicate writes and preserves focus',async()=>{
 const held=deferred();setup(undefined,{copy:value=>{copied.push(value);return held.promise;}});await explorer.load(0,auditSelection('?event=42'));
 const detail=get('audit-detail-42'),button=detail.querySelector('button'),status=detail.querySelector('.audit-copy-status');
 button.focus();button.click();button.click();
 assert.equal(button.textContent,'Copying…');assert.equal(button.getAttribute('aria-disabled'),'true');assert.equal(copied.length,1);
 held.resolve();await new Promise(r=>setImmediate(r));
 assert.equal(status.textContent,'Link copied.');assert.equal(status.getAttribute('role'),'status');assert.equal(status.closest('.sr-only'),null);
 assert.equal(status.classList.contains('is-error'),false);assert.equal(button.hasAttribute('aria-disabled'),false);
 assert.equal(dom.window.document.activeElement,button);
});

test('clipboard failure gives visible inline recovery and can be retried',async()=>{
 let fail=true;setup(undefined,{copy:async()=>{if(fail)throw new Error('Clipboard denied');}});await explorer.load(0,auditSelection('?event=42'));
 const detail=get('audit-detail-42'),button=detail.querySelector('button'),status=detail.querySelector('.audit-copy-status');
 button.click();await new Promise(r=>setImmediate(r));
 assert.match(status.textContent,/Could not copy.*Open the event number/);assert.equal(status.classList.contains('is-error'),true);
 assert.equal(status.closest('.sr-only'),null);assert.equal(button.textContent,'Copy event link');
 fail=false;button.click();await new Promise(r=>setImmediate(r));
 assert.equal(status.textContent,'Link copied.');assert.equal(status.classList.contains('is-error'),false);
});

for(const transition of ['collapse','query','stop','session'])test(`late clipboard completion cannot announce into a departed ${transition}`,async()=>{
 const held=deferred();setup(undefined,{copy:()=>held.promise});await explorer.load(0,auditSelection('?event=42'));
 const detail=get('audit-detail-42'),status=detail.querySelector('.audit-copy-status');detail.querySelector('button').click();
 if(transition==='collapse')get('audit-event-42').querySelector('button').click();
 if(transition==='query')await explorer.load(0,auditSelection('?q=next'));
 if(transition==='stop')explorer.stop();
 if(transition==='session')state.sessionIdentity={};
 held.resolve();await new Promise(r=>setImmediate(r));assert.equal(status.textContent,'');
 assert.doesNotMatch(get('audit-announcement').textContent,/copied/);
});
test('filters submit one combined server query and pagination preserves it in a shareable URL',async()=>{
 setup(async()=>response(payload([event],220,true)));await explorer.load(0,empty());
 get('audit-search').value='forecast';get('audit-action-filter').value='update_app';get('audit-since').value='2026-11-01';
 get('audit-filters').dispatchEvent(new dom.window.Event('submit',{cancelable:true}));
 assert.equal(paths[0][0],'/audit-log?q=forecast&action=update_app&since=2026-11-01');
 const selection=auditSelection('?q=forecast&action=update_app&since=2026-11-01');await explorer.load(1,selection);
 assert.match(calls.at(-1).path,/offset=100&q=forecast&action=update_app&since=2026-11-01/);
 get('audit-next').click();assert.equal(paths.at(-1)[0],'/audit-log?q=forecast&action=update_app&since=2026-11-01&page=3');
 assert.match(get('audit-range').textContent,/101-101 of 220/);
});
test('exact event links open details and replace browsing filters',async()=>{
 setup();await explorer.load(0,auditSelection('?event=42&q=other&action=login'));
 assert.equal(calls[0].path,'/api/audit?limit=100&offset=0&event=42');
 assert.equal(get('audit-detail-42').closest('tr').hidden,false);assert.equal(get('audit-pagination').hidden,true);
 assert.equal(get('audit-search').value,'');assert.equal(get('audit-action-filter').value,'');
});
test('refresh failure retains the loaded investigation, retry recovers, different queries never show old rows',async()=>{
 let fail=false;setup(async()=>{if(fail)throw new Error();return response(payload());});await explorer.load(0,empty());
 fail=true;await explorer.load(0,empty());assert.equal(get('audit-table').hidden,false);assert.match(get('audit-error').textContent,/last loaded/);
 await explorer.load(0,auditSelection('?q=missing'));assert.equal(get('audit-table').hidden,true);assert.equal(get('audit-empty').hidden,true);
 fail=false;await explorer.load(0,auditSelection('?q=missing'));assert.equal(get('audit-failure').hidden,true);
});
test('invalid shared calendar dates and inverted ranges do not silently broaden the investigation',async()=>{
 setup();for(const query of ['?since=2026-02-30','?since=2026-11-03&until=2026-11-01','?since=yesterday']){
 await explorer.load(0,auditSelection(query),query);assert.equal(calls.length,0);assert.equal(get('audit-validation').hidden,false);
 get('audit-refresh').click();await new Promise(r=>setImmediate(r));assert.equal(calls.length,0);
 }
});
test('invalid form date range retains results and offers a specific correction',async()=>{
 setup();await explorer.load(0,empty());get('audit-since').value='2026-11-03';get('audit-until').value='2026-11-01';
 get('audit-filters').dispatchEvent(new dom.window.Event('submit',{cancelable:true}));
 assert.equal(paths.length,0);assert.equal(get('audit-table').hidden,false);assert.match(get('audit-validation').textContent,/on or after/);
});
test('superseded reads are aborted, old responses cannot replace newer filters',async()=>{
 const old=deferred();setup((path)=>path.includes('q=old')?old.promise:response(payload([{...event,id:43}])));
 const pending=explorer.load(0,auditSelection('?q=old'));await explorer.load(0,auditSelection('?q=new'));
 assert.equal(calls[0].opts.signal.aborted,true);old.resolve(response(payload()));await pending;
 assert.ok(get('audit-event-43'));assert.equal(get('audit-event-42'),null);
});
test('leaving the route cancels pending reads and clears private events',async()=>{
 const held=deferred();setup(()=>held.promise);const pending=explorer.load(0,empty());explorer.stop();
 assert.equal(calls[0].opts.signal.aborted,true);held.resolve(response(payload()));await pending;
 assert.equal(get('audit-body').children.length,0);assert.equal(get('audit-table').hidden,true);
});
for(const status of [401,403])test(`HTTP ${status} clears access and uses the proper recovery`,async()=>{
 setup(async()=>response({},status));await explorer.load(0,empty());assert.equal(get('audit-body').children.length,0);
 assert.equal(expired,status===401?1:0);assert.equal(paths.length,status===403?1:0);
});
test('same-account capability loss clears events immediately',async()=>{
 setup();await explorer.load(0,empty());state.canReadAudit=false;dom.window.document.dispatchEvent(new dom.window.Event('session-capabilities-change'));
 assert.equal(get('audit-body').children.length,0);assert.deepEqual(paths,[['/',{replace:true}]]);
});
test('session replacement ignores late responses',async()=>{
 const held=deferred();setup(()=>held.promise);const pending=explorer.load(0,empty());state.sessionIdentity={};held.resolve(response(payload()));await pending;
 assert.equal(get('audit-body').children.length,0);
});
for(const data of [{},{events:{},total:0,has_more:false},payload([null]),payload([{...event,id:'x'}])])test('malformed data cannot masquerade as an empty audit history '+JSON.stringify(data),async()=>{
 setup(async()=>response(data));await explorer.load(0,empty());assert.equal(get('audit-failure').hidden,false);assert.equal(get('audit-empty').hidden,true);
});
test('unknown timestamps, anonymous actors, recording errors and hostile text are safe',async()=>{
 setup(async()=>response(payload([{...event,username:null,user_id:null,created_at:'bad',resource_id:'<img src=x onerror=alert(1)>',detail:'{"detail_error":"Unavailable"}'}])));
 await explorer.load(0,empty());assert.match(get('audit-body').textContent,/Time unavailable/);assert.match(get('audit-body').textContent,/System or unauthenticated/);
 assert.match(get('audit-body').textContent,/could not be recorded/);assert.equal(get('audit-body').querySelector('img'),null);
});
test('a stalled JSON body has a bounded wait and retry path',async()=>{
 let timeout;setup(async()=>({ok:true,status:200,json:()=>new Promise(()=>{})}),{requestOptions:{setTimer:fn=>{timeout=fn;return 1;},clearTimer(){}}});
 const pending=explorer.load(0,empty());await new Promise(r=>setImmediate(r));timeout();await pending;
 assert.equal(get('audit-loading').hidden,true);assert.equal(get('audit-failure').hidden,false);
});
test('empty, missing event and unmatched filters communicate different states',async()=>{
 setup(async()=>response(payload([])));await explorer.load(0,empty());assert.equal(get('audit-empty-heading').textContent,'No events yet');
 await explorer.load(0,auditSelection('?q=nothing'));assert.equal(get('audit-empty-heading').textContent,'No matching events');
 await explorer.load(0,auditSelection('?event=100'));assert.equal(get('audit-empty-heading').textContent,'Event not available');
});
test('view paths, UTC date validity, byte limits and page restoration',()=>{
 assert.equal(auditViewPath(auditSelection('?q=forecast&since=2026-11-01'),2),'/audit-log?q=forecast&since=2026-11-01&page=3');
 assert.equal(auditPage('?page=3'),2);assert.equal(auditPage('?page=no'),0);assert.equal(auditPage('?page=-3'),0);
 assert.match(auditValidation({q:'東京'.repeat(50)}),/200/);assert.equal(auditValidation({since:'2024-02-29'}),'');
 assert.match(auditValidation({since:'2025-02-29'}),/valid/);
});
test('loaded and expanded investigation has no detectable accessibility violations',async()=>{
 setup();await explorer.load(0,auditSelection('?event=42'));dom.window.eval(axe.source);
 const result=await dom.window.axe.run(dom.window.document,{rules:{'color-contrast':{enabled:false}}});assert.equal(result.violations.length,0,JSON.stringify(result.violations.map(v=>v.id)));
});

test('query transitions focus the new investigation while refresh preserves the active row',async()=>{
 setup();await explorer.load(0,empty());
 const button=get('audit-body').querySelector('button');button.focus();await explorer.load(0,empty());
 assert.equal(dom.window.document.activeElement.dataset.auditFocus,'42');
 await explorer.load(0,auditSelection('?event=42'));assert.equal(dom.window.document.activeElement.id,'audit-heading');
 assert.equal(get('audit-filters').hidden,true);
});

test('Escape closes event details and restores focus to its inspection button',async()=>{
 setup();await explorer.load(0,auditSelection('?event=42'));
 const copy=get('audit-detail-42').querySelector('button');copy.focus();
 copy.dispatchEvent(new dom.window.KeyboardEvent('keydown',{key:'Escape',bubbles:true}));
 assert.equal(get('audit-detail-42').closest('tr').hidden,true);
 assert.equal(dom.window.document.activeElement.dataset.auditFocus,'42');
 assert.match(dom.window.document.activeElement.textContent,/App · forecast.*event #42/);
});
