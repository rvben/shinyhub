const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {JSDOM} = require('jsdom');
const source = fs.readFileSync(path.join(__dirname,'../src/shinyhub_agent/www/charts.js'),'utf8');
const chart = () => ({id:'one',kind:'bar',title:'<img src=x onerror=alert(1)>',x:'group.name',y:'value[0]',
  fields:{'group.name':'nominal','value[0]':'quantitative'},rows:[{'group.name':'<script>bad()</script>','value[0]':2}],source:'Authorized dataset'});
async function setup(t, fail = false) {
  const dom = new JSDOM('<!doctype html><html><head></head><body><div id="log"></div></body></html>',{runScripts:'outside-only',url:'https://apps.example.test/'});
  t.after(()=>dom.window.close()); const {window} = dom; const doc = window.document;
  Object.defineProperty(doc,'currentScript',{value:{src:'https://apps.example.test/lib/agent/charts.js'}});
  let finalized=0, rendered;
  window.shinyhubAgentChartVendor = {};
  window.shinyhubAgentChartVendor.vegaEmbed = async (container,spec,options)=>{
    rendered={container,spec,options}; return {view:{finalize(){finalized++},height(){return this},resize(){return this},runAsync(){return Promise.resolve()}}};
  };
  window.HTMLDialogElement.prototype.showModal=function(){this.open=true};
  window.HTMLDialogElement.prototype.close=function(){this.open=false;this.dispatchEvent(new window.Event('close'))};
  window.eval(source);
  async function flush() {
    for(let i=0;i<12;i++) {await new Promise(r=>setTimeout(r,0)); const script=doc.head.querySelector('script:not([data-done])');
      if(script){script.dataset.done='yes'; if(fail)script.onerror();else script.onload();}}
  }
  return {window,doc,flush,rendered:()=>rendered,finalized:()=>finalized};
}
test('literal labels, safe field paths, fixed specs, and duplicate event suppression',async t=>{
  const env=await setup(t), api=env.window.shinyhubAgentCharts, log=env.doc.querySelector('#log');
  const figure=api.create(chart());log.append(figure);assert.equal(api.create(chart()),null);
  await env.flush();const {spec,options}=env.rendered();
  assert.equal(spec.encoding.x.field,'f0');assert.equal(spec.encoding.y.field,'f1');
  assert.equal(spec.data.values[0].f0,'<script>bad()</script>');
  assert.equal(figure.querySelectorAll('script,img').length,0);
  assert.equal(figure.querySelector('figcaption').textContent,chart().title);
  assert.equal(options.actions,false);assert.equal(spec.encoding.y.stack,null);
  await assert.rejects(options.loader.load('https://external.example/data'));
  assert.equal(env.doc.head.querySelectorAll('script').length,3);
  assert.ok([...env.doc.head.querySelectorAll('script')].every(s=>s.src.startsWith('https://apps.example.test/lib/agent/vendor/')));
  api.dispose(log);assert.equal(env.finalized(),1);
});
test('a date tooltip names the day in the data, whatever the visitor\'s time zone',async t=>{
  const zone=process.env.TZ;process.env.TZ='America/Los_Angeles';
  t.after(()=>{if(zone===undefined)delete process.env.TZ;else process.env.TZ=zone;});
  assert.equal(new Date('2026-11-27T00:00:00Z').getDate(),26,'the test runs west of UTC');
  const env=await setup(t), api=env.window.shinyhubAgentCharts, log=env.doc.querySelector('#log');
  log.append(api.create({id:'days',kind:'line',title:'Daily',x:'Day',y:'Revenue',source:'Authorized dataset',
    fields:{Day:'temporal',Revenue:'quantitative'},rows:[{Day:'2026-11-27',Revenue:1},{Day:'2026-11-28T06:30:00+02:00',Revenue:2}]}));
  await env.flush();const {spec}=env.rendered();
  // Draw the fixed spec with the packaged libraries and read the tooltips off the marks.
  env.window.structuredClone=structuredClone;
  for(const name of ['vega','vega-lite'])env.window.eval(fs.readFileSync(path.join(__dirname,'../src/shinyhub_agent/www/vendor',name+'.min.js'),'utf8'));
  const {vega,vegaLite}=env.window.shinyhubAgentChartVendor;
  const view=new vega.View(vega.parse(vegaLite.compile({...JSON.parse(JSON.stringify(spec)),width:300}).spec),{renderer:'none'});
  await view.runAsync();t.after(()=>view.finalize());
  const days=new Set();(function walk(item){if(item.tooltip)days.add(item.tooltip.Day);(item.items||[]).forEach(walk);})(view.scenegraph().root);
  assert.deepEqual([...days].sort(),['2026-11-27','2026-11-28T06:30:00+02:00']);
  assert.deepEqual(Array.from(view.scale('x').domain(),Number),[Date.parse('2026-11-27T00:00:00Z'),Date.parse('2026-11-28T04:30:00Z')]);
});

test('reset disposes an expanded chart and its dialog',async t=>{
  const env=await setup(t), api=env.window.shinyhubAgentCharts, log=env.doc.querySelector('#log');
  log.append(api.create(chart()));await env.flush();
  log.querySelector('button').click();assert.equal(env.doc.querySelectorAll('dialog[open]').length,1);
  api.dispose(log);log.replaceChildren();assert.equal(env.doc.querySelectorAll('dialog').length,0);assert.equal(env.finalized(),1);
});
test('library failure preserves the entire accessible data table',async t=>{
  const env=await setup(t,true), figure=env.window.shinyhubAgentCharts.create(chart());env.doc.querySelector('#log').append(figure);
  await env.flush();assert.equal(figure.querySelector('details').open,true);
  assert.equal(figure.querySelector('tbody').textContent,'<script>bad()</script>2');
  assert.match(figure.querySelector('[role=status]').textContent,/could not load/);
  assert.equal(figure.querySelectorAll('button')[1].disabled,true);
});
test('late rendering completion is finalized after reset',async t=>{
  const env=await setup(t), log=env.doc.querySelector('#log'), api=env.window.shinyhubAgentCharts;
  let complete,finalized=0;env.window.shinyhubAgentChartVendor.vegaEmbed=()=>new Promise(r=>complete=r);
  log.append(api.create(chart()));await env.flush();api.dispose(log);log.replaceChildren();
  complete({view:{finalize(){finalized++}}});await env.flush();assert.equal(finalized,1);
});
test('source table is lazy and paginated at the 2000-row bound',async t=>{
  const env=await setup(t), c=chart();c.rows=Array.from({length:2000},(_,i)=>({'group.name':String(i),'value[0]':i}));
  const figure=env.window.shinyhubAgentCharts.create(c);env.doc.querySelector('#log').append(figure);await env.flush();
  assert.equal(figure.querySelectorAll('tbody tr').length,0);
  const details=figure.querySelector('details');details.open=true;await env.flush();
  assert.equal(figure.querySelectorAll('tbody tr').length,50);
  const next=[...figure.querySelectorAll('button')].find(b=>b.textContent==='Next rows');
  for(let i=0;i<39;i++)next.click();
  assert.equal(figure.querySelector('tbody tr').textContent,'19501950');assert.equal(next.disabled,true);
  assert.match(figure.querySelector('.sh-agent-chart-pages [role=status]').textContent,/1951–2000 of 2000/);
  details.open=false;await env.flush();assert.equal(figure.querySelectorAll('tbody tr').length,0);
});
test('long conversations release old views and retain at most eight charts',async t=>{
  const env=await setup(t), log=env.doc.querySelector('#log');
  for(let i=0;i<24;i++){log.append(env.window.shinyhubAgentCharts.create({...chart(),id:String(i)}));await env.flush();}
  assert.equal(log.querySelectorAll('figure').length,8);assert.equal(env.finalized(),16);
  assert.match(log.firstChild.textContent,/Earlier chart cleared/);
  env.window.shinyhubAgentCharts.dispose(log);assert.equal(env.finalized(),24);
});
test('failed library load can be retried without losing data',async t=>{
  const env=await setup(t,true), figure=env.window.shinyhubAgentCharts.create(chart());env.doc.querySelector('#log').append(figure);
  await env.flush();
  const retry=[...figure.querySelectorAll('button')].find(b=>b.textContent==='Retry chart');assert.equal(retry.hidden,false);retry.click();
  for(let i=0;i<12;i++) {await new Promise(r=>setImmediate(r));const s=env.doc.head.querySelector('script:not([data-done])');if(s){s.dataset.done='yes';s.onload();}}
  assert.equal(figure.querySelector('[role=status]').hidden,true);assert.equal(retry.hidden,true);
});
test('a stalled library times out and stale render results are finalized',async t=>{
  const env=await setup(t), timers=new Map();let id=0;
  env.window.setTimeout=(fn,ms)=>{timers.set(++id,{fn,ms});return id};env.window.clearTimeout=id=>timers.delete(id);
  const figure=env.window.shinyhubAgentCharts.create(chart());env.doc.querySelector('#log').append(figure);
  await new Promise(r=>setImmediate(r));
  [...timers.values()].find(v=>v.ms===10000).fn();await new Promise(r=>setImmediate(r));await new Promise(r=>setImmediate(r));
  assert.match(figure.querySelector('[role=status]').textContent,/could not load/);
  const retry=[...figure.querySelectorAll('button')].find(b=>b.textContent==='Retry chart');assert.equal(retry.hidden,false);
  let complete,finalized=0;env.window.shinyhubAgentChartVendor.vegaEmbed=()=>new Promise(r=>complete=r);retry.click();await env.flush();
  [...timers.values()].find(v=>v.ms===20000).fn();assert.equal(retry.hidden,false);
  complete({view:{finalize(){finalized++}}});await env.flush();assert.equal(finalized,1);
});
