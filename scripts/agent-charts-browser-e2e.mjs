#!/usr/bin/env node
// Exercise the real, locally bundled chart renderer with native and fallback chat.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { readFile, mkdir } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
const require = createRequire(import.meta.url);
const driverRequire = createRequire(new URL('../loadtest/render/driver/package.json', import.meta.url));
const engines = driverRequire('playwright');
const engine = process.env.SHINYHUB_E2E_BROWSER || 'chromium';
const axe = require('axe-core');
const base = new URL('../packaging/python-agent/src/shinyhub_agent/www/', import.meta.url);
const assets = new Map(await Promise.all(['chat.js', 'chat.css', 'charts.js', 'vendor/vega.min.js',
  'vendor/vega-lite.min.js', 'vendor/vega-embed.min.js'].map(async p => ['/' + p, await readFile(new URL(p, base))])));
assets.set('/nav.js', await readFile(new URL('../internal/appnav/assets/nav.min.js', import.meta.url)));
const server = createServer((req, res) => {
  if (assets.has(req.url)) { res.setHeader('Content-Type', req.url.endsWith('.css') ? 'text/css' : 'text/javascript'); res.end(assets.get(req.url)); return; }
  if (req.url === '/nav.json') { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify({apps:[{slug:'demo',name:'Demo',url:'/app/demo/'}]})); return; }
  res.setHeader('Content-Type', 'text/html');
  if (req.url === '/strict-csp') res.setHeader('Content-Security-Policy', "script-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'");
  res.end(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Chart test</title><link rel="stylesheet" href="/chat.css"></head>
  <body><main><h1>Operations dashboard</h1></main><script>
  window.vega = {hostLibrary:true}; window.vegaLite = {hostLibrary:true}; window.vegaEmbed = {hostLibrary:true};
  window.define = function(){throw new Error("Charts must not use host AMD loader")}; window.define.amd = {};
  window.handlers = new Map(); window.Shiny = {addCustomMessageHandler(n,h){handlers.set(n,h)},setInputValue(n,v){if(v.message)window.request=v}};
  window.chatEvent = e => handlers.get('shinyhub-agent-chat-event')({version:1,session:'chart-session',requestId:request.requestId,...e});
  </script>${req.url.startsWith('/native') ? '<script src="/nav.js" id="shinyhub-app-nav" data-nav-url="/nav.json" data-current-slug="demo" data-current-name="Demo"></script>' : ''}
  <script src="/charts.js"></script><script src="/chat.js"></script><script>handlers.get('shinyhub-agent-chat-capabilities')({version:1,enabled:true,session:'chart-session'});</script></body></html>`);
});
server.listen(0, '127.0.0.1'); await once(server, 'listening');
const origin = `http://127.0.0.1:${server.address().port}`;
const qa = process.env.SHINYHUB_CHART_QA_DIR || join(tmpdir(), 'shinyhub-chart-qa'); await mkdir(qa, {recursive:true});
let browser;
const rows = [
  {Month:'2026-01-01', Revenue:120, Region:'North'}, {Month:'2026-02-01', Revenue:190, Region:'North'},
  {Month:'2026-03-01', Revenue:160, Region:'North'}, {Month:'2026-01-01', Revenue:90, Region:'South'},
  {Month:'2026-02-01', Revenue:135, Region:'South'}, {Month:'2026-03-01', Revenue:180, Region:'South'},
];
const chart = {id:'chart-test',dataset_id:'dataset-test',kind:'line',title:'Monthly revenue by region',
  x:'Month',y:'Revenue',color:'Region',fields:{Month:'temporal',Revenue:'quantitative',Region:'nominal'},rows,source:'Authorized sales · EUR thousands'};
try {
  browser = await engines[engine].launch({...(engine === 'chromium' ? {channel:process.env.SHINYHUB_E2E_BROWSER_CHANNEL || 'chrome'} : {}),headless:true});
  for (const native of [false,true]) for (const width of [1280,375]) {
    const tab = await browser.newPage({viewport:{width,height:900},acceptDownloads:true});
    const errors = [], external = [];
    tab.on('pageerror', e => errors.push(e.message));
    tab.on('request', r => { if(!r.url().startsWith(origin)) external.push(r.url()); });
    await tab.goto(origin + (native ? '/native' : '/fallback'));
    await tab.waitForFunction(() => window.shinyhubAgentChat?.open());
    assert.equal(await tab.locator('script[src*="vendor/"]').count(), 0, 'libraries load on demand');
    await tab.locator('.sh-agent-input').fill('Compare monthly revenue by region'); await tab.locator('.sh-agent-send').click();
    await tab.evaluate(chart => { chatEvent({type:'chart',chart}); chatEvent({type:'delta',text:'Revenue by region, in EUR thousands. These values are a snapshot.'});chatEvent({type:'done'}); }, chart);
    await tab.locator('.sh-agent-chart-plot svg').waitFor();
    await tab.waitForFunction(() => document.querySelector('.sh-agent-chart-status').hidden);
    assert.equal(await tab.locator('.sh-agent-chart').count(),1);
    assert.equal(await tab.locator('.sh-agent-chart tbody tr').count(),0);
    await tab.locator('.sh-agent-chart summary').click();
    await tab.locator('.sh-agent-chart tbody tr').first().waitFor();
    assert.equal(await tab.locator('.sh-agent-chart tbody tr').count(),6);
    const plot = await tab.locator('.sh-agent-chart-plot').boundingBox(); assert.ok(plot.width <= width);
    await tab.evaluate(() => { if (!window.define.amd) throw new Error("Host AMD changed"); delete window.define; });
    await tab.addScriptTag({content:axe.source});
    const violations = await tab.evaluate(async () => (await axe.run(document.querySelector('.sh-agent-chart'))).violations);
    assert.deepEqual(violations.map(v => v.id), []);
    await tab.screenshot({path:`${qa}/${native?'native':'fallback'}-${width}.png`});
    await tab.getByRole('button',{name:'Expand chart: Monthly revenue by region'}).click();
    await tab.getByRole('dialog',{name:'Monthly revenue by region',exact:true}).waitFor({state:'visible'});
    assert.equal(await tab.locator('dialog .sh-agent-chart').count(),1);
    await tab.screenshot({path:`${qa}/${native?'native':'fallback'}-${width}-expanded.png`});
    await tab.keyboard.press('Escape');
    await tab.getByRole('dialog',{name:'Monthly revenue by region',exact:true}).waitFor({state:'detached'});
    const [download] = await Promise.all([tab.waitForEvent('download'),tab.getByRole('button',{name:'Download SVG'}).click()]);
    assert.equal(download.suggestedFilename(),'chart.svg');
    // All four chart types use the same validated schema.
    for (const kind of ['bar','area','scatter']) {
      await tab.locator('.sh-agent-input').fill('Change chart');await tab.locator('.sh-agent-send').click();
      await tab.evaluate(({chart,kind}) => { var next={...chart,id:'chart-'+kind,kind};
        if(kind==='scatter') {next.x='Revenue'; next.color=undefined;} chatEvent({type:'chart',chart:next});chatEvent({type:'delta',text:'Updated chart.'});chatEvent({type:'done'}); },{chart,kind});
    }
    await tab.waitForFunction(() => document.querySelectorAll('.sh-agent-chart-plot svg').length === 4);
    // Tooltips are real interactive output, not only SVG title attributes.
    await tab.locator('.sh-agent-chart').first().locator('path[aria-roledescription="point"]').first().hover();
    await tab.locator('#vg-tooltip-element.visible').waitFor({state:'visible'});
    await tab.evaluate(() => chatEvent({type:'reset'}));
    assert.equal(await tab.locator('.sh-agent-chart').count(),0);
    assert.equal(await tab.locator('#vg-tooltip-element.visible').count(),0, 'reset removes chart tooltip');
    assert.equal(await tab.evaluate(()=>vega.hostLibrary && vegaLite.hostLibrary && vegaEmbed.hostLibrary),true);
    assert.deepEqual(errors,[]); assert.deepEqual(external,[]);
    await tab.close(); console.log(`Charts passed: ${native?'native':'fallback'} ${width}px`);
  }
  // Rendering failures keep the complete source table and do not break chat.
  const tab = await browser.newPage(); await tab.route('**/vendor/**',r=>r.abort());
  await tab.goto(origin+'/fallback');await tab.waitForFunction(()=>window.shinyhubAgentChat?.open());
  await tab.locator('.sh-agent-input').fill('Graph');await tab.locator('.sh-agent-send').click();
  await tab.evaluate(chart=>{chatEvent({type:'chart',chart});chatEvent({type:'delta',text:'Snapshot.'});chatEvent({type:'done'});},chart);
  await tab.getByText('Chart could not load. View data to inspect the values.').waitFor();
  assert.equal(await tab.locator('details[open] tbody tr').count(),6);
  await tab.unroute('**/vendor/**');
  await tab.getByRole('button',{name:'Retry chart'}).click();
  await tab.locator('.sh-agent-chart-plot svg').waitFor();
  await tab.waitForFunction(()=>document.querySelector('.sh-agent-chart-status').hidden);
  await tab.close();console.log('Chart failure fallback and retry passed');
  // A deployment that disallows expression compilation must retain usable data.
  const strict = await browser.newPage();
  await strict.goto(origin+'/strict-csp');
  await strict.waitForFunction(()=>window.shinyhubAgentChat?.open());
  await strict.locator('.sh-agent-input').fill('Graph');await strict.locator('.sh-agent-send').click();
  await strict.evaluate(chart=>{chatEvent({type:'chart',chart});chatEvent({type:'delta',text:'Snapshot.'});chatEvent({type:'done'});},chart);
  await strict.getByText('Chart could not load. View data to inspect the values.').waitFor();
  assert.equal(await strict.locator('details[open] tbody tr').count(),6);
  assert.equal(await strict.getByRole('button',{name:'Retry chart'}).isEnabled(),true);
  await strict.locator('.sh-agent-input').fill('Can still ask another question');
  assert.equal(await strict.locator('.sh-agent-send').isEnabled(),true);
  await strict.close();console.log('Strict CSP source-data fallback passed');
} finally {await browser?.close();server.close();await once(server,'close');}
