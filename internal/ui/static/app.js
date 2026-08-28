import { createRouter } from '/static/router.js';
import { createMetricsController } from '/static/metrics-controller.js';
import { mountAppsGrid } from '/static/views/apps-grid.js';
import { mountOverview } from '/static/views/overview.js';
import { mountLaunchpad } from '/static/views/launchpad.js';
import { renderAppAvatar, avatarView } from '/static/views/app-avatar.js';
import { isSingleEmoji, renderEmojiPicker } from '/static/views/emoji-picker.js';
import { mountUsers } from '/static/views/users.js';
import { tokenListModels, renderTokenList } from '/static/views/tokens.js';
import { mountWorkers, workerDisplay } from '/static/views/workers.js';
import {
  activationAttentionTooltip,
  degradedTooltip,
  summariseFleetHealth,
} from '/static/views/fleet-health.js';
import { createFocusTrap } from '/static/views/focus-trap.js';
import {
  auditEmptyMessage,
  auditListPath,
  auditLoadError,
  auditLoadingMessage,
  createLatestRequestGate,
  mountAuditLog,
} from '/static/views/audit-log.js';
import { mountAppDetail, refreshFleetSurfaces } from '/static/views/app-detail.js';
import { appCardBadge, updateCardStatusBadge, updateStatusPill } from '/static/views/app-card-badge.js';
import {
  appsSurfaceForSession,
  homeSurfaceForSession,
  renderSidebarApps,
  highlightSidebarApp,
  isPrimaryNavActive,
} from '/static/views/sidebar-nav.js';
import { createSidebarDrawer } from '/static/views/sidebar-drawer.js';
import { headerStats } from '/static/views/stat-format.js';
import { appCardFacts } from '/static/views/app-card-facts.js';
import { appCardActions } from '/static/views/app-card-actions.js';
import { createAppCardLifecycle, requestAppRestart, restartConfirmationCopy } from '/static/views/app-card-lifecycle.js';
import { applyLoginProviders } from '/static/views/login-providers.js';
import { applyBranding } from '/static/views/branding.js';
import { formatManifestSummary, renderDeployResult } from '/static/deploy-summary.js';
import { segmentApps } from '/static/views/fleet-ui.js';
import {
  activationState,
	activationErrorDetail,
	canCancelActivation,
  deletedScheduleActivations,
  deployTriggerLabel,
  afterSuccessDetail,
  afterSuccessLabel,
  dstAdvisoryMarkup,
  formatAge,
  formatDuration,
  runTriggerLabel,
  runDurationSeconds,
  scheduleState,
  scheduleDetailSignature,
  scheduleSummary,
  producerCompatibilityState,
} from '/static/views/schedule-ui.js';
import { auditDetailEntries } from '/static/views/audit-detail.js';
import { readAutoscaleForm, parseReplicaBound, renderAutoscaleSummary, summariseAutoscale } from '/static/views/autoscale.js';
import { workerCapacityLine, keepWarmInertNote } from '/static/views/worker-isolation.js';
import { parseRenderSeconds, renderPacingAdvice } from '/static/views/render-pacing.js';
import { initTheme, getThemePreference, setThemePreference } from '/static/views/theme.js';
import { backendLabel, metricsText, reasonLabel } from '/static/views/replica-display.js';
import { formatStatus } from '/static/views/status-label.js';
import { userRowCaps, RESERVED_USER_HINT } from '/static/views/user-row.js';
import { identityModel } from '/static/views/user-identity.js';
import { createServerInfoLoader, renderAbout } from '/static/views/about.js';
import { groupAppsForGrid } from '/static/views/app-grid-groups.js';
import { createGroupDisclosure } from '/static/views/group-disclosure.js';
import { buildProjectPatchBody } from '/static/views/project-edit-body.js';
import {
  CLI_CONNECT_STORAGE_KEY,
  cliConnectDeviceLabel,
  cliConnectRequestFromSearch,
  validCLIConnectRequest,
} from '/static/views/cli-connect.js';

function setHidden(element, hidden) {
  element.hidden = hidden;
}

function setError(element, message) {
  element.textContent = message || '';
  element.hidden = !message;
}

// Per-modal focus traps, created lazily and keyed by the overlay element. A
// trap confines Tab focus to the dialog while open and restores focus to the
// element that opened it on release.
const _modalTraps = new WeakMap();
let _modalBackgroundDepth = 0;
function setModalBackgroundInert(inert) {
  const regions = [document.getElementById('app-shell'), document.getElementById('mobile-topbar')].filter(Boolean);
  _modalBackgroundDepth = Math.max(0, _modalBackgroundDepth + (inert ? 1 : -1));
  for (const region of regions) {
    if (_modalBackgroundDepth > 0) region.setAttribute('inert', '');
    else region.removeAttribute('inert');
  }
}
function modalTrap(overlayEl) {
  let trap = _modalTraps.get(overlayEl);
  if (!trap) {
    const focusTrap = createFocusTrap(overlayEl.querySelector('.modal-card') || overlayEl);
    let active = false;
    trap = {
      activate() {
        if (active) return;
        active = true;
        setModalBackgroundInert(true);
        focusTrap.activate();
      },
      release() {
        if (!active) return;
        active = false;
        focusTrap.release();
        setModalBackgroundInert(false);
      },
    };
    _modalTraps.set(overlayEl, trap);
  }
  return trap;
}

function canManageApp(user, app) {
  if (!user || !app) {
    return false;
  }
  // Prefer the server-computed value when present (it accounts for per-app
  // member/group manager roles the client cannot see). Falls back to the
  // global-role/owner heuristic for list views that do not carry it.
  if (typeof app.can_manage === 'boolean') {
    return app.can_manage;
  }
  return user.role === 'admin' || user.role === 'operator' || user.id === app.owner_id;
}

function relativeTime(date) {
	if (!(date instanceof Date) || Number.isNaN(date.getTime())) return '—';
  const diff = Math.floor((Date.now() - date.getTime()) / 1000);
	if (diff < 0) {
		const remaining = Math.abs(diff);
		if (remaining < 3600) return `in ${Math.max(1, Math.ceil(remaining / 60))}m`;
		if (remaining < 86400) return `in ${Math.ceil(remaining / 3600)}h`;
		return `in ${Math.ceil(remaining / 86400)}d`;
	}
  if (diff < 60)    return `${diff}s ago`;
  if (diff < 3600)  return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}

// wireKebab wires a kebab "⋯" toggle to its dropdown list: click toggles
// open/closed, Escape and outside-click close it, clicking any menu item closes
// it, and aria-expanded stays in sync. The optional container gets the
// `kebab-open` class while open so CSS can lift it above its neighbours (the
// dropdown is intentionally allowed to overflow its card). This is the single
// wiring path for BOTH the dashboard card kebab and the app-detail header kebab
// (the latter previously had no handler at all, so its menu never opened).
// kebabControls maps a .kebab-menu element to the handle wireKebab returned for
// it, so code holding only the element (the metrics poller, re-syncing a card
// whose app changed state) can close the menu without re-implementing its
// open/closed bookkeeping.
const kebabControls = new WeakMap();

// Returns a handle so a caller that hides the menu (because the app's state no
// longer offers any action) can close it through the same setOpen that opened
// it, instead of writing `hidden`/aria-expanded/kebab-open a second time.
function wireKebab(button, list, container) {
  if (!button || !list) return null;
  const availableItems = () => [...list.querySelectorAll('button:not([disabled])')]
    .filter(item => !item.closest('[hidden]'));
  function onDocClick(e) {
    if (!list.contains(e.target) && !button.contains(e.target)) setOpen(false);
  }
  function onKey(e) {
    if (e.key === 'Escape') {
      e.preventDefault();
      setOpen(false);
      button.focus();
      return;
    }
    if (e.key === 'Tab') {
      setOpen(false);
      return;
    }
    const items = availableItems();
    if (!items.length || !list.contains(document.activeElement)) return;
    const current = Math.max(0, items.indexOf(document.activeElement));
    let next = null;
    if (e.key === 'ArrowDown') next = items[(current + 1) % items.length];
    if (e.key === 'ArrowUp') next = items[(current - 1 + items.length) % items.length];
    if (e.key === 'Home') next = items[0];
    if (e.key === 'End') next = items.at(-1);
    if (next) {
      e.preventDefault();
      next.focus();
    }
  }
  function setOpen(open, focus = '') {
    list.hidden = !open;
    button.setAttribute('aria-expanded', String(open));
    if (container) container.classList.toggle('kebab-open', open);
    if (open) {
      document.addEventListener('click', onDocClick, true);
      document.addEventListener('keydown', onKey, true);
      const items = availableItems();
      if (focus === 'first') items[0]?.focus();
      if (focus === 'last') items.at(-1)?.focus();
    } else {
      document.removeEventListener('click', onDocClick, true);
      document.removeEventListener('keydown', onKey, true);
    }
  }
  button.addEventListener('click', (e) => {
    e.stopPropagation();
    const opening = list.hidden;
    setOpen(opening, opening ? 'first' : '');
  });
  button.addEventListener('keydown', (e) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    setOpen(true, e.key === 'ArrowDown' ? 'first' : 'last');
  });
  list.addEventListener('click', (e) => {
    if (e.target.closest('button')) setOpen(false);
  });
  return { close: () => setOpen(false) };
}

// formatStatus (status → display label) is imported from views/status-label.js
// so the cards, detail pill, sidebar, and replica badges all speak with one
// voice. Badges no longer use `text-transform: uppercase`, so casing matters.

document.addEventListener('DOMContentLoaded', () => {
  // Branding: server injects window.__SHINYHUB_BRANDING__ (see
  // internal/ui/branding.go RenderIndex). Absent/empty in zero-branding mode.
  // Applied to every .brand slot (sidebar, mobile top bar, boot splash, and the
  // login card) by the unit-tested views/branding.js.
  applyBranding(document, window.__SHINYHUB_BRANDING__);

  const state = {
    user: null,
    apps: [],
    auditPage: 0,
    auditHasMore: false,
    auditSelection: { event: '', run: '', action: '' },
    canCreateApps: false,
    canManageApps: false,
    resetPwTargetId: null,
    resetPwTargetUsername: '',
  };
  const auditRequests = createLatestRequestGate();

  // Project rows from GET /api/projects, keyed by slug. The grid group objects
  // carry only what an app row can carry (slug, name, icon), so the edit modal
  // reads the description from here. Without it the modal would show an empty
  // Description for every project and saving would clear it.
  const projectRows = new Map();
  // Restart feedback belongs to the affected app card, not the page. The
  // durable model survives search/sort/refresh re-renders; the control map is
  // rebuilt with the DOM and never retains detached cards.
  const appCardLifecycleControls = new Map();
  const appRestartFeedback = new Map();
  let appRestartFeedbackVersion = 0;

  const loginView = document.getElementById('login-view');
  const overviewView = document.getElementById('overview-view');
  const launchpadView = document.getElementById('launchpad-view');
  const appsView = document.getElementById('apps-view');
  const appDetailView = document.getElementById('app-detail-view');
  const loginForm = document.getElementById('login-form');
  const usernameInput = document.getElementById('login-username');
  const passwordInput = document.getElementById('login-password');
  const loginError = document.getElementById('login-error');
  const appError = document.getElementById('app-error');
  const refreshButton = document.getElementById('refresh-button');
  const fleetHealthEl = document.getElementById('fleet-health');
  const logoutButton = document.getElementById('logout-button');
  // Sidebar identity card + profile modal (self-service display name + password).
  const identityCard    = document.getElementById('identity-card');
  const identityAvatar  = document.getElementById('identity-avatar');
  const identityName    = document.getElementById('identity-name');
  const identityRole    = document.getElementById('identity-role');
  const profileModal    = document.getElementById('profile-modal');
  const profileClose    = document.getElementById('profile-close');
  const profileForm     = document.getElementById('profile-form');
  const profileAvatar   = document.getElementById('profile-avatar');
  const profileUsername = document.getElementById('profile-username');
  const profileRole     = document.getElementById('profile-role');
  const profileLogin    = document.getElementById('profile-login');
  const profileNameField   = document.getElementById('profile-name-field');
  const profileDisplayName = document.getElementById('profile-display-name');
  const profileSubmit      = document.getElementById('profile-submit');
  const profilePwSection   = document.getElementById('profile-password-section');
  const profilePwManaged   = document.getElementById('profile-password-managed');
  const profileCurrentPw   = document.getElementById('profile-current-password');
  const profileNewPw       = document.getElementById('profile-new-password');
  const profileError    = document.getElementById('profile-error');
  const profileSuccess  = document.getElementById('profile-success');
  // About dialog: server identity + host runtimes. Fetched at most once per
  // session; /api/server-info is unauthenticated, so this needs no credentials.
  const aboutModal   = document.getElementById('about-modal');
  const aboutButton  = document.getElementById('about-button');
  const aboutClose   = document.getElementById('about-close');
  const loadServerInfo = createServerInfoLoader((url) => fetch(url));
  const appGrid = document.getElementById('app-grid');
  const emptyState = document.getElementById('empty-state');
  const emptyStateHeading = document.getElementById('empty-state-heading');
  const emptyStateEyebrow = document.getElementById('empty-state-eyebrow');
  const emptyStateLead    = document.getElementById('empty-state-lead');
  const emptyStateActions = document.getElementById('empty-state-actions');
  const emptyStateCTA     = document.getElementById('empty-state-cta');
  const logPane = document.getElementById('log-pane');
  const logPaneTitle = document.getElementById('log-pane-title');
  const logPaneBody = document.getElementById('log-pane-body');
  const logPaneClose = document.getElementById('log-pane-close');
  const auditView   = document.getElementById('audit-view');
  const auditError  = document.getElementById('audit-error');
  const auditBody   = document.getElementById('audit-body');
  const auditEmpty  = document.getElementById('audit-empty');
  const auditLoading = document.getElementById('audit-loading');
  const auditPrev   = document.getElementById('audit-prev');
  const auditNext   = document.getElementById('audit-next');
  const auditRange  = document.getElementById('audit-range');
  const auditContext = document.getElementById('audit-context');
  const auditContextCopy = document.getElementById('audit-context-copy');
  const auditPagination = document.getElementById('audit-pagination');
  const tabAudit    = document.getElementById('tab-audit');
  const tabUsers    = document.getElementById('tab-users');
  const tabWorkers  = document.getElementById('tab-workers');
  const tabOverview = document.getElementById('tab-overview');
  const tabApps     = document.getElementById('tab-apps');
  let   sidebarDrawer = null; // mobile off-canvas drawer controller (wired below)
  const workersView    = document.getElementById('workers-view');
  const workersError   = document.getElementById('workers-error');
  const workersEmpty   = document.getElementById('workers-empty');
  const workersTable   = document.getElementById('workers-table');
  const workersBody    = document.getElementById('workers-body');
  const usersView   = document.getElementById('users-view');
  const usersError  = document.getElementById('users-error');
  const usersBody   = document.getElementById('users-body');
  const usersRefresh   = document.getElementById('users-refresh');
  const newUserButton  = document.getElementById('new-user-button');
  const newUserModal   = document.getElementById('new-user-modal');
  const newUserClose   = document.getElementById('new-user-close');
  const newUserCancel  = document.getElementById('new-user-cancel');
  const newUserForm    = document.getElementById('new-user-form');
  const newUserUsername = document.getElementById('new-user-username');
  const newUserPassword = document.getElementById('new-user-password');
  const newUserRole     = document.getElementById('new-user-role');
  const newUserError    = document.getElementById('new-user-error');
  const newUserSnippet  = document.getElementById('new-user-snippet');
  const newUserSnippetCopy = document.getElementById('new-user-snippet-copy');
  const identityPeopleTab = document.getElementById('identity-people-tab');
  const identityServicesTab = document.getElementById('identity-services-tab');
  const identityPeoplePanel = document.getElementById('identity-people-panel');
  const identityServicesPanel = document.getElementById('identity-services-panel');
  const serviceAccountsList = document.getElementById('service-accounts-list');
  const serviceAccountsError = document.getElementById('service-accounts-error');
  const serviceAccountsStatus = document.getElementById('service-accounts-status');
  const serviceCredentialModal = document.getElementById('service-credential-modal');
  const serviceCredentialForm = document.getElementById('service-credential-form');
  const serviceCredentialSecret = document.getElementById('service-credential-secret');
  const serviceCredentialName = document.getElementById('service-credential-name');
  const serviceCredentialRole = document.getElementById('service-credential-role');
  const serviceCredentialApps = document.getElementById('service-credential-apps');
  const serviceCredentialAppList = document.getElementById('service-credential-app-list');
  const serviceCredentialExpiry = document.getElementById('service-credential-expiry');
  const serviceCredentialError = document.getElementById('service-credential-error');
  const serviceCredentialToken = document.getElementById('service-credential-token');
  const serviceCredentialRoleHelp = document.getElementById('service-credential-role-help');
  const serviceCredentialRoleWarning = document.getElementById('service-credential-role-warning');
  const serviceCredentialPlan = document.getElementById('service-credential-plan');
  const serviceCredentialCreatedSummary = document.getElementById('service-credential-created-summary');
  const serviceCredentialCreatedWarning = document.getElementById('service-credential-created-warning');
  const serviceCredentialHeading = document.getElementById('service-credential-heading');
  const serviceCredentialContext = document.getElementById('service-credential-context');
  let serviceCredentialAccountKey = 'deployment';
  let serviceCredentialSecretRevealed = false;
  let serviceCredentialRotation = null;
  // API tokens page + new-token modal
  const tokensView      = document.getElementById('tokens-view');
  const tokensList      = document.getElementById('tokens-list');
  const tokensError     = document.getElementById('tokens-error');
  const tokensRefresh   = document.getElementById('tokens-refresh');
  const newTokenButton  = document.getElementById('new-token-button');
  const newTokenModal   = document.getElementById('new-token-modal');
  const newTokenClose   = document.getElementById('new-token-close');
  const newTokenCancel  = document.getElementById('new-token-cancel');
  const newTokenForm    = document.getElementById('new-token-form');
  const newTokenName    = document.getElementById('new-token-name');
  const newTokenError   = document.getElementById('new-token-error');
  const tokenReveal     = document.getElementById('token-reveal');
  const tokenRevealValue = document.getElementById('token-reveal-value');
  const tokenRevealCopy = document.getElementById('token-reveal-copy');
  const tokenRevealDone = document.getElementById('token-reveal-done');
  const tokenRevealStatus = document.getElementById('token-reveal-status');
  const profileTokensLink = document.getElementById('profile-tokens-link');
  const cliConnectPanel   = document.getElementById('cli-connect-panel');
  const cliConnectUser    = document.getElementById('cli-connect-user');
  const cliConnectDevice  = document.getElementById('cli-connect-device');
  const cliConnectCode    = document.getElementById('cli-connect-code');
  const cliConnectError   = document.getElementById('cli-connect-error');
  const cliConnectSuccess = document.getElementById('cli-connect-success');
  const cliConnectCancel  = document.getElementById('cli-connect-cancel');
  const cliConnectApprove = document.getElementById('cli-connect-approve');
  const resetPwModal    = document.getElementById('reset-password-modal');
  const resetPwClose    = document.getElementById('reset-password-close');
  const resetPwCancel   = document.getElementById('reset-password-cancel');
  const resetPwForm     = document.getElementById('reset-password-form');
  const resetPwInput    = document.getElementById('reset-password-input');
  const resetPwUsername = document.getElementById('reset-password-username');
  const resetPwError    = document.getElementById('reset-password-error');
  const newAppButton       = document.getElementById('new-app-button');
  const newAppModal        = document.getElementById('new-app-modal');
  const newAppHeading      = document.getElementById('new-app-heading');
  const newAppStep         = document.getElementById('new-app-step');
  const newAppClose        = document.getElementById('new-app-close');
  const newAppCancel       = document.getElementById('new-app-cancel');
  const newAppForm         = document.getElementById('new-app-form');
  const newAppSlug         = document.getElementById('new-app-slug');
  const newAppName         = document.getElementById('new-app-name');
  const newAppProject      = document.getElementById('new-app-project');
  const newAppOptional     = newAppProject.closest('details');
  const newAppError        = document.getElementById('new-app-error');
  const newAppHandoff      = document.getElementById('new-app-handoff');
  const newAppSnippet      = document.getElementById('new-app-snippet');
  const newAppSnippetLabel = document.getElementById('new-app-snippet-label');
  const newAppSnippetCopy  = document.getElementById('new-app-snippet-copy');
  const newAppDone         = document.getElementById('new-app-done');
  const newAppDeployNow    = document.getElementById('new-app-deploy-now');
  const newAppSubmit       = document.getElementById('new-app-submit');

  const projectEditModal    = document.getElementById('project-edit-modal');
  const projectEditSave     = document.getElementById('project-edit-save');
  const projectEditCancel   = document.querySelector('[data-close-project-edit]');

  const deployModal        = document.getElementById('deploy-modal');
  const deployModalClose   = document.getElementById('deploy-modal-close');
  const deployAppName      = document.getElementById('deploy-app-name');
  const deployDropzone     = document.getElementById('deploy-dropzone');
  const deployPick         = document.getElementById('deploy-pick');
  const deployFileInput    = document.getElementById('deploy-file');
  const deploySummary      = document.getElementById('deploy-summary');
  const deploySourceName   = document.getElementById('deploy-source-name');
  const deployFileCount    = document.getElementById('deploy-file-count');
  const deployBundleSize   = document.getElementById('deploy-bundle-size');
  const deployIgnoredRow   = document.getElementById('deploy-ignored-row');
  const deployIgnoredList  = document.getElementById('deploy-ignored-list');
  const deployProgressWrap = document.getElementById('deploy-progress-wrap');
  const deployProgressBar  = document.getElementById('deploy-progress-bar');
  const deployProgressText = document.getElementById('deploy-progress-text');
  const deployResult       = document.getElementById('deploy-result');
  const deployResultList   = document.getElementById('deploy-result-list');
  const deployError        = document.getElementById('deploy-error');
  const deployCancel       = document.getElementById('deploy-cancel');
  const deploySubmit       = document.getElementById('deploy-submit');
  const deployCliSnippet   = document.getElementById('deploy-cli-snippet');
  const deployCliCopy      = document.getElementById('deploy-cli-snippet-copy');
  const deployCliCopyLabel = deployCliCopy.querySelector('.copy-label');
  const deployCliCopyStatus = document.getElementById('deploy-cli-snippet-status');

  // Mirrors internal/slug.Pattern. RFC-1123 hostname label: 1-63 chars,
  // lowercase alphanumerics and hyphens, must start and end with an alphanumeric.
  const SLUG_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

  let BUNDLE_RULES = null;
  async function loadBundleRules() {
    if (BUNDLE_RULES) return BUNDLE_RULES;
    const resp = await fetch('/static/bundle-rules.json');
    if (!resp.ok) throw new Error('failed to load bundle rules');
    BUNDLE_RULES = await resp.json();
    return BUNDLE_RULES;
  }

  // Classify a single entry (file or directory) relative to the bundle root.
  // Returns one of: 'accept', 'skipCacheDir', 'rejectDataDir',
  // 'rejectDatasetDir', 'rejectExtension', 'rejectFileSize'.
  // Directory decisions should pass size=0. The leading-slash strip keeps
  // first-segment classification in lockstep with server-side bundle.Inspect,
  // which operates on paths cleaned via path.Clean.
  function inspectBundleEntry(rules, relPath, size) {
    const clean = relPath.replace(/^\/+/, '');
    const first = clean.split('/')[0];
    if (first === 'data') return 'rejectDataDir';
    if (first === 'datasets' || first === '.shinyhub-data') return 'rejectDatasetDir';
    if (rules.cacheDirs.includes(first)) return 'skipCacheDir';
    const lower = clean.toLowerCase();
    for (const ext of rules.dataExtensions) {
      if (lower.endsWith(ext.toLowerCase())) return 'rejectExtension';
    }
    if (rules.maxFileBytes > 0 && size > rules.maxFileBytes) return 'rejectFileSize';
    return 'accept';
  }

  let activeEventSource = null;
  let deployState = null; // { slug, appName, blob, fileCount, rejections: Map<string, string[]>, xhr }
  let slugEdited = false;

  // Derive a slug from a display name: strip diacritics, lowercase, replace
  // runs of non-alphanumerics with dashes, cap at 63 chars, *then* trim
  // leading/trailing dashes. Truncation must happen before the trailing-dash
  // trim — otherwise a long input landing on `-` at byte 63 produces a slug
  // ending in `-`, which SLUG_RE rejects and the modal silently refuses to
  // submit. Mirrors internal/cli/deploy.go sanitizeSlug; both must agree.
  function slugify(name) {
    return name
      .normalize('NFKD')
      .replace(/[\u0300-\u036f]/g, '')
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .slice(0, 63)
      .replace(/^-+|-+$/g, '');
  }

  function readCookie(name) {
    const prefix = name + '=';
    for (const raw of document.cookie.split(';')) {
      const c = raw.trim();
      if (c.startsWith(prefix)) return decodeURIComponent(c.slice(prefix.length));
    }
    return '';
  }

  async function api(path, options = {}) {
    const init = {
      credentials: 'same-origin',
      headers: {},
      ...options,
    };
    init.headers = {...init.headers};
    if (init.body && !init.headers['Content-Type']) {
      init.headers['Content-Type'] = 'application/json';
    }
    const method = (init.method || 'GET').toUpperCase();
    if (method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS') {
      const token = readCookie('csrf_token');
      if (token) init.headers['X-CSRF-Token'] = token;
    }
    return fetch(path, init);
  }

  // errorMessage extracts the human-readable message from a failed API
  // response. Every handler writes {"error": "..."} (see internal/api/helpers.go
  // writeError), so parse that instead of showing the raw JSON body in a toast.
  async function errorMessage(resp, fallback = 'Request failed') {
    try {
      const j = await resp.json();
      if (j && j.error) return j.error;
    } catch { /* non-JSON body */ }
    return fallback;
  }

  function renderEmptyStateCopy() {
    if (state.canCreateApps) {
      emptyStateEyebrow.textContent = 'Ready when you are';
      emptyStateHeading.innerHTML = 'Deploy your <strong>first Shiny app</strong>.';
      emptyStateLead.textContent =
        'Name it, upload a folder or .zip, and ShinyHub will take you straight to the live logs. You can use the CLI instead whenever you prefer.';
      emptyStateActions.hidden = false;
    } else {
      emptyStateEyebrow.textContent = 'Nothing here yet';
      emptyStateHeading.innerHTML = 'No apps are visible to you.';
      emptyStateLead.textContent =
        'Apps shared with you will appear here once the owner deploys them. Check back soon.';
      emptyStateActions.hidden = true;
    }
  }

  // Renders grouped apps into the provided grid and empty-state elements. Takes
  // explicit DOM references so mountAppsGrid can call it from the view module
  // without closing over the closure-level appGrid/emptyState constants.
  function renderCardFacts(container, app, live = null) {
    container.textContent = '';
    for (const item of appCardFacts(app, live)) {
      const el = document.createElement('span');
      el.className = `app-card-fact${item.tone ? ` is-${item.tone}` : ''}`;
      el.textContent = item.text;
      if (item.title) el.title = item.title;
      container.appendChild(el);
    }
  }

  function applyRestartFeedbackToCard(slug, card, control) {
    if (!card || !control) return;
    const entry = appRestartFeedback.get(slug);
    const pending = entry && entry.phase === 'pending';

    if (entry) control.setPhase(entry.phase, entry.message || '');
    else control.clear();

    card.classList.toggle('is-lifecycle-busy', !!pending);
    const menuButton = card.querySelector('.kebab-menu > button');
    if (menuButton) menuButton.disabled = !!pending;

    const badge = card.querySelector('.app-header .badge');
    const app = state.apps.find((item) => item.slug === slug);
    if (!badge || !app) return;
    if (pending) {
      badge.className = 'badge badge-deploying';
      badge.textContent = 'Restarting';
      return;
    }
    const info = appCardBadge(app, formatStatus);
    badge.className = info.cls;
    badge.textContent = info.text;
  }

  function setRestartFeedback(slug, phase, message = '') {
    const entry = { phase, message, version: ++appRestartFeedbackVersion };
    appRestartFeedback.set(slug, entry);
    const control = appCardLifecycleControls.get(slug);
    const card = appGrid.querySelector(`.app-card[data-slug="${slug}"]`);
    applyRestartFeedbackToCard(slug, card, control);
    return entry;
  }

  function clearRestartFeedback(slug, expectedVersion = null) {
    const current = appRestartFeedback.get(slug);
    if (expectedVersion !== null && current && current.version !== expectedVersion) return;
    appRestartFeedback.delete(slug);
    const control = appCardLifecycleControls.get(slug);
    const card = appGrid.querySelector(`.app-card[data-slug="${slug}"]`);
    applyRestartFeedbackToCard(slug, card, control);
  }

  function renderGridVerbatim(groups, gridEl, emptyEl, options = {}) {
    gridEl.textContent = '';
    appCardLifecycleControls.clear();
    const total = groups.reduce((n, g) => n + g.apps.length, 0);
    const empty = total === 0;
    emptyEl.hidden = !empty;
    if (empty) renderEmptyStateCopy();

    // A lone ungrouped group needs no heading: it would label the whole grid
    // "All apps", which is what the grid already is. This keeps the dashboard
    // of an operator who uses no projects looking exactly as it does today.
    const soleUngrouped = groups.length === 1 && groups[0].project === '';

    for (const group of groups) {
      let cardHost;
      if (soleUngrouped) {
        cardHost = document.createElement('div');
        cardHost.className = 'app-grid-flat';
        gridEl.appendChild(cardHost);
      } else {
        const disclosure = createGroupDisclosure(document, {
          view: 'apps',
          groupKey: group.project,
          label: group.name || 'Other apps',
          count: group.apps.length,
          iconEmoji: group.iconEmoji,
          classPrefix: 'app-grid',
          forceExpanded: !!options.forceExpanded,
        });
        if (group.project && isOperatorRole(state.user)) {
          const edit = document.createElement('button');
          edit.type = 'button';
          edit.className = 'app-grid-group-edit';
          edit.setAttribute('aria-label', `Edit project ${group.name}`);
          edit.title = `Edit ${group.name}`;
          const editIcon = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
          editIcon.setAttribute('viewBox', '0 0 20 20');
          editIcon.setAttribute('fill', 'none');
          editIcon.setAttribute('stroke', 'currentColor');
          editIcon.setAttribute('stroke-width', '1.65');
          editIcon.setAttribute('stroke-linecap', 'round');
          editIcon.setAttribute('stroke-linejoin', 'round');
          editIcon.setAttribute('aria-hidden', 'true');
          const editPath = document.createElementNS('http://www.w3.org/2000/svg', 'path');
          editPath.setAttribute('d', 'M12.9 3.1a1.7 1.7 0 0 1 2.4 2.4L7 13.8l-3.2.8.8-3.2 8.3-8.3Z');
          editIcon.appendChild(editPath);
          edit.appendChild(editIcon);
          edit.addEventListener('click', () => { openProjectEditModal(group); });
          disclosure.header.appendChild(edit);
        }
        gridEl.appendChild(disclosure.root);
        cardHost = disclosure.body;
      }

    for (const app of group.apps) {
      const card = document.createElement('div');
      card.className = 'app-card';
      card.dataset.slug = app.slug;

      const header = document.createElement('div');
      header.className = 'app-header';

      const identity = document.createElement('div');
      identity.className = 'app-card-identity';
      const titleLink = document.createElement('a');
      titleLink.href = `/apps/${app.slug}`;
      titleLink.setAttribute('data-nav', '');
      titleLink.className = 'app-card-title';
      titleLink.setAttribute('aria-label', canManageApp(state.user, app)
        ? `Manage ${app.name}`
        : `View ${app.name}`);
      const name = document.createElement('strong');
      name.textContent = app.name;
      const manageArrow = document.createElement('span');
      manageArrow.className = 'app-card-manage-arrow';
      manageArrow.setAttribute('aria-hidden', 'true');
      manageArrow.textContent = '›';
      titleLink.append(name, manageArrow);
      const slug = document.createElement('div');
      slug.className = 'app-card-slug';
      slug.textContent = `/${app.slug}`;
      identity.append(titleLink, slug);
      header.appendChild(identity);

      // Every badge goes in one wrapping container rather than straight into
      // the header row. The header is a nowrap flex row inside a ~310px grid
      // column and .app-card is overflow:visible (so the kebab dropdown can
      // escape), so a badge appended directly to it has nothing to stop it
      // rendering out past the card border.
      const badges = document.createElement('div');
      badges.className = 'app-header-badges';
      header.appendChild(badges);

      const badgeInfo = appCardBadge(app, formatStatus);
      const badge = document.createElement('span');
      badge.className = badgeInfo.cls;
      badge.textContent = badgeInfo.text;
      // Tag the status badge so the 10s metrics poll can refresh it in place
      // (see onMetrics → updateCardStatusBadge); without this the badge would
      // freeze at its render-time status and miss wake/hibernate transitions.
      badge.dataset.slug = app.slug;
      badges.appendChild(badge);

      const facts = document.createElement('div');
      facts.className = 'app-card-facts';
      facts.dataset.slug = app.slug;
      renderCardFacts(facts, app);

      const actions = document.createElement('div');
      actions.className = 'app-actions';

      const lifecycleControl = createAppCardLifecycle(document, {
        appName: app.name,
        onConfirm: () => performRestart(app.slug, null, true),
        onRetry: () => performRestart(app.slug, null, true),
        onViewLogs: () => openLogs(app.slug),
      });
      appCardLifecycleControls.set(app.slug, lifecycleControl);

      const canManage = canManageApp(state.user, app);
      const cardActions = appCardActions(app, canManage);

      if (cardActions.showOpen) {
        const openLink = document.createElement('a');
        openLink.href = `/app/${app.slug}/`;
        openLink.target = '_blank';
        openLink.rel = 'noopener noreferrer';
        openLink.append(document.createTextNode('Open app'));
        const externalArrow = document.createElement('span');
        externalArrow.setAttribute('aria-hidden', 'true');
        externalArrow.textContent = '↗';
        openLink.appendChild(externalArrow);
        openLink.setAttribute('aria-label', `Open ${app.name} app in a new tab`);
        actions.appendChild(openLink);
      }

      if (canManage) {
        // First deploy is required activation, so it stays visible. Once the
        // app has a bundle, manual redeploy remains available in the overflow
        // menu without occupying every card (CI/CLI may own that workflow).
        if (cardActions.deployIsPrimary) {
          const deployButton = document.createElement('button');
          deployButton.type = 'button';
          deployButton.textContent = 'Deploy first release';
          deployButton.className = 'btn-primary';
          deployButton.setAttribute('aria-label', `Deploy first bundle to ${app.name}`);
          deployButton.addEventListener('click', () => openDeployModal(app));
          actions.appendChild(deployButton);
        }

        // The menu lists whichever lifecycle actions apply to this app right
        // now, so a stopped app still gets a menu (holding Start) where a
        // running one holds Restart, Sleep and Stop. All four rows are always
        // rendered and hidden per state rather than built from the current
        // state, so the metrics poller can re-sync them in place when an app
        // hibernates or wakes between grid loads.
        const lifecycleItems = [
          ['redeploy', 'Deploy new bundle', () => openDeployModal(app)],
          ['restart', 'Restart', restart],
          ['sleep', 'Sleep', sleepApp],
          ['stop', 'Stop', stopApp],
          ['start', 'Start', startApp],
        ];
        const kebab = document.createElement('div');
        kebab.className = 'kebab-menu';
        kebab.dataset.slug = app.slug;
        const rows = lifecycleItems
          .map(([action, label]) => `<li role="none" hidden><button type="button" role="menuitem" data-kebab="${action}">${label}</button></li>`)
          .join('\n              ');
        kebab.innerHTML = `
          <button type="button" aria-haspopup="menu" aria-expanded="false">⋯</button>
          <ul class="kebab-list" role="menu" hidden>
            ${rows}
          </ul>
        `;
        const kebabBtn = kebab.querySelector('button');
        kebabBtn.setAttribute('aria-label', `More actions for ${app.name}`);
        const kebabList = kebab.querySelector('.kebab-list');
        kebabControls.set(kebab, wireKebab(kebabBtn, kebabList, card));
        for (const [action, , handler] of lifecycleItems) {
          kebabList.querySelector(`[data-kebab="${action}"]`)
            .addEventListener('click', (e) => handler(app.slug, e.currentTarget));
        }
        actions.appendChild(kebab);
        syncCardActions(kebab, cardActions);
      }

      card.appendChild(header);
      card.appendChild(facts);
      card.appendChild(lifecycleControl.root);
      card.appendChild(actions);
      applyRestartFeedbackToCard(app.slug, card, lifecycleControl);
      cardHost.appendChild(card);
    }
    }
  }

  function renderApps() {
    const searchEl = document.getElementById('apps-search');
    const sortEl   = document.getElementById('apps-sort');
    const resultsStatus = document.getElementById('apps-results-status');

    let apps = state.apps.slice();

    // Filter by search query.
    const q = (searchEl ? searchEl.value : '').trim().toLowerCase();
    if (q) {
      apps = apps.filter(a =>
        a.name.toLowerCase().includes(q) || a.slug.toLowerCase().includes(q),
      );
    }

    // Filter by fleet management segment.
    const segEl = document.getElementById('apps-segment');
    apps = segmentApps(apps, segEl ? segEl.value : 'all');
    if (resultsStatus) {
      const filtered = !!q || !!(segEl && segEl.value !== 'all');
      resultsStatus.textContent = filtered
        ? `${apps.length} ${apps.length === 1 ? 'app' : 'apps'} shown.`
        : '';
    }

    // Sort is applied WITHIN each project group, using the same comparators as
    // before (they now live in app-grid-groups.js). Groups themselves keep the
    // shared order: ungrouped first, then projects by display name. Ordering
    // sections by "most recent deploy" would make headings jump between
    // renders, which defeats the purpose of an index.
    const sortKey = sortEl ? sortEl.value : 'default';
    renderGridVerbatim(
      groupAppsForGrid(apps, { sortKey }),
      appGrid,
      emptyState,
      { forceExpanded: !!q },
    );
  }

  function showLoggedOut() {
    closeLogs();
    metrics.setTargets([]);
    state.user = null;
    state.apps = [];
    state.auditPage = 0;
    state.auditHasMore = false;
    state.auditSelection = { event: '', run: '', action: '' };
    auditRequests.invalidate();
    state.canCreateApps = false;
    state.canManageApps = false;
    appRestartFeedback.clear();
    appCardLifecycleControls.clear();
    newAppButton.hidden = true;
    closeProfileModal();
    renderIdentity(null);
    setHidden(logoutButton, true);
    setHidden(loginView, false);
    setHidden(overviewView, true);
    setHidden(launchpadView, true);
    setHidden(appsView, true);
    setHidden(usersView, true);
    setHidden(auditView, true);
    setHidden(appDetailView, true);
    document.body.dataset.auth = 'out';
    // Fully close the drawer (clears .app-content inert + releases the focus
    // trap), not just the class — otherwise logging out from an open mobile
    // drawer leaves the login form inert and unfocusable.
    if (sidebarDrawer) sidebarDrawer.close();
    setError(loginError, '');
    setError(appError, '');
    renderApps();
    syncSidebar();
    // Land the caret in the first field: the login card is the whole page in
    // this state, so there is nothing else to focus.
    if (usernameInput.offsetParent !== null) usernameInput.focus();
  }

  function showLoggedIn(payload) {
    state.user = payload.user;
    state.canCreateApps = !!payload.can_create_apps;
    state.canManageApps = !!payload.can_manage_apps;
    state.canReadAudit = !!payload.can_read_audit;
    renderIdentity(payload.user);
    setHidden(logoutButton, false);
    setHidden(loginView, true);
    document.body.dataset.auth = 'in';
    // Audit access is a server-computed capability (admin, or operator when
    // auth.operator_audit_access is on), not a client-side role check.
    tabAudit.hidden = !state.canReadAudit;
    tabUsers.hidden = payload.user.role !== 'admin';
    tabWorkers.hidden = payload.user.role !== 'admin';
    // The home (/) is role-adaptive: fleet operators (admin/operator) get the
    // Overview, while Apps is the one application destination for every role.
    // Its route renders an opening gallery for viewers and the management grid
    // for developers/operators/admins.
    const isOperator = payload.user.role === 'admin' || payload.user.role === 'operator';
    tabOverview.hidden = !isOperator;
    tabApps.hidden = false;
    newAppButton.hidden = !state.canCreateApps;
    const selfService = payload.user.can_manage_self !== false;
    if (newTokenButton) newTokenButton.hidden = !selfService;
    if (cliConnectPanel && !selfService) cliConnectPanel.hidden = true;
    // Apply the new session's sidebar policy before any asynchronous list load
    // so switching identities in one tab cannot flash the previous catalog.
    syncSidebar();
    // Load the admin fleet-health banner now that state.user is set; loadApps
    // can fire before this during boot, when the admin gate would skip it.
    loadFleetHealth();
    // Viewers use the Apps gallery as their complete catalog, so repeating the
    // same inventory in the sidebar adds a second competing navigation tree.
    // Management roles retain the sidebar quick-switch list.
    if (appsSurfaceForSession(payload.user.role, state.canManageApps) === 'manage') loadAppsIndex();
    // Warm the server-info cache so the About dialog paints on first open
    // rather than filling in a beat late. One small request per session.
    loadServerInfo();
    // The router (started by the caller) will mount the view that matches
    // the current URL — do not pre-show apps-view here, it leaks through
    // on direct loads of /users, /audit-log, /apps/<slug>.
  }

  // ── Sidebar identity card + profile modal ──────────────────────────────────

  // renderIdentity paints the sidebar identity card from a session user. null
  // clears it (logged-out / boot). Guarded against a missing card so a partial
  // DOM never throws out of the IIFE and takes the whole dashboard down with it.
  function renderIdentity(user) {
    if (!identityCard) return;
    const m = identityModel(user);
    identityAvatar.textContent = m.initials;
    identityAvatar.style.setProperty('--avatar-hue', m.hue);
    identityName.textContent = m.name;
    identityRole.textContent = m.roleLabel;
    identityCard.setAttribute('aria-label', user ? `Open your profile, ${m.name}` : 'Open your profile');
  }

  function openProfileModal() {
    if (!profileModal || !state.user) return;
    const u = state.user;
    const m = identityModel(u);
    profileAvatar.textContent = m.initials;
    profileAvatar.style.setProperty('--avatar-hue', m.hue);
    profileUsername.textContent = m.name;
    profileRole.textContent = m.roleLabel;
    // When a display name is set, show the underlying @username (normal case, so
    // it never reads like a second role); otherwise the primary line already is
    // the username, so leave this blank.
    profileLogin.textContent = m.secondary ? `@${m.secondary}` : '';

    // SSO accounts are governed by the identity provider: name + password are
    // read-only here (refreshed from the IdP on each login). Local accounts get
    // the full editor. can_set_password is the local-account signal.
    const selfService = u.can_manage_self !== false;
    const isLocalAccount = selfService && !!u.can_set_password;
    setHidden(profileNameField, !isLocalAccount);
    setHidden(profilePwSection, !isLocalAccount);
    setHidden(profilePwManaged, isLocalAccount);
    if (profilePwManaged && !selfService) {
      profilePwManaged.textContent = 'This shared account is read-only. Profile, password, and API-token changes are disabled by the server.';
    } else if (profilePwManaged) {
      profilePwManaged.textContent = 'Your name and password are managed by your identity provider.';
    }
    setHidden(profileTokensLink, !selfService);
    setHidden(profileSubmit, !isLocalAccount);
    profileDisplayName.value = u.display_name || '';
    profileCurrentPw.value = '';
    profileNewPw.value = '';
    setError(profileError, '');
    setHidden(profileSuccess, true);
    reflectThemeRadios();
    profileModal.hidden = false;
    modalTrap(profileModal).activate();
    (isLocalAccount ? profileDisplayName : (profileClose || logoutButton)).focus();
  }

  // reflectThemeRadios checks the radio matching the stored theme preference, so
  // the Appearance control shows the current choice each time the modal opens.
  function reflectThemeRadios() {
    const pref = getThemePreference(window);
    document.querySelectorAll('input[name="theme-pref"]').forEach((el) => {
      el.checked = el.value === pref;
    });
  }

  function closeProfileModal() {
    if (!profileModal || profileModal.hidden) return;
    profileModal.hidden = true;
    modalTrap(profileModal).release();
    setError(profileError, '');
    setHidden(profileSuccess, true);
  }

  // openAboutModal paints from the cached server info when there is one, so a
  // reopen is instant; the first open fills in as soon as the fetch lands.
  function openAboutModal() {
    if (!aboutModal) return;
    // On mobile the drawer and modal live in separate stacking contexts. Close
    // the drawer before presenting About so the dialog is visible immediately
    // and focus never lands behind the navigation plane.
    if (sidebarDrawer) sidebarDrawer.close();
    loadServerInfo().then((info) => renderAbout(document, info));
    aboutModal.hidden = false;
    modalTrap(aboutModal).activate();
    if (aboutClose) aboutClose.focus();
  }

  function closeAboutModal() {
    if (!aboutModal || aboutModal.hidden) return;
    aboutModal.hidden = true;
    modalTrap(aboutModal).release();
  }

  async function submitProfile(event) {
    event.preventDefault();
    if (!state.user) return;
    const canSetPw = state.user.can_manage_self !== false && !!state.user.can_set_password;
    // SSO accounts have nothing to save (name + password are IdP-managed); the
    // Save button is hidden, but guard against an Enter-key submit anyway.
    if (!canSetPw) { closeProfileModal(); return; }
    setError(profileError, '');
    setHidden(profileSuccess, true);

    const body = { display_name: profileDisplayName.value.trim() };
    const wantsPwChange = canSetPw &&
      (profileNewPw.value.length > 0 || profileCurrentPw.value.length > 0);
    if (wantsPwChange) {
      if (profileNewPw.value.length < 15) {
        setError(profileError, 'New password must be at least 15 characters');
        return;
      }
      body.current_password = profileCurrentPw.value;
      body.new_password = profileNewPw.value;
    }

    let resp;
    try {
      resp = await api('/api/auth/me', { method: 'PATCH', body: JSON.stringify(body) });
    } catch {
      setError(profileError, 'Network error');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let msg = 'Failed to save profile';
      try { const j = await resp.json(); if (j && j.error) msg = j.error; } catch {}
      setError(profileError, msg);
      return;
    }
    let data = null;
    try { data = await resp.json(); } catch {}
    if (data && data.user) {
      state.user = data.user;
      renderIdentity(data.user);
    }
    profileCurrentPw.value = '';
    profileNewPw.value = '';
    profileSuccess.textContent = wantsPwChange ? 'Profile and password updated' : 'Profile updated';
    setHidden(profileSuccess, false);
    // If the admin Users table is on screen, refresh it so this user's display
    // name updates there too.
    if (usersView && !usersView.hidden) loadUsers();
  }

  async function handleUnauthorized() {
    // A 401 wipes all client state unconditionally. If the operator had unsaved
    // settings edits in flight, silently discarding them with no explanation
    // looks like data loss; check for dirty edits before the logout clears
    // state, and surface a clear message so they know to redo the edit after
    // logging in.
    const hadUnsavedChanges = anySettingsDirty();
    showLoggedOut();
    setError(loginError, hadUnsavedChanges
      ? 'Your session expired and you were logged out. Unsaved changes were lost - please log in again.'
      : '');
  }

  async function loadFleetHealth() {
    if (!fleetHealthEl) return;
    // Admin-only aggregate; hide for non-admins (the API would 403 anyway).
    if (!state.user || state.user.role !== 'admin') {
      fleetHealthEl.hidden = true;
      return;
    }
    let resp;
    try {
      resp = await api('/api/fleet/health');
    } catch {
      renderFleetHealth(summariseFleetHealth({ complete: false, unavailable_components: ['fleet health request'] }));
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      renderFleetHealth(summariseFleetHealth({ complete: false, unavailable_components: [`fleet health HTTP ${resp.status}`] }));
      return;
    }
    let body;
    try {
      body = await resp.json();
    } catch {
      renderFleetHealth(summariseFleetHealth({ complete: false, unavailable_components: ['invalid fleet health response'] }));
      return;
    }
    renderFleetHealth(summariseFleetHealth(body));
  }

  function renderFleetHealth(s) {
    if (!fleetHealthEl) return;
    fleetHealthEl.textContent = '';
    fleetHealthEl.className = `fleet-health fleet-health-${s.statusClass}`;

    const dot = document.createElement('span');
    dot.className = `badge badge-${s.statusClass}`;
    dot.textContent = s.statusLabel;
    fleetHealthEl.appendChild(dot);

    const head = document.createElement('span');
    head.className = 'fleet-health-headline';
    head.textContent = s.headline;
    fleetHealthEl.appendChild(head);

    // Per-tier trouble chips (operator-controlled tier names → textContent, XSS-safe).
    for (const chip of s.tierChips) {
      const c = document.createElement('span');
      c.className = 'fleet-health-chip';
      const bits = [];
      if (chip.lost > 0) bits.push(`${chip.lost} lost`);
      if (chip.workersDown > 0) bits.push(`${chip.workersDown} worker${chip.workersDown === 1 ? '' : 's'} down`);
      c.textContent = `${chip.tier}: ${bits.join(', ')}`;
      fleetHealthEl.appendChild(c);
    }

    // Surface which apps are degraded (and why) without crowding the banner:
    // a hover tooltip plus the accessible name carry the actionable detail.
    // Name stale schedules and activations needing attention in the
    // tooltip/accessible name. Attribute assignment (not HTML interpolation)
    // keeps operator-controlled slugs, schedule names and errors XSS-safe.
    const staleNames = (s.staleSchedules || []).map((x) =>
      `${x.slug}: ${x.schedule}${x.refreshing ? ' (refreshing)' : ''}`);
    const baseTip = degradedTooltip(s);
    const activationTip = activationAttentionTooltip(s);
    const tipParts = [];
    if (baseTip) tipParts.push(baseTip);
    if (staleNames.length) tipParts.push(`Stale schedules - ${staleNames.join(', ')}`);
    if (activationTip) tipParts.push(`Data activations needing attention - ${activationTip}`);
    const tip = tipParts.join(' | ');
    if (tip) {
      fleetHealthEl.title = tip;
      fleetHealthEl.setAttribute('aria-label', `Fleet health: ${s.statusLabel}. ${tip}`);
    } else {
      fleetHealthEl.removeAttribute('title');
      fleetHealthEl.setAttribute('aria-label', `Fleet health: ${s.statusLabel}`);
    }

    fleetHealthEl.hidden = false;
  }

  async function loadApps() {
    setError(appError, '');

    // Show a loading placeholder only on the first paint (empty grid), so
    // periodic refreshes don't flash over already-rendered cards.
    const showLoading = appGrid.childElementCount === 0;
    if (showLoading) {
      appGrid.setAttribute('aria-busy', 'true');
      appGrid.textContent = '';
      const ph = document.createElement('p');
      ph.className = 'grid-loading';
      ph.setAttribute('role', 'status');
      ph.textContent = 'Loading apps…';
      appGrid.appendChild(ph);
    }
    const clearGridLoading = () => {
      appGrid.removeAttribute('aria-busy');
      const ph = appGrid.querySelector('.grid-loading');
      if (ph) ph.remove();
    };

    let response;
    try {
      response = await api('/api/apps');
    } catch {
      clearGridLoading();
      setError(appError, 'Network error');
      return;
    }

    if (response.status === 401) {
      clearGridLoading();
      await handleUnauthorized();
      return;
    }
    if (!response.ok) {
      clearGridLoading();
      setError(appError, 'Failed to load apps');
      return;
    }

    {
      // Standard {items,...} list envelope; tolerate a bare array for resilience.
      const gridBody = (await response.json()) || [];
      state.apps = Array.isArray(gridBody) ? gridBody : (Array.isArray(gridBody.items) ? gridBody.items : []);
      const visibleSlugs = new Set(state.apps.map((app) => app.slug));
      for (const slug of appRestartFeedback.keys()) {
        if (!visibleSlugs.has(slug)) appRestartFeedback.delete(slug);
      }
    }
    clearGridLoading();
    renderApps();
    syncSidebar();
    metrics.setTargets(state.apps.map(a => a.slug));
    loadFleetHealth();
    populateProjectDatalist();
  }

  // Populates the shared project datalist and caches the full project rows for
  // the edit modal. Failure is silent by design: the datalist is a convenience,
  // and a typed project slug still works without it, so a projects fetch that
  // fails must not block creating or editing an app.
  async function populateProjectDatalist() {
    const list = document.getElementById('project-slugs');
    let projects = [];
    try {
      const resp = await api('/api/projects');
      if (!resp.ok) return;
      const body = (await resp.json()) || {};
      projects = Array.isArray(body.items) ? body.items : [];
    } catch {
      return;
    }
    projectRows.clear();
    for (const p of projects) {
      if (p && p.slug) projectRows.set(p.slug, p);
    }
    if (!list) return;
    list.textContent = '';
    for (const p of projects) {
      const opt = document.createElement('option');
      opt.value = p.slug;
      // The label shows the display name so an operator picking from the
      // dropdown recognizes "Quarterly Reports", not just "qr".
      if (p.name && p.name !== p.slug) opt.label = p.name;
      list.appendChild(opt);
    }
  }

  async function openProjectEditModal(group) {
    const modal = document.getElementById('project-edit-modal');
    // A row may be missing if the modal is opened before the first projects
    // fetch, or if the project was created on another tab. Refetch once.
    if (!projectRows.has(group.project)) await populateProjectDatalist();
    const row = projectRows.get(group.project);
    modal.dataset.slug = group.project;
    document.getElementById('project-edit-slug').textContent = group.project;
    // The heading falls back to the slug when the project is unnamed; showing
    // that fallback in the Name field would silently turn a missing name into a
    // real one on the next save.
    document.getElementById('project-edit-name').value =
      group.name === group.project ? '' : group.name;
    // Only send a description back if we know the current one. A group object
    // never carries a description, so an unconditional send would clear it.
    modal.dataset.descriptionKnown = row ? '1' : '0';
    document.getElementById('project-edit-description').value =
      row ? row.description || '' : '';
    document.getElementById('project-edit-description').disabled = !row;
    document.getElementById('project-edit-icon').value = group.iconEmoji || '';
    modal.hidden = false;
    modalTrap(modal).activate();
    document.getElementById('project-edit-name').focus();
  }

  async function saveProjectEdit() {
    const modal = document.getElementById('project-edit-modal');
    const slug = modal.dataset.slug;
    const body = buildProjectPatchBody({
      name: document.getElementById('project-edit-name').value,
      iconEmoji: document.getElementById('project-edit-icon').value,
      description: document.getElementById('project-edit-description').value,
      descriptionKnown: modal.dataset.descriptionKnown === '1',
    });
    const resp = await api(`/api/projects/${encodeURIComponent(slug)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!resp.ok) {
      setError(appError, 'Could not save the project. Check the name and icon and try again.');
      return;
    }
    modal.hidden = true;
    modalTrap(modal).release();
    // Refetch rather than patching state in place: the grid, the sidebar and
    // the datalist all carry a copy of the display metadata.
    await loadApps();
    populateProjectDatalist();
  }

  // syncSidebar renders the management quick-switch list from the FULL app
  // index (state.apps), never the grid-filtered set, so dashboard search/sort
  // never hides apps from the sidebar. Viewers get one catalog—the Apps gallery—
  // rather than a second, differently behaving copy in the sidebar.
  const sidebarBadge = (a) => appCardBadge(a, formatStatus);
  function isOperatorRole(u) {
    return !!u && (u.role === 'admin' || u.role === 'operator');
  }

  function syncSidebar() {
    const el = document.getElementById('sidebar-apps');
    if (!el) return;
    const role = state.user && state.user.role;
    if (appsSurfaceForSession(role, state.canManageApps) === 'viewer') {
      el.replaceChildren();
      el.hidden = true;
      return;
    }
    el.hidden = false;
    renderSidebarApps(el, state.apps, location.pathname, sidebarBadge, document);
  }

  // loadAppsIndex populates the sidebar app list independently of which view is
  // mounted. showLoggedIn calls it fire-and-forget so the sidebar is filled even
  // on a direct deep link (/users, /apps/<slug>) where the grid never mounts. It
  // deliberately omits loadApps's metrics.setTargets/loadFleetHealth side effects
  // so it never disturbs the active view's metric polling.
  async function loadAppsIndex() {
    // Snapshot the session identity: showLoggedIn assigns a fresh state.user
    // object per login, so a reference change means logout (or a different user)
    // happened during an in-flight request. Bail in that case so a stale
    // response never overwrites the new session's apps or fires a late 401.
    const sessionUser = state.user;
    let response;
    try { response = await api('/api/apps'); }
    catch { return; }
    if (state.user !== sessionUser) return;
    if (response.status === 401) { await handleUnauthorized(); return; }
    if (!response.ok) return;
    // Standard {items,...} list envelope; tolerate a bare array for resilience.
    const idxBody = (await response.json()) || [];
    const apps = Array.isArray(idxBody) ? idxBody : (Array.isArray(idxBody.items) ? idxBody.items : []);
    if (state.user !== sessionUser) return;
    state.apps = apps;
    syncSidebar();
  }

  async function loadWorkers() {
    setError(workersError, '');
    let resp;
    try {
      resp = await api('/api/workers');
    } catch {
      setError(workersError, 'Network error');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (resp.status === 403) { setError(workersError, 'Admin access required'); return; }
    if (!resp.ok) { setError(workersError, 'Failed to load workers'); return; }
    let workers;
    try {
      workers = await resp.json();
    } catch {
      setError(workersError, 'Invalid response from server');
      return;
    }
    renderWorkers(Array.isArray(workers) ? workers : []);
  }

  function renderWorkers(workers) {
    workersBody.textContent = '';
    if (workersEmpty) workersEmpty.hidden = workers.length !== 0;
    if (workersTable) workersTable.hidden = workers.length === 0;
    for (const w of workers) {
      const d = workerDisplay(w);
      const tr = document.createElement('tr');

      const node = document.createElement('td');
      node.textContent = d.node;
      tr.appendChild(node);

      const tier = document.createElement('td');
      tier.textContent = d.tier;
      tr.appendChild(tier);

      const status = document.createElement('td');
      const badge = document.createElement('span');
      badge.className = `badge badge-${d.statusClass}`;
      badge.textContent = d.statusText;
      status.appendChild(badge);
      tr.appendChild(status);

      const version = document.createElement('td');
      version.textContent = d.version;
      tr.appendChild(version);

      const hb = document.createElement('td');
      let hbText = 'never';
      if (w.last_heartbeat) {
        const dt = new Date(w.last_heartbeat);
        hbText = isNaN(dt.getTime()) ? String(w.last_heartbeat) : relativeTime(dt);
      }
      hb.textContent = hbText;
      tr.appendChild(hb);

      workersBody.appendChild(tr);
    }
  }

  async function loadAuditEvents(page, selection = state.auditSelection) {
    const requestID = auditRequests.begin();
    setError(auditError, '');
    state.auditSelection = selection;
    updateAuditContext(selection);
    if (auditLoading) {
      auditLoading.textContent = auditLoadingMessage(selection);
      auditLoading.hidden = false;
    }
    if (auditPagination) auditPagination.hidden = true;
    auditBody.textContent = '';
    if (auditEmpty) auditEmpty.hidden = true;
    const auditTable = document.getElementById('audit-table');
    if (auditTable) {
      auditTable.hidden = false;
      auditTable.setAttribute('aria-busy', 'true');
    }
    let resp;
    try {
      resp = await api(auditListPath(page, selection));
    } catch {
      if (!auditRequests.isCurrent(requestID)) return;
      if (auditLoading) auditLoading.hidden = true;
      if (auditTable) { auditTable.removeAttribute('aria-busy'); auditTable.hidden = true; }
      setError(auditError, auditLoadError(selection));
      return;
    }
    if (!auditRequests.isCurrent(requestID)) return;
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      if (auditLoading) auditLoading.hidden = true;
      if (auditTable) { auditTable.removeAttribute('aria-busy'); auditTable.hidden = true; }
      setError(auditError, auditLoadError(selection));
      return;
    }

    let body;
    try {
      body = await resp.json();
    } catch {
      if (!auditRequests.isCurrent(requestID)) return;
      if (auditLoading) auditLoading.hidden = true;
      if (auditTable) { auditTable.removeAttribute('aria-busy'); auditTable.hidden = true; }
      setError(auditError, auditLoadError(selection));
      return;
    }
    if (!auditRequests.isCurrent(requestID)) return;
    // Server returns {events, total, has_more}; legacy callers may still get
    // a bare array if anything reverts the API, so be defensive.
    const events = Array.isArray(body) ? body : (body.events || []);
    state.auditHasMore = Array.isArray(body)
      ? events.length === 100
      : !!body.has_more;
    state.auditTotal = Array.isArray(body) ? null : (body.total ?? null);
    state.auditPage = page;
    renderAuditEvents(events);
  }

  function renderAuditEvents(events) {
    if (auditLoading) auditLoading.hidden = true;
    auditBody.textContent = '';
    const noEvents = events.length === 0;
    if (auditEmpty) {
      auditEmpty.textContent = auditEmptyMessage(state.auditSelection);
      auditEmpty.hidden = !noEvents;
    }
    const auditTable = document.getElementById('audit-table');
    if (auditTable) {
      auditTable.removeAttribute('aria-busy');
      auditTable.hidden = noEvents;
    }

    const knownActions = [
      // Deployment actions (green)
      'deploy', 'restart', 'rollback', 'fleet_apply_started', 'fleet_apply_finished',
      // Auth actions
      'login', 'login_failed', 'logout',
      // App lifecycle (blue - config)
      'create_app', 'update_app', 'delete_app', 'stop', 'sleep', 'set_access',
      // User management (blue - config)
      'create_user', 'update_user', 'delete_user', 'reset_user_password',
      // Token management (amber - security)
      'create_token', 'delete_token',
      // Environment (blue - config)
      'env.set', 'env.delete',
      // Data (blue - config)
      'data.push', 'data.delete',
      // Schedules (blue - config)
			'schedule_create', 'schedule_update', 'schedule_delete', 'schedule_run_manual',
			'schedule_run_succeeded', 'schedule_run_failed', 'schedule_run_timed_out',
			'schedule_run_cancelled', 'schedule_run_interrupted',
			'schedule_activation_roll', 'schedule_activation_outcome',
      // Access management (amber - security)
      'grant_access', 'revoke_access',
      // Group-access management (amber - security)
      'grant_group_access', 'revoke_group_access', 'reconcile_group_access',
      // Shared data (blue - config)
      'shared_data_grant', 'shared_data_revoke',
      // Autoscale scale events (blue - config)
      'autoscale_scale_up', 'autoscale_scale_down',
      // Deploy quota rejection (red)
      'deploy_rejected_quota',
    ];

    for (const e of events) {
      const tr = document.createElement('tr');
      tr.id = `audit-event-${e.id}`;
      const selectedEvent = state.auditSelection.event === String(e.id);
      if (selectedEvent) {
        tr.className = 'audit-event-selected';
        tr.tabIndex = -1;
        tr.setAttribute('aria-label', `Selected audit event ${e.id}`);
      }

      // Time
      const timeCell = document.createElement('td');
      const ts = new Date(e.created_at);
      timeCell.textContent = relativeTime(ts);
      timeCell.title = ts.toISOString();
      tr.appendChild(timeCell);

      // User
      const userCell = document.createElement('td');
      userCell.textContent = e.username || '—';
      tr.appendChild(userCell);

      // Action badge
      const actionCell = document.createElement('td');
      const badge = document.createElement('span');
      // Replace dots with hyphens so the class name is valid CSS and
      // matches the stylesheet's .badge-action-env-set etc. selectors.
      const actionClass = knownActions.includes(e.action)
        ? `badge-action-${e.action.replace(/\./g, '-')}`
        : 'badge-action-default';
      badge.className = `badge ${actionClass}`;
      badge.textContent = e.action;
      actionCell.appendChild(badge);
      tr.appendChild(actionCell);

      // Resource
      const resourceCell = document.createElement('td');
      const parts = [e.resource_type, e.resource_id].filter(Boolean);
      resourceCell.textContent = parts.length ? parts.join(' ') : '—';
      tr.appendChild(resourceCell);

			// Action-specific durable attribution. Only allowlisted fields become
			// text nodes; arbitrary audit JSON is never interpreted as markup.
			const detailCell = document.createElement('td');
			const detailEntries = auditDetailEntries(e);
			if (detailEntries.length === 0) {
				detailCell.textContent = '—';
			} else {
				const details = document.createElement('details');
				details.className = 'audit-details';
				const summary = document.createElement('summary');
				summary.textContent = 'View outcome';
				const facts = document.createElement('dl');
				for (const entry of detailEntries) {
					const term = document.createElement('dt');
					term.textContent = entry.label;
					const value = document.createElement('dd');
					value.textContent = entry.value;
					facts.append(term, value);
				}
				details.append(summary, facts);
				detailCell.appendChild(details);
			}
			tr.appendChild(detailCell);

      // IP
      const ipCell = document.createElement('td');
      ipCell.textContent = e.ip_address || '—';
      tr.appendChild(ipCell);

      auditBody.appendChild(tr);
    }

    const selection = state.auditSelection;
    updateAuditContext(selection);
    if (auditPagination) auditPagination.hidden = !!selection.event;

    const start = state.auditPage * 100 + 1;
    const end   = state.auditPage * 100 + events.length;
    if (events.length === 0) {
      auditRange.textContent = 'No events';
    } else if (state.auditTotal != null) {
      auditRange.textContent = `Showing ${start}–${end} of ${state.auditTotal}`;
    } else {
      auditRange.textContent = `Showing ${start}–${end}`;
    }
    auditPrev.disabled = state.auditPage === 0;
    auditNext.disabled = !state.auditHasMore;
    const selectedRow = state.auditSelection.event
      ? document.getElementById(`audit-event-${state.auditSelection.event}`)
      : null;
    if (selectedRow) selectedRow.focus({ preventScroll: false });
  }

  function updateAuditContext(selection) {
    const filtered = !!(selection.event || selection.run || selection.action);
    if (auditContext && auditContextCopy) {
      auditContext.hidden = !filtered;
      if (selection.event) auditContextCopy.textContent = `Showing audit event ${selection.event}.`;
      else if (selection.run) auditContextCopy.textContent = `Showing events from run ${selection.run}.`;
      else if (selection.action) auditContextCopy.textContent = `Showing ${selection.action} events.`;
      else auditContextCopy.textContent = '';
    }
  }

  async function loadUsers() {
    setError(usersError, '');

    const showLoading = usersBody.childElementCount === 0;
    if (showLoading) {
      usersBody.setAttribute('aria-busy', 'true');
      const tr = document.createElement('tr');
      const td = document.createElement('td');
      td.colSpan = 4;
      td.className = 'grid-loading';
      td.setAttribute('role', 'status');
      td.textContent = 'Loading users…';
      tr.appendChild(td);
      usersBody.appendChild(tr);
    }
    const clearUsersLoading = () => usersBody.removeAttribute('aria-busy');

    let resp;
    try {
      resp = await api('/api/users');
    } catch {
      clearUsersLoading();
      usersBody.textContent = '';
      setError(usersError, 'Network error');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (resp.status === 403) { clearUsersLoading(); usersBody.textContent = ''; setError(usersError, 'Admin only'); return; }
    if (!resp.ok) { clearUsersLoading(); usersBody.textContent = ''; setError(usersError, 'Failed to load users'); return; }
    let users = [];
    try {
      const uBody = (await resp.json()) || [];
      // Standard {items,...} list envelope; tolerate a bare array for resilience.
      users = Array.isArray(uBody) ? uBody : (Array.isArray(uBody.items) ? uBody.items : []);
    }
    catch { clearUsersLoading(); usersBody.textContent = ''; setError(usersError, 'Invalid response'); return; }
    clearUsersLoading();
    renderUsers(users.filter(u => u.principal_type !== 'service_account'));
  }

  function renderUsers(users) {
    usersBody.textContent = '';
    const selfId = state.user ? state.user.id : null;

    for (const u of users) {
      const tr = document.createElement('tr');
      const caps = userRowCaps(u, selfId);

      // Username
      const nameCell = document.createElement('td');
      nameCell.className = 'users-username';
      const usernameText = document.createElement('span');
      usernameText.textContent = u.username;
      nameCell.appendChild(usernameText);
      if (caps.isSelf) {
        const tag = document.createElement('span');
        tag.className = 'users-self-tag';
        tag.textContent = 'you';
        nameCell.appendChild(tag);
      }
      if (caps.reserved) {
        const tag = document.createElement('span');
        tag.className = 'users-reserved-tag';
        tag.textContent = 'token';
        tag.title = RESERVED_USER_HINT;
        nameCell.appendChild(tag);
      }
      nameCell.appendChild(makeCopyButton(u.username, `Copy username ${u.username}`));
      // Friendly display name (when set) as a subtitle under the username.
      if (u.display_name) {
        const dn = document.createElement('span');
        dn.className = 'users-display-name';
        dn.textContent = u.display_name;
        nameCell.appendChild(dn);
      }
      tr.appendChild(nameCell);

      // Role (editable select; disabled for self)
      const roleCell = document.createElement('td');
      const select = document.createElement('select');
      select.className = 'users-row-role';
      // "(SSO-managed)" clears any manual override (sends PATCH {role:""}),
      // returning the user to group/default governance. Selected as the
      // fallback when the user's role is not an explicit manual option.
      const ssoOpt = document.createElement('option');
      ssoOpt.value = '';
      ssoOpt.textContent = '(SSO-managed)';
      const explicitRoles = ['developer', 'operator', 'admin'];
      if (!explicitRoles.includes(u.role)) ssoOpt.selected = true;
      select.appendChild(ssoOpt);
      for (const r of ['developer', 'operator', 'admin']) {
        const opt = document.createElement('option');
        opt.value = r;
        opt.textContent = r.charAt(0).toUpperCase() + r.slice(1);
        if (u.role === r) opt.selected = true;
        select.appendChild(opt);
      }
      if (!caps.canChangeRole) {
        select.disabled = true;
        if (caps.roleHint) select.title = caps.roleHint;
      } else {
        select.addEventListener('change', () => updateUserRole(u.id, u.username, select));
      }
      roleCell.appendChild(select);
      tr.appendChild(roleCell);

      // Created
      const createdCell = document.createElement('td');
      createdCell.className = 'users-created';
      const ts = new Date(u.created_at);
      createdCell.textContent = relativeTime(ts);
      createdCell.title = ts.toISOString();
      tr.appendChild(createdCell);

      // Actions
      const actionsCell = document.createElement('td');
      const actions = document.createElement('div');
      actions.className = 'users-row-actions';

      const resetBtn = document.createElement('button');
      resetBtn.type = 'button';
      resetBtn.className = 'btn-row';
      resetBtn.textContent = 'Reset password';
      resetBtn.setAttribute('aria-label', `Reset password for ${u.username}`);
      if (!caps.canResetPassword) {
        resetBtn.disabled = true;
        resetBtn.title = RESERVED_USER_HINT;
      } else {
        resetBtn.addEventListener('click', () => openResetPasswordModal(u));
      }
      actions.appendChild(resetBtn);

      const delBtn = document.createElement('button');
      delBtn.type = 'button';
      delBtn.className = 'btn-row btn-row-danger';
      delBtn.textContent = 'Delete';
      delBtn.setAttribute('aria-label', `Delete user ${u.username}`);
      if (!caps.canDelete) {
        delBtn.disabled = true;
        if (caps.deleteHint) delBtn.title = caps.deleteHint;
      } else {
        delBtn.addEventListener('click', () => deleteUser(u.id, u.username, delBtn));
      }
      actions.appendChild(delBtn);

      actionsCell.appendChild(actions);
      tr.appendChild(actionsCell);

      usersBody.appendChild(tr);
    }
  }

  function showIdentityPanel(kind) {
    const services = kind === 'services';
    identityPeoplePanel.hidden = services;
    identityServicesPanel.hidden = !services;
    identityPeopleTab.setAttribute('aria-selected', String(!services));
    identityServicesTab.setAttribute('aria-selected', String(services));
	identityPeopleTab.tabIndex = services ? -1 : 0;
	identityServicesTab.tabIndex = services ? 0 : -1;
    newUserButton.hidden = services;
    if (services) loadServiceAccounts();
  }

  async function loadServiceAccounts() {
    if (!serviceAccountsList) return;
    setError(serviceAccountsError, '');
    const hadInventory = Boolean(serviceAccountsList.querySelector('.service-account-card'));
    serviceAccountsList.setAttribute('aria-busy', 'true');
    serviceAccountsStatus.textContent = 'Loading service accounts.';
    if (!serviceAccountsList.childElementCount) {
      serviceAccountsList.innerHTML = '<p class="grid-loading" role="status">Loading service accounts…</p>';
    }
    let accountsResp;
    try {
      accountsResp = await api('/api/service-accounts');
    } catch {
      serviceAccountsList.removeAttribute('aria-busy');
      if (!hadInventory) serviceAccountsList.textContent = '';
      setError(serviceAccountsError, 'Service accounts could not be loaded. Check your connection and try again.');
      serviceAccountsStatus.textContent = 'Service accounts could not be loaded.';
      return;
    }
    if (accountsResp.status === 401) { await handleUnauthorized(); return; }
    if (!accountsResp.ok) {
      serviceAccountsList.removeAttribute('aria-busy');
      if (!hadInventory) serviceAccountsList.textContent = '';
      setError(serviceAccountsError, accountsResp.status === 403 ? 'Human administrators manage service accounts.' : 'Service accounts could not be loaded. Try again.');
      serviceAccountsStatus.textContent = 'Service accounts could not be loaded.';
      return;
    }
    const accountBody = await accountsResp.json();
    const accounts = Array.isArray(accountBody?.items) ? accountBody.items : [];
    const hydrated = await Promise.all(accounts.map(async account => {
      try {
        const response = await api(`/api/service-accounts/${encodeURIComponent(account.key)}/credentials`);
        if (!response.ok) return {...account, credentials: [], credentialsError: true};
        const body = await response.json();
        return {...account, credentials: Array.isArray(body?.items) ? body.items : []};
      } catch { return {...account, credentials: [], credentialsError: true}; }
    }));
    renderServiceAccounts(hydrated);
    serviceAccountsList.removeAttribute('aria-busy');
    serviceAccountsStatus.textContent = hydrated.some(account => account.credentialsError)
      ? 'Some credentials could not be loaded.'
      : 'Service accounts loaded.';
  }

  function serviceCredentialScopeMarkup(credential) {
    if (credential.unrestricted) return '<span class="service-scope-all">All apps</span>';
    const apps = Array.isArray(credential.apps) ? credential.apps : [];
    if (!apps.length) return 'No apps';
    const visible = apps.slice(0, 3).map(escapeHtml).join(', ');
    const remaining = apps.slice(3);
    const overflow = remaining.length
      ? ` <details class="service-scope-more"><summary aria-label="Show ${remaining.length} more app ${remaining.length === 1 ? 'scope' : 'scopes'}">+${remaining.length} more</summary><span>${remaining.map(escapeHtml).join(', ')}</span></details>`
      : '';
    return visible + overflow;
  }

  function serviceCredentialTimeMarkup(value, kind) {
    if (!value) return '<span class="service-time-state">Never</span>';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return '—';
    const exact = new Intl.DateTimeFormat(undefined, {dateStyle: 'medium', timeStyle: 'short'}).format(date);
    const expired = kind === 'expiry' && date.getTime() <= Date.now();
    const state = expired ? 'Expired' : relativeTime(date);
    const stateClass = expired ? ' service-time-state--expired' : '';
    return `<span class="service-time-state${stateClass}">${escapeHtml(state)}</span><time datetime="${escapeHtml(date.toISOString())}">${escapeHtml(exact)}</time>`;
  }

  function renderServiceAccounts(accounts) {
    if (!accounts.length) {
      serviceAccountsList.innerHTML = '<div class="schedules-empty"><h3>No service accounts available</h3><p>The platform-managed deployment account is created when the server starts.</p></div>';
      return;
    }
    serviceAccountsList.innerHTML = accounts.map(account => {
      const tableKey = String(account.key || 'account').replace(/[^a-zA-Z0-9_-]/g, '-');
      const headers = {
        credential: `service-${tableKey}-credential`, role: `service-${tableKey}-role`, apps: `service-${tableKey}-apps`,
        expires: `service-${tableKey}-expires`, used: `service-${tableKey}-used`, action: `service-${tableKey}-action`,
      };
      const credentials = Array.isArray(account.credentials) ? account.credentials : [];
      const rows = credentials.map(credential => {
        const apps = Array.isArray(credential.apps) ? credential.apps : [];
        const expired = credential.expires_at && new Date(credential.expires_at).getTime() <= Date.now();
        const searchText = [credential.name, credential.role, ...apps, credential.unrestricted ? 'all apps' : '', credential.managed_by ? 'platform managed' : ''].join(' ').toLowerCase();
        const actions = credential.managed_by
          ? '<span class="service-managed-note">Change server configuration to revoke</span>'
          : `<div class="service-row-actions"><button type="button" class="btn-row" data-service-rotate="${credential.id}" data-service-key="${escapeHtml(account.key)}" data-service-name="${escapeHtml(credential.name)}" data-service-role="${escapeHtml(credential.role || 'developer')}" data-service-apps="${escapeHtml(JSON.stringify(apps))}" data-service-unrestricted="${credential.unrestricted ? 'true' : 'false'}">Rotate</button><button type="button" class="btn-row btn-row-danger" data-service-revoke="${credential.id}" data-service-key="${escapeHtml(account.key)}" data-service-name="${escapeHtml(credential.name)}">Revoke</button></div><span class="service-revoke-error" role="alert" hidden></span>`;
        return `<tr role="row" data-service-credential-row data-service-search="${escapeHtml(searchText)}" data-service-state="${expired ? 'expired' : 'active'}">
        <td role="cell" headers="${headers.credential}" data-label="Credential"><strong>${escapeHtml(credential.name)}</strong>${credential.managed_by ? `<span class="service-managed-tag">Platform-managed</span>` : ''}</td>
        <td role="cell" headers="${headers.role}" data-label="Role">${escapeHtml((credential.role || 'developer').replace(/^./, value => value.toUpperCase()))}</td>
        <td role="cell" headers="${headers.apps}" data-label="Apps">${serviceCredentialScopeMarkup(credential)}</td>
        <td role="cell" headers="${headers.expires}" data-label="Expires" class="service-time-cell">${serviceCredentialTimeMarkup(credential.expires_at, 'expiry')}</td>
        <td role="cell" headers="${headers.used}" data-label="Last used" class="service-time-cell">${serviceCredentialTimeMarkup(credential.last_used_at, 'used')}</td>
        <td role="cell" headers="${headers.action}" data-label="Action">${actions}</td>
      </tr>`;
      }).join('');
      const body = account.credentialsError
        ? '<tr role="row"><td role="cell" colspan="6" class="service-credentials-load-error"><strong>Credentials could not be loaded.</strong><button type="button" class="btn-row" data-service-retry>Retry</button></td></tr>'
        : rows || '<tr role="row"><td role="cell" colspan="6" class="env-empty">No credentials. Create one for each team or pipeline.</td></tr>';
      const tools = credentials.length >= 5 && !account.credentialsError
        ? `<div class="service-credential-tools"><label><span class="sr-only">Search credentials</span><input type="search" placeholder="Search credentials" data-service-search-input></label><label><span class="sr-only">Filter credential status</span><select data-service-state-filter><option value="all">All statuses</option><option value="active">Active</option><option value="expired">Expired</option></select></label><span class="service-filter-status" role="status" data-service-filter-status>${credentials.length} credentials</span></div>`
        : '';
      return `<article class="service-account-card">
        <header><div><h3>${escapeHtml(account.name)}</h3><p>Compatibility username <code>${escapeHtml(account.username)}</code> · Platform-managed</p></div><button type="button" class="btn-primary" data-service-create="${escapeHtml(account.key)}">New credential</button></header>
        ${tools}
        <div class="service-credential-table"><table role="table"><thead role="rowgroup"><tr role="row"><th role="columnheader" scope="col" id="${headers.credential}">Credential</th><th role="columnheader" scope="col" id="${headers.role}">Role</th><th role="columnheader" scope="col" id="${headers.apps}">Apps</th><th role="columnheader" scope="col" id="${headers.expires}">Expires</th><th role="columnheader" scope="col" id="${headers.used}">Last used</th><th role="columnheader" scope="col" id="${headers.action}"><span class="sr-only">Action</span></th></tr></thead><tbody role="rowgroup">${body}</tbody></table></div>
      </article>`;
    }).join('');
  }

  function updateServiceCredentialPlan() {
    const role = serviceCredentialRole.value || 'developer';
    const scope = serviceCredentialForm.querySelector('input[name="service-scope"]:checked')?.value || 'apps';
    const days = Number(serviceCredentialExpiry.value) || 90;
    const roleCopy = {
      viewer: 'Can inspect apps inside this credential’s scope, but cannot change or deploy them.',
      developer: 'Can deploy and manage apps inside this credential’s scope.',
      operator: 'Can manage scoped apps. Project catalog changes and the global audit log require All apps.',
      admin: 'Can administer people and server settings platform-wide. App scope limits app-specific operations only.',
    };
    serviceCredentialRoleHelp.textContent = roleCopy[role] || roleCopy.developer;
    const warning = role === 'admin'
      ? 'Admin authority over people and server settings is platform-wide, even with Selected apps. App scope limits app-specific operations; project catalog changes and global audit require All apps.'
      : '';
    serviceCredentialRoleWarning.textContent = warning;
    serviceCredentialRoleWarning.hidden = !warning;
    const allApps = scope === 'all';
    serviceCredentialAppList.hidden = allApps;
    serviceCredentialApps.disabled = allApps;
    const appPlan = `App operations: ${allApps ? 'all apps' : 'selected apps'}`;
    const platformPlan = role === 'admin' ? 'Platform administration: global' : '';
    serviceCredentialPlan.textContent = [role.replace(/^./, value => value.toUpperCase()), appPlan, platformPlan, `Expires after ${days} days`].filter(Boolean).join(' · ');
    serviceCredentialPlan.classList.toggle('credential-plan--elevated', role === 'admin' && allApps);
  }

  function openServiceCredentialModal(key, rotation = null) {
    serviceCredentialAccountKey = key;
    serviceCredentialRotation = rotation;
    serviceCredentialForm.hidden = false;
    serviceCredentialSecret.hidden = true;
    serviceCredentialForm.reset();
    serviceCredentialRole.value = 'developer';
    serviceCredentialExpiry.value = '90';
    serviceCredentialName.removeAttribute('aria-invalid');
    serviceCredentialName.removeAttribute('aria-describedby');
    serviceCredentialName.removeAttribute('aria-errormessage');
    serviceCredentialApps.removeAttribute('aria-invalid');
    serviceCredentialHeading.textContent = rotation ? 'Rotate automation credential' : 'New automation credential';
    serviceCredentialContext.textContent = rotation ? `Create a replacement for “${rotation.name}”. The old credential stays active until you revoke it.` : '';
    serviceCredentialContext.hidden = !rotation;
    if (rotation) {
      const suffix = ` replacement ${new Date().toISOString().slice(0, 10)}`;
      serviceCredentialName.value = `${rotation.name.slice(0, Math.max(1, 64 - suffix.length))}${suffix}`;
      serviceCredentialRole.value = rotation.role || 'developer';
      const allRadio = serviceCredentialForm.querySelector('input[name="service-scope"][value="all"]');
      const appsRadio = serviceCredentialForm.querySelector('input[name="service-scope"][value="apps"]');
      allRadio.checked = Boolean(rotation.unrestricted);
      appsRadio.checked = !rotation.unrestricted;
      serviceCredentialApps.value = Array.isArray(rotation.apps) ? rotation.apps.join(', ') : '';
    }
    serviceCredentialSecretRevealed = false;
    serviceCredentialCreatedSummary.textContent = '';
    serviceCredentialCreatedWarning.textContent = '';
    serviceCredentialCreatedWarning.hidden = true;
    setError(serviceCredentialError, '');
    updateServiceCredentialPlan();
    serviceCredentialModal.hidden = false;
    serviceCredentialModal.querySelector('.service-credential-modal-box').scrollTop = 0;
    modalTrap(serviceCredentialModal).activate();
    serviceCredentialName.focus();
  }

  function closeServiceCredentialModal({acknowledged = false} = {}) {
    if (serviceCredentialSecretRevealed && !acknowledged &&
        !confirm('This credential cannot be shown again. Close only after you have stored it securely.')) {
      return false;
    }
    const completedRotation = acknowledged && serviceCredentialSecretRevealed && serviceCredentialRotation;
    modalTrap(serviceCredentialModal).release();
    serviceCredentialModal.hidden = true;
    serviceCredentialToken.textContent = '';
    serviceCredentialSecretRevealed = false;
    serviceCredentialRotation = null;
    if (completedRotation) flashToast(`Replacement created. Update pipelines, then revoke “${completedRotation.name}”.`, 'success');
    return true;
  }

  async function submitServiceCredential(event) {
    event.preventDefault();
    const scope = serviceCredentialForm.querySelector('input[name="service-scope"]:checked')?.value;
    const apps = serviceCredentialApps.value.split(',').map(value => value.trim()).filter(Boolean);
    const credentialName = serviceCredentialName.value.trim();
    serviceCredentialName.removeAttribute('aria-invalid');
    serviceCredentialName.removeAttribute('aria-describedby');
    serviceCredentialName.removeAttribute('aria-errormessage');
    serviceCredentialApps.removeAttribute('aria-invalid');
    if (credentialName.toUpperCase() === 'SHINYHUB_DEPLOY_TOKEN') {
      setError(serviceCredentialError, 'That name is reserved for the credential managed by server configuration.');
      serviceCredentialName.setAttribute('aria-invalid', 'true');
      serviceCredentialName.setAttribute('aria-describedby', 'service-credential-error');
      serviceCredentialName.setAttribute('aria-errormessage', 'service-credential-error');
      serviceCredentialName.focus();
      return;
    }
    if (scope === 'apps' && !apps.length) {
      setError(serviceCredentialError, 'Add at least one app, or choose All apps.');
      serviceCredentialApps.setAttribute('aria-invalid', 'true');
      serviceCredentialApps.focus();
      return;
    }
    const invalid = apps.find(value => !SLUG_RE.test(value));
    if (scope === 'apps' && invalid) {
      setError(serviceCredentialError, `“${invalid}” is not a valid app slug. Use lowercase letters, numbers, and internal hyphens.`);
      serviceCredentialApps.setAttribute('aria-invalid', 'true');
      serviceCredentialApps.focus();
      return;
    }
    const duplicate = apps.find((value, index) => apps.indexOf(value) !== index);
    if (scope === 'apps' && duplicate) {
      setError(serviceCredentialError, `Remove the duplicate app slug “${duplicate}”.`);
      serviceCredentialApps.setAttribute('aria-invalid', 'true');
      serviceCredentialApps.focus();
      return;
    }
    const submit = document.getElementById('service-credential-submit');
    submit.disabled = true;
    submit.textContent = 'Creating…';
    serviceCredentialForm.setAttribute('aria-busy', 'true');
    let response;
    try {
      response = await api(`/api/service-accounts/${encodeURIComponent(serviceCredentialAccountKey)}/credentials`, {method: 'POST', body: JSON.stringify({
        name: credentialName, role: serviceCredentialRole.value,
        apps: scope === 'apps' ? apps : [], unrestricted: scope === 'all',
        expires_in_days: Number(serviceCredentialExpiry.value),
      })});
    } catch {
      setError(serviceCredentialError, 'Network error. The credential was not created.');
      submit.disabled = false;
      submit.textContent = 'Create credential';
      serviceCredentialForm.removeAttribute('aria-busy');
      return;
    }
    submit.disabled = false;
    submit.textContent = 'Create credential';
    serviceCredentialForm.removeAttribute('aria-busy');
    if (!response.ok) {
      const body = await response.json().catch(() => ({}));
      setError(serviceCredentialError, body.error || 'Credential could not be created.');
      return;
    }
    const credential = await response.json();
    serviceCredentialToken.textContent = credential.token;
    const scopeSummary = credential.unrestricted ? 'All apps' : (credential.apps || []).join(', ');
    const expirySummary = credential.expires_at
      ? new Intl.DateTimeFormat(undefined, {dateStyle: 'medium', timeStyle: 'short'}).format(new Date(credential.expires_at))
      : 'No expiry';
    const rotationSummary = serviceCredentialRotation ? `Replacement for ${serviceCredentialRotation.name} · ` : '';
    serviceCredentialCreatedSummary.textContent = `${rotationSummary}${(credential.role || 'developer').replace(/^./, value => value.toUpperCase())} · ${scopeSummary} · Expires ${expirySummary}`;
    serviceCredentialCreatedWarning.textContent = [credential.warning, serviceCredentialRotation ? 'The old credential is still active. Update pipelines to use this replacement, then revoke the old credential from the inventory.' : ''].filter(Boolean).join(' ');
    serviceCredentialCreatedWarning.hidden = !serviceCredentialCreatedWarning.textContent;
    serviceCredentialForm.hidden = true;
    serviceCredentialSecret.hidden = false;
    serviceCredentialSecretRevealed = true;
    document.getElementById('service-credential-copy').focus();
    await loadServiceAccounts();
    serviceAccountsStatus.textContent = `Credential ${credential.name || ''} created. Store the secret now.`;
  }

  async function revokeServiceCredential(key, id, name, button) {
    if (!confirm(`Revoke automation credential "${name}"? Pipelines using it will stop working.`)) return;
    button.disabled = true;
    button.setAttribute('aria-busy', 'true');
    button.textContent = 'Revoking…';
    const error = button.closest('td')?.querySelector('.service-revoke-error');
    if (error) { error.hidden = true; error.textContent = ''; }
    const response = await api(`/api/service-accounts/${encodeURIComponent(key)}/credentials/${id}`, {method: 'DELETE'}).catch(() => null);
    if (!response || !response.ok) {
      button.disabled = false;
      button.removeAttribute('aria-busy');
      button.textContent = 'Retry revoke';
      if (error) { error.textContent = 'Could not revoke. Check your connection, then retry.'; error.hidden = false; }
      else setError(serviceAccountsError, 'Credential could not be revoked. Check your connection, then retry.');
      return;
    }
    flashToast(`Credential ${name} revoked.`, 'success');
    await loadServiceAccounts();
    serviceAccountsStatus.textContent = `Credential ${name} revoked.`;
  }

  function applyServiceCredentialFilters(control) {
    const card = control.closest('.service-account-card');
    if (!card) return;
    const query = (card.querySelector('[data-service-search-input]')?.value || '').trim().toLowerCase();
    const stateFilter = card.querySelector('[data-service-state-filter]')?.value || 'all';
    let shown = 0;
    const rows = [...card.querySelectorAll('[data-service-credential-row]')];
    for (const row of rows) {
      const matchesQuery = !query || (row.dataset.serviceSearch || '').includes(query);
      const matchesState = stateFilter === 'all' || row.dataset.serviceState === stateFilter;
      row.hidden = !(matchesQuery && matchesState);
      if (!row.hidden) shown++;
    }
    const status = card.querySelector('[data-service-filter-status]');
    if (status) status.textContent = `${shown} of ${rows.length} credentials shown`;
  }

  async function updateUserRole(id, username, selectEl) {
    const newRole = selectEl.value;
    const previous = selectEl.dataset.previous || '';
    selectEl.disabled = true;
    let resp;
    try {
      resp = await api(`/api/users/${id}`, {
        method: 'PATCH',
        body: JSON.stringify({role: newRole}),
      });
    } catch {
      setError(usersError, 'Network error');
      selectEl.disabled = false;
      return;
    }
    selectEl.disabled = false;
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      setError(usersError, `Failed to update role for ${username}`);
      if (previous) selectEl.value = previous;
      return;
    }
    setError(usersError, '');
    loadUsers();
  }

  async function deleteUser(id, username, btn) {
    if (!confirm(`Delete user "${username}"? This cannot be undone.`)) return;
    if (btn) btn.disabled = true;
    let resp;
    try {
      resp = await api(`/api/users/${id}`, {method: 'DELETE'});
    } catch {
      setError(usersError, 'Network error');
      return;
    } finally {
      if (btn) btn.disabled = false;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) { setError(usersError, `Failed to delete ${username}`); return; }
    setError(usersError, '');
    loadUsers();
  }

  function openResetPasswordModal(user) {
    state.resetPwTargetId = user.id;
    state.resetPwTargetUsername = user.username;
    resetPwUsername.textContent = user.username;
    resetPwInput.value = '';
    setError(resetPwError, '');
    resetPwModal.hidden = false;
    modalTrap(resetPwModal).activate();
    resetPwInput.focus();
  }

  function closeResetPasswordModal() {
    resetPwModal.hidden = true;
    modalTrap(resetPwModal).release();
    state.resetPwTargetId = null;
    state.resetPwTargetUsername = '';
    resetPwInput.value = '';
    setError(resetPwError, '');
  }

  async function submitResetPassword(event) {
    event.preventDefault();
    const id = state.resetPwTargetId;
    if (!id) return;
    const password = resetPwInput.value;
    if (password.length < 15) {
      setError(resetPwError, 'Password must be at least 15 characters');
      return;
    }
    let resp;
    try {
      resp = await api(`/api/users/${id}/password`, {
        method: 'PATCH',
        body: JSON.stringify({password}),
      });
    } catch {
      setError(resetPwError, 'Network error');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let msg = 'Failed to reset password';
      try { const j = await resp.json(); if (j && j.error) msg = j.error; } catch {}
      setError(resetPwError, msg);
      return;
    }
    closeResetPasswordModal();
  }

  function renderNewUserSnippet() {
    if (!newUserSnippet) return;
    const origin = window.location.origin;
    const username = newUserUsername.value.trim() || '<username>';
    newUserSnippet.textContent =
      `shinyhub login --host ${origin} --username ${username}`;
  }

  function openNewUserModal() {
    newUserForm.reset();
    setError(newUserError, '');
    renderNewUserSnippet();
    newUserModal.hidden = false;
    modalTrap(newUserModal).activate();
    newUserUsername.focus();
  }

  function closeNewUserModal() {
    newUserModal.hidden = true;
    modalTrap(newUserModal).release();
    newUserForm.reset();
    setError(newUserError, '');
  }

  async function submitNewUser(event) {
    event.preventDefault();
    const username = newUserUsername.value.trim();
    const password = newUserPassword.value;
    const role     = newUserRole.value;
    if (!username || password.length < 15) {
      setError(newUserError, 'Username and 15+ char password are required');
      return;
    }
    const submitBtn = newUserForm.querySelector('button[type="submit"]');
    if (submitBtn) submitBtn.disabled = true;
    let resp;
    try {
      resp = await api('/api/users', {
        method: 'POST',
        body: JSON.stringify({username, password, role}),
      });
    } catch {
      setError(newUserError, 'Network error');
      return;
    } finally {
      if (submitBtn) submitBtn.disabled = false;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let msg = 'Failed to create user';
      try { const j = await resp.json(); if (j && j.error) msg = j.error; } catch {}
      setError(newUserError, msg);
      return;
    }
    closeNewUserModal();
    loadUsers();
  }

  // ── API tokens (self-service, /tokens page) ────────────────────────────────
  async function loadTokens() {
    if (!tokensList) return;
    const selfService = !state.user || state.user.can_manage_self !== false;
    if (newTokenButton) newTokenButton.hidden = !selfService;
    setError(tokensError, '');
    let resp;
    try {
      resp = await api('/api/tokens');
    } catch {
      setError(tokensError, 'Network error loading tokens');
      renderTokenList(tokensList, [], document, {mutable: selfService});
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      setError(tokensError, 'Failed to load tokens');
      renderTokenList(tokensList, [], document, {mutable: selfService});
      return;
    }
    let body = [];
    try { body = await resp.json(); } catch {}
    // The server returns the standard {items,...} list envelope; tolerate a bare
    // array for resilience across versions.
    const tokens = Array.isArray(body) ? body : (body && Array.isArray(body.items) ? body.items : []);
    renderTokenList(tokensList, tokenListModels(tokens), document, {mutable: selfService});
  }

  function persistCLIConnectRequest() {
    const request = cliConnectRequestFromSearch(window.location.search);
    if (!request) return null;
    try { sessionStorage.setItem(CLI_CONNECT_STORAGE_KEY, JSON.stringify(request)); } catch { /* storage may be blocked */ }
    return request;
  }

  function pendingCLIConnectRequest() {
    const current = cliConnectRequestFromSearch(window.location.search);
    if (current) return current;
    try {
      const saved = JSON.parse(sessionStorage.getItem(CLI_CONNECT_STORAGE_KEY) || 'null');
      if (validCLIConnectRequest(saved)) return saved;
    } catch { /* absent, malformed, or blocked storage */ }
    return null;
  }

  function clearCLIConnectRequest() {
    try { sessionStorage.removeItem(CLI_CONNECT_STORAGE_KEY); } catch { /* storage may be blocked */ }
  }

  function restoreCLIConnectRoute() {
    const request = pendingCLIConnectRequest();
    if (!request || cliConnectRequestFromSearch(window.location.search)) return;
    const params = new URLSearchParams({
      connect_hash: request.tokenHash,
      connect_name: request.name,
      connect_code: request.code,
    });
    history.replaceState(null, '', `/tokens?${params}`);
  }

  function renderCLIConnectRequest() {
    if (!cliConnectPanel) return;
    const request = pendingCLIConnectRequest();
    cliConnectPanel.hidden = !request;
    if (!request) return;
    cliConnectUser.textContent = (state.user && (state.user.display_name || state.user.username)) || 'your account';
    cliConnectDevice.textContent = cliConnectDeviceLabel(request.name);
    cliConnectCode.textContent = request.code;
    setError(cliConnectError, '');
    cliConnectSuccess.hidden = true;
    cliConnectApprove.hidden = false;
    cliConnectCancel.hidden = false;
    cliConnectApprove.disabled = false;
    cliConnectApprove.textContent = 'Connect CLI';
  }

  async function approveCLIConnect() {
    const request = pendingCLIConnectRequest();
    if (!request) return;
    cliConnectApprove.disabled = true;
    cliConnectApprove.textContent = 'Connecting…';
    setError(cliConnectError, '');
    let resp;
    try {
      resp = await api('/api/tokens/connect', {
        method: 'POST',
        body: JSON.stringify({ token_hash: request.tokenHash, name: request.name }),
      });
    } catch {
      setError(cliConnectError, 'Network error. The terminal is still waiting; try again.');
      cliConnectApprove.disabled = false;
      cliConnectApprove.textContent = 'Connect CLI';
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let message = 'Could not connect this CLI';
      try { const body = await resp.json(); if (body && body.error) message = body.error; } catch {}
      setError(cliConnectError, message);
      cliConnectApprove.disabled = false;
      cliConnectApprove.textContent = 'Connect CLI';
      return;
    }
    clearCLIConnectRequest();
    cliConnectSuccess.hidden = false;
    cliConnectApprove.hidden = true;
    cliConnectCancel.textContent = 'Done';
    history.replaceState(null, '', '/tokens');
    loadTokens();
  }

  function cancelCLIConnect() {
    clearCLIConnectRequest();
    if (cliConnectPanel) cliConnectPanel.hidden = true;
    router.navigate('/tokens', { replace: true });
  }

  function openNewTokenModal() {
    if (state.user?.can_manage_self === false) {
      flashToast('This shared account is read-only.', 'error');
      return;
    }
    newTokenForm.reset();
    setError(newTokenError, '');
    newTokenForm.hidden = false;
    tokenReveal.hidden = true;
    tokenRevealValue.textContent = '';
    newTokenModal.hidden = false;
    modalTrap(newTokenModal).activate();
    newTokenName.focus();
  }

  function closeNewTokenModal() {
    newTokenModal.hidden = true;
    modalTrap(newTokenModal).release();
    newTokenForm.reset();
    setError(newTokenError, '');
    // Clear the revealed secret so it never lingers in the DOM after close.
    tokenRevealValue.textContent = '';
    tokenReveal.hidden = true;
    newTokenForm.hidden = false;
  }

  async function submitNewToken(event) {
    event.preventDefault();
    const name = newTokenName.value.trim();
    if (!name) { setError(newTokenError, 'A token name is required'); return; }
    const payload = { name };
    const expiryDays = parseInt(document.getElementById('new-token-expiry').value, 10);
    if (Number.isFinite(expiryDays)) payload.expires_in_days = expiryDays;
    let resp;
    try {
      resp = await api('/api/tokens', { method: 'POST', body: JSON.stringify(payload) });
    } catch {
      setError(newTokenError, 'Network error');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let msg = 'Failed to create token';
      if (resp.status === 409) {
        msg = 'You already have a token with that name';
      } else {
        try { const j = await resp.json(); if (j && j.error) msg = j.error; } catch {}
      }
      setError(newTokenError, msg);
      return;
    }
    let body = {};
    try { body = await resp.json(); } catch {}
    // Reveal the raw token ONCE: swap the form for the reveal panel. The value is
    // never re-fetchable (only the hash is stored server-side).
    newTokenForm.hidden = true;
    tokenRevealValue.textContent = body.token || '';
    tokenReveal.hidden = false;
    if (tokenRevealDone) tokenRevealDone.focus();
    loadTokens(); // refresh the list behind the modal
  }

  async function revokeToken(id, name, btn) {
    if (!confirm(`Revoke token "${name}"? Any CLI or app using it will stop working immediately.`)) return;
    if (btn) btn.disabled = true;
    let resp;
    try {
      resp = await api(`/api/tokens/${id}`, { method: 'DELETE' });
    } catch {
      setError(tokensError, 'Network error');
      return;
    } finally {
      if (btn) btn.disabled = false;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) { setError(tokensError, `Failed to revoke ${name}`); return; }
    setError(tokensError, '');
    loadTokens();
  }

  async function performRestart(slug, btn, cardLocal = false) {
    if (cardLocal && appRestartFeedback.get(slug)?.phase === 'pending') return;
    setError(appError, '');
    if (cardLocal) setRestartFeedback(slug, 'pending');
    if (btn) btn.disabled = true;

    let result;
    try {
      result = await requestAppRestart(api, slug);
    } finally {
      if (btn) btn.disabled = false;
    }

    if (result.kind === 'unauthorized') {
      if (cardLocal) clearRestartFeedback(slug);
      await handleUnauthorized();
      return;
    }

    if (result.kind !== 'success') {
      if (cardLocal) {
        setRestartFeedback(slug, 'error', result.message);
        // Restart failures can leave the app stopped. Refresh the durable state
        // while preserving the local recovery panel across the grid re-render.
        if (result.kind === 'error') await loadApps();
      } else {
        setError(appError, result.message);
      }
      return;
    }

    if (result.app) {
      const app = state.apps.find((item) => item.slug === slug);
      if (app) Object.assign(app, result.app);
    }

    if (cardLocal) {
      // The restart endpoint responds only after deployRun has passed readiness
      // and the proxy has switched, so success here means the app is serving.
      const success = setRestartFeedback(slug, 'success');
      await loadApps();
      window.setTimeout(() => clearRestartFeedback(slug, success.version), 6000);
    } else {
      await loadApps();
    }
  }

  async function restart(slug, btn) {
    const card = btn && btn.closest('.app-card');
    const control = card && appCardLifecycleControls.get(slug);
    if (card && control) {
      const returnFocus = card.querySelector('.kebab-menu > button');
      control.showConfirmation(returnFocus);
      return;
    }

    const app = state.apps.find((item) => item.slug === slug);
    const copy = restartConfirmationCopy(app ? app.name : slug);
    if (!window.confirm(`${copy.title}\n\n${copy.message}`)) return;
    await performRestart(slug, btn, false);
  }

  // lifecycleAction is the shared POST-then-refresh path behind Sleep, Stop and
  // Start. It mirrors restart(): clear the error region, disable the control,
  // post, then refresh so the card badge reflects the new state. The server's
  // message wins over the generic one, because a refused lifecycle call answers
  // 409 with the actual reason (the app is not running, the app uses elastic
  // worker isolation) and that reason is what tells the operator what to do.
  async function lifecycleAction(slug, path, btn, failureMessage) {
    clearRestartFeedback(slug);
    setError(appError, '');
    if (btn) btn.disabled = true;

    let response;
    try {
      response = await api(`/api/apps/${encodeURIComponent(slug)}${path}`, {method: 'POST'});
    } catch {
      setError(appError, 'Network error');
      return;
    } finally {
      if (btn) btn.disabled = false;
    }

    if (response.status === 401) {
      await handleUnauthorized();
      return;
    }

    let body = null;
    try { body = await response.json(); } catch { /* non-JSON */ }

    if (!response.ok) {
      setError(appError, (body && body.error) || failureMessage);
      return;
    }

    // The detail header renders once per navigation, so an action taken there
    // has to fold the new status back in or the menu keeps offering Sleep for an
    // app that is already asleep. Repaint the pill from the same merge the
    // metrics poller uses and derive the menu from the merged model, so the two
    // cannot disagree during the window before the next tick - waiting for that
    // tick left the header reading "Sleeping" above a Stop item.
    if (detailApp && detailApp.slug === slug && body && body.status) {
      const live = {status: body.status, deploying: false};
      const pillEl = document.getElementById('app-detail-status');
      if (pillEl) {
        updateStatusPill(pillEl, detailApp, live, formatStatus);
      } else {
        detailApp.status = live.status;
      }
      syncDetailHeaderActions(detailApp);
    }
    loadApps();
  }

  // Sleep releases the app's resources and leaves it wakeable: the next visitor
  // request brings it back transparently, so there is nothing to confirm.
  async function sleepApp(slug, btn) {
    await lifecycleAction(slug, '/sleep', btn, 'Sleep failed');
  }

  // Stop is terminal. Visitors get a stopped page until an operator starts the
  // app again, which is user-visible, so it is confirmed first.
  async function stopApp(slug, btn) {
    if (!window.confirm('Stop this app? Visitors will see a "stopped" page until you start it again.')) return;
    await lifecycleAction(slug, '/stop', btn, 'Stop failed');
  }

  // Start uses the idempotent restart form so a second click cannot cycle an app
  // that another operator already brought up.
  async function startApp(slug, btn) {
    await lifecycleAction(slug, '/restart?if_not_running=true', btn, 'Start failed');
  }

  function openLogs(slug) {
    closeLogs();
    logPaneTitle.textContent = `Logs — ${slug}`;
    logPaneBody.textContent = '';
    setHidden(logPane, false);

    const es = new EventSource(`/api/apps/${slug}/logs`, {withCredentials: true});
    activeEventSource = es;

    es.onmessage = (event) => {
      const atBottom =
        logPaneBody.scrollHeight - Math.ceil(logPaneBody.scrollTop) <= logPaneBody.clientHeight + 1;
      logPaneBody.appendChild(document.createTextNode(event.data + '\n'));
      if (atBottom) {
        logPaneBody.scrollTop = logPaneBody.scrollHeight;
      }
    };

    es.onerror = () => {
      es.close();
      activeEventSource = null;
      logPaneBody.appendChild(document.createTextNode('(log stream disconnected)\n'));
      logPaneBody.scrollTop = logPaneBody.scrollHeight;
    };
  }

  function closeLogs() {
    if (activeEventSource) {
      activeEventSource.close();
      activeEventSource = null;
    }
    setHidden(logPane, true);
  }

  let settingsSlug = null;

  // A successful settings mutation can create (or clear) a temporary fleet
  // override. Re-read the server-owned convergence envelope and repaint only
  // fleet surfaces so the current form, focus, and unrelated unsaved edits stay
  // intact. Failure is deliberately non-fatal: the save already succeeded and
  // the next route mount will recover the display.
  async function refreshDetailFleetState(slug) {
    if (!slug || !detailApp || detailApp.slug !== slug) return;
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}`);
    } catch {
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) return;
    let body;
    try { body = await resp.json(); } catch { return; }
    // The user may have navigated while the refresh was in flight.
    if (!detailApp || detailApp.slug !== slug) return;
    if (body && body.app) Object.assign(detailApp, body.app);
    detailLastEnvelope = body || {};
    refreshFleetSurfaces(document, detailApp, body && body.fleet_state);
  }

  // --- Settings dirty-state tracking (explicit-save model) ---
  // Every settings section registers the inputs it owns. populate*() snapshots a
  // baseline; when live values diverge the section is "dirty": its Save button
  // enables and an "Unsaved changes" hint shows. Saving (or a confirmed discard)
  // re-snapshots to clean. A nav/unload guard warns before losing unsaved edits,
  // so all settings tabs behave identically.
  const settingsSections = {};
  function registerSettingsSection(name, getEls, saveBtnId, dirtyId) {
    const els = getEls();
    const rec = {
      els,
      btn: document.getElementById(saveBtnId),
      dirtyEl: dirtyId ? document.getElementById(dirtyId) : null,
      baseline: null,
    };
    settingsSections[name] = rec;
    const onChange = () => recomputeDirty(name);
    els.forEach(el => {
      el.addEventListener('input', onChange);
      el.addEventListener('change', onChange);
    });
  }
  function sectionSnapshot(rec) {
    // Disabled inputs don't contribute to the saved payload (e.g. the custom
    // hibernate-minutes / autoscale-target fields are disabled unless their mode
    // is selected). Excluding them keeps a section from reading "dirty" after a
    // toggle to a mode-specific field and back.
    return JSON.stringify(rec.els.map(el => {
      if (el.disabled) return null;
      return (el.type === 'checkbox' || el.type === 'radio') ? el.checked : el.value;
    }));
  }
  function snapshotSettingsSection(name) {
    const rec = settingsSections[name];
    if (!rec) return;
    rec.baseline = sectionSnapshot(rec);
    recomputeDirty(name);
  }
  function isSectionDirty(name) {
    const rec = settingsSections[name];
    if (!rec || rec.baseline === null) return false;
    return rec.baseline !== sectionSnapshot(rec);
  }
  function recomputeDirty(name) {
    const rec = settingsSections[name];
    if (!rec) return;
    const dirty = isSectionDirty(name);
    if (rec.btn && !rec.btn.hidden) rec.btn.disabled = !dirty;
    if (rec.dirtyEl) setHidden(rec.dirtyEl, !dirty);
  }
  function anySettingsDirty() {
    return Object.keys(settingsSections).some(isSectionDirty);
  }
  // clearSettingsDirty re-snapshots every section to its current values, so the
  // unsaved-changes guard treats them as clean. Used on a confirmed discard and
  // after a destructive action (delete) where the edits are moot.
  function clearSettingsDirty() {
    Object.keys(settingsSections).forEach(snapshotSettingsSection);
  }
  // confirmDiscardIfDirty gates navigation: returns true when it is safe to
  // leave (nothing unsaved, or the user confirmed). On confirm it re-snapshots
  // so the guard doesn't fire again during teardown.
  function confirmDiscardIfDirty() {
    if (!anySettingsDirty()) return true;
    const ok = window.confirm('You have unsaved changes in this app’s settings. Leave without saving?');
    if (ok) clearSettingsDirty();
    return ok;
  }

  function populateAccessPanel(app) {
    const canManage = canManageApp(state.user, app);
    const radios = document.querySelectorAll('input[name="access-level"]');
    radios.forEach(r => {
      r.checked = r.value === app.access;
      r.disabled = !canManage;
    });
    document.getElementById('visibility-save-btn').hidden = !canManage;
    setError(document.getElementById('visibility-error'), '');
    setHidden(document.getElementById('visibility-status'), true);
    snapshotSettingsSection('visibility');

    // Clear previous state.
    document.getElementById('members-list').innerHTML = '';
    document.getElementById('grant-username').value = '';
    document.getElementById('grant-error').hidden = true;
  }

  async function saveVisibility() {
    if (!settingsSlug) return;
    const errEl = document.getElementById('visibility-error');
    const statusEl = document.getElementById('visibility-status');
    setError(errEl, '');
    setHidden(statusEl, true);
    const selected = document.querySelector('input[name="access-level"]:checked');
    if (!selected) { setError(errEl, 'Pick a visibility.'); return; }
    // Capture the submitted slug + value BEFORE the await: the user might switch
    // apps or radios while the PATCH is in flight. Freeze the radio group so the
    // selection can't drift mid-save.
    const slug = settingsSlug;
    const value = selected.value;
    const btn = document.getElementById('visibility-save-btn');
    const radios = [...document.querySelectorAll('input[name="access-level"]')];
    btn.disabled = true;
    radios.forEach(r => { r.disabled = true; });
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}/access`, {
        method: 'PATCH',
        body: JSON.stringify({ access: value }),
      });
    } catch {
      if (settingsSlug === slug) {
        radios.forEach(r => { r.disabled = false; });
        setError(errEl, 'Failed to save. Check your connection.');
        recomputeDirty('visibility');
      }
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    // The save applied to `slug`; reflect it in the cached list regardless of
    // where the user navigated.
    const ok = resp.ok;
    if (ok) {
      const app = state.apps.find(a => a.slug === slug);
      if (app) app.access = value;
    }
    // Only touch the visibility panel if it still shows the same app (the user
    // may have navigated to another app's Access tab during the PATCH, which
    // re-populated and re-set the radios for that app).
    if (settingsSlug !== slug) return;
    radios.forEach(r => { r.disabled = false; });
    if (!ok) {
      let message = 'Failed to save.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      recomputeDirty('visibility');
      return;
    }
    statusEl.textContent = 'Saved.';
    setHidden(statusEl, false);
    snapshotSettingsSection('visibility');
    await refreshDetailFleetState(slug);
    flashToast('Access updated', 'success');
  }

  // --- General tab ---

  function hibernateModeFromValue(minutes) {
    if (minutes === null || minutes === undefined) return 'default';
    if (minutes === 0) return 'never';
    return 'custom';
  }

  // renderPacingBlock holds the server's render_pacing advisory for the app
  // currently open in Configuration, so the advice line can be recomputed on
  // every keystroke without refetching. It describes the SAVED render_seconds
  // (the server computes it from what is persisted), which is why
  // renderPacingAdvice compares it against the typed value rather than assuming
  // they agree. Null when pacing is off or no app is open.
  let renderPacingBlock = null;

  function updateRenderPacingAdvice() {
    const el = document.getElementById('render-pacing-advice');
    if (!el) return;
    const raw = document.getElementById('render-seconds').value.trim();
    // A blank field has no advice to give: saving it is a validation error, not
    // a state the operator can be told the consequences of.
    el.textContent = raw === '' ? '' : renderPacingAdvice(Number(raw), renderPacingBlock);
  }

  function populateGeneralTab(app, envelope) {
    const mode = hibernateModeFromValue(app.hibernate_timeout_minutes);
    document.querySelectorAll('input[name="hibernate-mode"]').forEach(r => {
      r.checked = r.value === mode;
    });

    const customInput = document.getElementById('hibernate-custom-minutes');
    customInput.value = mode === 'custom' ? String(app.hibernate_timeout_minutes) : '';
    customInput.disabled = mode !== 'custom';

    const canEdit = canManageApp(state.user, app);
    document.querySelectorAll('input[name="hibernate-mode"]').forEach(r => {
      r.disabled = !canEdit;
    });
    if (!canEdit) customInput.disabled = true;
    document.getElementById('hibernate-save-btn').hidden = !canEdit;

    // Keep-warm floor: min_warm_replicas.
    const minWarmInput = document.getElementById('min-warm-replicas');
    minWarmInput.value = String(app.min_warm_replicas ?? 0);
    minWarmInput.disabled = !canEdit;
    // The inert-floor note reads the EFFECTIVE isolation: the raw column is
    // empty for an app inheriting an elastic fleet default, and that app's
    // floor is just as inert as one that set grouped explicitly.
    updateMinWarmWarning(app.replicas ?? 1, app.min_warm_replicas ?? 0,
      app.effective_worker_isolation || app.worker_isolation || 'multiplex');

    setError(document.getElementById('hibernate-error'), '');
    setHidden(document.getElementById('hibernate-status'), true);

    // Scaling fieldset: replicas + per-replica session cap.
    const replicasInput = document.getElementById('scaling-replicas');
    const capInput = document.getElementById('scaling-cap');
    const currentReplicas = app.replicas ?? 1;
    replicasInput.value = String(currentReplicas);
    capInput.value = String(app.max_sessions_per_replica ?? 0);
    replicasInput.dataset.original = String(currentReplicas);
    replicasInput.dataset.appStatus = String(app.status ?? '');
    replicasInput.disabled = !canEdit;
    capInput.disabled = !canEdit;
    document.getElementById('scaling-save-btn').hidden = !canEdit;
    setError(document.getElementById('scaling-error'), '');
    setHidden(document.getElementById('scaling-status'), true);
    updateScalingCeiling();

    // Worker isolation controls.
    const isolationSelect = document.getElementById('worker-isolation');
    const groupedSizeInput = document.getElementById('worker-grouped-size');
    const maxWorkersInput = document.getElementById('worker-max-workers');
    const warmSparesInput = document.getElementById('worker-warm-spares');
    isolationSelect.value = app.worker_isolation || 'multiplex';
    isolationSelect.dataset.original = isolationSelect.value;
    isolationSelect.dataset.appStatus = String(app.status ?? '');
    isolationSelect.dataset.lifetimeSecs = app.worker_max_session_lifetime_secs != null ? String(app.worker_max_session_lifetime_secs) : '';
    groupedSizeInput.value = String(app.worker_grouped_size ?? 1);
    maxWorkersInput.value = String(app.worker_max_workers ?? 0);
    warmSparesInput.value = String(app.worker_warm_spares ?? 0);
    isolationSelect.disabled = !canEdit;
    groupedSizeInput.disabled = !canEdit;
    maxWorkersInput.disabled = !canEdit;
    warmSparesInput.disabled = !canEdit;
    updateWorkerCapacity();

    // Render pacing: one number, plus the server's advisory for what it implies
    // on this host. The block is absent when the SAVED value is 0 (pacing off),
    // which is exactly the state the advice line then reports.
    const renderInput = document.getElementById('render-seconds');
    renderPacingBlock = (envelope && envelope.render_pacing) || null;
    renderInput.value = String(app.render_seconds ?? 0);
    renderInput.disabled = !canEdit;
    document.getElementById('render-save-btn').hidden = !canEdit;
    setError(document.getElementById('render-error'), '');
    setHidden(document.getElementById('render-status'), true);
    updateRenderPacingAdvice();

    // General info: display name + description + project slug (rename / regroup).
    document.getElementById('general-name').value = app.name ?? '';
    document.getElementById('general-description').value = app.description ?? '';
    document.getElementById('general-project').value = app.project_slug ?? '';
    document.getElementById('general-name').disabled = !canEdit;
    document.getElementById('general-description').disabled = !canEdit;
    document.getElementById('general-project').disabled = !canEdit;
    document.getElementById('general-save-btn').hidden = !canEdit;
    setError(document.getElementById('general-error'), '');
    setHidden(document.getElementById('general-status'), true);

    renderIconPicker(app, canEdit);

    // Resources: per-replica memory/CPU caps. null (no limit) renders as empty.
    // Limits are enforced in BOTH native and docker mode, so the controls are
    // editable in both. Native enforcement is best-effort (needs cgroup v2
    // delegation), so resource_enforcement drives a warning when it is absent.
    const memInput = document.getElementById('resources-memory');
    const cpuInput = document.getElementById('resources-cpu');
    memInput.value = app.memory_limit_mb != null ? String(app.memory_limit_mb) : '';
    cpuInput.value = app.cpu_quota_percent != null ? String(app.cpu_quota_percent) : '';
    memInput.disabled = !canEdit;
    cpuInput.disabled = !canEdit;
    document.getElementById('resources-save-btn').hidden = !canEdit;
    // Stash originals + status so saveResources confirms the restart only when a
    // value actually changes on a running app (mirrors the scaling guard).
    memInput.dataset.original = memInput.value;
    cpuInput.dataset.original = cpuInput.value;
    memInput.dataset.appStatus = app.status || '';
    const enf = app.resource_enforcement;
    const nativeMode = app.runtime_mode !== 'docker';
    const note = document.getElementById('resources-runtime-note');
    let warn = '';
    if (nativeMode && enf) {
      if (!enf.memory && !enf.cpu) {
        warn = 'This host has no cgroup v2 delegation, so memory and CPU limits set here are not enforced. Configure systemd Delegate=cpu memory on the service to apply them.';
      } else if (!enf.cpu) {
        warn = 'CPU limits are not enforced on this host (the cgroup cpu controller is not delegated; needs Delegate=cpu). Memory limits are enforced.';
      } else if (!enf.memory) {
        warn = 'Memory limits are not enforced on this host (the cgroup memory controller is not delegated; needs Delegate=memory).';
      }
    }
    note.textContent = warn;
    setHidden(note, warn === '');
    setError(document.getElementById('resources-error'), '');
    setHidden(document.getElementById('resources-status'), true);

    // Danger zone (lifecycle) now lives at the bottom of Configuration, not
    // Access. Visible to managers; the typed-confirm gate stays the same.
    resetDangerZone();
    const dangerZone = document.getElementById('danger-zone');
    dangerZone.hidden = !canEdit;
    document.getElementById('delete-confirm-slug').textContent = app.slug;

    // Snapshot baselines so the explicit-save dirty tracker starts clean.
    snapshotSettingsSection('general');
    snapshotSettingsSection('resources');
    snapshotSettingsSection('hibernate');
    snapshotSettingsSection('scaling');
    snapshotSettingsSection('render');
  }

  // The icon picker uploads immediately (it is not tied to the General Save
  // button). Handlers are assigned with .onclick/.onchange so re-populating the
  // tab replaces them rather than stacking listeners across app switches.
  const ICON_MAX_BYTES = 512 * 1024;
  const ICON_TYPES = ['image/png', 'image/jpeg', 'image/webp', 'image/svg+xml'];

  // renderDetailHeaderAvatar fills the app-detail header's icon slot with the
  // app's uploaded icon (or its monogram). Called on every detail mount (via
  // setDetailApp) and after an icon upload/removal.
  function renderDetailHeaderAvatar(app) {
    const slot = document.getElementById('app-detail-icon');
    if (!slot || !app) return;
    slot.replaceChildren(renderAppAvatar(document, avatarView(app), 'app-detail-avatar'));
  }

  function renderIconPicker(app, canEdit) {
    const preview = document.getElementById('general-icon-preview');
    const uploadBtn = document.getElementById('general-icon-upload');
    const removeBtn = document.getElementById('general-icon-remove');
    const fileInput = document.getElementById('general-icon-file');
    const statusEl = document.getElementById('general-icon-status');
    const emojiBtn = document.getElementById('general-icon-emoji-btn');
    const emojiClearBtn = document.getElementById('general-icon-emoji-clear-btn');
    if (!preview || !uploadBtn || !removeBtn || !fileInput) return;

    setHidden(statusEl, true);
    preview.replaceChildren(renderAppAvatar(document, avatarView(app), 'icon-picker-preview'));
    uploadBtn.hidden = !canEdit;
    removeBtn.hidden = !canEdit || (!app.icon_mime && !app.icon_emoji);
    // The emoji trigger and its Clear-emoji control are wired once (see the
    // top-level setup block below); only their visibility changes per render.
    if (emojiBtn) emojiBtn.hidden = !canEdit;
    if (emojiClearBtn) emojiClearBtn.hidden = !canEdit || !app.icon_emoji;

    if (!canEdit) {
      uploadBtn.onclick = null;
      removeBtn.onclick = null;
      fileInput.onchange = null;
      return;
    }
    uploadBtn.onclick = () => fileInput.click();
    fileInput.onchange = () => {
      const f = fileInput.files && fileInput.files[0];
      fileInput.value = ''; // let the same file be re-picked later
      if (f) uploadIcon(app, f);
    };
    removeBtn.onclick = () => removeIcon(app);
  }

  async function uploadIcon(app, file) {
    const errEl = document.getElementById('general-error');
    const statusEl = document.getElementById('general-icon-status');
    setError(errEl, '');
    if (file.size > ICON_MAX_BYTES) {
      setError(errEl, 'Icon must be 512 KB or smaller.');
      return;
    }
    if (file.type && !ICON_TYPES.includes(file.type)) {
      setError(errEl, 'Icon must be a PNG, JPEG, WebP, or SVG image.');
      return;
    }
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(app.slug)}/icon`, {
        method: 'PUT',
        headers: { 'Content-Type': file.type || 'application/octet-stream' },
        body: file,
      });
    } catch {
      setError(errEl, 'Upload failed. Check your connection.');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let msg = 'Upload failed.';
      try { const b = await resp.json(); if (b && b.error) msg = b.error; } catch { /* non-JSON */ }
      setError(errEl, msg);
      return;
    }
    let body = {};
    try { body = await resp.json(); } catch { /* tolerate */ }
    applyIconChange(app, { mime: body.icon_mime || file.type, emoji: '' });
    statusEl.textContent = 'Icon updated.';
    setHidden(statusEl, false);
    await refreshDetailFleetState(app.slug);
  }

  async function removeIcon(app) {
    const errEl = document.getElementById('general-error');
    const statusEl = document.getElementById('general-icon-status');
    setError(errEl, '');
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(app.slug)}/icon`, { method: 'DELETE' });
    } catch {
      setError(errEl, 'Failed to remove icon. Check your connection.');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) { setError(errEl, 'Failed to remove icon.'); return; }
    applyIconChange(app, { mime: '', emoji: '' });
    statusEl.textContent = 'Icon removed.';
    setHidden(statusEl, false);
    await refreshDetailFleetState(app.slug);
  }

  // setEmojiIcon PATCHes a single emoji as the app's icon (server-side this
  // destroys any uploaded image - explicit user intent, mirrored by
  // applyIconChange's mime: ''). The coarse client-side check (isSingleEmoji)
  // only blocks the empty string, plain text, and a multi-emoji paste before
  // a round trip; the server (deploy.ValidateIconEmoji) is the authority and
  // its message is shown verbatim on a 400.
  async function setEmojiIcon(app, rawValue) {
    const errEl = document.getElementById('general-error');
    const statusEl = document.getElementById('general-icon-status');
    setError(errEl, '');
    const value = (rawValue || '').trim();
    if (!isSingleEmoji(value)) {
      setError(errEl, 'Enter a single emoji.');
      return;
    }
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(app.slug)}`, {
        method: 'PATCH',
        body: JSON.stringify({ icon_emoji: value }),
      });
    } catch {
      setError(errEl, 'Failed to save. Check your connection.');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let msg = 'Failed to save icon.';
      try { const b = await resp.json(); if (b && b.error) msg = b.error; } catch { /* non-JSON */ }
      setError(errEl, msg);
      return;
    }
    let body = {};
    try { body = await resp.json(); } catch { /* tolerate */ }
    applyIconChange(app, { mime: '', emoji: (body.app && body.app.icon_emoji) || '' });
    const input = document.getElementById('general-icon-emoji-input');
    if (input) input.value = '';
    statusEl.textContent = 'Icon updated.';
    setHidden(statusEl, false);
    await refreshDetailFleetState(app.slug);
  }

  // clearEmojiIcon PATCHes icon_emoji back to empty. Unlike removeIcon (the
  // Remove button, which destroys both the image and the emoji), this only
  // clears the emoji: the server's clear path (SetAppIconEmoji) writes
  // icon_emoji alone and never touches icon_mime, so an app whose emoji was
  // set through manifest reconciliation (which also never touches icon_mime)
  // keeps its image and it must resurface once the emoji is gone. So the mime
  // written locally has to come from the server's own response, not a
  // hardcoded '' - that would blank a retained image everywhere in the UI
  // until a reload, contradicting the mutual-exclusivity contract this whole
  // control follows. icon_mime is omitempty, so "no image" is an absent key,
  // not ''; if the body itself fails to parse, keep the app's last-known mime
  // rather than guessing blank.
  async function clearEmojiIcon(app) {
    const errEl = document.getElementById('general-error');
    const statusEl = document.getElementById('general-icon-status');
    setError(errEl, '');
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(app.slug)}`, {
        method: 'PATCH',
        body: JSON.stringify({ icon_emoji: '' }),
      });
    } catch {
      setError(errEl, 'Failed to clear icon. Check your connection.');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let msg = 'Failed to clear icon.';
      try { const b = await resp.json(); if (b && b.error) msg = b.error; } catch { /* non-JSON */ }
      setError(errEl, msg);
      return;
    }
    let body = {};
    try { body = await resp.json(); } catch { /* tolerate */ }
    const mime = body.app ? (body.app.icon_mime || '') : app.icon_mime;
    applyIconChange(app, { mime, emoji: '' });
    statusEl.textContent = 'Icon updated.';
    setHidden(statusEl, false);
    await refreshDetailFleetState(app.slug);
  }

  // applyIconChange records the new icon state on the app and every cached copy
  // (detailApp, the sidebar/grid list) and bumps updated_at as the cache-buster,
  // then re-renders the picker preview and the detail-header avatar so the change
  // shows everywhere without a reload. Both mime and emoji are written on every
  // call, even the one that did not change, because the server enforces mutual
  // exclusivity (setting one clears the other): the cached copy must mirror
  // exactly what the server just did, or a stale emoji (which now renders ahead
  // of the image) keeps winning until a reload.
  function applyIconChange(app, { mime, emoji }) {
    const stamp = new Date().toISOString();
    app.icon_mime = mime;
    app.icon_emoji = emoji;
    app.updated_at = stamp;
    if (detailApp && detailApp.slug === app.slug && detailApp !== app) {
      detailApp.icon_mime = mime;
      detailApp.icon_emoji = emoji;
      detailApp.updated_at = stamp;
    }
    const listed = (state.apps || []).find((a) => a && a.slug === app.slug);
    if (listed) { listed.icon_mime = mime; listed.icon_emoji = emoji; listed.updated_at = stamp; }
    renderIconPicker(app, true);
    renderDetailHeaderAvatar(app);
    if (typeof syncSidebar === 'function') syncSidebar();
  }

  async function saveGeneralInfo() {
    if (!settingsSlug) return;
    const slug = settingsSlug;
    const errEl = document.getElementById('general-error');
    const statusEl = document.getElementById('general-status');
    setError(errEl, '');
    setHidden(statusEl, true);
    const name = document.getElementById('general-name').value.trim();
    const description = document.getElementById('general-description').value.trim();
    const project = document.getElementById('general-project').value.trim();
    if (name.length < 1 || name.length > 128) {
      setError(errEl, 'Display name must be 1 to 128 characters.');
      return;
    }
    // Count code points (spread iterates by code point), not UTF-16 units, so the
    // limit matches the server's rune count and an emoji-bearing description that
    // the backend accepts is not rejected here.
    if ([...description].length > 280) {
      setError(errEl, 'Description must be 280 characters or fewer.');
      return;
    }
    const btn = document.getElementById('general-save-btn');
    btn.disabled = true;
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}`, {
        method: 'PATCH',
        body: JSON.stringify({ name, description, project_slug: project }),
      });
    } catch {
      setError(errEl, 'Failed to save. Check your connection.');
      recomputeDirty('general');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let message = 'Failed to save.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      recomputeDirty('general');
      return;
    }
    statusEl.textContent = 'Saved.';
    setHidden(statusEl, false);
    snapshotSettingsSection('general');
    const heading = document.getElementById('app-detail-heading');
    if (heading) heading.textContent = name;
    if (detailApp) { detailApp.name = name; detailApp.description = description; }
    // A rename changes the monogram initials (derived from the name), so re-render
    // the header + picker avatar; for an app with an uploaded icon this is a no-op
    // re-render of the same image.
    if (detailApp) {
      renderDetailHeaderAvatar(detailApp);
      const preview = document.getElementById('general-icon-preview');
      if (preview) preview.replaceChildren(renderAppAvatar(document, avatarView(detailApp), 'icon-picker-preview'));
    }
    await loadApps();
    await refreshDetailFleetState(slug);
  }

  async function saveResources() {
    if (!settingsSlug) return;
    const slug = settingsSlug;
    const errEl = document.getElementById('resources-error');
    const statusEl = document.getElementById('resources-status');
    setError(errEl, '');
    setHidden(statusEl, true);
    const parseLimit = (raw, label, min, max) => {
      const t = raw.trim();
      if (t === '') return null; // empty == no limit (inherit)
      // The API contract is a non-negative integer; reject fractional/exponent
      // input (e.g. "1.5", "1e2") instead of letting parseInt silently truncate.
      if (!/^\d+$/.test(t)) {
        throw new Error(`${label} must be a whole number (leave empty for no limit).`);
      }
      const n = Number(t);
      if (!Number.isInteger(n) || n < 0) {
        throw new Error(`${label} must be 0 or a positive whole number (leave empty for no limit).`);
      }
      // 0 always means "no limit"; a positive value must fall in [min, max].
      if (n !== 0 && min != null && n < min) {
        throw new Error(`${label} must be 0 (no limit) or at least ${min} (leave empty to inherit).`);
      }
      if (max != null && n > max) {
        throw new Error(`${label} must be between 0 and ${max} (leave empty for no limit).`);
      }
      return n;
    };
    let memory, cpu;
    try {
      memory = parseLimit(document.getElementById('resources-memory').value, 'Memory limit', 16, 1048576);
      cpu = parseLimit(document.getElementById('resources-cpu').value, 'CPU quota', 1, 6400);
    } catch (e) {
      setError(errEl, e.message);
      return;
    }
    // A resource-limit change restarts the app (the cgroup ceiling is applied at
    // spawn), dropping active sessions. Confirm before the disruptive case, only
    // when a value actually changed on a running app (mirrors the scaling guard).
    const memInput = document.getElementById('resources-memory');
    const cpuInput = document.getElementById('resources-cpu');
    const wasRunning = memInput.dataset.appStatus === 'running';
    const changed = memInput.value.trim() !== (memInput.dataset.original ?? '') ||
      cpuInput.value.trim() !== (cpuInput.dataset.original ?? '');
    if (wasRunning && changed) {
      const ok = window.confirm('Changing resource limits will restart the app and drop all active sessions. Continue?');
      if (!ok) return;
    }
    const btn = document.getElementById('resources-save-btn');
    btn.disabled = true;
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}`, {
        method: 'PATCH',
        body: JSON.stringify({ memory_limit_mb: memory, cpu_quota_percent: cpu }),
      });
    } catch {
      setError(errEl, 'Failed to save. Check your connection.');
      recomputeDirty('resources');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let message = 'Failed to save.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      recomputeDirty('resources');
      return;
    }
    statusEl.textContent = 'Saved.';
    setHidden(statusEl, false);
    snapshotSettingsSection('resources');
    await loadApps();
    await refreshDetailFleetState(slug);
  }

  function updateMinWarmWarning(replicas, minWarm, isolation) {
    const el = document.getElementById('min-warm-warning');
    if (!el) return;
    el.hidden = !(replicas < minWarm);
    // A positive floor under elastic isolation is stored but does nothing;
    // say so next to the field rather than letting the operator wait for
    // replicas that an elastic pool never runs.
    const inertEl = document.getElementById('min-warm-inert-warning');
    if (!inertEl) return;
    const note = keepWarmInertNote(minWarm, isolation);
    inertEl.textContent = note;
    inertEl.hidden = !note;
  }

  // currentIsolationChoice is the isolation the Scaling select currently
  // shows: the saved value after a load, or the pending edit before a save.
  function currentIsolationChoice() {
    const sel = document.getElementById('worker-isolation');
    return sel && sel.value ? sel.value : 'multiplex';
  }

  function updateScalingCeiling() {
    const el = document.getElementById('scaling-ceiling');
    if (!el) return;
    const r = parseInt(document.getElementById('scaling-replicas').value, 10);
    const c = parseInt(document.getElementById('scaling-cap').value, 10);
    if (!Number.isFinite(r) || r < 1) { el.textContent = ''; return; }
    if (!Number.isFinite(c) || c < 0) { el.textContent = ''; return; }
    if (c === 0) {
      el.innerHTML = `Admission ceiling: <strong>${r}</strong> replica${r === 1 ? '' : 's'} × runtime default cap.`;
    } else {
      el.innerHTML = `Admission ceiling: <strong>${r} × ${c} = ${r * c}</strong> concurrent new sessions before 503.`;
    }
  }

  function updateWorkerCapacity() {
    const el = document.getElementById('worker-capacity');
    if (!el) return;
    const mode = document.getElementById('worker-isolation').value;
    const gs = parseInt(document.getElementById('worker-grouped-size').value, 10);
    const mw = parseInt(document.getElementById('worker-max-workers').value, 10);
    // The per-session RAM estimate uses the configured memory limit so an
    // operator who has set limits gets a meaningful worst-case figure.
    const memMB = parseInt(document.getElementById('resources-memory').value || '0', 10);
    el.textContent = workerCapacityLine(
      mode,
      Number.isFinite(gs) ? gs : 0,
      Number.isFinite(mw) ? mw : 0,
      Number.isFinite(memMB) ? memMB : 0,
    );
  }

  function onHibernateModeChange() {
    const selected = document.querySelector('input[name="hibernate-mode"]:checked');
    const customInput = document.getElementById('hibernate-custom-minutes');
    const isCustom = selected && selected.value === 'custom';
    customInput.disabled = !isCustom;
    if (isCustom && !customInput.value) customInput.value = '60';
    if (isCustom) customInput.focus();
    setError(document.getElementById('hibernate-error'), '');
    setHidden(document.getElementById('hibernate-status'), true);
  }

  async function saveHibernateSettings() {
    if (!settingsSlug) return;
    const slug = settingsSlug;
    const errEl = document.getElementById('hibernate-error');
    const statusEl = document.getElementById('hibernate-status');
    setError(errEl, '');
    setHidden(statusEl, true);

    const selected = document.querySelector('input[name="hibernate-mode"]:checked');
    if (!selected) {
      setError(errEl, 'Pick an option.');
      return;
    }

    let payload;
    if (selected.value === 'default') {
      payload = { hibernate_timeout_minutes: null };
    } else if (selected.value === 'never') {
      payload = { hibernate_timeout_minutes: 0 };
    } else {
      const raw = document.getElementById('hibernate-custom-minutes').value.trim();
      const n = parseInt(raw, 10);
      if (!Number.isFinite(n) || n < 1) {
        setError(errEl, 'Enter a whole number of minutes (1 or more).');
        return;
      }
      payload = { hibernate_timeout_minutes: n };
    }

    // Always include min_warm_replicas so the keep-warm floor is persisted
    // alongside any hibernation-mode change.
    const minWarmRaw = document.getElementById('min-warm-replicas').value.trim();
    const minWarm = parseInt(minWarmRaw, 10);
    if (!Number.isFinite(minWarm) || minWarm < 0 || minWarm > 1000) {
      setError(errEl, 'Keep warm must be a whole number between 0 and 1000.');
      return;
    }
    payload.min_warm_replicas = Number(minWarm);

    const btn = document.getElementById('hibernate-save-btn');
    btn.disabled = true;
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}`, {
        method: 'PATCH',
        body: JSON.stringify(payload),
      });
    } catch {
      btn.disabled = false;
      setError(errEl, 'Failed to save. Check your connection.');
      return;
    }
    btn.disabled = false;

    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let message = 'Failed to save.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      return;
    }

    statusEl.textContent = 'Saved.';
    setHidden(statusEl, false);
    snapshotSettingsSection('hibernate');
    // Re-evaluate the keep-warm warning using the values just saved.
    const savedReplicas = parseInt(document.getElementById('scaling-replicas').value, 10);
    const savedMinWarm = minWarm;
    updateMinWarmWarning(Number.isFinite(savedReplicas) ? savedReplicas : 1, savedMinWarm, currentIsolationChoice());
    await loadApps();
    await refreshDetailFleetState(slug);
  }

  async function saveScalingSettings() {
    if (!settingsSlug) return;
    const slug = settingsSlug;
    const errEl = document.getElementById('scaling-error');
    const statusEl = document.getElementById('scaling-status');
    setError(errEl, '');
    setHidden(statusEl, true);

    const replicasRaw = document.getElementById('scaling-replicas').value.trim();
    const capRaw = document.getElementById('scaling-cap').value.trim();
    const replicas = parseInt(replicasRaw, 10);
    const cap = parseInt(capRaw, 10);
    if (!Number.isFinite(replicas) || replicas < 1) {
      setError(errEl, 'Replicas must be a whole number ≥ 1.');
      return;
    }
    if (!Number.isFinite(cap) || cap < 0 || cap > 1000) {
      setError(errEl, 'Max sessions per replica must be between 0 and 1000.');
      return;
    }

    // Replica-count changes restart the app (apps.go redeployApp), which
    // drops every active session. Cap changes are hot. Confirm before the
    // disruptive case.
    const replicasInput = document.getElementById('scaling-replicas');
    const originalReplicas = parseInt(replicasInput.dataset.original ?? '', 10);
    const wasRunning = replicasInput.dataset.appStatus === 'running';
    if (wasRunning && Number.isFinite(originalReplicas) && replicas !== originalReplicas) {
      const ok = window.confirm(
        `Changing replicas from ${originalReplicas} to ${replicas} will restart the app and drop all active sessions. Continue?`,
      );
      if (!ok) return;
    }

    // Isolation mode changes also restart the app. Confirm before the
    // disruptive case.
    const isolationSelect = document.getElementById('worker-isolation');
    const workerIsolation = isolationSelect.value;
    const originalIsolation = isolationSelect.dataset.original ?? 'multiplex';
    const wasRunningIso = isolationSelect.dataset.appStatus === 'running';
    if (wasRunningIso && workerIsolation !== originalIsolation) {
      const ok = window.confirm(
        `Changing worker isolation mode from ${originalIsolation} to ${workerIsolation} will restart the app and drop all active sessions. Continue?`,
      );
      if (!ok) return;
    }

    const groupedSizeRaw = document.getElementById('worker-grouped-size').value.trim();
    const maxWorkersRaw = document.getElementById('worker-max-workers').value.trim();
    const warmSparesRaw = document.getElementById('worker-warm-spares').value.trim();
    const workerGroupedSize = parseInt(groupedSizeRaw, 10);
    const workerMaxWorkers = parseInt(maxWorkersRaw, 10);
    const workerWarmSpares = parseInt(warmSparesRaw, 10);
    if (!Number.isFinite(workerWarmSpares) || workerWarmSpares < 0 || workerWarmSpares > 1000) {
      setError(errEl, 'Warm workers must be a whole number between 0 and 1000.');
      return;
    }
    if (workerIsolation !== 'multiplex' && Number.isFinite(workerMaxWorkers) && workerWarmSpares > workerMaxWorkers) {
      setError(errEl, 'Warm workers cannot exceed max workers.');
      return;
    }
    const lifetimeRaw = isolationSelect.dataset.lifetimeSecs;
    const workerMaxSessionLifetimeSecs = lifetimeRaw ? parseInt(lifetimeRaw, 10) : null;

    const payload = {
      replicas,
      max_sessions_per_replica: cap,
      worker_isolation: workerIsolation,
      worker_grouped_size: Number.isFinite(workerGroupedSize) ? workerGroupedSize : 1,
      worker_max_workers: Number.isFinite(workerMaxWorkers) ? workerMaxWorkers : 0,
      worker_warm_spares: workerWarmSpares,
    };
    if (workerMaxSessionLifetimeSecs !== null && Number.isFinite(workerMaxSessionLifetimeSecs)) {
      payload.worker_max_session_lifetime_secs = workerMaxSessionLifetimeSecs;
    }
    const btn = document.getElementById('scaling-save-btn');
    btn.disabled = true;
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}`, {
        method: 'PATCH',
        body: JSON.stringify(payload),
      });
    } catch {
      btn.disabled = false;
      setError(errEl, 'Failed to save. Check your connection.');
      return;
    }
    btn.disabled = false;

    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let message = 'Failed to save.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      return;
    }

    statusEl.textContent = 'Saved.';
    setHidden(statusEl, false);
    snapshotSettingsSection('scaling');
    // Isolation is one half of the keep-warm advisory shown in the Hibernation
    // fieldset, so a save that switches mode re-evaluates it against the
    // floor the operator has in the field.
    updateMinWarmWarning(replicas, parseInt(document.getElementById('min-warm-replicas').value, 10) || 0, workerIsolation);
    await loadApps();
    await refreshDetailFleetState(slug);
    // Isolation is the one saved field the header menu is derived from, and the
    // metrics poller only ever merges status and deploying. Without folding the
    // reloaded app back into detailApp, switching multiplex to grouped leaves
    // Sleep in the header until the next navigation, and every click 409s; the
    // reverse switch hides Sleep on an app that can now take it.
    if (detailApp && detailApp.slug === settingsSlug) {
      const fresh = state.apps && state.apps.find(a => a.slug === settingsSlug);
      if (fresh) {
        Object.assign(detailApp, fresh);
        syncDetailHeaderActions(detailApp);
      }
    }
  }

  async function saveRenderPacing() {
    if (!settingsSlug) return;
    const errEl = document.getElementById('render-error');
    const statusEl = document.getElementById('render-status');
    setError(errEl, '');
    setHidden(statusEl, true);

    const parsed = parseRenderSeconds(document.getElementById('render-seconds').value);
    if (!parsed.ok) {
      setError(errEl, parsed.error);
      return;
    }

    // Pacing applies live (the PATCH calls proxy.ApplyRenderPacing), so this is
    // not a restart-and-drop-sessions change and needs no confirmation.
    const slug = settingsSlug;
    const btn = document.getElementById('render-save-btn');
    btn.disabled = true;
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}`, {
        method: 'PATCH',
        body: JSON.stringify({ render_seconds: parsed.value }),
      });
    } catch {
      btn.disabled = false;
      setError(errEl, 'Failed to save. Check your connection.');
      return;
    }
    btn.disabled = false;

    if (resp.status === 401) { await handleUnauthorized(); return; }
    let body = null;
    try { body = await resp.json(); } catch { /* non-JSON */ }
    if (!resp.ok) {
      setError(errEl, (body && body.error) || 'Failed to save.');
      return;
    }
    // The user may have navigated to another app while the PATCH was in flight;
    // that app's panel owns the controls now, so adopting this response's
    // advisory would describe the wrong app.
    if (settingsSlug !== slug) return;
    // Adopt the advisory for the value just persisted: pacing is in effect from
    // here on, and the block that described the previous setting is now stale.
    renderPacingBlock = (body && body.render_pacing) || null;
    updateRenderPacingAdvice();
    statusEl.textContent = 'Saved.';
    setHidden(statusEl, false);
    snapshotSettingsSection('render');
    await refreshDetailFleetState(slug);
  }

  // populateAutoscaleTab seeds the autoscale fieldset from the GET envelope
  // (app.autoscale_* columns and replicas) and gates editing on the same
  // canManageApp permission the rest of the General tab uses. Fleet-managed
  // apps remain editable; app-detail.js marks proven temporary overrides beside
  // the affected fields after this function seeds their current values.
  function populateAutoscaleTab(app) {
    const enabledInput = document.getElementById('autoscale-enabled');
    const minInput = document.getElementById('autoscale-min');
    const maxInput = document.getElementById('autoscale-max');
    const targetInput = document.getElementById('autoscale-target');
    const canEdit = canManageApp(state.user, app);

    enabledInput.checked = !!app.autoscale_enabled;
    minInput.value = String(app.autoscale_min_replicas ?? 1);
    maxInput.value = String(app.autoscale_max_replicas ?? 1);

    // The API stores 0 to mean "inherit"; the radio mirrors that: "default"
    // when target == 0 (no override), "custom" otherwise. The number input is
    // pre-filled with the explicit value so a toggle to custom does not lose it.
    const explicitTarget = Number(app.autoscale_target) || 0;
    const mode = explicitTarget > 0 ? 'custom' : 'default';
    document.querySelectorAll('input[name="autoscale-target-mode"]').forEach(r => {
      r.checked = r.value === mode;
    });
    targetInput.value = explicitTarget > 0 ? explicitTarget.toFixed(2) : '';
    targetInput.disabled = mode !== 'custom' || !canEdit;

    enabledInput.disabled = !canEdit;
    minInput.disabled = !canEdit;
    maxInput.disabled = !canEdit;
    document.querySelectorAll('input[name="autoscale-target-mode"]').forEach(r => {
      r.disabled = !canEdit;
    });
    document.getElementById('autoscale-save-btn').hidden = !canEdit;

    setError(document.getElementById('autoscale-error'), '');
    setHidden(document.getElementById('autoscale-status'), true);
    updateAutoscaleCeiling();
    snapshotSettingsSection('autoscale');
  }

  // onAutoscaleEnabledChange seeds sane bounds the moment autoscale is turned on,
  // so the operator never lands on the invalid 0/0 defaults. Min becomes the
  // current replica count (>=1); max becomes at least min.
  function onAutoscaleEnabledChange() {
    const enabled = document.getElementById('autoscale-enabled').checked;
    if (enabled) {
      const minEl = document.getElementById('autoscale-min');
      const maxEl = document.getElementById('autoscale-max');
      const current = Math.max(1, parseInt(document.getElementById('scaling-replicas').value, 10) || 1);
      if ((parseInt(minEl.value, 10) || 0) < 1) minEl.value = String(current);
      const minNow = parseInt(minEl.value, 10) || 1;
      if ((parseInt(maxEl.value, 10) || 0) < minNow) maxEl.value = String(Math.max(minNow, current));
    }
    updateAutoscaleCeiling();
    recomputeDirty('autoscale');
  }

  function onAutoscaleTargetModeChange() {
    const selected = document.querySelector('input[name="autoscale-target-mode"]:checked');
    const targetInput = document.getElementById('autoscale-target');
    const isCustom = selected && selected.value === 'custom';
    targetInput.disabled = !isCustom;
    if (isCustom && !targetInput.value) targetInput.value = '0.80';
    if (isCustom) targetInput.focus();
    setError(document.getElementById('autoscale-error'), '');
    setHidden(document.getElementById('autoscale-status'), true);
  }

  function updateAutoscaleCeiling() {
    const el = document.getElementById('autoscale-ceiling');
    if (!el) return;
    const enabled = document.getElementById('autoscale-enabled').checked;
    // Share the save-path parser so the live preview and the PATCH never
    // disagree about what value will be sent (parseInt would have truncated
    // "1.5" or "1e2" only here, leaving the preview off by orders of
    // magnitude until the user clicked Save).
    const min = parseReplicaBound(document.getElementById('autoscale-min').value);
    const max = parseReplicaBound(document.getElementById('autoscale-max').value);
    if (!enabled) {
      el.textContent = 'Autoscale disabled: the controller will not change the replica count.';
      return;
    }
    if (min === null || max === null || min < 1 || max < min) {
      el.textContent = '';
      return;
    }
    el.innerHTML = `Controller will steer between <strong>${min}</strong> and <strong>${max}</strong> replicas.`;
  }

  async function saveAutoscaleSettings() {
    if (!settingsSlug) return;
    const slug = settingsSlug;
    const errEl = document.getElementById('autoscale-error');
    const statusEl = document.getElementById('autoscale-status');
    setError(errEl, '');
    setHidden(statusEl, true);

    const { payload, error } = readAutoscaleForm(document);
    if (error) {
      setError(errEl, error);
      return;
    }

    const btn = document.getElementById('autoscale-save-btn');
    btn.disabled = true;
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}`, {
        method: 'PATCH',
        body: JSON.stringify({ autoscale: payload }),
      });
    } catch {
      btn.disabled = false;
      setError(errEl, 'Failed to save. Check your connection.');
      return;
    }
    btn.disabled = false;

    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let message = 'Failed to save.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      return;
    }

    statusEl.textContent = 'Saved.';
    setHidden(statusEl, false);
    snapshotSettingsSection('autoscale');
    await loadApps();
    await refreshDetailFleetState(slug);
  }

  function resetDangerZone() {
    const input = document.getElementById('delete-confirm');
    const btn = document.getElementById('delete-app-btn');
    input.value = '';
    btn.disabled = true;
    btn.textContent = 'Delete app';
    setError(document.getElementById('delete-error'), '');
  }

  // --- Environment tab ---

  let envEditingKey = null;

  async function refreshEnvList(slug) {
    const tbody = document.querySelector('#env-list tbody');
    tbody.innerHTML = '';
    const errEl = document.getElementById('env-form-error');

    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}/env`);
    } catch {
      setError(errEl, 'Failed to load environment variables.');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let message = 'Failed to load environment variables.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      return;
    }

    let data;
    try { data = await resp.json(); } catch { setError(errEl, 'Invalid response from server.'); return; }

    // Standard {items,...} list envelope; tolerate a bare array for resilience.
    const vars = Array.isArray(data) ? data : (data && Array.isArray(data.items) ? data.items : []);
    const empty = document.getElementById('env-empty');
    const table = document.getElementById('env-list');
    empty.hidden = vars.length > 0;
    table.hidden = vars.length === 0;

    // Resolve via the current detail app (carries can_manage, present on a
    // deep-link) before the cached list, so "+ Add variable" isn't wrongly
    // hidden for a manager who landed directly on /apps/:slug/configuration.
    const app = (detailApp && detailApp.slug === slug ? detailApp : null)
      || state.apps.find(a => a.slug === slug);
    const canWrite = canManageApp(state.user, app);
    document.getElementById('env-add-btn').hidden = !canWrite;

    for (const v of vars) {
      const tr = document.createElement('tr');
      const keyTd = document.createElement('td');
      keyTd.textContent = v.key;
      const valTd = document.createElement('td');
      valTd.className = v.secret ? 'env-secret' : '';
      valTd.textContent = v.secret ? '••••••' : v.value;
      const actTd = document.createElement('td');
      actTd.className = 'env-actions';
      if (canWrite) {
        const editBtn = document.createElement('button');
        editBtn.type = 'button';
        editBtn.textContent = 'Edit';
        editBtn.addEventListener('click', () => openEnvForm(v));
        const delBtn = document.createElement('button');
        delBtn.type = 'button';
        delBtn.className = 'env-btn-danger';
        delBtn.textContent = 'Delete';
        delBtn.addEventListener('click', () => deleteEnvVar(slug, v.key));
        actTd.append(editBtn, delBtn);
      }
      tr.append(keyTd, valTd, actTd);
      tbody.appendChild(tr);
    }
  }

  function openEnvForm(existing) {
    envEditingKey = existing ? existing.key : null;
    document.getElementById('env-form-heading').textContent = existing ? `Edit ${existing.key}` : 'Add variable';
    const keyInput = document.getElementById('env-form-key');
    const valueInput = document.getElementById('env-form-value');
    const secretInput = document.getElementById('env-form-secret');
    keyInput.value = existing ? existing.key : '';
    keyInput.readOnly = !!existing;
    valueInput.value = '';
    valueInput.placeholder = (existing && existing.secret) ? 'Enter new value (current value is write-only)' : '';
    secretInput.checked = existing ? existing.secret : false;
    secretInput.disabled = !!existing;
    document.getElementById('env-form-error').hidden = true;
    document.getElementById('env-form').hidden = false;
    keyInput.focus();
  }

  function closeEnvForm() {
    const form = document.getElementById('env-form');
    if (form) form.hidden = true;
    envEditingKey = null;
  }

  async function submitEnvForm(e) {
    e.preventDefault();
    const key = document.getElementById('env-form-key').value.trim();
    const value = document.getElementById('env-form-value').value;
    const secret = document.getElementById('env-form-secret').checked;
    const restart = document.getElementById('env-form-restart').checked;
    const errEl = document.getElementById('env-form-error');
    errEl.hidden = true;

    if (!/^[A-Z_][A-Z0-9_]*$/.test(key)) {
      setError(errEl, 'Key must match [A-Z_][A-Z0-9_]*');
      return;
    }

    const url = `/api/apps/${encodeURIComponent(settingsSlug)}/env/${encodeURIComponent(key)}` + (restart ? '?restart=true' : '');
    let resp;
    try {
      resp = await api(url, { method: 'PUT', body: JSON.stringify({ value, secret }) });
    } catch {
      setError(errEl, 'Network error.');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let message = 'Save failed.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      return;
    }

    closeEnvForm();
    await refreshEnvList(settingsSlug);
  }

  async function deleteEnvVar(slug, key) {
    if (!window.confirm(`Delete environment variable ${key}?`)) return;
    const errEl = document.getElementById('env-form-error');
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}/env/${encodeURIComponent(key)}?restart=true`, { method: 'DELETE' });
    } catch {
      setError(errEl, 'Network error.');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok && resp.status !== 204) {
      let message = 'Delete failed.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      return;
    }
    await refreshEnvList(slug);
  }

  // --- Data tab ---

  function encodeDataPath(p) {
    return p.split('/').map(encodeURIComponent).join('/');
  }

  async function refreshDataTab(slug, app) {
    const tbody = document.getElementById('data-list');
    tbody.innerHTML = '';
    const errEl = document.getElementById('data-error');
    setError(errEl, '');

    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}/data`);
    } catch {
      setError(errEl, 'Failed to load data files.');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let message = 'Failed to load data files.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      return;
    }

    let env;
    try { env = await resp.json(); } catch { setError(errEl, 'Invalid response from server.'); return; }

    // Standard {items,...} list envelope (quota_mb/used_bytes are sibling keys);
    // tolerate a bare array for resilience.
    const files = Array.isArray(env) ? env : (env && Array.isArray(env.items) ? env.items : []);
    const empty = document.getElementById('data-empty');
    const table = document.getElementById('data-list-table');
    empty.hidden = files.length > 0;
    table.hidden = files.length === 0;

    // Resolve the app for the write-permission check. Prefer an explicitly
    // passed app, then the current detail app (set on every detail mount and
    // carrying can_manage — this is what keeps a direct deep-link working after
    // an upload/delete re-renders via refreshDataTab(slug) with no app arg),
    // then the cached list as a last resort.
    const resolvedApp = app
      || (detailApp && detailApp.slug === slug ? detailApp : null)
      || state.apps.find(a => a.slug === slug);
    const canWrite = canManageApp(state.user, resolvedApp);
    document.getElementById('data-upload-form').hidden = !canWrite;

    const quotaEl = document.getElementById('data-quota');
    if (env) {
      const used = formatBytes(env.used_bytes || 0);
      quotaEl.textContent = env.quota_mb
        ? `Using ${used} of ${env.quota_mb} MiB`
        : `Using ${used} (no quota set)`;
    } else {
      quotaEl.textContent = '';
    }

    for (const f of files) {
      const tr = document.createElement('tr');

      const pathTd = document.createElement('td');
      pathTd.textContent = f.path;

      const sizeTd = document.createElement('td');
      sizeTd.textContent = formatBytes(f.size);

      const modTd = document.createElement('td');
      modTd.textContent = new Date(f.modified_at * 1000).toLocaleString();

      const actTd = document.createElement('td');
      actTd.className = 'env-actions';
      if (canWrite) {
        const delBtn = document.createElement('button');
        delBtn.type = 'button';
        delBtn.className = 'env-btn-danger';
        delBtn.textContent = 'Delete';
        delBtn.addEventListener('click', () => deleteDataFile(slug, f.path));
        actTd.appendChild(delBtn);
      }

      tr.append(pathTd, sizeTd, modTd, actTd);
      tbody.appendChild(tr);
    }
  }

  async function deleteDataFile(slug, path) {
    if (!window.confirm(`Delete ${path}?`)) return;
    const errEl = document.getElementById('data-error');
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}/data/${encodeDataPath(path)}`, { method: 'DELETE' });
    } catch {
      setError(errEl, 'Network error.');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok && resp.status !== 204) {
      let message = 'Delete failed.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      return;
    }
    await refreshDataTab(slug);
  }

  function uploadDataFile(event) {
    event.preventDefault();
    const slug = settingsSlug;
    if (!slug) return;

    const fileInput = document.getElementById('data-file-input');
    const destInput = document.getElementById('data-dest-input');
    const restartInput = document.getElementById('data-restart-input');
    const uploadBtn = document.getElementById('data-upload-btn');
    const progressEl = document.getElementById('data-progress');
    const errEl = document.getElementById('data-error');

    const file = fileInput.files[0];
    if (!file) return;

    const dest = destInput.value.trim() || file.name;
    const restart = restartInput.checked;
    const url = `/api/apps/${encodeURIComponent(slug)}/data/${encodeDataPath(dest)}${restart ? '?restart=true' : ''}`;

    setError(errEl, '');
    uploadBtn.disabled = true;
    progressEl.value = 0;
    progressEl.hidden = false;

    const xhr = new XMLHttpRequest();
    xhr.withCredentials = true;
    xhr.open('PUT', url);
    xhr.setRequestHeader('Content-Type', 'application/octet-stream');
    xhr.setRequestHeader('X-CSRF-Token', readCookie('csrf_token'));

    xhr.upload.addEventListener('progress', e => {
      if (e.lengthComputable) {
        progressEl.value = Math.round((e.loaded / e.total) * 100);
      }
    });

    xhr.onload = () => {
      progressEl.hidden = true;
      if (xhr.status >= 400) {
        let message = `Upload failed (${xhr.status} ${xhr.statusText})`;
        try {
          const b = JSON.parse(xhr.responseText);
          if (b && b.error) {
            message = b.error;
            if (b.used_bytes !== undefined && b.quota_bytes !== undefined) {
              message += ` — quota: ${formatBytes(b.used_bytes + b.requested_bytes)} requested, ${formatBytes(b.quota_bytes)} limit`;
            }
          }
        } catch { /* non-JSON */ }
        setError(errEl, message);
      } else {
        document.getElementById('data-upload-form').reset();
        setError(errEl, '');
        refreshDataTab(slug);
      }
    };

    xhr.onloadend = () => {
      uploadBtn.disabled = false;
      progressEl.hidden = true;
    };

    xhr.send(file);
  }

  async function refreshMemberList() {
    if (!settingsSlug) return;
    const list = document.getElementById('members-list');
    list.innerHTML = '<li class="loading-placeholder">Loading…</li>';
    let resp;
    try {
      resp = await api(`/api/apps/${settingsSlug}/members`);
    } catch { list.innerHTML = ''; return; }
    if (!resp.ok) { list.innerHTML = ''; return; }
    const memBody = await resp.json();
    // Standard {items,...} list envelope; tolerate a bare array for resilience.
    const members = Array.isArray(memBody) ? memBody : (memBody && Array.isArray(memBody.items) ? memBody.items : []);
    list.innerHTML = '';
    for (const m of members) {
      const li = document.createElement('li');
      const nameSpan = document.createElement('span');
      nameSpan.className = 'member-name';
      nameSpan.textContent = m.username;
      const roleSelect = document.createElement('select');
      roleSelect.className = 'member-role member-role-select';
      roleSelect.setAttribute('aria-label', `Role for ${m.username}`);
      for (const role of ['viewer', 'manager']) {
        const opt = document.createElement('option');
        opt.value = role;
        opt.textContent = role.charAt(0).toUpperCase() + role.slice(1);
        if (m.role === role) opt.selected = true;
        roleSelect.appendChild(opt);
      }
      roleSelect.dataset.previous = m.role;
      roleSelect.addEventListener('change', () => updateMemberRole(m.user_id, m.username, roleSelect));
      const revokeBtn = document.createElement('button');
      revokeBtn.textContent = 'Revoke';
      revokeBtn.setAttribute('aria-label', `Revoke access for ${m.username}`);
      revokeBtn.addEventListener('click', async () => {
        const slug = settingsSlug;
        if (!slug) return;
        try {
          const r = await api(`/api/apps/${slug}/members`, {
            method: 'DELETE',
            body: JSON.stringify({ user_id: m.user_id }),
          });
          if (r.ok) li.remove();
        } catch { /* network error — leave row in place */ }
      });
      li.appendChild(nameSpan);
      li.appendChild(roleSelect);
      li.appendChild(revokeBtn);
      list.appendChild(li);
    }
  }

  async function refreshGroupAccessList() {
    if (!settingsSlug) return;
    const list = document.getElementById('group-access-list');
    if (!list) return;
    list.innerHTML = '<li class="loading-placeholder">Loading…</li>';
    let resp;
    try {
      resp = await api(`/api/apps/${settingsSlug}/group-access`);
    } catch { list.innerHTML = ''; return; }
    if (!resp.ok) { list.innerHTML = ''; return; }
    const gaBody = await resp.json();
    // Standard {items,...} list envelope; tolerate a bare array for resilience.
    const rules = Array.isArray(gaBody) ? gaBody : (gaBody && Array.isArray(gaBody.items) ? gaBody.items : []);
    list.innerHTML = '';
    for (const rule of rules) {
      const li = document.createElement('li');
      const nameSpan = document.createElement('span');
      nameSpan.className = 'member-name';
      nameSpan.textContent = rule.group;
      const roleSpan = document.createElement('span');
      roleSpan.className = 'member-role';
      roleSpan.textContent = rule.role;
      if (rule.source === 'manifest') {
        const tag = document.createElement('span');
        tag.className = 'member-role';
        tag.textContent = '(manifest)';
        li.appendChild(nameSpan);
        li.appendChild(roleSpan);
        li.appendChild(tag);
        list.appendChild(li);
        continue; // manifest rules are managed by the bundle; not removable here
      }
      const removeBtn = document.createElement('button');
      removeBtn.textContent = 'Remove';
      removeBtn.setAttribute('aria-label', `Remove group rule ${rule.group}`);
      removeBtn.addEventListener('click', async () => {
        const slug = settingsSlug;
        if (!slug) return;
        try {
          const r = await api(`/api/apps/${slug}/group-access/${encodeURIComponent(rule.group)}`, { method: 'DELETE' });
          if (r.ok) li.remove();
        } catch { /* network error - leave row in place */ }
      });
      li.appendChild(nameSpan);
      li.appendChild(roleSpan);
      li.appendChild(removeBtn);
      list.appendChild(li);
    }
  }

  async function updateMemberRole(userId, username, selectEl) {
    const slug = settingsSlug;
    if (!slug) return;
    const newRole = selectEl.value;
    const previous = selectEl.dataset.previous || '';
    selectEl.disabled = true;
    let resp;
    try {
      resp = await api(`/api/apps/${slug}/members/${userId}`, {
        method: 'PATCH',
        body: JSON.stringify({ role: newRole }),
      });
    } catch {
      selectEl.disabled = false;
      if (previous) selectEl.value = previous;
      return;
    }
    selectEl.disabled = false;
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      if (previous) selectEl.value = previous; // revert to last-known role
      return;
    }
    selectEl.dataset.previous = newRole;
  }

  logPaneClose.addEventListener('click', closeLogs);

  usersRefresh.addEventListener('click', async () => {
    usersRefresh.disabled = true;
    usersRefresh.textContent = 'Refreshing…';
    try {
      if (identityServicesTab?.getAttribute('aria-selected') === 'true') await loadServiceAccounts();
      else await loadUsers();
    } finally {
      usersRefresh.disabled = false;
      usersRefresh.textContent = 'Refresh';
    }
  });
  identityPeopleTab?.addEventListener('click', () => showIdentityPanel('people'));
  identityServicesTab?.addEventListener('click', () => showIdentityPanel('services'));
	for (const tab of [identityPeopleTab, identityServicesTab].filter(Boolean)) {
		tab.addEventListener('keydown', event => {
			if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
			event.preventDefault();
			const destination = event.key === 'Home' ? identityPeopleTab
				: event.key === 'End' ? identityServicesTab
				: tab === identityPeopleTab ? identityServicesTab : identityPeopleTab;
			destination.focus();
			destination.click();
		});
	}
  serviceAccountsList?.addEventListener('click', event => {
    const create = event.target.closest('[data-service-create]');
    if (create) openServiceCredentialModal(create.dataset.serviceCreate);
    if (event.target.closest('[data-service-retry]')) loadServiceAccounts();
    const revoke = event.target.closest('[data-service-revoke]');
    if (revoke) revokeServiceCredential(revoke.dataset.serviceKey, revoke.dataset.serviceRevoke, revoke.dataset.serviceName, revoke);
    const rotate = event.target.closest('[data-service-rotate]');
    if (rotate) openServiceCredentialModal(rotate.dataset.serviceKey, {
      id: rotate.dataset.serviceRotate,
      name: rotate.dataset.serviceName,
      role: rotate.dataset.serviceRole,
      apps: JSON.parse(rotate.dataset.serviceApps || '[]'),
      unrestricted: rotate.dataset.serviceUnrestricted === 'true',
    });
  });
  serviceAccountsList?.addEventListener('input', event => {
    if (event.target.matches('[data-service-search-input]')) applyServiceCredentialFilters(event.target);
  });
  serviceAccountsList?.addEventListener('change', event => {
    if (event.target.matches('[data-service-state-filter]')) applyServiceCredentialFilters(event.target);
  });
  serviceCredentialForm?.addEventListener('submit', submitServiceCredential);
  serviceCredentialRole?.addEventListener('change', updateServiceCredentialPlan);
  serviceCredentialExpiry?.addEventListener('change', updateServiceCredentialPlan);
  serviceCredentialForm?.addEventListener('change', event => {
    if (event.target.matches('input[name="service-scope"]')) updateServiceCredentialPlan();
  });
  serviceCredentialApps?.addEventListener('input', () => {
    const ownsError = serviceCredentialApps.getAttribute('aria-invalid') === 'true';
    serviceCredentialApps.removeAttribute('aria-invalid');
    if (ownsError && !serviceCredentialError.hidden) setError(serviceCredentialError, '');
  });
  serviceCredentialName?.addEventListener('input', () => {
    const ownsError = serviceCredentialName.getAttribute('aria-invalid') === 'true';
    serviceCredentialName.removeAttribute('aria-invalid');
    serviceCredentialName.removeAttribute('aria-describedby');
    serviceCredentialName.removeAttribute('aria-errormessage');
    if (ownsError && !serviceCredentialError.hidden) setError(serviceCredentialError, '');
  });
  document.getElementById('service-credential-close')?.addEventListener('click', () => closeServiceCredentialModal());
  document.getElementById('service-credential-cancel')?.addEventListener('click', () => closeServiceCredentialModal());
  document.getElementById('service-credential-done')?.addEventListener('click', () => closeServiceCredentialModal({acknowledged: true}));
  serviceCredentialModal?.addEventListener('click', event => { if (event.target === event.currentTarget) closeServiceCredentialModal(); });
  document.getElementById('service-credential-copy')?.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(serviceCredentialToken.textContent);
      document.getElementById('service-credential-copy-status').textContent = 'Copied to clipboard';
    } catch { document.getElementById('service-credential-copy-status').textContent = 'Copy is unavailable in this browser'; }
  });
  document.getElementById('workers-refresh')?.addEventListener('click', () => loadWorkers());
  newUserButton.addEventListener('click', openNewUserModal);
  newUserClose.addEventListener('click', closeNewUserModal);
  newUserCancel.addEventListener('click', closeNewUserModal);
  newUserForm.addEventListener('submit', submitNewUser);
  newUserUsername.addEventListener('input', renderNewUserSnippet);

  // API tokens page + new-token modal wiring.
  if (newTokenButton) newTokenButton.addEventListener('click', openNewTokenModal);
  if (newTokenClose)  newTokenClose.addEventListener('click', closeNewTokenModal);
  if (newTokenCancel) newTokenCancel.addEventListener('click', closeNewTokenModal);
  if (tokenRevealDone) tokenRevealDone.addEventListener('click', closeNewTokenModal);
  if (newTokenForm)   newTokenForm.addEventListener('submit', submitNewToken);
  if (tokensRefresh)  tokensRefresh.addEventListener('click', () => loadTokens());
  if (newTokenModal)  newTokenModal.addEventListener('click', e => {
    if (e.target === e.currentTarget) closeNewTokenModal();
  });
  if (tokensList) tokensList.addEventListener('click', e => {
    const btn = e.target.closest('button[data-token-id]');
    if (btn) revokeToken(btn.getAttribute('data-token-id'), btn.getAttribute('data-token-name'), btn);
  });
  if (profileTokensLink) profileTokensLink.addEventListener('click', () => {
    // The data-nav link navigates to /tokens; close the profile modal behind it.
    closeProfileModal();
  });
  if (cliConnectApprove) cliConnectApprove.addEventListener('click', approveCLIConnect);
  if (cliConnectCancel) cliConnectCancel.addEventListener('click', cancelCLIConnect);
  if (tokenRevealCopy) {
    const copyLabel = tokenRevealCopy.querySelector('.copy-label');
    tokenRevealCopy.addEventListener('click', async () => {
      try {
        await navigator.clipboard.writeText(tokenRevealValue.textContent);
        tokenRevealCopy.classList.add('is-copied');
        if (copyLabel) copyLabel.textContent = 'Copied';
        if (tokenRevealStatus) tokenRevealStatus.textContent = 'Copied to clipboard';
        setTimeout(() => {
          tokenRevealCopy.classList.remove('is-copied');
          if (copyLabel) copyLabel.textContent = 'Copy';
          if (tokenRevealStatus) tokenRevealStatus.textContent = '';
        }, 2000);
      } catch { /* clipboard unavailable */ }
    });
  }

  // Theme: re-apply the saved preference (the inline head script already set it
  // pre-paint) and, while on 'system', track OS light/dark changes live; keep the
  // Appearance radios in sync when the OS flips.
  initTheme(window, reflectThemeRadios);
  document.querySelectorAll('input[name="theme-pref"]').forEach((el) => {
    el.addEventListener('change', () => {
      if (el.checked) setThemePreference(window, el.value);
    });
  });

  // Profile modal: open from the sidebar identity card; close/submit wiring.
  if (identityCard) identityCard.addEventListener('click', openProfileModal);
  if (profileClose) profileClose.addEventListener('click', closeProfileModal);
  if (profileForm)  profileForm.addEventListener('submit', submitProfile);
  if (profileModal) profileModal.addEventListener('click', e => {
    if (e.target === e.currentTarget) closeProfileModal();
  });

  // About modal: open from the sidebar footer.
  if (aboutButton) aboutButton.addEventListener('click', openAboutModal);
  if (aboutClose)  aboutClose.addEventListener('click', closeAboutModal);
  if (aboutModal)  aboutModal.addEventListener('click', e => {
    if (e.target === e.currentTarget) closeAboutModal();
  });

  if (newUserSnippetCopy) {
    const copyLabel  = newUserSnippetCopy.querySelector('.copy-label');
    const copyStatus = document.getElementById('new-user-snippet-status');
    newUserSnippetCopy.addEventListener('click', async () => {
      try {
        await navigator.clipboard.writeText(newUserSnippet.textContent);
        newUserSnippetCopy.classList.add('is-copied');
        if (copyLabel)  copyLabel.textContent  = 'Copied';
        if (copyStatus) copyStatus.textContent = 'Copied to clipboard';
        setTimeout(() => {
          newUserSnippetCopy.classList.remove('is-copied');
          if (copyLabel)  copyLabel.textContent  = 'Copy';
          if (copyStatus) copyStatus.textContent = '';
        }, 2000);
      } catch { /* clipboard unavailable */ }
    });
  }
  resetPwClose.addEventListener('click', closeResetPasswordModal);
  resetPwCancel.addEventListener('click', closeResetPasswordModal);
  resetPwForm.addEventListener('submit', submitResetPassword);
  auditPrev.addEventListener('click', () => loadAuditEvents(state.auditPage - 1, state.auditSelection));
  auditNext.addEventListener('click', () => loadAuditEvents(state.auditPage + 1, state.auditSelection));

  // Apps search + sort. Restore previous values from sessionStorage.
  const appsSearchEl = document.getElementById('apps-search');
  const appsSortEl   = document.getElementById('apps-sort');
  if (appsSearchEl) {
    try {
      const saved = sessionStorage.getItem('appsSearch');
      if (saved) appsSearchEl.value = saved;
    } catch { /* storage may be blocked */ }
    appsSearchEl.addEventListener('input', () => {
      try { sessionStorage.setItem('appsSearch', appsSearchEl.value); } catch { /* ignore */ }
      renderApps();
    });
  }
  if (appsSortEl) {
    try {
      const saved = sessionStorage.getItem('appsSort');
      if (saved) appsSortEl.value = saved;
    } catch { /* storage may be blocked */ }
    appsSortEl.addEventListener('change', () => {
      try { sessionStorage.setItem('appsSort', appsSortEl.value); } catch { /* ignore */ }
      renderApps();
    });
  }
  const appsSegmentEl = document.getElementById('apps-segment');
  if (appsSegmentEl) {
    try {
      const saved = sessionStorage.getItem('appsSegment');
      if (saved) appsSegmentEl.value = saved;
    } catch { /* storage may be blocked */ }
    appsSegmentEl.addEventListener('change', () => {
      try { sessionStorage.setItem('appsSegment', appsSegmentEl.value); } catch { /* ignore */ }
      renderApps();
    });
  }

  document.addEventListener('keydown', e => {
    if (e.key === 'Escape') {
      const scheduleModal = document.getElementById('schedule-form-modal');
      if (!deployModal.hidden) {
        closeDeployModal();
      } else if (!newAppModal.hidden) {
        closeNewAppModal();
      } else if (!newUserModal.hidden) {
        closeNewUserModal();
      } else if (profileModal && !profileModal.hidden) {
        closeProfileModal();
      } else if (aboutModal && !aboutModal.hidden) {
        closeAboutModal();
      } else if (!resetPwModal.hidden) {
        closeResetPasswordModal();
      } else if (scheduleModal && !scheduleModal.hidden) {
        closeScheduleForm();
      } else if (newTokenModal && !newTokenModal.hidden) {
        // Without this branch, Escape left the modal open after the secret
        // token value was already revealed in the DOM once (tokenRevealValue).
        closeNewTokenModal();
      } else if (serviceCredentialModal && !serviceCredentialModal.hidden) {
        closeServiceCredentialModal();
      } else if (!projectEditModal.hidden) {
        closeProjectEditModal();
      } else if (!document.getElementById('log-pane').hidden) {
        closeLogs();
      }
    }
  });

  document.addEventListener('click', () => {
    for (const el of document.querySelectorAll('.kebab-list')) el.hidden = true;
    for (const el of document.querySelectorAll('.kebab-menu [aria-expanded]')) el.setAttribute('aria-expanded', 'false');
  });

  newUserModal.addEventListener('click', e => {
    if (e.target === e.currentTarget) closeNewUserModal();
  });
  resetPwModal.addEventListener('click', e => {
    if (e.target === e.currentTarget) closeResetPasswordModal();
  });

  // --- Settings tabs: explicit-save wiring + dirty tracking ---
  // General + Resources (display name/project, memory/CPU limits).
  document.getElementById('general-save-btn').addEventListener('click', saveGeneralInfo);
  document.getElementById('resources-save-btn').addEventListener('click', saveResources);

  // Hibernation: mode radios + save button.
  document.querySelectorAll('input[name="hibernate-mode"]').forEach(r => {
    r.addEventListener('change', onHibernateModeChange);
  });
  document.getElementById('hibernate-save-btn').addEventListener('click', saveHibernateSettings);

  // Scaling: save + live admission-ceiling helper.
  document.getElementById('scaling-save-btn').addEventListener('click', saveScalingSettings);
  document.getElementById('scaling-replicas').addEventListener('input', updateScalingCeiling);
  document.getElementById('scaling-cap').addEventListener('input', updateScalingCeiling);

  // Render pacing: save + live advisory line.
  document.getElementById('render-save-btn').addEventListener('click', saveRenderPacing);
  document.getElementById('render-seconds').addEventListener('input', updateRenderPacingAdvice);

  // Worker isolation: live capacity helper on any input change.
  document.getElementById('worker-isolation').addEventListener('change', updateWorkerCapacity);
  // Live re-evaluation of the keep-warm advisory as either half is edited, so
  // the note reflects the pending choice and not only the last saved one.
  const refreshKeepWarmNote = () => {
    const replicas = parseInt(document.getElementById('scaling-replicas').value, 10);
    const minWarm = parseInt(document.getElementById('min-warm-replicas').value, 10);
    updateMinWarmWarning(Number.isFinite(replicas) ? replicas : 1,
      Number.isFinite(minWarm) ? minWarm : 0, currentIsolationChoice());
  };
  document.getElementById('worker-isolation').addEventListener('change', refreshKeepWarmNote);
  document.getElementById('min-warm-replicas').addEventListener('input', refreshKeepWarmNote);
  document.getElementById('worker-grouped-size').addEventListener('input', updateWorkerCapacity);
  document.getElementById('worker-max-workers').addEventListener('input', updateWorkerCapacity);
  document.getElementById('worker-warm-spares').addEventListener('input', updateWorkerCapacity);

  // Autoscale: enabling seeds sane bounds; bounds inputs + target-mode radios.
  document.querySelectorAll('input[name="autoscale-target-mode"]').forEach(r => {
    r.addEventListener('change', onAutoscaleTargetModeChange);
  });
  document.getElementById('autoscale-save-btn').addEventListener('click', saveAutoscaleSettings);
  document.getElementById('autoscale-enabled').addEventListener('change', onAutoscaleEnabledChange);
  document.getElementById('autoscale-min').addEventListener('input', updateAutoscaleCeiling);
  document.getElementById('autoscale-max').addEventListener('input', updateAutoscaleCeiling);

  // Environment tab: add button, form submit/cancel.
  document.getElementById('env-add-btn').addEventListener('click', () => openEnvForm(null));
  document.getElementById('env-form').addEventListener('submit', submitEnvForm);
  document.getElementById('env-form-cancel').addEventListener('click', closeEnvForm);

  // Data tab: upload form submit.
  document.getElementById('data-upload-form').addEventListener('submit', uploadDataFile);

  // Visibility: explicit Save. (Previously auto-applied on radio change; now
  // consistent with every other settings tab.)
  document.getElementById('visibility-save-btn').addEventListener('click', saveVisibility);

  // Register every settings section with the dirty tracker so Save stays
  // disabled until something changes and an "Unsaved changes" hint appears.
  registerSettingsSection('general',
    () => [document.getElementById('general-name'), document.getElementById('general-description'), document.getElementById('general-project')],
    'general-save-btn', 'general-dirty');
  registerSettingsSection('resources',
    () => [document.getElementById('resources-memory'), document.getElementById('resources-cpu')],
    'resources-save-btn', 'resources-dirty');
  registerSettingsSection('hibernate',
    () => [...document.querySelectorAll('input[name="hibernate-mode"]'),
           document.getElementById('hibernate-custom-minutes'),
           document.getElementById('min-warm-replicas')],
    'hibernate-save-btn', 'hibernate-dirty');
  registerSettingsSection('scaling',
    () => [
      document.getElementById('scaling-replicas'),
      document.getElementById('scaling-cap'),
      document.getElementById('worker-isolation'),
      document.getElementById('worker-grouped-size'),
      document.getElementById('worker-max-workers'),
      document.getElementById('worker-warm-spares'),
    ],
    'scaling-save-btn', 'scaling-dirty');
  registerSettingsSection('render',
    () => [document.getElementById('render-seconds')],
    'render-save-btn', 'render-dirty');
  registerSettingsSection('autoscale',
    () => [document.getElementById('autoscale-enabled'),
           document.getElementById('autoscale-min'),
           document.getElementById('autoscale-max'),
           ...document.querySelectorAll('input[name="autoscale-target-mode"]'),
           document.getElementById('autoscale-target')],
    'autoscale-save-btn', 'autoscale-dirty');
  registerSettingsSection('visibility',
    () => [...document.querySelectorAll('input[name="access-level"]')],
    'visibility-save-btn', 'visibility-dirty');

  // Grant button.
  document.getElementById('grant-btn').addEventListener('click', async () => {
    const grantBtn = document.getElementById('grant-btn');
    const username = document.getElementById('grant-username').value.trim();
    const errEl = document.getElementById('grant-error');
    errEl.hidden = true;
    if (!username) return;

    grantBtn.disabled = true;
    grantBtn.textContent = 'Granting…';
    try {
      // Grant by username: the server resolves it to a user id under the same
      // manage-app authorization, so the UI never needs a separate user-lookup
      // (which would require broader privilege than managing this app).
      const grantResp = await api(`/api/apps/${settingsSlug}/members`, {
        method: 'POST',
        body: JSON.stringify({ username }),
      });
      if (!grantResp.ok) {
        let grantMsg = grantResp.status === 404 ? 'User not found' : 'Grant failed';
        try {
          const j = await grantResp.json();
          if (j && j.error && grantResp.status !== 404) grantMsg = j.error;
        } catch { /* non-JSON */ }
        errEl.textContent = grantMsg;
        errEl.hidden = false;
        flashToast(grantMsg, 'error');
        return;
      }
      document.getElementById('grant-username').value = '';
      await refreshMemberList();
      flashToast(`Granted ${username} as viewer`, 'success');
    } finally {
      grantBtn.disabled = false;
      grantBtn.textContent = 'Grant';
    }
  });

  // Group access: add a group rule (bound once; uses current settingsSlug at call time).
  document.getElementById('group-access-add-btn')?.addEventListener('click', async () => {
    const nameEl = document.getElementById('group-access-name');
    const roleEl = document.getElementById('group-access-role');
    const errEl = document.getElementById('group-access-error');
    errEl.hidden = true;
    const group = nameEl.value.trim();
    if (!group || !settingsSlug) return;
    try {
      const resp = await api(`/api/apps/${settingsSlug}/group-access`, {
        method: 'POST',
        body: JSON.stringify({ group, role: roleEl.value }),
      });
      if (!resp.ok) { errEl.textContent = 'Add failed'; errEl.hidden = false; return; }
      const warn = resp.headers.get('X-ShinyHub-Warning');
      if (warn) flashToast(warn, 'info');
      nameEl.value = '';
      await refreshGroupAccessList();
    } catch { errEl.textContent = 'Network error'; errEl.hidden = false; }
  });

  // Danger zone: typed-confirmation unlocks the Delete button.
  document.getElementById('delete-confirm').addEventListener('input', (e) => {
    const btn = document.getElementById('delete-app-btn');
    btn.disabled = e.target.value !== settingsSlug;
  });

  document.getElementById('delete-app-btn').addEventListener('click', async () => {
    if (!settingsSlug) return;
    const slug = settingsSlug;
    const btn = document.getElementById('delete-app-btn');
    const errEl = document.getElementById('delete-error');
    setError(errEl, '');
    btn.disabled = true;
    btn.textContent = 'Deleting…';

    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}`, { method: 'DELETE' });
    } catch {
      setError(errEl, 'Network error');
      btn.disabled = false;
      btn.textContent = 'Delete app';
      return;
    }

    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok && resp.status !== 204) {
      let message = 'Delete failed.';
      try {
        const body = await resp.json();
        if (body && body.error) message = body.error;
      } catch { /* non-JSON */ }
      setError(errEl, message);
      btn.disabled = false;
      btn.textContent = 'Delete app';
      return;
    }

    // The app (and its settings) are gone; any unsaved edits are moot. Clear the
    // dirty state so the unsaved-changes guard doesn't veto this navigation and
    // strand the user on a now-deleted app's page.
    clearSettingsDirty();
    router.navigate('/');
  });

  function renderNewAppSnippet(slug) {
    const origin = window.location.origin;
    const effectiveSlug = slug && slug.length > 0 ? slug : '<slug>';
    newAppSnippet.textContent =
      `uv tool install shinyhub\n` +
      `shinyhub connect ${origin}\n` +
      `shinyhub deploy . --slug ${effectiveSlug} --wait`;
  }

  const newAppDivider = document.querySelector('#new-app-modal .handoff-card-divider');

  function resetNewAppModal() {
    newAppForm.hidden = false;
    if (newAppDivider) newAppDivider.hidden = false;
    newAppHandoff.hidden = true;
    newAppSnippetLabel.textContent = 'Deploy from your machine after creating';
    newAppStep.textContent = 'Step 1 of 2 · App details';
    newAppSlug.value = '';
    newAppName.value = '';
    newAppProject.value = '';
    if (newAppOptional) newAppOptional.open = false;
    setError(newAppError, '');
    newAppSubmit.disabled = false;
    newAppSubmit.textContent = 'Create';
    slugEdited = false;
    renderNewAppSnippet('');
  }

  function openNewAppModal() {
    resetNewAppModal();
    const firstApp = !state.apps || state.apps.length === 0;
    newAppHeading.textContent = firstApp ? 'Deploy your first app' : 'New app';
    newAppModal.hidden = false;
    modalTrap(newAppModal).activate();
    newAppName.focus();
  }

  function closeNewAppModal() {
    newAppModal.hidden = true;
    modalTrap(newAppModal).release();
    resetNewAppModal();
  }

  function showNewAppHandoff(app) {
    newAppForm.hidden = true;
    if (newAppDivider) newAppDivider.hidden = true;
    newAppHandoff.hidden = false;
    newAppStep.textContent = 'Step 2 of 2 · Deploy';
    newAppSnippetLabel.textContent = 'Deploy from your machine';
    renderNewAppSnippet(app.slug);
    newAppDeployNow.dataset.slug = app.slug;
    newAppDeployNow.dataset.name = app.name || app.slug;
    newAppDeployNow.focus();
  }

  async function submitNewApp(event) {
    event.preventDefault();
    setError(newAppError, '');

    const slug = newAppSlug.value.trim();
    const name = newAppName.value.trim();
    const projectSlug = newAppProject.value.trim();

    if (!SLUG_RE.test(slug)) {
      setError(newAppError, 'Slug must be 1-63 lowercase letters, digits, or hyphens, starting and ending with a letter or digit.');
      newAppSlug.focus();
      return;
    }
    if (name.length < 1 || name.length > 128) {
      setError(newAppError, 'Display name must be 1-128 characters.');
      newAppName.focus();
      return;
    }

    newAppSubmit.disabled = true;
    newAppSubmit.textContent = 'Creating…';

    let resp;
    try {
      resp = await api('/api/apps', {
        method: 'POST',
        body: JSON.stringify({ slug, name, project_slug: projectSlug }),
      });
    } catch {
      setError(newAppError, 'Network error');
      newAppSubmit.disabled = false;
      newAppSubmit.textContent = 'Create';
      return;
    }

    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (resp.status === 409) {
      setError(newAppError, 'Slug already taken. Pick another.');
      newAppSubmit.disabled = false;
      newAppSubmit.textContent = 'Create';
      return;
    }
    if (resp.status === 403) {
      setError(newAppError, 'You do not have permission to create apps.');
      newAppSubmit.disabled = false;
      newAppSubmit.textContent = 'Create';
      return;
    }
    if (!resp.ok) {
      let message = 'Failed to create app.';
      try {
        const body = await resp.json();
        if (body && body.error) message = body.error;
      } catch { /* non-JSON response */ }
      setError(newAppError, message);
      newAppSubmit.disabled = false;
      newAppSubmit.textContent = 'Create';
      return;
    }

    let createdApp = { slug, name };
    try {
      const body = await resp.json();
      if (body && typeof body.slug === 'string' && body.slug) {
        createdApp = body;
      }
    } catch { /* the submitted values are a complete fallback */ }
    showNewAppHandoff(createdApp);
    await loadApps();
  }

  function formatBytes(n) {
    if (n < 1024) return `${n} B`;
    if (n < 1048576) return `${(n / 1024).toFixed(1)} KB`;
    return `${(n / 1048576).toFixed(1)} MB`;
  }

  function resetDeployModal() {
    deploySummary.hidden = true;
    deployIgnoredRow.hidden = true;
    deployProgressWrap.hidden = true;
    deployProgressBar.value = 0;
    deployProgressText.textContent = '0%';
    deployResult.hidden = true;
    deployResultList.innerHTML = '';
    setError(deployError, '');
    deploySubmit.disabled = true;
    deploySubmit.textContent = 'Deploy';
    deployCancel.textContent = 'Cancel';
    deployFileInput.value = '';
    deployDropzone.classList.remove('dragover');
    if (deployState && deployState.xhr) {
      deployState.xhr.abort();
    }
    deployState = null;
  }

  function openDeployModal(app) {
    resetDeployModal();
    deployState = { slug: app.slug, appName: app.name, blob: null, fileCount: 0, rejections: new Map(), xhr: null };
    deployAppName.textContent = app.name ? `: ${app.name}` : '';
    renderDeployCliSnippet(app.slug);
    deployModal.hidden = false;
    modalTrap(deployModal).activate();
    // The dropzone is a mouse/drag drop region (not a focusable control); land
    // keyboard focus on the real "pick a file" button instead.
    deployPick.focus();
  }

  function renderDeployCliSnippet(slug) {
    const origin = window.location.origin;
    deployCliSnippet.textContent =
      `uv tool install shinyhub\n` +
      `shinyhub connect ${origin}\n` +
      `shinyhub deploy . --slug ${slug} --wait`;
  }

  function closeDeployModal() {
    resetDeployModal();
    deployModal.hidden = true;
    modalTrap(deployModal).release();
  }

  function renderDeploySummary(sourceName, blobSize, fileCount, rejections, rules) {
    deploySourceName.textContent = sourceName;
    deployFileCount.textContent = fileCount == null ? '—' : String(fileCount);
    deployBundleSize.textContent = formatBytes(blobSize);
    if (rejections && rejections.size > 0) {
      deployIgnoredRow.hidden = false;
      const labels = {
        rejectDataDir:     'data dir',
        rejectDatasetDir:  'dataset dir',
        rejectExtension:   'data extension',
        rejectFileSize:    'oversize file',
      };
      const parts = [];
      for (const [decision, paths] of rejections) {
        const label = labels[decision] || decision;
        parts.push(`${label}: ${paths.sort().join(', ')}`);
      }
      parts.sort();
      deployIgnoredList.textContent = parts.join('; ');
    } else {
      deployIgnoredRow.hidden = true;
    }
    deploySummary.hidden = false;

    const cap = rules && rules.maxBundleBytes > 0 ? rules.maxBundleBytes : 0;
    if (cap > 0 && blobSize > cap) {
      const mib = Math.round(cap / (1024 * 1024));
      setError(deployError, `Bundle is ${formatBytes(blobSize)} — exceeds the ${mib} MiB upload limit. Use the CLI for larger bundles.`);
      deploySubmit.disabled = true;
      return;
    }
    setError(deployError, '');
    deploySubmit.disabled = false;
  }

  async function acceptZipFile(file) {
    // Pre-built .zip — counting files would require parsing the central
    // directory, so leave the file count unknown and render it as "—".
    let rules;
    try {
      rules = await loadBundleRules();
    } catch (err) {
      console.error('loadBundleRules failed:', err);
      setError(deployError, 'Failed to load bundle rules from server.');
      return;
    }
    deployState.blob = file;
    deployState.fileCount = null;
    deployState.rejections = new Map();
    renderDeploySummary(file.name, file.size, null, null, rules);
  }

  function bindDropzoneEvents() {
    deployDropzone.addEventListener('click', (e) => {
      if (e.target.tagName === 'BUTTON') return; // pick button handles itself
      deployFileInput.click();
    });
    deployPick.addEventListener('click', (e) => {
      e.stopPropagation();
      deployFileInput.click();
    });
    deployFileInput.addEventListener('change', async () => {
      const file = deployFileInput.files && deployFileInput.files[0];
      if (!file) return;
      if (!file.name.toLowerCase().endsWith('.zip')) {
        setError(deployError, 'File picker only accepts .zip. Drop a folder to zip it in the browser.');
        return;
      }
      await acceptZipFile(file);
    });

    deployDropzone.addEventListener('dragover', (e) => {
      e.preventDefault();
      deployDropzone.classList.add('dragover');
    });
    deployDropzone.addEventListener('dragleave', () => {
      deployDropzone.classList.remove('dragover');
    });
    deployDropzone.addEventListener('drop', async (e) => {
      e.preventDefault();
      deployDropzone.classList.remove('dragover');
      await handleDrop(e);
    });
  }

  async function handleDrop(e) {
    const items = [...(e.dataTransfer?.items || [])];
    if (items.length === 0) {
      setError(deployError, 'Nothing to upload.');
      return;
    }
    // Single .zip file → treat as pre-built.
    if (items.length === 1 && items[0].kind === 'file') {
      const entry = items[0].webkitGetAsEntry ? items[0].webkitGetAsEntry() : null;
      const file = items[0].getAsFile();
      if (file && !entry?.isDirectory && file.name.toLowerCase().endsWith('.zip')) {
        await acceptZipFile(file);
        return;
      }
      if (entry && entry.isDirectory) {
        await zipFolderEntry(entry);
        return;
      }
    }
    // Folder drop may come in as multiple file entries OR a single directory
    // entry depending on the browser.  If any item is a directory entry, zip it.
    const directories = items
      .map(i => i.webkitGetAsEntry ? i.webkitGetAsEntry() : null)
      .filter(en => en && en.isDirectory);
    if (directories.length === 1) {
      await zipFolderEntry(directories[0]);
      return;
    }
    setError(deployError, 'Drop a single folder or a single .zip. Multiple files are not supported yet.');
  }

  let jsZipPromise = null;

  function loadJSZip() {
    if (!jsZipPromise) {
      jsZipPromise = new Promise((resolve, reject) => {
        const s = document.createElement('script');
        s.src = '/static/vendor/jszip.min.js';
        s.onload = () => {
          if (window.JSZip) resolve(window.JSZip);
          else reject(new Error('JSZip loaded but global is missing'));
        };
        s.onerror = () => reject(new Error('Failed to load JSZip'));
        document.head.appendChild(s);
      });
    }
    return jsZipPromise;
  }

  function readEntriesAll(dirReader) {
    // webkit's readEntries returns results in chunks; loop until empty.
    return new Promise((resolve, reject) => {
      const all = [];
      function readBatch() {
        dirReader.readEntries((entries) => {
          if (entries.length === 0) { resolve(all); return; }
          all.push(...entries);
          readBatch();
        }, reject);
      }
      readBatch();
    });
  }

  function fileFromEntry(entry) {
    return new Promise((resolve, reject) => entry.file(resolve, reject));
  }

  // Walks a DirectoryEntry tree, yielding { relativePath, file } for every
  // accepted file. rootEntry itself contributes no prefix — mirrors the CLI's
  // filepath.Rel(dir, path) behavior so the archive contains the folder's
  // contents, not the folder itself.
  //
  // Cache dirs (e.g. .venv, __pycache__) are silently skipped.
  // Data dirs, dataset dirs, oversize files, and data-extension files are
  // recorded in the rejections Map under the corresponding decision key.
  async function* walkFolder(rootEntry, rules, rejections) {
    const queue = [{ entry: rootEntry, path: '' }];
    while (queue.length > 0) {
      const { entry, path } = queue.shift();
      if (entry.isDirectory) {
        const reader = entry.createReader();
        const children = await readEntriesAll(reader);
        for (const child of children) {
          const childPath = path ? `${path}/${child.name}` : child.name;
          if (child.isDirectory) {
            // Inspect with size=0 — directory decisions don't depend on size.
            const decision = inspectBundleEntry(rules, childPath, 0);
            if (decision === 'skipCacheDir') {
              continue; // silent skip
            }
            if (decision !== 'accept') {
              const arr = rejections.get(decision) || [];
              arr.push(childPath);
              rejections.set(decision, arr);
              continue;
            }
          }
          queue.push({ entry: child, path: childPath });
        }
      } else if (entry.isFile) {
        const file = await fileFromEntry(entry);
        const decision = inspectBundleEntry(rules, path, file.size);
        if (decision === 'skipCacheDir') {
          continue;
        }
        if (decision !== 'accept') {
          const arr = rejections.get(decision) || [];
          arr.push(path);
          rejections.set(decision, arr);
          continue;
        }
        yield { relativePath: path, file };
      }
    }
  }

  // buildZipNative and supportsCompressionStream are defined in zip-writer.js,
  // loaded before this script in index.html.

  async function buildZipJSZip(fileList) {
    const JSZip = await loadJSZip();
    const zip = new JSZip();
    for (const { relativePath, file } of fileList) zip.file(relativePath, file);
    return zip.generateAsync({
      type: 'blob',
      compression: 'DEFLATE',
      compressionOptions: { level: 6 },
    });
  }

  async function zipFolderEntry(rootEntry) {
    setError(deployError, '');
    deploySubmit.disabled = true;
    deploySourceName.textContent = rootEntry.name + '/';
    deployFileCount.textContent = 'Reading…';
    deployBundleSize.textContent = '—';
    deploySummary.hidden = false;

    let rules;
    try {
      rules = await loadBundleRules();
    } catch (err) {
      console.error('loadBundleRules failed:', err);
      setError(deployError, 'Failed to load bundle rules from server.');
      return;
    }

    const rejections = new Map();
    const fileList = [];
    try {
      for await (const item of walkFolder(rootEntry, rules, rejections)) fileList.push(item);
    } catch (err) {
      console.error('walkFolder failed:', err);
      setError(deployError, 'Failed to read folder contents.');
      return;
    }

    if (fileList.length === 0) {
      setError(deployError, 'Folder is empty after filtering excluded paths.');
      return;
    }

    const hasAppPy = fileList.some(f => f.relativePath === 'app.py' || f.relativePath.endsWith('/app.py'));
    const hasAppR  = fileList.some(f => f.relativePath === 'app.R'  || f.relativePath.endsWith('/app.R'));
    if (!hasAppPy && !hasAppR) {
      setError(deployError, 'Bundle contains no app.py or app.R at any level. Did you drop the wrong folder?');
      return;
    }

    let blob;
    try {
      blob = supportsCompressionStream()
        ? await buildZipNative(fileList)
        : await buildZipJSZip(fileList);
    } catch (err) {
      console.error('zip build failed:', err);
      setError(deployError, 'Failed to build zip archive.');
      return;
    }

    deployState.blob = blob;
    deployState.fileCount = fileList.length;
    deployState.rejections = rejections;
    renderDeploySummary(rootEntry.name + '/', blob.size, fileList.length, rejections, rules);
  }

  function uploadBundle(slug, blob) {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      const form = new FormData();
      form.append('bundle', blob, 'bundle.zip');

      xhr.open('POST', `/api/apps/${encodeURIComponent(slug)}/deploy`, true);
      xhr.withCredentials = true;
      xhr.setRequestHeader('X-Shinyhub-Deploy-Channel', 'dashboard');
      const csrf = readCookie('csrf_token');
      if (csrf) xhr.setRequestHeader('X-CSRF-Token', csrf);

      xhr.upload.addEventListener('progress', (e) => {
        if (!e.lengthComputable) return;
        const pct = Math.floor((e.loaded / e.total) * 100);
        deployProgressBar.value = pct;
        deployProgressText.textContent = `${pct}%`;
      });
      xhr.addEventListener('load', () => {
        if (xhr.status >= 200 && xhr.status < 300) {
          let body = {};
          try { body = JSON.parse(xhr.responseText) || {}; }
          catch { /* keep empty body; callers treat as no manifest */ }
          resolve(body);
        } else {
          let msg = 'Deploy failed';
          try {
            const body = JSON.parse(xhr.responseText);
            if (body && body.error) msg = body.error;
          } catch { /* non-JSON; keep default */ }
          const err = new Error(msg);
          err.status = xhr.status;
          reject(err);
        }
      });
      xhr.addEventListener('error',  () => reject(new Error('Network error during upload')));
      xhr.addEventListener('abort',  () => {
        const err = new Error('Upload cancelled');
        err.code = 'UPLOAD_CANCELLED';
        reject(err);
      });
      xhr.addEventListener('timeout',() => reject(new Error('Upload timed out')));

      deployState.xhr = xhr;
      xhr.send(form);
    });
  }

  deploySubmit.addEventListener('click', async () => {
    if (!deployState || !deployState.blob) return;

    // Re-entry: after a successful deploy this button becomes "View logs".
    if (deployState.completed) {
      const slug = deployState.slug;
      closeDeployModal();
      openLogs(slug);
      return;
    }

    setError(deployError, '');
    deploySubmit.disabled = true;
    deployCancel.textContent = 'Close';
    deployProgressWrap.hidden = false;
    deployProgressBar.value = 0;
    deployProgressText.textContent = '0%';

    const { slug, blob } = deployState;

    let body;
    try {
      body = await uploadBundle(slug, blob);
    } catch (err) {
      if (err.code === 'UPLOAD_CANCELLED') {
        return; // closeDeployModal resets state
      }
      if (err.status === 401) { await handleUnauthorized(); return; }
      setError(deployError, err.message || 'Deploy failed');
      deploySubmit.disabled = false;
      deployCancel.textContent = 'Cancel';
      return;
    }

    deployProgressText.textContent = 'Deployed';
    await loadApps();

    // body.manifest is set when the bundle's shinyhub.toml applied [app]
    // settings or [[schedule]] blocks; show it inside the modal so the
    // operator can confirm what landed before jumping to logs. When the
    // bundle has no manifest, fall back to the original auto-redirect
    // behaviour so the no-config flow stays unchanged.
    const summaryLines = formatManifestSummary(body && body.manifest);
    if (summaryLines.length > 0) {
      renderDeployResult(deployResult, deployResultList, summaryLines);
      deployState.completed = true;
      deploySubmit.disabled = false;
      deploySubmit.textContent = 'View logs';
      return;
    }

    closeDeployModal();
    openLogs(slug);
  });

  deployModalClose.addEventListener('click', closeDeployModal);
  deployCancel.addEventListener('click', closeDeployModal);
  deployModal.addEventListener('click', (e) => {
    if (e.target === e.currentTarget) closeDeployModal();
  });
  bindDropzoneEvents();

  newAppButton.addEventListener('click', openNewAppModal);
  emptyStateCTA.addEventListener('click', openNewAppModal);
  newAppClose.addEventListener('click', closeNewAppModal);
  newAppCancel.addEventListener('click', closeNewAppModal);
  newAppDone.addEventListener('click', closeNewAppModal);
  newAppDeployNow.addEventListener('click', () => {
    const app = {
      slug: newAppDeployNow.dataset.slug,
      name: newAppDeployNow.dataset.name,
    };
    closeNewAppModal();
    openDeployModal(app);
  });
  newAppModal.addEventListener('click', e => {
    if (e.target === e.currentTarget) closeNewAppModal();
  });
  newAppForm.addEventListener('submit', submitNewApp);

  function closeProjectEditModal() {
    projectEditModal.hidden = true;
    modalTrap(projectEditModal).release();
  }
  projectEditSave.addEventListener('click', saveProjectEdit);
  projectEditCancel.addEventListener('click', closeProjectEditModal);
  projectEditModal.addEventListener('click', e => {
    if (e.target === e.currentTarget) closeProjectEditModal();
  });

  newAppName.addEventListener('input', () => {
    if (slugEdited) return;
    newAppSlug.value = slugify(newAppName.value);
    renderNewAppSnippet(newAppSlug.value);
  });
  newAppSlug.addEventListener('input', () => {
    slugEdited = newAppSlug.value.length > 0;
    renderNewAppSnippet(newAppSlug.value);
  });
  const newAppSnippetCopyLabel  = newAppSnippetCopy.querySelector('.copy-label');
  const newAppSnippetCopyStatus = document.getElementById('new-app-snippet-status');

  newAppSnippetCopy.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(newAppSnippet.textContent);
      newAppSnippetCopy.classList.add('is-copied');
      if (newAppSnippetCopyLabel)  newAppSnippetCopyLabel.textContent  = 'Copied';
      if (newAppSnippetCopyStatus) newAppSnippetCopyStatus.textContent = 'Copied to clipboard';
      setTimeout(() => {
        newAppSnippetCopy.classList.remove('is-copied');
        if (newAppSnippetCopyLabel)  newAppSnippetCopyLabel.textContent  = 'Copy';
        if (newAppSnippetCopyStatus) newAppSnippetCopyStatus.textContent = '';
      }, 1800);
    } catch { /* clipboard blocked; user can select text manually */ }
  });

  deployCliCopy.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(deployCliSnippet.textContent);
      deployCliCopy.classList.add('is-copied');
      if (deployCliCopyLabel)  deployCliCopyLabel.textContent  = 'Copied';
      if (deployCliCopyStatus) deployCliCopyStatus.textContent = 'Copied to clipboard';
      setTimeout(() => {
        deployCliCopy.classList.remove('is-copied');
        if (deployCliCopyLabel)  deployCliCopyLabel.textContent  = 'Copy';
        if (deployCliCopyStatus) deployCliCopyStatus.textContent = '';
      }, 1800);
    } catch { /* clipboard blocked; user can select text manually */ }
  });

  loginForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    setError(loginError, '');

    const submitBtn = loginForm.querySelector('button[type="submit"]');
    const submitLabel = submitBtn ? submitBtn.textContent : '';
    if (submitBtn) {
      submitBtn.disabled = true;
      submitBtn.textContent = 'Signing in…';
      submitBtn.setAttribute('aria-busy', 'true');
    }
    try {
      let response;
      try {
        response = await api('/api/auth/session', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({
            username: usernameInput.value,
            password: passwordInput.value,
          }),
        });
      } catch {
        setError(loginError, "Can't reach the server. Check your connection and try again.");
        return;
      }

      // Never say which of the two was wrong: that turns the form into a
      // username oracle.
      if (response.status === 401) {
        setError(loginError, 'Wrong username or password.');
        return;
      }
      if (response.status === 429) {
        setError(loginError, 'Too many attempts. Try again in a moment.');
        return;
      }
      if (!response.ok) {
        setError(loginError, 'Sign in failed. Try again in a moment.');
        return;
      }

      const payload = await response.json();
      showLoggedIn(payload);
      passwordInput.value = '';
      // Mirror the bootstrap path: await router.start() so state.apps is
      // populated before handleDeployHash consumes the persisted slug.
      // Without the await + handleDeployHash call here, a logged-out user
      // who landed on /#deploy=<slug> would persist the slug, log in, and
      // then never get the deploy modal (the bootstrap path doesn't run on
      // an interactive login).
      await router.start();
      consumeNextParam();
      await handleDeployHash();
    } finally {
      if (submitBtn) {
        submitBtn.disabled = false;
        submitBtn.textContent = submitLabel;
        submitBtn.removeAttribute('aria-busy');
      }
    }
  });

  refreshButton.addEventListener('click', async () => {
    refreshButton.disabled = true;
    refreshButton.textContent = 'Refreshing…';
    try {
      await loadApps();
    } finally {
      refreshButton.disabled = false;
      refreshButton.textContent = 'Refresh';
    }
  });

  // Set just before a deliberate full navigation away (logout) so the
  // unsaved-changes beforeunload guard does not strand the user: by then the
  // session is already revoked, so there is nothing left to save and the prompt
  // would only leave them logged-out but stuck on the old screen.
  let suppressUnloadGuard = false;

  logoutButton.addEventListener('click', async () => {
    // Distinguish transport errors from server-side rejections. The previous
    // unconditional showLoggedOut() lied to the user when the POST returned a
    // non-OK status (e.g. 403 from a missing CSRF cookie): the SPA showed the
    // login form locally but the server session stayed alive, so a refresh
    // logged them straight back in. We only clear local state when the server
    // actually killed the session — 204 (success) or 401 (already gone).
    // Anything else surfaces an error and keeps the user signed in.
    let resp;
    try {
      resp = await api('/api/auth/logout', {method: 'POST'});
    } catch {
      flashToast('Logout failed: network error', 'error');
      return;
    }
    if (resp.ok || resp.status === 401) {
      // Land on the front door. With auth-aware `/`, a now-sessionless request to
      // `/` serves the branding landing page when configured, otherwise the SPA
      // shell falls through to the login view - so a full navigation does the
      // right thing in both cases without the client knowing the branding config.
      suppressUnloadGuard = true;
      window.location.assign('/');
      return;
    }
    flashToast(`Logout failed (${resp.status})`, 'error');
  });

  // Desktop sidebar collapse (icon rail), persisted per browser.
  {
    const collapseBtn = document.getElementById('sidebar-collapse');
    const COLLAPSE_KEY = 'shinyhub.sidebarCollapsed';
    const applyCollapsed = (on) => {
      document.body.classList.toggle('sidebar-collapsed', on);
      delete document.body.dataset.sidebarTooltipHidden;
      if (collapseBtn) {
        const label = on ? 'Expand sidebar' : 'Collapse sidebar';
        collapseBtn.setAttribute('aria-expanded', on ? 'false' : 'true');
        collapseBtn.setAttribute('aria-label', label);
        collapseBtn.dataset.tooltip = label;
      }
    };
    try { applyCollapsed(localStorage.getItem(COLLAPSE_KEY) === '1'); } catch {}
    if (collapseBtn) {
      collapseBtn.addEventListener('click', () => {
        const on = !document.body.classList.contains('sidebar-collapsed');
        applyCollapsed(on);
        try { localStorage.setItem(COLLAPSE_KEY, on ? '1' : '0'); } catch {}
      });
    }
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && document.body.classList.contains('sidebar-collapsed')) {
        document.body.dataset.sidebarTooltipHidden = 'true';
      }
    });
    const restoreTooltip = (e) => {
      if (e.target && typeof e.target.closest === 'function' && e.target.closest('[data-tooltip]')) {
        delete document.body.dataset.sidebarTooltipHidden;
      }
    };
    document.addEventListener('focusin', restoreTooltip);
    document.addEventListener('mouseover', restoreTooltip);
  }

  // Mobile drawer: hamburger toggles, backdrop click + Escape close, and the
  // post-mount onNavigated() hook (in updateActiveNav) closes it after an
  // allowed navigation. createFocusTrap is reused for focus containment.
  sidebarDrawer = createSidebarDrawer({
    body: document.body,
    toggle: document.getElementById('sidebar-toggle'),
    backdrop: document.getElementById('sidebar-backdrop'),
    sidebar: document.getElementById('sidebar'),
    content: document.querySelector('.app-content'),
    createFocusTrap,
    doc: document,
  });
  {
    const drawerMedia = window.matchMedia('(max-width: 860px)');
    const syncDrawerAccessibility = () => sidebarDrawer.syncAccessibility(drawerMedia.matches);
    syncDrawerAccessibility();
    if (typeof drawerMedia.addEventListener === 'function') drawerMedia.addEventListener('change', syncDrawerAccessibility);
    const toggleBtn = document.getElementById('sidebar-toggle');
    const backdrop = document.getElementById('sidebar-backdrop');
    if (toggleBtn) toggleBtn.addEventListener('click', () => sidebarDrawer.toggle());
    if (backdrop) backdrop.addEventListener('click', () => sidebarDrawer.close());
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && sidebarDrawer.isOpen()) sidebarDrawer.close();
    });
  }

  async function loadProviders() {
    try {
      const resp = await api('/api/auth/providers');
      if (!resp.ok) return;
      const data = await resp.json();
      // Reveal only the SSO buttons the server reports configured; the GitHub and
      // Google buttons start hidden in index.html, so a failed fetch or a
      // native-only server never shows a dead button (see login-providers.js).
      applyLoginProviders(document, data);
    } catch (e) { /* non-critical */ }
  }

  // detailLastEnvelope is updated by renderOverview (via ctx.setDetailEnvelope)
  // and read by onMetrics to refresh the autoscale summary on each 10s poll
  // without requiring a full GET /api/apps/:slug refetch.
  let detailLastEnvelope = {};
  // detailApp holds the app object for the detail page currently shown, so the
  // static header kebab can restart the right app.
  let detailApp = null;

  const metrics = createMetricsController({
    intervalMs: 10000,
    onMetrics: (slug, m) => {
      // Grid card status badge: keep it live so a card opened while an app was
      // hibernating or deploying reflects the transition (m.status is the live
      // app-level status from /metrics; m.deploying flags an executing deploy).
      // Update the stored model too so a later re-render (search/sort/filter)
      // carries the fresh state.
      const liveStatus = {
        status: m.status,
        deploying: m.deploying,
        last_deployment_status: m.last_deployment_status,
      };
      const badgeEl = appGrid.querySelector(`.app-header .badge[data-slug="${slug}"]`);
      const gridApp = state.apps && state.apps.find(a => a.slug === slug);
      if (badgeEl && gridApp) {
        updateCardStatusBadge(badgeEl, gridApp, liveStatus, formatStatus);
      }
      // The badge merge above rewrites gridApp.status AND gridApp.deploying, so
      // re-derive the card's lifecycle menu from it, and only after it. Without
      // this an app that hibernated on the idle watchdog, or woke on a visitor's
      // request, would show its new badge while the menu still offered the
      // previous state's actions; run before the merge and the menu trails the
      // badge by one tick, offering Sleep and Stop on a deploy already in flight.
      if (gridApp) {
        const kebabEl = appGrid.querySelector(`.kebab-menu[data-slug="${slug}"]`);
        if (kebabEl) {
          syncCardActions(kebabEl, appCardActions(gridApp, canManageApp(state.user, gridApp)));
        }
      }
      // The index stays scan-first: refresh release/readiness facts from live
      // replica state, while detailed CPU/RAM remains on the app overview.
      const factsEl = appGrid.querySelector(`.app-card-facts[data-slug="${slug}"]`);
      if (factsEl && gridApp) renderCardFacts(factsEl, gridApp, m);
      // Detail header (only when the detail view for this slug is visible).
      const detailView = document.getElementById('app-detail-view');
      if (!detailView.hidden && location.pathname.startsWith(`/apps/${slug}`)) {
        // Keep the header status pill live too, on the same model merge the
        // card badge uses, so an open detail page flips to "Deploying" and
        // back during a deploy instead of freezing at its load-time state.
        const pillEl = document.getElementById('app-detail-status');
        if (pillEl && detailApp && detailApp.slug === slug) {
          updateStatusPill(pillEl, detailApp, liveStatus, formatStatus);
          // Same reason as the card menu above: the pill merge refreshes
          // detailApp.status, so the header kebab has to follow it.
          syncDetailHeaderActions(detailApp);
        }
        // Header metric tiles show fleet aggregates (per-replica detail lives in
        // the Overview replicas panel below). Set bare values; the labels are
        // static markup. CPU/Memory both summed across replicas → note that.
        const configured = (detailApp && detailApp.replicas) || 1;
        const stats = headerStats(m, configured);
        const setStat = (id, val, title) => {
          const el = document.getElementById(id);
          if (!el) return;
          el.textContent = val;
          el.classList.toggle('is-empty', val === '—');
          if (title !== undefined) el.title = title;
        };
        const naNote = 'Live CPU/RAM not collected for this backend (Fargate/remote tasks: see CloudWatch / the worker host)';
        const cpuRamNote = (stats.running && !stats.metricsAvailable)
          ? naNote
          : (stats.multiReplica ? 'Summed across replicas' : '');
        setStat('app-detail-cpu', stats.cpu, cpuRamNote);
        setStat('app-detail-ram', stats.ram, cpuRamNote);
        setStat('app-detail-sessions', stats.sessions, '');
        setStat('app-detail-replicas', stats.replicas, '');
        renderReplicasPanel(m);

        // Keep the stored envelope in sync with autoscale_status from the poll
        // so renderAutoscaleSummary's cooldown row reflects the latest event
        // without requiring a full GET /api/apps/:slug refetch.
        if (m.autoscale_status) {
          detailLastEnvelope.autoscale_status = m.autoscale_status;
        }
        const autoscaleDl = document.getElementById('autoscale-summary');
        if (autoscaleDl && detailLastEnvelope.autoscale_status) {
          const app = state.apps ? state.apps.find(a => a.slug === slug) : null;
          if (app) {
            renderAutoscaleSummary(autoscaleDl, summariseAutoscale(app, detailLastEnvelope));
          }
        }
      }
    },
    // A failed background metrics poll used to fail silently — no onError was
    // wired, so the CPU/RAM line just stopped updating with no visible signal.
    // A 401 specifically means the session died while the dashboard was open
    // in the background; log the user out instead of polling a dead session
    // forever. metrics-controller.js reports a non-2xx as `Error('status N')`.
    onError: (slug, err) => {
      if (err && /status 401/.test(err.message)) {
        handleUnauthorized();
      }
    },
  });

  function renderReplicasPanel(m) {
    const listEl = document.getElementById('overview-replicas-list');
    const capEl = document.getElementById('overview-replicas-cap');
    if (!listEl || !capEl) return;
    const cap = Number(m.sessions_cap || 0);
    capEl.textContent = cap > 0 ? `(cap ${cap} sessions/replica)` : '(uncapped)';
    const replicas = Array.isArray(m.replicas) ? m.replicas : [];
    if (replicas.length === 0) {
      listEl.innerHTML = '<li class="replicas-empty">No replicas tracked yet.</li>';
      return;
    }
    listEl.innerHTML = '';
    for (const r of replicas) {
      const li = document.createElement('li');
      li.className = 'replica-row';
      const status = r.status || 'stopped';
      const sessions = Number(r.sessions ?? -1);
      const sessionsText = sessions < 0
        ? '—'
        : (cap > 0 ? `${sessions}/${cap}` : String(sessions));
      const saturated = cap > 0 && sessions >= cap;
      // Use metricsText for honest display: PID-less replicas get "n/a".
      const { cpuText, ramText, physicalText, attributionNote, note } = metricsText(r);
      const cpuDisplay = (status === 'running') ? cpuText : '—';
      const ramDisplay = (status === 'running') ? ramText : '—';
      const physicalDisplay = (status === 'running') ? physicalText : '—';
      // Escape the backend label: r.tier/r.provider come from operator YAML config
      // and could contain HTML metacharacters if misconfigured.
      const backend = escapeHtml(backendLabel(r));
      // reason explains a degraded state (e.g. "worker unavailable" for a lost
      // replica). Server-supplied and fixed today, but escape defensively.
      const reason = reasonLabel(r);
      const reasonHTML = reason
        ? `<span class="replica-reason" title="${escapeHtml(reason)}">${escapeHtml(reason)}</span>`
        : '';
      li.innerHTML = `
        <div class="replica-identity">
          <span class="replica-index">#${r.index}</span>
          <span class="badge badge-${status}"${reason ? ` title="${escapeHtml(reason)}"` : ''}>${formatStatus(status)}</span>
          <span class="replica-backend" title="Backend/tier">${backend}</span>
        </div>
        ${reasonHTML}
        <div class="replica-measures">
          <span class="replica-measure replica-sessions${saturated ? ' replica-sessions-saturated' : ''}" title="Active sessions${cap > 0 ? ' / cap' : ''}"><span class="replica-measure-label">Sessions</span><span class="replica-measure-value">${sessionsText}</span></span>
          <span class="replica-measure replica-cpu"><span class="replica-measure-label">CPU</span><span class="replica-measure-value">${cpuDisplay}</span></span>
          <span class="replica-measure replica-ram"${note ? ` title="${note}"` : ''}><span class="replica-measure-label">Memory</span><span class="replica-measure-value">${ramDisplay}</span></span>
          <span class="replica-measure replica-physical"${attributionNote ? ` title="${escapeHtml(attributionNote)}"` : ''}><span class="replica-measure-label">PSS</span><span class="replica-measure-value">${physicalDisplay}</span></span>
        </div>
      `;
      listEl.appendChild(li);
    }
  }

  // A failed view mount used to leave the shell blank with no message (the
  // v0.8.7 dashboard-blanking class). The router now catches mount throws and
  // routes them here: hide every main view and reveal the generic error state
  // so one view's failure never takes down the whole dashboard.
  const routeErrorView = document.getElementById('route-error-view');
  const routeErrorReload = document.getElementById('route-error-reload');
  if (routeErrorReload) {
    routeErrorReload.addEventListener('click', () => location.reload());
  }
  function showRouteError(err) {
    console.error('route mount failed', err);
    for (const s of document.querySelectorAll('main > section')) s.hidden = true;
    if (routeErrorView) routeErrorView.hidden = false;
  }
  function clearRouteError() {
    if (routeErrorView) routeErrorView.hidden = true;
  }
  const router = createRouter({ onError: showRouteError, onMounted: clearRouteError });

  // Last-resort net for throws outside the router mount path (event handlers,
  // async callbacks): log them so failures are observable rather than silent,
  // and surface the inline banner. The router boundary above owns the full
  // error view; these never blank an otherwise-working view.
  window.addEventListener('error', (e) => {
    console.error('uncaught error', e.error || e.message);
    if (appError) setError(appError, 'Something went wrong. Some actions may not have completed.');
  });
  window.addEventListener('unhandledrejection', (e) => {
    console.error('unhandled rejection', e.reason);
    if (appError) setError(appError, 'Something went wrong. Some actions may not have completed.');
  });

  // Warn before navigating away (SPA route change or full unload) with unsaved
  // settings edits, so the explicit-save model never silently loses work.
  router.setNavGuard(confirmDiscardIfDirty);
  window.addEventListener('beforeunload', (e) => {
    if (!suppressUnloadGuard && anySettingsDirty()) { e.preventDefault(); e.returnValue = ''; }
  });

  function updateActiveNav(pathname) {
    // Scoped to the primary section nav so active state never leaks onto other
    // [data-nav] links (app cards, overview links, detail folder tabs).
    for (const el of document.querySelectorAll('#primary-nav [data-nav]')) {
      const url = new URL(el.href);
      const active = isPrimaryNavActive(url.pathname, pathname, state.user && state.user.role);
      el.classList.toggle('tab-active', active);
      if (active) el.setAttribute('aria-current', 'page'); else el.removeAttribute('aria-current');
    }
    // Sidebar app rows own their active state separately (slug-prefix, nested
    // tabs). Runs on every mount (post-allowed-navigation), and also closes the
    // mobile drawer there so a guard-vetoed navigation keeps it open.
    highlightSidebarApp(document.getElementById('sidebar-apps'), pathname);
    if (sidebarDrawer) sidebarDrawer.onNavigated();
  }

  const ctx = {
    state,
    metrics,
    api,
    navigate: (p, o) => router.navigate(p, o),
    onUnauthorized: handleUnauthorized,
    canManageApp,
    renderGridVerbatim,
    applyGridFilters: renderApps,
    // Surface (or clear, with "") a grid load error in the shared #app-error
    // banner so a failed initial /api/apps load is visible with a retry hint
    // instead of showing a silent empty grid.
    showError: (m) => { if (appError) setError(appError, m); },
    updateActiveNav,
    syncSidebar,
    setSettingsSlug: (slug) => { settingsSlug = slug; },
    populateGeneralTab,
    populateAutoscaleTab,
    populateAccessPanel,
    refreshEnvList,
    refreshDataTab,
    loadSchedules,
    loadSharedData,
    refreshMemberList,
    refreshGroupAccessList,
    // setDetailEnvelope is called by renderOverview in app-detail.js to keep
    // the stored envelope current so onMetrics can refresh the autoscale
    // summary on each 10s poll without a full GET /api/apps/:slug refetch.
    setDetailEnvelope: (env) => { detailLastEnvelope = env; },
    // setDetailApp records the app currently shown on the detail page so the
    // static header kebab (wired once below) knows which app to act on, and
    // renders the header's icon/monogram avatar for that app.
    setDetailApp: (app) => { detailApp = app; renderDetailHeaderAvatar(app); syncDetailHeaderActions(app); },
    // restart triggers POST /api/apps/:slug/restart (the same action as the
    // header kebab), exposed so the crash banner's Restart button can reuse it.
    restart: (slug) => restart(slug),
    // flashToast lets app-detail.js report failures (e.g. a failed rollback)
    // through the same accessible, auto-dismissing toast used everywhere else
    // in the dashboard instead of a blocking window.alert().
    flashToast,
  };

  const appDetailMount = mountAppDetail({
    ...ctx,
    openDeployModal,
  });

  // syncCardActions shows exactly the secondary rows that apply to a card's
  // current state, and hides the whole kebab when none do (a viewer, or an app
  // with nothing to act on). Called at render and again from the metrics
  // poller, so a card whose badge flips to "Sleeping" or "Running" on its own
  // does not keep offering the actions of the state it just left.
  function syncCardActions(kebab, acts) {
    if (!kebab) return;
    let anyShown = false;
    for (const [action, show] of [
      ['redeploy', acts.showRedeploy],
      ['restart', acts.showRestart],
      ['sleep', acts.showSleep],
      ['stop', acts.showStop],
      ['start', acts.showStart],
    ]) {
      const btn = kebab.querySelector(`[data-kebab="${action}"]`);
      if (!btn) continue;
      (btn.closest('li') || btn).hidden = !show;
      if (show) anyShown = true;
    }
    kebab.hidden = !anyShown;
    // A poll can empty the menu while it is open (an app that starts deploying
    // or waking offers nothing), so close it rather than leaving an open menu
    // inside a hidden container.
    if (!anyShown) {
      const ctl = kebabControls.get(kebab);
      if (ctl) ctl.close();
    }
  }

  // The detail header's kebab is static markup listing every lifecycle action,
  // so it has to be filtered per app. The decision comes from the same
  // appCardActions helper the grid cards use, which is what keeps the two
  // surfaces from disagreeing about what an app offers. The whole menu is
  // hidden when nothing applies, rather than opening onto an empty list.
  function syncDetailHeaderActions(app) {
    const acts = app ? appCardActions(app, canManageApp(state.user, app)) : null;
    const items = [
      ['app-detail-restart', !!acts && acts.showRestart],
      ['app-detail-sleep', !!acts && acts.showSleep],
      ['app-detail-stop', !!acts && acts.showStop],
      ['app-detail-start', !!acts && acts.showStart],
    ];
    let anyShown = false;
    for (const [id, show] of items) {
      const el = document.getElementById(id);
      if (!el) continue;
      (el.closest('li') || el).hidden = !show;
      if (show) anyShown = true;
    }
    const kebabBtn = document.getElementById('app-detail-kebab');
    const menu = kebabBtn && kebabBtn.closest('.kebab-menu');
    if (menu) menu.hidden = !anyShown;
  }

  // Wire the app-detail header actions once. They are static markup and always
  // act on detailApp, which is refreshed whenever a detail route mounts.
  {
    const dDeploy = document.getElementById('app-detail-deploy');
    if (dDeploy) {
      dDeploy.addEventListener('click', () => { if (detailApp) openDeployModal(detailApp); });
    }
    const dBtn = document.getElementById('app-detail-kebab');
    const dList = document.getElementById('app-detail-kebab-menu');
    wireKebab(dBtn, dList, dBtn && dBtn.closest('.kebab-menu'));
    const dRestart = document.getElementById('app-detail-restart');
    if (dRestart) {
      dRestart.addEventListener('click', () => { if (detailApp) restart(detailApp.slug, dRestart); });
    }
    const dSleep = document.getElementById('app-detail-sleep');
    if (dSleep) {
      dSleep.addEventListener('click', () => { if (detailApp) sleepApp(detailApp.slug, dSleep); });
    }
    const dStop = document.getElementById('app-detail-stop');
    if (dStop) {
      dStop.addEventListener('click', () => { if (detailApp) stopApp(detailApp.slug, dStop); });
    }
    const dStart = document.getElementById('app-detail-start');
    if (dStart) {
      dStart.addEventListener('click', () => { if (detailApp) startApp(detailApp.slug, dStart); });
    }
  }

  // Wire the emoji picker once, the same way: the trigger + popover are
  // static markup, the grid is built once via renderEmojiPicker (a fresh grid
  // on every render would stack listeners), and every handler acts on
  // whichever app is currently on the detail page (detailApp) rather than
  // re-wiring per render. renderIconPicker only ever toggles this markup's
  // `hidden` state.
  {
    const eBtn = document.getElementById('general-icon-emoji-btn');
    const ePopover = document.getElementById('general-icon-emoji-popover');
    wireKebab(eBtn, ePopover, eBtn && eBtn.closest('.emoji-picker'));
    const eGridSlot = document.getElementById('general-icon-emoji-grid');
    if (eGridSlot) {
      eGridSlot.replaceChildren(renderEmojiPicker(document, {
        onPick: (emoji) => { if (detailApp) setEmojiIcon(detailApp, emoji); },
      }));
    }
    const eInput = document.getElementById('general-icon-emoji-input');
    const eUseBtn = document.getElementById('general-icon-emoji-use-btn');
    if (eUseBtn) {
      eUseBtn.addEventListener('click', () => {
        if (detailApp && eInput) setEmojiIcon(detailApp, eInput.value);
      });
    }
    const eClearBtn = document.getElementById('general-icon-emoji-clear-btn');
    if (eClearBtn) {
      eClearBtn.addEventListener('click', () => { if (detailApp) clearEmojiIcon(detailApp); });
    }
  }

  // Hide every top-level page section before mounting a new view so a
  // sibling view never bleeds through. The previous-view unmount() handles
  // this on SPA transitions, but on a direct page load (e.g. reload on
  // /users) there is no previous view to clean up — the sections inherit
  // whatever showLoggedIn left them in.
  function hideAllPageViews() {
    overviewView.hidden = true;
    launchpadView.hidden = true;
    appsView.hidden = true;
    usersView.hidden = true;
    workersView.hidden = true;
    auditView.hidden = true;
    appDetailView.hidden = true;
    if (tokensView) tokensView.hidden = true;
  }

  // Apps is one role-adaptive destination: viewers get the focused opening
  // gallery; every management-capable role gets the operational grid.
  const mountAppsDestination = () => {
    hideAllPageViews();
    const role = ctx.state.user && ctx.state.user.role;
    if (appsSurfaceForSession(role, ctx.state.canManageApps) === 'viewer') return mountLaunchpad(ctx);
    const view = mountAppsGrid(ctx);
    updateActiveNav(location.pathname);
    return view;
  };

  // Role-adaptive home: fleet operators land on Overview; every other signed-in
  // role lands on its version of Apps. Mounted at both `/` and `/home` so a
  // branded landing page can link to a stable authenticated home.
  const mountHome = () => {
    const role = ctx.state.user && ctx.state.user.role;
    if (homeSurfaceForSession(role) === 'overview') {
      hideAllPageViews();
      return mountOverview(ctx);
    }
    return mountAppsDestination();
  };
  router.register('/', mountHome);
  router.register('/home', mountHome);
  // Preserve old bookmarks without preserving a second navigation concept.
  router.register('/launchpad', () => ctx.navigate('/apps', { replace: true }));
  router.register('/apps', mountAppsDestination);
  router.register('/users', () => {
    hideAllPageViews();
    return mountUsers({ ...ctx, loadUsers });
  });
  router.register('/tokens', () => {
    hideAllPageViews();
    if (tokensView) tokensView.hidden = false;
    renderCLIConnectRequest();
    loadTokens();
    return { unmount() { if (tokensView) tokensView.hidden = true; } };
  });
  router.register('/workers', () => {
    hideAllPageViews();
    return mountWorkers({ ...ctx, loadWorkers });
  });
  router.register('/audit-log', (_, search) => {
    hideAllPageViews();
    return mountAuditLog({ ...ctx, loadAuditEvents }, search);
  });
  router.register('/apps/:slug', (p) => {
    hideAllPageViews();
    return appDetailMount({ ...p, tab: 'overview' });
  });
  router.register('/apps/:slug/schedules/:scheduleId', (p) => {
    hideAllPageViews();
    return appDetailMount({ ...p, tab: 'schedules' });
  });
  router.register('/apps/:slug/:tab', (p) => {
    hideAllPageViews();
    return appDetailMount(p);
  });

  async function initialize() {
    // Persist any /#deploy=<slug> hash before the auth check so the slug
    // survives the login redirect in case the user is not authenticated.
    persistDeployHash();
    // Keep a CLI pairing request in this tab across an OAuth/OIDC redirect.
    // Those callbacks intentionally return to `/`; the pending request restores
    // `/tokens` only after the browser session has been verified.
    persistCLIConnectRequest();
    loadProviders();
    setError(loginError, '');

    let response;
    try {
      response = await api('/api/auth/me');
    } catch {
      showLoggedOut();
      setError(loginError, 'Network error');
      return;
    }

    if (response.status === 401) {
      showLoggedOut();
      return;
    }
    if (!response.ok) {
      showLoggedOut();
      setError(loginError, 'Failed to load session');
      return;
    }

    const payload = await response.json();
    showLoggedIn(payload);
    restoreCLIConnectRoute();
    await router.start();
    consumeNextParam();
    await handleDeployHash();
  }

  // Honour /#deploy=<slug> from the server-rendered empty-state page.
  // The hash is saved to sessionStorage so it survives the in-tab login
  // redirect: if the user is not authenticated when they visit
  // /#deploy=<slug>, the hash is persisted here and consumed after login
  // completes. After the apps list has loaded the deploy modal is opened
  // and the stored slug is cleared so refreshing doesn't re-trigger.
  //
  // We intentionally use sessionStorage rather than localStorage so the
  // pending intent stays scoped to the originating tab. localStorage would
  // bleed the slug to every tab on the same origin: a second tab logging
  // in as a different account would see the marker, fail the membership
  // check, and clear it — losing the original tab's deploy hint and
  // surfacing a confusing modal for an app it doesn't own.
  //
  // DEPLOY_HASH_RE captures a slug after `#deploy=`. The slug rule mirrors
  // SLUG_RE (RFC-1123 label) but is wrapped in a single capturing group.
  const DEPLOY_HASH_RE = /^#deploy=([a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)$/i;

  function persistDeployHash() {
    const match = DEPLOY_HASH_RE.exec(window.location.hash);
    if (!match) return;
    try { sessionStorage.setItem('pendingDeploy', match[1]); } catch { /* storage may be blocked */ }
    // Clear the hash without adding a history entry.
    history.replaceState(null, '', window.location.pathname + window.location.search);
  }

  // consumeNextParam reads a same-origin `next=<path>` query parameter from
  // the current URL, validates it, removes it from the address bar, and
  // navigates to it. Returns true if a navigation was triggered.
  //
  // Producer: internal/access/middleware.go renderAccessDeniedPage sends
  // unauthenticated browsers to /?next=<original>, where <original> is the
  // RequestURI of the protected app — which always lives under /app/<slug>
  // (the proxy path served outside the SPA). The earlier consumer handed
  // these to router.navigate(), but the SPA router has no /app/... route
  // so mount() fell through to the no-match branch and replaced the URL
  // with /. The user never made it back to the app they asked for.
  //
  // Strategy: any path that's not part of the SPA (anything outside the
  // SPA_ROUTE_PREFIXES allow-list) is dispatched as a full document
  // navigation via window.location.replace(). SPA paths still go through
  // the router so we don't reload the world for an in-app route.
  //
  // Same-origin enforcement: the value must be a relative path starting
  // with a single `/`, must not begin with `//` (protocol-relative), and
  // must not contain `\` (Windows-separator normalization). It must not be
  // `/` or `/login` (those would no-op or loop). Anything else falls
  // through silently.
  const SPA_ROUTE_PREFIXES = ['/launchpad', '/apps/', '/users', '/workers', '/audit-log', '/tokens'];
  function consumeNextParam() {
    const params = new URLSearchParams(window.location.search);
    const raw = params.get('next');
    if (!raw) return false;
    // Strip the param from the URL regardless of validity so the bad value
    // can't loop forever on refresh.
    params.delete('next');
    const search = params.toString();
    const cleaned = window.location.pathname + (search ? '?' + search : '');
    history.replaceState(null, '', cleaned);
    if (!raw.startsWith('/') || raw.startsWith('//') || raw.includes('\\')) return false;
    if (raw === '/' || raw === '/login') return false;
    const isSpaRoute = SPA_ROUTE_PREFIXES.some(p =>
      p.endsWith('/') ? raw.startsWith(p) : (raw === p || raw.startsWith(p + '/') || raw.startsWith(p + '?')),
    );
    if (isSpaRoute) {
      router.navigate(raw, { replace: true });
    } else {
      // Proxy / static / unknown path — SPA can't handle it. Hard-navigate
      // so /app/<slug>/ actually loads through internal/proxy.
      window.location.replace(raw);
    }
    return true;
  }

  async function handleDeployHash() {
    // Check hash first, then fall back to sessionStorage (per-tab so the
    // intent doesn't bleed across tabs / accounts).
    const hashMatch = DEPLOY_HASH_RE.exec(window.location.hash);
    let slug = hashMatch ? hashMatch[1] : null;
    if (!slug) {
      try { slug = sessionStorage.getItem('pendingDeploy'); } catch { /* storage may be blocked */ }
    }
    if (!slug) return;
    // Clear the hash without adding a history entry. Do this before any
    // async work so a refresh during the load doesn't re-trigger.
    history.replaceState(null, '', window.location.pathname + window.location.search);

    // Ensure the apps list is populated. The user may have landed on a
    // non-`/` route (e.g. /apps/foo) so the grid mount didn't run, or the
    // grid mount may not have completed yet on first paint.
    if (!state.apps.length) {
      await loadApps();
    }

    const app = state.apps.find(a => a.slug === slug);
    if (!app) {
      // App vanished between persist and consume (deleted, or user no longer
      // has visibility). Drop the pending slug so it doesn't loop forever.
      try { sessionStorage.removeItem('pendingDeploy'); } catch { /* ignore */ }
      return;
    }
    if (!canManageApp(state.user, app)) {
      try { sessionStorage.removeItem('pendingDeploy'); } catch { /* ignore */ }
      return;
    }
    // Only consume the stored slug once we've confirmed we can act on it.
    try { sessionStorage.removeItem('pendingDeploy'); } catch { /* ignore */ }
    const card = appGrid.querySelector(`.app-card[data-slug="${slug}"]`);
    if (card) card.scrollIntoView({behavior: 'smooth', block: 'center'});
    openDeployModal(app);
  }

  // --- Schedules + Shared Data ---

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));
  }

  // Show a brief accessible toast notification.
  // type: 'info' (default), 'success', or 'error'
  // Create a small icon-only copy button that copies `text` to the clipboard.
  // Uses the same is-copied / SVG pattern as the snippet copy buttons.
  function makeCopyButton(text, label) {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'copy-btn copy-btn-inline';
    btn.setAttribute('aria-label', label || `Copy ${text}`);
    btn.innerHTML = `
      <svg class="copy-icon-clipboard" viewBox="0 0 16 16" fill="none"
           stroke="currentColor" stroke-width="1.5" stroke-linecap="round"
           stroke-linejoin="round" aria-hidden="true">
        <rect x="4" y="3" width="8" height="10" rx="1.2"/>
        <path d="M6.5 3V2.25a.75.75 0 0 1 .75-.75h1.5a.75.75 0 0 1 .75.75V3"/>
      </svg>
      <svg class="copy-icon-check" viewBox="0 0 16 16" fill="none"
           stroke="currentColor" stroke-width="1.8" stroke-linecap="round"
           stroke-linejoin="round" aria-hidden="true">
        <path d="M3.5 8.5 6.5 11.5 12.5 5"/>
      </svg>
      <span class="sr-only" aria-live="polite"></span>`;
    btn.addEventListener('click', async (e) => {
      e.stopPropagation();
      e.preventDefault();
      try {
        await navigator.clipboard.writeText(text);
        btn.classList.add('is-copied');
        btn.querySelector('[aria-live]').textContent = 'Copied';
        setTimeout(() => {
          btn.classList.remove('is-copied');
          btn.querySelector('[aria-live]').textContent = '';
        }, 1800);
      } catch { /* clipboard blocked */ }
    });
    return btn;
  }

  // Show a brief accessible toast notification.
  // type: 'info' (default), 'success', or 'error'
  function flashToast(msg, type = 'info') {
    const t = document.createElement('div');
    t.className = `toast toast-${type}`;
    t.setAttribute('role', 'status');
    t.setAttribute('aria-live', 'polite');
    t.textContent = msg;
    document.body.appendChild(t);
    requestAnimationFrame(() => t.classList.add('show'));
    setTimeout(() => {
      t.classList.remove('show');
      setTimeout(() => t.remove(), 200);
    }, 3000);
  }

  // Keep the pre-save hint honest: the browser cannot reproduce the server's
  // cron parser and timezone/DST semantics exactly. Existing schedules show
  // the authoritative next fire returned by the server.
  function updateCronPreview(expr, timezone = '') {
    const el = document.getElementById('cron-preview');
    if (!el) return;
    const fields = expr.trim().split(/\s+/).filter(Boolean);
    const zone = timezone.trim() || 'the server timezone';
    if (!expr.trim()) { el.textContent = `Five-field cron · evaluated in ${zone}`; return; }
    el.textContent = fields.length === 5
      ? `Five-field cron · evaluated in ${zone}`
      : 'Cron expressions require five fields: minute, hour, day, month, weekday';
  }

  let mountedScheduleSurface = null;

  function scheduleDate(value, timezone, withDate = true) {
    if (!value) return '—';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return '—';
    try {
      return new Intl.DateTimeFormat(undefined, {
        ...(withDate ? {year: 'numeric', month: 'short', day: 'numeric'} : {}),
        hour: '2-digit', minute: '2-digit',
        timeZone: timezone || undefined,
        timeZoneName: 'short',
      }).format(date);
    } catch {
      return date.toLocaleString();
    }
  }

  function scheduleStatusMarkup(state) {
    return `<span class="schedule-state schedule-state-${escapeHtml(state.tone)}"><span aria-hidden="true"></span>${escapeHtml(state.label)}</span>`;
  }

  function stopScheduleSurface(surface = mountedScheduleSurface) {
    if (!surface || surface !== mountedScheduleSurface) return;
    surface.stopped = true;
    if (surface.timer) clearTimeout(surface.timer);
    if (surface.visibilityHandler) document.removeEventListener('visibilitychange', surface.visibilityHandler);
    mountedScheduleSurface = null;
  }

  function scheduleNextPoll(surface, schedules) {
    if (surface.stopped || surface !== mountedScheduleSurface || document.hidden) return;
    if (surface.timer) clearTimeout(surface.timer);
    const active = schedules.some(s => scheduleState(s).key === 'running' || activationState(s).active);
    surface.timer = setTimeout(() => refreshScheduleSurface(surface), active ? 5000 : 30000);
  }

  async function loadSchedules(slug, options) {
    let surface = mountedScheduleSurface;
    if (options || !surface || surface.slug !== slug) {
      stopScheduleSurface();
      surface = {
        slug,
        options: options || {},
        timer: null,
        stopped: false,
        visibilityHandler: null,
      };
      surface.visibilityHandler = () => {
        if (!document.hidden && !surface.stopped) refreshScheduleSurface(surface);
        else if (surface.timer) clearTimeout(surface.timer);
      };
      document.addEventListener('visibilitychange', surface.visibilityHandler);
      mountedScheduleSurface = surface;
    }
    const addBtn = document.getElementById('schedules-add-btn');
    if (addBtn) addBtn.hidden = !surface.options.canManage;
    await refreshScheduleSurface(surface);
    return () => stopScheduleSurface(surface);
  }

  async function refreshScheduleSurface(surface, {announce = false} = {}) {
    if (!surface || surface.stopped || surface !== mountedScheduleSurface) return;
    const container = document.getElementById('schedules-list');
    const statusEl = document.getElementById('schedules-surface-status');
    const focusedKey = document.activeElement?.dataset?.scheduleFocus || null;
    if (!container) return;
    if (surface.timer) clearTimeout(surface.timer);
    container.setAttribute('aria-busy', 'true');
    if (announce && statusEl) statusEl.textContent = 'Refreshing schedule status…';

    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(surface.slug)}/schedules`);
    } catch {
      if (!surface.stopped) {
        container.innerHTML = '<div class="schedule-load-error"><p>Schedule status could not be loaded.</p><button type="button" class="env-btn-secondary" data-schedule-retry>Try again</button></div>';
        container.querySelector('[data-schedule-retry]')?.addEventListener('click', () => refreshScheduleSurface(surface, {announce: true}));
        container.setAttribute('aria-busy', 'false');
        if (statusEl) statusEl.textContent = 'Schedule status could not be loaded. Try again.';
        scheduleNextPoll(surface, []);
      }
      return;
    }
    if (surface.stopped || surface !== mountedScheduleSurface) return;
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      container.innerHTML = '<div class="schedule-load-error"><p>Schedule status could not be loaded.</p><button type="button" class="env-btn-secondary" data-schedule-retry>Try again</button></div>';
      container.querySelector('[data-schedule-retry]')?.addEventListener('click', () => refreshScheduleSurface(surface, {announce: true}));
      container.setAttribute('aria-busy', 'false');
      if (statusEl) statusEl.textContent = 'Schedule status could not be loaded. Try again.';
      scheduleNextPoll(surface, []);
      return;
    }
    const schedBody = await resp.json();
    const schedules = Array.isArray(schedBody) ? schedBody : (schedBody && Array.isArray(schedBody.items) ? schedBody.items : []);
    surface.schedules = schedules;
    renderScheduleSummary(schedules);
    renderScheduleList(surface, schedules);
    container.setAttribute('aria-busy', 'false');
    if (statusEl) statusEl.textContent = announce ? 'Schedule status updated.' : '';

    const selected = surface.options.selectedScheduleID == null
      ? null
      : schedules.find(s => String(s.id) === String(surface.options.selectedScheduleID));
    if (surface.options.selectedScheduleID != null && !selected) {
      router.navigate(`/apps/${encodeURIComponent(surface.slug)}/schedules`, {replace: true});
      return;
    }
    const forceDetail = announce || (selected && scheduleState(selected).key === 'running');
    await renderScheduleDetail(surface, selected, forceDetail);
    if (focusedKey) {
      const replacement = Array.from(document.querySelectorAll('[data-schedule-focus]'))
        .find(el => el.dataset.scheduleFocus === focusedKey);
      replacement?.focus({preventScroll: true});
    }
    scheduleNextPoll(surface, schedules);
  }

  function renderScheduleSummary(schedules) {
    const summary = scheduleSummary(schedules);
    const set = (id, value, attention = false) => {
      const el = document.getElementById(id);
      if (!el) return;
      el.textContent = value;
      el.classList.toggle('has-attention', attention);
    };
    set('schedules-summary-attention', String(summary.attention), summary.attention > 0);
    set('schedules-summary-compatible', `${summary.compatible} / ${summary.total}`);
    set('schedules-summary-active', String(summary.running + summary.activating));
    set('schedules-summary-next', summary.nextFire ? scheduleDate(summary.nextFire.toISOString()) : '—');
  }

	function deletedActivationHistoryMarkup(surface, schedules) {
		const retained = deletedScheduleActivations(surface.options.latestActivations, schedules);
		if (retained.length === 0) return '';
		const rows = retained.map(activation => {
			const state = activationState({latest_activation: activation});
			const run = activation.schedule_run_id ? `Run #${activation.schedule_run_id}` : 'Source run retained as a snapshot';
			const generation = Number.isFinite(Number(activation.target_generation))
				? `Generation ${activation.target_generation}`
				: 'Generation unavailable';
			const occurredAt = activation.finished_at || activation.updated_at || activation.created_at;
			const identity = activation.id ? `Activation #${activation.id}` : 'Activation ID unavailable';
			const when = occurredAt ? scheduleDate(occurredAt) : 'Time unavailable';
			return `<li>
				<div class="schedule-retained-head"><strong>${escapeHtml(activation.schedule_name || 'Deleted schedule')}</strong>${scheduleStatusMarkup(state)}</div>
				<p>${escapeHtml(`${identity} · ${run} · ${generation}`)}</p>
				<p>${occurredAt ? `<time datetime="${escapeHtml(occurredAt)}">${escapeHtml(when)}</time> · ` : ''}source schedule deleted</p>
				${activation.last_error ? `<p class="schedule-retained-error">${escapeHtml(activation.last_error)}</p>` : ''}
			</li>`;
		}).join('');
		return `<section class="schedule-retained-history" aria-labelledby="schedule-retained-title">
			<div class="schedule-history-heading"><div><h3 id="schedule-retained-title">Retained serving-data activation outcomes</h3><p>Durable outcomes retained after their source schedules were deleted.</p></div></div>
			<ul>${rows}</ul>
		</section>`;
	}

  function renderScheduleList(surface, schedules) {
    const container = document.getElementById('schedules-list');
    const addBtn = document.getElementById('schedules-add-btn');
    if (!container) return;
    container.querySelectorAll('.schedule-actions-menu').forEach(menu => kebabControls.get(menu)?.close());
    if (addBtn) addBtn.hidden = !surface.options.canManage;
    if (schedules.length === 0) {
			const retained = deletedActivationHistoryMarkup(surface, schedules);
      container.innerHTML = `<div class="schedules-empty"><h3>No scheduled jobs yet</h3><p>Schedules run data preparation and maintenance commands independently of the app process.</p>${surface.options.canManage ? '<button type="button" class="btn-primary" data-schedule-empty-add>Add first schedule</button>' : ''}</div>${retained}`;
      container.querySelector('[data-schedule-empty-add]')?.addEventListener('click', () => openScheduleForm(surface.slug, null));
      return;
    }

    const rows = schedules.map(s => {
      const state = scheduleState(s);
      const activation = activationState(s);
      const compatibility = producerCompatibilityState(s);
      const tz = s.effective_timezone || 'UTC';
      const lastRun = s.last_run_at ? scheduleDate(s.last_run_at, tz) : 'No completed run recorded';
      const lastSuccess = Number.isFinite(s.last_success_age_s) ? `Last success ${formatAge(s.last_success_age_s)}` : 'No successful run recorded';
      const nextFire = s.enabled === false ? 'Paused' : scheduleDate(s.next_fire, tz);
      const selected = String(s.id) === String(surface.options.selectedScheduleID);
      const actions = surface.options.canManage ? `
        <button type="button" class="schedule-run-btn" data-action="run" data-id="${s.id}" data-name="${escapeHtml(s.name)}" data-schedule-focus="schedule-${s.id}-run">
          <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M5.25 3.5 12 8l-6.75 4.5z"/></svg><span>Run now</span>
        </button>
        <div class="kebab-menu schedule-actions-menu">
          <button type="button" aria-haspopup="menu" aria-expanded="false" aria-label="More actions for ${escapeHtml(s.name)}">
            <svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="3" cy="8" r="1.25"/><circle cx="8" cy="8" r="1.25"/><circle cx="13" cy="8" r="1.25"/></svg>
          </button>
          <ul class="kebab-list" role="menu" hidden>
            <li role="none"><button type="button" role="menuitem" data-action="edit" data-schedule='${escapeHtml(JSON.stringify(s))}' data-schedule-focus="schedule-${s.id}-edit">Edit schedule</button></li>
            <li role="none" class="schedule-menu-danger"><button type="button" role="menuitem" data-action="delete" data-id="${s.id}" data-name="${escapeHtml(s.name)}" data-schedule-focus="schedule-${s.id}-delete">Delete schedule</button></li>
          </ul>
        </div>` : '';
      return `<tr${selected ? ' class="is-selected" aria-current="true"' : ''}>
        <td data-label="Schedule">
          <div class="schedule-cell-stack">
            <a class="schedule-name-link" data-nav href="/apps/${encodeURIComponent(surface.slug)}/schedules/${s.id}" data-schedule-focus="schedule-${s.id}-open">${escapeHtml(s.name)}</a>
            <span class="schedule-definition"><code>${escapeHtml(s.cron_expr)}</code> · ${escapeHtml(tz)}${s.timezone_inherited ? ' · inherited' : ''}</span>
            <span class="schedule-command" title="${escapeHtml((s.command || []).join(' '))}">${escapeHtml((s.command || []).join(' '))}</span>
            ${dstAdvisoryMarkup(s)}
          </div>
        </td>
        <td data-label="Latest run">
          <div class="schedule-cell-stack">
            ${scheduleStatusMarkup(state)}
            <span class="schedule-cell-primary">${escapeHtml(lastRun)}</span>
            <span class="schedule-cell-secondary">${escapeHtml(lastSuccess)}</span>
          </div>
        </td>
		<td data-label="Code/data compatibility">
		  <div class="schedule-cell-stack">
			${scheduleStatusMarkup(compatibility)}
			<span class="schedule-cell-primary">${escapeHtml(deployTriggerLabel(s.deploy_trigger))}</span>
			<span class="schedule-cell-secondary">${escapeHtml(compatibility.detail)}</span>
          </div>
        </td>
        <td data-label="Next run">
          <div class="schedule-cell-stack"><span class="schedule-cell-primary">${escapeHtml(nextFire)}</span></div>
        </td>
        <td class="table-actions" data-label="Actions"><div class="schedule-row-actions">${actions}</div></td>
      </tr>`;
    }).join('');
    container.innerHTML = `<table>
      <thead><tr><th scope="col">Schedule</th><th scope="col">Latest run</th><th scope="col">Code/data compatibility</th><th scope="col">Next run</th><th scope="col"><span class="sr-only">Actions</span></th></tr></thead>
      <tbody>${rows}</tbody>
    </table>${deletedActivationHistoryMarkup(surface, schedules)}`;

    container.querySelectorAll('.schedule-actions-menu').forEach(menu => {
      kebabControls.set(menu, wireKebab(menu.querySelector(':scope > button'), menu.querySelector('.kebab-list'), menu.closest('tr')));
    });

    container.querySelectorAll('[data-action]').forEach(btn => {
      btn.addEventListener('click', () => {
        const action = btn.dataset.action;
        const id = parseInt(btn.dataset.id, 10);
        if (action === 'run') runScheduleNow(surface.slug, id, btn.dataset.name, btn);
        else if (action === 'delete') deleteSchedule(surface.slug, id, btn.dataset.name, btn);
        else if (action === 'edit') openScheduleForm(surface.slug, JSON.parse(btn.dataset.schedule));
      });
    });
  }

  async function renderScheduleDetail(surface, schedule, force = false) {
    const workspace = document.getElementById('schedules-workspace');
    const detail = document.getElementById('schedule-detail');
    if (!workspace || !detail) return;
    workspace.classList.toggle('has-detail', !!schedule);
    detail.hidden = !schedule;
    if (!schedule) {
      surface.renderedDetailID = null;
      surface.renderedDetailSignature = null;
      detail.replaceChildren();
      return;
    }
    const detailSignature = scheduleDetailSignature(schedule);
    if (!force && String(surface.renderedDetailID) === String(schedule.id) && surface.renderedDetailSignature === detailSignature) return;
    surface.renderedDetailID = schedule.id;
    surface.renderedDetailSignature = detailSignature;

    const state = scheduleState(schedule);
    const activation = activationState(schedule);
    const compatibility = producerCompatibilityState(schedule);
    const latestActivation = schedule.latest_activation;
    const timezone = schedule.effective_timezone || 'UTC';
    detail.setAttribute('aria-busy', 'true');
    detail.querySelectorAll('.schedule-actions-menu').forEach(menu => kebabControls.get(menu)?.close());
    detail.innerHTML = `
      <div class="schedule-detail-head">
        <a class="schedule-detail-back" data-nav href="/apps/${encodeURIComponent(surface.slug)}/schedules">All schedules</a>
        <div class="schedule-detail-title-row">
          <div><h2 id="schedule-detail-title">${escapeHtml(schedule.name)}</h2>${scheduleStatusMarkup(state)}</div>
          ${surface.options.canManage ? `<div class="schedule-detail-actions">
            <button type="button" class="schedule-run-btn" data-detail-action="run" data-schedule-focus="schedule-${schedule.id}-detail-run"><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M5.25 3.5 12 8l-6.75 4.5z"/></svg><span>Run now</span></button>
            <div class="kebab-menu schedule-actions-menu" data-schedule-actions-menu>
              <button type="button" aria-haspopup="menu" aria-expanded="false" aria-label="More actions for ${escapeHtml(schedule.name)}"><svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="3" cy="8" r="1.25"/><circle cx="8" cy="8" r="1.25"/><circle cx="13" cy="8" r="1.25"/></svg></button>
              <ul class="kebab-list" role="menu" hidden>
                <li role="none"><button type="button" role="menuitem" data-detail-action="edit" data-schedule-focus="schedule-${schedule.id}-detail-edit">Edit schedule</button></li>
                <li role="none" class="schedule-menu-danger"><button type="button" role="menuitem" data-detail-action="delete" data-schedule-focus="schedule-${schedule.id}-detail-delete">Delete schedule</button></li>
              </ul>
            </div>
          </div>` : ''}
        </div>
      </div>
      ${schedule.roll_feasibility_advisory ? `<div class="schedule-roll-advisory"><strong>Roll capacity warning</strong><span>${escapeHtml(schedule.roll_feasibility_advisory)}</span></div>` : ''}
      <section class="schedule-compatibility-panel schedule-compatibility-${compatibility.tone}" aria-labelledby="schedule-compatibility-title">
        <div class="schedule-history-heading">
          <div><h3 id="schedule-compatibility-title">Code and data compatibility</h3><p>${escapeHtml(compatibility.detail)}</p></div>
          <div class="schedule-activation-actions">${scheduleStatusMarkup(compatibility)}${surface.options.canManage && compatibility.attention ? `<button type="button" class="btn-primary" data-detail-action="retry-convergence">Retry producer</button>` : ''}</div>
        </div>
        <dl class="schedule-compatibility-facts">
          <div><dt>Deployment policy</dt><dd>${escapeHtml(deployTriggerLabel(schedule.deploy_trigger))}</dd></div>
          <div><dt>Convergence</dt><dd>${escapeHtml(schedule.convergence_status || (schedule.deploy_trigger_satisfied ? 'satisfied' : 'not requested'))}</dd></div>
          <div><dt>Current code</dt><dd><strong>${escapeHtml(schedule.current_app_version || 'Unknown version')}</strong><code>${escapeHtml(schedule.current_content_digest || 'Digest unavailable')}</code></dd></div>
          <div><dt>Data producer</dt><dd><strong>${escapeHtml(schedule.producer_app_version || 'No proven producer')}</strong><code>${escapeHtml(schedule.producer_content_digest || 'Digest unavailable')}</code></dd></div>
          ${schedule.producer_published_at ? `<div class="schedule-activation-wide"><dt>Published</dt><dd>${escapeHtml(scheduleDate(schedule.producer_published_at, timezone))}</dd></div>` : ''}
          ${schedule.convergence_error ? `<div class="schedule-activation-wide schedule-activation-error"><dt>Last error</dt><dd>${escapeHtml(schedule.convergence_error)}</dd></div>` : ''}
        </dl>
      </section>
      <dl class="schedule-definition-grid">
        <div><dt>Timing</dt><dd><code>${escapeHtml(schedule.cron_expr)}</code></dd></div>
        <div><dt>Timezone</dt><dd>${escapeHtml(timezone)}${schedule.timezone_inherited ? ' <span>(inherited)</span>' : ''}</dd></div>
        <div><dt>Next run</dt><dd>${escapeHtml(schedule.enabled === false ? 'Paused' : scheduleDate(schedule.next_fire, timezone))}</dd></div>
        <div><dt>After success</dt><dd>${escapeHtml(afterSuccessLabel(schedule))}<span>${escapeHtml(afterSuccessDetail(schedule))}</span></dd></div>
        <div><dt>Deploy trigger</dt><dd>${escapeHtml(deployTriggerLabel(schedule.deploy_trigger))}</dd></div>
        <div class="schedule-definition-command"><dt>Command</dt><dd><code>${escapeHtml((schedule.command || []).join(' '))}</code></dd></div>
      </dl>
	  ${latestActivation || schedule.on_success === 'roll' ? `<section class="schedule-activation-panel" aria-labelledby="schedule-activation-title">
		<div class="schedule-history-heading"><div><h3 id="schedule-activation-title">Serving-data activation</h3><p>${schedule.on_success === 'roll' ? 'Job health and activation health are reported separately.' : 'Earlier policy · this durable activation was requested before rollout was disabled for future runs.'}</p></div><div class="schedule-activation-actions">${scheduleStatusMarkup(activation)}${surface.options.canManage && canCancelActivation(schedule) ? `<button type="button" class="env-btn-secondary" data-detail-action="cancel-activation" data-schedule-focus="schedule-${schedule.id}-cancel-activation">Cancel activation</button>` : ''}</div></div>
        ${latestActivation ? `<dl class="schedule-activation-facts">
          <div><dt>Requested by</dt><dd>${latestActivation.schedule_run_id ? `Run #${escapeHtml(latestActivation.schedule_run_id)}` : 'Retained activation record'}</dd></div>
          <div><dt>Target generation</dt><dd>${escapeHtml(latestActivation.target_generation)}</dd></div>
          <div><dt>Phase</dt><dd>${escapeHtml(latestActivation.phase || 'pending')}</dd></div>
          <div><dt>Due</dt><dd>${escapeHtml(scheduleDate(latestActivation.due_at, timezone))}</dd></div>
          ${latestActivation.finished_at ? `<div><dt>Finished</dt><dd>${escapeHtml(scheduleDate(latestActivation.finished_at, timezone))}</dd></div>` : ''}
          ${latestActivation.defer_reason ? `<div class="schedule-activation-wide"><dt>Deferred</dt><dd>${escapeHtml(latestActivation.defer_reason)}</dd></div>` : ''}
          ${latestActivation.last_error ? `<div class="schedule-activation-wide schedule-activation-error"><dt>Last error</dt><dd>${escapeHtml(latestActivation.last_error)}</dd></div>` : ''}
        </dl>` : '<p class="schedule-activation-empty">No successful run has requested an activation yet.</p>'}
      </section>` : ''}
      <div class="schedule-history-heading"><div><h3>Run history</h3><p>Newest runs first. Times use ${escapeHtml(timezone)}.</p></div></div>
      <div class="schedule-history" data-schedule-history><p class="schedule-loading">Loading run history…</p></div>`;

    const detailActionsMenu = detail.querySelector('[data-schedule-actions-menu]');
    if (detailActionsMenu) {
      kebabControls.set(detailActionsMenu, wireKebab(detailActionsMenu.querySelector(':scope > button'), detailActionsMenu.querySelector('.kebab-list'), detailActionsMenu));
    }

    detail.querySelector('[data-detail-action="run"]')?.addEventListener('click', e => {
      runScheduleNow(surface.slug, schedule.id, schedule.name, e.currentTarget);
    });
    detail.querySelector('[data-detail-action="edit"]')?.addEventListener('click', () => openScheduleForm(surface.slug, schedule));
    detail.querySelector('[data-detail-action="delete"]')?.addEventListener('click', e => {
      deleteSchedule(surface.slug, schedule.id, schedule.name, e.currentTarget);
    });
    detail.querySelector('[data-detail-action="cancel-activation"]')?.addEventListener('click', e => {
      cancelScheduleActivation(surface.slug, schedule.id, schedule.name, e.currentTarget);
    });
    detail.querySelector('[data-detail-action="retry-convergence"]')?.addEventListener('click', e => {
      retryScheduleConvergence(surface.slug, schedule.id, schedule.name, e.currentTarget);
    });

    await loadScheduleRunPage(surface, schedule, 0, true);
    if (!surface.stopped && detail) detail.setAttribute('aria-busy', 'false');
  }

  async function loadScheduleRunPage(surface, schedule, offset, replace) {
    const host = document.querySelector('[data-schedule-history]');
    if (!host || surface.stopped) return;
    let response;
    try {
      response = await api(`/api/apps/${encodeURIComponent(surface.slug)}/schedules/${schedule.id}/runs?limit=50&offset=${offset}`);
    } catch {
      if (replace) host.innerHTML = '<div class="schedule-load-error"><p>Run history could not be loaded.</p><button type="button" class="env-btn-secondary" data-run-history-retry>Try again</button></div>';
      else {
        flashToast('More run history could not be loaded.', 'error');
        renderScheduleRuns(surface, schedule, surface.detailTotal || (surface.detailRuns || []).length);
      }
      host.querySelector('[data-run-history-retry]')?.addEventListener('click', () => loadScheduleRunPage(surface, schedule, 0, true));
      return;
    }
    if (response.status === 401) { await handleUnauthorized(); return; }
    if (!response.ok) {
      if (replace) host.innerHTML = '<div class="schedule-load-error"><p>Run history could not be loaded.</p><button type="button" class="env-btn-secondary" data-run-history-retry>Try again</button></div>';
      else {
        flashToast('More run history could not be loaded.', 'error');
        renderScheduleRuns(surface, schedule, surface.detailTotal || (surface.detailRuns || []).length);
      }
      host.querySelector('[data-run-history-retry]')?.addEventListener('click', () => loadScheduleRunPage(surface, schedule, 0, true));
      return;
    }
    const body = await response.json();
    const page = Array.isArray(body) ? body : (Array.isArray(body.items) ? body.items : []);
    const total = Number.isFinite(body.total) ? body.total : page.length;
    surface.detailTotal = total;
    surface.detailRuns = replace ? page : [...(surface.detailRuns || []), ...page];
    renderScheduleRuns(surface, schedule, total);
  }

  function renderScheduleRuns(surface, schedule, total) {
    const host = document.querySelector('[data-schedule-history]');
    const runs = surface.detailRuns || [];
    if (!host) return;
    if (runs.length === 0) {
      host.innerHTML = '<div class="schedule-runs-empty"><h4>No runs yet</h4><p>The first scheduled or manual run will appear here.</p></div>';
      return;
    }
    const rows = runs.map(run => {
      const runState = scheduleState({
        enabled: true,
        active_run_id: run.status === 'running' ? run.id : null,
        last_run_status: run.status,
      });
      const duration = formatDuration(runDurationSeconds(run));
      const trigger = runTriggerLabel(run.trigger);
      const exit = run.exit_code == null ? '—' : String(run.exit_code);
      const activationResult = run.on_success === 'roll'
        ? activationState({on_success: 'roll', latest_activation: run.activation_status ? {
            status: run.activation_status,
            phase: run.activation_phase,
          } : null})
        : null;
	  const activationError = activationErrorDetail(run);
      const actions = [
        surface.options.canReadLogs ? `<button type="button" class="env-btn-secondary" data-run-action="logs" data-run-id="${run.id}" data-schedule-focus="run-${run.id}-logs">View logs</button>` : '',
        surface.options.canCancel && run.status === 'running' ? `<button type="button" class="btn-danger-sm" data-run-action="cancel" data-run-id="${run.id}" data-schedule-focus="run-${run.id}-cancel">Cancel</button>` : '',
      ].join('');
      return `<tr>
        <td data-label="Result"><div class="schedule-cell-stack">${scheduleStatusMarkup(runState)}</div></td>
        <td data-label="Started"><div class="schedule-cell-stack"><time datetime="${escapeHtml(run.started_at || '')}">${escapeHtml(scheduleDate(run.started_at, schedule.effective_timezone || 'UTC'))}</time></div></td>
        <td data-label="Duration"><div class="schedule-cell-stack">${escapeHtml(duration)}</div></td>
        <td data-label="Trigger"><div class="schedule-cell-stack">${escapeHtml(trigger)}</div></td>
        <td data-label="Exit"><div class="schedule-cell-stack">${escapeHtml(exit)}</div></td>
		<td data-label="Activation"><div class="schedule-cell-stack">${activationResult ? scheduleStatusMarkup(activationResult) : '—'}${run.target_generation ? `<span>Generation ${escapeHtml(run.target_generation)}</span>` : ''}${activationError ? `<small class="schedule-run-activation-error"><strong>Activation error:</strong> ${escapeHtml(activationError)}</small>` : ''}</div></td>
        <td class="table-actions" data-label="Actions"><div class="schedule-row-actions">${actions}</div></td>
      </tr>`;
    }).join('');
    const remaining = Math.max(0, total - runs.length);
    host.innerHTML = `<table>
      <thead><tr><th scope="col">Job result</th><th scope="col">Started</th><th scope="col">Duration</th><th scope="col">Trigger</th><th scope="col">Exit</th><th scope="col">Activation</th><th scope="col"><span class="sr-only">Actions</span></th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
    <div class="schedule-history-footer"><span>Showing ${runs.length} of ${total}</span>${remaining ? `<button type="button" class="env-btn-secondary" data-load-more-runs data-schedule-focus="schedule-${schedule.id}-load-more">Load ${Math.min(50, remaining)} more</button>` : ''}</div>`;

    host.querySelectorAll('[data-run-action]').forEach(btn => {
      btn.addEventListener('click', () => {
        const runID = Number.parseInt(btn.dataset.runId, 10);
        if (btn.dataset.runAction === 'logs') openScheduleRunLogs(surface.slug, schedule.id, runID);
        if (btn.dataset.runAction === 'cancel') cancelScheduleRun(surface.slug, schedule.id, runID, btn);
      });
    });
    host.querySelector('[data-load-more-runs]')?.addEventListener('click', e => {
      e.currentTarget.disabled = true;
      loadScheduleRunPage(surface, schedule, runs.length, false);
    });
  }

  // Load and render the shared-data mounts list.
  async function loadSharedData(slug) {
    const container = document.getElementById('shared-data-list');
    if (!container) return;
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}/shared-data`);
    } catch {
      container.innerHTML = '<p class="error">Failed to load shared data mounts.</p>';
      return;
    }
    if (!resp.ok) {
      container.innerHTML = '<p class="error">Failed to load shared data mounts.</p>';
      return;
    }
    const body = await resp.json();
    // Standard {items,...} list envelope; tolerate a bare array for resilience.
    const mounts = Array.isArray(body) ? body : (body && Array.isArray(body.items) ? body.items : []);
    if (mounts.length === 0) {
      container.innerHTML = '<p class="env-empty">No shared data mounts configured.</p>';
      return;
    }
    const items = mounts.map(m => `
      <li>
        <span>data/shared/<strong>${escapeHtml(m.source_slug)}</strong>/</span>
        <button type="button" class="btn-danger-sm" data-action="revoke" data-slug="${escapeHtml(m.source_slug)}">Unmount</button>
      </li>`).join('');
    container.innerHTML = `<ul class="shared-data-list">${items}</ul>`;

    container.querySelectorAll('[data-action="revoke"]').forEach(btn => {
      btn.addEventListener('click', async () => {
        const sourceSlug = btn.dataset.slug;
        if (!confirm(`Unmount data from "${sourceSlug}"?`)) return;
        btn.disabled = true;
        try {
          let r;
          try {
            r = await api(`/api/apps/${encodeURIComponent(slug)}/shared-data/${encodeURIComponent(sourceSlug)}`, {
              method: 'DELETE',
            });
          } catch {
            flashToast('Unmount failed: network error', 'error');
            return;
          }
          if (r.status === 401) { await handleUnauthorized(); return; }
          if (!r.ok) {
            flashToast('Unmount failed: ' + (await errorMessage(r)), 'error');
            return;
          }
          await loadSharedData(slug);
        } finally {
          btn.disabled = false;
        }
      });
    });
  }

  // Open the schedule add/edit form modal.
  function openScheduleForm(slug, existing) {
    const modal = document.getElementById('schedule-form-modal');
    const title = document.getElementById('schedule-form-title');
    const form = document.getElementById('schedule-form');
    const errEl = document.getElementById('schedule-form-error');
    if (!modal || !form) return;

    title.textContent = existing ? 'Edit schedule' : 'Add schedule';
    form.reset();

    if (existing) {
      document.getElementById('sched-name').value = existing.name || '';
      document.getElementById('sched-cron').value = existing.cron_expr || '';
      document.getElementById('sched-command').value = (existing.command || []).join('\n');
      document.getElementById('sched-timeout').value = existing.timeout_seconds || 3600;
      document.getElementById('sched-overlap').value = existing.overlap_policy || 'skip';
      document.getElementById('sched-missed').value = existing.missed_policy || 'skip';
      document.getElementById('sched-enabled').checked = existing.enabled !== false;
      document.getElementById('sched-timezone').value = existing.timezone || '';
      document.getElementById('sched-on-success').value = existing.on_success || 'none';
      document.getElementById('sched-deploy-trigger').value = existing.deploy_trigger || 'never';
      document.getElementById('sched-min-roll-interval').value = existing.min_roll_interval_seconds || 0;
      document.getElementById('sched-roll-fallback').value = existing.roll_fallback || 'defer';
      document.getElementById('sched-max-defer-age').value = existing.max_defer_age_seconds || 0;
    }
    updateCronPreview(document.getElementById('sched-cron').value, document.getElementById('sched-timezone').value);

    // Replace the submit handler to capture the current slug/existing binding.
    const newForm = form.cloneNode(true);
    form.parentNode.replaceChild(newForm, form);
    setError(document.getElementById('schedule-form-error'), '');

    // Re-attach cron preview input listener and cancel button.
    const refreshCronHint = () => updateCronPreview(
      newForm.querySelector('#sched-cron').value,
      newForm.querySelector('#sched-timezone').value,
    );
    newForm.querySelector('#sched-cron').addEventListener('input', refreshCronHint);
    newForm.querySelector('#sched-timezone').addEventListener('input', refreshCronHint);
    newForm.querySelector('#schedule-form-cancel')?.addEventListener('click', closeScheduleForm);

    const refreshRollOptions = () => {
      const rolling = newForm.querySelector('#sched-on-success').value === 'roll';
      newForm.querySelector('#sched-roll-options').hidden = !rolling;
      newForm.querySelector('#sched-min-roll-interval').disabled = !rolling;
      newForm.querySelector('#sched-roll-fallback').disabled = !rolling;
      newForm.querySelector('#sched-max-defer-age').disabled = !rolling;
    };
    newForm.querySelector('#sched-on-success').addEventListener('change', refreshRollOptions);
    refreshRollOptions();

    const refreshConsequence = () => {
      const trigger = newForm.querySelector('#sched-deploy-trigger').value;
      const rolling = newForm.querySelector('#sched-on-success').value === 'roll';
      const consequence = newForm.querySelector('#schedule-consequence');
      const triggerCopy = trigger === 'bundle_change'
        ? 'Every changed bundle builds compatible data before new replicas start.'
        : trigger === 'first_deploy'
          ? 'Only the first deployment is guaranteed a producer run; later code changes reuse existing data.'
          : 'Deployments never run this job; code and data compatibility is not tracked.';
      consequence.innerHTML = `<strong>What will happen</strong><span>${escapeHtml(triggerCopy)} ${rolling ? 'After each successful job, serving replicas roll onto the new data.' : 'Successful jobs do not reload serving replicas.'}</span>`;
    };
    newForm.querySelector('#sched-deploy-trigger').addEventListener('change', refreshConsequence);
    newForm.querySelector('#sched-on-success').addEventListener('change', refreshConsequence);
    refreshConsequence();

    newForm.addEventListener('submit', async e => {
      e.preventDefault();
      const name = newForm.querySelector('#sched-name').value.trim();
      const cronExpr = newForm.querySelector('#sched-cron').value.trim();
      const command = newForm.querySelector('#sched-command').value.split('\n').map(l => l.trim()).filter(Boolean);
      const timeoutSeconds = parseInt(newForm.querySelector('#sched-timeout').value, 10);
      const overlapPolicy = newForm.querySelector('#sched-overlap').value;
      const missedPolicy = newForm.querySelector('#sched-missed').value;
      const enabled = newForm.querySelector('#sched-enabled').checked;
      const onSuccess = newForm.querySelector('#sched-on-success').value;
      const deployTrigger = newForm.querySelector('#sched-deploy-trigger').value;
      const minRollIntervalSeconds = onSuccess === 'roll'
        ? parseInt(newForm.querySelector('#sched-min-roll-interval').value || '0', 10)
        : 0;
      const rollFallback = onSuccess === 'roll'
        ? newForm.querySelector('#sched-roll-fallback').value
        : 'defer';
      const maxDeferAgeSeconds = onSuccess === 'roll'
        ? parseInt(newForm.querySelector('#sched-max-defer-age').value || '0', 10)
        : 0;

      const newErrEl = document.getElementById('schedule-form-error');
      setError(newErrEl, '');

      const timezone = newForm.querySelector('#sched-timezone').value.trim();
      const body = JSON.stringify({name, cron_expr: cronExpr, command, timeout_seconds: timeoutSeconds, overlap_policy: overlapPolicy, missed_policy: missedPolicy, enabled, timezone, deploy_trigger: deployTrigger, on_success: onSuccess, min_roll_interval_seconds: minRollIntervalSeconds, roll_fallback: rollFallback, max_defer_age_seconds: maxDeferAgeSeconds});
      const submitBtn = newForm.querySelector('button[type="submit"]');
      if (submitBtn) submitBtn.disabled = true;
      try {
        let r;
        try {
          if (existing) {
            r = await api(`/api/apps/${encodeURIComponent(slug)}/schedules/${existing.id}`, {
              method: 'PATCH',
              headers: {'Content-Type': 'application/json'},
              body,
            });
          } else {
            r = await api(`/api/apps/${encodeURIComponent(slug)}/schedules`, {
              method: 'POST',
              headers: {'Content-Type': 'application/json'},
              body,
            });
          }
        } catch {
          setError(newErrEl, 'Network error');
          return;
        }
        if (r.status === 401) { await handleUnauthorized(); return; }
        if (!r.ok) {
          setError(newErrEl, await errorMessage(r));
          return;
        }
        closeScheduleForm();
        await loadSchedules(slug);
      } finally {
        if (submitBtn) submitBtn.disabled = false;
      }
    });

    modal.hidden = false;
    modalTrap(modal).activate();
    newForm.querySelector('#sched-name')?.focus();
  }

  function closeScheduleForm() {
    const modal = document.getElementById('schedule-form-modal');
    if (modal) {
      modal.hidden = true;
      modalTrap(modal).release();
    }
  }

  async function runScheduleNow(slug, id, name, btn) {
    if (!confirm(`Run "${name || 'this schedule'}" now? The command starts immediately against the current deployed bundle.`)) return;
    if (btn) btn.disabled = true;
    try {
      let r;
      try {
        r = await api(`/api/apps/${encodeURIComponent(slug)}/schedules/${id}/run`, {method: 'POST'});
      } catch {
        flashToast('Run failed: network error', 'error');
        return;
      }
      if (r.status === 401) { await handleUnauthorized(); return; }
      if (!r.ok) {
        flashToast('Run failed: ' + (await errorMessage(r)), 'error');
        return;
      }
      const started = await r.json().catch(() => ({}));
      flashToast(started.run_id ? `Run #${started.run_id} started.` : 'Schedule started.', 'success');
      await router.navigate(`/apps/${encodeURIComponent(slug)}/schedules/${id}`);
    } finally {
      if (btn) btn.disabled = false;
    }
  }

  async function retryScheduleConvergence(slug, id, name, btn) {
    if (btn) {
      btn.disabled = true;
      btn.textContent = 'Retrying…';
    }
    try {
      let response;
      try {
        response = await api(`/api/apps/${encodeURIComponent(slug)}/schedules/${id}/convergence/retry`, {method: 'POST'});
      } catch {
        flashToast('Producer retry failed: network error', 'error');
        return;
      }
      if (response.status === 401) { await handleUnauthorized(); return; }
      if (!response.ok) {
        flashToast('Producer retry failed: ' + (await errorMessage(response)), 'error');
        return;
      }
      const result = await response.json().catch(() => ({}));
      flashToast(result.run_id ? `Producer run #${result.run_id} started.` : `Producer retry started for ${name}.`, 'success');
      if (mountedScheduleSurface?.slug === slug) {
        mountedScheduleSurface.renderedDetailSignature = '';
        await refreshScheduleSurface(mountedScheduleSurface, {announce: true});
      }
    } finally {
      if (btn && btn.isConnected) {
        btn.disabled = false;
        btn.textContent = 'Retry producer';
      }
    }
  }

  async function deleteSchedule(slug, id, name, btn) {
    if (!confirm(`Delete schedule "${name}"?`)) return;
    if (btn) btn.disabled = true;
    try {
      let r;
      try {
        r = await api(`/api/apps/${encodeURIComponent(slug)}/schedules/${id}`, {method: 'DELETE'});
      } catch {
        flashToast('Delete failed: network error', 'error');
        return;
      }
      if (r.status === 401) { await handleUnauthorized(); return; }
      if (!r.ok) {
        flashToast('Delete failed: ' + (await errorMessage(r)), 'error');
        return;
      }
      if (mountedScheduleSurface && String(mountedScheduleSurface.options.selectedScheduleID) === String(id)) {
        await router.navigate(`/apps/${encodeURIComponent(slug)}/schedules`, {replace: true});
      } else {
        await loadSchedules(slug);
      }
    } finally {
      if (btn) btn.disabled = false;
    }
  }

  async function cancelScheduleRun(slug, schedID, runID, btn) {
    if (!confirm(`Cancel run #${runID}? The scheduled process will be stopped.`)) return;
    if (btn) btn.disabled = true;
    try {
      let response;
      try {
        response = await api(`/api/apps/${encodeURIComponent(slug)}/schedules/${schedID}/runs/${runID}/cancel`, {method: 'POST'});
      } catch {
        flashToast('Cancel failed: network error', 'error');
        return;
      }
      if (response.status === 401) { await handleUnauthorized(); return; }
      if (!response.ok) {
        flashToast('Cancel failed: ' + (await errorMessage(response)), 'error');
        return;
      }
      flashToast(`Run #${runID} cancelled.`, 'success');
      if (mountedScheduleSurface) await refreshScheduleSurface(mountedScheduleSurface, {announce: true});
    } finally {
      if (btn) btn.disabled = false;
    }
  }

  async function cancelScheduleActivation(slug, schedID, name, btn) {
    if (!confirm(`Cancel the pending serving-data activation for "${name}"? The current serving replicas will not be stopped.`)) return;
    if (btn) btn.disabled = true;
    try {
      let response;
      try {
        response = await api(`/api/apps/${encodeURIComponent(slug)}/schedules/${schedID}/activation/cancel`, {method: 'POST'});
      } catch {
        flashToast('Activation cancellation failed: network error', 'error');
        return;
      }
      if (response.status === 401) { await handleUnauthorized(); return; }
      if (!response.ok) {
        flashToast('Activation cancellation failed: ' + (await errorMessage(response)), 'error');
        return;
      }
      flashToast('Serving-data activation cancelled. The current replicas are unchanged.', 'success');
      if (mountedScheduleSurface) await refreshScheduleSurface(mountedScheduleSurface, {announce: true});
    } finally {
      if (btn) btn.disabled = false;
    }
  }

  // Stream logs for a specific schedule run into the log pane.
  function openScheduleRunLogs(slug, schedID, runID) {
    if (activeEventSource) { activeEventSource.close(); activeEventSource = null; }
    logPaneTitle.textContent = `Run #${runID} logs`;
    logPaneBody.textContent = '';
    setHidden(logPane, false);

    const url = `/api/apps/${encodeURIComponent(slug)}/schedules/${schedID}/runs/${runID}/logs`;
    const es = new EventSource(url);
    activeEventSource = es;
    es.onmessage = e => {
      const line = document.createElement('div');
      line.textContent = e.data;
      logPaneBody.appendChild(line);
      logPaneBody.scrollTop = logPaneBody.scrollHeight;
    };
    es.onerror = () => {
      es.close();
      activeEventSource = null;
      const line = document.createElement('div');
      line.textContent = '(log stream disconnected)';
      logPaneBody.appendChild(line);
      logPaneBody.scrollTop = logPaneBody.scrollHeight;
    };
  }

  // Wire the Schedules and Shared Data buttons.
  document.getElementById('schedules-add-btn')?.addEventListener('click', () => {
    if (settingsSlug) openScheduleForm(settingsSlug, null);
  });
  document.getElementById('schedules-refresh-btn')?.addEventListener('click', async event => {
    if (!mountedScheduleSurface) return;
    const btn = event.currentTarget;
    btn.disabled = true;
    try {
      await refreshScheduleSurface(mountedScheduleSurface, {announce: true});
    } finally {
      btn.disabled = false;
    }
  });

  document.getElementById('shared-data-add-btn')?.addEventListener('click', async (event) => {
    if (!settingsSlug) return;
    const sourceSlug = (prompt('Source app slug to mount read-only:') || '').trim();
    if (!sourceSlug) return;
    const btn = event.currentTarget;
    btn.disabled = true;
    try {
      let r;
      try {
        r = await api(`/api/apps/${encodeURIComponent(settingsSlug)}/shared-data`, {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({source_slug: sourceSlug}),
        });
      } catch {
        flashToast('Mount failed: network error', 'error');
        return;
      }
      if (r.status === 401) { await handleUnauthorized(); return; }
      if (!r.ok) {
        flashToast('Mount failed: ' + (await errorMessage(r)), 'error');
        return;
      }
      await loadSharedData(settingsSlug);
    } finally {
      btn.disabled = false;
    }
  });

  // Wire the schedule form close button and overlay-click dismiss.
  document.getElementById('schedule-form-close')?.addEventListener('click', closeScheduleForm);
  document.getElementById('schedule-form-modal')?.addEventListener('click', e => {
    if (e.target === e.currentTarget) closeScheduleForm();
  });

  initialize();
});
