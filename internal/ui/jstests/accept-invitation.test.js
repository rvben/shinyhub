import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { mountInvitation } from '../static/views/accept-invitation.js';
import { applyPeopleOnboarding } from '../static/views/people-onboarding.js';
function fixture(fetch) {
  const dom = new JSDOM(readFileSync(new URL('../static/accept-invitation.html', import.meta.url), 'utf8'), {url: 'https://hub.example/invite#'+'a'.repeat(64)});
  const controller = mountInvitation({document:dom.window.document,location:dom.window.location,history:dom.window.history,fetch});
  return {dom, document:dom.window.document,controller};
}
const preview = {ok:true,json:async()=>({username:'person',role:'viewer',expires_at:'2030-01-01T00:00:00Z'})};
const event = {preventDefault(){}};
test('recipient chooses credentials; acceptance removes the secret and offers sign-in', async()=>{
 const calls=[];const f=fixture(async(path,options)=>{calls.push({path,...options});return path.endsWith('preview')?preview:{ok:true,json:async()=>({username:'person'})};});
 await f.controller.preview();
 f.document.getElementById('invitation-password').value='my new long passphrase';
 f.document.getElementById('invitation-confirm').value='my new long passphrase';
 await f.controller.submit(event);
 assert.equal(calls.length,3);
 assert.equal(calls[2].path,"/api/auth/session");
 assert.equal(calls[2].credentials,"same-origin");
 assert.equal(f.document.getElementById("invitation-signin").getAttribute("href"),"/apps");
 assert.equal(calls[1].credentials,'omit');
 assert.equal(JSON.parse(calls[1].body).password,'my new long passphrase');
 assert.equal(f.dom.window.location.hash,'');
 assert.equal(f.document.getElementById('invitation-password').value,'');
 assert.equal(f.document.getElementById('invitation-signin').hidden,false);
 assert.equal(f.document.activeElement.id,'invitation-signin');
});
test('invalid invitation has no password form and transient failures can be retried',async()=>{
 for(const status of [404,500]) {
  const f=fixture(async()=>({ok:false,status,json:async()=>({error:'Link unavailable'})}));
  await f.controller.preview();
  assert.equal(f.document.getElementById('invitation-form').hidden,true);
  assert.equal(f.document.getElementById('invitation-error').textContent,'Link unavailable');
  assert.equal(f.document.getElementById('invitation-retry').hidden,status===404);
 }
});
test('password mismatch and Unicode limits block submission',async()=>{
 let calls=0;const f=fixture(async()=>{calls++;return preview});await f.controller.preview();
 for(const [password,confirm] of [['a very long password','a different password'],['😀'.repeat(8),'😀'.repeat(8)],['😀'.repeat(19),'😀'.repeat(19)]]) {
  f.document.getElementById('invitation-password').value=password;f.document.getElementById('invitation-confirm').value=confirm;
  await f.controller.submit(event);assert.equal(calls,1);assert.equal(f.document.getElementById('invitation-error').hidden,false);
 }
});
test('SSO-only, mixed, local, and unavailable onboarding show only usable actions',()=>{
 const document=new JSDOM(readFileSync(new URL('../static/index.html',import.meta.url),'utf8')).window.document;
 for(const [mode,invite,share] of [[{local:true,sso:false},true,false],[{local:false,sso:true},false,true],[{local:true,sso:true},true,true],[null,false,false]]) {
  applyPeopleOnboarding(document,mode);
  assert.equal(document.getElementById('new-user-button').hidden,!invite);
  assert.equal(document.getElementById('people-signin-copy').hidden,!share);
  assert.equal(document.getElementById('people-joining').hidden,mode===null);
  assert.equal(document.getElementById('people-joining').open,false);
  assert.equal(document.querySelector('#users-view > .toolbar #new-user-button'),null);
  assert.equal(document.getElementById('new-user-button').closest('details').id,'people-joining');
 }
 applyPeopleOnboarding(document,{local:true,sso:true},true);
 assert.equal(document.getElementById('new-user-button').hidden,true);
 assert.equal(document.getElementById('people-signin-copy').hidden,true);
});

test('pending recipient submission cannot create two accounts and a failure retains the form', async()=>{
 let finish; let calls=0;
 const f=fixture(async(path)=>{if(path.endsWith('preview'))return preview;calls++;return new Promise(resolve=>finish=resolve);});
 await f.controller.preview();
 f.document.getElementById('invitation-password').value='my new long passphrase';
 f.document.getElementById('invitation-confirm').value='my new long passphrase';
 const pending=f.controller.submit(event);await f.controller.submit(event);
 assert.equal(calls,1);assert.equal(f.document.getElementById('invitation-submit').disabled,true);
 finish({ok:false,status:500,json:async()=>({error:'Please try again'})});await pending;
 assert.equal(f.document.getElementById('invitation-form').hidden,false);
 assert.equal(f.document.getElementById('invitation-password').value,'my new long passphrase');
 assert.equal(f.document.getElementById('invitation-submit').disabled,false);
});

test('recipient form has accessible labels and no WCAG A/AA violations',async()=>{
 const {default:axe}=await import('axe-core');
 const f=fixture(async()=>preview);await f.controller.preview();
 const source=axe.source;
 const dom=new JSDOM(f.dom.serialize(),{runScripts:'outside-only'});
 dom.window.eval(source);
 const results=await dom.window.axe.run(dom.window.document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']},rules:{'color-contrast':{enabled:false}}});
 assert.equal(results.violations.length,0,results.violations.map(v=>v.id).join(', '));
});

test('account stays complete when automatic sign-in fails',async()=>{
 const f=fixture(async path=>path.endsWith('preview')?preview:{ok:!path.endsWith('session'),json:async()=>({})});
 await f.controller.preview();
 f.document.getElementById('invitation-password').value='my new long passphrase';
 f.document.getElementById('invitation-confirm').value='my new long passphrase';
 await f.controller.submit(event);
 assert.equal(f.document.getElementById('invitation-form').hidden,true);
 assert.equal(f.document.getElementById('invitation-signin').getAttribute('href'),'/login?next=%2Fapps');
 assert.equal(f.dom.window.location.hash,'');
});
test('SSO onboarding names the configured providers',()=>{
 const document=new JSDOM(readFileSync(new URL('../static/index.html',import.meta.url),'utf8')).window.document;
 applyPeopleOnboarding(document,{local:false,sso:true,providers:['GitHub','Company SSO']});
 assert.match(document.getElementById('people-onboarding').textContent,/GitHub or Company SSO/);
});

test('replacing a pending link displays the new invitation and reloads the list',async()=>{
 const {createInvitationList}=await import('../static/views/people-onboarding.js');
 const document=new JSDOM(readFileSync(new URL('../static/index.html',import.meta.url),'utf8')).window.document;
 let loaded=0,replaced;
 const result={token:'b'.repeat(64),invitation:{id:'new',username:'person',role:'viewer',expires_at:'2030-01-01T00:00:00Z'}};
 const controller=createInvitationList({document,confirm:()=>true,onUnauthorized(){},announce(){},onReplaced:r=>replaced=r,api:async(path,options)=>{
  if(options){assert.equal(path,'/api/user-invitations/old/replace');assert.equal(options.method,'POST');return {ok:true,json:async()=>result};}
  loaded++;return {ok:true,json:async()=>({items:[{...result.invitation,id:'old'}]})};
 }});
 await controller.load();
 document.querySelector('[aria-label="Replace invitation link for person"]').click();
 await new Promise(resolve=>setImmediate(resolve));
 assert.deepEqual(replaced,result);assert.equal(loaded,2);
});
