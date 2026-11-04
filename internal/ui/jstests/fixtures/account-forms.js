// Exercise the live app's handlers with the real page markup and injectable I/O.
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';
import { installPasswordVisibility, profileFieldErrors, setProfileFieldError } from '../../static/views/auth-fields.js';
import { identityModel } from '../../static/views/user-identity.js';
const html = readFileSync(new URL('../../static/index.html', import.meta.url), 'utf8');
const source = readFileSync(new URL('../../static/app.js', import.meta.url), 'utf8');
export function accountForms({ user, api, confirm = () => true } = {}) {
  const dom = new JSDOM(html, { url: 'http://localhost/', pretendToBeVisual: true });
  const doc = dom.window.document, $ = id => doc.getElementById(id);
  const ctx = { document: doc, window: { confirm }, state: { user: user || { id: 1, username: 'alice', display_name: '', can_set_password: true } },
    pendingAvatar: undefined, pendingAvatarColor: undefined, profileSaving: false, photoPreparing: false, photoGeneration: 0,
    api: api || (async () => new Response(JSON.stringify({ user: ctx.state.user }))),
    TextEncoder, passwordVisibility: installPasswordVisibility(doc), profileFieldErrors, setProfileFieldError, identityModel,
    setError: (el, message) => { el.textContent = message; el.hidden = !message; }, setHidden: (el, hidden) => { if (el) el.hidden = hidden; },
    modalTrap: () => ({ release() {}, activate() {} }), renderIdentity() {}, renderProfileAvatar() {},
    renderUserAvatar() {}, avatarPickerColor: () => '#123456', runInFlight: async (_, fn) => fn(),
    usersView: { hidden: true }, loadUsers() {}, sidebarDrawer: null, getThemePreference: () => 'system',
    expired: 0, handleUnauthorized: async () => { ctx.expired++; },
    loginPending: false, signedIn: 0, showLoggedIn: () => { ctx.signedIn++; },
    router: {}, startAuthenticatedRouter: async () => false, handleDeployHash: async () => {}, restorePendingSupportSession: async () => {},
  };
  const ids = { profileModal:'profile-modal', profileForm:'profile-form', profileSubmit:'profile-submit', profileCancel:'profile-cancel', profileClose:'profile-close', profilePhotoStatus:'profile-photo-status', profilePhotoChange:'profile-photo-change', profilePhotoRemove:'profile-photo-remove', profilePhotoFile:'profile-photo-file', profileAvatarColor:'profile-avatar-color', profileColorReset:'profile-color-reset', profileDisplayName:'profile-display-name', profileCurrentPw:'profile-current-password', profileNewPw:'profile-new-password', profilePwSection:'profile-password-section', profileError:'profile-error', profileSuccess:'profile-success', profileUsername:'profile-username', profileLogin:'profile-login', profileRole:'profile-role', profileAvatar:'profile-avatar', profilePhotoControls:'profile-photo-controls', profileNameField:'profile-name-field', profilePwManaged:'profile-password-managed', profileTokensLink:'profile-tokens-link', logoutButton:'logout-button', loginForm:'login-form', loginError:'login-error', usernameInput:'login-username', passwordInput:'login-password' };
  for (const [name,id] of Object.entries(ids)) ctx[name] = $(id);
  vm.createContext(ctx);
  const functions = source.slice(source.indexOf('  function profilePreviewUser()'), source.indexOf('  // openAboutModal'));
  const submit = source.slice(source.indexOf('  async function submitProfile(event)'), source.indexOf('  async function handleUnauthorized()'));
  vm.runInContext(functions + submit, ctx);
  const inputStart = source.indexOf('  for (const input of [profileDisplayName, profileCurrentPw, profileNewPw]) {');
  vm.runInContext(source.slice(inputStart, source.indexOf('  if (profileCancel)', inputStart)), ctx);
  let loginHandler;
  ctx.loginForm.addEventListener = (_, handler) => { loginHandler = handler; };
  vm.runInContext(source.slice(source.indexOf("  loginForm.addEventListener('submit'"), source.indexOf("  refreshButton.addEventListener('click'")), ctx);
  ctx.openProfileModal();
  return { ctx, doc, $, dom, submit: () => ctx.submitProfile({ preventDefault() {} }), login: () => loginHandler({ preventDefault() {} }), close: () => dom.window.close() };
}
