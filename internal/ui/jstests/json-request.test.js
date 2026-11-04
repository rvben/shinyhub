import {test} from 'node:test';
import assert from 'node:assert/strict';
import {requestJSON} from '../static/views/json-request.js';

for (const stalledBody of [false,true]) test(`deadline aborts a stalled ${stalledBody?'JSON body':'connection'}`,async()=>{
 let expire,signal,cleared=false;
 const policy={setTimer:fn=>{expire=fn;return 1;},clearTimer:id=>{assert.equal(id,1);cleared=true;}};
 const never=new Promise(()=>{});
 const pending=requestJSON(async(_path,options)=>{signal=options.signal;return stalledBody?{ok:true,status:200,json:()=>never}:never;},'/test',{},policy);
 expire();await assert.rejects(pending,/stopped/);assert.equal(signal.aborted,true);assert.equal(cleared,true);
});
test('caller cancellation bounds a body read and successful decoding clears the timer',async()=>{
 const controller=new AbortController();
 const pending=requestJSON(async()=>({ok:true,status:200,json:()=>new Promise(()=>{})}),'/test',{signal:controller.signal});
 controller.abort();await assert.rejects(pending,/stopped/);
 let cleared=false;
 const result=await requestJSON(async()=>({ok:true,status:200,json:async()=>({role:'viewer'})}),'/test',{}, {setTimer:()=>1,clearTimer:()=>{cleared=true;}});
 assert.deepEqual(await result.json(),{role:'viewer'});assert.equal(cleared,true);
});
