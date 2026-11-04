import { settingsRouteParams } from './app-detail-nav.js';
import { avatarView, renderAppAvatar } from './app-avatar.js';

const SECTIONS = [
  ['overview', 'Overview'],
  ['deployments', 'Deployments'],
  ['logs', 'Logs', true],
  ['traces', 'Traces'],
  ['usage', 'Usage', true],
  ['schedules', 'Schedules'],
  ['data', 'Data', true],
];
const SETTINGS = [
  ['general', 'General'],
  ['environment', 'Environment'],
  ['resources', 'Resources'],
  ['scaling', 'Scaling'],
  ['access', 'Access'],
  ['privacy', 'Privacy'],
];

function setText(node, value) {
  if (node.textContent !== value) node.textContent = value;
}

function markCurrent(link, current, value = 'page') {
  link.classList.toggle('tab-active', current);
  if (current) link.setAttribute('aria-current', value);
  else link.removeAttribute('aria-current');
}

// The shell owns the host and its responsive styling. All destinations remain
// real links; only unmodified left clicks use the router and its draft guard.
export function installAppContextNavigation({ document, navigate }) {
  const host = document.getElementById('app-context-nav');
  const sidebar = document.getElementById('sidebar');
  const primary = document.getElementById('primary-nav');
  if (!host || !sidebar) return { update() {}, clear() {} };
  const make = (tag, className, text) => {
    const node = document.createElement(tag);
    node.className = className;
    if (text != null) node.textContent = text;
    return node;
  };
  const makeLink = (className, label) => {
    const link = make('a', `nav-item ${className}`);
    link.append(make('span', 'nav-label', label));
    return link;
  };
  const back = makeLink('app-context-back', 'Back to apps');
  back.href = '/apps';
  const switcher = make('details', 'app-context-switcher');
  const identity = make('summary', 'app-context-identity');
  const name = make('span', 'app-context-label');
  let avatarKey = '';
  identity.append(make('span', 'sr-only', 'Switch app, '), name);
  const appList = make('nav', 'app-context-apps');
  appList.setAttribute('aria-label', 'Switch app');
  switcher.append(identity, appList);
  const links = make('nav', 'app-context-links');
  links.setAttribute('aria-label', 'App sections');
  const sectionLinks = SECTIONS.map(([route, label, managerOnly]) => {
    const link = makeLink('app-context-link', label);
    link.dataset.appSection = route;
    links.append(link);
    return { route, managerOnly, link };
  });
  const settings = make('section', 'app-context-settings');
  const heading = make('h2', 'app-context-heading', 'Settings');
  const settingLinks = make('nav', 'app-context-settings-links');
  settingLinks.setAttribute('aria-label', 'App settings');
  const settingsLinks = SETTINGS.map(([route, label]) => {
    const link = makeLink('app-context-setting', label);
    link.dataset.settingsPage = route;
    settingLinks.append(link);
    return { route, link };
  });
  settings.append(heading, settingLinks);
  host.replaceChildren(back, switcher, links, settings);
  host.hidden = true;
  const appLinks = new Map();
  let currentApp = null;

  host.addEventListener('click', event => {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const link = event.target.closest?.('a[href]');
    if (!link || !host.contains(link) || link.closest('[hidden]') || (link.target && link.target !== '_self')) return;
    event.preventDefault();
    navigate(link.getAttribute('href'));
  });

  function clearDirty() {
    for (const { link } of settingsLinks) {
      link.removeAttribute('data-dirty');
      link.setAttribute('aria-label', link.textContent);
    }
  }

  function clear() {
    host.hidden = true;
    if (primary) primary.hidden = false;
    if (sidebar.dataset.context === 'app') delete sidebar.dataset.context;
    currentApp = null;
    switcher.open = false;
    setText(name, '');
    name.removeAttribute('title');
    identity.querySelector('.app-card-avatar')?.remove();
    avatarKey = '';
    identity.removeAttribute('aria-label');
    appList.replaceChildren();
    appLinks.clear();
    clearDirty();
    settings.hidden = true;
    for (const { link } of [...sectionLinks, ...settingsLinks]) {
      link.removeAttribute('href');
      markCurrent(link, false);
    }
  }

  function update({ app, apps = [], canManage = false, path } = {}) {
    if (!app || typeof app.slug !== 'string' || !app.slug) { clear(); return; }
    const sameApp = currentApp?.slug === app.slug
      && (currentApp.id == null || app.id == null || currentApp.id === app.id);
    if (!sameApp) {
      clearDirty();
      switcher.open = false;
    }
    currentApp = { slug: app.slug, id: app.id ?? (sameApp ? currentApp.id : null) };
    const previousFocus = document.activeElement;
    host.hidden = false;
    if (primary) primary.hidden = true;
    sidebar.dataset.context = 'app';
    setText(name, app.name || app.slug);
    name.title = app.name || app.slug;
    const avatar = avatarView(app);
    const nextAvatarKey = JSON.stringify(avatar);
    if (nextAvatarKey !== avatarKey) {
      identity.querySelector('.app-card-avatar')?.remove();
      identity.prepend(renderAppAvatar(document, avatar, 'app-card-avatar'));
      avatarKey = nextAvatarKey;
    }
    const base = `/apps/${encodeURIComponent(app.slug)}`;
    let pathname = (path || base).split(/[?#]/)[0].replace(/\/+$/, '') || '/';
    if (pathname === `${base}/overview`) pathname = base;
    else if (pathname === `${base}/configuration`) pathname = `${base}/settings/general`;
    else if (pathname === `${base}/access`) pathname = `${base}/settings/access`;
    else if (pathname.startsWith(`${base}/settings/`)) {
      const requested = pathname.slice(`${base}/settings/`.length);
      if (requested && !requested.includes('/')) {
        let section;
        try { section = decodeURIComponent(requested); } catch { section = requested; }
        pathname = `${base}/settings/${settingsRouteParams({ section }).section}`;
      }
    }
    for (const { route, managerOnly, link } of sectionLinks) {
      const href = route === 'overview' ? base : `${base}/${route}`;
      link.setAttribute('href', href);
      link.hidden = !!managerOnly && !canManage;
      markCurrent(link, !link.hidden && (pathname === href || (route !== 'overview' && pathname.startsWith(`${href}/`))));
    }
    settings.hidden = !canManage;
    for (const { route, link } of settingsLinks) {
      const href = `${base}/settings/${route}`;
      link.setAttribute('href', href);
      markCurrent(link, canManage && pathname === href);
    }

    // Reconcile keyed links instead of replacing the list during background
    // refreshes, so a focused app link and the native disclosure remain stable.
    const choices = new Map();
    for (const candidate of [...(Array.isArray(apps) ? apps : []), app]) {
      if (candidate && typeof candidate.slug === 'string' && candidate.slug) choices.set(candidate.slug, candidate);
    }
    for (const [slug, link] of appLinks) {
      if (!choices.has(slug)) {
        if (link.contains(document.activeElement)) identity.focus();
        link.remove();
        appLinks.delete(slug);
      }
    }
    let index = 0;
    for (const [slug, candidate] of choices) {
      let link = appLinks.get(slug);
      if (!link) {
        link = makeLink('app-context-app', candidate.name || slug);
        appLinks.set(slug, link);
      }
      setText(link.firstElementChild, candidate.name || slug);
      link.title = candidate.name || slug;
      link.setAttribute('href', `/apps/${encodeURIComponent(slug)}`);
      markCurrent(link, slug === app.slug, 'true');
      if (appList.children[index] !== link) appList.insertBefore(link, appList.children[index] || null);
      index++;
    }
    if (sameApp && previousFocus?.isConnected && host.contains(previousFocus)
        && !previousFocus.closest('[hidden]') && document.activeElement === document.body) {
      previousFocus.focus({ preventScroll: true });
    }
    const focused = document.activeElement;
    if (host.contains(focused) && focused.closest('[hidden]')) sectionLinks[0].link.focus();
  }

  return { update, clear };
}
