#!/usr/bin/env node
// Render the production dashboard with synthetic API data; no live accounts or mutations.
// Screenshots are opt-in local artifacts. Requires the render-driver Playwright install.
import assert from 'node:assert/strict';
import { checkShellConsistency } from './browser-shell-checks.mjs';
import { checkSelectPickers } from './browser-select-checks.mjs';
const shellOnly = process.env.SHINYHUB_E2E_SHELL_ONLY === '1';
import { createServer } from 'node:http';
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { resolve, extname } from 'node:path';
import { createRequire } from 'node:module';
const root=process.cwd(), out=process.env.SHINYHUB_E2E_SCREENSHOTS;
if(out) await mkdir(out,{recursive:true});
const require=createRequire(root+'/loadtest/render/driver/package.json');
const {chromium}=require('playwright');
const now=new Date().toISOString(), ago=new Date(Date.now()-3600000).toISOString(), mb=1048576;
let role='admin', mode='ready', delayedPath='';
const apps=[{slug:'forecast',name:'Regional forecast',description:'Explore regional demand and capacity forecasts.',status:'running',project:'analytics',project_slug:'analytics',can_manage:true,access:'private',replicas:2,replicas_running:2,deploy_count:3,current_version:'3',last_deployment_status:'succeeded',last_deployed_at:ago,framework:'shiny',runtime:'python',entrypoint:'app.py',memory_limit_mb:1024,cpu_quota_percent:100,autoscale_enabled:true,autoscale_min_replicas:1,autoscale_max_replicas:4,max_sessions_per_replica:20}, {slug:'revenue',name:'Revenue explorer',description:'Track performance across markets.',status:'sleeping',can_manage:true,access:'private',deploy_count:1,project:'analytics',project_slug:'analytics'}, {slug:'reports',name:'Weekly reports and financial reconciliation',status:'crashed',can_manage:true,access:'private',deploy_count:2,last_deployment_status:'failed'}];
const reps=[0,1].map(index=>({index,status:'running',desired_state:'running',metrics_available:true,cpu_percent:10+index*20,rss_bytes:(180+index*30)*mb,sessions:3+index,effective_memory_limit_mb:1024,effective_cpu_quota_percent:100,memory_limit_enforced:true,cpu_quota_enforced:true,resource_enforcement_known:true,pid:1000+index,run_id:'fixture-'+index}));
const metrics={replicas:reps,status:'running',replicas_running:2,replicas_desired:2,cpu_cores:.4,cpu_capacity_cores:2,cpu_capacity_source:'quota',cpu_host_cores:8,cpu_saturation_available:true,sessions_cap:20,sessions_ceiling:40,generated_at:now};
const users=[{id:1,username:'operator',display_name:'Platform operator',role:'admin',auth_provider:'local',can_manage_self:true},{id:2,username:'analyst',display_name:'Research and analytics',role:'developer',auth_provider:'local'},{id:3,username:'viewer',display_name:'Report viewer',role:'viewer',auth_provider:'oidc'}];
const projects=[{slug:'analytics',name:'Analytics',description:'Production dashboards for planning and reporting.',icon_emoji:'📊'}];
const tokens=[{id:1,name:'Workstation CLI',created_at:ago,expires_at:new Date(Date.now()+86400000*80).toISOString(),last_used_at:now},{id:2,name:'Scheduled reporting integration with a long descriptive name',created_at:ago,expires_at:ago}];
const deployments=[{id:3,version:'3',release_number:3,status:'succeeded',created_at:ago,finished_at:now,created_by:'operator',message:'Refresh demand forecast'},{id:2,version:'2',release_number:2,status:'failed',created_at:ago,failure_reason:'Dependency installation failed'},{id:1,version:'1',release_number:1,status:'succeeded',created_at:ago}];
const list=items=>({items:mode==='empty'?[]:items,total:items.length,limit:50,offset:0});
const types={'.js':'text/javascript','.css':'text/css','.svg':'image/svg+xml','.woff2':'font/woff2','.png':'image/png','.ico':'image/x-icon'};
const unknown=new Set();
const server=createServer(async(req,res)=>{
try{const path=new URL(req.url,'http://localhost').pathname;
if(path===delayedPath) await new Promise(r=>setTimeout(r,700));
let body,type='application/json',status=200;
if(path==='/announcements.js'){body=Buffer.from('');type='text/javascript';}
else if(path.startsWith('/static/')){body=await readFile(resolve(root+'/internal/ui','.'+path));type=types[extname(path)]||'application/octet-stream';}
else if(path==='/api/auth/me'){body={user:{...users[0],role},can_create_apps:role==='admin',can_manage_apps:role==='admin',can_read_audit:role==='admin',can_manage_announcements:role==='admin'};if(role==='guest'){status=401;body={error:'Sign in required'};}}
else if(path==='/api/auth/logout'){role='guest';body={};}
else if(path==='/api/auth/providers')body={providers:[]};
else if(path==='/api/server-info')body={version:'local review',runtime_mode:'native',can_create_apps:true};
else if(path==='/api/support-sessions/current')body={active:null};
else if(path==='/api/fleet/health')body={status:'healthy',workers:[],alerts:[]};
else if(path==='/api/apps')body=list(apps.map(a=>({...a,can_manage:role==='admin'})));
else if(path==='/api/projects')body=list(projects);
else if(path==='/api/users')body={...list(users.map(u=>({...u,created_at:ago,has_local_password:u.auth_provider==='local'}))),onboarding:{mode:'local',can_create_local_users:true}};
else if(path==='/api/service-accounts')body=list([{id:4,key:'report-runner',username:'report-runner',name:'Reporting automation',role:'developer',created_at:ago,enabled:true}]);
else if(path.startsWith('/api/') && path.endsWith('/credentials'))body=list([]);
else if(path==='/api/tokens')body=list(tokens);
else if(path==='/api/workers')body=mode==='empty'?[]:[{node_id:'worker-eu-production-01',name:'Primary worker',tier:'local',status:'up',version:'0.21.4',last_heartbeat:now},{node_id:'worker-eu-production-02',name:'Standby worker',tier:'remote',status:'down',version:'0.21.3',last_heartbeat:ago}];
else if(path==='/api/audit')body={events:mode==='empty'?[]:[{id:1,action:'app.deploy',username:'operator',actor:'operator',resource:'forecast',resource_type:'app',resource_id:'forecast',created_at:now,details:{version:'3',status:'succeeded'},ip:'192.0.2.1'},{id:2,action:'user.create',username:'operator',resource:'analyst',created_at:ago,details:{role:'developer'},ip:'192.0.2.1'}],total:2,has_more:false};
else if(path==='/api/announcements')body={announcements:[],has_more:false};
else if(path==='/api/apps/metrics')body={metrics:{forecast:metrics},host:{cores:8,memory_mb:16384},generated_at:now};
else if(path.startsWith('/api/') && path.endsWith('/metrics/history'))body={history:{},series:{ts:[],cpu:[],rss:[],sessions:[],instances:[]},window_seconds:900};
else if(path==='/api/apps/forecast')body={app:apps[0],can_manage:role==='admin',replicas_status:reps,runtime_mode:'native',effective_max_sessions_per_replica:20,effective_autoscale_target:.75,global_autoscale_enabled:true};
else if(path==='/api/apps/forecast/metrics')body=metrics;
else if(path.startsWith('/api/') && path.endsWith('/usage'))body={enabled:true,window_days:30,identity_mode:'pseudonymous',capabilities:{unique_viewers:true},generated_at:now,summary:{sessions:326,unique_viewers:24,peak_concurrent_sessions:8,active_sessions:4,average_duration_seconds:125,authenticated_sessions:300,anonymous_sessions:26,last_opened_at:now},daily:[{date:now.slice(0,10),sessions:26,unique_viewers:12,peak_concurrent_sessions:8}],viewers:[],recent_sessions:[]};
else if(path.startsWith('/api/') && path.endsWith('/deployments'))body=list(deployments);
else if(path.startsWith('/api/') && path.endsWith('/env'))body=list([{key:'REGION',value:'europe',secret:false},{key:'API_KEY',secret:true}]);
else if(path.startsWith('/api/') && path.endsWith('/data'))body={...list([{path:'regional-forecast.csv',size:820000,modified_at:Math.floor(Date.now()/1000)}]),quota_mb:1024,used_bytes:820000};
else if(path.startsWith('/api/') && path.endsWith('/members'))body=list([{user_id:2,username:'analyst',role:'developer'}]);
else if(path.startsWith('/api/') && path.endsWith('/group-access'))body=list([]);
else if(path.startsWith('/api/') && path.endsWith('/shared-data'))body={sources:[]};
else if(path.startsWith('/api/') && path.endsWith('/schedules'))body=list([]);
else if(path.startsWith('/api/') && path.endsWith('/traces'))body={traces:[],total:0};
else if(path.startsWith('/api/') && path.endsWith('/logs'))body={entries:[],lines:[],next_cursor:'',has_more:false};
else if(path.startsWith('/api/')){unknown.add(path);body={items:[],total:0,events:[],entries:[],invitations:[],announcements:[],has_more:false};}
else {body=await readFile(root+'/internal/ui/static/index.html');type='text/html';}
if(['error','denied'].includes(mode)&&['/api/tokens','/api/workers','/api/users','/api/audit','/api/projects'].includes(path)){status=mode==='denied'?403:503;body={error:mode==='denied'?'Access denied':'Temporarily unavailable'};}
res.writeHead(status,{'content-type':type,'cache-control':'no-store'});res.end(Buffer.isBuffer(body)?body:JSON.stringify(body));
}catch(e){res.writeHead(500);res.end(e.message);}});
await new Promise(r=>server.listen(0,'127.0.0.1',r));
const origin=`http://127.0.0.1:${server.address().port}`;
const browser=await chromium.launch({headless:true,executablePath:process.env.SHINYHUB_E2E_CHROMIUM || undefined});
const routes=[['overview','/'],['apps','/apps'],['project','/projects/analytics'],['identity','/identity'],['services','/identity','#identity-services-tab'],['workers','/workers'],['audit','/audit-log'],['tokens','/tokens'],['announcements','/announcements'],...['overview','usage','logs','traces','deployments','schedules','configuration','data','access'].map(t=>['app-'+t,'/apps/forecast'+(t==='overview'?'':'/'+t)]),['viewer','/apps'],['login','/login'],['profile','/','#identity-card'],['new-app','/apps','#new-app-button'],['new-token','/tokens','#new-token-button'],['deploy','/apps/forecast','#app-detail-deploy'],['schedule-editor','/apps/forecast/schedules','#schedules-add-btn'],['announcement-editor','/announcements','[data-new]'],['project-editor','/projects/analytics','#project-detail-edit'],['about','/','#about-button']];
const results=[];
try{
for(const theme of ['light','dark']) for(const width of (shellOnly ? [1440,900,390] : [1440,390])){
 const context=await browser.newContext({viewport:{width,height:1000},userAgent:'shinyhub-ui-audit'});await context.addInitScript(t=>localStorage.setItem('shinyhub-theme',t),theme);
 const page=await context.newPage();let errors=[];page.on('pageerror',e=>errors.push(e.message));
 role='admin';mode='ready';
 await checkShellConsistency(page,origin,{role,width,theme,extraRoutes:[...routes.filter(([name,,click])=>!click&&!['viewer','login'].includes(name)).map(([,path])=>path),...['general','environment','resources','scaling','access','privacy'].map(section=>`/apps/forecast/settings/${section}`)]});
 role='viewer';
 await checkShellConsistency(page,origin,{role,width,theme,extraRoutes:['/apps','/tokens']});
 assert.deepEqual(errors,[],`${theme}/${width}: shell navigation page errors`);
 if(shellOnly){await context.close();continue;}
 role='admin';
 await checkSelectPickers(page,origin,{width,theme});
 for(const [name,path,click]of routes.filter(r=>!process.env.SHINYHUB_E2E_PAGES_FILTER||process.env.SHINYHUB_E2E_PAGES_FILTER.split(',').includes(r[0]))){role=name==='viewer'?'viewer':name==='login'?'guest':'admin';mode='ready';errors=[];
 await page.goto(origin+path,{waitUntil:'domcontentloaded'});await page.locator(`body[data-auth="${role==='guest'?'out':'in'}"]`).waitFor();await page.waitForTimeout(350);await page.evaluate(()=>document.fonts.ready);
 if(click){if(['profile','about'].includes(name)&&width<760)await page.locator('#sidebar-toggle').click();await page.locator(click).click();}
 await page.addScriptTag({path:root+'/node_modules/axe-core/axe.min.js'});
 const axe=await page.evaluate(()=>window.axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}}));
 const geometry=await page.evaluate(()=>({overflow:document.documentElement.scrollWidth>innerWidth,wide:[...document.querySelectorAll('main *')].filter(e=>{const r=e.getBoundingClientRect();return r.width&&r.right>innerWidth+2&&getComputedStyle(e).position!=='absolute'}).slice(0,12).map(e=>e.id||e.className),title:document.title}));
 const key=`${name}-${theme}-${width}`;if(out) await page.screenshot({path:`${out}/${key}.png`,fullPage:false,animations:'disabled'});
 results.push({key,...geometry,errors:[...errors],violations:axe.violations.map(v=>({id:v.id,impact:v.impact,nodes:v.nodes.map(n=>({target:n.target,summary:n.failureSummary}))}))});
 console.log(key,JSON.stringify({overflow:geometry.overflow,errors,violations:axe.violations.map(v=>v.id)}));
 if(!process.env.SHINYHUB_E2E_AUDIT) { assert.deepEqual(errors,[],`${key}: page errors`);
 assert.equal(geometry.overflow,false,`${key}: horizontal overflow`);
 assert.deepEqual(axe.violations.map(v=>({id:v.id,nodes:v.nodes.map(n=>({target:n.target,summary:n.failureSummary}))})),[],`${key}: accessibility`); }
 }
 await context.close();
}
if (!shellOnly) {
// Failure/empty states on list pages, captured separately for truthful state review.
const context=await browser.newContext({viewport:{width:390,height:844},userAgent:'shinyhub-ui-audit'});const page=await context.newPage();role='admin';
for(const state of ['empty','error'])for(const name of ['workers','tokens','identity','audit-log','projects/analytics']){mode=state;await page.goto(origin+'/'+name);await page.waitForTimeout(250);const key=`${name.replace('/','-')}-${state}`;if(out) await page.screenshot({path:`${out}/${key}.png`,animations:'disabled'});results.push({key,text:await page.locator('main').innerText()});}

// Failed initial loads are not empty inventories; refresh errors retain data.
for(const name of ['tokens','workers']) {
 mode='error';await page.goto(origin+'/'+name);
 await page.locator('#'+name+'-error').waitFor();
 assert.doesNotMatch(await page.locator('#'+name+'-view').innerText(),/No API tokens yet|No workers have joined/);
 mode='ready';await page.locator('#'+name+'-refresh').click();
 const rows=name==='tokens'?'[data-token-row]':'#workers-body tr';await page.locator(rows).first().waitFor();
 const count=await page.locator(rows).count();mode='error';await page.locator('#'+name+'-refresh').click();
 await page.locator('#'+name+'-error').waitFor();assert.equal(await page.locator(rows).count(),count);
 assert.match(await page.locator('#'+name+'-error').innerText(),/last loaded list/);
 assert.equal(await page.locator('#'+name+'-refresh').isEnabled(),true);
 mode='ready';await page.locator('#'+name+'-refresh').click();await page.locator('#'+name+'-error').waitFor({state:'hidden'});
 mode='denied';await page.locator('#'+name+'-refresh').click();await page.locator('#'+name+'-error').waitFor();
 assert.equal(await page.locator(rows).count(),0,'permission loss clears the previous inventory');
 mode='ready';
}
// A pending personal inventory must not repopulate after sign-out.
await page.goto(origin+'/tokens');await page.locator('[data-token-row]').first().waitFor();
delayedPath='/api/tokens';await page.locator('#tokens-refresh').click();await page.locator('#tokens-loading').waitFor();
await page.locator('#sidebar-toggle').click();await page.locator('#identity-card').click();await page.locator('#logout-button').click();
await page.locator('body[data-auth="out"]').waitFor();await page.waitForTimeout(900);
assert.equal(await page.locator('[data-token-row]').count(),0);assert.equal(await page.locator('#tokens-view').isHidden(),true);
await context.close();
if(out) await writeFile(out+'/results.json',JSON.stringify({results,unknown:[...unknown]},null,2));
console.log('Page accessibility, responsive layouts, inventory recovery, and sign-out checks passed');
}
console.log('Cross-route shell consistency checks passed');

}finally{await browser.close();server.closeAllConnections();await new Promise(r=>server.close(r));}
