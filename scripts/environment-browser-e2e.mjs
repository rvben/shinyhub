#!/usr/bin/env node
// Isolated production-server checks. All credentials, apps, and screenshots
// live in a disposable /tmp directory; no existing server or profile is used.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { once } from 'node:events';
import { createServer } from 'node:net';
import { createServer as createHTTPS } from 'node:https';
import { request as httpRequest } from 'node:http';
import { createRequire } from 'node:module';
import { mkdtemp, mkdir, writeFile, readFile, cp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
const require = createRequire(new URL('../loadtest/render/driver/package.json',import.meta.url));
const engines = require(process.env.SHINYHUB_E2E_PLAYWRIGHT || 'playwright');
const binary = resolve(process.env.SHINYHUB_E2E_BINARY || 'bin/shinyhub');
const work = await mkdtemp(join(tmpdir(),'shinyhub-environment-browser-'));
const username='environment-admin',password=randomBytes(24).toString('hex');
const env=Object.fromEntries(Object.entries(process.env).filter(([key])=>!key.startsWith('SHINYHUB_')));
let server,browser,tlsProxy;const proxySockets=new Set();
async function command(args,name,extraEnv={}) {
  const child=spawn(args[0],args.slice(1),{cwd:work,env:{...env,...extraEnv}});let output='';child.stdout.on('data',x=>output+=x);child.stderr.on('data',x=>output+=x);
  const [code]=await once(child,'exit');await writeFile(join(work,name+'.log'),output,{mode:0o600});assert.equal(code,0,`${name} failed: ${output.slice(-2000)}`);
}
async function until(fn,message,timeout=45000) {const end=Date.now()+timeout;while(Date.now()<end){if(await fn())return;await delay(200);}throw new Error(message);}
try {
  const reservation=createServer();reservation.listen(0,'127.0.0.1');await once(reservation,'listening');const port=reservation.address().port;await new Promise(resolve=>reservation.close(resolve));
  const apiHub=`http://127.0.0.1:${port}`;
  const cert=join(work,'cert.pem'),key=join(work,'key.pem');
  await command(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',key,'-out',cert,'-subj','/CN=localhost','-days','1'],'certificate');
  const proxyOptions=req=>({host:'127.0.0.1',port,method:req.method,path:req.url,headers:{...req.headers,'x-forwarded-proto':'https'}});
  tlsProxy=createHTTPS({key:await readFile(key),cert:await readFile(cert)},(req,res)=>{
    const upstream=httpRequest(proxyOptions(req),response=>{res.writeHead(response.statusCode,response.headers);response.pipe(res);});upstream.on('error',()=>{res.writeHead(502);res.end();});req.pipe(upstream);
  });
  tlsProxy.on('connection',socket=>{proxySockets.add(socket);socket.on('close',()=>proxySockets.delete(socket));});
  tlsProxy.on('upgrade',(req,socket,head)=>{
    const upstream=httpRequest(proxyOptions(req));upstream.on('upgrade',(response,backend,backendHead)=>{
      socket.write('HTTP/1.1 101 Switching Protocols\r\n'+Object.entries(response.headers).map(([key,value])=>`${key}: ${value}`).join('\r\n')+'\r\n\r\n');
      if(head.length)backend.write(head);if(backendHead.length)socket.write(backendHead);socket.pipe(backend).pipe(socket);socket.on('error',()=>backend.destroy());backend.on('error',()=>socket.destroy());
    });upstream.on('error',()=>socket.destroy());upstream.end();
  });
  tlsProxy.listen(0,'127.0.0.1');await once(tlsProxy,'listening');const tlsPort=tlsProxy.address().port;
  const hub=`https://127.0.0.1:${tlsPort}`,apps=`https://localhost:${tlsPort}`;
  const config=join(work,'hub.yaml'),passwordFile=join(work,'password');
  await writeFile(passwordFile,password,{mode:0o600});
  await writeFile(config,`server:\n  host: 127.0.0.1\n  port: ${port}\n  base_url: ${hub}\n  app_origin: ${apps}\n  app_nav: true\n  shutdown_apps: stop\nauth:\n  secret: ${randomBytes(32).toString('hex')}\ndatabase:\n  dsn: ${JSON.stringify(join(work,'hub.db'))}\nstorage:\n  apps_dir: ${JSON.stringify(join(work,'apps'))}\n  app_data_dir: ${JSON.stringify(join(work,'app-data'))}\n`,{mode:0o600});
  await writeFile(config, (await readFile(config,'utf8')) + 'branding:\n  environment:\n    label: Acceptance\n    color: "#f5b301"\n    message: Test environment. Data may be incomplete or out of date.\n    production_url: https://production.example\n');
  await command([binary,'init','--config',config,'--admin-user',username,'--admin-password-file',passwordFile],'init');
  server=spawn(binary,['serve','--config',config,'--no-browser'],{cwd:work,env,detached:true});let logs='';server.stdout.on('data',x=>logs+=x);server.stderr.on('data',x=>logs+=x);
  await until(()=>fetch(apiHub+'/healthz',{headers:{'User-Agent':'shinyhub-environment-test'}}).then(r=>r.ok).catch(()=>false),'server did not start');
  const login=await fetch(apiHub+'/api/auth/login',{method:'POST',headers:{'Content-Type':'application/json','User-Agent':'shinyhub-environment-test'},body:JSON.stringify({username,password})});assert.equal(login.status,200);const {token}=await login.json();
  const api=async(method,path,body,status=200)=>{const r=await fetch(apiHub+path,{method,headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json','User-Agent':'shinyhub-environment-test'},body:body===undefined?undefined:JSON.stringify(body)});assert.equal(r.status,status,`${method} ${path}: ${await r.clone().text()}`);return r.json();};
  const fixture=join(work,'fixture');await mkdir(fixture);
  await writeFile(join(fixture,'shinyhub.toml'),'[app]\ncommand = ["python3", "app.py", "--port", "{port}", "--host", "{host}"]\n');
  await writeFile(join(fixture,'app.py'),String.raw`from http.server import BaseHTTPRequestHandler,HTTPServer
import argparse
p=argparse.ArgumentParser();p.add_argument('--port',type=int);p.add_argument('--host');a=p.parse_args()
class Handler(BaseHTTPRequestHandler):
 def do_GET(self):
  self.send_response(200);self.send_header('Content-Type','text/html')
  if self.path.startswith('/strict'): self.send_header('Content-Security-Policy',"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'")
  if self.path.startswith('/blocked'): self.send_header('Content-Security-Policy',"script-src 'none'; img-src 'none'")
  self.end_headers();self.wfile.write(b'<!doctype html><html lang="en"><head><title>Token FinOps</title><link rel="icon" href="/authored.ico"></head><body><h1>Token FinOps</h1><button id="calculate">Calculate</button><script>document.getElementById("calculate").onclick=()=>{document.title="Updated report";history.replaceState({},"","?report=new&__shinyhub_launch=secret#chart");document.getElementById("calculate").textContent="Still working";}</script><p>App content remains available.</p></body></html>')
HTTPServer((a.host,a.port),Handler).serve_forever()
`);
  const deploy=async(dir,slug)=>command([binary,'deploy',dir,'--slug',slug,'--visibility','public','--config',join(work,'client.json'),'--output','json'],'deploy-'+slug,{SHINYHUB_HOST:apiHub,SHINYHUB_TOKEN:token});
  await deploy(fixture,'environment-test');
  await api('POST','/api/announcements',{title:'Maintenance',message:'Real notice remains visible.',severity:'critical',publication:'published',dismissible:false},201);
  const me=await api('GET','/api/auth/me');assert.equal(me.environment_label,'Acceptance');
  const info=await api('GET','/api/server-info');assert.equal(info.environment_label,'Acceptance');
  await command([binary,'whoami','--output','json'],'whoami',{SHINYHUB_HOST:apiHub,SHINYHUB_TOKEN:token});
  assert.equal(JSON.parse(await readFile(join(work,'whoami.log'),'utf8')).environment_label,'Acceptance');
  const names=(process.env.SHINYHUB_E2E_BROWSERS || 'chromium,firefox,webkit').split(',');
  for(const name of names){
    browser=await engines[name].launch({headless:true});
    const context=await browser.newContext({ignoreHTTPSErrors:true,viewport:{width:1440,height:1000},userAgent:'shinyhub-environment-browser-test'});
    const page=await context.newPage();const errors=[];const consoleErrors=[];page.on('pageerror',e=>errors.push(e.message));page.on('console',m=>{if(m.type()==='error')consoleErrors.push(m.text());});
    await page.goto(hub+'/login');await page.locator('#shinyhub-environment').waitFor();
    assert.match(await page.title(),/^\[Acceptance\] /);
    assert.equal(await page.locator('#shinyhub-environment a').getAttribute('href'),'https://production.example/');
    const tile=await context.request.get(hub+'/favicon.ico');assert.equal(tile.headers()['content-type'],'image/png');
    await page.locator('#login-username').fill(username);await page.locator('#login-password').fill(password);await page.getByRole('button',{name:'Sign in',exact:true}).click();await page.locator('body[data-auth="in"]').waitFor();
    await page.goto(hub+'/apps');await page.locator('#shinyhub-environment').waitFor();assert.match(await page.title(),/^\[Acceptance\] /);
    await page.screenshot({path:join(work,name+'-hub-desktop.png')});
    // The platform shell must reserve the strip's height even after scrolling.
    await page.evaluate(()=>{document.querySelector('main').style.minHeight='2500px';window.scrollTo(0,1000);});
    await page.waitForTimeout(100);
    const desktop=await page.evaluate(()=>{const sidebar=document.getElementById('sidebar').getBoundingClientRect();const strip=document.getElementById('shinyhub-environment').getBoundingClientRect();return {top:sidebar.top,bottom:sidebar.bottom,stripBottom:strip.bottom,height:innerHeight};});
    assert.ok(desktop.top>=desktop.stripBottom-1 && desktop.bottom<=desktop.height+1,JSON.stringify(desktop));
    await page.setViewportSize({width:375,height:900});
    await page.evaluate(()=>window.scrollTo(0,document.documentElement.scrollHeight));
    await page.waitForTimeout(100);
    const mobile=await page.evaluate(()=>{const toggle=document.getElementById('sidebar-toggle').getBoundingClientRect();const strip=document.getElementById('shinyhub-environment').getBoundingClientRect();return {top:toggle.top,bottom:toggle.bottom,stripBottom:strip.bottom,height:innerHeight};});
    assert.ok(mobile.top>=mobile.stripBottom-1 && mobile.bottom<=mobile.height,JSON.stringify(mobile));
    await page.locator('#sidebar-toggle').click();
    assert.ok(await page.locator('body').evaluate(el=>el.classList.contains('sidebar-open')));
    await page.waitForFunction(()=>document.getElementById('sidebar').getBoundingClientRect().x>=-1);
    const drawer=await page.locator('#sidebar').boundingBox();
    assert.ok(drawer.x>=-1 && drawer.y>=mobile.stripBottom-1 && drawer.y+drawer.height<=900+1,JSON.stringify(drawer));
    await page.screenshot({path:join(work,name+'-hub-mobile-scrolled.png')});
    await page.setViewportSize({width:1440,height:1000});

    await page.goto(apps+'/app/environment-test/?report=old#chart');await page.getByRole('heading',{name:'Token FinOps'}).waitFor();await page.locator('#shinyhub-environment').waitFor();
    await page.locator('#shinyhub-platform-announcements').waitFor();
    assert.equal(await page.title(),'[Acceptance] Token FinOps');
    assert.equal(await page.locator('#shinyhub-environment a').getAttribute('referrerpolicy'),'no-referrer');
    await page.getByRole('button',{name:'Calculate',exact:true}).click();
    await page.waitForFunction(()=>document.title==='[Acceptance] Updated report');
    const link=page.locator('#shinyhub-environment a');await link.focus();
    assert.equal(await link.getAttribute('href'),'https://production.example/app/environment-test/?report=new#chart');
    assert.equal(await page.evaluate(()=>getComputedStyle(document.getElementById('shinyhub-environment').shadowRoot.firstElementChild).backgroundColor),'rgb(245, 179, 1)');
    for(const width of [1440,375]){
      await page.setViewportSize({width,height:900});
      const layout=await page.evaluate(()=>{const env=document.getElementById('shinyhub-environment').getBoundingClientRect();const notice=document.getElementById('shinyhub-platform-announcements').getBoundingClientRect();return {envBottom:env.bottom,noticeTop:notice.top,width:document.documentElement.scrollWidth,viewport:innerWidth};});
      assert.ok(layout.noticeTop>=layout.envBottom-1,JSON.stringify(layout));assert.ok(layout.width<=layout.viewport,JSON.stringify(layout));
      await page.screenshot({path:join(work,`${name}-app-${width}.png`)});
    }
    const strictResponse=await page.goto(apps+'/app/environment-test/strict');
    await page.locator('#shinyhub-environment').waitFor().catch(async error=>{await writeFile(join(work,name+'-strict.html'),await page.content());console.log('Strict response',strictResponse.headers(),consoleErrors,'Work',work);throw error;});
    assert.equal(await page.evaluate(()=>getComputedStyle(document.getElementById('shinyhub-environment').shadowRoot.firstElementChild).backgroundColor),'rgb(245, 179, 1)');
    await page.goto(apps+'/app/environment-test/blocked');assert.equal(await page.title(),'[Acceptance] Token FinOps');assert.equal(await page.locator('#shinyhub-environment').count(),0);
    await page.goto(apps+'/app/missing/');await page.getByRole('heading',{name:'App not found',exact:true}).waitFor();assert.equal(await page.getByRole('link',{name:'Browse apps',exact:true}).getAttribute('href'),hub+'/');await page.locator('#shinyhub-environment').waitFor();
    const raw=await context.request.get(apps+'/app/missing/');assert.equal(raw.status(),404);assert.deepEqual(await raw.json(),{error:'unknown app',slug:'missing'});
    assert.deepEqual(errors,[],name+' raised browser errors');
    await context.close();await browser.close();browser=null;console.log(name+' environment browser checks passed');
  }
  await writeFile(join(work,'server.log'),logs,{mode:0o600});console.log('Screenshots: '+work);
} finally {
  await browser?.close();for(const socket of proxySockets)socket.destroy();if(tlsProxy)await new Promise(resolve=>tlsProxy.close(resolve));if(server){try{process.kill(-server.pid,'SIGTERM');}catch{};await Promise.race([once(server,'exit'),delay(10000)]);}
}
