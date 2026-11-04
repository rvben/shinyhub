import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { JSDOM } from 'jsdom';
import { mountAnnouncements } from '../static/views/announcements.js';
const source = await readFile(new URL('../../announcementui/assets/banner.js',import.meta.url),'utf8');
const flush = () => new Promise(resolve=>setTimeout(resolve,20));
function reader() {
  const dom = new JSDOM('<html><body><h1 tabindex="-1">App</h1><script data-announcements-url="/app/demo/.shinyhub/announcements.json"></script></body></html>',{url:'https://apps.example.test/app/demo/',runScripts:'outside-only',pretendToBeVisual:true});
  const w=dom.window;const roots=new WeakMap();const original=w.Element.prototype.attachShadow;
  w.Element.prototype.attachShadow=function(options){const root=original.call(this,options);roots.set(this,root);return root;};
  Object.defineProperty(w.document,'currentScript',{value:w.document.querySelector('script')});
  return {dom,w,root:()=>roots.get(w.document.getElementById('shinyhub-platform-announcements'))};
}
function notice(overrides={}) {return {id:'notice',title:'Maintenance',message:'Save your work',severity:'warning',display_revision:1,dismissible:true,ends_at:null,...overrides};}
function feed(announcements) {return {announcements,server_time:new Date().toISOString(),next_transition:null};}
test('open app receives notices, remembers dismissal, and shows edited revisions without HTML execution',async(t)=>{
  const r=reader();t.after(()=>r.dom.window.close());let items=[];let fail=false;
  r.w.fetch=async()=>{if(fail)throw new Error('offline');return {ok:true,json:async()=>feed(items)}};
  r.w.eval(source);await flush();const host=r.w.document.getElementById('shinyhub-platform-announcements');assert.equal(host.hidden,true);
  items=[notice()];r.w.dispatchEvent(new r.w.Event('shinyhub:announcements-changed'));await flush();assert.equal(host.hidden,false);
  const root=r.root();assert.equal(root.querySelector('h2').textContent,'Maintenance');root.querySelector('[data-dismiss]').click();assert.equal(host.hidden,true);
  r.w.dispatchEvent(new r.w.Event('shinyhub:announcements-changed'));await flush();assert.equal(host.hidden,true);
  items=[notice({display_revision:2,message:'<img src=x onerror=alert(1)>'})];r.w.dispatchEvent(new r.w.Event('shinyhub:announcements-changed'));await flush();assert.equal(host.hidden,false);assert.equal(root.querySelector('img'),null);assert.match(root.textContent,/<img/);
  fail=true;r.w.dispatchEvent(new r.w.Event('shinyhub:announcements-changed'));await flush();assert.equal(host.hidden,false,'failure does not claim there are no notices');
  fail=false;items=[];r.w.dispatchEvent(new r.w.Event('shinyhub:announcements-changed'));await flush();assert.equal(host.hidden,true);r.dom.window.close();
});
test('critical notices cannot be dismissed, overlap expands, expiry and body replacement recover',async(t)=>{
  const r=reader();t.after(()=>r.dom.window.close());const end=new Date(Date.now()+500).toISOString();let items=[notice({id:'critical',severity:'critical',dismissible:false,ends_at:end}),notice({id:'info',severity:'information',details_url:'javascript:alert(1)'})];
  r.w.fetch=async()=>({ok:true,json:async()=>feed(items)});r.w.eval(source);await flush();const root=r.root();assert.equal(root.querySelector('.notice > .primary').querySelector('button'),null);assert.equal(root.querySelector('a'),null);
  root.querySelector('.more').click();assert.equal(root.querySelector('.others').hidden,false);
  const host=r.w.document.getElementById('shinyhub-platform-announcements');host.remove();r.w.document.body.replaceChildren(r.w.document.createElement('main'));await flush();assert.equal(host.parentNode,r.w.document.documentElement);
  await new Promise(resolve=>setTimeout(resolve,550));assert.equal(root.querySelector('[data-dismiss]').dataset.dismiss,'info');r.dom.window.close();
});
test('offline feed retains the last notice until expiry using server time',async(t)=>{
  const r=reader();t.after(()=>r.dom.window.close());let offline=false;
  const serverTime=Date.now()+3600000;
  const end=new Date(serverTime+800).toISOString();
  r.w.fetch=async()=>{if(offline)throw new Error('offline');return {ok:true,json:async()=>({announcements:[notice({ends_at:end})],server_time:new Date(serverTime).toISOString(),next_transition:null})}};
  r.w.eval(source);await flush();const host=r.w.document.getElementById('shinyhub-platform-announcements');assert.equal(host.hidden,false);
  offline=true;r.w.dispatchEvent(new r.w.Event('shinyhub:announcements-changed'));await flush();
  assert.equal(host.hidden,false,'offline read retains an unexpired notice');
  await new Promise(resolve=>setTimeout(resolve,850));
  assert.equal(host.hidden,true,'notice expires locally while feed is still offline');
});
test('dismissal still works when browser storage is unavailable',async(t)=>{
  const r=reader();t.after(()=>r.dom.window.close());Object.defineProperty(r.w,'localStorage',{get(){throw new Error('blocked')}});r.w.fetch=async()=>({ok:true,json:async()=>feed([notice()])});r.w.eval(source);await flush();r.root().querySelector('button').click();assert.equal(r.w.document.getElementById('shinyhub-platform-announcements').hidden,true);r.dom.window.close();
});
function admin(t,api,options={}) {
  const dom=new JSDOM('<html><body><section id="announcements-view"></section></body></html>',{url:'https://hub.example.test/announcements',pretendToBeVisual:true});
  const old={};for(const key of ['window','document','location','FormData','MutationObserver','Event']){old[key]=globalThis[key];globalThis[key]=dom.window[key];}
  t.after(()=>{dom.window.close();for(const key of Object.keys(old)){if(old[key]===undefined)delete globalThis[key];else globalThis[key]=old[key];}});
  const controller=mountAnnouncements({document:dom.window.document,api,preview:()=>{},confirm:()=>false,...options});return {controller,doc:dom.window.document,w:dom.window};
}
test('successful publication leaves editor clean and saving failure preserves edits',async(t)=>{
  let savedBody,fail=false;
  const r=admin(t,async(path,options)=>{
    if(!options?.method)return {ok:true,json:async()=>({announcements:[],has_more:false})};savedBody=JSON.parse(options.body);
    return fail?{ok:false,status:409,json:async()=>({error:'conflict'})}:{ok:true,json:async()=>({...notice(),...savedBody,id:'saved',status:'active',publication:'published',revision:1,updated_at:new Date().toISOString()})};
  });await flush();r.doc.querySelector('[data-new]').click();
  const form=r.doc.querySelector('form');form.elements.title.value='Planned maintenance';form.elements.message.value='Save work';form.dispatchEvent(new r.w.Event('input',{bubbles:true}));assert.equal(r.controller.isDirty(),true);assert.equal(r.controller.allowLeave(),false);
  form.elements.delivery.value='now';form.dispatchEvent(new r.w.Event('submit',{bubbles:true,cancelable:true}));await flush();assert.equal(savedBody.publication,'published');assert.equal(r.controller.isDirty(),false,'disabled controls must not produce a false dirty baseline');
  form.elements.message.value='My unsaved edit';form.dispatchEvent(new r.w.Event('input',{bubbles:true}));fail=true;form.dispatchEvent(new r.w.Event('submit',{bubbles:true,cancelable:true}));await flush();assert.equal(form.elements.message.value,'My unsaved edit');assert.equal(r.controller.isDirty(),true);assert.equal(r.doc.querySelector('[data-reload]').hidden,false);assert.equal(form.elements.message.disabled,false);r.controller.unmount();
});

const managed = (overrides={}) => ({...notice(),publication:'draft',status:'draft',revision:1,created_at:new Date().toISOString(),updated_at:new Date().toISOString(),...overrides});
const result = (body,status=200) => ({ok:status>=200&&status<300,status,json:async()=>body});
const edit = r => {r.doc.querySelector('[data-new]').click();const form=r.doc.querySelector('form');form.elements.title.value='Planned maintenance';form.elements.message.value='Save your work before maintenance.';form.dispatchEvent(new r.w.Event('input',{bubbles:true}));return form;};
const submitEditor = (r,form) => form.dispatchEvent(new r.w.Event('submit',{bubbles:true,cancelable:true}));

test('inventory remains intact after failed refresh and refresh never steals list focus',async t=>{
  let fail=false;
  const r=admin(t,async()=>{if(fail)throw new Error('offline');return result({announcements:[managed()],has_more:false});});
  await flush();const row=r.doc.querySelector('[data-announcement-id]');row.focus();
  r.doc.querySelector('[data-refresh]').click();await flush();assert.equal(r.doc.activeElement.dataset.announcementId,'notice');
  const retained=r.doc.querySelector('[data-announcement-id]');fail=true;r.doc.querySelector('[data-refresh]').click();await flush();
  assert.equal(r.doc.querySelector('[data-announcement-id]'),retained);assert.match(r.doc.querySelector('[data-list-error]').textContent,/last loaded list/);assert.equal(r.doc.querySelector('.announcements-empty'),null);
  r.controller.unmount();
});

test('one publication choice validates scheduling and preserves the chosen window',async t=>{
  let sent;
  const r=admin(t,async(_,options)=>options?.method?(sent=JSON.parse(options.body),result(managed({...sent,id:'scheduled',status:'scheduled'}))):result({announcements:[],has_more:false}));
  await flush();assert.equal(r.doc.querySelector('[data-editor-panel]').hidden,true);const form=edit(r);form.elements.delivery.value='schedule';form.dispatchEvent(new r.w.Event('change',{bubbles:true}));
  assert.equal(r.doc.querySelector('[data-inventory]').hidden,true);assert.equal(r.doc.querySelector('[data-start-field]').hidden,false);
  form.elements.starts_at.value='2020-01-01T12:00';submitEditor(r,form);await flush();assert.equal(sent,undefined);assert.match(r.doc.querySelector('[data-editor-error]').textContent,/future/);
  const later=new Date(Date.now()+86400000);form.elements.starts_at.value=`${later.getFullYear()}-${String(later.getMonth()+1).padStart(2,'0')}-${String(later.getDate()).padStart(2,'0')}T12:00`;
  submitEditor(r,form);await flush();assert.equal(sent.publication,'published');assert.ok(Date.parse(sent.starts_at)>Date.now());assert.equal(r.controller.isDirty(),false);
  r.doc.querySelector('[data-close]').click();assert.equal(r.doc.querySelector('[data-inventory]').hidden,false);assert.equal(r.doc.activeElement.dataset.announcementId,'scheduled');
  r.controller.unmount();
});

test('lost successful response retries the exact request without duplicating a creation',async t=>{
  let stored=null;const attempts=[];
  const r=admin(t,async(_,options)=>{
    if(!options?.method)return result({announcements:[],has_more:false});
    attempts.push(options);
    if(!stored){stored=managed({...JSON.parse(options.body),id:'created'});throw new Error('connection lost after write');}
    return result(stored);
  });await flush();const form=edit(r);submitEditor(r,form);submitEditor(r,form);await flush();assert.equal(attempts.length,1);
  assert.equal(r.doc.querySelector('[data-save]').hidden,true);assert.equal(r.doc.querySelector('[data-retry]').hidden,false);assert.equal(form.elements.title.disabled,true);assert.equal(r.controller.isDirty(),true);
  assert.equal(r.doc.activeElement,r.doc.querySelector('[data-retry]'));
  r.doc.querySelector('[data-retry]').click();await flush();
  assert.equal(attempts.length,2);assert.equal(attempts[1].body,attempts[0].body);assert.equal(attempts[1].headers['Idempotency-Key'],attempts[0].headers['Idempotency-Key']);
  assert.ok(attempts[0].headers['Idempotency-Key']);assert.equal(r.doc.querySelector('[data-save]').hidden,false);assert.equal(form.elements.title.disabled,false);assert.equal(r.controller.isDirty(),false);assert.match(r.doc.querySelector('[data-success]').textContent,/saved/);
  r.controller.unmount();
});

test('a save that never reached the server remains retryable through repeated failures',async t=>{
  let posts=0,commits=0;const keys=[],bodies=[];
  const r=admin(t,async(_,options)=>{
    if(!options?.method)return result({announcements:[],has_more:false});
    posts++;keys.push(options.headers['Idempotency-Key']);bodies.push(options.body);
    if(posts===1)throw new Error('disconnected before write');
    if(posts===2)return result({error:'Temporarily unavailable'},503);
    commits++;return result(managed({...JSON.parse(options.body),id:'created',revision:commits}));
  });await flush();const form=edit(r);submitEditor(r,form);await flush();
  assert.equal(form.elements.message.value,'Save your work before maintenance.');
  r.doc.querySelector('[data-retry]').click();await flush();assert.equal(r.doc.querySelector('[data-retry]').disabled,false);assert.equal(commits,0);
  r.doc.querySelector('[data-retry]').click();await flush();assert.equal(commits,1);assert.equal(new Set(keys).size,1);assert.equal(new Set(bodies).size,1);assert.equal(r.controller.isDirty(),false);
  form.elements.message.value='A fresh change';form.dispatchEvent(new r.w.Event('input',{bubbles:true}));submitEditor(r,form);await flush();assert.equal(commits,2);assert.notEqual(keys[3],keys[0]);assert.equal(form.elements.message.disabled,false);
  r.controller.unmount();
});

test('a definitive rejection after an uncertain request restores editable fields',async t=>{
  let writes=0;
  const r=admin(t,async(_,options)=>{
    if(!options?.method)return result({announcements:[],has_more:false});
    return ++writes===1?Promise.reject(new Error('offline')):result({error:'Correct the details link.'},400);
  });await flush();const form=edit(r);submitEditor(r,form);await flush();r.doc.querySelector('[data-retry]').click();await flush();
  assert.equal(form.elements.title.disabled,false);assert.equal(r.doc.querySelector('[data-save]').hidden,false);assert.equal(r.doc.querySelector('[data-retry]').hidden,true);assert.equal(r.controller.isDirty(),true);assert.match(r.doc.querySelector('[data-editor-error]').textContent,/Correct the details link/);
  r.controller.unmount();
});

test('emoji limits match the backend character count and retain over-limit input for correction',async t=>{
  const bodies=[];
  const r=admin(t,async(_,options)=>{
    if(!options?.method)return result({announcements:[],has_more:false});
    bodies.push(JSON.parse(options.body));return result(managed({...bodies.at(-1),id:'emoji'}));
  });await flush();const form=edit(r);
  assert.equal(form.elements.title.getAttribute('maxlength'),null);assert.equal(form.elements.message.getAttribute('maxlength'),null);
  form.elements.title.value='😀'.repeat(121);form.dispatchEvent(new r.w.Event('input',{bubbles:true}));submitEditor(r,form);await flush();
  assert.equal(bodies.length,0);assert.equal(form.elements.title.value,'😀'.repeat(121));assert.equal(form.elements.title.getAttribute('aria-invalid'),'true');assert.match(r.doc.querySelector('#announcement-title-count').textContent,/121 \/ 120/);assert.equal(r.doc.querySelector('#announcement-title-error').hidden,false);
  form.elements.title.value='😀'.repeat(120);form.elements.message.value='🌍'.repeat(601);form.dispatchEvent(new r.w.Event('input',{bubbles:true}));submitEditor(r,form);await flush();assert.equal(bodies.length,0);assert.equal(form.elements.message.getAttribute('aria-invalid'),'true');
  form.elements.message.value='🌍'.repeat(600);form.dispatchEvent(new r.w.Event('input',{bubbles:true}));assert.equal(form.checkValidity(),true);submitEditor(r,form);await flush();
  assert.equal(bodies.length,1);assert.equal(Array.from(bodies[0].title).length,120);assert.equal(Array.from(bodies[0].message).length,600);assert.equal(form.elements.title.hasAttribute('aria-invalid'),false);assert.equal(r.doc.querySelector('#announcement-message-error').hidden,true);assert.equal(r.controller.isDirty(),false);
  r.controller.unmount();
});

test('preview wording stays accurate for both new and already published notices',async t=>{
  const active=managed({status:'active',publication:'published',starts_at:new Date(Date.now()-60000).toISOString()});
  const r=admin(t,async()=>result({announcements:[active],has_more:false}));await flush();r.doc.querySelector('[data-new]').click();
  assert.equal(r.doc.querySelector('[data-preview-note]').textContent,'Unsaved changes appear only in this preview.');r.doc.querySelector('[data-close]').click();r.doc.querySelector('[data-announcement-id]').click();
  assert.equal(r.doc.querySelector('[data-preview-note]').textContent,'Unsaved changes appear only in this preview.');assert.equal(r.doc.querySelector('[data-live-note]').hidden,false);r.controller.unmount();
});

test('conflicts prevent stale resubmission and keep the draft intact',async t=>{
  let writes=0;
  const r=admin(t,async(_,options)=>options?.method?(writes++,result({error:'conflict'},409)):result({announcements:[managed()],has_more:false}));
  await flush();r.doc.querySelector('[data-announcement-id]').click();const form=r.doc.querySelector('form');form.elements.message.value='Keep my correction';submitEditor(r,form);await flush();
  assert.equal(form.elements.message.value,'Keep my correction');assert.equal(r.doc.querySelector('[data-save]').disabled,true);assert.equal(r.doc.querySelector('[data-reload]').hidden,false);submitEditor(r,form);await flush();assert.equal(writes,1);
  r.controller.unmount();
});

test('unmount aborts pending reads and rejects late list results',async t=>{
  let finish,signal;
  const r=admin(t,async(_,options)=>{signal=options.signal;return new Promise(resolve=>finish=resolve);});
  r.controller.unmount();assert.equal(signal.aborted,true);finish(result({announcements:[managed()],has_more:false}));await flush();assert.equal(r.doc.querySelector('[data-announcement-id]'),null);assert.equal(r.doc.querySelector('#announcements-view').hidden,true);
});

test('announcement management belongs to Administration, outside account and help',async()=>{
  const html=await readFile(new URL('../static/index.html',import.meta.url),'utf8');const dom=new JSDOM(html);const doc=dom.window.document;
  assert.ok(doc.querySelector('#primary-nav #administration-nav #tab-announcements'));assert.equal(doc.querySelector('.sidebar-footer #tab-announcements'),null);assert.equal(doc.querySelector('#administration-nav').getAttribute('aria-label'),'Administration');assert.equal(doc.querySelector('#administration-nav').hidden,true);dom.window.close();
});

test('visibility boundaries update locally without a refresh and release their timer on unmount',async t=>{
  let current=Date.parse('2026-11-03T12:00:00Z'),callback,reads=0,cleared=0;
  const r=admin(t,async()=>{reads++;return result({announcements:[managed({publication:'published',status:'active',starts_at:'2026-11-03T11:00:00Z',ends_at:'2026-11-03T12:00:02Z'})],has_more:false});},{clock:{now:()=>current,setTimer:fn=>{callback=fn;return 1;},clearTimer:()=>cleared++}});
  await flush();const filter=r.doc.querySelector('#announcement-filter');filter.value='all';filter.dispatchEvent(new r.w.Event('change'));r.doc.querySelector('[data-announcement-id]').focus();
  current+=3000;callback();assert.match(r.doc.querySelector('.announcement-status').textContent,/Expired/);assert.equal(r.doc.activeElement.dataset.announcementId,'notice');assert.equal(reads,1);assert.ok(cleared>0);r.controller.unmount();
});


test('an old draft can be archived without inventing a new publication start',async t=>{
  let sent;
  const draft=managed({starts_at:null,ends_at:'2020-01-01T12:00:00Z'});
  const r=admin(t,async(_,options)=>options?.method?(sent=JSON.parse(options.body),result({...draft,...sent,status:'archived'})):result({announcements:[draft],has_more:false}));
  await flush();r.doc.querySelector('[data-announcement-id]').click();r.doc.querySelector('[data-archive]').click();await flush();
  assert.equal(sent.publication,'archived');assert.equal(sent.starts_at,null);assert.equal(sent.ends_at,draft.ends_at);assert.equal(r.controller.isDirty(),false);r.controller.unmount();
});


test('creating an announcement advances pagination without repeating the last loaded row',async t=>{
  const paths=[];const initial=Array.from({length:50},(_,n)=>managed({id:'old-'+n,title:'Notice '+n}));
  const r=admin(t,async(path,options)=>{
    if(options?.method)return result(managed({...JSON.parse(options.body),id:'created'}));
    paths.push(path);return result({announcements:paths.length===1?initial:[managed({id:'older',title:'Older notice'})],has_more:paths.length===1});
  });await flush();const form=edit(r);submitEditor(r,form);await flush();r.doc.querySelector('[data-close]').click();r.doc.querySelector('[data-more]').click();await flush();
  assert.match(paths[1],/offset=51$/);assert.equal(r.doc.querySelectorAll('[data-announcement-id]').length,52);r.controller.unmount();
});


test('retrying a completed publication after expiry reports its current visibility',async t=>{
  let timestamp=Date.now(),stored;
  const r=admin(t,async(_,options)=>{
    if(!options?.method)return result({announcements:[],has_more:false});
    if(!stored){stored=managed({...JSON.parse(options.body),id:'expired',publication:'published',status:'active',starts_at:new Date(timestamp-60000).toISOString(),ends_at:new Date(timestamp+60000).toISOString()});throw new Error('lost publication response');}
    return result(stored);
  },{clock:{now:()=>timestamp,setTimer:()=>1,clearTimer:()=>{}}});
  await flush();const form=edit(r);submitEditor(r,form);await flush();timestamp+=120000;r.doc.querySelector('[data-retry]').click();await flush();
  assert.equal(r.doc.querySelector('[data-live-note]').hidden,true);assert.doesNotMatch(r.doc.querySelector('[data-success]').textContent,/published/);
  r.doc.querySelector('[data-close]').click();const filter=r.doc.querySelector('#announcement-filter');filter.value='all';filter.dispatchEvent(new r.w.Event('change'));assert.equal(r.doc.querySelector('.announcement-status').textContent,'Expired');r.controller.unmount();
});


test('retrying a completed schedule after its start reports the notice as live',async t=>{
  let timestamp=Date.now(),stored;
  const r=admin(t,async(_,options)=>{
    if(!options?.method)return result({announcements:[],has_more:false});
    if(!stored){stored=managed({...JSON.parse(options.body),id:'started',publication:'published',status:'scheduled',starts_at:new Date(timestamp+60000).toISOString(),ends_at:new Date(timestamp+3600000).toISOString()});throw new Error('lost scheduling response');}
    return result(stored);
  },{clock:{now:()=>timestamp,setTimer:()=>1,clearTimer:()=>{}}});
  await flush();const form=edit(r);submitEditor(r,form);await flush();timestamp+=120000;r.doc.querySelector('[data-retry]').click();await flush();
  assert.equal(r.doc.querySelector('[data-live-note]').hidden,false);assert.match(r.doc.querySelector('[data-success]').textContent,/published/);assert.equal(form.elements.delivery.selectedOptions[0].textContent,'Keep published');r.controller.unmount();
});
