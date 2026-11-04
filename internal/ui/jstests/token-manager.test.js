import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {JSDOM} from 'jsdom';
import {createTokenManager} from '../static/views/token-manager.js';

const html = readFileSync(new URL('../static/index.html',import.meta.url),'utf8');
const item = (id = 1, name = 'laptop') => ({id,name,created_at:'2026-11-01T12:00:00Z',expires_at:'2027-02-01T12:00:00Z',last_used_at:null});
const created = {...item(),token:'shk_test_secret_in_memory_only'};
const response = (body, status = 200) => ({ok:status >= 200 && status < 300,status,json:async()=>body});
const held = () => { let resolve; return {promise:new Promise(r=>resolve=r),resolve:value=>resolve(value)}; };
async function settle(predicate) {
  for (let i = 0; i < 60; i++) { if (predicate()) return; await new Promise(r=>setImmediate(r)); }
  assert.fail('Token view did not settle');
}
function fixture(t, handler, options = {}) {
  const dom = new JSDOM(html,{url:'http://localhost/tokens',pretendToBeVisual:true});
  const document = dom.window.document, get = id=>document.getElementById(id), requests = [], copies = [];
  const state = {user:{id:1,role:'developer',can_manage_self:true},sessionIdentity:{}};
  let activated = 0, released = 0, unauthorized = 0;
  const manager = createTokenManager({document,state,
    api:async(path,init={})=>{requests.push({path,...init});return handler(path,init);},
    activate:()=>activated++,release:()=>released++,onUnauthorized:()=>unauthorized++,
    copy:async value=>{copies.push(value);},...options});
  t.after(()=>{manager.stop();dom.window.close();});
  const submit = () => get('new-token-form').dispatchEvent(new dom.window.Event('submit',{cancelable:true}));
  const connect = () => get('cli-connect-form').dispatchEvent(new dom.window.Event('submit',{cancelable:true}));
  return {dom,document,get,manager,state,requests,copies,submit,connect,
    activated:()=>activated,released:()=>released,unauthorized:()=>unauthorized};
}

test('inventory presents expiry and usage, retaining focused actions across refresh',async t=>{
  const f=fixture(t,async()=>response({items:[item()]})); await f.manager.start();
  assert.match(f.get('tokens-count').textContent,/1 token/);
  assert.match(f.get('tokens-list').textContent,/Feb 1, 2027/);
  const button=f.document.querySelector('[data-token-id]'); button.focus();
  await f.manager.load();
  assert.equal(f.document.activeElement.dataset.tokenId,'1');
  assert.equal(f.get('cli-connect-panel').open,false);
});
test('a retained empty inventory stays visible during failed refresh and retry',async t=>{
  let fail=false; const f=fixture(t,async()=>{if(fail)throw new Error();return response({items:[]});});
  await f.manager.start(); fail=true; await f.manager.load();
  assert.ok(f.document.querySelector('[data-tokens-empty]')); assert.match(f.get('tokens-error').textContent,/last loaded/);
  fail=false; await f.manager.load(); assert.equal(f.get('tokens-error').hidden,true);
  f.document.querySelector('[data-token-connect]').click();
  assert.equal(f.get('cli-connect-panel').open,true); assert.equal(f.document.activeElement,f.get('cli-connect-code-input'));
});
test('malformed and initial failed reads never masquerade as an empty inventory',async t=>{
  let payload={items:[null]}; const f=fixture(t,async()=>response(payload));
  await f.manager.start(); assert.equal(f.get('tokens-error').hidden,false); assert.equal(f.document.querySelector('[data-tokens-empty]'),null);
  payload={items:[item()]}; await f.manager.load(); assert.equal(f.get('tokens-error').hidden,true);
});
test('creation sends trimmed name and chosen expiry, focuses copy and clears the secret on close',async t=>{
  let exists=false; const f=fixture(t,async(_,init)=>{
    if(init.method==='POST'){exists=true;return response(created,201);}return response({items:exists?[item()]:[]});
  }); await f.manager.start(); f.manager.open();
  f.get('new-token-name').value=' laptop '; f.get('new-token-expiry').value='30'; f.submit();
  await settle(()=>!f.get('token-reveal').hidden);
  assert.deepEqual(JSON.parse(f.requests.find(r=>r.method==='POST').body),{name:'laptop',expires_in_days:30});
  assert.equal(f.document.activeElement,f.get('token-reveal-copy'));
  assert.equal(f.get('new-token-heading').textContent,'Token created');
  f.get('token-reveal-copy').click(); await settle(()=>f.copies.length===1 && !f.get('token-reveal-copy').disabled);
  assert.equal(f.copies[0],created.token); assert.match(f.get('token-reveal-status').textContent,/Token copied/);
  f.get('token-reveal-done').click(); assert.equal(f.get('new-token-modal').hidden,true);
  assert.equal(f.get('token-reveal-value').textContent,''); assert.equal(f.get('token-reveal-status').textContent,'');
  f.manager.open(); assert.equal(f.get('token-reveal').hidden,true); assert.equal(f.get('new-token-heading').textContent,'New API token');
});
test('pending creation blocks duplicate submission, dismissal and navigation',async t=>{
  const pending=held(); const f=fixture(t,async(_,init)=>init.method==='POST'?pending.promise:response({items:[]}));
  await f.manager.start(); f.manager.open(); f.get('new-token-name').value='laptop'; f.submit(); f.submit();
  assert.equal(f.requests.filter(r=>r.method==='POST').length,1);
  assert.equal(f.get('new-token-close').disabled,true); assert.equal(f.get('new-token-cancel').disabled,true);
  assert.equal(f.manager.allowLeave(),false); f.manager.close(); assert.equal(f.get('new-token-modal').hidden,false);
  pending.resolve(response(created,201)); await settle(()=>!f.manager.isPending());
  assert.equal(f.manager.allowLeave(),false); assert.equal(f.get('new-token-close').disabled,false);
  f.get('token-reveal-done').click(); assert.equal(f.manager.allowLeave(),true);
});
test('duplicate token names preserve the draft and focus its correction',async t=>{
  const f=fixture(t,async(_,init)=>init.method==='POST'?response({},409):response({items:[]}));
  await f.manager.start(); f.manager.open(); f.get('new-token-name').value='laptop'; f.submit();
  await settle(()=>!f.manager.isPending());
  assert.match(f.get('new-token-error').textContent,/Choose another name/); assert.equal(f.get('new-token-name').value,'laptop');
  assert.equal(f.document.activeElement,f.get('new-token-name')); assert.equal(f.get('new-token-submit').disabled,false);
});
test('unknown creation outcome requires checking metadata and offers recovery for a lost secret',async t=>{
  let exists=false; const f=fixture(t,async(_,init)=>{
    if(init.method==='POST'){exists=true;throw new Error('Response lost');}return response({items:exists?[item()]:[]});
  }); await f.manager.start(); f.manager.open(); f.get('new-token-name').value='laptop'; f.submit();
  await settle(()=>!f.manager.isPending()); assert.equal(f.get('new-token-submit').disabled,true);
  assert.match(f.get('new-token-error').textContent,/may already exist/);
  f.submit(); assert.equal(f.requests.filter(r=>r.method==='POST').length,1);
  await f.manager.checkCreation(); assert.match(f.get('new-token-error').textContent,/revoke it, then create a replacement/);
  assert.equal(f.get('new-token-check').textContent,'View tokens'); await f.manager.checkCreation();
  assert.equal(f.get('new-token-modal').hidden,true); assert.equal(f.document.activeElement.dataset.tokenId,'1');
});
test('unknown creation with no matching token can be retried after checking',async t=>{
  const f=fixture(t,async(_,init)=>{if(init.method==='POST')throw new Error();return response({items:[]});});
  await f.manager.start(); f.manager.open(); f.get('new-token-name').value='laptop'; f.submit();
  await settle(()=>!f.manager.isPending()); await f.manager.checkCreation();
  assert.equal(f.get('new-token-submit').disabled,false); assert.equal(f.get('new-token-name').value,'laptop');
  assert.match(f.get('new-token-error').textContent,/No token with this name/);
});
test('an unreadable creation response cannot claim success or show an empty reveal',async t=>{
  const f=fixture(t,async(_,init)=>init.method==='POST'?response({},201):response({items:[]}));
  await f.manager.start(); f.manager.open(); f.get('new-token-name').value='laptop'; f.submit();
  await settle(()=>!f.manager.isPending()); assert.equal(f.get('token-reveal').hidden,true);
  assert.equal(f.get('new-token-check').hidden,false);
});
test('copy failure is inline and retryable, and a late copy cannot repopulate closed feedback',async t=>{
  let fail=true, delay=null;
  const f=fixture(t,async(_,init)=>init.method==='POST'?response(created,201):response({items:[]}),{
    copy:async()=>{if(delay)return delay.promise;if(fail)throw new Error();},
  }); await f.manager.start(); f.manager.open(); f.get('new-token-name').value='laptop'; f.submit();
  await settle(()=>!f.get('token-reveal').hidden);
  f.get('token-reveal-copy').click(); await settle(()=>!f.get('token-reveal-copy').disabled);
  assert.match(f.get('token-reveal-status').textContent,/copy it manually/); assert.equal(f.get('token-reveal-status').closest('.sr-only'),null);
  fail=false; f.get('token-reveal-copy').click(); await settle(()=>/Token copied/.test(f.get('token-reveal-status').textContent));
  delay=held(); f.get('token-reveal-copy').click(); f.manager.close(true); delay.resolve();
  await new Promise(r=>setImmediate(r)); assert.equal(f.get('token-reveal-status').textContent,''); assert.equal(f.get('token-reveal-value').textContent,'');
});
test('revoke is confirmed inline, cancellable and retains row context on failure',async t=>{
  let fail=true; const f=fixture(t,async(_,init)=>init.method==='DELETE'?response({},fail?503:204):response({items:[item()]}));
  await f.manager.start(); f.document.querySelector('[data-token-id]').click();
  assert.equal(f.requests.some(r=>r.method==='DELETE'),false); assert.equal(f.document.activeElement.dataset.focusKey,'token:1:cancel');
  f.document.querySelector('[data-token-cancel]').click(); assert.equal(f.document.activeElement.dataset.tokenId,'1');
  f.document.querySelector('[data-token-id]').click(); f.document.querySelector('[data-token-confirm]').click();
  await settle(()=>!f.manager.isPending()); assert.match(f.get('tokens-list').textContent,/may still work/);
  assert.equal(f.document.activeElement.dataset.focusKey,'token:1:confirm');
  fail=false; f.document.querySelector('[data-token-confirm]').click(); await settle(()=>!f.manager.isPending());
  assert.equal(f.document.querySelector('[data-token-row]'),null); assert.match(f.get('tokens-status').textContent,/revoked/);
  assert.equal(f.document.activeElement,f.get('new-token-button'));
});
test('revocation keeps keyboard focus on the adjacent token instead of a collapsed mobile action',async t=>{
  const f=fixture(t,async(_,init)=>init.method==='DELETE'?response(null,204):response({items:[item(1),item(2,'workstation')]}));
  await f.manager.start();
  f.document.querySelector('[data-token-id="1"]').click();f.document.querySelector('[data-token-confirm]').click();
  await settle(()=>!f.manager.isPending());
  assert.equal(f.document.activeElement.dataset.tokenId,'2');
});
test('stale inventory read cannot restore a successfully revoked token',async t=>{
  const delayed=held(); let reads=0;
  const f=fixture(t,async(_,init)=>init.method==='DELETE'?response(null,204):++reads===1?response({items:[item()]}):delayed.promise);
  await f.manager.start(); const refresh=f.manager.load();
  f.document.querySelector('[data-token-id]').click(); f.document.querySelector('[data-token-confirm]').click();
  await settle(()=>!f.manager.isPending()); delayed.resolve(response({items:[item()]})); await refresh;
  assert.equal(f.document.querySelector('[data-token-row]'),null);
});
test('CLI approval normalizes typed code, suppresses repeat Enter and reports actual expiry',async t=>{
  const delayed=held(); const f=fixture(t,async(path)=>path.endsWith('/approve')?delayed.promise:response({items:[]}));
  await f.manager.start(); f.get('cli-connect-code-input').value=' abcd 1234 '; f.connect(); f.connect();
  const approvals=f.requests.filter(r=>r.path.endsWith('/approve')); assert.equal(approvals.length,1);
  assert.deepEqual(JSON.parse(approvals[0].body),{user_code:'ABCD-1234'}); assert.equal(f.manager.allowLeave(),false);
  delayed.resolve(response({name:'cli-workstation-a1b2c3',expires_at:created.expires_at},201));
  await settle(()=>!f.manager.isPending()); assert.match(f.get('cli-connect-success').textContent,/Connected workstation.*Feb 1, 2027/);
  assert.equal(f.get('cli-connect-code-input').value,''); assert.equal(f.document.activeElement,f.get('cli-connect-code-input'));
});
test('invalid CLI input does not request approval and server rejection preserves correction',async t=>{
  const f=fixture(t,async(path)=>path.endsWith('/approve')?response({},400):response({items:[]}));
  await f.manager.start(); f.get('cli-connect-code-input').value='abc'; f.connect();
  assert.equal(f.requests.some(r=>r.path.endsWith('/approve')),false); assert.equal(f.get('cli-connect-code-input').getAttribute('aria-invalid'),'true');
  f.get('cli-connect-code-input').value='ABCD-1234'; f.connect(); await settle(()=>!f.manager.isPending());
  assert.match(f.get('cli-connect-error').textContent,/invalid, expired or already used/); assert.equal(f.get('cli-connect-code-input').value,'ABCD-1234');
});
test('a legacy URL only opens an upgrade notice and never fills or approves a code',async t=>{
  const f=fixture(t,async()=>response({items:[]}),{search:()=>'?connect_hash=hostile&connect_code=ABCD-1234'});
  await f.manager.start(); assert.equal(f.get('cli-connect-panel').open,true); assert.equal(f.get('cli-connect-code-input').value,'');
  assert.equal(f.get('cli-connect-legacy-notice').hidden,false); assert.deepEqual(f.requests.map(r=>r.path),['/api/tokens']);
});
test('route departure aborts reads, discards late approvals and clears secrets and codes',async t=>{
  const delayed=held(); const f=fixture(t,async(path)=>path.endsWith('/approve')?delayed.promise:response({items:[item()]}));
  await f.manager.start(); f.get('cli-connect-code-input').value='ABCD-1234'; f.connect(); f.manager.stop();
  assert.equal(f.requests.at(-1).signal.aborted,true);
  delayed.resolve(response({name:'cli-workstation-a1b2c3',expires_at:created.expires_at},201));
  await new Promise(r=>setImmediate(r)); assert.equal(f.get('cli-connect-success').textContent,''); assert.equal(f.get('tokens-list').textContent,'');
});
test('managed-account capability changes immediately clear a revealed secret and remove mutation actions',async t=>{
  const f=fixture(t,async(_,init)=>init.method==='POST'?response(created,201):response({items:[item()]}));
  await f.manager.start(); f.manager.open(); f.get('new-token-name').value='laptop'; f.submit(); await settle(()=>!f.get('token-reveal').hidden);
  f.state.user.can_manage_self=false; f.document.dispatchEvent(new f.dom.window.Event('session-capabilities-change'));
  await settle(()=>f.get('tokens-loading').hidden);
  assert.equal(f.get('token-reveal-value').textContent,''); assert.equal(f.get('new-token-modal').hidden,true);
  assert.equal(f.get('new-token-button').hidden,true); assert.equal(f.get('cli-connect-panel').hidden,true);
  assert.equal(f.document.querySelector('[data-token-id]'),null); f.manager.open(); assert.equal(f.get('new-token-modal').hidden,true);
});
test('expired authentication clears private rows and invokes session recovery',async t=>{
  let expired=false; const f=fixture(t,async()=>expired?response({},401):response({items:[item()]}));
  await f.manager.start(); expired=true; await f.manager.load();
  assert.equal(f.unauthorized(),1); assert.equal(f.get('tokens-list').textContent,'');
});
test('stalled response bodies time out and expose uncertain creation recovery',async t=>{
  const f=fixture(t,async(_,init)=>init.method==='POST'?{ok:true,status:201,json:()=>new Promise(()=>{})}:response({items:[]}),{requestOptions:{timeoutMs:10}});
  await f.manager.start(); f.manager.open(); f.get('new-token-name').value='laptop'; f.submit();
  await new Promise(r=>setTimeout(r,30)); assert.equal(f.manager.isPending(),false); assert.equal(f.get('new-token-check').hidden,false);
});

// Deterministic clock, including scheduled expiry deadlines and cleanup.
function controlledClock(initial) {
  let time=initial,id=0; const timers=new Map();
  return {now:()=>time,setTimer:(callback,delay)=>{timers.set(++id,{callback,at:time+delay});return id;},clearTimer:key=>timers.delete(key),
    pending:()=>timers.size,
    advance(ms){const end=time+ms;while(true){const next=[...timers].sort((a,b)=>a[1].at-b[1].at)[0];if(!next||next[1].at>end)break;time=next[1].at;timers.delete(next[0]);next[1].callback();}time=end;},
    jump(ms){time+=ms;},
  };
}
test('closing a one-time reveal requires explicit acknowledgment even after copying',async t=>{
  const f=fixture(t,async(_,init)=>init.method==='POST'?response(created,201):response({items:[]}));
  await f.manager.start(); f.manager.open(); f.get('new-token-name').value='laptop';f.submit();await settle(()=>!f.manager.isPending());
  assert.equal(f.manager.needsSave(),true);
  for(const dismiss of [()=>f.get('new-token-close').click(),()=>f.get('new-token-modal').click(),()=>f.manager.close()]){
    dismiss();assert.equal(f.get('new-token-modal').hidden,false);assert.equal(f.get('token-reveal-value').textContent,created.token);
    assert.equal(f.get('token-reveal-dismiss').hidden,false);assert.equal(f.document.activeElement,f.get('token-reveal-keep'));
    f.get('token-reveal-keep').click();assert.equal(f.get('token-reveal-dismiss').hidden,true);assert.equal(f.document.activeElement,f.get('token-reveal-copy'));
  }
  f.get('token-reveal-copy').click();await settle(()=>!f.get('token-reveal-copy').disabled);
  f.manager.close();assert.equal(f.get('token-reveal-dismiss').hidden,false,'Copying is not acknowledgment of safe storage');
  f.get('token-reveal-discard').click();assert.equal(f.manager.needsSave(),false);assert.equal(f.get('token-reveal-value').textContent,'');assert.equal(f.get('new-token-modal').hidden,true);
  f.manager.open();assert.equal(f.get('token-reveal-dismiss').hidden,true);
});
test('late clipboard completion cannot move focus away from the dismissal choice',async t=>{
  const delayed=held();const f=fixture(t,async(_,init)=>init.method==='POST'?response(created,201):response({items:[]}),{copy:()=>delayed.promise});
  await f.manager.start();f.manager.open();f.get('new-token-name').value='laptop';f.submit();await settle(()=>!f.manager.isPending());
  f.get('token-reveal-copy').click();f.manager.close();delayed.resolve();await settle(()=>!f.get('token-reveal-copy').disabled);
  assert.equal(f.document.activeElement,f.get('token-reveal-keep'));assert.equal(f.get('token-reveal-value').textContent,created.token);
  f.manager.stop();assert.equal(f.get('new-token-modal').hidden,true);assert.equal(f.get('token-reveal-value').textContent,'');assert.equal(f.get('token-reveal-dismiss').hidden,true);
});
test('idle expiry and relative ages update without a request or replacing focused controls',async t=>{
  const initial=Date.parse('2026-11-03T12:00:00Z'),clock=controlledClock(initial);
  const token={...item(),created_at:new Date(initial).toISOString(),last_used_at:new Date(initial).toISOString(),expires_at:new Date(initial+2500).toISOString()};
  const f=fixture(t,async()=>response({items:[token]}),{clock});await f.manager.start();
  f.document.querySelector('[data-token-id]').click();const row=f.document.querySelector('[data-token-row]');
  const cancel=f.document.querySelector('[data-token-cancel]'),confirmation=f.document.querySelector('.token-confirm');
  clock.advance(2500);assert.equal(row.classList.contains('token-expired'),true);assert.equal(row.querySelector('.token-expires').textContent,'Expired');
  assert.match(f.get('tokens-count').textContent,/1 expired/);assert.equal(f.document.activeElement,cancel);assert.equal(f.document.querySelector('.token-confirm'),confirmation);
  clock.advance(60000);assert.equal(row.querySelector('.token-created').textContent,'Created 1m ago');assert.equal(row.querySelector('.token-last-used').textContent,'1m ago');assert.equal(f.requests.length,1);
  f.manager.stop();assert.equal(clock.pending(),0);clock.advance(60000);assert.equal(f.get('tokens-list').textContent,'');
});
test('returning to a visible page catches up expiry immediately and does not restart a stopped view',async t=>{
  const initial=Date.parse('2026-11-03T12:00:00Z'),clock=controlledClock(initial);
  const f=fixture(t,async()=>response({items:[{...item(),expires_at:new Date(initial+2000).toISOString()}]}),{clock});await f.manager.start();
  Object.defineProperty(f.document,'hidden',{configurable:true,value:true});f.document.dispatchEvent(new f.dom.window.Event('visibilitychange'));assert.equal(clock.pending(),0);
  clock.jump(5000);Object.defineProperty(f.document,'hidden',{configurable:true,value:false});f.document.dispatchEvent(new f.dom.window.Event('visibilitychange'));
  assert.equal(f.document.querySelector('.token-expires').textContent,'Expired');assert.equal(f.requests.length,1);assert.equal(clock.pending(),1);
  f.manager.stop();f.document.dispatchEvent(new f.dom.window.Event('visibilitychange'));f.dom.window.dispatchEvent(new f.dom.window.Event('pageshow'));assert.equal(clock.pending(),0);
});
test('the manual CLI example copies only a public placeholder and recovers from clipboard failure',async t=>{
  let fail=true;const copied=[];const f=fixture(t,async()=>response({items:[]}),{origin:'https://hub.example.com',copy:async text=>{if(fail)throw new Error();copied.push(text);}});
  await f.manager.start();const example=f.get('token-usage-command').textContent;
  assert.equal(example.replaceAll(/\\\n\s*/g,''),"SHINYHUB_TOKEN='<token>' shinyhub apps list --host https://hub.example.com");
  f.get('token-usage-copy').click();await settle(()=>!f.get('token-usage-copy').disabled);assert.match(f.get('token-usage-status').textContent,/copy it manually/);
  assert.equal(f.get('token-usage-status').classList.contains('sr-only'),false);
  fail=false;f.get('token-usage-copy').click();await settle(()=>!f.get('token-usage-copy').disabled);assert.deepEqual(copied,[example]);assert.match(f.get('token-usage-status').textContent,/Command copied/);
  assert.equal(f.get('token-usage-copy').textContent,'Copied');
  assert.equal(f.get('token-usage-status').classList.contains('sr-only'),true);
  f.get('token-api-copy').click();await settle(()=>!f.get('token-api-copy').disabled);
  assert.deepEqual(copied,[example,'Authorization: token <token>']);assert.match(f.get('token-api-status').textContent,/Header copied/);
  f.manager.stop();assert.equal(f.get('token-usage-status').textContent,'');
  assert.equal(f.get('token-api-status').textContent,'');assert.equal(f.get('token-usage-copy').textContent,'Copy command');assert.equal(f.get('token-api-copy').textContent,'Copy header');
});
