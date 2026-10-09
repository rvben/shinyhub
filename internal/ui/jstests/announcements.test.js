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
function admin(t,api) {
  const dom=new JSDOM('<html><body><section id="announcements-view"></section></body></html>',{url:'https://hub.example.test/announcements'});
  const old={};for(const key of ['window','document','location','FormData','MutationObserver','Event']){old[key]=globalThis[key];globalThis[key]=dom.window[key];}
  t.after(()=>{dom.window.close();for(const key of Object.keys(old)){if(old[key]===undefined)delete globalThis[key];else globalThis[key]=old[key];}});
  const controller=mountAnnouncements({document:dom.window.document,api,preview:()=>{},confirm:()=>false});return {controller,doc:dom.window.document,w:dom.window};
}
test('successful publication leaves editor clean and saving failure preserves edits',async(t)=>{
  let savedBody,fail=false;
  const r=admin(t,async(path,options)=>{
    if(!options)return {ok:true,json:async()=>({announcements:[],has_more:false})};savedBody=JSON.parse(options.body);
    return fail?{ok:false,status:409,json:async()=>({error:'conflict'})}:{ok:true,json:async()=>({...notice(),...savedBody,id:'saved',status:'active',publication:'published',revision:1,updated_at:new Date().toISOString()})};
  });await flush();r.doc.querySelector('[data-new]').click();
  const form=r.doc.querySelector('form');form.elements.title.value='Planned maintenance';form.elements.message.value='Save work';form.dispatchEvent(new r.w.Event('input',{bubbles:true}));assert.equal(r.controller.isDirty(),true);assert.equal(r.controller.allowLeave(),false);
  r.doc.querySelector('[data-publish]').click();await flush();assert.equal(savedBody.publication,'published');assert.equal(r.controller.isDirty(),false,'disabled controls must not produce a false dirty baseline');
  form.elements.message.value='My unsaved edit';form.dispatchEvent(new r.w.Event('input',{bubbles:true}));fail=true;form.dispatchEvent(new r.w.Event('submit',{bubbles:true,cancelable:true}));await flush();assert.equal(form.elements.message.value,'My unsaved edit');assert.equal(r.controller.isDirty(),true);assert.equal(r.doc.querySelector('[data-reload]').hidden,false);assert.equal(form.elements.message.disabled,false);r.controller.unmount();
});
