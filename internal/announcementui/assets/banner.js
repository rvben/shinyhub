(() => {
  'use strict';
  const loader = document.currentScript;
  if (!loader || !window.fetch || !Element.prototype.attachShadow) return;
  const consoleTarget = loader.getAttribute('data-announcements-target');
  if (!consoleTarget) {
    try { if (window.self !== window.top) return; } catch { return; }
  }
  const url = loader.getAttribute('data-announcements-url');
  if (!url) return;
  const css = `
    :host { display:block; color-scheme:dark; --notice-bg:#0e1426; --notice-fg:#e8eeff; --notice-soft:#a8b4d4; --notice-line:#2b3a63; --notice-info:#7dd3fc; --notice-warning:#fbbf24; --notice-critical:#f87171; }
    :host([data-theme="light"]) { color-scheme:light; --notice-bg:#fff; --notice-fg:#16203a; --notice-soft:#45526e; --notice-line:#c4cfe2; --notice-info:#075985; --notice-warning:#854d0e; --notice-critical:#b91c1c; }
    :host([hidden]) { display:none!important; }
    * { box-sizing:border-box; }
    .notice { background:var(--notice-bg); color:var(--notice-fg); border:1px solid var(--notice-line); border-radius:8px; padding:12px 16px; max-height:55vh; overflow:auto; font:500 14px/1.5 var(--font,ui-sans-serif,system-ui,sans-serif); }
    .primary { display:flex; gap:12px; align-items:flex-start; }
    .copy { flex:1; min-width:0; }
    .severity { display:block; font-size:12px; font-weight:700; color:var(--notice-info); margin-bottom:2px; }
    [data-severity="warning"] .severity { color:var(--notice-warning); }
    [data-severity="critical"] .severity { color:var(--notice-critical); }
    h2 { font:750 14px/1.5 var(--font,ui-sans-serif,system-ui,sans-serif); margin:0; overflow-wrap:anywhere; }
    p { margin:3px 0 0; white-space:pre-wrap; overflow-wrap:anywhere; max-width:75ch; }
    a { color:var(--notice-info); text-decoration:underline; text-underline-offset:3px; display:inline-block; margin-top:5px; }
    button { appearance:none; font:650 12px/1.4 var(--font,ui-sans-serif,system-ui,sans-serif); color:var(--notice-fg); background:transparent; border:1px solid var(--notice-line); border-radius:8px; padding:7px 10px; cursor:pointer; }
    button:hover { background:var(--notice-line); }
    button:focus-visible,a:focus-visible { outline:2px solid var(--notice-info); outline-offset:3px; }
    .more { margin-top:10px; }
    .others { margin-top:12px; border-top:1px solid var(--notice-line); padding-top:12px; display:grid; gap:12px; }
    .sr { position:absolute; width:1px; height:1px; overflow:hidden; clip-path:inset(50%); white-space:nowrap; }
    ::selection { background:var(--notice-info); color:var(--notice-bg); }
    [hidden] { display:none!important; }
    @media(max-width:620px) { .notice { padding:10px 12px; } .primary { flex-wrap:wrap; } .copy { flex-basis:calc(100% - 90px); } button { min-height:44px; } }
  `;
  function createSurface(host) {
    const root = host.attachShadow({mode:'closed'});
    if (typeof CSSStyleSheet === 'function' && 'adoptedStyleSheets' in root) {
      try { const sheet = new CSSStyleSheet(); sheet.replaceSync(css); root.adoptedStyleSheets = [sheet]; }
      catch { const style = document.createElement('style'); style.textContent = css; root.append(style); }
    } else { const style = document.createElement('style'); style.textContent = css; root.append(style); }
    const content = document.createElement('div'); root.append(content);
    const live = document.createElement('div'); live.className = 'sr'; live.setAttribute('role','status'); live.setAttribute('aria-live','polite'); live.setAttribute('aria-atomic','true'); root.append(live);
    return {content, live};
  }
  function article(a, onDismiss) {
    const el = document.createElement('article'); el.className = 'primary'; el.dataset.severity = a.severity;
    const copy = document.createElement('div'); copy.className = 'copy';
    const severity = document.createElement('span'); severity.className = 'severity'; severity.textContent = ({information:'Information',warning:'Warning',critical:'Critical'})[a.severity] || 'Information';
    const title = document.createElement('h2'); title.textContent = a.title;
    const message = document.createElement('p'); message.textContent = a.message;
    copy.append(severity,title,message);
    if (a.details_url) {
      try {
        const parsed = new URL(a.details_url);
        if (['https:','http:'].includes(parsed.protocol) && !parsed.username && !parsed.password) {
          const link = document.createElement('a'); link.href = parsed.href; link.textContent = 'Details'; link.target = '_blank'; link.rel = 'noopener noreferrer'; link.setAttribute('aria-label',`Details about ${a.title}`); copy.append(link);
        }
      } catch { /* invalid links never become navigation */ }
    }
    el.append(copy);
    if (a.dismissible && onDismiss) {
      const button = document.createElement('button'); button.type = 'button'; button.textContent = 'Dismiss'; button.setAttribute('aria-label',`Dismiss ${a.title}`); button.dataset.dismiss = a.id;
      button.addEventListener('click', () => onDismiss(a)); el.append(button);
    }
    return el;
  }
  // The editor uses this exact renderer, including theme and text handling.
  window.ShinyHubAnnouncements = {
    preview(target,a) {
      let host = target.firstElementChild;
      if (!host || !host.__announcementSurface) {
        host = document.createElement('div'); target.replaceChildren(host); host.__announcementSurface = createSurface(host);
      }
      host.dataset.theme = document.documentElement.dataset.theme || (window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light':'dark');
      const notice = document.createElement('div'); notice.className = 'notice'; notice.append(article(a,null)); host.__announcementSurface.content.replaceChildren(notice);
    }
  };
  const host = document.createElement('div'); host.id = 'shinyhub-platform-announcements'; host.hidden = true;
  host.setAttribute('data-shinyhub-platform-ui','announcements');
  const {content,live} = createSurface(host);
  const remembered = new Set();
  let notices = [], signature = '', expanded = false, serverNow = Date.now(), receivedAt = performance.now(), failures = 0;
  let timer, expiryTimer, transitionTimer, request = null, stopped = false, refreshQueued = false;
  let lastAnnounced = '';
  function key(a) { return `shinyhub-announcement:${a.id}:${a.display_revision}`; }
  function dismissed(a) {
    if (!a.dismissible) return false;
    if (remembered.has(key(a))) return true;
    try { return localStorage.getItem(key(a)) === '1'; } catch { return false; }
  }
  function dismiss(a) {
    remembered.add(key(a)); try {localStorage.setItem(key(a),'1');} catch { /* memory still remembers */ }
    signature = ''; render();
    const focus = content.querySelector('button,a');
    if (focus) focus.focus({preventScroll:true});
    else if (consoleTarget) document.querySelector('main h1,main input,main button')?.focus({preventScroll:true});
    else { const previous = document.body.getAttribute('tabindex'); document.body.setAttribute('tabindex','-1'); document.body.focus({preventScroll:true}); if(previous===null) document.body.removeAttribute('tabindex'); }
  }
  function now() { return serverNow + performance.now() - receivedAt; }
  function eligible() { return notices.filter(a => (!a.ends_at || Date.parse(a.ends_at)>now()) && !dismissed(a)); }
  function theme() {
    host.dataset.theme = document.documentElement.dataset.theme || (window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light':'dark');
  }
  function place() {
    theme();
    if (consoleTarget) { const target = document.querySelector(consoleTarget); if(target) {target.hidden=host.hidden;if(host.parentNode!==target) target.append(host);} return; }
    const html = document.documentElement;
    const support = document.getElementById('shinyhub-support-session');
    if (support?.parentNode===html) {
      if (support.nextElementSibling!==host) support.after(host);
    } else if(host.parentNode!==html || host.nextSibling!==document.body) html.insertBefore(host,document.body || null);
    const supportHeight = support?.getBoundingClientRect().height || 0;
    host.style.cssText = `display:${host.hidden?'none':'block'}!important;position:sticky!important;top:${supportHeight}px!important;z-index:2147483646!important;width:100%!important;isolation:isolate!important;`;
    html.style.setProperty('--shinyhub-announcement-height',`${host.hidden ? 0 : host.getBoundingClientRect().height}px`);
  }
  function render() {
    const active = eligible(); const next = JSON.stringify(active);
    if(next===signature) {place();return;}
    const focused = content.querySelector(':focus'); const focusID = focused?.dataset.dismiss; const focusedMore = focused?.classList.contains('more');
    signature = next; host.hidden = !active.length;
    content.replaceChildren();
    if (active.length) {
      const box = document.createElement('section'); box.className = 'notice'; box.setAttribute('aria-label','Platform announcements');box.append(article(active[0],dismiss));
      if(active.length>1) {
        const more = document.createElement('button'); more.type = 'button'; more.className = 'more'; more.textContent = expanded ? 'Show primary announcement' : `View all announcements (${active.length})`; more.setAttribute('aria-expanded',String(expanded));more.setAttribute('aria-controls','announcement-others');
        const others = document.createElement('div');others.id='announcement-others';others.className='others';others.hidden=!expanded;
        for (const a of active.slice(1)) others.append(article(a,dismiss));
        more.addEventListener('click',()=>{expanded=!expanded;others.hidden=!expanded;more.setAttribute('aria-expanded',String(expanded));more.textContent=expanded?'Show primary announcement':`View all announcements (${active.length})`;place();});
        box.append(more,others);
      }
      content.append(box);
      const announce = active.map(a=>`${a.id}:${a.display_revision}`).join(',');
      if(announce!==lastAnnounced) { live.textContent = `${active[0].title}. ${active[0].message}${active.length>1 ? ` ${active.length} platform announcements are available.`:''}`;lastAnnounced=announce; }
      if(focusID) Array.from(content.querySelectorAll('[data-dismiss]')).find(el=>el.dataset.dismiss===focusID)?.focus({preventScroll:true});
      else if(focusedMore) content.querySelector('.more')?.focus({preventScroll:true});
    } else {live.textContent='';lastAnnounced='';}
    place();
  }
  function expiry() {
    clearTimeout(expiryTimer);const times=notices.filter(a=>a.ends_at).map(a=>Date.parse(a.ends_at)-now()).filter(ms=>ms>0);
    if(times.length) expiryTimer=setTimeout(()=>{signature='';render();expiry();},Math.min(Math.min(...times)+10,2147483647));
  }
  function schedule(delay) {clearTimeout(timer);if(!stopped && !document.hidden) timer=setTimeout(refresh,delay);}
  async function refresh() {
    if(stopped || document.hidden) return;
    if(request) {refreshQueued=true;return;}
    request = new AbortController();const timeout=setTimeout(()=>request?.abort(),10000);
    try {
      const response = await fetch(url,{credentials:'omit',cache:'no-store',signal:request.signal});
      if(!response.ok) throw new Error('announcement feed unavailable');
      const payload=await response.json();
      if(!Array.isArray(payload.announcements) || !Number.isFinite(Date.parse(payload.server_time))) throw new Error('invalid announcement feed');
      notices=payload.announcements;serverNow=Date.parse(payload.server_time);receivedAt=performance.now();failures=0;render();expiry();
      clearTimeout(transitionTimer);
      if(payload.next_transition){const wait=Date.parse(payload.next_transition)-now();if(wait>0)transitionTimer=setTimeout(refresh,Math.min(wait+20,2147483647));}
    } catch {failures++; render();expiry();}
    finally {clearTimeout(timeout);request=null;if(refreshQueued){refreshQueued=false;refresh();}else schedule(Math.min(120000,30000*Math.pow(2,Math.min(failures,2)))+Math.random()*2000);}
  }
  const observer = new MutationObserver(place);
  observer.observe(document.documentElement,{childList:true,attributes:true,attributeFilter:['data-theme']});
  const sizeObserver = typeof ResizeObserver==='function' ? new ResizeObserver(place):null;
  sizeObserver?.observe(host);
  function onVisible(){clearTimeout(timer);if(!document.hidden){signature='';render();refresh();}}
  document.addEventListener('visibilitychange',onVisible);
  window.addEventListener('storage',(event)=>{if(event.key?.startsWith('shinyhub-announcement:')){signature='';render();}});
  window.addEventListener('shinyhub:announcements-changed',()=>{clearTimeout(timer);refresh();});
  window.matchMedia?.('(prefers-color-scheme: light)').addEventListener?.('change',theme);
  window.addEventListener('pagehide',(event)=>{clearTimeout(timer);clearTimeout(transitionTimer);clearTimeout(expiryTimer);request?.abort();if(!event.persisted){stopped=true;observer.disconnect();sizeObserver?.disconnect();}});
  window.addEventListener('pageshow',(event)=>{if(event.persisted){render();expiry();refresh();}});
  try { place();refresh(); } catch { /* optional chrome cannot break the app */ }
})();
