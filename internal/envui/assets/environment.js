(() => {
  'use strict';
  const loader = document.currentScript;
  if (!loader || document.getElementById('shinyhub-environment')) return;
  try { if (window.self !== window.top) return; } catch { return; }
  // Config owns normalization; preserve its exact Unicode label for title parity.
  const label = loader.dataset.label;
  if (!label) return;
  const prefix = `[${label}] `;
  const color = loader.dataset.color;
  const foreground = loader.dataset.textColor;
  const iconURL = loader.dataset.icon;
  const production = loader.dataset.productionUrl;
  const routes = JSON.parse(loader.dataset.uiRoutes || '[]');
  const slugPattern = '[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?';
  const appRoute = new RegExp(`^/app/(${slugPattern})(?:/(.*))?$`);
  const hubDetail = new RegExp(`^/apps/${slugPattern}(?:/[a-z-]+)?/?$`);
  const projectDetail = new RegExp(`^/projects/${slugPattern}/?$`);
  function productionHref() {
    if (!production) return '';
    const current = new URL(window.location.href);
    let path;
    try { path = decodeURIComponent(current.pathname); } catch { return production + '/'; }
    const app = appRoute.exec(path);
    if (app) {
      // Capture groups inside the slug pattern mean the remaining path is group 3.
      if (app[3] === '.shinyhub' || app[3]?.startsWith('.shinyhub/')) return production + '/app/' + app[1] + '/';
      const pairs = current.search.slice(1).split('&').filter(pair => {
        try { return !decodeURIComponent(pair.split('=')[0].replace(/\+/g, ' ')).startsWith('__shinyhub_'); }
        catch { return true; }
      });
      const query = pairs.join('&');
      return production + current.pathname + (query ? '?' + query : '') + current.hash;
    }
    if (path !== '/login' && (routes.includes(path) || hubDetail.test(path) || projectDetail.test(path))) return production + current.pathname;
    return production + '/';
  }
  const host = document.createElement('div');
  host.id = 'shinyhub-environment';
  host.dataset.shinyhubPlatformUi = 'environment';
  host.setAttribute('role', 'region');
  host.setAttribute('aria-label', `Environment: ${label}`);
  const shadow = host.attachShadow({mode:'open'});
  const strip = document.createElement('div');
  // CSSOM assignments avoid inline stylesheet admission in restrictive app CSPs.
  strip.style.cssText = `display:flex;align-items:center;flex-wrap:wrap;gap:8px 16px;box-sizing:border-box;min-height:44px;padding:6px 16px;background:${color};color:${foreground};font:500 14px/1.5 ui-sans-serif,system-ui,sans-serif;`;
  const pill = document.createElement('strong');
  pill.textContent = label;
  pill.style.cssText = 'border:1px solid currentColor;border-radius:999px;padding:1px 10px;font-size:12px;overflow-wrap:anywhere;';
  strip.append(pill);
  if (loader.dataset.message) {
    const message = document.createElement('span');
    message.textContent = loader.dataset.message;
    message.style.cssText = 'flex:1 1 240px;overflow-wrap:anywhere;';
    strip.append(message);
  }
  let link;
  if (production) {
    link = document.createElement('a');
    link.textContent = 'Open in production \u2192';
    link.href = productionHref();
    link.referrerPolicy = 'no-referrer';
    link.style.cssText = 'display:inline-flex;align-items:center;min-height:32px;color:inherit;text-decoration:underline;text-underline-offset:3px;font-weight:650;margin-left:auto;';
    const refresh = () => { link.href = productionHref(); };
    for (const event of ['pointerdown','focusin','contextmenu','click','auxclick']) link.addEventListener(event, refresh, true);
    strip.append(link);
  }
  shadow.append(strip);
  function identity() {
    const title = document.title;
    if (title === prefix.trimEnd()) document.title = prefix + 'ShinyHub';
    else if (!title.startsWith(prefix)) document.title = prefix + (title || 'ShinyHub');
    if (!iconURL || !document.head) return;
    let owned = document.head.querySelector('link[data-shinyhub-environment-icon]');
    for (const icon of document.querySelectorAll('link[rel]')) {
      if (icon !== owned && icon.rel.toLowerCase().split(/\s+/).includes('icon')) icon.remove();
    }
    if (!owned) {
      owned = document.createElement('link');
      owned.dataset.shinyhubEnvironmentIcon = '';
      owned.rel = 'icon'; owned.type = 'image/png';
      document.head.append(owned);
    }
    if (owned.rel !== 'icon') owned.rel = 'icon';
    if (owned.getAttribute('href') !== iconURL) owned.setAttribute('href', iconURL);
  }
  let observedSupport = null;
  let previousTop = null;
  let previousRoot = null;
  const sizeObserver = window.ResizeObserver ? new ResizeObserver(place) : null;
  sizeObserver?.observe(host);
  function place() {
    const html = document.documentElement;
    const support = document.getElementById('shinyhub-support-session');
    if (support !== observedSupport && sizeObserver) {
      if (observedSupport) sizeObserver.unobserve(observedSupport);
      if (support) sizeObserver.observe(support);
      observedSupport = support;
    }
    if (support?.parentNode === html) {
      if (support.nextElementSibling !== host) support.after(host);
    } else {
      const announcement = document.getElementById('shinyhub-platform-announcements');
      const before = announcement?.parentNode === html ? announcement : document.body;
      if (host.parentNode !== html || host.nextElementSibling !== before) html.insertBefore(host, before || null);
    }
    const top = support?.getBoundingClientRect().height || 0;
    if (previousTop !== top || previousRoot !== html) host.style.cssText = `display:block!important;position:sticky!important;top:${top}px!important;z-index:2147483646!important;width:100%!important;isolation:isolate!important;`;
    previousTop = top; previousRoot = html;
    const height = `${host.getBoundingClientRect().height}px`;
    if (html.style.getPropertyValue('--shinyhub-environment-height') !== height) {
      html.style.setProperty('--shinyhub-environment-height', height);
      window.dispatchEvent(new Event('shinyhub:environment-resize'));
    }
  }
  let queued = false;
  const updateIdentity = () => {
    if (queued) return;
    queued = true;
    // Timers continue in background tabs; identity itself never reads layout.
    window.setTimeout(() => { queued = false; identity(); }, 0);
  };
  let observedRoot = null;
  let observedHead = null;
  const headObserver = new MutationObserver(updateIdentity);
  const rootObserver = new MutationObserver(() => { watch(); place(); updateIdentity(); });
  function watch() {
    if (observedRoot !== document.documentElement) {
      rootObserver.disconnect(); observedRoot = document.documentElement;
      if (observedRoot) rootObserver.observe(observedRoot, {childList:true});
    }
    if (observedHead !== document.head) {
      headObserver.disconnect(); observedHead = document.head;
      if (observedHead) headObserver.observe(observedHead, {childList:true,subtree:true,characterData:true,attributes:true,attributeFilter:['href','rel']});
    }
  }
  identity(); place(); watch();
  new MutationObserver(() => { watch(); place(); updateIdentity(); }).observe(document, {childList:true});
  window.addEventListener('resize', place);
})();
