import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import vm from 'node:vm';
import { installSidebarAccount } from '../static/views/sidebar-account.js';

function setup() {
  const dom = new JSDOM(readFileSync(new URL('../static/index.html', import.meta.url), 'utf8'), { pretendToBeVisual: true });
  const { document: doc } = dom.window;
  const trigger = doc.getElementById('identity-card');
  const menu = doc.getElementById('account-menu');
  const help = doc.getElementById('help-button');
  const helpMenu = doc.getElementById('help-menu');
  trigger.getBoundingClientRect = help.getBoundingClientRect = () => ({ left: 12, top: 700 });
  const toggleMenu = (target, state) => {
    const event = new dom.window.Event('beforetoggle');
    Object.defineProperty(event, 'newState', { value: state });
    target.dispatchEvent(event);
  };
  const toggle = state => toggleMenu(menu, state);
  const toggleHelp = state => toggleMenu(helpMenu, state);
  menu.hidePopover = () => toggle('closed');
  helpMenu.hidePopover = () => toggleHelp('closed');
  const controller = installSidebarAccount(doc, dom.window);
  return { dom, doc, trigger, menu, toggle, controller, help, helpMenu, toggleHelp };
}

test('account popover escapes the rail and stays within the phone viewport', () => {
  const { dom, trigger, menu, toggle } = setup();
  Object.defineProperty(dom.window, 'innerWidth', { value: 390 });
  Object.defineProperty(dom.window, 'innerHeight', { value: 844 });
  toggle('open');
  assert.equal(trigger.getAttribute('aria-expanded'), 'true');
  assert.equal(menu.style.left, '12px');
  assert.equal(menu.style.bottom, '152px');
  assert.equal(menu.getAttribute('popover'), 'auto');
  assert.ok(trigger.closest('#sidebar').contains(menu), 'drawer focus trap must include popover items');
  dom.window.close();
});

test('Escape dismisses the account popover before the phone drawer handles it', () => {
  const { dom, doc, trigger, toggle } = setup();
  toggle('open');
  let escapedToDrawer = false;
  doc.addEventListener('keydown', () => { escapedToDrawer = true; });
  doc.dispatchEvent(new dom.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
  assert.equal(trigger.getAttribute('aria-expanded'), 'false');
  assert.equal(doc.activeElement, trigger);
  assert.equal(escapedToDrawer, false);
  dom.window.close();
});

test('selecting Profile closes the popover before opening the dialog and restores a visible return point', () => {
  const { dom, doc, trigger, toggle } = setup();
  toggle('open');
  let observed;
  doc.getElementById('profile-button').addEventListener('click', () => {
    observed = { expanded: trigger.getAttribute('aria-expanded'), focus: doc.activeElement };
  });
  doc.getElementById('profile-button').click();
  assert.equal(observed.expanded, 'false');
  assert.equal(observed.focus, trigger);
  dom.window.close();
});

test('navigation cleanup dismisses the account menu and hidden admin links stay gated', () => {
  const { dom, doc, trigger, toggle, controller } = setup();
  toggle('open');
  controller.close();
  assert.equal(trigger.getAttribute('aria-expanded'), 'false');
  assert.equal(doc.getElementById('administration-nav').hidden, true);
  assert.equal(doc.getElementById('tab-announcements').hidden, true);
  assert.ok(doc.getElementById('help-menu').contains(doc.getElementById('about-button')));
  assert.equal(doc.getElementById('account-menu').contains(doc.getElementById('about-button')), false);
  dom.window.close();
});


test('opening Help closes Account without moving focus to the previous trigger', () => {
  const { dom, doc, trigger, toggle, help, toggleHelp } = setup();
  toggle('open');
  help.focus();
  toggleHelp('open');
  assert.equal(trigger.getAttribute('aria-expanded'), 'false');
  assert.equal(help.getAttribute('aria-expanded'), 'true');
  assert.equal(doc.activeElement, help);
  doc.dispatchEvent(new dom.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
  assert.equal(help.getAttribute('aria-expanded'), 'false');
  assert.equal(doc.activeElement, help);
  dom.window.close();
});

test('About closes Help before the dialog captures its return point', () => {
  const { dom, doc, help, toggleHelp } = setup();
  toggleHelp('open');
  let observed;
  doc.getElementById('about-button').addEventListener('click', () => {
    observed = { expanded: help.getAttribute('aria-expanded'), focus: doc.activeElement };
  });
  doc.getElementById('about-button').click();
  assert.equal(observed.expanded, 'false');
  assert.equal(observed.focus, help);
  dom.window.close();
});

test('account, administration and help have separate ownership and collapse belongs to the header', () => {
  const { dom, doc, menu, helpMenu } = setup();
  assert.deepEqual([...menu.querySelectorAll('a,button')].map(el => el.textContent.trim()), ['Your profile', 'API tokens', 'Log out']);
  assert.deepEqual([...helpMenu.querySelectorAll('a,button')].map(el => el.textContent.trim()), ['Documentation', 'About ShinyHub']);
  assert.equal(doc.getElementById('account-tokens-link').getAttribute('href'), '/tokens');
  assert.equal(helpMenu.querySelector('a').getAttribute('rel'), 'noopener noreferrer');
  assert.ok(doc.querySelector('.sidebar-brand #sidebar-collapse'));
  assert.equal(doc.querySelector('.sidebar-footer #sidebar-collapse'), null);
  dom.window.close();
});

test('tabbing away dismisses a menu without pulling focus back', () => {
  const { dom, doc, menu, toggle, help, trigger } = setup();
  toggle('open');
  help.focus();
  menu.dispatchEvent(new dom.window.FocusEvent('focusout', { relatedTarget: help, bubbles: true }));
  assert.equal(trigger.getAttribute('aria-expanded'), 'false');
  assert.equal(doc.activeElement, help);
  dom.window.close();
});


test('session changes gate Administration and personal tokens independently', () => {
  const { dom, doc } = setup();
  const source = readFileSync(new URL('../static/app.js', import.meta.url), 'utf8');
  const ctx = { document: doc, Event: dom.window.Event, state: {}, renderIdentity() {}, syncSidebar() {},
    isOperatorRole: user => ['admin', 'publisher'].includes(user.role), appIsolationBanner: null,
    newAppButton: doc.getElementById('new-app'), newTokenButton: null, cliConnectPanel: null,
    tabAudit: doc.getElementById('tab-audit'), tabUsers: doc.getElementById('tab-users'), tabWorkers: doc.getElementById('tab-workers'),
    tabOverview: doc.getElementById('tab-overview'), tabApps: doc.getElementById('tab-apps') };
  ctx.newAppButton = doc.createElement('button');
  vm.createContext(ctx);
  vm.runInContext(source.slice(source.indexOf('  function applySessionCapabilities('), source.indexOf('  function showLoggedIn(')), ctx);
  for (const [payload, admin, tokens, audit, announcements] of [
    [{user:{role:'admin'}}, true, true, false, false],
    [{user:{role:'viewer'},can_read_audit:true}, true, true, true, false],
    [{user:{role:'publisher'},can_manage_announcements:true}, true, true, false, true],
    [{user:{role:'viewer',can_manage_self:false}}, false, false, false, false],
    [{user:{role:'viewer'}}, false, true, false, false],
  ]) {
    ctx.applySessionCapabilities(payload);
    assert.equal(!doc.getElementById('administration-nav').hidden, admin);
    assert.equal(!doc.getElementById('account-tokens-link').hidden, tokens);
    assert.equal(!ctx.tabUsers.hidden, payload.user.role === 'admin');
    assert.equal(!ctx.tabAudit.hidden, audit);
    assert.equal(!doc.getElementById('tab-announcements').hidden, announcements);
  }
  dom.window.close();
});

test('direct account logout shares the dirty guard, prevents duplicates and permits retry after failure', async () => {
  const { dom, doc } = setup();
  const source = readFileSync(new URL('../static/app.js', import.meta.url), 'utf8');
  let allow = false, requests = 0, resolveRequest, ended = 0, destination, toast;
  const ctx = { document: doc, logoutButton: doc.getElementById('logout-button'),
    confirmDiscardIfDirty: () => allow,
    api: () => { requests++; return new Promise(resolve => { resolveRequest = resolve; }); },
    flashToast: message => { toast = message; }, sessionController: {end: () => { ended++; }},
    suppressUnloadGuard: false, window: {location:{assign: url => { destination = url; }}},
    logoutTarget: () => '/', loginProviders: [] };
  vm.createContext(ctx);
  vm.runInContext(source.slice(source.indexOf("  const accountLogout ="), source.indexOf('  // Desktop sidebar collapse')), ctx);
  const direct = doc.getElementById('account-logout');
  direct.click(); assert.equal(requests, 0);
  allow = true; direct.click(); direct.click(); ctx.logoutButton.click();
  assert.equal(requests, 1); assert.equal(direct.disabled, true);
  resolveRequest({ok:false,status:500}); await new Promise(resolve => setImmediate(resolve));
  assert.equal(ended, 0); assert.equal(destination, undefined); assert.equal(direct.disabled, false); assert.match(toast, /500/);
  direct.click(); resolveRequest({ok:true,status:204}); await new Promise(resolve => setImmediate(resolve));
  assert.equal(requests, 2); assert.equal(ended, 1); assert.equal(destination, '/');
  dom.window.close();
});
