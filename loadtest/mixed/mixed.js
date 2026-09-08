import http from 'k6/http';
import ws from 'k6/ws';
import { sleep } from 'k6';
import execution from 'k6/execution';
import { Rate, Trend, Counter } from 'k6/metrics';

const host = __ENV.LT_HOST;
const auth = JSON.parse(open(__ENV.LT_AUTH_FILE));
const n = Number(__ENV.LT_CLIENTS || 1);
const duration = __ENV.LT_DURATION || '30s';
const hold = Number(__ENV.LT_WS_HOLD || 20);
const rate = Number(__ENV.LT_PAGE_RATE || n * 2);
const reportInterval = Number(__ENV.LT_REPORT_INTERVAL || 5);
const failures = new Rate('mixed_failure');
const pages = new Trend('page_ms', true);
const assets = new Trend('asset_ms', true);
const reports = new Trend('report_ms', true);
const connections = new Trend('session_establish_ms', true);
const roundtrips = new Trend('session_rtt_ms', true);
const sessionFailure = new Rate('session_failure');
const establishedCount = new Counter('session_established');
const wake = new Trend('wake_ms', true);
const wakeFailure = new Rate('wake_failure');

export const options = {
 userAgent: 'shinyhub-loadtest',
 summaryTrendStats: ['min','med','p(95)','p(99)','max','count'],
 scenarios: {
  pages: {executor:'constant-arrival-rate', exec:'page', rate, timeUnit:'1s', duration, preAllocatedVUs:Math.max(2,n), maxVUs:Math.max(4,n*3), gracefulStop:'5s'},
  reports: {executor:'constant-arrival-rate', exec:'report', rate:1, timeUnit:`${reportInterval}s`, duration, preAllocatedVUs:2, maxVUs:8, gracefulStop:'5s'},
  sessions: {executor:'constant-vus', exec:'session', vus:n, duration, gracefulStop:`${hold+5}s`},
  wakes: {executor:'constant-vus', exec:'wakeApp', vus:1, duration, gracefulStop:'15s'},
 },
 thresholds: {
  mixed_failure:['rate<0.01'], session_failure:['rate<0.01'], wake_failure:['rate==0'],
  page_ms:['p(95)<250','p(99)<500'], asset_ms:['p(99)<500'], report_ms:['p(95)<2000'],
  session_establish_ms:['p(95)<1000'], session_rtt_ms:['p(99)<250'], wake_ms:['p(95)<3000'],
  dropped_iterations:['count==0'],
  ...(parseInt(duration, 10) >= 300 ? Object.fromEntries(['early','middle','late'].flatMap(phase => [
   [`page_ms{phase:${phase}}`, ['p(95)<250']], [`report_ms{phase:${phase}}`, ['p(95)<2000']],
  ])) : {}),
 },
};
function params(who,kind,html=false) {
 return {redirects:0, timeout:'5s', headers:{...(who==='admin'?{Authorization:`Bearer ${auth.admin}`}:{ }), Cookie:`shiny_session=${auth[who]}`, Accept:html?'text/html':'*/*'}, tags:{kind}};
}
function phase() {
 const elapsed = (Date.now() - execution.scenario.startTime) / 1000;
 return {phase: ['early','middle','late'][Math.min(2,Math.floor(elapsed / (parseInt(duration,10) / 3)))]};
}
function observed(res,marker,metric) {
 const ok=res.status===200 && typeof res.body==='string' && res.body.includes(marker);
 metric.add(res.timings.duration,phase()); failures.add(!ok); return ok;
}
export function page() {
 observed(http.get(`${host}/app/mixed/`,params('viewer','page',true)), 'id="mixed-fixture"', pages);
 observed(http.get(`${host}/app/mixed/app.js`,params('viewer','asset')), 'mixed-js', assets);
 observed(http.get(`${host}/app/mixed/style.css`,params('viewer','asset')), 'mixed-css', assets);
}
export function report() {
 const res=http.get(`${host}/api/apps/mixed/usage?days=7`,params('admin','report'));
 let ok=false;
 try { ok=res.status===200 && res.json().summary.sessions >= Number(__ENV.LT_SEED_SESSIONS); } catch (_) {}
 reports.add(res.timings.duration,phase()); failures.add(!ok);
}
export function session() {
 const root=`${host}/app/mixed/`;
 const res=http.get(root,params('viewer','session_page',true));
 if(res.status!==200 || !res.body.includes('id="mixed-fixture"')) {sessionFailure.add(true);sleep(1);return;}
 const jar=http.cookieJar().cookiesForURL(root);
 const cookies=Object.entries(jar).flatMap(([k,vals])=>vals.map(v=>`${k}=${v}`));
 cookies.push(`shiny_session=${auth.viewer}`);
 const start=Date.now();let ready=false,held=false,errored=false,echoes=0;
 const upgrade=ws.connect(root.replace(/^http/,'ws')+'websocket/',{headers:{Cookie:cookies.join('; ')},tags:{kind:'session'}},socket=>{
  socket.on('open',()=>socket.setTimeout(()=>{if(!ready){errored=true;socket.close();}},3000));
  socket.on('message',message=>{
   if(!ready){
    if(message!=='ready'){errored=true;socket.close();return;}
    ready=true;establishedCount.add(1);connections.add(Date.now()-start);
    socket.setInterval(()=>socket.send(String(Date.now())),1000);
    socket.setTimeout(()=>{held=true;socket.close();},hold*1000);
   } else {
    const sent=Number(message);if(Number.isFinite(sent)){echoes++;roundtrips.add(Date.now()-sent);}else{errored=true;}
   }
  });
  socket.on('error',()=>{errored=true;});
  socket.setTimeout(()=>socket.close(),(hold+5)*1000);
 });
 sessionFailure.add(!upgrade || upgrade.status!==101 || !ready || !held || errored || echoes<Math.max(1,Math.floor(hold)-2));
 if(!ready)sleep(1);
}
export function wakeApp() {
 const slept=http.post(`${host}/api/apps/wake/sleep`,null,params('admin','sleep'));
 if(slept.status!==200){wakeFailure.add(true);sleep(10);return;}
 const start=Date.now();let ok=false;
 while(Date.now()-start<10000){
  const res=http.get(`${host}/app/wake/`,params('viewer','wake',true));
  if(res.status===200 && res.body.includes('id="mixed-fixture"')){ok=true;break;}
  sleep(0.1);
 }
 wake.add(Date.now()-start);wakeFailure.add(!ok);sleep(10);
}
export function handleSummary(data) {
 return {[__ENV.LT_SUMMARY]:JSON.stringify(data,null,2),stdout:'mixed load stage completed\n'};
}
