import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {JSDOM} from 'jsdom';
const script = readFileSync(new URL('../../envui/assets/environment.js',import.meta.url),'utf8');
const links = JSON.parse(readFileSync(new URL('../../../testdata/environment-links.json',import.meta.url)));
const routes = ['/login','/home','/launchpad','/apps','/identity','/users','/workers','/announcements','/audit-log','/tokens'];
function mount(path='/app/finops/', options={}, configure=()=>{}) {
 const dom=new JSDOM('<!doctype html><html><head><title>FinOps</title><link rel="icon" href="/original.ico"><link rel="apple-touch-icon" href="/touch.png"></head><body><main>Dashboard</main></body></html>',{url:'https://acceptance.example'+path,runScripts:'outside-only'});
 const {document}=dom.window;
 const sized=[];dom.sized=sized;
 dom.window.ResizeObserver=class {constructor(fn){this.fn=fn;}observe(node){sized.push(node);}unobserve(){} };
 const observers=[];const NativeObserver=dom.window.MutationObserver;
 dom.window.MutationObserver=class extends NativeObserver { constructor(fn){super(fn);observers.push(this);} };
 dom.cleanup=()=>{for(const observer of observers)observer.disconnect();dom.window.close();};
 const loader=document.createElement('script');
 Object.assign(loader.dataset,{label:'Acceptance',color:'#f5b301',textColor:'#000000',message:'Test data',productionUrl:'https://production.example',productionHref:'https://production.example/',icon:'/app/.shinyhub/favicon.ico?environment=test',uiRoutes:JSON.stringify(routes)},options);
 document.body.append(loader);
 Object.defineProperty(document,'currentScript',{get:()=>loader});
 configure(dom.window);
 dom.window.eval(script);
 return dom;
}
for(const tc of links) test(`environment link ${tc.current}`,()=>{
 const dom=mount(tc.current);try {
 const link=dom.window.document.getElementById('shinyhub-environment').shadowRoot.querySelector('a');
 assert.equal(link.href,'https://production.example'+tc.target);
 link.dispatchEvent(new dom.window.Event('pointerdown'));
 assert.equal(link.href,'https://production.example'+tc.target);
 }finally{dom.cleanup();}
});
test('environment survives dynamic titles, icons and DOM replacement without duplicate prefixes',async()=>{
 const dom=mount();try {
 const d=dom.window.document;const tick=()=>new Promise(resolve=>dom.window.setTimeout(resolve,40));
 assert.equal(d.title,'[Acceptance] FinOps');
 d.title='Updated report';
 const icon=d.createElement('link');icon.rel='icon';icon.href='/app-owned.ico';d.head.append(icon);
 const host=d.getElementById('shinyhub-environment');host.remove();
 await tick();
 assert.equal(d.title,'[Acceptance] Updated report');
 assert.equal(d.querySelectorAll('link[rel="icon"]').length,1);
 assert.match(d.querySelector('link[rel="icon"]').href,/environment=test/);
 assert.ok(d.querySelector('link[rel="apple-touch-icon"]'));
 assert.equal(host.parentNode,d.documentElement);
 d.title='[Acceptance] Updated report';await tick();assert.equal(d.title,'[Acceptance] Updated report');
 dom.window.history.replaceState({},'', '/app/finops/?report=new&__shinyhub_launch=secret#chart');
 const link=host.shadowRoot.querySelector('a');link.dispatchEvent(new dom.window.Event('focusin'));
 assert.equal(link.href,'https://production.example/app/finops/?report=new#chart');
 }finally{dom.cleanup();}
});
test('environment respects icon denial and omitted optional fields',()=>{
 const dom=mount('/login',{icon:'',message:'',productionUrl:''});try{
 const d=dom.window.document;assert.equal(d.querySelector('link[rel="icon"]').getAttribute('href'),'/original.ico');
 const root=d.getElementById('shinyhub-environment').shadowRoot;
 assert.equal(root.querySelector('a'),null);assert.equal(root.textContent,'Acceptance');
 }finally{dom.cleanup();}
});
test('environment orders itself below support and above app announcements',async()=>{
 const dom=mount();try{
 const d=dom.window.document;const env=d.getElementById('shinyhub-environment');
 const support=d.createElement('div');support.id='shinyhub-support-session';support.getBoundingClientRect=()=>({height:52});
 const announcement=d.createElement('div');announcement.id='shinyhub-platform-announcements';
 d.documentElement.insertBefore(support,d.body);d.documentElement.insertBefore(announcement,d.body);
 await new Promise(resolve=>dom.window.setTimeout(resolve,40));
 assert.equal(support.nextElementSibling,env);assert.equal(env.style.top,'52px');
 assert.ok(dom.sized.includes(support),'late-mounted support rail must be observed for height changes');
 }finally{dom.cleanup();}
});

test('gives empty dynamic titles a stable fallback without laying out ordinary app updates',async()=>{
 const dom=mount('/app/finops/',{label:'Acceptance West'});try{
 const d=dom.window.document;
 assert.equal(d.title,'[Acceptance West] FinOps');
 for(const title of ['', '  ', '[Acceptance West]']) {
  d.title=title;
  await new Promise(resolve=>dom.window.setTimeout(resolve,40));
  assert.equal(d.title,'[Acceptance West] ShinyHub');
 }
 const host=d.getElementById('shinyhub-environment');let layouts=0;
 host.getBoundingClientRect=()=>{layouts++;return {height:32};};
 const main=d.querySelector('main');
 main.append(d.createElement('div'));main.firstChild.textContent='Updated application data';
 await new Promise(resolve=>dom.window.setTimeout(resolve,40));
 assert.equal(layouts,0,'ordinary app updates must not trigger environment layout');
 }finally{dom.cleanup();}
});

test('retains server-normalized Unicode labels and corrects titles without animation frames',async()=>{
 const label='QA\uFEFF';
 const dom=mount('/app/finops/',{label}, window=>{
  // Background tabs do not deliver animation frames.
  window.requestAnimationFrame=()=>{};
 });try{
 const d=dom.window.document;
 d.title=`[${label}] FinOps`;
 await new Promise(resolve=>dom.window.setTimeout(resolve,40));
 assert.equal(d.title,`[${label}] FinOps`);
 d.title='Background result';
 await new Promise(resolve=>dom.window.setTimeout(resolve,40));
 assert.equal(d.title,`[${label}] Background result`);
 }finally{dom.cleanup();}
});
