import {beforeEach, afterEach, test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {JSDOM} from 'jsdom';
import {createPeopleDirectory} from '../static/views/people-directory.js';
const html=readFileSync(new URL('../static/index.html',import.meta.url),'utf8').match(/<section id="users-view"[\s\S]*?(?=    <section id="tokens-view")/)[0];
const users=[
 {id:1,username:'qa-admin',display_name:'Platform administrator',role:'admin',manual_role:'admin',created_at:'2026-11-03T12:00:00Z',has_local_password:true},
 {id:2,username:'analyst',display_name:'Analytics · 東京',role:'viewer',manual_role:'',role_source:'sso',created_at:'2026-11-02T12:00:00Z',has_local_password:false},
];
const payload=(items=users)=>({items,onboarding:{local:true,sso:true,providers:['SSO']},support_sessions:{enabled:false}});
const response=(body,status=200)=>({ok:status>=200&&status<300,status,json:async()=>body});
function deferred(){let resolve;return {promise:new Promise(r=>resolve=r),resolve:v=>resolve(v)};}
let dom,dir,ctx,calls,toasts,navigations,expired;
const get=id=>dom.window.document.getElementById(id);
async function settle(predicate){for(let i=0;i<100;i++){if(predicate())return;await new Promise(r=>setImmediate(r));}assert.fail('Directory did not settle');}
function mount(handler=async()=>response(payload())){
 calls=[];toasts=[];navigations=[];expired=0;
 ctx={document:dom.window.document,state:{user:{id:1,role:'admin'},sessionIdentity:{}},
 api:async(path,options={})=>{calls.push({path,options});return path==='/api/user-invitations'?response({items:[]}):handler(path,options);},
 onUnauthorized:()=>expired++,navigate:(...args)=>navigations.push(args),flashToast:m=>toasts.push(m),confirm:()=>true,
 makeCopyButton:(_value,label)=>{const button=dom.window.document.createElement('button');button.setAttribute('aria-label',label);return button;},openResetPasswordModal(){},openSupportSessionModal(){},onReplaced(){}};
 dir=createPeopleDirectory(ctx);return dir.start();
}
function search(value){get('people-search').value=value;get('people-search').dispatchEvent(new dom.window.Event('input'));}
function selectRole(value, save=true){const select=get('users-body').querySelector('select');select.focus();select.value=value;select.dispatchEvent(new dom.window.Event('change'));if(save)get('users-body').querySelector('[aria-label="Save role for analyst"]')?.click();return get('users-body').querySelector('select');}
beforeEach(()=>{dom=new JSDOM(html,{pretendToBeVisual:true,runScripts:'outside-only'});get('users-view').hidden=false;get('identity-people-panel').hidden=false;});
afterEach(()=>{dir?.stop();dir=null;dom.window.close();});

test('people load with effective roles, a read-only own role and useful identity names',async()=>{
 await mount();assert.equal(get('users-table').hidden,false);assert.equal(get('people-count').textContent,'2 people');
 assert.equal(get('users-body').querySelector('.person-name').textContent,'Platform administrator');
 assert.match(get('users-body').textContent,/@qa-admin/);assert.equal(get('users-body').querySelectorAll('select').length,1);
 assert.match(get('users-body').textContent,/From group rules/);assert.equal(get('new-user-button').hidden,false);
});
test('loading is distinct from empty and preserves no invented rows',async()=>{
 const held=deferred();const started=mount(()=>held.promise);assert.equal(get('people-loading').hidden,false);assert.equal(get('users-table').hidden,true);
 held.resolve(response(payload([])));await started;assert.equal(get('people-loading').hidden,true);assert.equal(get('people-empty').hidden,false);
});
test('Unicode search, role filters and clear action recover from no matches',async()=>{
 await mount();search('東京');assert.equal(get('people-count').textContent,'1 of 2 people');assert.equal(get('people-clear').hidden,false);
 get('people-role-filter').value='admin';get('people-role-filter').dispatchEvent(new dom.window.Event('change'));
 assert.equal(get('people-no-matches').hidden,false);assert.equal(get('users-table').hidden,true);
 get('people-no-matches-clear').click();assert.equal(get('people-count').textContent,'2 people');assert.equal(dom.window.document.activeElement,get('people-search'));
});
test('failed refresh retains inventory, controls, filters and keyboard focus, then recovers',async()=>{
 let failed=false;await mount(()=>failed?response(null,503):response(payload()));search('analyst');
 const select=get('users-body').querySelector('select');select.focus();failed=true;await dir.load();
 assert.match(get('users-error').textContent,/last loaded list/);assert.equal(get('users-body').rows.length,1);assert.equal(dom.window.document.activeElement,select);
 failed=false;await dir.load();assert.equal(get('users-error').hidden,true);assert.equal(get('people-search').value,'analyst');
 assert.equal(dom.window.document.activeElement.dataset.focusKey,'person:2:role');
});
for(const body of [{}, {items:[{id:2,username:'x',role:'root'}]}, {items:[null]}])test('malformed inventory never masquerades as an empty directory: '+JSON.stringify(body),async()=>{
 await mount(()=>response(body));assert.equal(get('users-error').hidden,false);assert.equal(get('people-empty').hidden,true);
});
test('concurrent refreshes share one request',async()=>{
 const held=deferred();const started=mount(()=>held.promise);dir.load();dir.load();assert.equal(calls.length,1);
 held.resolve(response(payload()));await started;
});
test('role update uses returned effective role, retains focus, and blocks a duplicate submission',async()=>{
 const held=deferred();await mount((path)=>path==='/api/users'?response(payload()):held.promise);
 const select=selectRole('developer');select.dispatchEvent(new dom.window.Event('change'));assert.equal(select.disabled,true);
 assert.equal(calls.filter(c=>c.options.method==='PATCH').length,1);
 held.resolve(response({...users[1],role:'developer',manual_role:'developer'}));await settle(()=>get('people-status').textContent.includes('Developer'));
 assert.equal(get('users-body').querySelector('select').value,'developer');assert.equal(dom.window.document.activeElement.dataset.focusKey,'person:2:role');
 assert.match(get('users-body').textContent,/Manual override/);
});
test('role update failure restores the previous role and shows the server explanation beside the person',async()=>{
 await mount(path=>response(path==='/api/users'?payload():{error:'cannot remove the last admin'},path==='/api/users'?200:409));
 selectRole('admin');await settle(()=>get('users-body').querySelector('.person-row-error'));
 assert.equal(get('users-body').querySelector('select').value,'');assert.match(get('users-body').querySelector('.person-row-error').textContent,/Cannot remove the last admin/);
});
test('an earlier directory read cannot overwrite a completed role update',async()=>{
 const held=deferred();let reads=0;await mount(path=>path==='/api/users'?(++reads===1?response(payload()):held.promise):response({...users[1],role:'operator',manual_role:'operator'}));
 const read=dir.load();selectRole('operator');await settle(()=>get('people-status').textContent.includes('Operator'));
 held.resolve(response(payload()));await read;assert.equal(get('users-body').querySelector('select').value,'operator');
});
test('session revocation is confirmed, sent once, and announced',async()=>{
 const held=deferred();await mount(path=>path==='/api/users'?response(payload()):held.promise);
 const button=get('users-body').querySelector('[aria-label="Sign out analyst everywhere"]');button.click();button.click();
 assert.equal(calls.filter(c=>c.path.endsWith('/revoke-sessions')).length,1);
 held.resolve(response({}));await settle(()=>toasts.some(m=>m.includes('signed out everywhere')));
 assert.equal(get('users-body').rows.length,2);
});
test('deletion updates the directory without a stale follow-up read',async()=>{
 await mount(path=>response(path==='/api/users'?payload():{}));
 get('users-body').querySelector('[aria-label="Delete user analyst"]').click();await settle(()=>get('users-body').rows.length===1);
 assert.equal(get('people-count').textContent,'1 person');assert.equal(dom.window.document.activeElement,get('users-refresh'));
});
test('signing out the current administrator invokes logout immediately',async()=>{
 await mount(path=>response(path==='/api/users'?payload():{}));
 get('users-body').querySelector('[aria-label="Sign out qa-admin everywhere"]').click();await settle(()=>expired===1);
 assert.equal(get('users-body').children.length,0);
});
test('unmount aborts requests and clears inventory, invitations and onboarding',async()=>{
 const held=deferred();const started=mount(()=>held.promise);const signal=calls[0].options.signal;dir.stop();assert.equal(signal.aborted,true);
 held.resolve(response(payload()));await started;assert.equal(get('users-body').children.length,0);assert.equal(get('new-user-button').hidden,true);
});
test('a same-account role downgrade clears immediately and redirects once',async()=>{
 await mount();ctx.state.user.role='viewer';dom.window.document.dispatchEvent(new dom.window.Event('session-capabilities-change'));
 assert.equal(get('users-body').children.length,0);assert.deepEqual(navigations,[['/apps',{replace:true}]]);await dir.load();assert.equal(navigations.length,1);
});
for(const status of [401,403])test('authorization failure '+status+' clears previously loaded data',async()=>{
 let denied=false;await mount(()=>response(denied?null:payload(),denied?status:200));denied=true;await dir.load();
 assert.equal(get('users-body').children.length,0);assert.equal(status===401?expired:navigations.length,1);
});
test('bad creation dates do not crash rendering',async()=>{
 await mount(()=>response(payload([{...users[0],created_at:'invalid'}])));assert.match(get('users-body').textContent,/Not reported/);
});
test('expanded account actions have no structural WCAG A/AA violations',async()=>{
 await mount();get('users-body').querySelector('[aria-label="More actions for analyst"]').click();dom.window.HTMLCanvasElement.prototype.getContext=()=>null;const {default:axe}=await import('axe-core');dom.window.eval(axe.source);
 const result=await dom.window.axe.run(get('users-view'),{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']},rules:{'color-contrast':{enabled:false}}});
 assert.equal(result.violations.length,0,JSON.stringify(result.violations));
});

test('choosing a role only drafts it; explicit save reviews platform-wide access',async()=>{
 await mount();selectRole('admin',false);
 assert.equal(calls.some(call=>call.options.method==='PATCH'),false);
 assert.match(get('users-body').querySelector('.person-role-review').textContent,/Viewer to Admin/);
 assert.match(get('users-body').querySelector('.person-role-review').textContent,/including people and server settings/);
 assert.equal(get('users-body').querySelector('[aria-label="Save role for analyst"]').textContent,'Grant Admin access');
 assert.match(get('users-body').textContent,/Unsaved change/);
});
test('cancel and Escape discard an unsaved role and return focus without writing',async()=>{
 await mount();selectRole('operator',false);get('users-body').querySelector('.person-role-actions button:last-child').click();
 assert.equal(get('users-body').querySelector('select').value,'');
 selectRole('admin',false);get('users-body').querySelector('select').dispatchEvent(new dom.window.KeyboardEvent('keydown',{key:'Escape',bubbles:true}));
 assert.equal(get('users-body').querySelector('.person-role-review'),null);
 assert.equal(dom.window.document.activeElement.dataset.focusKey,'person:2:role');
 assert.equal(calls.some(call=>call.options.method==='PATCH'),false);
});
test('automatic roles are previewed from the server before saving an override removal',async()=>{
 const held=deferred();await mount(path=>path==='/api/users'?response(payload([users[0],{...users[1],role:'operator',manual_role:'operator'}])):held.promise);
 selectRole('',false);assert.equal(get('users-body').querySelector('[aria-label="Save role for analyst"]').disabled,true);
 assert.equal(calls.some(call=>call.options.method==='PATCH'),false);
 held.resolve(response({role:'viewer',source:'default'}));await settle(()=>get('users-body').querySelector('.person-role-plan').textContent.includes('Viewer'));
 assert.match(get('users-body').querySelector('.person-role-review').textContent,/Operator to Viewer · Automatic/);
 assert.match(get('users-body').querySelector('.person-role-review').textContent,/server default/);
 assert.equal(get('users-body').querySelector('[aria-label="Save role for analyst"]').disabled,false);
});
test('failed or obsolete automatic previews cannot enable an unsafe save',async()=>{
 const held=deferred();await mount(path=>path==='/api/users'?response(payload([users[0],{...users[1],role:'operator',manual_role:'operator'}])):held.promise);
 selectRole('',false);selectRole('developer',false);held.resolve(response({role:'admin',source:'sso'}));
 await new Promise(r=>setImmediate(r));assert.match(get('users-body').querySelector('.person-role-plan').textContent,/Operator to Developer/);
 assert.doesNotMatch(get('users-body').querySelector('.person-role-review').textContent,/Admin/);
});
test('a failed automatic preview provides retry and keeps Save disabled',async()=>{
 let fail=true;await mount(path=>path==='/api/users'?response(payload([users[0],{...users[1],role:'operator',manual_role:'operator'}])):response(fail?{}:{role:'developer',source:'sso'},fail?503:200));
 selectRole('',false);await settle(()=>get('users-body').textContent.includes('Retry preview'));
 assert.equal(get('users-body').querySelector('[aria-label="Save role for analyst"]').disabled,true);
 fail=false;[...get('users-body').querySelectorAll('button')].find(b=>b.textContent==='Retry preview').click();
 await settle(()=>get('users-body').textContent.includes('Operator to Developer'));
 assert.match(get('users-body').querySelector('.person-role-review').textContent,/sign-in group rules/);
});

test('saving an automatic role includes the reviewed expectation and confirms the returned role',async()=>{
 await mount((path,options)=>path==='/api/users'?response(payload([users[0],{...users[1],role:'operator',manual_role:'operator'}]))
  :options.method==='PATCH'?response({...users[1],role:'viewer',manual_role:''}):response({role:'viewer',source:'default'}));
 selectRole('',false);await settle(()=>get('users-body').querySelector('.person-role-plan').textContent.includes('Viewer'));
 get('users-body').querySelector('[aria-label="Save role for analyst"]').click();
 await settle(()=>get('people-status').textContent.includes('Viewer'));
 const patch=calls.find(call=>call.options.method==='PATCH');assert.deepEqual(JSON.parse(patch.options.body),{role:'',expected_role:'viewer'});
 assert.equal(get('users-body').querySelector('.person-role-review'),null);
 assert.equal(get('users-body').querySelector('select').value,'');
});

test('saving keeps keyboard focus on its busy control and blocks a second write',async()=>{
 const held=deferred();await mount(path=>path==='/api/users'?response(payload()):held.promise);
 selectRole('developer',false);const save=get('users-body').querySelector('[aria-label="Save role for analyst"]');save.focus();save.click();
 assert.equal(dom.window.document.activeElement.dataset.focusKey,'person:2:save-role');
 assert.equal(dom.window.document.activeElement.getAttribute('aria-disabled'),'true');
 dom.window.document.activeElement.click();assert.equal(calls.filter(call=>call.options.method==='PATCH').length,1);
 held.resolve(response({...users[1],role:'developer',manual_role:'developer'}));await settle(()=>get('people-status').textContent.includes('Developer'));
 assert.equal(dom.window.document.activeElement.dataset.focusKey,'person:2:role');
});
