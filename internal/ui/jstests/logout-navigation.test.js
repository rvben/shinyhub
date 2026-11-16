import { test } from 'node:test';
import assert from 'node:assert/strict';
import { logoutTarget, reconnectForwardAuth, browserLogoutTarget } from '../static/views/logout-navigation.js';

test('logout uses the fixed bridge only for an advertised capability', () => {
 assert.equal(logoutTarget({forward_auth_logout:true}), '/api/auth/forward-auth/logout');
 for (const p of [null, {}, {forward_auth_logout:'true'}]) assert.equal(logoutTarget(p, '/identity'), '/identity');
});
test('resume requires an explicit protected POST and handles edge redirects', async () => {
 let calls=0;
 const api=async (path, opts)=>{calls++;assert.equal(path,'/api/auth/forward-auth/resume');assert.deepEqual(opts,{method:'POST',redirect:'manual'});return {ok:true,status:204};};
 assert.equal(await reconnectForwardAuth(api,{}),'reload');assert.equal(calls,0);
 assert.equal(await reconnectForwardAuth(api,{forward_auth_resume:true}),'reload');assert.equal(calls,1);
 for (const response of [{ok:false,status:401},{type:'opaqueredirect',status:0}]) assert.equal(await reconnectForwardAuth(async()=>response,{forward_auth_resume:true}),'upstream');
 assert.equal(await reconnectForwardAuth(async()=>{throw new TypeError('CORS');},{forward_auth_resume:true}),'upstream');
 for (const status of [403,503]) await assert.rejects(reconnectForwardAuth(async()=>({ok:false,status}),{forward_auth_resume:true}),new RegExp(String(status)));
});

test('browser logout accepts 200 provider logout, 204 local logout and already-ended sessions', async () => {
 assert.equal(await browserLogoutTarget({ok:true,status:200,json:async()=>({logout_url:'https://login.example/logout?hint=opaque'})},{}),'https://login.example/logout?hint=opaque');
 assert.equal(await browserLogoutTarget({ok:true,status:204},{}),'/');
 assert.equal(await browserLogoutTarget({ok:false,status:401},{oidc:{auto_redirect:true}}),'/?signed_out=1');
 assert.equal(await browserLogoutTarget({ok:true,status:200,json:async()=>({logout_url:'javascript:alert(1)'})},{}),'/');
 assert.equal(await browserLogoutTarget({ok:true,status:200,json:async()=>null},{}),'/');
 for (const status of [403,500]) await assert.rejects(browserLogoutTarget({ok:false,status},{}),/Logout failed/);
});

 test('support re-auth preserves its destination without ending provider SSO', async () => {
 const response = {ok:true,status:200,json:async()=>({logout_url:'https://idp.example/logout'})};
 for (const providers of [{}, {oidc:{auto_redirect:true}}]) {
  assert.equal(await browserLogoutTarget(response, providers, '/identity', {providerLogout:false}), providers.oidc ? '/identity?signed_out=1' : '/identity');
 }
 });
