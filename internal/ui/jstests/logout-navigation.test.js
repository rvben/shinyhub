import { test } from 'node:test';
import assert from 'node:assert/strict';
import { logoutTarget, reconnectForwardAuth } from '../static/views/logout-navigation.js';

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
