const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {JSDOM} = require('jsdom');
const www = path.join(__dirname,'../src/shinyhub_agent/www');
const charts = fs.readFileSync(path.join(www,'charts.js'),'utf8');
const views = fs.readFileSync(path.join(www,'views.js'),'utf8');
const HOSTILE = '<img src=x onerror=alert(1)>';
const chart = () => ({kind:'bar',x:'Region',y:'Revenue (EUR)',fields:{Region:'nominal','Revenue (EUR)':'quantitative'},
  rows:[{Region:'North','Revenue (EUR)':10},{Region:'<b>South</b>','Revenue (EUR)':null}]});
const payload = (...tiles) => ({version:1,view:{title:'Overview',tiles}});
const stat = (value, extra = {}) => ({id:'tile_1',kind:'stat',title:'Total',state:'ok',value,label:'Revenue (EUR)',...extra});
const bar = (extra = {}) => ({id:'tile_2',kind:'bar',title:'By region',state:'ok',chart:chart(),...extra});

async function setup(t, {fail = false, shiny = true} = {}) {
  const dom = new JSDOM('<!doctype html><html><head></head><body><div id="generated" class="shinyhub-agent-view"></div><div id="log"></div></body></html>',
    {runScripts:'outside-only',url:'https://apps.example.test/'});
  t.after(()=>dom.window.close()); const {window} = dom, doc = window.document;
  Object.defineProperty(doc,'currentScript',{value:{src:'https://apps.example.test/lib/agent/charts.js'},configurable:true});
  const embeds = []; let finalized = 0;
  window.shinyhubAgentChartVendor = {vegaEmbed: async (container,spec,options)=>{
    embeds.push({container,spec,options});
    return {view:{finalize(){finalized++},height(){return this},resize(){return this},runAsync(){return Promise.resolve()}}};
  }};
  const inputs = [], registered = [];
  if (shiny) {
    window.jQuery = scope => ({find: selector => [...scope.querySelectorAll(selector)]});
    window.Shiny = {OutputBinding: function () {}, outputBindings:{register:(binding,name)=>registered.push({binding,name})},
      setInputValue:(id,value,options)=>inputs.push({id,value,options})};
  }
  window.eval(charts); window.eval(views);
  async function flush() {
    for(let i=0;i<12;i++) {await new Promise(r=>setTimeout(r,0)); const script=doc.head.querySelector('script:not([data-done])');
      if(script){script.dataset.done='yes'; if(fail)script.onerror();else script.onload();}}
  }
  return {window,doc,el:doc.querySelector('#generated'),api:window.shinyhubAgentViews,flush,embeds,inputs,registered,finalized:()=>finalized};
}

test('the output binding finds view containers and draws what Shiny sends',async t=>{
  const env=await setup(t);
  assert.equal(env.registered.length,1);
  const {binding}=env.registered[0];
  assert.deepEqual(binding.find(env.doc),[env.el]);
  binding.renderValue(env.el,payload(stat(31)));
  assert.equal(env.el.querySelector('.sh-agent-view-value').textContent,'31');
  assert.equal(env.el.querySelector('h2').textContent,'Overview');
  assert.match(env.el.querySelector('.sh-agent-view-kicker').textContent,/assistant/);
});

test('an absent measure reads n/a, never zero or blank',async t=>{
  const env=await setup(t);
  env.api.render(env.el,payload(stat(null)));
  assert.equal(env.el.querySelector('.sh-agent-view-value').textContent,'n/a');
  env.api.render(env.el,payload(stat(0)));
  assert.equal(env.el.querySelector('.sh-agent-view-value').textContent,'0');
  env.api.render(env.el,payload({id:'t',kind:'table',title:'Regions',state:'ok',columns:['Region','Revenue (EUR)'],rows:[['North',null],['South',0]]}));
  assert.deepEqual([...env.el.querySelectorAll('tbody td')].map(td=>td.textContent),['North','n/a','South','0']);
});

test('titles, labels, messages and cell values are literal text',async t=>{
  const env=await setup(t);
  env.api.render(env.el,{version:1,view:{title:HOSTILE,tiles:[
    stat(1,{title:HOSTILE,label:HOSTILE}),
    {id:'e',kind:'bar',title:HOSTILE,state:'error',message:HOSTILE},
    {id:'t',kind:'table',title:HOSTILE,state:'ok',columns:[HOSTILE],rows:[[HOSTILE]]}]}});
  assert.equal(env.el.querySelectorAll('img,script').length,0);
  assert.equal(env.el.querySelector('h2').textContent,HOSTILE);
  assert.equal(env.el.querySelector('td').textContent,HOSTILE);
  assert.equal(env.el.querySelector('.sh-agent-view-problem').textContent,HOSTILE);
});

test('empty and failed tiles say so instead of showing a value',async t=>{
  const env=await setup(t);
  env.api.render(env.el,payload({id:'a',kind:'stat',title:'Total',state:'empty'},
    {id:'b',kind:'bar',title:'By region',state:'error',message:'More than 10 series.'}));
  const tiles=[...env.el.querySelectorAll('article')];
  assert.match(tiles[0].textContent,/No data/);assert.equal(tiles[0].querySelector('.sh-agent-view-value'),null);
  assert.match(tiles[1].textContent,/More than 10 series/);
  await env.flush();assert.equal(env.embeds.length,0);
});

test('chart tiles use the locked-down embed and offer their data as a table',async t=>{
  const env=await setup(t);
  env.api.render(env.el,payload(bar()));await env.flush();
  assert.equal(env.embeds.length,1);
  const {spec,options,container}=env.embeds[0];
  assert.ok(env.el.contains(container));
  assert.equal(spec.mark.type,'bar');assert.equal(spec.encoding.x.title,'Region');
  assert.equal(options.actions,false);await assert.rejects(options.loader.load('https://external.example/data'));
  assert.equal(env.el.querySelector('[role=status]').hidden,true);
  assert.equal(env.el.querySelectorAll('tbody tr').length,0);
  const details=env.el.querySelector('details');details.open=true;details.dispatchEvent(new env.window.Event('toggle'));
  assert.deepEqual([...env.el.querySelectorAll('tbody td')].map(td=>td.textContent),['North','10','<b>South</b>','n/a']);
});

test('a new value disposes the previous charts; an unchanged value redraws nothing',async t=>{
  const env=await setup(t);
  env.api.render(env.el,payload(bar()));await env.flush();
  const first=env.el.firstChild;
  env.api.render(env.el,payload(bar()));await env.flush();
  assert.equal(env.el.firstChild,first);assert.equal(env.embeds.length,1);assert.equal(env.finalized(),0);
  env.api.render(env.el,payload(bar({title:'Changed'})));await env.flush();
  assert.equal(env.embeds.length,2);assert.equal(env.finalized(),1);
  env.api.render(env.el,{version:1,view:null});
  assert.equal(env.finalized(),2);assert.equal(env.el.childNodes.length,0);assert.equal(env.el.hidden,true);
});

test('a refresh that changes one tile leaves an unchanged chart drawn',async t=>{
  const env=await setup(t);
  env.api.render(env.el,payload(stat(1),bar()));await env.flush();
  const chartTile=env.el.querySelector('article[data-tile="tile_2"]');
  env.api.render(env.el,payload(stat(2),bar()));await env.flush();
  assert.equal(env.el.querySelector('.sh-agent-view-value').textContent,'2');
  assert.equal(env.el.querySelector('article[data-tile="tile_2"]'),chartTile,'the chart tile is the same element');
  assert.equal(env.embeds.length,1);assert.equal(env.finalized(),0);
  assert.deepEqual([...env.el.querySelectorAll('article')].map(a=>a.dataset.tile),['tile_1','tile_2']);
  env.api.render(env.el,payload(bar(),stat(2)));await env.flush();
  assert.deepEqual([...env.el.querySelectorAll('article')].map(a=>a.dataset.tile),['tile_2','tile_1'],'tiles follow the new order');
  assert.equal(env.embeds.length,1);assert.equal(env.finalized(),0);
  env.api.render(env.el,payload(stat(2)));await env.flush();
  assert.equal(env.finalized(),1,'a removed chart is disposed');
});

test('a chart that finishes after its view was replaced is finalized',async t=>{
  const env=await setup(t);let complete,finalized=0;
  env.window.shinyhubAgentChartVendor.vegaEmbed=()=>new Promise(r=>complete=r);
  env.api.render(env.el,payload(bar()));await env.flush();
  env.api.render(env.el,{version:1,view:null});
  complete({view:{finalize(){finalized++}}});await env.flush();
  assert.equal(finalized,1);
});

test('a chart library failure falls back to the data table',async t=>{
  const env=await setup(t,{fail:true});
  env.api.render(env.el,payload(bar()));await env.flush();
  assert.match(env.el.querySelector('[role=status]').textContent,/could not load/);
  assert.equal(env.el.querySelector('details').open,true);
  assert.equal(env.el.querySelectorAll('tbody tr').length,2);
});

test('page tiles do not consume the conversation chart allowance',async t=>{
  const env=await setup(t), log=env.doc.querySelector('#log');
  env.api.render(env.el,payload(bar(),bar({id:'tile_3',title:'Second'})));await env.flush();
  for(let i=0;i<8;i++){log.append(env.window.shinyhubAgentCharts.create({...chart(),id:String(i),title:'Inline'}));await env.flush();}
  assert.equal(log.querySelectorAll('figure').length,8);assert.equal(env.finalized(),0);
  env.window.shinyhubAgentCharts.dispose(log);
  assert.equal(env.finalized(),8);assert.equal(env.el.querySelectorAll('article').length,2);
});

test('Dismiss asks the server to remove the view',async t=>{
  const env=await setup(t);
  env.api.render(env.el,payload(stat(1)));
  env.el.querySelector('button').click();
  assert.equal(env.inputs.length,1);
  assert.equal(env.inputs[0].id,'.shinyhub_agent_view_dismiss');
  assert.equal(env.inputs[0].options.priority,'event');
  assert.equal(env.el.querySelector('article') !== null,true);
});

test('an unknown payload version shows nothing',async t=>{
  const env=await setup(t);
  env.api.render(env.el,{version:2,view:{title:'Later format',tiles:[stat(1)]}});
  assert.equal(env.el.childNodes.length,0);assert.equal(env.el.hidden,true);
});

test('the binding registers once Shiny arrives after the script',async t=>{
  const env=await setup(t,{shiny:false});
  const registered=[];let bound=0;
  env.window.jQuery=scope=>({find:selector=>[...scope.querySelectorAll(selector)]});
  env.window.Shiny={OutputBinding:function(){},outputBindings:{register:b=>registered.push(b)},shinyapp:{},bindAll(){bound++}};
  env.doc.dispatchEvent(new env.window.Event('DOMContentLoaded'));
  assert.equal(registered.length,1);assert.equal(bound,1);
});

test('a small nonzero reading is not shown as zero',async t=>{
  const env=await setup(t);
  env.api.render(env.el,payload(stat(0.0003)));
  assert.equal(env.el.querySelector('.sh-agent-view-value').textContent,(0.0003).toLocaleString(undefined,{maximumSignificantDigits:3}));
  assert.notEqual(env.el.querySelector('.sh-agent-view-value').textContent,'0');
  env.api.render(env.el,payload(stat(-0.00000012)));
  assert.notEqual(Number(env.el.querySelector('.sh-agent-view-value').textContent.replace(/[^0-9.e-]/g,'')),0);
  env.api.render(env.el,payload(stat(1234.5678)));
  assert.equal(env.el.querySelector('.sh-agent-view-value').textContent,(1234.57).toLocaleString());
});

test('a live refresh keeps an open data table and the focused control',async t=>{
  const env=await setup(t);
  env.api.render(env.el,payload(stat(1),bar()));await env.flush();
  const details=env.el.querySelector('details');details.open=true;details.dispatchEvent(new env.window.Event('toggle'));
  env.el.querySelector('summary').focus();
  assert.equal(env.doc.activeElement,env.el.querySelector('summary'));
  env.api.render(env.el,payload(stat(2),bar({scope:[['Year','2024']]})));await env.flush();
  assert.equal(env.el.querySelector('.sh-agent-view-value').textContent,'2');
  assert.notEqual(env.el.querySelector('details'),details);
  assert.equal(env.el.querySelector('details').open,true);
  assert.equal(env.el.querySelectorAll('tbody tr').length,2);
  assert.equal(env.doc.activeElement,env.el.querySelector('summary'));
  const scroller=env.el.querySelector('.sh-agent-view-table');scroller.scrollTop=40;scroller.scrollLeft=7;
  env.api.render(env.el,payload(stat(2.5),bar({scope:[['Year','2025']]})));await env.flush();
  assert.notEqual(env.el.querySelector('.sh-agent-view-table'),scroller);
  assert.equal(env.el.querySelector('.sh-agent-view-table').scrollTop,40);
  assert.equal(env.el.querySelector('.sh-agent-view-table').scrollLeft,7);
  // A tile that is gone takes its state with it; the rest of the page keeps focus.
  env.api.render(env.el,payload(stat(3),{id:'tile_9',kind:'bar',title:'Other',state:'ok',chart:chart()}));await env.flush();
  assert.equal(env.el.querySelector('details').open,false);
  env.el.querySelector('button').focus();
  env.api.render(env.el,payload(stat(4)));
  assert.equal(env.doc.activeElement,env.el.querySelector('button'));
});

test('a chart removed at any moment after it finishes drawing is finalized once',async t=>{
  const env=await setup(t);
  env.api.render(env.el,payload(bar()));await env.flush();env.api.render(env.el,null);
  for(let ticks=0;ticks<10;ticks++){
    let complete,finalized=0;env.window.shinyhubAgentChartVendor.vegaEmbed=()=>new Promise(r=>complete=r);
    env.api.render(env.el,payload(bar()));await env.flush();
    complete({view:{finalize(){finalized++},height(){return this},resize(){return this},runAsync(){return Promise.resolve()}}});
    for(let i=0;i<ticks;i++)await Promise.resolve();
    env.api.render(env.el,null);await env.flush();
    assert.equal(finalized,1,'removed '+ticks+' microtasks after the draw completed');
  }
});

test('a chart that finishes drawing after the render timeout is finalized',async t=>{
  const env=await setup(t);let complete,finalized=0,expire;
  const realTimeout=env.window.setTimeout.bind(env.window);
  env.window.setTimeout=(fn,ms)=>{if(ms===20000){expire=fn;return 0}return realTimeout(fn,ms)};
  env.window.shinyhubAgentChartVendor.vegaEmbed=()=>new Promise(r=>complete=r);
  env.api.render(env.el,payload(bar()));await env.flush();
  assert.equal(typeof expire,'function');assert.equal(typeof complete,'function');
  expire();await env.flush();
  assert.match(env.el.querySelector('[role=status]').textContent,/could not load/);
  complete({view:{finalize(){finalized++}}});await env.flush();
  assert.equal(finalized,1);
  env.api.render(env.el,{version:1,view:null});
  assert.equal(finalized,1);
});

test('a narrowed tile names its scope as literal text',async t=>{
  const env=await setup(t);
  env.api.render(env.el,payload(stat(7,{scope:[['Region',HOSTILE],['Channel','online']]}),stat(10,{id:'tile_2'}),stat(1,{id:'tile_3',scope:[['Region',5],'x']})));
  const [narrowed,whole,odd]=env.el.querySelectorAll('article');
  assert.equal(narrowed.querySelector('.sh-agent-view-scope').textContent,'Only Region: '+HOSTILE+', Channel: online');
  assert.equal(narrowed.querySelector('img'),null);
  assert.equal(whole.querySelector('.sh-agent-view-scope'),null);
  assert.equal(odd.querySelector('.sh-agent-view-scope'),null);
});
