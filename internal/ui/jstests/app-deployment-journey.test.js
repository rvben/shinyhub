import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { deployCommands, cliSetupCommands } from '../static/views/deploy-commands.js';
import { formatManifestSummary, renderDeployResult } from '../static/deploy-summary.js';

const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
const source = readFileSync(new URL('../static/app.js', import.meta.url), 'utf8');
function section(start, end) {
  const a = source.indexOf(start), b = source.indexOf(end, a);
  assert.ok(a >= 0 && b > a, start);
  return source.slice(a, b);
}
async function settle() { for (let i = 0; i < 25; i++) await Promise.resolve(); }
function harness(t, options = {}) {
  const dom = new JSDOM(html, { url:'http://localhost', pretendToBeVisual:true });
  t.after(() => dom.window.close());
  const doc = dom.window.document, requests = [], navigations = [], logs = [], pending = new Set();
  let refreshed = 0;
  const scope = {
    document:doc, window:dom.window, state:{apps:[]},
    api:async (path, init) => { requests.push({path, init}); return options.response || {ok:true,status:201,json:async()=>({slug:'sales',name:'Sales'})}; },
    loadApps:async()=>{refreshed++;},
    modalTrap:()=>({activate(){}, release(){}}),
    setError:(el, text)=>{ el.textContent=text; el.hidden=!text; },
    isInFlight:el=>pending.has(el),
    runInFlight:async(el,fn)=>{pending.add(el);try{await fn();}finally{pending.delete(el);el.disabled=false;}},
    handleUnauthorized:async()=>{},
    deployCommands,cliSetupCommands,formatManifestSummary,renderDeployResult,
    router:{navigate:path=>navigations.push(path)}, openLogs:slug=>logs.push(slug),
    bindDropzoneEvents(){},
    uploadBundle:options.upload || (async()=>({})),
  };
  const code = section('  const newAppButton ', '  // Mirrors internal/slug.Pattern')
    + `\nconst SLUG_RE=/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/; let slugEdited=false,sessionVersion=0,deployState=null;\n`
    + section('  function resetNewAppModal()', '  // Every selection owns')
    + section('  function beginDeploySelection()', '  function renderDeploySummary(')
    + section('  function deployErrorMessage(', '  function uploadBundle(')
    + section("  deploySubmit.addEventListener('click'", "  newAppButton.addEventListener('click'");
  const controls = new Function(...Object.keys(scope), code+`\nreturn {open:openNewAppModal,submit:submitNewApp,deploy:openDeployModal,close:closeDeployModal,select:()=>{beginDeploySelection();deployState.blob={};},state:()=>deployState,expire:()=>sessionVersion++};`)(...Object.values(scope));
  return {...controls,doc,requests,navigations,logs,refreshes:()=>refreshed,field:id=>doc.getElementById(id),submitEvent:{preventDefault(){}}};
}

test('creation contains only app details, then advances directly to deploy with the real slug', async t => {
  const h=harness(t); h.open();
  assert.equal(h.doc.activeElement.id,'new-app-name');
  assert.equal(h.doc.querySelector('#new-app-modal pre'),null,'No placeholder deployment commands before creation');
  h.field('new-app-name').value='Sales'; h.field('new-app-slug').value='sales';
  await h.submit(h.submitEvent);
  assert.equal(h.requests.length,1);
  assert.equal(h.field('new-app-modal').hidden,true);
  assert.equal(h.field('deploy-modal').hidden,false);
  assert.equal(h.field('deploy-step').hidden,false);
  assert.equal(h.field('deploy-created').hidden,false);
  assert.match(h.field('deploy-cli-snippet').textContent,/--slug sales/);
  assert.equal(h.field('deploy-overview').getAttribute('href'),'/apps/sales');
  assert.equal(h.doc.activeElement.id,'deploy-cli-snippet-copy');
});

test('duplicate slugs retain entered values and offer an accessible correction', async t => {
  const h=harness(t,{response:{status:409,ok:false}});h.open();
  h.field('new-app-name').value='Sales';h.field('new-app-slug').value='sales';
  await h.submit(h.submitEvent);
  assert.equal(h.field('new-app-name').value,'Sales');
  assert.equal(h.field('new-app-slug').value,'sales');
  assert.equal(h.field('new-app-slug').getAttribute('aria-invalid'),'true');
  assert.match(h.field('new-app-error').textContent,/Choose another slug/);
  assert.equal(h.field('new-app-error').getAttribute('role'),'alert');
  assert.equal(h.field('new-app-submit').disabled,false);
  assert.equal(h.field('deploy-modal').hidden,true);
});

test('a response arriving after the account changes never starts deployment', async t => {
  let resolve;const response=new Promise(r=>resolve=r);
  const h=harness(t,{response});h.open();h.field('new-app-name').value='Sales';h.field('new-app-slug').value='sales';
  const submitting=h.submit(h.submitEvent);h.expire();resolve({ok:true,status:201,json:async()=>({slug:'sales'})});await submitting;
  assert.equal(h.field('deploy-modal').hidden,true);
});

for(const manifest of [undefined,{app:{replicas:2}}]) test(`deployment success offers Overview consistently, manifest=${!!manifest}`,async t=>{
  const h=harness(t,{upload:async()=>({manifest})});h.deploy({slug:'sales',name:'Sales'});h.select();
  h.field('deploy-submit').disabled=false;h.field('deploy-submit').click();await settle();
  assert.equal(h.field('deploy-result').hidden,false);
  assert.equal(h.field('deploy-upload-state').hidden,false);
  assert.equal(h.field('deploy-submit').textContent,'View app');
  assert.equal(h.field('deploy-success-logs').hidden,false);
  assert.equal(h.doc.activeElement.className,'deploy-result-heading');
  h.field('deploy-submit').click();await settle();assert.deepEqual(h.navigations,['/apps/sales']);
  assert.deepEqual(h.logs,[]);
});

test('failed deploy keeps its bundle for retry and does not claim success',async t=>{
  let attempts=0;const h=harness(t,{upload:async()=>{if(++attempts===1)throw Object.assign(new Error('The app exited during startup.'),{status:500});return{};}});
  h.deploy({slug:'sales'});h.select();h.field('deploy-submit').disabled=false;h.field('deploy-submit').click();await settle();
  assert.match(h.field('deploy-error').textContent,/exited during startup/);
  assert.equal(h.field('deploy-submit').textContent,'Retry deployment');
  assert.equal(h.field('deploy-error-logs').hidden,false);
  assert.equal(h.field('deploy-progress-wrap').hidden,true);
  assert.equal(h.field('deploy-result').hidden,true);
  h.field('deploy-submit').click();await settle();assert.equal(attempts,2);assert.equal(h.state().completed,true);
});

test('rejected archives offer a replacement instead of retrying invalid input or opening app logs',async t=>{
  let attempts=0,picks=0;
  const h=harness(t,{upload:async()=>{if(++attempts===1)throw Object.assign(new Error('bundle rejected: upload a valid ZIP bundle; the archive is invalid or damaged'),{status:422});return{};}});
  h.field('deploy-file').addEventListener('click',()=>picks++);
  h.deploy({slug:'sales'});h.select();h.field('deploy-submit').disabled=false;h.field('deploy-submit').click();await settle();
  assert.match(h.field('deploy-error').textContent,/Choose a new ZIP file or upload the app folder/);
  assert.equal(h.field('deploy-error-logs').hidden,true);
  assert.equal(h.field('deploy-submit').textContent,'Choose another bundle');
  h.field('deploy-submit').click();await settle();assert.equal(picks,1);assert.equal(attempts,1);
  h.select();h.field('deploy-submit').disabled=false;h.field('deploy-submit').click();await settle();
  assert.equal(attempts,2);assert.equal(h.state().completed,true);
});

test('closing a deploy and opening another isolates the late result',async t=>{
  let finish;const upload=new Promise(r=>finish=r);const h=harness(t,{upload:()=>upload});
  h.deploy({slug:'sales'});h.select();h.field('deploy-submit').disabled=false;h.field('deploy-submit').click();await settle();
  h.close();h.deploy({slug:'other'});finish({});await settle();
  assert.equal(h.refreshes(),1,'A completed deployment refreshes the app list even after its viewer closes');
  assert.equal(h.state().slug,'other');assert.equal(h.field('deploy-result').hidden,true);assert.equal(h.field('deploy-submit').disabled,true);
});

test('no-result filtering offers recovery without pretending this is first use',t=>{
  const h=harness(t);const f=new Function('document','state','emptyStateCTA','emptyStateHeading','emptyStateLead','emptyStateActions','emptyStateEyebrow',section('  function renderEmptyStateCopy(', '  // Renders grouped apps')+'\nreturn renderEmptyStateCopy;')(
    h.doc,{canCreateApps:true},...['empty-state-cta','empty-state-heading','empty-state-lead','empty-state-actions','empty-state-eyebrow'].map(id=>h.field(id)));
  f({filtered:true});assert.match(h.field('empty-state-heading').textContent,/No apps match/);assert.equal(h.field('empty-state-clear').hidden,false);assert.equal(h.field('empty-state-cta').hidden,true);
  f();assert.equal(h.field('empty-state-clear').hidden,true);assert.equal(h.field('empty-state-cta').hidden,false);
});

 test('deployment feedback explains correction without exposing server filesystem paths',()=>{
  const message=new Function(section('  function deployErrorMessage(', '  function uploadBundle(')+'\nreturn deployErrorMessage;')();
  assert.match(message({status:400,message:'deploy failed: no app entrypoint found in /private/server/apps/x/versions/123'}),/Choose an updated bundle/);
  assert.doesNotMatch(message({status:400,message:'no app entrypoint found in /private/server/apps/x'}),/private|versions/);
  assert.match(message(new TypeError('Failed to fetch')),/may still be deploying/);
 });

 test('upload completion means startup is pending; stale upload events cannot paint a new dialog',async t=>{
  const dom=new JSDOM(html);t.after(()=>dom.window.close());const doc=dom.window.document;
  let xhr;
  class XHR {
    constructor(){xhr=this;this.events={};this.upload={events:{},addEventListener:(type,fn)=>this.upload.events[type]=fn};}
    open(){} setRequestHeader(){} send(){} addEventListener(type,fn){this.events[type]=fn;}
  }
  const run=new Function('XMLHttpRequest','FormData','readCookie','deployProgressBar','deployProgressText',`let deployState={};${section('  function uploadBundle(', "  deploySubmit.addEventListener('click'")}return {start:uploadBundle,detach:()=>deployState=null};`)(XHR,dom.window.FormData,()=>'',doc.getElementById('deploy-progress-bar'),doc.getElementById('deploy-progress-text'));
  const uploading=run.start('sales',new dom.window.Blob(['app.py']));
  xhr.upload.events.progress({lengthComputable:true,loaded:50,total:100});assert.equal(doc.getElementById('deploy-progress-bar').value,50);
  xhr.upload.events.load();assert.equal(doc.getElementById('deploy-progress-bar').hasAttribute('value'),false);
  assert.match(doc.getElementById('deploy-progress-text').textContent,/Upload complete/);
  run.detach();xhr.upload.events.progress({lengthComputable:true,loaded:75,total:100});assert.equal(doc.getElementById('deploy-progress-bar').hasAttribute('value'),false);
  xhr.status=200;xhr.responseText='{}';xhr.events.load();assert.deepEqual(await uploading,{});
 });

for(const [body,heading,description] of [
  [{kept_stopped:true,app:{status:'stopped'}},/App is stopped/,/Start the app/],
  [{kept_hibernated:true,app:{status:'hibernated'}},/App is sleeping/,/wake it/],
  [{status:'running'},/App is ready/,/app has started/],
  [{app:{status:'running'}},/App is ready/,/app has started/],
  [{status:'idle'},/App is ready/,/app has started/],
  [{},/Deployment complete/,/current health/],
]) test(`deployment result reflects actual backend outcome: ${JSON.stringify(body)}`,async t=>{
  const h=harness(t,{upload:async()=>body});h.deploy({slug:'sales'});h.select();h.field('deploy-submit').disabled=false;h.field('deploy-submit').click();await settle();
  assert.match(h.doc.querySelector('.deploy-result-heading').textContent,heading);
  assert.match(h.field('deploy-result-description').textContent,description);
  assert.equal(h.doc.querySelector('.deploy-overview-link').hidden,true,'No duplicate Overview link on completion');
});

test('successful deployment preserves a server warning instead of hiding partial outcomes',async t=>{
  const h=harness(t,{upload:async()=>({app:{status:'running'},warning:'Some schedules could not converge.'})});h.deploy({slug:'sales'});h.select();h.field('deploy-submit').disabled=false;h.field('deploy-submit').click();await settle();
  assert.equal(h.field('deploy-warning').hidden,false);assert.match(h.field('deploy-warning').textContent,/schedules/);
});

test('capacity-blocked redeploy preserves its bundle and explains that retry is manual',async t=>{
  const h=harness(t,{upload:async()=>{throw Object.assign(new Error('CPU: 0.000000 cores available'),{status:409,deployment:{failure_kind:'capacity'}});}});
  h.deploy({slug:'sales'});h.select();h.field('deploy-submit').disabled=false;h.field('deploy-submit').click();await settle();
  assert.match(h.field('deploy-error').textContent,/current version is preserved/);
  assert.match(h.field('deploy-error').textContent,/will not retry automatically/);
  assert.equal(h.field('deploy-error-logs').hidden,true);
  assert.equal(h.field('deploy-submit').textContent,'Retry deployment');
});

 test('a deployment finishing after an account change never reloads the new account list',async t=>{
  let finish;const upload=new Promise(r=>finish=r);const h=harness(t,{upload:()=>upload});h.deploy({slug:'sales'});h.select();h.field('deploy-submit').disabled=false;h.field('deploy-submit').click();await settle();h.expire();h.close();finish({status:'running'});await settle();assert.equal(h.refreshes(),0);
 });
