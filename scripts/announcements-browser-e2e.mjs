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
const { chromium } = require('playwright');
const binary = resolve(process.env.SHINYHUB_E2E_BINARY || 'bin/shinyhub');
const work = await mkdtemp(join(tmpdir(),'shinyhub-announcements-browser-'));
const username='announcement-admin',password=randomBytes(24).toString('hex');
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
  await writeFile(config,`server:\n  host: 127.0.0.1\n  port: ${port}\n  base_url: ${hub}\n  app_origin: ${apps}\n  app_nav: false\n  shutdown_apps: stop\nauth:\n  secret: ${randomBytes(32).toString('hex')}\ndatabase:\n  dsn: ${JSON.stringify(join(work,'hub.db'))}\nstorage:\n  apps_dir: ${JSON.stringify(join(work,'apps'))}\n  app_data_dir: ${JSON.stringify(join(work,'app-data'))}\n`,{mode:0o600});
  await command([binary,'init','--config',config,'--admin-user',username,'--admin-password-file',passwordFile],'init');
  server=spawn(binary,['serve','--config',config,'--no-browser'],{cwd:work,env,detached:true});let logs='';server.stdout.on('data',x=>logs+=x);server.stderr.on('data',x=>logs+=x);
  await until(()=>fetch(apiHub+'/healthz',{headers:{'User-Agent':'shinyhub-announcements-test'}}).then(r=>r.ok).catch(()=>false),'server did not start');
  const login=await fetch(apiHub+'/api/auth/login',{method:'POST',headers:{'Content-Type':'application/json','User-Agent':'shinyhub-announcements-test'},body:JSON.stringify({username,password})});assert.equal(login.status,200);const {token}=await login.json();
  const api=async(method,path,body,status=200)=>{const r=await fetch(apiHub+path,{method,headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json','User-Agent':'shinyhub-announcements-test'},body:body===undefined?undefined:JSON.stringify(body)});assert.equal(r.status,status,`${method} ${path}: ${await r.clone().text()}`);return r.json();};
  const fixture=join(work,'fixture');await mkdir(fixture);
  await writeFile(join(fixture,'shinyhub.toml'),'[app]\ncommand = ["python3", "app.py", "--port", "{port}", "--host", "{host}"]\n');
  await writeFile(join(fixture,'app.py'),`from http.server import BaseHTTPRequestHandler,HTTPServer\nimport argparse\np=argparse.ArgumentParser();p.add_argument('--port',type=int);p.add_argument('--host');a=p.parse_args()\nclass Handler(BaseHTTPRequestHandler):\n def do_GET(self):\n  self.send_response(200);self.send_header('Content-Type','text/html');self.end_headers();self.wfile.write(b'<!doctype html><html lang="en"><head><title>Working app</title></head><body><h1>Working app</h1><button id="calculate">Calculate</button><script>document.getElementById("calculate").onclick=()=>document.getElementById("calculate").textContent="Still working"</script><p>App content remains available.</p></body></html>')\nHTTPServer((a.host,a.port),Handler).serve_forever()\n`);
  const deploy=async(dir,slug)=>command([binary,'deploy',dir,'--slug',slug,'--visibility','public','--config',join(work,'client.json'),'--output','json'],'deploy-'+slug,{SHINYHUB_HOST:apiHub,SHINYHUB_TOKEN:token});
  await deploy(fixture,'notice-test');
  browser=await chromium.launch({channel:process.env.SHINYHUB_E2E_BROWSER_CHANNEL||undefined,headless:true});
  const context=await browser.newContext({ignoreHTTPSErrors:true,viewport:{width:1440,height:1000},userAgent:'shinyhub-announcements-browser-test'});
  await context.addInitScript(()=>{const attach=Element.prototype.attachShadow;window.__noticeRoots=new WeakMap();Element.prototype.attachShadow=function(options){const root=attach.call(this,options);window.__noticeRoots.set(this,root);return root;};});
  const page=await context.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.goto(hub+'/announcements');await page.locator('body[data-auth="out"]').waitFor();
  await page.locator('#login-username').fill(username);await page.locator('#login-password').fill(password);await page.getByRole('button',{name:'Sign in',exact:true}).click();await page.getByRole('heading',{name:'Announcements',exact:true}).waitFor();
  await page.getByRole('button',{name:'New announcement',exact:true}).click();await page.getByLabel('Title',{exact:true}).fill('Scheduled maintenance');await page.getByLabel('Message',{exact:true}).fill('3 October, 22:00–22:30 CEST. Apps may disconnect. Save your work before 22:00.');
  await page.getByRole('button',{name:'Save draft',exact:true}).click();await page.getByText('Announcement saved.',{exact:true}).waitFor();
  const list=await api('GET','/api/announcements');let notice=list.announcements[0];assert.equal(notice.publication,'draft');
  const app=await context.newPage();app.on('pageerror',e=>errors.push(e.message));await app.goto(apps+'/app/notice-test/');await app.getByRole('heading',{name:'Working app'}).waitFor();
  assert.equal(await app.evaluate(()=>document.getElementById('shinyhub-platform-announcements')?.hidden),true);
  await page.getByRole('button',{name:'Publish now',exact:true}).click();await page.getByText('Announcement published.',{exact:true}).waitFor();
  await app.bringToFront();assert.equal(await app.evaluate(()=>document.visibilityState),'visible');
  const rootText=tab=>tab.evaluate(()=>{const host=document.getElementById('shinyhub-platform-announcements');return host && !host.hidden ? window.__noticeRoots.get(host).textContent:'';});
  await until(async()=> (await rootText(app)).includes('Scheduled maintenance'),'open app did not receive publication');
  await app.getByRole('button',{name:'Calculate',exact:true}).click();await app.getByRole('button',{name:'Still working',exact:true}).waitFor();
  await app.evaluate(()=>window.__noticeRoots.get(document.getElementById('shinyhub-platform-announcements')).querySelector('[data-dismiss]').click());
  assert.equal(await app.evaluate(()=>document.getElementById('shinyhub-platform-announcements').hidden),true);
  await page.getByLabel('Message',{exact:true}).fill('Maintenance begins soon. Save your work.');await page.getByRole('button',{name:'Save changes',exact:true}).click();await page.getByText('Announcement published.',{exact:true}).waitFor();
  await app.bringToFront();
  await until(async()=> (await rootText(app)).includes('Maintenance begins soon'),'edited revision did not reappear');
  await app.evaluate(()=>document.getElementById('shinyhub-platform-announcements').remove());await until(()=>app.evaluate(()=>!!document.getElementById('shinyhub-platform-announcements')),'hosted DOM replacement removed announcement');
  for(const [theme,width] of [['dark',1440],['light',390]]) {
    await page.setViewportSize({width,height:1000});await page.evaluate(theme=>{document.documentElement.dataset.theme=theme;},theme);await delay(100);
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,`${theme}: horizontal overflow`);
    await page.evaluate(()=>window.scrollTo(0,0));await page.screenshot({path:join(work,`admin-${theme}.png`),fullPage:true});
    await app.setViewportSize({width,height:1000});await app.evaluate(theme=>document.documentElement.dataset.theme=theme,theme);await delay(100);
    assert.equal(await app.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,`${theme}: app overflow`);
    await app.evaluate(()=>window.scrollTo(0,0));await app.screenshot({path:join(work,`app-${theme}.png`),fullPage:true});
  }
  // A public feed is available on the app origin; the administration API is not.
  assert.equal((await context.request.get(apps+'/api/announcements')).status(),404);
  notice=(await api('GET','/api/announcements')).announcements[0];
  notice=await api('PATCH','/api/announcements/'+notice.id,{expected_revision:notice.revision,ends_at:new Date(Date.now()+2000).toISOString()});
  await app.evaluate(()=>window.dispatchEvent(new Event('shinyhub:announcements-changed')));await until(()=>app.evaluate(()=>document.getElementById('shinyhub-platform-announcements').hidden),'expired notice remained visible',10000);
  notice=await api('PATCH','/api/announcements/'+notice.id,{expected_revision:notice.revision,starts_at:new Date(Date.now()+1500).toISOString(),ends_at:new Date(Date.now()+60000).toISOString()});
  await app.evaluate(()=>window.dispatchEvent(new Event('shinyhub:announcements-changed')));await until(async()=> (await rootText(app)).includes('Maintenance begins soon'),'scheduled notice did not appear at boundary',10000);
  notice=await api('PATCH','/api/announcements/'+notice.id,{expected_revision:notice.revision,publication:'disabled'});await app.evaluate(()=>window.dispatchEvent(new Event('shinyhub:announcements-changed')));await until(()=>app.evaluate(()=>document.getElementById('shinyhub-platform-announcements').hidden),'disabled notice remained visible',10000);
  const publicTab=await browser.newPage({ignoreHTTPSErrors:true,userAgent:'shinyhub-announcements-browser-test'});await publicTab.goto(hub+'/login');await publicTab.locator('body[data-auth="out"]').waitFor();assert.equal(await publicTab.locator('body').getAttribute('data-auth'),'out');await publicTab.close();
  // Optional real-framework smoke matrix, using the same production proxy.
  if(process.env.SHINYHUB_ANNOUNCEMENT_FRAMEWORKS==='1') {
    notice=await api('PATCH','/api/announcements/'+notice.id,{expected_revision:notice.revision,publication:'published',starts_at:null,ends_at:null});
    for(const [slug,path] of [['dash-demo','examples/dash-demo'],['streamlit','examples/streamlit-demo'],['shiny','cmd/shinyhub/example_app']]) {
      const dir=join(work,slug);await cp(resolve(path),dir,{recursive:true,filter:path=>!path.includes('__pycache__') && !path.includes('.venv')});await deploy(dir,slug);const tab=await context.newPage();await tab.goto(apps+`/app/${slug}/`);await until(async()=> (await rootText(tab)).includes('Maintenance begins soon'),`${slug} missing announcement`);const heading=slug==='dash-demo'?'Make a live metric move.':slug==='streamlit'?'Turn an input into an answer.':'Explore a live energy forecast.';await tab.getByRole('heading',{name:heading,exact:true}).waitFor();
      for(const width of [1440,390]){await tab.setViewportSize({width,height:1000});assert.equal(await tab.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,`${slug} overflow`);await tab.screenshot({path:join(work,`${slug}-${width}.png`)});}await tab.close();
    }
    const rdir=join(work,'r-shiny');await mkdir(rdir);await writeFile(join(rdir,'shinyhub.toml'),`[app]\ncommand = ["Rscript", "-e", "shiny::runApp('.', host='{host}', port={port})"]\n`);await writeFile(join(rdir,'app.R'),`library(shiny)\nshinyApp(fluidPage(h1('R Shiny'),actionButton('calculate','Calculate'),textOutput('result')),function(input,output,session){output$result<-renderText(input$calculate)})\n`);await deploy(rdir,'r-shiny');const tab=await context.newPage();await tab.goto(apps+'/app/r-shiny/');await until(async()=> (await rootText(tab)).includes('Maintenance begins soon'),'R Shiny missing announcement');await tab.getByRole('button',{name:'Calculate'}).click();for(const width of [1440,390]){await tab.setViewportSize({width,height:1000});await tab.screenshot({path:join(work,`r-shiny-${width}.png`)});}await tab.close();
  }
  assert.deepEqual(errors,[],'console raised browser errors');console.log(`Announcement browser checks passed. Screenshots: ${work}`);
  await writeFile(join(work,'server.log'),logs,{mode:0o600});
} finally {
  await browser?.close();for(const socket of proxySockets)socket.destroy();if(tlsProxy)await new Promise(resolve=>tlsProxy.close(resolve));if(server){try{process.kill(-server.pid,'SIGTERM');}catch{};await Promise.race([once(server,'exit'),delay(10000)]);}
}
