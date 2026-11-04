import { uploadDeployment, deploymentOutcome } from '/static/views/deployment-upload.js';
import { installSidebarAccount } from '/static/views/sidebar-account.js';
import { installPasswordVisibility, profileFieldErrors, setProfileFieldError } from '/static/views/auth-fields.js';
import { installAppContextNavigation } from '/static/views/app-context-nav.js';
import { settingsRouteParams, isSameAppSettingsDestination } from '/static/views/app-detail-nav.js';
import { directSettings, populateDirectSetting, installDirectSetting, refreshDirectSetting, readDirectSetting } from '/static/views/direct-setting.js';
import { applyConfigurationContext, markConfigurationBehaviorStale, updateConfigurationDirty, updateWorkerFields, switchWorkerFields, usageModeLabel, environmentEditValue } from '/static/views/configuration-context.js';
import { installMobileDisclosures } from '/static/views/mobile-disclosure.js';
import { enhancePageHeaders, bindFilterBar, setFeedback } from '/static/views/page-components.js';
import { deployCommands, cliSetupCommands } from '/static/views/deploy-commands.js';
import { mountAnnouncements } from '/static/views/announcements.js';
import { createSessionController } from '/static/views/session-controller.js';
import { createPeopleDirectory } from '/static/views/people-directory.js';
import { scalingSettingsSnapshot, scalingSettingsPatch } from '/static/views/scaling-settings.js';
import { resourceSettingsPatch } from '/static/views/resource-settings.js';
import { createNewPersonController } from '/static/views/new-person.js';
import { createRouter } from '/static/router.js';
import { startAuthenticatedRouter } from '/static/auth-navigation.js';
import { logoutTarget, reconnectForwardAuth } from '/static/views/logout-navigation.js';
import { createMetricsController } from '/static/metrics-controller.js';
import { mountAppsGrid } from '/static/views/apps-grid.js';
import { mountProjectDetail } from '/static/views/project-detail.js';
import { mountOverview } from '/static/views/overview.js';
import { mountLaunchpad } from '/static/views/launchpad.js';
import { renderAppAvatar, avatarView } from '/static/views/app-avatar.js';
import { isSingleEmoji, renderEmojiPicker } from '/static/views/emoji-picker.js';
import { mountUsers } from '/static/views/users.js';
import { createTokenManager } from '/static/views/token-manager.js';
import { mountWorkers } from '/static/views/workers.js';
import { createGETCoalescer } from '/static/views/request-coalesce.js';
import { createBootPrefetch } from '/static/views/boot-prefetch.js';
import {
  activationAttentionTooltip,
  degradedTooltip,
  summariseFleetHealth,
} from '/static/views/fleet-health.js';
import { createFocusTrap } from '/static/views/focus-trap.js';
import { installSelectPickerGuard } from '/static/views/select-picker.js';
import { mountAuditLog } from '/static/views/audit-log.js';
import { createAuditExplorer } from '/static/views/audit-explorer.js';
import { mountAppDetail, refreshFleetSurfaces } from '/static/views/app-detail.js';
import { appCardBadge, appListSummary, applyLiveStatus, paintCardStatusBadge, updateStatusPill } from '/static/views/app-card-badge.js';
import {
  appsSurfaceForSession,
  homeSurfaceForSession,
  resolveAdminOnlyAccess,
  resolveAuditLogAccess,
  renderSidebarApps,
  highlightSidebarApp,
  isPrimaryNavActive,
} from '/static/views/sidebar-nav.js';
import { createSidebarDrawer } from '/static/views/sidebar-drawer.js';
import { createLogPane } from '/static/views/log-pane.js';
import { renderDetailCapacity, markDetailCapacityError } from '/static/views/detail-capacity.js';
import { appCardFacts } from '/static/views/app-card-facts.js';
import { renderAppAttention } from '/static/views/app-attention.js';
import { appCardActions } from '/static/views/app-card-actions.js';
import { inspectBundleEntry } from '/static/views/bundle-filter.js';
import { createAppCardLifecycle, requestAppRestart, restartConfirmationCopy } from '/static/views/app-card-lifecycle.js';
import { applyLoginProviders, groupAccessWarningText } from '/static/views/login-providers.js';
import { usagePrivacyErrorMessage } from '/static/views/usage-privacy.js';
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
  scheduleTimingLabel,
  createScheduleRequestGate,
  producerCompatibilityState,
} from '/static/views/schedule-ui.js';
import { readAutoscaleForm, parseReplicaBound, renderAutoscaleSummary, summariseAutoscale } from '/static/views/autoscale.js';
import { workerCapacityLine, keepWarmInertNote } from '/static/views/worker-isolation.js';
import { parseRenderSeconds, renderPacingAdvice } from '/static/views/render-pacing.js';
import { isInFlight, runInFlight } from '/static/views/in-flight.js';
import { initTheme, getThemePreference, setThemePreference } from '/static/views/theme.js';
import { backendLabel, metricsText, reasonLabel } from '/static/views/replica-display.js';
import { formatStatus } from '/static/views/status-label.js';
import {
  consumeSupportDraft,
  clearSupportAppOwnedError,
  supportReasonError,
  createSupportAppRequestGate,
  createSupportSessionModalLock,
  isFreshSupportDraft,
  restoreSupportAppSelection,
  restoreFailedActionFocus,
  saveSupportDraft,
} from '/static/views/support-session-modal.js';
import { createSupportSessionRecovery } from '/static/views/support-session-recovery.js';
import { shouldShowAppIsolationBanner, wireAppIsolationBanner } from '/static/views/app-isolation-banner.js';
import { identityModel } from '/static/views/user-identity.js';
import { renderUserAvatar, prepareUserAvatar, avatarPickerColor } from '/static/views/user-avatar.js';
import { createServerInfoLoader, renderAbout } from '/static/views/about.js';
import { groupAppsForGrid } from '/static/views/app-grid-groups.js';
import { GRID_PAGE, windowCount, createShowMore } from '/static/views/render-window.js';
import { createGroupDisclosure } from '/static/views/group-disclosure.js';
import { focusedKey, restoreFocus, siblingKey } from '/static/views/focus-restore.js';
import { wireKebab } from '/static/views/kebab-menu.js';
import { buildProjectPatchBody } from '/static/views/project-edit-body.js';

function setHidden(element, hidden) {
  element.hidden = hidden;
}

function setError(element, message) {
  setFeedback(element,message,{error:true});
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
        // Lift inert BEFORE restoring focus. An inert subtree cannot take
        // focus, so focusing the opener while the shell is still inert is a
        // silent no-op and the browser leaves focus on <body>: the operator
        // who opened the dialog from a card is returned to the top of the
        // page instead of to the control they opened it with.
        setModalBackgroundInert(false);
        focusTrap.release();
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

// kebabControls maps a .kebab-menu element to the handle wireKebab returned for
// it, so code holding only the element (the metrics poller, re-syncing a card
// whose app changed state) can close the menu without re-implementing its
// open/closed bookkeeping. wireKebab itself lives in views/kebab-menu.js so its
// keyboard behaviour can be exercised without booting the shell.
const kebabControls = new WeakMap();

// formatStatus (status → display label) is imported from views/status-label.js
// so the cards, detail pill, sidebar, and replica badges all speak with one
// voice. Badges no longer use `text-transform: uppercase`, so casing matters.

document.addEventListener('DOMContentLoaded', () => {
  enhancePageHeaders(document);
  installSelectPickerGuard(document);
  installMobileDisclosures(document, window);
  // Branding: server injects window.__SHINYHUB_BRANDING__ (see
  // internal/ui/branding.go RenderIndex). Absent/empty in zero-branding mode.
  // Applied to every .brand slot (sidebar, mobile top bar, boot splash, and the
  // login card) by the unit-tested views/branding.js.
  applyBranding(document, window.__SHINYHUB_BRANDING__);

  let sessionVersion = 0;
  const state = {
    user: null,
    sessionIdentity: {},
    apps: [],
    canCreateApps: false,
    canManageApps: false,
    resetPwTargetId: null,
    resetPwTargetUsername: '',
    supportSessions: { enabled: false, duration_seconds: 900 },
    supportTarget: null,
  };

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
  const appLiveViews = new Map();
  let appRestartFeedbackVersion = 0;

  const loginView = document.getElementById('login-view');
  const overviewView = document.getElementById('overview-view');
  const launchpadView = document.getElementById('launchpad-view');
  const appsView = document.getElementById('apps-view');
  const projectDetailView = document.getElementById('project-detail-view');
  const appDetailView = document.getElementById('app-detail-view');
  const passwordVisibility = installPasswordVisibility(document);
  const sidebarAccount = installSidebarAccount(document, window);
  let loginPending = false;
  const loginForm = document.getElementById('login-form');
  const usernameInput = document.getElementById('login-username');
  const passwordInput = document.getElementById('login-password');
  const loginError = document.getElementById('login-error');
  const appError = document.getElementById('app-error');
  const refreshButton = document.getElementById('refresh-button');
  const fleetHealthEl = document.getElementById('fleet-health');
  const logoutButton = document.getElementById('logout-button');
  // Sidebar identity card + profile modal (photo, display name, and password).
  const identityCard    = document.getElementById('identity-card');
  const identityAvatar  = document.getElementById('identity-avatar');
  const identityName    = document.getElementById('identity-name');
  const identityRole    = document.getElementById('identity-role');
  const profileModal    = document.getElementById('profile-modal');
  const profileClose    = document.getElementById('profile-close');
  const profileCancel = document.getElementById('profile-cancel');
  const profileForm     = document.getElementById('profile-form');
  const profileAvatar   = document.getElementById('profile-avatar');
  const profilePhotoControls = document.getElementById('profile-photo-controls');
  const profilePhotoChange = document.getElementById('profile-photo-change');
  const profilePhotoRemove = document.getElementById('profile-photo-remove');
  const profilePhotoFile = document.getElementById('profile-photo-file');
  const profilePhotoStatus = document.getElementById('profile-photo-status');
  const profileAvatarColor = document.getElementById('profile-avatar-color');
  const profileColorReset = document.getElementById('profile-color-reset');
  let pendingAvatarColor;
  let pendingAvatar;
  let photoGeneration = 0;
  let photoPreparing = false;
  let profileSaving = false;
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
  const loadServerInfo = createServerInfoLoader((url) => bootPrefetch.fetch(url, undefined, (u) => fetch(u)));
  const appGrid = document.getElementById('app-grid');
  const projectDetailGrid = document.getElementById('project-detail-grid');
  const emptyState = document.getElementById('empty-state');
  const emptyStateHeading = document.getElementById('empty-state-heading');
  const emptyStateLead    = document.getElementById('empty-state-lead');
  const emptyStateActions = document.getElementById('empty-state-actions');
  const emptyStateCTA     = document.getElementById('empty-state-cta');
  const logPane = document.getElementById('log-pane');
  const logPaneTitle = document.getElementById('log-pane-title');
  const logPaneBody = document.getElementById('log-pane-body');
  const logPaneStatus = document.getElementById('log-pane-status');
  const logPaneClose = document.getElementById('log-pane-close');
  const logPaneController = createLogPane({
    pane: logPane,
    title: logPaneTitle,
    body: logPaneBody,
    status: logPaneStatus,
    closeButton: logPaneClose,
    EventSourceClass: EventSource,
    createFocusTrap: (el) => modalTrap(el),
    doc: document,
    setHidden,
  });
  const auditView   = document.getElementById('audit-view');
  const tabAudit    = document.getElementById('tab-audit');
  const tabUsers    = document.getElementById('tab-users');
  const tabWorkers  = document.getElementById('tab-workers');
  const tabOverview = document.getElementById('tab-overview');
  const tabApps     = document.getElementById('tab-apps');
  let   sidebarDrawer = null; // mobile off-canvas drawer controller (wired below)
  const workersView    = document.getElementById('workers-view');
  const usersView   = document.getElementById('users-view');
  const usersRefresh   = document.getElementById('users-refresh');
  const newUserButton  = document.getElementById('new-user-button');
  const newUserModal   = document.getElementById('new-user-modal');
  const newUserClose   = document.getElementById('new-user-close');
  const newUserCancel  = document.getElementById('new-user-cancel');
  const newUserForm    = document.getElementById('new-user-form');
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
  const tokensView = document.getElementById('tokens-view');
  const newTokenButton = document.getElementById('new-token-button');
  const newTokenModal = document.getElementById('new-token-modal');
  const profileTokensLink = document.getElementById('profile-tokens-link');
  const cliConnectPanel = document.getElementById('cli-connect-panel');
  const resetPwModal    = document.getElementById('reset-password-modal');
  const resetPwClose    = document.getElementById('reset-password-close');
  const resetPwCancel   = document.getElementById('reset-password-cancel');
  const resetPwForm     = document.getElementById('reset-password-form');
  const resetPwSubmit   = document.getElementById('reset-password-submit');
  const resetPwInput    = document.getElementById('reset-password-input');
  const resetPwUsername = document.getElementById('reset-password-username');
  const resetPwError    = document.getElementById('reset-password-error');
  const supportModal    = document.getElementById('support-session-modal');
  const supportForm     = document.getElementById('support-session-form');
  const supportUsername = document.getElementById('support-session-username');
  const supportApp      = document.getElementById('support-session-app');
  const supportAppStatus = document.getElementById('support-session-app-status');
  const supportReason   = document.getElementById('support-session-reason');
  const supportError    = document.getElementById('support-session-error');
  const supportSubmit   = document.getElementById('support-session-submit');
  const supportClose    = document.getElementById('support-session-close');
  const supportCancel   = document.getElementById('support-session-cancel');
  const supportAppsRetry = document.getElementById('support-session-apps-retry');
  const supportReauth   = document.getElementById('support-session-reauth');
  const supportRecoveryRoot = document.getElementById('support-session-recovery');
  const appIsolationBanner = document.getElementById('app-isolation-banner');
  const appIsolationBannerDismiss = document.getElementById('app-isolation-banner-dismiss');
  wireAppIsolationBanner({ root: appIsolationBanner, dismissButton: appIsolationBannerDismiss });
  const supportModalLock = supportModal ? createSupportSessionModalLock({
    modal: supportModal, closeButton: supportClose, cancelButton: supportCancel,
  }) : null;
  const supportAppsGate = createSupportAppRequestGate();
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
  const deployErrorLogs    = document.getElementById('deploy-error-logs');
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

  // A cold dashboard load fires more than one independent caller that wants
  // the same read-only list at once (showLoggedIn's sidebar index and the
  // view the router is about to mount both fetch /api/apps on their own,
  // unaware of each other), so GET requests share one underlying fetch per
  // URL while one is in flight instead of one each. Only ever applies to a
  // plain GET with no body and no caller-supplied headers - anything else
  // (every mutating request, and a GET a caller has customized) always gets
  // its own fetch, unchanged.
  //
  // Underneath, the first GETs of a cold load are answered by the requests the
  // shell already started while it parsed (index.html, views/boot-prefetch.js),
  // until the boot finishes or anything mutates server state.
  const bootPrefetch = createBootPrefetch(window.__shinyhubBoot);
  const coalescedGET = createGETCoalescer((path, init) => bootPrefetch.fetch(path, init, (p, i) => fetch(p, i)));

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
    const mutating = method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS';
    if (mutating) {
      bootPrefetch.discard();
      const token = readCookie('csrf_token');
      if (token) init.headers['X-CSRF-Token'] = token;
    }
    const coalesceable = !mutating && !init.body && !init.signal && init.cache !== 'no-store' && Object.keys(init.headers).length === 0;
    const resp = coalesceable ? await coalescedGET(path, init) : await fetch(path, init);
    // A successful mutating request means anything a view is holding may no
    // longer be true. Announce it here, at the one place every request passes
    // through, rather than asking each call site to remember: a view that caches
    // server data (the app-detail envelope) refetches before it renders again,
    // and a mutation added later is covered without touching it.
    if (mutating && resp.ok) {
      document.dispatchEvent(new CustomEvent('shinyhub:mutated', { detail: { path, method } }));
    }
    return resp;
  }

  function focusCurrentRouteHeading() {
    const heading = document.querySelector('main > section:not([hidden]) h1');
    if (heading) {
      heading.tabIndex = -1;
      heading.focus({ preventScroll: true });
    }
  }

  const supportRecovery = createSupportSessionRecovery({
    root: supportRecoveryRoot,
    request: api,
    onUnauthorized: handleUnauthorized,
    onEnded: focusCurrentRouteHeading,
    onStatusCleared: focusCurrentRouteHeading,
  });

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

  function renderEmptyStateCopy({ filtered = false } = {}) {
    const clear = document.getElementById('empty-state-clear');
    clear.hidden = !filtered;
    emptyStateCTA.hidden = filtered;
    if (filtered) {
      emptyStateHeading.textContent = 'No apps match your filters';
      emptyStateLead.textContent = 'Try another name or slug, or clear your filters to see all apps.';
      emptyStateActions.hidden = false;
      return;
    }
    if (state.canCreateApps) {
      emptyStateHeading.innerHTML = 'Deploy your <strong>first Shiny app</strong>.';
      emptyStateLead.textContent =
        'Create an app, then deploy your code from the CLI. Browser upload is available when you need it.';
      emptyStateActions.hidden = false;
    } else {
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

  function attentionRail(gridEl) {
    return Array.from(gridEl.children).find((child) => child.classList.contains('app-attention')) || null;
  }

  function mountAttentionRail(gridEl, apps) {
    const current = attentionRail(gridEl);
    const next = renderAppAttention(document, apps, appLiveViews);
    if (current && next && current.dataset.signature === next.dataset.signature) return;

    const keepFocus = current ? focusedKey(current) : '';
    if (current && next) { next.open = current.open; current.replaceWith(next); }
    else if (current) current.remove();
    else if (next) gridEl.prepend(next);
    if (keepFocus && (!next || !restoreFocus(next, keepFocus))) {
      const parts = keepFocus.split(':');
      if (parts[0] === 'attention' && parts[1]) restoreFocus(gridEl, `app:${parts[1]}:title`);
    }
  }

  function refreshAttentionRail() {
    const grid = !projectDetailView.hidden ? projectDetailGrid : appGrid;
    if (appsView.hidden && projectDetailView.hidden) return;
    const visible = Array.from(grid.querySelectorAll('.app-card[data-slug]'))
      .map((card) => state.apps.find((app) => app.slug === card.dataset.slug))
      .filter(Boolean);
    mountAttentionRail(grid, visible);
  }

  // syncCardFromModel brings one card back in line with the stored app model,
  // optionally merging a live view first (a /metrics sample, or the row a
  // lifecycle action just returned). The badge, the lifecycle menu and the facts
  // line all describe the same app, so they are refreshed together and in this
  // order: the merge rewrites app.status AND app.deploying, and the menu and the
  // facts are derived from it. Derive them before the merge and they trail the
  // badge by one tick, offering Sleep and Stop on a deploy already in flight.
  //
  // This is what a one-app change costs: no /api/apps, no grid rebuild, and no
  // change to what the metrics poller is watching. Returns the app model, or
  // null when the fleet does not hold that slug.
  function syncCardFromModel(slug, live = null) {
    const app = state.apps && state.apps.find((item) => item.slug === slug);
    if (!app) return null;
    if (live) {
      appLiveViews.set(slug, live);
      applyLiveStatus(app, live);
    }
    const grid = !projectDetailView.hidden ? projectDetailGrid : appGrid;
    const card = grid.querySelector(`.app-card[data-slug="${slug}"]`);
    if (card) card.dataset.launchable = String(['running', 'idle', 'hibernated', 'degraded'].includes(app.status));
    paintCardStatusBadge(grid.querySelector(`.app-card .badge[data-slug="${slug}"]`), app, formatStatus);
    const kebabEl = grid.querySelector(`.kebab-menu[data-slug="${slug}"]`);
    if (kebabEl) syncCardActions(kebabEl, appCardActions(app, canManageApp(state.user, app)));
    if (card) syncCardPrimaryActions(card, app);
    const factsEl = grid.querySelector(`.app-card-facts[data-slug="${slug}"]`);
    if (factsEl) renderCardFacts(factsEl, app, live);
    refreshAttentionRail();
    updateAppsResults();
    return app;
  }

  function syncCardPrimaryActions(card, app) {
    const acts = appCardActions(app, canManageApp(state.user, app));
    const focusWasPrimary = card.querySelector('.app-actions > [data-card-primary]:focus');
    for (const [key, visible] of [['open', acts.showOpen], ['start', acts.startIsPrimary], ['overview', acts.showOverview]]) {
      const control = card.querySelector(`[data-card-primary="${key}"]`);
      if (control) control.hidden = !visible;
    }
    if (focusWasPrimary?.hidden) {
      card.querySelector('.app-actions > [data-card-primary]:not([hidden])')?.focus();
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

    const badge = card.querySelector('.badge[data-slug]');
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
    const grid = !projectDetailView.hidden ? projectDetailGrid : appGrid;
    const card = grid.querySelector(`.app-card[data-slug="${slug}"]`);
    applyRestartFeedbackToCard(slug, card, control);
    return entry;
  }

  function clearRestartFeedback(slug, expectedVersion = null) {
    const current = appRestartFeedback.get(slug);
    if (expectedVersion !== null && current && current.version !== expectedVersion) return;
    appRestartFeedback.delete(slug);
    const control = appCardLifecycleControls.get(slug);
    const grid = !projectDetailView.hidden ? projectDetailGrid : appGrid;
    const card = grid.querySelector(`.app-card[data-slug="${slug}"]`);
    applyRestartFeedbackToCard(slug, card, control);
  }

  // Cards revealed per `${grid id}:${project}` beyond the first GRID_PAGE.
  const gridWindowShown = new Map();

  function renderGridVerbatim(groups, gridEl, emptyEl, options = {}) {
    // The grid is rebuilt from scratch on every search keystroke, sort change,
    // project save and app reload. Whichever control the keyboard was on is
    // about to be discarded, so remember what it was and hand focus back to the
    // control doing the same job in the rebuilt grid. Every focusable control
    // below carries a data-focus-key naming its job, never its position, so a
    // rebuild that regroups or reorders the cards still finds it.
    const keepFocus = focusedKey(gridEl);
    gridEl.textContent = '';
    appCardLifecycleControls.clear();
    const total = groups.reduce((n, g) => n + g.apps.length, 0);
    const visibleApps = groups.flatMap((group) => group.apps);
    const empty = total === 0;
    emptyEl.hidden = !empty;
    if (empty) renderEmptyStateCopy(options);
    else mountAttentionRail(gridEl, visibleApps);

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
        disclosure.toggle.dataset.focusKey = `group:${group.project}:toggle`;
        if (group.project && isOperatorRole(state.user)) {
          const edit = document.createElement('button');
          edit.type = 'button';
          edit.className = 'app-grid-group-edit';
          edit.dataset.focusKey = `group:${group.project}:edit`;
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
        if (group.project) {
          const openProject = document.createElement('a');
          openProject.href = `/projects/${encodeURIComponent(group.project)}`;
          openProject.setAttribute('data-nav', '');
          openProject.className = 'app-grid-group-open';
          openProject.dataset.focusKey = `group:${group.project}:open`;
          openProject.textContent = 'View project';
          openProject.setAttribute('aria-label', `View project ${group.name}`);
          disclosure.header.appendChild(openProject);
        }
        gridEl.appendChild(disclosure.root);
        cardHost = disclosure.body;
      }

    // Render one page of cards per group; "Show more" reveals the next page.
    // The count is remembered per grid and group, so a poll-driven rebuild
    // keeps what the operator already expanded.
    const windowKey = `${gridEl.id}:${group.project}`;
    const count = windowCount(group.apps.length, gridWindowShown.get(windowKey), GRID_PAGE);
    for (const app of group.apps.slice(0, count)) {
      const card = document.createElement('div');
      card.className = 'app-card';
      card.dataset.slug = app.slug;
      card.dataset.launchable = String(['running', 'idle', 'hibernated', 'degraded'].includes(app.status));

      const header = document.createElement('div');
      header.className = 'app-header';

      const heading = document.createElement('div');
      heading.className = 'app-card-heading';
      heading.appendChild(renderAppAvatar(document, avatarView(app), 'app-card-avatar'));

      const identity = document.createElement('div');
      identity.className = 'app-card-identity';
      const titleLink = document.createElement('a');
      titleLink.href = `/apps/${app.slug}`;
      titleLink.setAttribute('data-nav', '');
      titleLink.className = 'app-card-title';
      titleLink.dataset.focusKey = `app:${app.slug}:title`;
      titleLink.setAttribute('aria-label', `View app ${app.name}`);
      const name = document.createElement('strong');
      name.textContent = app.name;
      titleLink.append(name);
      const slug = document.createElement('div');
      slug.className = 'app-card-slug';
      slug.textContent = `/${app.slug}`;
      identity.append(titleLink, slug);
      heading.appendChild(identity);
      header.appendChild(heading);

      // The status shares a consistent footer with the available actions.
      // Keep its own container so long labels can wrap independently.
      const badges = document.createElement('div');
      badges.className = 'app-header-badges';

      const badgeInfo = appCardBadge(app, formatStatus);
      const badge = document.createElement('span');
      badge.className = badgeInfo.cls;
      badge.textContent = badgeInfo.text;
      // Tag the status badge so the 10s metrics poll can refresh it in place
      // (see onMetrics → updateCardStatusBadge); without this the badge would
      // freeze at its render-time status and miss wake/hibernate transitions.
      badge.dataset.slug = app.slug;
      badges.appendChild(badge);

      let description = null;
      if (app.description) {
        description = document.createElement('p');
        description.className = 'app-card-description';
        description.textContent = app.description;
      }

      const facts = document.createElement('div');
      facts.className = 'app-card-facts';
      facts.dataset.slug = app.slug;
      renderCardFacts(facts, app);

      let governance = null;
      if (app.managed_by) {
        governance = document.createElement('p');
        governance.className = 'app-card-governance';
        governance.textContent = 'Fleet managed';
        governance.title = `Managed by ${app.managed_by}`;
      }

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

      {
        const openLink = document.createElement('a');
        openLink.href = `/app/${app.slug}/`;
        openLink.target = '_blank';
        openLink.rel = 'noopener noreferrer';
        openLink.dataset.focusKey = `app:${app.slug}:open`;
        openLink.dataset.cardPrimary = 'open';
        openLink.append(document.createTextNode('Open app'));
        const externalArrow = document.createElement('span');
        externalArrow.setAttribute('aria-hidden', 'true');
        externalArrow.textContent = '↗';
        openLink.appendChild(externalArrow);
        openLink.setAttribute('aria-label', `Open app ${app.name} in a new tab`);
        actions.appendChild(openLink);

        const startButton = document.createElement('button');
        startButton.type = 'button';
        startButton.textContent = 'Start app';
        startButton.dataset.cardPrimary = 'start';
        startButton.dataset.focusKey = `app:${app.slug}:start`;
        startButton.setAttribute('aria-label', `Start app ${app.name}`);
        startButton.addEventListener('click', () => startApp(app.slug, startButton));
        actions.appendChild(startButton);

        const overviewLink = document.createElement('a');
        overviewLink.href = `/apps/${app.slug}`;
        overviewLink.setAttribute('data-nav', '');
        overviewLink.dataset.cardPrimary = 'overview';
        overviewLink.dataset.focusKey = `app:${app.slug}:overview`;
        overviewLink.textContent = 'View app';
        overviewLink.setAttribute('aria-label', `View app overview for ${app.name}`);
        actions.appendChild(overviewLink);
      }

      if (canManage) {
        // First deploy is required activation, so it stays visible. Once the
        // app has a bundle, manual redeploy remains available in the overflow
        // menu without occupying every card (CI/CLI may own that workflow).
        if (cardActions.deployIsPrimary) {
          const deployButton = document.createElement('button');
          deployButton.type = 'button';
          deployButton.textContent = 'Deploy code';
          deployButton.className = 'btn-primary';
          deployButton.dataset.focusKey = `app:${app.slug}:deploy`;
          deployButton.setAttribute('aria-label', `Deploy code for ${app.name}`);
          deployButton.addEventListener('click', () => openDeployModal(app));
          actions.appendChild(deployButton);
        }

        // The menu lists whichever lifecycle actions apply to this app right
        // now. Start stays on the card when it is the primary action; a
        // running app's menu holds Restart, Sleep and Stop. All rows are always
        // rendered and hidden per state rather than built from the current
        // state, so the metrics poller can re-sync them in place when an app
        // hibernates or wakes between grid loads.
        const lifecycleItems = [
          ['redeploy', 'Deploy code', () => openDeployModal(app)],
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
        kebabBtn.dataset.focusKey = `app:${app.slug}:menu`;
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

      const body = document.createElement('div');
      body.className = 'app-card-body';
      body.appendChild(facts);
      if (description) body.appendChild(description);
      if (governance) body.appendChild(governance);
      const footer = document.createElement('div');
      footer.className = 'app-card-footer';
      footer.append(badges, actions);
      card.append(header, body, lifecycleControl.root, footer);
      syncCardPrimaryActions(card, app);
      applyRestartFeedbackToCard(app.slug, card, lifecycleControl);
      cardHost.appendChild(card);
    }
    const more = createShowMore(document, {
      hidden: group.apps.length - count,
      page: GRID_PAGE,
      className: 'app-grid-show-more',
      focusKey: `group:${group.project}:more`,
      onMore: () => {
        gridWindowShown.set(windowKey, count + GRID_PAGE);
        const firstNew = group.apps[count];
        renderGridVerbatim(groups, gridEl, emptyEl, options);
        if (firstNew) restoreFocus(gridEl, `app:${firstNew.slug}:title`);
      },
    });
    if (more) cardHost.appendChild(more);
    }

    // A control can legitimately vanish across a rebuild: "Deploy first
    // release" disappears the moment that deploy succeeds. The card is still
    // there, so fall back to its title link and leave the keyboard on the app
    // the operator was working on instead of dropping it to the top of the page.
    if (keepFocus && !restoreFocus(gridEl, keepFocus)) {
      restoreFocus(gridEl, siblingKey(keepFocus, 'title'));
    }
  }

  function appsMatchingFilters() {
    const searchEl = document.getElementById('apps-search');
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
    return { apps, q, segEl };
  }

  function updateAppsResults(matches = appsMatchingFilters()) {
    const { apps, q, segEl } = matches;
    const resultsStatus = document.getElementById('apps-results-status');
    document.querySelector('#apps-view .apps-filters').hidden = state.apps.length === 0;
    document.querySelector('#apps-view .apps-results').hidden = state.apps.length === 0;
    document.getElementById('apps-clear-filters').hidden = !q && (!segEl || segEl.value === 'all');
    if (resultsStatus) {
      const filtered = !!q || !!(segEl && segEl.value !== 'all');
      const count = filtered
        ? `${apps.length} of ${state.apps.length} ${state.apps.length === 1 ? 'app' : 'apps'}`
        : `${apps.length} ${apps.length === 1 ? 'app' : 'apps'}`;
      const text = [count, appListSummary(apps, formatStatus)].filter(Boolean).join(' · ');
      if (resultsStatus.textContent !== text) resultsStatus.textContent = text;
    }
  }

  function renderApps() {
    const matches = appsMatchingFilters();
    const { apps, q, segEl } = matches;
    const sortEl = document.getElementById('apps-sort');
    updateAppsResults(matches);

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
      { forceExpanded: !!q, filtered: !!q || !!(segEl && segEl.value !== 'all') },
    );
  }

  const sessionController = createSessionController({
    request: api,
    onExpired: handleUnauthorized,
    onSession: payload => {
      // Cookies are shared by tabs. An account switch in another tab must not
      // silently make this tab's pending edits act as a different person.
      if (state.user && payload.user.id !== state.user.id) {
        handleUnauthorized();
        return;
      }
      const wasAdmin = state.user?.role === 'admin';
      applySessionCapabilities(payload);
      if (payload.user.role !== 'admin') supportRecovery.clear();
      else if (!wasAdmin) supportRecovery.load({ announce: false });
    },
  });

  function showLoggedOut() {
    sessionVersion += 1;
    resetDeployModal();
    state.sessionIdentity = {};
    closeNewTokenModal(true);
    closeNewAppModal(true);
    closeDeployModal();
    sessionController.stop();
    tokenManager.stop();
    closeLogs();
    metrics.setTargets([]);
    state.user = null;
    router.reset();
    detailApp = null;
    appContextNavigation?.clear();
    closeEnvForm(true);
    clearSettingsDirty();
    settingsSlug = null;
    settingsRevision++;
    envApplyStates.clear();
    state.apps = [];
    auditExplorer.stop();
    state.canCreateApps = false;
    state.canManageApps = false;
    state.canManageAnnouncements = false;
    document.getElementById('tab-announcements').hidden = true;
    sidebarAccount.close();
    document.getElementById('administration-nav').hidden = true;
    document.getElementById('announcements-view').hidden = true;
    state.appIsolationWarning = false;
    if (appIsolationBanner) appIsolationBanner.hidden = true;
    supportRecovery.clear();
    appRestartFeedback.clear();
    appCardLifecycleControls.clear();
    appLiveViews.clear();
    newAppButton.hidden = true;
    closeProfileModal(true);
    passwordVisibility.reset(loginForm);
    passwordInput.value = '';
    renderIdentity(null);
    setHidden(logoutButton, true);
    setHidden(loginView, false);
    setHidden(overviewView, true);
    setHidden(launchpadView, true);
    setHidden(appsView, true);
    setHidden(projectDetailView, true);
    setHidden(usersView, true);
    setHidden(tokensView, true);
    setHidden(document.getElementById('workers-view'), true);
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

  // Initial sign-in and renewal share every server-computed capability and
  // control. Updating identity must never reset an operator's open work.
  function applySessionCapabilities(payload) {
    state.user = payload.user;
    state.canCreateApps = !!payload.can_create_apps;
    state.canManageApps = !!payload.can_manage_apps;
    state.canReadAudit = !!payload.can_read_audit;
    state.canManageAnnouncements = !!payload.can_manage_announcements;
    state.appIsolationWarning = !!payload.app_isolation_warning;
    renderIdentity(payload.user);
    tabAudit.hidden = !state.canReadAudit;
    tabUsers.hidden = payload.user.role !== 'admin';
    tabWorkers.hidden = payload.user.role !== 'admin';
    tabOverview.hidden = !isOperatorRole(payload.user);
    tabApps.hidden = false;
    newAppButton.hidden = !state.canCreateApps;
    document.getElementById('tab-announcements').hidden = !state.canManageAnnouncements;
    document.getElementById('administration-nav').hidden = payload.user.role !== 'admin' && !state.canReadAudit && !state.canManageAnnouncements;
    if (appIsolationBanner) {
      appIsolationBanner.hidden = !shouldShowAppIsolationBanner(state.appIsolationWarning);
    }
    const selfService = payload.user.can_manage_self !== false;
    document.getElementById('account-tokens-link').hidden = !selfService;
    if (newTokenButton) newTokenButton.hidden = !selfService;
    if (cliConnectPanel) cliConnectPanel.hidden = !selfService;
    syncSidebar();
    document.dispatchEvent(new Event('session-capabilities-change'));
  }

  function showLoggedIn(payload) {
    state.sessionIdentity = {};
    sessionController.start(payload);
    applySessionCapabilities(payload);
    setHidden(logoutButton, false);
    setHidden(loginView, true);
    document.body.dataset.auth = 'in';
    if (payload.user.role === 'admin') supportRecovery.load({ announce: true });
    else supportRecovery.clear();
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
    renderUserAvatar(identityAvatar, user);
    identityName.textContent = m.name;
    identityRole.textContent = m.roleLabel;
  }

  function profilePreviewUser() {
    return { ...state.user, avatar_color: pendingAvatarColor !== undefined ? pendingAvatarColor : state.user?.avatar_color };
  }

  function renderProfileAvatar() {
    renderUserAvatar(profileAvatar, profilePreviewUser(), pendingAvatar !== undefined ? pendingAvatar : state.user?.avatar_url || '');
  }

  function profileHasChanges() {
    if (!state.user || state.user.can_manage_self === false) return false;
    return pendingAvatar !== undefined ||
      (pendingAvatarColor !== undefined && pendingAvatarColor !== (state.user.avatar_color || '')) ||
      (state.user.can_set_password && (profileDisplayName.value.trim() !== (state.user.display_name || '') ||
        profileCurrentPw.value !== '' || profileNewPw.value !== ''));
  }

  function syncProfileSave() {
    const dirty = profileHasChanges();
    const hadSaveFocus = document.activeElement === profileSubmit;
    profileSubmit.disabled = profileSaving || photoPreparing || !dirty;
    profileForm.setAttribute('aria-busy', String(profileSaving || photoPreparing));
    if (profileCancel) profileCancel.textContent = dirty ? 'Discard changes' : 'Close';
    if (!dirty && !profileSaving && !photoPreparing && hadSaveFocus && profileCancel) profileCancel.focus({ preventScroll: true });
    if (profilePhotoStatus) {
      profilePhotoStatus.textContent = !profileError.hidden && !photoPreparing ? '' :
        photoPreparing ? 'Preparing photo…' : profileSaving ? 'Saving…' : dirty ? 'Unsaved changes' : '';
    }
  }

  function syncProfileColor() {
    const user = profilePreviewUser();
    if (profileAvatarColor) {
      profileAvatarColor.value = avatarPickerColor(user);
      profileAvatarColor.disabled = profileSaving;
    }
    if (profileColorReset) profileColorReset.disabled = profileSaving || !user.avatar_color;
  }

  function openProfileModal() {
    if (!profileModal || !state.user) return;
    if (sidebarDrawer) sidebarDrawer.close();
    const u = state.user;
    const m = identityModel(u);
    photoGeneration++;
    pendingAvatar = undefined;
    pendingAvatarColor = undefined;
    syncProfileColor();
    photoPreparing = false;
    passwordVisibility.reset(profileForm);
    document.getElementById('profile-initials').open = false;
    for (const field of ['name', 'current', 'new']) setProfileFieldError(document, field);
    if (profilePwSection) profilePwSection.open = false;
    if (profilePhotoStatus) profilePhotoStatus.textContent = '';
    if (profilePhotoFile) profilePhotoFile.value = '';
    renderUserAvatar(profileAvatar, u);
    setHidden(profilePhotoControls, u.can_manage_self === false);
    setHidden(profilePhotoRemove, !u.avatar_url);
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
    setHidden(profileSubmit, !selfService);
    profileDisplayName.value = u.display_name || '';
    profileCurrentPw.value = '';
    profileNewPw.value = '';
    setError(profileError, '');
    syncProfileSave();
    setHidden(profileSuccess, true);
    reflectThemeRadios();
    profileModal.hidden = false;
    modalTrap(profileModal).activate();
    (selfService ? profilePhotoChange : (profileClose || logoutButton)).focus({ preventScroll: true });
    const body = profileModal.querySelector('.profile-body');
    if (body) body.scrollTop = 0;
  }

  // reflectThemeRadios checks the radio matching the stored theme preference, so
  // the Appearance control shows the current choice each time the modal opens.
  function reflectThemeRadios() {
    const pref = getThemePreference(window);
    document.querySelectorAll('input[name="theme-pref"]').forEach((el) => {
      el.checked = el.value === pref;
    });
  }

  function closeProfileModal(force = false) {
    if (!profileModal || profileModal.hidden) return true;
    if (force !== true) {
      if (profileSaving) { setError(profileError, 'Wait for your changes to finish saving.'); return false; }
      if ((profileHasChanges() || photoPreparing) && !window.confirm('Discard your unsaved profile changes? Appearance changes have already been applied.')) return false;
    }
    profileModal.hidden = true;
    profileCurrentPw.value = '';
    profileNewPw.value = '';
    passwordVisibility.reset(profileForm);
    photoGeneration++;
    modalTrap(profileModal).release();
    setError(profileError, '');
    setHidden(profileSuccess, true);
    return true;
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
    if (state.user.can_manage_self === false || photoPreparing || profileSaving || !profileHasChanges()) return;
    setError(profileError, '');
    setHidden(profileSuccess, true);

    const errors = canSetPw ? profileFieldErrors(profileDisplayName.value, profileCurrentPw.value, profileNewPw.value) : {};
    for (const field of ['name', 'current', 'new']) setProfileFieldError(document, field, errors[field]);
    if (Object.keys(errors).length) {
      profileForm.querySelector('[aria-invalid="true"]').focus();
      return;
    }
    const body = canSetPw ? { display_name: profileDisplayName.value.trim() } : {};
    if (pendingAvatar !== undefined) body.avatar = pendingAvatar;
    if (pendingAvatarColor !== undefined) body.avatar_color = pendingAvatarColor;
    const wantsPwChange = canSetPw &&
      (profileNewPw.value.length > 0 || profileCurrentPw.value.length > 0);
    if (wantsPwChange) {
      body.current_password = profileCurrentPw.value;
      body.new_password = profileNewPw.value;
    }

    const generation = photoGeneration;
    profileSaving = true;
    syncProfileColor();
    for (const input of [profileDisplayName, profileCurrentPw, profileNewPw]) input.disabled = true;
    if (profilePhotoStatus) profilePhotoStatus.textContent = 'Saving…';
    if (profilePhotoChange) profilePhotoChange.disabled = true;
    if (profilePhotoRemove) profilePhotoRemove.disabled = true;
    try {
      await runInFlight(profileSubmit, async () => {
        let resp;
        try {
          resp = await api('/api/auth/me', { method: 'PATCH', body: JSON.stringify(body) });
        } catch {
          setError(profileError, wantsPwChange ? 'The save could not be confirmed. Your password may have changed. Check your connection; if saving again fails, sign in with your new password.' : 'The save could not be confirmed. Your edits are still here. Check your connection and save again.');
          return;
        }
        if (!resp.ok) {
          let msg = 'Your changes could not be saved. Try again.';
          try { const j = await resp.json(); if (j && j.error) msg = j.error; } catch {}
          if (resp.status === 401 && msg === 'current password is incorrect') {
            setProfileFieldError(document, 'current', 'Your current password is incorrect. Try again.');
            profileCurrentPw.disabled = false;
            profileCurrentPw.focus();
          } else if (resp.status === 401) { await handleUnauthorized(); }
          else setError(profileError, resp.status === 429 ? 'Too many profile changes. Wait a minute and save again. Your edits are still here.' : msg);
          return;
        }
        let data = null;
        try { data = await resp.json(); } catch {}
        if (!data?.user) {
          setError(profileError, 'The save could not be confirmed. Your changes may have been saved. Reopen your profile to check before trying again.');
          return;
        }
        // A late response must never restore an account after sign-out or
        // overwrite a different person's profile after an account switch.
        if (generation !== photoGeneration || state.user?.id !== data.user.id) return;
        state.user = data.user;
        renderIdentity(data.user);
        pendingAvatar = undefined;
        pendingAvatarColor = undefined;
        renderProfileAvatar();
        syncProfileColor();
        profileDisplayName.value = data.user.display_name || '';
        const identity = identityModel(data.user);
        profileUsername.textContent = identity.name;
        profileLogin.textContent = identity.secondary ? `@${identity.secondary}` : '';
        setHidden(profilePhotoRemove, !data.user.avatar_url);
        if (profilePhotoStatus) profilePhotoStatus.textContent = '';
        profileCurrentPw.value = '';
        profileNewPw.value = '';
        passwordVisibility.reset(profileForm);
        if (wantsPwChange && profilePwSection) profilePwSection.open = false;
        profileSuccess.textContent = wantsPwChange ? 'Profile and password updated' : 'Changes saved';
        setHidden(profileSuccess, false);
        // If the admin Users table is on screen, refresh it so this user's display
        // name updates there too.
        if (usersView && !usersView.hidden) loadUsers();
      });
    } finally {
      profileSaving = false;
      for (const input of [profileDisplayName, profileCurrentPw, profileNewPw]) input.disabled = false;
      syncProfileColor();
      syncProfileSave();
      if (profilePhotoChange) profilePhotoChange.disabled = false;
      if (profilePhotoRemove) profilePhotoRemove.disabled = false;
    }
  }

  async function handleUnauthorized() {
    if (!state.user) return;
    sessionController.end();
    // A 401 wipes all client state unconditionally. If the operator had unsaved
    // settings edits in flight, silently discarding them with no explanation
    // looks like data loss; check for dirty edits before the logout clears
    // state, and surface a clear message so they know to redo the edit after
    // logging in.
    const hadUnsavedChanges = anySettingsDirty();
    showLoggedOut();
    setError(loginError, hadUnsavedChanges
      ? 'Your session ended. Unsaved changes were lost. Please sign in again.'
      : 'Your session ended. Please sign in again.');
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
    // App readiness is reported by the list's status counts. This aggregate
    // banner is reserved for incidents, not a blanket claim that every app is ready.
    if (s.statusLabel === 'healthy') { fleetHealthEl.hidden = true; return; }
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
    const version = sessionVersion;
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
      if (version === sessionVersion) setError(appError, 'Could not refresh apps. Check your connection and use Refresh to retry.');
      return;
    }
    if (version !== sessionVersion) return;

    if (response.status === 401) {
      clearGridLoading();
      await handleUnauthorized();
      return;
    }
    if (!response.ok) {
      clearGridLoading();
      setError(appError, 'Could not refresh apps. Use Refresh to retry.');
      return;
    }

    {
      // Standard {items,...} list envelope; tolerate a bare array for resilience.
      let gridBody;
      try {
        gridBody = await response.json();
        if (!Array.isArray(gridBody) && !Array.isArray(gridBody?.items)) throw new Error('Invalid app list');
      } catch {
        clearGridLoading();
        setError(appError, 'Could not read the app list. Use Refresh to retry.');
        return;
      }
      if (version !== sessionVersion) return;
      state.apps = Array.isArray(gridBody) ? gridBody : gridBody.items;
      const visibleSlugs = new Set(state.apps.map((app) => app.slug));
      for (const slug of appRestartFeedback.keys()) {
        if (!visibleSlugs.has(slug)) appRestartFeedback.delete(slug);
      }
      // A list reload is a newer snapshot than any retained metrics response.
      // Render from that snapshot, then let the next poll repopulate live views.
      appLiveViews.clear();
    }
    clearGridLoading();
    // Rebuild cards only when the Apps index owns the screen. A project page
    // renders the same cards into its own grid; rebuilding the hidden fleet
    // grid would replace its lifecycle-control registry behind its back.
    if (!appsView.hidden) renderApps();
    syncSidebar();
    // Consumers outside the Apps index (notably a mounted project page) own
    // their card DOM and need to rebuild it from this newer app snapshot.
    document.dispatchEvent(new CustomEvent('shinyhub:apps-reloaded'));
    // The poller follows what is on screen, and the mounted view is what decides
    // that: the grid watches every app, an app's detail page watches one. A
    // reload triggered from a detail page (a setting saved, an app deployed)
    // must not silently widen that back to the whole fleet, which is a tenfold
    // increase in polling for a page showing one app.
    if (!document.getElementById('apps-view').hidden) {
      metrics.setTargets(state.apps.map(a => a.slug));
    }
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
    setError(document.getElementById('project-edit-error'), '');
    projectEditSave.disabled = false;
    projectEditSave.textContent = 'Save';
    projectEditCancel.disabled = false;
    modal.removeAttribute('aria-busy');
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
    if (projectEditSave.disabled) return;
    const slug = modal.dataset.slug;
    const body = buildProjectPatchBody({
      name: document.getElementById('project-edit-name').value,
      iconEmoji: document.getElementById('project-edit-icon').value,
      description: document.getElementById('project-edit-description').value,
      descriptionKnown: modal.dataset.descriptionKnown === '1',
    });
    projectEditSave.disabled = true;
    projectEditSave.textContent = 'Saving…';
    projectEditCancel.disabled = true;
    modal.setAttribute('aria-busy', 'true');
    let resp;
    try {
      resp = await api(`/api/projects/${encodeURIComponent(slug)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
    } catch {
      setError(document.getElementById('project-edit-error'), 'Could not reach the server. Check your connection and try again.');
      projectEditSave.disabled = false;
      projectEditSave.textContent = 'Save';
      projectEditCancel.disabled = false;
      modal.removeAttribute('aria-busy');
      return;
    }
    if (resp.status === 401) {
      projectEditSave.disabled = false;
      projectEditSave.textContent = 'Save';
      projectEditCancel.disabled = false;
      modal.removeAttribute('aria-busy');
      await handleUnauthorized();
      return;
    }
    if (!resp.ok) {
      setError(document.getElementById('project-edit-error'), 'Could not save the project. Check the name and icon and try again.');
      projectEditSave.disabled = false;
      projectEditSave.textContent = 'Save';
      projectEditCancel.disabled = false;
      modal.removeAttribute('aria-busy');
      return;
    }
    projectEditSave.disabled = false;
    projectEditSave.textContent = 'Save';
    projectEditCancel.disabled = false;
    modal.removeAttribute('aria-busy');
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

  let appContextNavigation = null;
  function syncAppContext(path = location.pathname) {
    if (!appContextNavigation) return;
    if (!state.user || !detailApp || !(path === `/apps/${encodeURIComponent(detailApp.slug)}` || path.startsWith(`/apps/${encodeURIComponent(detailApp.slug)}/`))) { appContextNavigation.clear(); return; }
    appContextNavigation.update({ app: detailApp, apps: state.apps, canManage: canManageApp(state.user, detailApp), path });
  }

  function syncSidebar() {
    syncAppContext();
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

  const auditExplorer = createAuditExplorer({
    document, state, api, navigate: (path, options) => router.navigate(path, options),
    onUnauthorized: handleUnauthorized, copy: text => navigator.clipboard.writeText(text), origin: window.location.origin,
  });

  const peopleDirectory = createPeopleDirectory({
    document, state, api, onUnauthorized: handleUnauthorized,
    navigate: (path, options) => router.navigate(path, options), flashToast,
    confirm: message => window.confirm(message), makeCopyButton,
    openResetPasswordModal, openSupportSessionModal,
    onReplaced: result => newPerson.showInvitation(result, true),
    onAccessLost: () => clearIdentityServices(),
  });
  function loadUsers() { return peopleDirectory.load(); }

  function showIdentityPanel(kind) {
    const services = kind === 'services';
    identityPeoplePanel.hidden = services;
    identityServicesPanel.hidden = !services;
    identityPeopleTab.setAttribute('aria-selected', String(!services));
    identityServicesTab.setAttribute('aria-selected', String(services));
	identityPeopleTab.tabIndex = services ? -1 : 0;
	identityServicesTab.tabIndex = services ? 0 : -1;
    peopleDirectory.applyOnboarding(services);
    if (services) loadServiceAccounts();
  }

  let serviceLoadGeneration = 0;
  function clearIdentityServices() {
    serviceLoadGeneration++;
    newPerson.reset();
    if (serviceCredentialModal && !serviceCredentialModal.hidden) {
      serviceCredentialSecretRevealed = false; closeServiceCredentialModal();
    }
    if (resetPwModal && !resetPwModal.hidden) closeResetPasswordModal();
    serviceAccountsList?.replaceChildren(); serviceAccountsList?.removeAttribute('aria-busy');
    if (serviceAccountsStatus) serviceAccountsStatus.textContent = '';
    setError(serviceAccountsError, '');
  }
  async function loadServiceAccounts() {
    if (!serviceAccountsList || !peopleDirectory.current()) return;
    const generation = ++serviceLoadGeneration;
    setError(serviceAccountsError, '');
    const hadInventory = Boolean(serviceAccountsList.querySelector('.service-account-card'));
    serviceAccountsList.setAttribute('aria-busy', 'true');
    serviceAccountsStatus.textContent = 'Loading service accounts.';
    if (!serviceAccountsList.childElementCount) {
      serviceAccountsList.innerHTML = '<p class="grid-loading" role="status">Loading service accounts…</p>';
    }
    let accountsResp;
    try {
      accountsResp = await peopleDirectory.request('/api/service-accounts');
    } catch {
      if (!peopleDirectory.current() || generation !== serviceLoadGeneration) return;
      serviceAccountsList.removeAttribute('aria-busy');
      if (!hadInventory) serviceAccountsList.textContent = '';
      setError(serviceAccountsError, 'Service accounts could not be loaded. Check your connection and try again.');
      serviceAccountsStatus.textContent = 'Service accounts could not be loaded.';
      return;
    }
    if (!peopleDirectory.current() || generation !== serviceLoadGeneration) return;
    if (accountsResp.status === 401) { await handleUnauthorized(); return; }
    if (!accountsResp.ok) {
      serviceAccountsList.removeAttribute('aria-busy');
      if (!hadInventory) serviceAccountsList.textContent = '';
      setError(serviceAccountsError, accountsResp.status === 403 ? 'Human administrators manage service accounts.' : 'Service accounts could not be loaded. Try again.');
      serviceAccountsStatus.textContent = 'Service accounts could not be loaded.';
      return;
    }
    let accountBody;
    try { accountBody = await accountsResp.json(); if (!Array.isArray(accountBody?.items)) throw new Error(); }
    catch {
      if (!peopleDirectory.current() || generation !== serviceLoadGeneration) return;
      serviceAccountsList.removeAttribute('aria-busy');
      setError(serviceAccountsError, 'Could not read service accounts. Select Refresh to try again.'); return;
    }
    const accounts = accountBody.items;
    const hydrated = await Promise.all(accounts.map(async account => {
      try {
        const response = await peopleDirectory.request(`/api/service-accounts/${encodeURIComponent(account.key)}/credentials`);
        if (!response.ok) return {...account, credentials: [], credentialsError: true};
        const body = await response.json();
        return {...account, credentials: Array.isArray(body?.items) ? body.items : []};
      } catch { return {...account, credentials: [], credentialsError: true}; }
    }));
    if (!peopleDirectory.current() || generation !== serviceLoadGeneration) return;
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
        <header><div><h3>${escapeHtml(account.name)}</h3><p>Platform-managed account</p></div><button type="button" class="btn-primary" data-service-create="${escapeHtml(account.key)}">New credential</button></header>
        ${tools}
        ${!credentials.length && !account.credentialsError ? '<p class="service-credential-empty">No credentials yet. Create one for your first team or pipeline.</p>' : `<div class="service-credential-table"><table role="table"><thead role="rowgroup"><tr role="row"><th role="columnheader" scope="col" id="${headers.credential}">Credential</th><th role="columnheader" scope="col" id="${headers.role}">Role</th><th role="columnheader" scope="col" id="${headers.apps}">Apps</th><th role="columnheader" scope="col" id="${headers.expires}">Expires</th><th role="columnheader" scope="col" id="${headers.used}">Last used</th><th role="columnheader" scope="col" id="${headers.action}"><span class="sr-only">Action</span></th></tr></thead><tbody role="rowgroup">${body}</tbody></table></div>`}
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
    const generation = serviceLoadGeneration;
    serviceCredentialForm.setAttribute('aria-busy', 'true');
    let response;
    try {
      response = await peopleDirectory.request(`/api/service-accounts/${encodeURIComponent(serviceCredentialAccountKey)}/credentials`, {method: 'POST', body: JSON.stringify({
        name: credentialName, role: serviceCredentialRole.value,
        apps: scope === 'apps' ? apps : [], unrestricted: scope === 'all',
        expires_in_days: Number(serviceCredentialExpiry.value),
      })});
    } catch {
      if (!peopleDirectory.current() || generation !== serviceLoadGeneration) return;
      setError(serviceCredentialError, 'Could not confirm credential creation. Refresh the credential list before trying again.');
      submit.disabled = false;
      submit.textContent = 'Create credential';
      serviceCredentialForm.removeAttribute('aria-busy');
      return;
    }
    if (!peopleDirectory.current() || generation !== serviceLoadGeneration) return;
    submit.disabled = false;
    submit.textContent = 'Create credential';
    serviceCredentialForm.removeAttribute('aria-busy');
    if (!response.ok) {
      const body = await response.json().catch(() => ({}));
      setError(serviceCredentialError, body.error || 'Credential could not be created.');
      return;
    }
    let credential;
    try {
      credential = await response.json();
      if (typeof credential?.token !== 'string' || !credential.token) throw new Error();
    } catch {
      if (peopleDirectory.current() && generation === serviceLoadGeneration)
        setError(serviceCredentialError, 'Could not confirm credential creation. Refresh the credential list before trying again.');
      return;
    }
    if (!peopleDirectory.current() || generation !== serviceLoadGeneration) return;
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

  async function openSupportSessionModal(user, draft = null) {
    document.getElementById('support-session-trusted-warning').hidden = state.supportSessions.trusted_apps !== true;
    state.supportTarget = user;
    supportUsername.textContent = user.username;
    supportReason.value = draft?.reason || '';
    supportReason.setCustomValidity('');
    supportApp.textContent = '';
    supportApp.removeAttribute('aria-invalid');
    supportApp.removeAttribute('aria-errormessage');
    const loadingOption = document.createElement('option');
    loadingOption.textContent = 'Loading eligible apps…';
    loadingOption.disabled = true;
    loadingOption.selected = true;
    supportApp.appendChild(loadingOption);
    supportApp.disabled = true;
    supportSubmit.disabled = true;
    supportAppsRetry.hidden = true;
    supportReauth.hidden = true;
    supportAppStatus.textContent = 'Loading eligible apps…';
    supportModalLock.setPending(false);
    setError(supportError, '');
    supportModal.hidden = false;
    modalTrap(supportModal).activate();
    supportClose.focus();
    await loadSupportSessionApps({ advanceFocus: !draft });
    if (draft && state.supportTarget?.id === user.id && !supportApp.disabled) {
      const dialogStillOwnsFocus = document.activeElement === supportClose;
      const restored = restoreSupportAppSelection(supportApp, supportSubmit, draft.app_slug, { focusOnMissing: dialogStillOwnsFocus });
      if (!restored) {
        setError(supportError, 'The previously selected app is no longer available. Choose another app to continue.');
        supportAppStatus.textContent = 'The previous app is unavailable; an explicit app choice is required.';
      }
      if (restored && supportReason.value === (draft.reason || '') && dialogStillOwnsFocus) {
        supportReason.focus();
      }
    }
  }

  async function loadSupportSessionApps({ advanceFocus = true } = {}) {
    const target = state.supportTarget;
    if (!target) return;
    const request = supportAppsGate.begin();
    const focusOwner = document.activeElement;
    supportApp.textContent = '';
    supportApp.removeAttribute('aria-invalid');
    supportApp.removeAttribute('aria-errormessage');
    const loadingOption = document.createElement('option');
    loadingOption.textContent = 'Loading eligible apps…';
    loadingOption.disabled = true;
    loadingOption.selected = true;
    supportApp.appendChild(loadingOption);
    supportApp.disabled = true;
    supportSubmit.disabled = true;
    supportAppsRetry.hidden = true;
    supportAppStatus.textContent = 'Loading eligible apps…';
    setError(supportError, '');
    try {
      const resp = await api(`/api/users/${encodeURIComponent(target.id)}/support-apps`, { signal: request.signal });
      if (resp.status === 401) { await handleUnauthorized(); return; }
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
      const body = await resp.json();
	  if (!supportAppsGate.isCurrent(request.generation) || !state.supportTarget || state.supportTarget.id !== target.id) return;
      const apps = Array.isArray(body?.items) ? body.items : [];
      supportApp.textContent = '';
      for (const app of apps.slice().sort((a, b) => (a.name || a.slug).localeCompare(b.name || b.slug))) {
      const option = document.createElement('option');
      option.value = app.slug;
      option.textContent = app.name && app.name !== app.slug ? `${app.name} (${app.slug})` : app.slug;
      supportApp.appendChild(option);
      }
      if (!supportApp.options.length) {
        const option = document.createElement('option');
        option.textContent = 'This user has no accessible apps';
        option.disabled = true;
        supportApp.appendChild(option);
        setError(supportError, 'There are no apps this user can access.');
        supportAppStatus.textContent = 'No eligible apps found.';
        return;
      }
      supportApp.disabled = false;
      supportSubmit.disabled = false;
      supportAppStatus.textContent = `${apps.length} eligible app${apps.length === 1 ? '' : 's'} loaded.`;
      if (advanceFocus && (document.activeElement === focusOwner || (focusOwner === supportAppsRetry && document.activeElement === document.body))) {
        supportApp.focus();
      }
    } catch (error) {
	  if (error?.name === 'AbortError' || !supportAppsGate.isCurrent(request.generation) || !state.supportTarget || state.supportTarget.id !== target.id) return;
      supportApp.textContent = '';
      const option = document.createElement('option');
      option.textContent = 'Apps unavailable';
      option.disabled = true;
      supportApp.appendChild(option);
      setError(supportError, 'Eligible apps could not be loaded. Check your connection and retry.');
      supportAppStatus.textContent = 'Eligible apps could not be loaded.';
      supportAppsRetry.hidden = false;
      if (document.activeElement === focusOwner || (focusOwner === supportAppsRetry && document.activeElement === document.body)) {
        supportAppsRetry.focus();
      }
    }
  }

  function closeSupportSessionModal() {
    supportModalLock.requestDismiss(() => {
      supportModal.hidden = true;
      modalTrap(supportModal).release();
      state.supportTarget = null;
      supportAppsGate.invalidate();
      supportReason.value = '';
      supportAppsRetry.hidden = true;
      supportReauth.hidden = true;
      setError(supportError, '');
    });
  }

  async function submitSupportSession(event) {
    event.preventDefault();
    const target = state.supportTarget;
    const reason = supportReason.value.trim();
    if (!target || !supportApp.value) return;
    const reasonError = supportReasonError(reason);
    if (reasonError) {
      setError(supportError, reasonError);
      supportReason.focus();
      return;
    }
    const submitOwnedFocus = document.activeElement === supportSubmit;
    supportSubmit.disabled = true;
    supportSubmit.textContent = 'Starting…';
    supportModalLock.setPending(true);
    let restoreSubmitFocus = false;
    let launched = false;
    try {
      const resp = await api('/api/support-sessions', {
        method: 'POST',
        body: JSON.stringify({ user_id: target.id, app_slug: supportApp.value, reason }),
      });
      if (resp.status === 401) { await handleUnauthorized(); return; }
      if (!resp.ok) {
        let message = 'The support session could not be started.';
        try {
          const body = await resp.json();
          if (body?.error) message = body.error;
          supportReauth.hidden = body?.code !== 'recent_authentication_required';
        } catch {}
        if (resp.status === 409) {
          message += ' Close this dialog to manage the active session from the dashboard.';
          supportRecovery.load({ announce: true });
        }
        setError(supportError, message);
        if (!supportReauth.hidden) supportReauth.focus();
        else restoreSubmitFocus = submitOwnedFocus;
        return;
      }
      const session = await resp.json();
      launched = true;
      window.location.assign(session.launch_url);
    } catch {
      setError(supportError, 'Network error. Check your connection and try again.');
      restoreSubmitFocus = submitOwnedFocus;
    } finally {
      if (!launched) {
        supportSubmit.disabled = false;
        supportSubmit.textContent = 'Start 15-minute session';
        supportModalLock.setPending(false);
        if (restoreSubmitFocus) restoreFailedActionFocus(supportSubmit, true);
      }
    }
  }

  async function reauthenticateForSupportSession() {
    const reauthOwnedFocus = document.activeElement === supportReauth;
    supportReauth.disabled = true;
    supportReauth.textContent = 'Signing out…';
    try {
      const draft = state.supportTarget ? {
        user_id: state.supportTarget.id,
        username: state.supportTarget.username,
        app_slug: supportApp.value,
        reason: supportReason.value,
        saved_at: Date.now(),
      } : null;
      if (draft && !saveSupportDraft(sessionStorage, draft)) {
        supportReauth.disabled = false;
        supportReauth.textContent = 'Sign out and sign in again';
        setError(supportError, 'This browser could not preserve your support-session draft. Copy the reason, then sign out manually.');
        supportReauth.focus();
        return;
      }
      const resp = await api('/api/auth/logout', { method: 'POST' });
      if (!resp.ok && resp.status !== 401) throw new Error(`HTTP ${resp.status}`);
      suppressUnloadGuard = true;
      sessionController.end();
      window.location.assign(logoutTarget(loginProviders, '/identity'));
    } catch {
      consumeSupportDraft(sessionStorage);
      supportReauth.disabled = false;
      supportReauth.textContent = 'Sign out and sign in again';
      setError(supportError, 'Could not sign out. Check your connection and try again.');
      restoreFailedActionFocus(supportReauth, reauthOwnedFocus);
    }
  }

  async function restorePendingSupportSession() {
    const draft = consumeSupportDraft(sessionStorage);
    if (state.user?.role !== 'admin') return;
    if (!isFreshSupportDraft(draft) || !draft.user_id || !draft.username) return;
    await openSupportSessionModal({ id: draft.user_id, username: draft.username }, draft);
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
    await runInFlight(resetPwSubmit, async () => {
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
    });
  }

  const newPerson = createNewPersonController({
    document, api: peopleDirectory.request, origin: window.location.origin,
    activate: () => modalTrap(newUserModal).activate(),
    release: () => modalTrap(newUserModal).release(),
    onCreated: () => peopleDirectory.refreshInvitations(),
    onUnauthorized: handleUnauthorized,
    copy: text => navigator.clipboard.writeText(text),
  });
  function openNewUserModal() { newPerson.open(); }
  function closeNewUserModal() { newPerson.close(); }
  function submitNewUser(event) { return newPerson.submit(event); }

  const tokenManager = createTokenManager({
    document, state, api, onUnauthorized:handleUnauthorized,
    activate:() => modalTrap(newTokenModal).activate(),
    release:() => modalTrap(newTokenModal).release(),
    copy:text => navigator.clipboard.writeText(text),
    search:() => location.search,
  });
  function closeNewTokenModal(force = false) { tokenManager.close(force); }

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
      return result;
    }

    if (result.kind !== 'success') {
      if (cardLocal) {
        setRestartFeedback(slug, 'error', result.message);
        // A failed restart can leave the app stopped, and the error body says
        // nothing about where it landed, so this is the one path that has to
        // ask. Ask about this app only: the rest of the fleet did not move, and
        // rebuilding the grid would take the recovery panel's focus with it.
        if (result.kind === 'error') await refreshApp(slug);
      } else {
        setError(appError, result.message);
      }
      return result;
    }

    // The restart endpoint answers with the app's new row. Merge it and repaint
    // the one card it describes rather than reloading the fleet: nothing else
    // changed, and a rebuild would drop the recovery panel's focus and re-point
    // the metrics poller at every app.
    if (result.app) {
      const app = state.apps.find((item) => item.slug === slug);
      if (app) Object.assign(app, result.app);
    }
    // A restart is not a deploy, so whatever "deploying" the model was carrying
    // is over by the time this responds. A response without a status leaves the
    // model's own status standing rather than inventing "running" for it.
    syncCardFromModel(slug, {status: result.app && result.app.status, deploying: false});
    syncSidebar();
    loadFleetHealth();

    if (cardLocal) {
      // The restart endpoint responds only after deployRun has passed readiness
      // and the proxy has switched, so success here means the app is serving.
      const success = setRestartFeedback(slug, 'success');
      window.setTimeout(() => clearRestartFeedback(slug, success.version), 6000);
    }
    return result;
  }

  // refreshApp re-reads one app and folds it back into the model and its card.
  // It is the narrow counterpart of loadApps for the paths where only one app's
  // state is in doubt. A missing app (404) or a failed request leaves the model
  // as it was: a stale card is a smaller lie than a card silently emptied by a
  // network blip.
  async function refreshApp(slug) {
    let response;
    try {
      response = await api(`/api/apps/${encodeURIComponent(slug)}`);
    } catch {
      return null;
    }
    if (response.status === 401) { await handleUnauthorized(); return null; }
    if (!response.ok) return null;
    let body = null;
    try { body = await response.json(); } catch { return null; }
    const fresh = body && body.app ? body.app : body;
    if (!fresh || !fresh.slug) return null;
    const app = state.apps.find((item) => item.slug === slug);
    if (app) Object.assign(app, fresh);
    const synced = syncCardFromModel(slug);
    syncSidebar();
    return synced;
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
    return await performRestart(slug, btn, false);
  }

  // lifecycleAction is the shared POST path behind Sleep, Stop and Start. It
  // mirrors restart(): clear the error region, disable the control, post, then
  // repaint the one app that changed from the row that came back. The server's
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

    // Sleep, Stop and Start all answer with the app's new row, and none of them
    // runs a deploy, so the new state is known here without asking the server
    // anything further. One app changed, so one card changes: reloading the
    // fleet to learn this would cost three more requests, rebuild every card
    // (dropping focus and any open menu with them), and re-point the metrics
    // poller at the whole fleet even when the visitor is on a single app's page.
    const live = body && body.status ? {status: body.status, deploying: false} : null;

    // The detail header renders once per navigation, so an action taken there
    // has to fold the new status back in or the menu keeps offering Sleep for an
    // app that is already asleep. Repaint the pill from the same merge the
    // metrics poller uses and derive the menu from the merged model, so the two
    // cannot disagree during the window before the next tick - waiting for that
    // tick left the header reading "Sleeping" above a Stop item.
    if (detailApp && detailApp.slug === slug && live) {
      const pillEl = document.getElementById('app-detail-status');
      if (pillEl) {
        updateStatusPill(pillEl, detailApp, live, formatStatus);
      } else {
        detailApp.status = live.status;
      }
      syncDetailHeaderActions(detailApp);
    }
    syncCardFromModel(slug, live);
    // The sidebar carries its own badge for every app and renders from the same
    // model the merge just updated, so it costs nothing to keep it truthful.
    syncSidebar();
    // Fleet health counts running and stopped apps, so it does move on a
    // lifecycle action even though nothing else in the fleet did.
    loadFleetHealth();
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
    logPaneController.open({ titleText: `Logs: ${slug}`, url: `/api/apps/${slug}/logs`, withCredentials: true });
  }

  function closeLogs() {
    logPaneController.close();
  }

  let settingsSlug = null;
  let settingsRevision = 0;

  // A successful settings mutation can create (or clear) a temporary fleet
  // override. Re-read the server-owned convergence envelope and repaint only
  // fleet surfaces so the current form, focus, and unrelated unsaved edits stay
  // intact. Failure is deliberately non-fatal: the save already succeeded and
  // the next route mount will recover the display.
  async function refreshDetailFleetState(slug, isCurrent = () => true) {
    if (!slug || !detailApp || detailApp.slug !== slug) return;
    const originalApp = detailApp;
    const stillCurrent = () => isCurrent() && detailApp?.slug === slug && (originalApp.id != null && detailApp.id != null
      ? originalApp.id === detailApp.id : originalApp === detailApp);
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}`);
    } catch {
      if (stillCurrent()) { markConfigurationBehaviorStale(document); }
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) { if (stillCurrent()) { markConfigurationBehaviorStale(document); } return; }
    let body;
    try { body = await resp.json(); } catch { if (stillCurrent()) { markConfigurationBehaviorStale(document); } return; }
    // The user may have navigated while the refresh was in flight.
    if (!stillCurrent()) return;
    if (body?.app?.id != null && originalApp.id != null && body.app.id !== originalApp.id) {
      markConfigurationBehaviorStale(document);
      return;
    }
    if (body && body.app) Object.assign(detailApp, body.app);
    detailLastEnvelope = body || {};
    refreshFleetSurfaces(document, detailApp, body && body.fleet_state);
    if (body && body.app) applyConfigurationContext(document, detailApp, body);
    else { markConfigurationBehaviorStale(document); }
    updateWorkerCapacity();
  }

  // --- Settings dirty-state tracking (explicit-save model) ---
  // Every settings section registers the inputs it owns. populate*() snapshots a
  // baseline; when live values diverge the section is "dirty": its Save button
  // enables and an "Unsaved changes" hint shows. Saving (or a confirmed discard)
  // re-snapshots to clean. A nav/unload guard warns before losing unsaved edits,
  // so all settings tabs behave identically.
  const settingsSections = {};
  let originalScalingSettings = {};
  let originalResourceSettings = {};
  let resourceApplyPending = false;
  function registerSettingsSection(name, getEls, saveBtnId, dirtyId) {
    const els = getEls();
    const rec = {
      els,
      btn: document.getElementById(saveBtnId),
      dirtyEl: dirtyId ? document.getElementById(dirtyId) : null,
      baseline: null,
    };
    settingsSections[name] = rec;
    const section = rec.btn?.closest('#detail-configuration-panel .settings-block, #detail-access-panel .settings-block');
    if (section) {
      rec.discard = document.createElement('button');
      rec.discard.type = 'button'; rec.discard.className = 'env-btn-secondary';
      rec.discard.textContent = 'Discard changes'; rec.discard.disabled = true;
      rec.btn.after(rec.discard);
      rec.discard.addEventListener('click', () => {
        rec.els.forEach((el, index) => {
          const saved = rec.originals[index];
          el.value = saved.value; el.checked = saved.checked; el.disabled = saved.disabled;
          delete el.dataset.modeDraft;
          el.removeAttribute('aria-invalid');
          el.removeAttribute('aria-errormessage');
        });
        for (const id of Object.keys(directSettings)) refreshDirectSetting(document, id, { resetDraft: rec.els.includes(document.getElementById(id)) });
        document.getElementById('general-description-count').textContent = `${[...document.getElementById('general-description').value].length} / 280 characters`;
        syncAutoscaleControls(); updateAutoscaleCeiling(); updateScalingCeiling(); updateWorkerCapacity(); updateRenderPacingAdvice(); updateUsagePrivacyHelp(); refreshKeepWarmNote();
        setError(document.getElementById(`${name}-error`), '');
        setHidden(document.getElementById(`${name}-status`), true);
        recomputeDirty(name);
        focusSettingsFeedback(rec);
      });
      rec.sectionId = section.id;
    }

    const onChange = event => {
      event.target.removeAttribute('aria-invalid');
      event.target.removeAttribute('aria-errormessage');
      recomputeDirty(name);
    };
    els.forEach(el => {
      el.addEventListener('input', onChange);
      el.addEventListener('change', onChange);
    });
  }
  function focusSettingsFeedback(rec) {
    const section = rec.btn.closest('.settings-block');
    const invalid = section.querySelector('[aria-invalid="true"]');
    const error = section.querySelector('.error:not([hidden])');
    if (invalid && error?.id) invalid.setAttribute('aria-errormessage', error.id);
    if (document.activeElement !== document.body && !section.contains(document.activeElement)) return;
    if (invalid && !invalid.disabled) {
      invalid.closest('details')?.setAttribute('open', '');
      invalid.focus();
      return;
    }
    const target = section.querySelector('.error:not([hidden])') || section.querySelector('h2, legend');
    target?.setAttribute('tabindex', '-1'); target?.focus({ preventScroll: true });
  }
  async function saveSettingsSection(name, action) {
    const rec = settingsSections[name];
    if (!rec || rec.saving) return;
    const section = rec.btn.closest('.settings-block');
    const controls = [...section.querySelectorAll('input, select, textarea, button')].filter(el => !el.closest('.settings-immediate'));
    const slug = settingsSlug, revision = settingsRevision;
    rec.lockedStates = new Map(controls.map(el => [el, el.disabled]));
    section.setAttribute('aria-busy', 'true');
    rec.saving = true;
    rec.els.forEach(el => { el.removeAttribute('aria-invalid'); el.removeAttribute('aria-errormessage'); });
    controls.forEach(el => { el.disabled = true; });
    try { await action(); }
    finally {
      if (settingsSlug === slug && settingsRevision === revision) {
        section.removeAttribute('aria-busy');
        for (const [el, disabled] of rec.lockedStates) el.disabled = disabled;
        rec.lockedStates = null; rec.saving = false;
        recomputeDirty(name);
        focusSettingsFeedback(rec);
      } else { section.removeAttribute('aria-busy'); rec.lockedStates = null; rec.saving = false; }
    }
  }

  function sectionSnapshot(rec) {
    // Track values independently of temporary save locks and mode relevance.
    // Policy values are explicit fields; Discard restores the whole draft.
    return JSON.stringify(rec.els.map(el => {
      // Inherited/off numbers are display values, not app configuration. A
      // refreshed server default must not create an unsaved app override.
      if (directSettings[el.id] && document.getElementById(`${el.id}-mode`).value !== 'custom') return null;
      return (el.type === 'checkbox' || el.type === 'radio') ? el.checked : el.value;
    }));
  }
  function snapshotSettingsSection(name) {
    const rec = settingsSections[name];
    if (!rec) return;
    rec.baseline = sectionSnapshot(rec);
    rec.originals = rec.els.map(el => ({ value: el.value, checked: el.checked, disabled: rec.lockedStates?.get(el) ?? el.disabled }));
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
    if (rec.btn && !rec.btn.hidden) rec.btn.disabled = !!rec.saving || !dirty;
    if (rec.dirtyEl) setHidden(rec.dirtyEl, !dirty);
    if (rec.discard) { rec.discard.disabled = !!rec.saving || !dirty; rec.discard.hidden = rec.btn.hidden; }
    if (rec.sectionId) {
      updateConfigurationDirty(document, rec.sectionId, dirty);
    }
    if (name === 'resources') {
      document.getElementById('resources-retry-btn').hidden = !resourceApplyPending || dirty || rec.btn.hidden;
      document.getElementById('resources-apply-note').hidden = !resourceApplyPending;
    }
    const status = document.getElementById(`${name}-status`);
    if (dirty && status) setHidden(status, true);
  }
  function anySettingsDirty() {
    return envFormDirty() || Object.keys(settingsSections).some(isSectionDirty);
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
  function confirmDiscardIfDirty(destination) {
    if (profileModal && !profileModal.hidden && !closeProfileModal()) return false;
    if (!tokenManager.allowLeave()) return false;
    if (envSaving || Object.values(settingsSections).some(rec => rec.saving)) { flashToast('Wait for the setting to finish saving.', 'info'); return false; }
    if (destination && isSameAppSettingsDestination(destination, settingsSlug)) return true;
    if (announcementEditor && !announcementEditor.allowLeave()) return false;
    if (!anySettingsDirty()) return true;
    const ok = window.confirm('You have unsaved changes in this app’s settings. Leave without saving?');
    if (ok) { closeEnvForm(true); clearSettingsDirty(); }
    return ok;
  }

  function updateAccessViewingNote(access) {
    const note = document.getElementById('access-viewing-note');
    const messages = {
      public: 'Currently public: anyone can open this app without signing in. Removing members or groups does not restrict viewing.',
      shared: 'Currently shared: all signed-in users can open this app. Removing members or groups does not restrict viewing.',
      private: 'Removing a grant may leave access through other memberships, groups or platform roles.',
    };
    note.textContent = messages[access] || '';
  }

  function populateAccessPanel(app) {
    const canManage = canManageApp(state.user, app);
    updateAccessViewingNote(app.access);
    const radios = document.querySelectorAll('input[name="access-level"]');
    radios.forEach(r => {
      r.checked = r.value === app.access;
      r.disabled = !canManage;
    });
    document.getElementById('visibility-save-btn').hidden = !canManage;
    setError(document.getElementById('visibility-error'), '');
    setHidden(document.getElementById('visibility-status'), true);
    snapshotSettingsSection('visibility');

    // A newly mounted app invalidates pending access requests.
    accessPanelRevision++;
    memberListRequest++;
    groupListRequest++;
    // Clear previous state.
    document.getElementById('members-list').innerHTML = '';
    document.getElementById('grant-username').value = '';
    document.getElementById('grant-error').hidden = true;
    document.getElementById('group-access-list').replaceChildren();
    document.getElementById('group-access-name').value = '';
    document.getElementById('group-access-role').value = 'viewer';
    document.getElementById('group-access-error').hidden = true;
    document.getElementById('member-add').open = false;
    document.getElementById('group-add').open = false;
    for (const id of ['members-refresh', 'groups-refresh']) document.getElementById(id).disabled = false;
    for (const [id, text] of [['grant-btn', 'Add member'], ['group-access-add-btn', 'Add group']]) {
      const button = document.getElementById(id);
      button.disabled = false; button.textContent = text;
    }
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
    const slug = settingsSlug, revision = accessPanelRevision;
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
      if (accessRequestCurrent(slug, revision)) {
        radios.forEach(r => { r.disabled = false; });
        setError(errEl, 'Failed to save. Check your connection.');
        recomputeDirty('visibility');
      }
      return;
    }
    if (resp.status === 401) { if (accessRequestCurrent(slug, revision)) await handleUnauthorized(); return; }
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
    if (!accessRequestCurrent(slug, revision)) return;
    radios.forEach(r => { r.disabled = false; });
    if (!ok) {
      let message = 'Failed to save.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      if (!accessRequestCurrent(slug, revision)) return;
      setError(errEl, message);
      recomputeDirty('visibility');
      return;
    }
    updateAccessViewingNote(value);
    statusEl.textContent = 'Visibility saved.';
    setHidden(statusEl, false);
    snapshotSettingsSection('visibility');
    await refreshDetailFleetState(slug);
    if (accessRequestCurrent(slug, revision)) flashToast('Visibility updated', 'success');
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

  function setStableText(el, value) { if (el.textContent !== value) el.textContent = value; }

  function updateRenderPacingAdvice() {
    const el = document.getElementById('render-pacing-advice');
    if (!el) return;
    const raw = document.getElementById('render-seconds-mode').value === 'off' ? '0' : document.getElementById('render-seconds').value.trim();
    // A blank field has no advice to give: saving it is a validation error, not
    // a state the operator can be told the consequences of.
    const custom = document.getElementById('render-seconds-mode').value === 'custom';
    const invalid = raw === '' || !Number.isFinite(Number(raw)) || Number(raw) > 600 || (custom && Number(raw) <= 0);
    setStableText(el, invalid ? '' : renderPacingAdvice(Number(raw), renderPacingBlock));
  }

  const usageModeRank = (mode) => ({ unattributed: 0, pseudonymous: 1, identified: 2 }[mode] ?? 0);

  function effectiveUsageMode(hubMode, override) {
    if (override === 'disabled') return { mode: 'unattributed', collecting: false };
    if (!override || override === 'inherit') return { mode: hubMode, collecting: true };
    if (!hubMode) return { mode: override, collecting: true };
    return usageModeRank(override) < usageModeRank(hubMode)
      ? { mode: override, collecting: true }
      : { mode: hubMode, collecting: true };
  }

  function usagePrivacyWarning(hubMode, originalMode, selectedMode) {
    const original = effectiveUsageMode(hubMode, originalMode);
    const selected = effectiveUsageMode(hubMode, selectedMode);
    // Pausing totals-only collection removes no identity, so is reversible.
    const lowering = selectedMode !== originalMode && original.collecting && ((!hubMode) ||
      (usageModeRank(original.mode) > (selected.collecting ? usageModeRank(selected.mode) : 0)));
    if (!lowering) return '';
    if (!hubMode) return 'Saving may permanently remove viewer identities from existing sessions. The hub policy is unavailable, so the full effect cannot be shown. This cannot be undone.';
    return `Saving permanently removes ${selected.mode === 'pseudonymous' && selected.collecting ? 'viewer names' : 'viewer names and unique-viewer keys'} from existing sessions. Session totals remain. This cannot be undone.`;
  }

  function updateUsagePrivacyHelp() {
    const select = document.getElementById('usage-privacy-mode');
    const help = document.getElementById('usage-privacy-help');
    const warning = document.getElementById('usage-privacy-warning');
    if (!select || !help || !warning) return;
    const hubMode = select.dataset.hubMode || '';
    const labels = { unattributed: 'Session totals only', pseudonymous: 'Unique viewer counts', identified: 'Named viewers' };
    const selected = effectiveUsageMode(hubMode, select.value);
    const originalMode = select.dataset.originalMode || 'inherit';
    const changed = select.value !== originalMode;
    const original = effectiveUsageMode(hubMode, originalMode);
    select.querySelector('option[value="inherit"]').textContent = `Use hub policy · ${labels[hubMode] || 'Unavailable'}`;
    const descriptions = {
      unattributed: 'Keeps session totals and audience type without an account reference or a stable viewer key.',
      pseudonymous: 'Counts unique viewers using a key specific to this app. Names are not retained.',
      identified: 'Retains signed-in accounts. Only platform administrators can see viewer names and individual sessions.',
    };
    let description = !selected.collecting
      ? 'Stops new collection for this app. Previously collected totals remain until their retention period ends.'
      : descriptions[selected.mode] || 'The hub policy is unavailable, so the effective privacy level cannot be shown.';
    if (changed && selected.collecting && (!original.collecting || usageModeRank(selected.mode) > usageModeRank(original.mode))) {
      description += ' Applies to new sessions; past viewer identities cannot be restored.';
    }
    help.textContent = (changed ? 'After saving: ' : '') + description;
    const limit = document.getElementById('usage-privacy-limit');
    if (limit) limit.textContent = select.dataset.collectionEnabled === 'false'
      ? 'Collection is turned off for the hub. This choice applies if collection resumes.'
      : hubMode ? `Hub limit: ${labels[hubMode]}.${hubMode === 'identified' ? '' : ' More identifying options are unavailable.'}`
        : 'The hub limit is unavailable. Reload before changing the privacy level.';

    warning.textContent = usagePrivacyWarning(hubMode, originalMode, select.value);
    setHidden(warning, !warning.textContent);
  }

  function populateGeneralTab(app, envelope) {
    applyConfigurationContext(document, app, envelope);
    const envNote = document.getElementById('env-apply-note');
    if (envNote.dataset.slug !== app.slug) { envNote.textContent = ''; envNote.dataset.slug = app.slug; }
    const canEdit = canManageApp(state.user, app);
    document.querySelectorAll('#detail-configuration-panel [aria-invalid]').forEach(el => el.removeAttribute('aria-invalid'));
    populateDirectSetting(document, 'hibernate-custom-minutes', app.hibernate_timeout_minutes, canEdit);
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
    populateDirectSetting(document, 'scaling-cap', app.max_sessions_per_replica ?? 0, canEdit);
    document.getElementById('scaling-save-btn').hidden = !canEdit;
    setError(document.getElementById('scaling-error'), '');
    setHidden(document.getElementById('scaling-status'), true);
    updateScalingCeiling();

    // Worker isolation controls.
    for (const id of ['worker-grouped-size', 'worker-max-workers', 'worker-warm-spares', 'scaling-replicas', 'scaling-cap', 'scaling-cap-mode']) delete document.getElementById(id).dataset.modeDraft;
    const isolationSelect = document.getElementById('worker-isolation');
    const groupedSizeInput = document.getElementById('worker-grouped-size');
    const maxWorkersInput = document.getElementById('worker-max-workers');
    const warmSparesInput = document.getElementById('worker-warm-spares');
    isolationSelect.value = app.worker_isolation || '';
    isolationSelect.dataset.original = isolationSelect.value;
    isolationSelect.dataset.appStatus = String(app.status ?? '');
    groupedSizeInput.value = String(app.worker_grouped_size ?? 1);
    maxWorkersInput.value = String(app.worker_max_workers ?? 0);
    warmSparesInput.value = String(app.worker_warm_spares ?? 0);
    isolationSelect.disabled = !canEdit;
    updateScalingCeiling();
    document.getElementById('worker-settings').open = currentIsolationChoice() !== 'multiplex';
    groupedSizeInput.disabled = !canEdit;
    maxWorkersInput.disabled = !canEdit;
    warmSparesInput.disabled = !canEdit;

    // Render pacing: one number, plus the server's advisory for what it implies
    // on this host. The block is absent when the SAVED value is 0 (pacing off),
    // which is exactly the state the advice line then reports.
    const renderInput = document.getElementById('render-seconds');
    renderPacingBlock = (envelope && envelope.render_pacing) || (app.render_seconds > 0 ? { render_seconds: app.render_seconds } : null);
    renderInput.value = String(app.render_seconds ?? 0);
    renderInput.disabled = !canEdit;
    populateDirectSetting(document, 'render-seconds', app.render_seconds ?? 0, canEdit);
    document.getElementById('render-save-btn').hidden = !canEdit;
    setError(document.getElementById('render-error'), '');
    setHidden(document.getElementById('render-status'), true);
    updateRenderPacingAdvice();

    // General info: display name + description + project slug (rename / regroup).
    document.getElementById('general-name').value = app.name ?? '';
    document.getElementById('general-description').value = app.description ?? '';
    document.getElementById('general-description-count').textContent = `${[...(app.description || '')].length} / 280 characters`;
    document.getElementById('general-project').value = app.project_slug ?? '';
    document.getElementById('general-name').disabled = !canEdit;
    document.getElementById('general-description').disabled = !canEdit;
    document.getElementById('general-project').disabled = !canEdit;
    document.getElementById('general-save-btn').hidden = !canEdit;
    setError(document.getElementById('general-error'), '');
    setHidden(document.getElementById('general-status'), true);

    // Usage privacy: per-app settings may only lower the hub-wide identity
    // ceiling. Options above that ceiling remain visible but unavailable so the
    // operator understands why a more identifying mode cannot be selected.
    const usageSelect = document.getElementById('usage-privacy-mode');
    const usagePolicy = (envelope && envelope.usage_policy) || {};
    const hubUsageMode = usagePolicy.hub_identity_mode || '';
    const appUsageMode = app.usage_identity_mode || 'inherit';
    usageSelect.dataset.hubMode = hubUsageMode;
    usageSelect.dataset.originalMode = appUsageMode;
    usageSelect.dataset.collectionEnabled = String(usagePolicy.collection_enabled !== false);
    const retention = document.getElementById('usage-privacy-retention');
    const period = days => days === 0 ? 'without a time limit' : `for ${days} ${days === 1 ? 'day' : 'days'}`;
    const raw = usagePolicy.raw_retention_days, aggregate = usagePolicy.aggregate_retention_days;
    const retentionKnown = Number.isInteger(raw) && raw >= 0 && Number.isInteger(aggregate) && aggregate >= 0;
    retention.textContent = retentionKnown ? `The hub keeps individual session records ${period(raw)} and daily totals ${period(aggregate)}.` : '';
    retention.hidden = !retentionKnown;
    usageSelect.value = appUsageMode;
    [...usageSelect.options].forEach(option => {
      option.disabled = ['pseudonymous', 'identified'].includes(option.value) &&
        usageModeRank(option.value) > usageModeRank(hubUsageMode);
    });
    usageSelect.disabled = !canEdit;
    document.getElementById('usage-privacy-save-btn').hidden = !canEdit;
    setError(document.getElementById('usage-privacy-error'), '');
    setHidden(document.getElementById('usage-privacy-status'), true);
    updateUsagePrivacyHelp();

    renderIconPicker(app, canEdit);

    // Resources: null inherits the server default; zero explicitly removes the cap.
    // Limits are enforced in BOTH native and docker mode, so the controls are
    // editable in both. Native enforcement is best-effort (needs cgroup v2
    // delegation), so resource_enforcement drives a warning when it is absent.
    const memInput = document.getElementById('resources-memory');
    const cpuInput = document.getElementById('resources-cpu');
    memInput.value = app.memory_limit_mb != null ? String(app.memory_limit_mb) : '';
    cpuInput.value = app.cpu_quota_percent != null ? String(app.cpu_quota_percent) : '';
    memInput.disabled = !canEdit;
    cpuInput.disabled = !canEdit;
    populateDirectSetting(document, 'resources-memory', app.memory_limit_mb, canEdit);
    populateDirectSetting(document, 'resources-cpu', app.cpu_quota_percent, canEdit);
    document.getElementById('resources-save-btn').hidden = !canEdit;
    // Stash originals + status so saveResources confirms the restart only when a
    // value actually changes on a running app (mirrors the scaling guard).
    memInput.dataset.original = memInput.value;
    cpuInput.dataset.original = cpuInput.value;
    originalResourceSettings = {
      memory_limit_mb: app.memory_limit_mb ?? null,
      cpu_quota_percent: app.cpu_quota_percent ?? null,
    };
    resourceApplyPending = app.status === 'degraded' && (app.last_error || '').startsWith('apply resource limits:');
    memInput.dataset.appStatus = app.status || '';
    const enf = app.resource_enforcement;
    const nativeMode = app.runtime_mode !== 'docker';
    const note = document.getElementById('resources-runtime-note');
    let warn = '';
    if (nativeMode && enf) {
      if (!enf.memory && !enf.cpu) {
        warn = 'Memory and CPU limits cannot be enforced on this host. Ask a server administrator to enable memory and CPU resource controls.';
      } else if (!enf.cpu) {
        warn = 'CPU limits cannot be enforced on this host. Ask a server administrator to enable CPU resource controls. Memory limits are enforced.';
      } else if (!enf.memory) {
        warn = 'Memory limits cannot be enforced on this host. Ask a server administrator to enable memory resource controls.';
      }
    }
    note.textContent = warn;
    setHidden(note, warn === '');
    setError(document.getElementById('resources-error'), '');
    setHidden(document.getElementById('resources-status'), true);
    updateWorkerCapacity();

    // Danger zone (lifecycle) now lives at the bottom of Configuration, not
    // Access. Visible to managers; the typed-confirm gate stays the same.
    resetDangerZone();
    const dangerZone = document.getElementById('danger-zone');
    dangerZone.hidden = !canEdit;
    document.getElementById('delete-confirm-slug').textContent = app.slug;

    // Snapshot baselines so the explicit-save dirty tracker starts clean.
    snapshotSettingsSection('general');
    snapshotSettingsSection('usage-privacy');
    snapshotSettingsSection('resources');
    snapshotSettingsSection('hibernate');
    originalScalingSettings = scalingSettingsSnapshot(document);
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
    setError(document.getElementById('general-icon-error'), '');
    preview.replaceChildren(renderAppAvatar(document, avatarView(app), 'icon-picker-preview'));
    uploadBtn.hidden = !canEdit;
    removeBtn.hidden = !canEdit;
    removeBtn.disabled = !app.icon_mime && !app.icon_emoji;
    // The emoji trigger and its Clear-emoji control are wired once (see the
    // top-level setup block below); only their visibility changes per render.
    if (emojiBtn) emojiBtn.hidden = !canEdit;
    if (emojiClearBtn) emojiClearBtn.hidden = !canEdit || !app.icon_emoji;
    syncIconSaveState(app, canEdit);

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
      if (f) withIconSave(() => uploadIcon(app, f));
    };
    removeBtn.onclick = () => withIconSave(() => removeIcon(app));
    const undoBtn = document.getElementById('general-icon-undo');
    if (undoBtn) undoBtn.onclick = () => withIconSave(() => undoIconChange(app), 'Restoring icon…');
  }

  // Keep one exact previous state per app. Image bytes are retained only in
  // memory, and the server checks the revision before restoring them.
  const iconUndo = new Map();
  let iconSaving = false;
  let iconSaveSlug = '';
  let iconSaveApp = null;
  function currentIconTarget(app, appID = app?.id) {
    return !!app && settingsSlug === app.slug && detailApp?.slug === app.slug
      && (appID != null && detailApp.id != null ? detailApp.id === appID : detailApp === app);
  }
  function syncIconSaveState(app, canEdit) {
    const group = document.getElementById('general-icon-preview')?.closest('.icon-picker');
    const pending = iconSaving && iconSaveSlug === app.slug && currentIconTarget(iconSaveApp);
    if (pending) group?.setAttribute('aria-busy', 'true');
    else group?.removeAttribute('aria-busy');
    for (const control of group?.querySelectorAll('button, input') || []) control.disabled = pending || !canEdit;
    const undo = document.getElementById('general-icon-undo');
    const previous = iconUndo.get(app.slug);
    if (undo) {
      undo.hidden = !canEdit || !previous || (app.id != null && previous.before.app_id !== app.id);
      undo.disabled = pending || !canEdit || undo.hidden;
    }
    const remove = document.getElementById('general-icon-remove');
    if (remove) remove.disabled = pending || !canEdit || (!app.icon_mime && !app.icon_emoji);
  }
  async function withIconSave(action, pendingText = 'Saving icon…') {
    if (iconSaving) { flashToast('An icon change is still saving. Try again when it finishes.', 'info'); return; }
    iconSaving = true;
    const slug = settingsSlug;
    iconSaveSlug = slug;
    const targetApp = detailApp;
    iconSaveApp = targetApp;
    const focused = document.activeElement;
    const status = document.getElementById('general-icon-status');
    try {
      const request = action();
      if (document.getElementById('general-icon-error').hidden) {
        status.textContent = pendingText;
        setHidden(status, false);
      }
      if (detailApp?.slug === slug) syncIconSaveState(detailApp, canManageApp(state.user, detailApp));
      await request;
    } finally {
      iconSaving = false;
      iconSaveSlug = '';
      iconSaveApp = null;
      if (currentIconTarget(targetApp)) {
        if (status.textContent === pendingText) { status.textContent = ''; setHidden(status, true); }
        if (detailApp?.slug === slug) syncIconSaveState(detailApp, canManageApp(state.user, detailApp));
        const restore = focused?.closest('.emoji-picker-popover[hidden]') || (focused?.closest('.icon-picker') && (focused.hidden || focused.disabled))
          ? document.getElementById('general-icon-emoji-btn') : focused;
        if (restore?.isConnected && document.activeElement === document.body && !restore.disabled && !restore.hidden) restore.focus({ preventScroll: true });
      }
    }
  }

  function clearIconFeedback() {
    setError(document.getElementById('general-icon-error'), '');
    setHidden(document.getElementById('general-icon-status'), true);
  }

  async function writeIconState(app, transform, { undo = false, message = 'Icon updated.' } = {}) {
    const err = document.getElementById('general-icon-error');
    const path = `/api/apps/${encodeURIComponent(app.slug)}/icon/state`;
    let before, target;
    try {
      if (undo) {
        const previous = iconUndo.get(app.slug);
        if (!previous) return;
        before = previous.before;
        target = { ...before, revision: previous.revision };
      } else {
        const snapshot = await api(path, { cache: 'no-store' });
        if (snapshot.status === 401) { await handleUnauthorized(); return; }
        if (!snapshot.ok) throw new Error('snapshot');
        before = await snapshot.json();
        if (!before.app_id || typeof before.revision !== 'string' || !before.revision) throw new Error('snapshot');
        if (!currentIconTarget(app)) return;
        if (app.id != null && app.id !== before.app_id) throw new Error('snapshot');
        target = { ...before, ...await transform(before) };
      }
    } catch {
      if (currentIconTarget(app)) setError(err, 'Could not prepare icon change. Check your connection and try again.');
      return;
    }
    if (!currentIconTarget(app, before.app_id)) return;
    let response;
    try {
      response = await api(path, { method: 'PUT', body: JSON.stringify(target) });
    } catch {
      if (currentIconTarget(app, before?.app_id)) setError(err, 'Could not save icon. Check your connection and try again.');
      return;
    }
    if (response.status === 401) { await handleUnauthorized(); return; }
    if (!response.ok) {
      // A stale Undo can never safely replace a newer icon; do not offer it again.
      if (response.status === 409) iconUndo.delete(app.slug);
      if (!currentIconTarget(app, before?.app_id)) return;
      let message = response.status === 409 ? 'Icon changed elsewhere. Reload to see the latest icon before trying again.' : 'Could not save icon. Try again.';
      try { const body = await response.json(); if (body.error) message = body.error; } catch { /* non-JSON */ }
      if (currentIconTarget(app, before.app_id)) setError(err, message);
      return;
    }
    let body;
    try {
      body = await response.json();
      if (body.app_id !== before.app_id || !body.revision) throw new Error('invalid state');
    } catch {
      iconUndo.delete(app.slug);
      if (currentIconTarget(app, before?.app_id)) setError(err, 'Icon saved, but its confirmation could not be read. Reload to check the result.');
      return;
    }
    if (undo) iconUndo.delete(app.slug);
    else iconUndo.set(app.slug, { before, revision: body.revision });
    if (!currentIconTarget(app, before?.app_id)) return;
    applyIconChange(app, { mime: body.icon_mime || '', emoji: body.icon_emoji || '' });
    const status = document.getElementById('general-icon-status');
    status.textContent = message;
    setHidden(status, false);
    await refreshDetailFleetState(app.slug);
    return currentIconTarget(app, before.app_id);
  }

  async function uploadIcon(app, file) {
    clearIconFeedback();
    const err = document.getElementById('general-icon-error');
    if (file.size > ICON_MAX_BYTES) { setError(err, 'Icon must be 512 KB or smaller.'); return; }
    if (file.type && !ICON_TYPES.includes(file.type)) { setError(err, 'Icon must be a PNG, JPEG, WebP, or SVG image.'); return; }
    await writeIconState(app, async () => {
      const bytes = new Uint8Array(await file.arrayBuffer());
      let binary = '';
      for (let i = 0; i < bytes.length; i += 8192) binary += String.fromCharCode(...bytes.subarray(i, i + 8192));
      return { mime: file.type || 'application/octet-stream', data: btoa(binary), emoji: '' };
    });
  }

  async function removeIcon(app) {
    clearIconFeedback();
    await writeIconState(app, () => ({ mime: '', data: null, emoji: '' }), { message: 'Icon removed.' });
  }

  async function setEmojiIcon(app, rawValue) {
    clearIconFeedback();
    const value = (rawValue || '').trim();
    if (!isSingleEmoji(value)) { setError(document.getElementById('general-icon-error'), 'Enter a single emoji.'); return; }
    const changed = await writeIconState(app, () => ({ mime: '', data: null, emoji: value }));
    if (changed && currentIconTarget(app) && document.getElementById('general-icon-error').hidden) document.getElementById('general-icon-emoji-input').value = '';
  }

  async function clearEmojiIcon(app) {
    clearIconFeedback();
    await writeIconState(app, () => ({ emoji: '' }));
  }

  async function undoIconChange(app) {
    clearIconFeedback();
    await writeIconState(app, null, { undo: true, message: 'Previous icon restored.' });
  }

  // applyIconChange records the new icon state on the app and every cached copy
  // (detailApp, the sidebar/grid list) and bumps updated_at as the cache-buster,
  // then re-renders the picker preview and the detail-header avatar so the change
  // shows everywhere without a reload. Both mime and emoji are written on every
  // call, even the one that did not change, so the cached copy must mirror
  // exactly what the server just did, or a stale emoji (which now renders ahead
  // of the image) keeps winning until a reload.
  function applyIconChange(app, { mime, emoji }) {
    const stamp = new Date().toISOString();
    app.icon_mime = mime;
    app.icon_emoji = emoji;
    app.updated_at = stamp;
    if (detailApp && detailApp.slug === app.slug && (app.id == null || detailApp.id == null || detailApp.id === app.id) && detailApp !== app) {
      detailApp.icon_mime = mime;
      detailApp.icon_emoji = emoji;
      detailApp.updated_at = stamp;
    }
    const listed = (state.apps || []).find((a) => a && a.slug === app.slug && (app.id == null || a.id == null || a.id === app.id));
    if (listed) { listed.icon_mime = mime; listed.icon_emoji = emoji; listed.updated_at = stamp; }
    if (settingsSlug === app.slug && (!detailApp || app.id == null || detailApp.id == null || detailApp.id === app.id)) {
      renderIconPicker(app, canManageApp(state.user, detailApp || app));
      renderDetailHeaderAvatar(app);
    }
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
      document.getElementById('general-name').setAttribute('aria-invalid', 'true');
      setError(errEl, 'Display name must be 1 to 128 characters.');
      return;
    }
    // Count code points (spread iterates by code point), not UTF-16 units, so the
    // limit matches the server's rune count and an emoji-bearing description that
    // the backend accepts is not rejected here.
    if ([...description].length > 280) {
      document.getElementById('general-description').setAttribute('aria-invalid', 'true');
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
      if (settingsSlug !== slug) return;
      setError(errEl, 'Failed to save. Check your connection.');
      recomputeDirty('general');
      return;
    }
    if (settingsSlug !== slug) return;
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let message = 'Failed to save.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      recomputeDirty('general');
      return;
    }
    if (settingsSlug !== slug) return;
    statusEl.textContent = 'Saved.';
    setHidden(statusEl, false);
    document.getElementById('general-name').value = name;
    document.getElementById('general-description').value = description;
    document.getElementById('general-project').value = project;
    document.getElementById('general-description-count').textContent = `${[...description].length} / 280 characters`;
    const descriptionEl = document.getElementById('app-detail-description');
    if (descriptionEl) { descriptionEl.textContent = description; descriptionEl.hidden = !description; }
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

  async function saveUsagePrivacy() {
    if (!settingsSlug) return;
    const slug = settingsSlug, revision = settingsRevision;
    const current = () => settingsSlug === slug && settingsRevision === revision;
    const select = document.getElementById('usage-privacy-mode');
    const errEl = document.getElementById('usage-privacy-error');
    const statusEl = document.getElementById('usage-privacy-status');
    setError(errEl, '');
    const progress = message => { statusEl.textContent = message; setHidden(statusEl, false); };
    const fail = message => { setHidden(statusEl, true); setError(errEl, message); };
    const override = select.value === 'inherit' ? null : select.value;
    // Identity removal is irreversible: confirm against the latest saved policy,
    // rather than a potentially stale form mounted before another manager's edit.
    progress('Checking privacy…');
    let latest;
    try {
      const check = await api(`/api/apps/${encodeURIComponent(slug)}`, { method: 'GET', cache: 'no-store' });
      if (!current()) return;
      if (check.status === 401) { await handleUnauthorized(); return; }
      if (!check.ok) throw new Error('Policy unavailable');
      latest = await check.json();
    } catch {
      if (current()) fail('The current privacy policy could not be checked. Try again before saving.');
      return;
    }
    if (!current()) return;
    const modes = ['unattributed', 'pseudonymous', 'identified'];
    const hubMode = latest?.usage_policy?.hub_identity_mode;
    const latestMode = latest?.app?.usage_identity_mode || 'inherit';
    if (latest?.app?.slug !== slug || (latest.app.id != null && detailApp?.id != null && latest.app.id !== detailApp.id) ||
        !modes.includes(hubMode) || !['inherit', 'disabled', ...modes].includes(latestMode)) {
      fail('The current privacy policy could not be confirmed. Reload before saving.'); return;
    }
    if (override !== null && override !== 'disabled' && (!modes.includes(override) || usageModeRank(override) > usageModeRank(hubMode))) {
      fail('This choice exceeds the current hub limit. Reload to see the available options.'); return;
    }
    const warning = usagePrivacyWarning(hubMode, latestMode, select.value);
    if (warning && !window.confirm(`${warning}\n\nSave this privacy change?`)) { setHidden(statusEl, true); return; }
    progress('Saving privacy…');
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}`, {
        method: 'PATCH', body: JSON.stringify({ usage_identity_mode: override }),
      });
    } catch {
      if (!current()) return;
      setHidden(statusEl, true);
      setError(errEl, 'Privacy could not be saved. Check your connection and try again.');
      return;
    }
    if (!current()) return;
    if (resp.status === 401) { await handleUnauthorized(); return; }
    let body;
    try { body = await resp.json(); } catch { /* An unreadable response cannot confirm the saved policy. */ }
    if (!current()) return;
    setHidden(statusEl, true);
    if (!resp.ok) {
      const message = body?.error || 'Privacy could not be saved. Try again.';
      setError(errEl, usagePrivacyErrorMessage(message));
      return;
    }
    const saved = body?.app?.usage_identity_mode ?? null;
    if (body?.app?.slug !== slug || (body.app.id != null && detailApp?.id != null && body.app.id !== detailApp.id) ||
        (saved !== null && !['disabled', 'unattributed', 'pseudonymous', 'identified'].includes(saved))) {
      setError(errEl, 'The saved privacy policy could not be confirmed. Reload before making another change.');
      return;
    }
    if (detailApp) detailApp.usage_identity_mode = saved;
    select.dataset.hubMode = hubMode;
    select.dataset.collectionEnabled = String(latest.usage_policy.collection_enabled !== false);
    [...select.options].forEach(option => {
      option.disabled = ['pseudonymous', 'identified'].includes(option.value) && usageModeRank(option.value) > usageModeRank(hubMode);
    });
    select.value = saved || 'inherit';
    select.dataset.originalMode = saved || 'inherit';
    updateUsagePrivacyHelp();
    snapshotSettingsSection('usage-privacy');
    statusEl.textContent = 'Privacy saved.';
    setHidden(statusEl, false);
    await refreshDetailFleetState(slug, current);
  }

  async function saveResources(retry = false) {
    if (!settingsSlug) return;
    const slug = settingsSlug, revision = settingsRevision;
    const current = () => settingsSlug === slug && settingsRevision === revision;
    const errEl = document.getElementById('resources-error');
    const statusEl = document.getElementById('resources-status');
    const progress = message => { statusEl.textContent = message; setHidden(statusEl, false); };
    setError(errEl, '');
    setHidden(statusEl, true);
    let desired;
    try {
      const read = (id, key) => {
        const rec = settingsSections.resources;
        const index = rec.els.findIndex(el => el.id === id);
        const modeIndex = rec.els.findIndex(el => el.id === `${id}-mode`);
        const mode = rec.els[modeIndex].value;
        const unchanged = mode === rec.originals[modeIndex].value && (mode !== 'custom' || rec.els[index].value === rec.originals[index].value);
        return unchanged ? originalResourceSettings[key] : readDirectSetting(document, id);
      };
      desired = {
        memory_limit_mb: read('resources-memory', 'memory_limit_mb'),
        cpu_quota_percent: read('resources-cpu', 'cpu_quota_percent'),
      };
    } catch (e) {
      if (e.fieldId) document.getElementById(e.fieldId)?.setAttribute('aria-invalid', 'true');
      setError(errEl, e.message);
      return;
    }
    let payload = resourceSettingsPatch(desired, originalResourceSettings);
    if (!retry && Object.keys(payload).length === 0) { snapshotSettingsSection('resources'); return; }
    const url = `/api/apps/${encodeURIComponent(slug)}`;
    let latest;
    // Read current lifecycle state before confirming; retry uses the latest
    // saved target, protected by the server's revision precondition.
    progress('Checking current limits…');
    try {
      const resp = await api(url, { cache: 'no-store' });
      if (!current()) return;
      if (resp.status === 401) { await handleUnauthorized(); return; }
      if (!resp.ok) throw new Error('read failed');
      latest = await resp.json();
      if (!current()) return;
      if (latest?.app?.slug !== slug || typeof latest.resource_revision !== 'string' || !latest.resource_revision) throw new Error('invalid app');
    } catch {
      if (current()) {
        setHidden(statusEl, true);
        setError(errEl, 'Could not check the latest limits. Your changes are kept. Try again.');
      }
      return;
    }
    if (retry) payload = { cpu_quota_percent: latest.app.cpu_quota_percent ?? null };
    const wasRunning = ['running', 'degraded'].includes(latest.app.status);
    if (wasRunning && !window.confirm('Save and apply resource limits? Changes apply live when supported. Lowering memory or changing limits on some runtimes restarts processes. Active sessions drain first; those still open at the deadline disconnect.')) {
      setHidden(statusEl, true);
      return;
    }
    progress(retry ? 'Retrying saved limits…' : 'Saving resource limits…');
    let resp, result;
    try {
      resp = await api(url, {
        method: 'PATCH',
        headers: { 'X-Shinyhub-If-Resource-Revision': latest.resource_revision },
        body: JSON.stringify(payload),
      });
      if (!current()) return;
      if (resp.status === 401) { await handleUnauthorized(); return; }
      if (!resp.ok) {
        let message = 'Could not save resource limits. Your changes are kept. Try again.';
        try {
          const body = await resp.json();
          if (typeof body?.error === 'string') message = resp.status === 409 && body.error.includes('app revision changed')
            ? 'The app changed while saving. Your changes are kept. Try again to check the latest state.'
            : body.error;
        } catch { /* non-JSON */ }
        if (current()) { setHidden(statusEl, true); setError(errEl, message); }
        return;
      }
      result = await resp.json();
      if (!current()) return;
      if (!result?.app) throw new Error('invalid confirmation');
    } catch {
      if (current()) {
        setHidden(statusEl, true);
        setError(errEl, 'Could not confirm resource limits. Your changes are kept. Try again to check the saved state.');
      }
      return;
    }
    resourceApplyPending = !!result.resource_update_error;
    originalResourceSettings = {
      memory_limit_mb: result.app.memory_limit_mb ?? null,
      cpu_quota_percent: result.app.cpu_quota_percent ?? null,
    };
    // Adopt both returned values: untouched limits may have changed elsewhere.
    const rec = settingsSections.resources;
    for (const [id, value] of [['resources-memory', originalResourceSettings.memory_limit_mb], ['resources-cpu', originalResourceSettings.cpu_quota_percent]]) {
      const input = document.getElementById(id);
      populateDirectSetting(document, id, value, !rec.btn.hidden);
      // Preserve the shared save lock until saveSettingsSection releases it.
      if (rec.saving) {
        input.disabled = true;
        document.getElementById(`${id}-mode`).disabled = true;
      }
    }
    document.getElementById('resources-memory').dataset.appStatus = result.app.status || '';
    snapshotSettingsSection('resources');
    updateWorkerCapacity();
    if (resourceApplyPending) {
      setHidden(statusEl, true);
      setError(errEl, 'Could not apply saved limits: ' + result.resource_update_error);
    } else {
      progress(wasRunning ? 'Resource limits saved. Any required restart runs automatically.' : 'Resource limits saved. They will be used when the app next starts.');
    }
    await loadApps();
    if (current()) await refreshDetailFleetState(slug);
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
    const idle = document.getElementById('hibernate-custom-minutes');
    const policy = document.getElementById('hibernate-custom-minutes-mode').value;
    const never = policy === 'off' || (policy === 'inherit' && idle.dataset.defaultValue === '0');
    const note = keepWarmInertNote(minWarm, isolation) || (minWarm > 0 && never
      ? 'Automatic hibernation is off. Keep warm still sets a minimum when autoscaling is enabled.' : '');
    inertEl.textContent = note;
    inertEl.hidden = !note;
  }

  // currentIsolationChoice is the isolation the Scaling select currently
  // shows: the saved value after a load, or the pending edit before a save.
  function currentIsolationChoice() {
    const sel = document.getElementById('worker-isolation');
    return sel?.value || sel?.dataset.defaultIsolation || 'multiplex';
  }

  function updateScalingCeiling() {
    const el = document.getElementById('scaling-ceiling');
    if (!el) return;
    el.hidden = ['grouped', 'per_session'].includes(currentIsolationChoice());
    if (el.hidden) return;
    const replicaText = document.getElementById('scaling-replicas').value.trim();
    const r = /^\d+$/.test(replicaText) ? Number(replicaText) : NaN;
    const capRaw = document.getElementById('scaling-cap-mode').value === 'inherit' ? '0' : document.getElementById('scaling-cap').value;
    const c = /^\d+$/.test(capRaw) ? Number(capRaw) : NaN;
    if (!Number.isFinite(r) || r < 1) { el.textContent = ''; return; }
    const customCap = document.getElementById('scaling-cap-mode').value === 'custom';
    if (!Number.isFinite(c) || c < (customCap ? 1 : 0) || c > 1000) { el.textContent = ''; return; }
    const inherited = document.getElementById('scaling-cap').dataset.defaultValue;
    const effective = c > 0 ? c : inherited === '' || inherited == null ? null : Number(inherited);
    if (effective == null) el.textContent = 'Session capacity unavailable: the server did not report its default cap.';
    else if (effective === 0) el.textContent = 'No per-replica session cap. CPU, memory and worker limits still apply.';
    else el.textContent = `Session capacity: ${r} × ${effective} = ${r * effective} concurrent sessions${c === 0 ? ' (inherited cap)' : ''}.`;
  }

  function updateWorkerCapacity() {
    const el = document.getElementById('worker-capacity');
    if (!el) return;
    updateWorkerFields(document);
    const mode = currentIsolationChoice();
    const gs = parseInt(document.getElementById('worker-grouped-size').value, 10);
    const mw = parseInt(document.getElementById('worker-max-workers').value, 10);
    // The per-session RAM estimate uses the configured memory limit so an
    // operator who has set limits gets a meaningful worst-case figure.
    const memoryInput = document.getElementById('resources-memory');
    const savedMemory = originalResourceSettings.memory_limit_mb;
    const memMB = Number(savedMemory == null ? memoryInput.dataset.defaultLimit : savedMemory);
    let capacity = workerCapacityLine(
      mode,
      Number.isFinite(gs) ? gs : 0,
      Number.isFinite(mw) ? mw : 0,
      Number.isFinite(memMB) ? memMB : 0,
    );
    if (capacity && isSectionDirty('resources')) capacity += ' Uses the saved memory limit; resource edits are not applied yet.';
    setStableText(el, capacity);
  }

  async function saveHibernateSettings() {
    if (!settingsSlug) return;
    const slug = settingsSlug, revision = settingsRevision;
    const current = () => settingsSlug === slug && settingsRevision === revision;
    const errEl = document.getElementById('hibernate-error');
    const statusEl = document.getElementById('hibernate-status');
    setError(errEl, '');
    setHidden(statusEl, true);

    let payload;
    try { payload = { hibernate_timeout_minutes: readDirectSetting(document, 'hibernate-custom-minutes') }; }
    catch (error) { if (error.fieldId) document.getElementById(error.fieldId)?.setAttribute('aria-invalid', 'true'); setError(errEl, error.message); return; }

    // Always include min_warm_replicas so the keep-warm floor is persisted
    // alongside any hibernation-mode change.
    const minWarmRaw = document.getElementById('min-warm-replicas').value.trim();
    const minWarm = /^\d+$/.test(minWarmRaw) ? Number(minWarmRaw) : NaN;
    if (!Number.isFinite(minWarm) || minWarm < 0 || minWarm > 1000) {
      document.getElementById('min-warm-replicas').setAttribute('aria-invalid', 'true');
      setError(errEl, 'Keep warm must be a whole number between 0 and 1000.');
      return;
    }
    payload.min_warm_replicas = Number(minWarm);

    statusEl.textContent = 'Saving hibernation…';
    setHidden(statusEl, false);
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}`, {
        method: 'PATCH',
        body: JSON.stringify(payload),
      });
    } catch {
      if (!current()) return;
      setHidden(statusEl, true);
      setError(errEl, 'Failed to save. Check your connection.');
      return;
    }

    if (!current()) return;
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      setHidden(statusEl, true);
      let message = 'Failed to save.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      if (!current()) return;
      setError(errEl, message);
      return;
    }

    if (!current()) return;
    statusEl.textContent = 'Hibernation settings saved.';
    setHidden(statusEl, false);
    snapshotSettingsSection('hibernate');
    // Re-evaluate the keep-warm warning using the values just saved.
    refreshKeepWarmNote();
    await loadApps();
    if (!current()) return;
    await refreshDetailFleetState(slug, current);
    if (!current()) return;
  }

  async function saveScalingSettings() {
    if (!settingsSlug) return;
    const slug = settingsSlug, revision = settingsRevision;
    const current = () => settingsSlug === slug && settingsRevision === revision;
    const errEl = document.getElementById('scaling-error');
    const statusEl = document.getElementById('scaling-status');
    setError(errEl, '');
    setHidden(statusEl, true);

    const replicasRaw = document.getElementById('scaling-replicas').value.trim();
    const capRaw = document.getElementById('scaling-cap-mode').value === 'inherit' ? '0' : document.getElementById('scaling-cap').value.trim();
    const replicas = /^\d+$/.test(replicasRaw) ? Number(replicasRaw) : NaN;
    const cap = parseInt(capRaw, 10);
    if (!Number.isFinite(replicas) || replicas < 1) {
      document.getElementById('scaling-replicas').setAttribute('aria-invalid', 'true');
      setError(errEl, 'Replicas must be a whole number ≥ 1.');
      return;
    }
    const customCap = document.getElementById('scaling-cap-mode').value === 'custom';
    if (!/^\d+$/.test(capRaw) || !Number.isFinite(cap) || cap < (customCap ? 1 : 0) || cap > 1000) {
      document.getElementById('scaling-cap').setAttribute('aria-invalid', 'true');
      setError(errEl, 'Max sessions per replica must be a whole number from 1 to 1000. Choose Server default to use the default limit.');
      return;
    }

    const replicasInput = document.getElementById('scaling-replicas');
    const isolationSelect = document.getElementById('worker-isolation');
    const workerIsolation = isolationSelect.value;
    let originalIsolation = isolationSelect.dataset.original || isolationSelect.dataset.defaultIsolation || 'multiplex';
    const effectiveIsolation = currentIsolationChoice();
    const maxWorkersRaw = document.getElementById('worker-max-workers').value.trim();
    const warmSparesRaw = document.getElementById('worker-warm-spares').value.trim();
    const workerMaxWorkers = /^\d+$/.test(maxWorkersRaw) ? Number(maxWorkersRaw) : NaN;
    const workerWarmSpares = /^\d+$/.test(warmSparesRaw) ? Number(warmSparesRaw) : NaN;
    if (!Number.isFinite(workerWarmSpares) || workerWarmSpares < 0 || workerWarmSpares > 1000) {
      document.getElementById('worker-warm-spares').setAttribute('aria-invalid', 'true');
      setError(errEl, 'Warm workers must be a whole number between 0 and 1000.');
      return;
    }
    if (effectiveIsolation !== 'multiplex' && (!Number.isInteger(workerMaxWorkers) || workerMaxWorkers < 1 || workerMaxWorkers > 1000)) {
      document.getElementById('worker-max-workers').setAttribute('aria-invalid', 'true');
      setError(errEl, 'Max workers must be a whole number from 1 to 1000 for per-session or grouped isolation.');
      return;
    }
    if (effectiveIsolation !== 'multiplex' && Number.isFinite(workerMaxWorkers) && workerWarmSpares > workerMaxWorkers) {
      document.getElementById('worker-warm-spares').setAttribute('aria-invalid', 'true');
      setError(errEl, 'Warm workers cannot exceed max workers.');
      return;
    }
    const groupedRaw = document.getElementById('worker-grouped-size').value.trim();
    if (effectiveIsolation === 'grouped' && (!/^\d+$/.test(groupedRaw) || Number(groupedRaw) < 1 || Number(groupedRaw) > 100)) {
      document.getElementById('worker-grouped-size').setAttribute('aria-invalid', 'true');
      setError(errEl, 'Clients per group must be a whole number from 1 to 100.'); return;
    }
    const savedScaling = scalingSettingsSnapshot(document);
    const payload = scalingSettingsPatch(savedScaling, originalScalingSettings);
    if (Object.keys(payload).length === 0) { snapshotSettingsSection('scaling'); return; }
    let originalReplicas = originalScalingSettings.replicas;
    let currentStatus = replicasInput.dataset.appStatus;
    let isolationChanged = Object.hasOwn(payload, 'worker_isolation') && effectiveIsolation !== originalIsolation;
    if (Object.hasOwn(payload, 'replicas') || Object.hasOwn(payload, 'worker_isolation')) {
      try {
        const latest = await api(`/api/apps/${encodeURIComponent(slug)}`);
        if (latest.status === 401) { await handleUnauthorized(); return; }
        if (!latest.ok) throw new Error('Could not read current replicas.');
        const latestBody = await latest.json();
        if (!current()) return;
        originalReplicas = latestBody.app?.replicas;
        currentStatus = latestBody.app?.status;
        originalIsolation = latestBody.app?.effective_worker_isolation || latestBody.app?.worker_isolation || isolationSelect.dataset.defaultIsolation || originalIsolation;
        isolationChanged = Object.hasOwn(payload, 'worker_isolation') && effectiveIsolation !== originalIsolation;
        if (!Number.isInteger(originalReplicas) || !currentStatus) throw new Error('Current app state unavailable.');
      } catch { if (current()) setError(errEl, 'Could not verify the current app state. Try again before changing replicas or isolation.'); return; }
    }
    const wasRunning = ['running', 'degraded'].includes(currentStatus);
    if (wasRunning && Object.hasOwn(payload, 'replicas') && replicas < originalReplicas && !window.confirm(
      `Reducing replicas from ${originalReplicas} to ${replicas} will drain the removed replicas. Sessions still using them when the drain timeout expires will be disconnected. Continue?`,
    )) return;
    if (wasRunning && isolationChanged && !window.confirm(
      `Changing worker isolation mode from ${originalIsolation} to ${effectiveIsolation} will drain sessions before replacing the pool. Sessions still active at the drain deadline will disconnect. Continue?`,
    )) return;
    statusEl.textContent = 'Saving scaling…';
    setHidden(statusEl, false);
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}`, {
        method: 'PATCH',
        body: JSON.stringify(payload),
      });
    } catch {
      if (!current()) return;
      setHidden(statusEl, true);
      setError(errEl, 'Failed to save. Check your connection.');
      return;
    }

    if (!current()) return;
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      setHidden(statusEl, true);
      let message = 'Failed to save.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      if (!current()) return;
      setError(errEl, message);
      return;
    }

    if (!current()) return;
    statusEl.textContent = 'Scaling settings saved.';
    setHidden(statusEl, false);
    originalScalingSettings = savedScaling;
    for (const id of ['worker-grouped-size', 'worker-max-workers', 'worker-warm-spares', 'scaling-replicas', 'scaling-cap', 'scaling-cap-mode']) delete document.getElementById(id).dataset.modeDraft;
    refreshDirectSetting(document, 'scaling-cap', { resetDraft: true });
    replicasInput.dataset.original = String(replicas);
    isolationSelect.dataset.original = workerIsolation;
    snapshotSettingsSection('scaling');
    // Isolation is one half of the keep-warm advisory shown in the Hibernation
    // fieldset, so a save that switches mode re-evaluates it against the
    // floor the operator has in the field.
    updateMinWarmWarning(replicas, parseInt(document.getElementById('min-warm-replicas').value, 10) || 0, effectiveIsolation);
    await loadApps();
    if (!current()) return;
    await refreshDetailFleetState(slug, current);
    if (!current()) return;
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

    let parsed;
    try { parsed = parseRenderSeconds(String(readDirectSetting(document, 'render-seconds'))); }
    catch (error) { if (error.fieldId) document.getElementById(error.fieldId)?.setAttribute('aria-invalid', 'true'); setError(errEl, error.message); return; }
    if (!parsed.ok) { setError(errEl, parsed.error); return; }

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
      if (settingsSlug !== slug) return;
      btn.disabled = false;
      setError(errEl, 'Failed to save. Check your connection.');
      return;
    }
    btn.disabled = false;

    if (settingsSlug !== slug) return;
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
    renderPacingBlock = (body && body.render_pacing) || (parsed.value > 0 ? { render_seconds: parsed.value } : null);
    updateRenderPacingAdvice();
    if (settingsSlug !== slug) return;
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

    enabledInput.disabled = !canEdit;
    minInput.disabled = !canEdit;
    maxInput.disabled = !canEdit;
    populateDirectSetting(document, 'autoscale-target', app.autoscale_target || 0, canEdit);
    syncAutoscaleControls();
    document.getElementById('autoscale-policy-fields').open = enabledInput.checked;
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
    document.getElementById('autoscale-policy-fields').open = enabled;
    if (enabled) {
      const minEl = document.getElementById('autoscale-min');
      const maxEl = document.getElementById('autoscale-max');
      const current = Math.max(1, parseInt(document.getElementById('scaling-replicas').value, 10) || 1);
      if ((parseInt(minEl.value, 10) || 0) < 1) minEl.value = String(current);
      const minNow = parseInt(minEl.value, 10) || 1;
      if ((parseInt(maxEl.value, 10) || 0) < minNow) maxEl.value = String(Math.max(minNow, current));
    }
    syncAutoscaleControls();
    updateAutoscaleCeiling();
    recomputeDirty('autoscale');
  }

  function syncAutoscaleControls() {
    const enabled = document.getElementById('autoscale-enabled');
    for (const id of ['autoscale-min', 'autoscale-max', 'autoscale-target']) {
      document.getElementById(id).disabled = enabled.disabled;
    }
    refreshDirectSetting(document, 'autoscale-target');
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
    el.hidden = !enabled;
    if (!enabled) {
      el.textContent = 'Autoscale is off. The bounds and target are inactive until you enable it.';
      return;
    }
    if (min === null || max === null || min < 1 || max < min) {
      el.textContent = '';
      return;
    }
    const summary = `Controller will steer between <strong>${min}</strong> and <strong>${max}</strong> replicas.`;
    if (el.innerHTML !== summary) el.innerHTML = summary;
  }

  async function saveAutoscaleSettings() {
    if (!settingsSlug) return;
    const slug = settingsSlug, revision = settingsRevision;
    const current = () => settingsSlug === slug && settingsRevision === revision;
    const errEl = document.getElementById('autoscale-error');
    const statusEl = document.getElementById('autoscale-status');
    setError(errEl, '');
    setHidden(statusEl, true);

    const { payload, error, fieldId } = readAutoscaleForm(document);
    if (error) {
      if (fieldId) document.getElementById(fieldId)?.setAttribute('aria-invalid', 'true');
      setError(errEl, error);
      return;
    }

    statusEl.textContent = 'Saving autoscale…';
    setHidden(statusEl, false);
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}`, {
        method: 'PATCH',
        body: JSON.stringify({ autoscale: payload }),
      });
    } catch {
      if (!current()) return;
      setHidden(statusEl, true);
      setError(errEl, 'Failed to save. Check your connection.');
      return;
    }

    if (!current()) return;
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      setHidden(statusEl, true);
      let message = 'Failed to save.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      if (!current()) return;
      setError(errEl, message);
      return;
    }

    if (!current()) return;
    statusEl.textContent = 'Autoscale policy saved.';
    setHidden(statusEl, false);
    snapshotSettingsSection('autoscale');
    await loadApps();
    if (!current()) return;
    await refreshDetailFleetState(slug, current);
    if (!current()) return;
  }

  function resetDangerZone() {
    const input = document.getElementById('delete-confirm');
    const btn = document.getElementById('delete-app-btn');
    input.value = '';
    document.querySelector('.settings-delete-confirm').open = false;
    btn.disabled = true;
    btn.textContent = 'Delete app';
    setError(document.getElementById('delete-error'), '');
  }

  // --- Environment tab ---

  let envEditingKey = null;
  let envBaseline = null;
  let envSaving = false;
  let envKeys = new Set();
  let envListRequest = 0;
  const envApplyStates = new Map();
  const envFormValue = () => JSON.stringify(['env-form-key', 'env-form-value', 'env-form-secret', 'env-form-restart'].map(id => {
    const el = document.getElementById(id); return el.type === 'checkbox' ? el.checked : el.value;
  }));
  function envFormDirty() { return !document.getElementById('env-form').hidden && envBaseline !== null && envFormValue() !== envBaseline; }
  function envRequestCurrent(slug, revision) { return settingsSlug === slug && settingsRevision === revision; }
  function envApp(slug) { return (detailApp?.slug === slug ? detailApp : null) || state.apps.find(app => app.slug === slug); }
  function syncEnvSecret() {
    const secret = document.getElementById('env-form-secret').checked;
    document.getElementById('env-form-value').type = secret ? 'password' : 'text';
    document.getElementById('env-form-value').autocomplete = secret ? 'new-password' : 'off';
    const reveal = document.getElementById('env-value-reveal');
    reveal.hidden = !secret; reveal.textContent = 'Show value'; reveal.setAttribute('aria-pressed', 'false');
  }
  function syncEnvApplyState(slug) {
    const running = ['running', 'degraded'].includes(envApp(slug)?.status);
    const saved = envApplyStates.get(slug);
    document.getElementById('env-apply-note').textContent = saved === 'pending'
      ? (running ? 'Saved changes are waiting for a restart.' : 'Saved changes will apply the next time the app starts.')
      : saved === 'applied' ? 'Saved variables were applied by the last restart.' : '';
    const button = document.getElementById('env-apply-btn');
    button.hidden = !canManageApp(state.user, envApp(slug)) || !running;
    button.textContent = saved === 'pending' ? 'Restart to apply' : 'Restart with saved values';
  }

  async function refreshEnvList(slug) {
    const revision = settingsRevision, request = ++envListRequest;
    const current = () => envRequestCurrent(slug, revision) && request === envListRequest;
    if (!current()) return;
    const tbody = document.querySelector('#env-list tbody');
    const table = document.getElementById('env-list');
    const status = document.getElementById('env-load-status');
    const error = document.getElementById('env-load-error');
    const retry = document.getElementById('env-retry-btn');
    const add = document.getElementById('env-add-btn');
    tbody.replaceChildren(); table.hidden = true;
    table.setAttribute('aria-busy', 'true');
    document.getElementById('env-empty').hidden = true;
    status.textContent = 'Loading variables…'; status.classList.remove('sr-only'); setError(error, ''); retry.hidden = true;
    add.disabled = true;
    const canWrite = canManageApp(state.user, envApp(slug));
    add.hidden = !canWrite;
    syncEnvApplyState(slug);
    try {
      const resp = await api(`/api/apps/${encodeURIComponent(slug)}/env`);
      if (!current()) return;
      if (resp.status === 401) { await handleUnauthorized(); return; }
      if (!resp.ok) throw new Error('request failed');
      const data = await resp.json();
      if (!current()) return;
      const vars = Array.isArray(data) ? data : data?.items;
      if (!Array.isArray(vars)) throw new Error('invalid response');
      envKeys = new Set(vars.map(v => v.key));
      document.getElementById('env-empty').hidden = vars.length > 0;
      table.hidden = vars.length === 0;
      status.classList.add('sr-only');
      status.textContent = vars.length ? `${vars.length} ${vars.length === 1 ? 'variable' : 'variables'} loaded.` : '';
      add.disabled = envSaving;
      for (const v of vars) {
        const tr = document.createElement('tr');
        tr.setAttribute('role', 'row');
        const keyTd = document.createElement('th');
        keyTd.scope = 'row'; keyTd.className = 'env-key'; keyTd.textContent = v.key;
        const valTd = document.createElement('td');
        valTd.className = v.secret ? 'env-value env-secret' : 'env-value';
        valTd.textContent = v.secret ? 'Secret value' : v.value === '' || v.value == null ? 'Empty value' : v.value;
        if (!v.secret && !v.value) valTd.classList.add('env-empty-value');
        const actTd = document.createElement('td');
        actTd.className = 'env-actions';
        if (v.source === 'bundle') {
          const note = document.createElement('span'); note.className = 'env-managed';
          note.textContent = 'Edit shinyhub.toml and deploy to change this variable.';
          actTd.appendChild(note);
        } else if (canWrite) {
          const editBtn = document.createElement('button'); editBtn.type = 'button';
          editBtn.textContent = v.secret ? 'Replace' : 'Edit';
          editBtn.setAttribute('aria-label', `${v.secret ? 'Replace' : 'Edit'} ${v.key}`);
          editBtn.addEventListener('click', () => openEnvForm(v));
          const delBtn = document.createElement('button'); delBtn.type = 'button';
          delBtn.className = 'env-btn-danger'; delBtn.textContent = 'Delete';
          delBtn.setAttribute('aria-label', `Delete ${v.key}`);
          delBtn.addEventListener('click', () => deleteEnvVar(slug, v.key, delBtn));
          actTd.append(editBtn, delBtn);
        }
        tr.append(keyTd, valTd, actTd); tbody.appendChild(tr);
      }
    } catch {
      if (current()) {
        status.textContent = ''; status.classList.add('sr-only');
        setError(error, 'Variables could not be loaded. Try again to see saved values.');
        retry.hidden = false;
      }
    } finally { if (current()) table.setAttribute('aria-busy', 'false'); }
  }

  async function retryEnvList() {
    const slug = settingsSlug, revision = settingsRevision;
    if (!slug) return;
    await refreshEnvList(slug);
    if (!envRequestCurrent(slug, revision)) return;
    const retry = document.getElementById('env-retry-btn');
    document.getElementById(retry.hidden ? 'env-add-btn' : 'env-retry-btn').focus({preventScroll:true});
  }

  function openEnvForm(existing) {
    if (envSaving) return;
    if (envFormDirty() && !window.confirm('Discard the unsaved variable edit?')) return;
    envEditingKey = existing ? existing.key : null;
    document.getElementById('env-form-heading').textContent = existing ? `${existing.secret ? 'Replace' : 'Edit'} ${existing.key}` : 'Add variable';
    document.getElementById('env-form-save').textContent = existing?.secret ? 'Replace secret' : existing ? 'Save changes' : 'Save variable';
    const keyInput = document.getElementById('env-form-key');
    const valueInput = document.getElementById('env-form-value');
    const secretInput = document.getElementById('env-form-secret');
    keyInput.removeAttribute('aria-invalid'); keyInput.removeAttribute('aria-errormessage');
    valueInput.removeAttribute('aria-invalid'); valueInput.removeAttribute('aria-errormessage');
    keyInput.value = existing ? existing.key : ''; keyInput.readOnly = !!existing;
    valueInput.value = environmentEditValue(existing);
    valueInput.placeholder = existing?.secret ? 'New secret value' : '';
    document.getElementById('env-key-help').textContent = existing ? 'Names cannot be changed.' : 'Use uppercase letters, numbers and underscores; start with a letter or underscore. SHINYHUB_ is reserved.';
    secretInput.checked = existing ? existing.secret : false; secretInput.disabled = !!existing;
    document.getElementById('env-form-restart').checked = false;
    setError(document.getElementById('env-form-error'), '');
    document.getElementById('env-form').hidden = false;
    document.getElementById('env-value-help').textContent = existing?.secret ? 'Enter a replacement. The stored secret cannot be shown.' : 'Empty values are allowed.';
    valueInput.required = !!existing?.secret;
    syncEnvSecret(); envBaseline = envFormValue();
    updateConfigurationDirty(document, 'configuration-environment', false);
    (existing ? valueInput : keyInput).focus();
  }

  function closeEnvForm(discard = false) {
    if (discard !== true && envFormDirty() && !window.confirm('Discard the unsaved variable edit?')) return;
    const form = document.getElementById('env-form');
    const hadFocus = form.contains(document.activeElement);
    form.hidden = true;
    // A completed or discarded secret must not remain in a hidden form.
    document.getElementById('env-form-value').value = '';
    syncEnvSecret();
    if (hadFocus) document.getElementById('env-add-btn').focus({ preventScroll: true });
    envEditingKey = null; envBaseline = null;
    updateConfigurationDirty(document, 'configuration-environment', false);
    setError(document.getElementById('env-form-error'), '');
  }

  async function withEnvMutation(button, action) {
    if (envSaving || !settingsSlug) return;
    const slug = settingsSlug, revision = settingsRevision;
    const current = () => envRequestCurrent(slug, revision);
    const controls = [...document.querySelectorAll('#configuration-environment input, #configuration-environment button')];
    const disabled = controls.map(el => el.disabled);
    envSaving = true;
    try {
      await runInFlight(button, async () => {
        controls.forEach(el => { el.disabled = true; });
        await action(slug, current);
      });
    } finally {
      envSaving = false;
      // These mounted controls are reused after navigation or a new session.
      // Release our locks even when the response belongs to an old context.
      controls.forEach((el, i) => { el.disabled = disabled[i]; });
      if (!document.getElementById('env-retry-btn').hidden || document.getElementById('env-list').getAttribute('aria-busy') === 'true') document.getElementById('env-add-btn').disabled = true;
      if (current()) {
        if (document.activeElement === document.body) {
          document.getElementById(document.getElementById('env-form').hidden ? 'env-add-btn' : 'env-form-save').focus({ preventScroll: true });
        }
      }
    }
  }

  async function submitEnvForm(e) {
    e.preventDefault();
    if (envSaving || !settingsSlug) return;
    const keyInput = document.getElementById('env-form-key');
    const key = keyInput.value.trim();
    const valueInput = document.getElementById('env-form-value');
    const value = valueInput.value, secret = document.getElementById('env-form-secret').checked;
    const restart = document.getElementById('env-form-restart').checked;
    const errEl = document.getElementById('env-form-error');
    setError(errEl, '');
    for (const input of [keyInput, valueInput]) { input.removeAttribute('aria-invalid'); input.removeAttribute('aria-errormessage'); }
    const invalid = (input, message) => {
      setError(errEl, message); input.setAttribute('aria-invalid', 'true'); input.setAttribute('aria-errormessage', errEl.id); input.focus();
    };
    if (!/^[A-Z_][A-Z0-9_]*$/.test(key)) { invalid(keyInput, 'Use uppercase letters, numbers and underscores. Start with a letter or underscore.'); return; }
    if (key.startsWith('SHINYHUB_')) { invalid(keyInput, 'SHINYHUB_ is reserved. Choose a different variable name.'); return; }
    if (!envEditingKey && envKeys.has(key)) { invalid(keyInput, 'This variable already exists. Use Edit or Replace on its row.'); return; }
    if (envEditingKey && secret && !value) { invalid(valueInput, 'Enter a new secret value to replace the stored secret.'); return; }
    if (new TextEncoder().encode(value).length > 65536) { invalid(valueInput, 'The value must be 64 KB or smaller.'); return; }
    if (restart && !window.confirm('Save and restart the app? Active sessions will drain first. Sessions still active at the drain deadline will disconnect.')) return;
    await withEnvMutation(document.getElementById('env-form-save'), async (slug, current) => {
      let resp;
      try {
        resp = await api(`/api/apps/${encodeURIComponent(slug)}/env/${encodeURIComponent(key)}${restart ? '?restart=true' : ''}`, {method:'PUT',body:JSON.stringify({value,secret})});
      } catch { if (current()) setError(errEl, 'Network error. Your draft is kept; try saving again.'); return; }
      if (!current()) return;
      if (resp.status === 401) { await handleUnauthorized(); return; }
      if (!resp.ok) {
        let message = 'The variable could not be saved. Your draft is kept; try again.';
        try { const body = await resp.json(); if (body.error) message = body.error; } catch { /* non-JSON */ }
        if (current()) setError(errEl, message);
        return;
      }
      let result;
      try { result = await resp.json(); if (!result || typeof result !== 'object') throw new Error('invalid response'); } catch {
        if (current()) setError(errEl, 'The save could not be confirmed. Reload to check saved values before trying again.'); return;
      }
      if (!current()) return;
      // An unchanged PUT deliberately skips restart on the server. Honor the
      // explicit Save and restart choice through the dedicated apply endpoint.
      if (restart && result.changed === false && !result.restarted) {
        try {
          const applied = await api(`/api/apps/${encodeURIComponent(slug)}/env/apply`, {method:'POST'});
          if (!current()) return;
          if (applied.status === 401) { await handleUnauthorized(); return; }
          const body = await applied.json();
          if (!body || applied.ok && typeof body.restarted !== 'boolean') throw new Error('invalid restart response');
          if (!current()) return;
          result = {...result, restarted:body.restarted === true, restart_required:body.restarted !== true,
            restart_error:applied.ok ? '' : body.restart_error || body.error || 'The app could not be restarted'};
        } catch { result = {...result, restart_error:'The restart could not be confirmed. Check the app status before trying again', restart_required:true}; }
      }
      if (!current()) return;
      if (result.restarted) envApplyStates.set(slug, 'applied');
      else if (result.changed !== false || result.restart_required || result.restart_error) envApplyStates.set(slug, 'pending');
      closeEnvForm(true);
      await refreshEnvList(slug);
      if (!current()) return;
      syncEnvApplyState(slug);
      if (result.restart_error) {
        setError(errEl, 'Variable saved, but applying it failed: ' + result.restart_error + '. Retry with the Restart to apply button.'); return;
      }
      flashToast(result.restarted ? 'Variable saved and applied.' : result.restart_required ? 'Variable saved. Restart to apply it to running processes.' : 'Variable saved.', 'success');
    });
  }

  async function deleteEnvVar(slug, key, btn) {
    if (envSaving || settingsSlug !== slug) return;
    if (envEditingKey === key && envFormDirty() && !window.confirm('Discard the unsaved edit before deleting this variable?')) return;
    if (!window.confirm(`Delete ${key}? Running processes keep its current value until the app restarts.`)) return;
    const errEl = document.getElementById('env-form-error'); setError(errEl, '');
    await withEnvMutation(btn, async (_, current) => {
      let resp;
      try { resp = await api(`/api/apps/${encodeURIComponent(slug)}/env/${encodeURIComponent(key)}`, {method:'DELETE'}); }
      catch { if (current()) setError(errEl, 'The deletion could not be confirmed. Reload to check saved values before trying again.'); return; }
      if (!current()) return;
      if (resp.status === 401) { await handleUnauthorized(); return; }
      if (!resp.ok) { setError(errEl, 'The variable could not be deleted. Try again.'); return; }
      const pending = resp.headers.get('X-Shinyhub-Restart-Required') === 'true';
      envApplyStates.set(slug, 'pending');
      if (envEditingKey === key) closeEnvForm(true);
      await refreshEnvList(slug);
      if (!current()) return;
      syncEnvApplyState(slug);
      flashToast(pending ? 'Variable deleted. Restart to apply the removal to running processes.' : 'Variable deleted.', 'success');
      document.getElementById('env-add-btn').focus({preventScroll:true});
    });
  }

  async function applyEnvChanges() {
    if (envSaving || !settingsSlug) return;
    if (!window.confirm('Restart with saved variables? Unsaved edits are not included. Active sessions will drain first. Sessions still active at the drain deadline will disconnect.')) return;
    const error = document.getElementById('env-form-error'); setError(error, '');
    await withEnvMutation(document.getElementById('env-apply-btn'), async (slug, current) => {
      try {
        const resp = await api(`/api/apps/${encodeURIComponent(slug)}/env/apply`, {method:'POST'});
        if (!current()) return;
        if (resp.status === 401) { await handleUnauthorized(); return; }
        let result;
        try { result = await resp.json(); if (!result || typeof result.restarted !== 'boolean' && resp.ok) throw new Error('invalid response'); } catch {
          if (current()) setError(error, 'The restart could not be confirmed. Check the app status before trying again.'); return;
        }
        if (!current()) return;
        if (!resp.ok) { setError(error, result.restart_error || result.error || 'Saved variables could not be applied. Try again.'); return; }
        envApplyStates.set(slug, result.restarted ? 'applied' : 'pending');
        syncEnvApplyState(slug);
        flashToast(result.restarted ? 'Saved environment applied.' : 'Saved variables will apply when the app starts.', 'success');
        await loadApps();
        if (current()) syncEnvApplyState(slug);
      } catch { if (current()) setError(error, 'The restart could not be confirmed. Check the app status before trying again.'); }
    });
  }

  let accessPanelRevision = 0;
  let memberListRequest = 0;
  let groupListRequest = 0;
  const accessWrites = new Map();

  function beginAccessWrite(kind, slug, revision) {
    const key = `${revision}:${kind}`;
    accessWrites.set(key, (accessWrites.get(key) || 0) + 1);
    const refresh = document.getElementById(kind === 'members' ? 'members-refresh' : 'groups-refresh');
    if (refresh) refresh.disabled = true;
    const invalidate = () => {
      if (!accessRequestCurrent(slug, revision)) return;
      if (kind === 'members') memberListRequest++; else groupListRequest++;
      const list = document.getElementById(kind === 'members' ? 'members-list' : 'group-access-list');
      list.setAttribute('aria-busy', 'false');
      list.querySelector('.access-list-state')?.remove();
    };
    invalidate();
    let released = false;
    return () => {
      if (released) return;
      released = true;
      const remaining = accessWrites.get(key) - 1;
      if (remaining) accessWrites.set(key, remaining); else accessWrites.delete(key);
      if (accessRequestCurrent(slug, revision) && refresh) refresh.disabled = remaining > 0;
      invalidate();
    };
  }

  function restoreAccessFocus(list, key) {
    if (!key) return;
    const target = [...list.querySelectorAll('[data-focus-key]')].find(node => node.dataset.focusKey === key && !node.disabled);
    (target || document.querySelector(list.id === 'members-list' ? '#member-add > summary' : '#group-add > summary'))?.focus();
  }

  function accessRequestCurrent(slug, revision) {
    return slug === settingsSlug && revision === accessPanelRevision;
  }

  function accessListState(list, message, retry, preserve = false) {
    const ownedFocus = list.contains(document.activeElement);
    if (preserve) list.querySelector('.access-list-state')?.remove();
    else list.replaceChildren();
    const status = document.getElementById(list.id === 'members-list' ? 'members-status' : 'groups-status');
    status.textContent = retry ? `${message} Try again using the button below.` : message;
    const li = document.createElement('li');
    li.className = 'access-list-state';
    const text = document.createElement('span');
    text.textContent = message;
    // The adjacent live region announces this state once. Keep the visual
    // duplicate out of the accessibility tree, while preserving retry access.
    text.setAttribute('aria-hidden', 'true');
    if (!retry) li.setAttribute('aria-hidden', 'true');
    li.appendChild(text);
    if (retry) {
      const button = document.createElement('button');
      button.type = 'button';
      button.textContent = 'Try again';
      button.addEventListener('click', retry);
      li.appendChild(button);
    }
    list.prepend(li);
    if (ownedFocus && document.activeElement === document.body) {
      if (retry) li.querySelector('button').focus();
      else { list.tabIndex = -1; list.focus(); }
    }
  }

  async function refreshMemberList() {
    const slug = settingsSlug, revision = accessPanelRevision, request = ++memberListRequest;
    if (!slug || accessWrites.has(`${revision}:members`)) return;
    const list = document.getElementById('members-list');
    const current = () => accessRequestCurrent(slug, revision) && request === memberListRequest;
    list.setAttribute('aria-busy', 'true');
    accessListState(list, 'Loading members…', null, true);
    try {
      const resp = await api(`/api/apps/${encodeURIComponent(slug)}/members`, { signal: AbortSignal.timeout(20000) });
      if (!current()) return;
      if (resp.status === 401) { await handleUnauthorized(); return; }
      if (!resp.ok) throw new Error('request failed');
      const body = await resp.json();
      if (!current()) return;
      const members = Array.isArray(body) ? body : body?.items;
      if (!Array.isArray(members)) throw new Error('invalid response');
      const focused = list.contains(document.activeElement) ? document.activeElement?.dataset.focusKey : null;
      const listFocused = document.activeElement === list;
      list.replaceChildren();
      if (!members.length) accessListState(list, 'No members added.');
      else document.getElementById('members-status').textContent = `${members.length} ${members.length === 1 ? 'member' : 'members'} loaded.`;
      for (const m of members) {
        const li = document.createElement('li');
        const nameSpan = document.createElement('span');
        nameSpan.className = 'member-name';
        const ownMembership = m.user_id === state.user?.id;
        nameSpan.textContent = ownMembership ? `${m.username} (you)` : m.username;
        const roleSelect = document.createElement('select');
        roleSelect.className = 'member-role member-role-select';
        roleSelect.setAttribute('aria-label', `Role for ${m.username}`);
        for (const role of ['viewer', 'manager']) {
          const opt = document.createElement('option');
          opt.value = role;
          opt.textContent = role.charAt(0).toUpperCase() + role.slice(1);
          opt.selected = m.role === role;
          roleSelect.appendChild(opt);
        }
        roleSelect.dataset.previous = m.role;
        roleSelect.dataset.focusKey = `member-role:${m.user_id}`;
        roleSelect.addEventListener('change', () => updateMemberRole(m.user_id, m.username, roleSelect));
        const revokeBtn = document.createElement('button');
        revokeBtn.type = 'button';
        revokeBtn.dataset.focusKey = `member-remove:${m.user_id}`;
        revokeBtn.className = 'access-remove';
        revokeBtn.textContent = 'Remove';
        revokeBtn.setAttribute('aria-label', `Remove member ${m.username}`);
        revokeBtn.addEventListener('click', () => runInFlight(revokeBtn, async () => {
          if (!accessRequestCurrent(slug, revision) || !revokeBtn.isConnected) return;
          const finishWrite = beginAccessWrite('members', slug, revision);
          const error = document.getElementById('grant-error');
          setError(error, '');
          roleSelect.disabled = true;
          try {
            const r = await api(`/api/apps/${encodeURIComponent(slug)}/members`, {
              method: 'DELETE', signal: AbortSignal.timeout(20000), body: JSON.stringify({ user_id: m.user_id }),
            });
            if (!accessRequestCurrent(slug, revision)) return;
            if (r.status === 401) { await handleUnauthorized(); return; }
            if (!r.ok) throw new Error('request failed');
            finishWrite();
            await refreshMemberList();
            if (!accessRequestCurrent(slug, revision)) return;
            flashToast(`Removed ${m.username} from members`, 'success');
            document.querySelector('#member-add > summary').focus();
          } catch {
            if (accessRequestCurrent(slug, revision)) setError(error, `Could not remove ${m.username}. Refresh to check their access, then try again.`);
          } finally { finishWrite(); roleSelect.disabled = false; }
        }));
        if (ownMembership) {
          roleSelect.disabled = true; revokeBtn.disabled = true;
          const note = document.createElement('span');
          note.id = `own-membership-${m.user_id}`;
          note.className = 'access-managed';
          note.textContent = 'Another manager must change your membership.';
          roleSelect.setAttribute('aria-describedby', note.id);
          revokeBtn.setAttribute('aria-describedby', note.id);
          li.append(nameSpan, roleSelect, revokeBtn, note);
        } else li.append(nameSpan, roleSelect, revokeBtn);
        list.appendChild(li);
      }
      restoreAccessFocus(list, focused || (listFocused ? '__list' : null));
    } catch {
      if (current()) accessListState(list, `Could not load members.${list.querySelector('.member-name') ? ' Showing the last known entries.' : ''}`, refreshMemberList, true);
    } finally { if (current()) list.setAttribute('aria-busy', 'false'); }
  }

  async function refreshGroupAccessList() {
    const slug = settingsSlug, revision = accessPanelRevision, request = ++groupListRequest;
    if (!slug || accessWrites.has(`${revision}:group-access`)) return;
    const list = document.getElementById('group-access-list');
    const current = () => accessRequestCurrent(slug, revision) && request === groupListRequest;
    list.setAttribute('aria-busy', 'true');
    accessListState(list, 'Loading groups…', null, true);
    try {
      const resp = await api(`/api/apps/${encodeURIComponent(slug)}/group-access`, { signal: AbortSignal.timeout(20000) });
      if (!current()) return;
      if (resp.status === 401) { await handleUnauthorized(); return; }
      if (!resp.ok) throw new Error('request failed');
      const body = await resp.json();
      if (!current()) return;
      const rules = Array.isArray(body) ? body : body?.items;
      if (!Array.isArray(rules)) throw new Error('invalid response');
      const focused = list.contains(document.activeElement) ? document.activeElement?.dataset.focusKey : null;
      const listFocused = document.activeElement === list;
      list.replaceChildren();
      if (!rules.length) accessListState(list, 'No groups added.');
      else document.getElementById('groups-status').textContent = `${rules.length} ${rules.length === 1 ? 'group' : 'groups'} loaded.`;
      for (const rule of rules) {
        const li = document.createElement('li');
        li.dataset.group = rule.group;
        const nameSpan = document.createElement('span');
        nameSpan.className = 'member-name';
        nameSpan.textContent = rule.group;
        const roleSpan = document.createElement('span');
        roleSpan.className = 'member-role';
        roleSpan.textContent = rule.role === 'manager' ? 'Manager' : 'Viewer';
        li.append(nameSpan, roleSpan);
        if (rule.source === 'manifest') {
          const note = document.createElement('span');
          note.className = 'access-managed';
          note.textContent = 'Edit shinyhub.toml and deploy to change this group.';
          li.appendChild(note);
        } else {
          const roleSelect = document.createElement('select');
          roleSelect.className = 'member-role group-role-select';
          roleSelect.setAttribute('aria-label', `Role for group ${rule.group}`);
          for (const role of ['viewer', 'manager']) {
            const option = document.createElement('option');
            option.value = role; option.textContent = role === 'manager' ? 'Manager' : 'Viewer';
            option.selected = rule.role === role; roleSelect.appendChild(option);
          }
          roleSelect.dataset.previous = rule.role;
          roleSelect.dataset.focusKey = `group-role:${rule.group}`;
          roleSelect.addEventListener('change', () => updateGroupRole(rule.group, roleSelect));
          roleSpan.replaceWith(roleSelect);
          const removeBtn = document.createElement('button');
          removeBtn.type = 'button';
          removeBtn.dataset.focusKey = `group-remove:${rule.group}`;
          removeBtn.className = 'access-remove';
          removeBtn.textContent = 'Remove';
          removeBtn.setAttribute('aria-label', `Remove group grant for ${rule.group}`);
          removeBtn.addEventListener('click', () => runInFlight(removeBtn, async () => {
            if (!accessRequestCurrent(slug, revision) || !removeBtn.isConnected) return;
            const finishWrite = beginAccessWrite('group-access', slug, revision);
            const error = document.getElementById('group-access-error');
            setError(error, '');
            roleSelect.disabled = true;
            try {
              const r = await api(`/api/apps/${encodeURIComponent(slug)}/group-access/${encodeURIComponent(rule.group)}`, { method: 'DELETE', signal: AbortSignal.timeout(20000) });
              if (!accessRequestCurrent(slug, revision)) return;
              if (r.status === 401) { await handleUnauthorized(); return; }
              if (!r.ok) throw new Error('request failed');
              finishWrite();
              if (!await reconcileGroupPermissions(slug, revision)) return;
              await refreshGroupAccessList();
              if (!accessRequestCurrent(slug, revision)) return;
              flashToast(`Removed group grant for ${rule.group}`, 'success');
              document.querySelector('#group-add > summary').focus();
            } catch {
              if (accessRequestCurrent(slug, revision)) setError(error, `Could not remove ${rule.group}. Refresh to check the group’s access, then try again.`);
            } finally { finishWrite(); roleSelect.disabled = false; }
          }));
          li.appendChild(removeBtn);
        }
        list.appendChild(li);
      }
      restoreAccessFocus(list, focused || (listFocused ? '__list' : null));
    } catch {
      if (current()) accessListState(list, `Could not load groups.${list.querySelector('.member-name') ? ' Showing the last known entries.' : ''}`, refreshGroupAccessList, true);
    } finally { if (current()) list.setAttribute('aria-busy', 'false'); }
  }

  // A manager can remove their own authority through a group grant. Ask the
  // server again before leaving stale management controls on screen.
  async function reconcileGroupPermissions(slug, revision) {
    try {
      const resp = await api(`/api/apps/${encodeURIComponent(slug)}`, { cache: 'no-store' });
      if (!accessRequestCurrent(slug, revision)) return false;
      if (resp.status === 401) { await handleUnauthorized(); return false; }
      let canManage = false;
      if (resp.ok) {
        const body = await resp.json();
        if (!accessRequestCurrent(slug, revision)) return false;
        if (!body.app || typeof body.can_manage !== 'boolean') throw new Error('invalid permissions');
        canManage = body.can_manage;
        if (detailApp?.slug === slug) Object.assign(detailApp, body.app, { can_manage: canManage });
        const cached = state.apps.find(app => app.slug === slug);
        if (cached) Object.assign(cached, body.app, { can_manage: canManage });
      } else if (resp.status !== 403 && resp.status !== 404) throw new Error('permission check failed');
      if (canManage) return true;
      document.getElementById('detail-access-panel').hidden = true;
      closeEnvForm(true);
      clearSettingsDirty();
      settingsSlug = null;
      settingsRevision++;
      accessPanelRevision++;
      syncDetailHeaderActions(detailApp);
      syncAppContext();
      flashToast('Group change saved. You no longer manage this app.', 'info');
      router.navigate(resp.ok && state.user?.role !== 'viewer' ? `/apps/${encodeURIComponent(slug)}` : '/apps', { replace: true });
    } catch {
      if (accessRequestCurrent(slug, revision)) setError(document.getElementById('group-access-error'), 'Group change saved, but your permissions could not be checked. Reload before making another change.');
    }
    return false;
  }

  async function updateGroupRole(group, selectEl) {
    const slug = settingsSlug, revision = accessPanelRevision;
    if (!slug || !selectEl.isConnected || selectEl.disabled) return;
    const finishWrite = beginAccessWrite('group-access', slug, revision);
    const role = selectEl.value, previous = selectEl.dataset.previous;
    const error = document.getElementById('group-access-error');
    const removeBtn = selectEl.closest('li').querySelector('.access-remove');
    setError(error, '');
    selectEl.disabled = removeBtn.disabled = true;
    try {
      const resp = await api(`/api/apps/${encodeURIComponent(slug)}/group-access`, {
        method: 'POST', signal: AbortSignal.timeout(20000), body: JSON.stringify({ group, role }),
      });
      if (!accessRequestCurrent(slug, revision)) return;
      if (resp.status === 401) { await handleUnauthorized(); return; }
      if (!resp.ok) throw new Error('request failed');
      selectEl.dataset.previous = role;
      if (!await reconcileGroupPermissions(slug, revision)) return;
      flashToast(`Updated group ${group} to ${role}`, 'success');
    } catch {
      selectEl.value = previous;
      if (accessRequestCurrent(slug, revision)) setError(error, `Could not update the role for ${group}. Refresh to check its role, then try again.`);
    } finally { finishWrite(); selectEl.disabled = removeBtn.disabled = false; }
  }

  async function updateMemberRole(userId, username, selectEl) {
    const slug = settingsSlug, revision = accessPanelRevision;
    if (!slug || !selectEl.isConnected || selectEl.disabled) return;
    const finishWrite = beginAccessWrite('members', slug, revision);
    const newRole = selectEl.value;
    const previous = selectEl.dataset.previous || '';
    const error = document.getElementById('grant-error');
    setError(error, '');
    const removeBtn = selectEl.closest('li').querySelector('button');
    selectEl.disabled = true;
    removeBtn.disabled = true;
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}/members/${userId}`, {
        method: 'PATCH', signal: AbortSignal.timeout(20000),
        body: JSON.stringify({ role: newRole }),
      });
    } catch {
      finishWrite();
      selectEl.disabled = false;
      removeBtn.disabled = false;
      if (previous) selectEl.value = previous;
      if (accessRequestCurrent(slug, revision)) setError(error, `Could not update the role for ${username}. Check your connection and try again.`);
      return;
    }
    finishWrite();
    selectEl.disabled = false;
    removeBtn.disabled = false;
    if (!accessRequestCurrent(slug, revision)) return;
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      if (previous) selectEl.value = previous; // revert to last-known role
      setError(error, `Could not update the role for ${username}. Refresh this page to check their role, then try again.`);
      return;
    }
    selectEl.dataset.previous = newRole;
    flashToast(`Updated ${username} to ${newRole}`, 'success');
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
  document.getElementById('people-signin-copy').addEventListener('click', async () => {
    const status = document.getElementById('people-onboarding-status');
    try { await navigator.clipboard.writeText(`${window.location.origin}/login`); status.textContent = 'Sign-in link copied. People need access through your sign-in provider.'; flashToast('Sign-in link copied.', 'success'); }
    catch { status.textContent = `Could not copy. Share ${window.location.origin}/login`; }
  });
  newUserButton.addEventListener('click', openNewUserModal);
  newUserClose.addEventListener('click', closeNewUserModal);
  newUserCancel.addEventListener('click', closeNewUserModal);
  newUserForm.addEventListener('submit', submitNewUser);

  if (profileTokensLink) profileTokensLink.addEventListener('click', event => {
    if (!closeProfileModal()) { event.preventDefault(); event.stopImmediatePropagation(); }
  });

  // Theme: re-apply the saved preference (the inline head script already set it
  // pre-paint) and, while on 'system', track OS light/dark changes live; keep the
  // Appearance radios in sync when the OS flips.
  initTheme(window, reflectThemeRadios);
  document.querySelectorAll('input[name="theme-pref"]').forEach((el) => {
    el.addEventListener('change', () => {
      if (el.checked) setThemePreference(window, el.value);
    });
  });

  // Profile opens from the shared account menu.
  document.getElementById('profile-button').addEventListener('click', openProfileModal);
  function previewAvatarColor(color) {
    if (!state.user || profileSaving || state.user.can_manage_self === false) return;
    pendingAvatarColor = color;
    renderProfileAvatar();
    syncProfileColor();
    setError(profileError, '');
    setHidden(profileSuccess, true);
    syncProfileSave();
  }
  if (profileAvatarColor) profileAvatarColor.addEventListener('input', () => previewAvatarColor(profileAvatarColor.value));
  if (profileColorReset) profileColorReset.addEventListener('click', () => {
    previewAvatarColor('');
    profileAvatarColor.focus();
  });
  if (profilePhotoChange) profilePhotoChange.addEventListener('click', () => profilePhotoFile.click());
  if (profilePhotoFile) profilePhotoFile.addEventListener('change', async () => {
    const file = profilePhotoFile.files?.[0];
    profilePhotoFile.value = '';
    if (!file || profileSaving || state.user?.can_manage_self === false) return;
    const generation = ++photoGeneration;
    photoPreparing = true;
    profileSubmit.disabled = true;
    profilePhotoStatus.textContent = 'Preparing photo…';
    setError(profileError, '');
    setHidden(profileSuccess, true);
    try {
      const photo = await prepareUserAvatar(file, window);
      if (generation !== photoGeneration) return;
      pendingAvatar = photo;
      renderProfileAvatar();
      setHidden(profilePhotoRemove, false);
      profilePhotoStatus.textContent = 'Unsaved changes';
    } catch (err) {
      if (generation !== photoGeneration) return;
      profilePhotoStatus.textContent = '';
      setError(profileError, err.message);
    } finally {
      if (generation === photoGeneration) {
        photoPreparing = false;
        syncProfileSave();
      }
    }
  });
  if (profilePhotoRemove) profilePhotoRemove.addEventListener('click', () => {
    if (profileSaving) return;
    photoGeneration++;
    photoPreparing = false;
    profileSubmit.disabled = false;
    pendingAvatar = '';
    renderProfileAvatar();
    setHidden(profilePhotoRemove, true);
    profilePhotoChange.focus();
    profilePhotoStatus.textContent = 'Unsaved changes';
    setError(profileError, '');
    setHidden(profileSuccess, true);
    syncProfileSave();
  });
  for (const input of [profileDisplayName, profileCurrentPw, profileNewPw]) {
    if (input) input.addEventListener('input', () => {
      setError(profileError, '');
      setHidden(profileSuccess, true);
      const field = input === profileDisplayName ? 'name' : input === profileCurrentPw ? 'current' : 'new';
      // Show the upper bound while typing, before a valid-looking password
      // reaches the server. Minimum-length errors wait until submission.
      const lengthError = field === 'new' && new TextEncoder().encode(input.value).length > 72
        ? profileFieldErrors('', '', input.value).new : '';
      setProfileFieldError(document, field, lengthError);
      syncProfileSave();
    });
  }
  if (profileCancel) profileCancel.addEventListener('click', closeProfileModal);
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

  resetPwClose.addEventListener('click', closeResetPasswordModal);
  resetPwCancel.addEventListener('click', closeResetPasswordModal);
  resetPwForm.addEventListener('submit', submitResetPassword);
  supportReason?.addEventListener('input', () => supportReason.setCustomValidity(supportReasonError(supportReason.value)));
  if (supportForm) supportForm.addEventListener('submit', submitSupportSession);
  supportClose?.addEventListener('click', closeSupportSessionModal);
  supportCancel?.addEventListener('click', closeSupportSessionModal);
  supportAppsRetry?.addEventListener('click', () => loadSupportSessionApps());
  supportApp?.addEventListener('change', () => {
    if (!supportApp.disabled && supportApp.value) {
      clearSupportAppOwnedError(supportApp, supportError);
      supportSubmit.disabled = false;
      supportAppStatus.textContent = 'App selected. Review the reason, then start the support session.';
    }
  });
  supportReauth?.addEventListener('click', reauthenticateForSupportSession);
  supportModal?.addEventListener('click', event => { if (event.target === event.currentTarget) closeSupportSessionModal(); });
  window.addEventListener('focus', () => {
    if (state.user?.role === 'admin') supportRecovery.load();
  });
  // Apps search + sort. Restore previous values from sessionStorage.
  const appFilterFields = {search:document.getElementById('apps-search'),sort:document.getElementById('apps-sort'),segment:document.getElementById('apps-segment')};
  const appFilterKeys = {search:'appsSearch',sort:'appsSort',segment:'appsSegment'};
  for (const [name,field] of Object.entries(appFilterFields)) {
    try { const saved = sessionStorage.getItem(appFilterKeys[name]); if(saved) field.value = saved; }
    catch { /* storage may be blocked */ }
  }
  bindFilterBar({root:appFilterFields.search.closest('.apps-filters'),fields:appFilterFields,
    resetFields:['search','segment'],clearButtons:[document.getElementById('apps-clear-filters'),document.getElementById('empty-state-clear')],
    onChange(values) {
      for (const [name,value] of Object.entries(values)) {
        try { sessionStorage.setItem(appFilterKeys[name],value); } catch { /* storage may be blocked */ }
      }
      renderApps();
    }});

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
      } else if (supportModal && !supportModal.hidden) {
        closeSupportSessionModal();
      } else if (!resetPwModal.hidden) {
        closeResetPasswordModal();
      } else if (scheduleModal && !scheduleModal.hidden) {
        closeScheduleForm();
      } else if (newTokenModal && !newTokenModal.hidden) {
        // The controller protects a one-time reveal from accidental dismissal.
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

  newUserModal.addEventListener('click', e => {
    if (e.target === e.currentTarget) closeNewUserModal();
  });
  resetPwModal.addEventListener('click', e => {
    if (e.target === e.currentTarget) closeResetPasswordModal();
  });

  // --- Settings tabs: explicit-save wiring + dirty tracking ---
  // General + Resources (display name/project, memory/CPU limits).
  document.getElementById('general-description').addEventListener('input', event => {
    const count = [...event.target.value].length;
    document.getElementById('general-description-count').textContent = `${count} / 280 characters`;
    event.target.setAttribute('aria-invalid', String(count > 280));
  });
  document.getElementById('general-save-btn').addEventListener('click', () => saveSettingsSection('general', saveGeneralInfo));
  document.getElementById('usage-privacy-save-btn').addEventListener('click', () => saveSettingsSection('usage-privacy', saveUsagePrivacy));
  document.getElementById('usage-privacy-mode').addEventListener('change', updateUsagePrivacyHelp);
  document.getElementById('resources-save-btn').addEventListener('click', () => saveSettingsSection('resources', saveResources));
  document.getElementById('resources-retry-btn').addEventListener('click', () => saveSettingsSection('resources', () => saveResources(true)));

  // Hibernation: mode radios + save button.

  document.getElementById('hibernate-save-btn').addEventListener('click', () => saveSettingsSection('hibernate', saveHibernateSettings));

  // Scaling: save + live admission-ceiling helper.
  document.getElementById('scaling-save-btn').addEventListener('click', () => saveSettingsSection('scaling', saveScalingSettings));
  document.getElementById('scaling-replicas').addEventListener('input', updateScalingCeiling);
  document.getElementById('scaling-cap').addEventListener('input', updateScalingCeiling);

  // Render pacing: save + live advisory line.
  document.getElementById('render-save-btn').addEventListener('click', () => saveSettingsSection('render', saveRenderPacing));
  document.getElementById('render-seconds').addEventListener('input', updateRenderPacingAdvice);

  // Worker isolation: live capacity helper on any input change.
  document.getElementById('worker-isolation').addEventListener('change', () => {
    switchWorkerFields(document, originalScalingSettings);
    updateWorkerCapacity(); updateScalingCeiling(); recomputeDirty('scaling');
  });
  // Hibernation advice uses saved Scaling settings, since sections save separately.
  const refreshKeepWarmNote = () => {
    const replicas = originalScalingSettings.replicas;
    const minWarm = parseInt(document.getElementById('min-warm-replicas').value, 10);
    const isolation = originalScalingSettings.worker_isolation || document.getElementById('worker-isolation').dataset.defaultIsolation || 'multiplex';
    updateMinWarmWarning(Number.isFinite(replicas) ? replicas : 1,
      Number.isFinite(minWarm) ? minWarm : 0, isolation);
  };
  document.getElementById('worker-isolation').addEventListener('change', refreshKeepWarmNote);
  document.getElementById('min-warm-replicas').addEventListener('input', refreshKeepWarmNote);
  document.getElementById('worker-grouped-size').addEventListener('input', updateWorkerCapacity);
  document.getElementById('worker-max-workers').addEventListener('input', updateWorkerCapacity);
  document.getElementById('worker-warm-spares').addEventListener('input', updateWorkerCapacity);

  // Autoscale: enabling seeds sane bounds; bounds inputs + target-mode radios.

  document.getElementById('autoscale-save-btn').addEventListener('click', () => saveSettingsSection('autoscale', saveAutoscaleSettings));
  document.getElementById('autoscale-enabled').addEventListener('change', onAutoscaleEnabledChange);
  document.getElementById('autoscale-min').addEventListener('input', updateAutoscaleCeiling);
  document.getElementById('autoscale-max').addEventListener('input', updateAutoscaleCeiling);

  // Environment tab: add button, form submit/cancel.
  document.getElementById('env-add-btn').addEventListener('click', () => openEnvForm(null));
  document.getElementById('env-apply-btn').addEventListener('click', applyEnvChanges);
  document.getElementById('env-retry-btn').addEventListener('click', retryEnvList);
  document.getElementById('env-form-key').addEventListener('input', event => {
    event.target.removeAttribute('aria-invalid');
    event.target.removeAttribute('aria-errormessage');
  });
  document.getElementById('env-form-secret').addEventListener('change', syncEnvSecret);
  document.getElementById('env-value-reveal').addEventListener('click', () => {
    const input = document.getElementById('env-form-value'); const reveal = document.getElementById('env-value-reveal');
    const show = input.type === 'password'; input.type = show ? 'text' : 'password';
    reveal.textContent = show ? 'Hide value' : 'Show value'; reveal.setAttribute('aria-pressed', String(show));
  });
  for (const event of ['input', 'change']) document.getElementById('env-form').addEventListener(event, () => updateConfigurationDirty(document, 'configuration-environment', envFormDirty()));
  document.getElementById('env-form').addEventListener('submit', submitEnvForm);
  document.getElementById('env-form-cancel').addEventListener('click', closeEnvForm);

  // Data tab: upload form submit.

  // Visibility: explicit Save. (Previously auto-applied on radio change; now
  // consistent with every other settings tab.)
  document.getElementById('visibility-save-btn').addEventListener('click', saveVisibility);

  // Register every settings section with the dirty tracker so Save stays
  // disabled until something changes and an "Unsaved changes" hint appears.
  registerSettingsSection('general',
    () => [document.getElementById('general-name'), document.getElementById('general-description'), document.getElementById('general-project')],
    'general-save-btn', 'general-dirty');
  registerSettingsSection('usage-privacy',
    () => [document.getElementById('usage-privacy-mode')],
    'usage-privacy-save-btn', 'usage-privacy-dirty');
  registerSettingsSection('resources',
    () => ['resources-memory', 'resources-cpu', 'resources-memory-mode', 'resources-cpu-mode'].map(id => document.getElementById(id)),
    'resources-save-btn', 'resources-dirty');
  registerSettingsSection('hibernate',
    () => [document.getElementById('hibernate-custom-minutes-mode'),
           document.getElementById('hibernate-custom-minutes'),
           document.getElementById('min-warm-replicas')],
    'hibernate-save-btn', 'hibernate-dirty');
  registerSettingsSection('scaling',
    () => [
      document.getElementById('scaling-replicas'),
      document.getElementById('scaling-cap'),
      document.getElementById('scaling-cap-mode'),
      document.getElementById('worker-isolation'),
      document.getElementById('worker-grouped-size'),
      document.getElementById('worker-max-workers'),
      document.getElementById('worker-warm-spares'),
    ],
    'scaling-save-btn', 'scaling-dirty');
  registerSettingsSection('render',
    () => [document.getElementById('render-seconds'), document.getElementById('render-seconds-mode')],
    'render-save-btn', 'render-dirty');
  registerSettingsSection('autoscale',
    () => [document.getElementById('autoscale-enabled'),
           document.getElementById('autoscale-min'),
           document.getElementById('autoscale-max'),
           document.getElementById('autoscale-target-mode'),
           document.getElementById('autoscale-target')],
    'autoscale-save-btn', 'autoscale-dirty');
  for (const [id, section] of Object.entries({
    'resources-memory': 'resources', 'resources-cpu': 'resources', 'scaling-cap': 'scaling',
    'hibernate-custom-minutes': 'hibernate', 'autoscale-target': 'autoscale', 'render-seconds': 'render',
  })) installDirectSetting(document, id, () => {
    setError(document.getElementById(`${section}-error`), '');
    setHidden(document.getElementById(`${section}-status`), true);
    recomputeDirty(section);
    if (section === 'autoscale') updateAutoscaleCeiling();
    if (section === 'scaling') updateScalingCeiling();
    if (section === 'resources') updateWorkerCapacity();
    if (section === 'render') updateRenderPacingAdvice();
    if (section === 'hibernate') refreshKeepWarmNote();
  });
  registerSettingsSection('visibility',
    () => [...document.querySelectorAll('input[name="access-level"]')],
    'visibility-save-btn', 'visibility-dirty');

  async function addMember(event) {
    event.preventDefault();
    const grantBtn = document.getElementById('grant-btn');
    if (grantBtn.disabled || !settingsSlug) return;
    const slug = settingsSlug, revision = accessPanelRevision;
    const nameEl = document.getElementById('grant-username');
    const username = nameEl.value.trim();
    const errEl = document.getElementById('grant-error');
    setError(errEl, '');
    if (!username) { setError(errEl, 'Enter a username.'); nameEl.focus(); return; }
    const finishWrite = beginAccessWrite('members', slug, revision);
    grantBtn.disabled = true;
    grantBtn.textContent = 'Adding…';
    try {
      // Omit role: additive POST preserves an existing manager's role.
      const resp = await api(`/api/apps/${encodeURIComponent(slug)}/members`, {
        method: 'POST', signal: AbortSignal.timeout(20000), body: JSON.stringify({ username }),
      });
      if (!accessRequestCurrent(slug, revision)) return;
      if (resp.status === 401) { await handleUnauthorized(); return; }
      if (!resp.ok) {
        setError(errEl, resp.status === 404 ? 'Username not found. Check the spelling and try again.' : 'Could not add this member. Refresh to check their access, then try again.');
        return;
      }
      // Keep a newer draft if it was entered while this request was pending.
      if (nameEl.value.trim() === username) {
        nameEl.value = '';
        document.getElementById('member-add').open = false;
        document.querySelector('#member-add > summary').focus();
      }
      finishWrite();
      await refreshMemberList();
      if (accessRequestCurrent(slug, revision)) flashToast(`Added ${username} to members`, 'success');
    } catch {
      if (accessRequestCurrent(slug, revision)) setError(errEl, 'Could not add this member. Refresh to check its access before trying again.');
    } finally {
      finishWrite();
      if (accessRequestCurrent(slug, revision)) {
        grantBtn.disabled = false;
        grantBtn.textContent = 'Add member';
      }
    }
  }
  document.getElementById('members-refresh').addEventListener('click', refreshMemberList);
  document.getElementById('groups-refresh').addEventListener('click', refreshGroupAccessList);
  document.getElementById('member-add-form').addEventListener('submit', addMember);

  async function addGroupAccess(event) {
    event.preventDefault();
    const button = document.getElementById('group-access-add-btn');
    if (button.disabled || !settingsSlug) return;
    const slug = settingsSlug, revision = accessPanelRevision;
    const nameEl = document.getElementById('group-access-name');
    const roleEl = document.getElementById('group-access-role');
    const errEl = document.getElementById('group-access-error');
    const group = nameEl.value.trim(), role = roleEl.value;
    setError(errEl, '');
    if (!group) { setError(errEl, 'Enter a group name.'); nameEl.focus(); return; }
    const existing = [...document.querySelectorAll('#group-access-list li[data-group]')].find(row => row.dataset.group === group);
    if (existing) {
      const roleSelect = existing.querySelector('select');
      setError(errEl, roleSelect ? 'This group already has a grant. Change its role in the list.' : 'This group already has a grant. Edit shinyhub.toml and deploy to change it.');
      roleSelect?.focus();
      return;
    }
    const finishWrite = beginAccessWrite('group-access', slug, revision);
    button.disabled = true;
    button.textContent = 'Adding…';
    try {
      const resp = await api(`/api/apps/${encodeURIComponent(slug)}/group-access`, {
        method: 'POST', signal: AbortSignal.timeout(20000), body: JSON.stringify({ group, role }),
      });
      if (!accessRequestCurrent(slug, revision)) return;
      if (resp.status === 401) { await handleUnauthorized(); return; }
      if (!resp.ok) { setError(errEl, 'Could not add this group. Refresh to check its access, then try again.'); return; }
      if (nameEl.value.trim() === group && roleEl.value === role) {
        nameEl.value = '';
        roleEl.value = 'viewer';
        document.getElementById('group-add').open = false;
        document.querySelector('#group-add > summary').focus();
      }
      finishWrite();
      if (!await reconcileGroupPermissions(slug, revision)) return;
      await refreshGroupAccessList();
      if (!accessRequestCurrent(slug, revision)) return;
      flashToast(`Saved group grant for ${group}`, 'success');
      const warn = resp.headers.get('X-ShinyHub-Warning');
      if (warn) flashToast(warn, 'info');
    } catch {
      if (accessRequestCurrent(slug, revision)) setError(errEl, 'Could not add this group. Refresh to check its access before trying again.');
    } finally {
      finishWrite();
      if (accessRequestCurrent(slug, revision)) {
        button.disabled = false;
        button.textContent = 'Add group';
      }
    }
  }
  document.getElementById('group-add-form').addEventListener('submit', addGroupAccess);

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

  function resetNewAppModal() {
    newAppForm.hidden = false;
    newAppStep.textContent = 'Step 1 of 2 · App details';
    newAppSlug.value = '';
    newAppName.value = '';
    newAppProject.value = '';
    if (newAppOptional) newAppOptional.open = false;
    setError(newAppError, '');
    newAppSubmit.disabled = false;
    newAppSubmit.textContent = 'Create app';
    slugEdited = false;
    newAppSlug.removeAttribute('aria-invalid');
    updateNewAppURL();
  }

  function updateNewAppURL() {
    document.getElementById('new-app-url').textContent = `/app/${newAppSlug.value || '<slug>'}/`;
  }

  function openNewAppModal(projectSlug = '') {
    resetNewAppModal();
    newAppProject.value = projectSlug;
    if (projectSlug && newAppOptional) newAppOptional.open = true;
    const firstApp = !state.apps || state.apps.length === 0;
    newAppHeading.textContent = firstApp ? 'Deploy your first app' : 'New app';
    newAppModal.hidden = false;
    modalTrap(newAppModal).activate();
    newAppName.focus();
  }

  function closeNewAppModal(force = false) {
    if (force !== true && isInFlight(newAppSubmit)) return;
    newAppModal.hidden = true;
    modalTrap(newAppModal).release();
    resetNewAppModal();
  }

  function beginFirstDeployment(app) {
    closeNewAppModal(true);
    openDeployModal(app, { firstDeploy: true });
  }

  async function submitNewApp(event) {
    event.preventDefault();
    if (newAppForm.hidden || isInFlight(newAppSubmit)) return;
    setError(newAppError, '');
    newAppSlug.removeAttribute('aria-invalid');

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

    await runInFlight(newAppSubmit, async () => {
      const version = sessionVersion;
      newAppForm.setAttribute('aria-busy', 'true');
      for (const control of newAppForm.elements) control.disabled = true;
      newAppClose.disabled = true;
      newAppSubmit.textContent = 'Creating…';
      try {
        let resp;
        try {
          resp = await api('/api/apps', {
            method: 'POST',
            body: JSON.stringify({ slug, name, project_slug: projectSlug }),
          });
        } catch {
          if (version === sessionVersion) setError(newAppError, 'Could not create the app. Check your connection and try again.');
          return;
        }
        if (version !== sessionVersion) return;
        if (resp.status === 401) { await handleUnauthorized(); return; }
        if (resp.status === 409) {
          setError(newAppError, 'An app already uses this slug. Choose another slug.');
          newAppSlug.setAttribute('aria-invalid', 'true');
          return;
        }
        if (resp.status === 403) {
          setError(newAppError, 'You do not have permission to create apps.');
          return;
        }
        if (!resp.ok) {
          let message = 'Failed to create app.';
          try {
            const body = await resp.json();
            if (body && body.error) message = body.error;
          } catch { /* non-JSON response */ }
          setError(newAppError, message);
          return;
        }

        let createdApp = { slug, name };
        try {
          const body = await resp.json();
          if (body && typeof body.slug === 'string' && body.slug) {
            createdApp = body;
          }
        } catch { /* the submitted values are a complete fallback */ }
        if (version !== sessionVersion) return;
        await loadApps();
        if (version === sessionVersion) beginFirstDeployment(createdApp);
      } finally {
        newAppForm.removeAttribute('aria-busy');
        for (const control of newAppForm.elements) control.disabled = false;
        newAppClose.disabled = false;
        newAppSubmit.textContent = 'Create app';
      }
    });
  }

  function formatBytes(n) {
    if (n < 1024) return `${n} B`;
    if (n < 1048576) return `${(n / 1024).toFixed(1)} KB`;
    return `${(n / 1048576).toFixed(1)} MB`;
  }

  function resetDeployModal() {
    document.getElementById('deploy-monitor').hidden = true;
    deployModalClose.setAttribute('aria-label', 'Close deploy modal');
    document.getElementById('deploy-cli-setup').open = false;
    document.getElementById('deploy-cli-setup-copy').textContent = 'Copy setup commands';
    document.getElementById('deploy-cli-setup-status').textContent = '';
    document.getElementById('deploy-upload-fallback').open = false;
    deployErrorLogs.hidden = true;
    document.getElementById('deploy-success-logs').hidden = true;
    document.getElementById('deploy-step').hidden = true;
    document.getElementById('deploy-step').textContent = 'Step 2 of 2 · Deploy code';
    document.getElementById('deploy-upload-state').hidden = true;
    document.querySelector('.deploy-cli-guide').hidden = false;
    document.getElementById('deploy-cli-setup').hidden = false;
    document.getElementById('deploy-upload-fallback').hidden = false;
    document.querySelector('.deploy-overview-link').hidden = false;
    document.getElementById('deploy-created').hidden = true;
    deployPick.disabled = deployFileInput.disabled = false;
    deploySummary.hidden = true;
    deployIgnoredRow.hidden = true;
    deployProgressWrap.hidden = true;
    deployProgressBar.value = 0;
    deployProgressBar.setAttribute('aria-label', 'Upload progress');
    deployProgressText.textContent = '0%';
    deployResult.hidden = true;
    deployResultList.innerHTML = '';
    setError(deployError, '');
    deploySubmit.disabled = true;
    deploySubmit.textContent = 'Deploy';
    deployCancel.textContent = 'Cancel';
    deployFileInput.value = '';
    deployDropzone.classList.remove('dragover');
    // Closing the dialog detaches the viewer. Once submitted, the server owns
    // deployment; aborting the request would not cancel that work.
    deployState = null;
  }

  function updateDeployMonitor(message) {
    const monitor = document.getElementById('deploy-monitor');
    monitor.hidden = !deployState;
    document.getElementById('deploy-monitor-text').textContent = deployState ? `${deployState.appName || deployState.slug}: ${message}` : '';
  }

  function revealDeployModal() {
    deployModal.hidden = false;
    modalTrap(deployModal).activate();
    (deployState?.uploading ? deployCancel : deploySubmit).focus();
  }
  document.getElementById('deploy-monitor-open').addEventListener('click', revealDeployModal);

  function openDeployModal(app, { firstDeploy = false } = {}) {
    if (deployState?.uploading) {
      revealDeployModal();
      return;
    }
    resetDeployModal();
    deployState = { slug: app.slug, appName: app.name, blob: null, fileCount: 0, rejections: new Map(), xhr: null };
    deployAppName.textContent = app.name ? `: ${app.name}` : '';
    renderDeployCliSnippet(app.slug);
    document.getElementById('deploy-step').hidden = !firstDeploy;
    document.getElementById('deploy-created').hidden = !firstDeploy;
    document.getElementById('deploy-overview').href = `/apps/${encodeURIComponent(app.slug)}`;
    deployModal.hidden = false;
    modalTrap(deployModal).activate();
    deployCliCopy.focus();
  }

  function renderDeployCliSnippet(slug) {
    deployCliSnippet.textContent = deployCommands(window.location.origin, slug);
    document.getElementById('deploy-cli-setup-snippet').textContent = cliSetupCommands(window.location.origin);
  }

  function closeDeployModal() {
    if (!deployState?.uploading) resetDeployModal();
    deployModal.hidden = true;
    modalTrap(deployModal).release();
  }

  // Every selection owns its asynchronous preparation. A replaced file or a
  // closed dialog must never restore an earlier bundle or enable its upload.
  function beginDeploySelection() {
    const selection = deployState;
    const version = selection.selectionVersion = (selection.selectionVersion || 0) + 1;
    selection.blob = null;
    selection.completed = false;
    selection.needsNewBundle = false;
    selection.uncertain = false;
    document.getElementById('deploy-upload-state').hidden = false;
    document.querySelector('.deploy-cli-guide').hidden = true;
    document.getElementById('deploy-cli-setup').hidden = true;
    deploySubmit.disabled = true;
    deploySubmit.textContent = 'Deploy';
    deploySummary.hidden = true;
    deployProgressWrap.hidden = true;
    deployResult.hidden = true;
    deployErrorLogs.hidden = true;
    document.getElementById('deploy-success-logs').hidden = true;
    setError(deployError, '');
    return () => deployState === selection && selection.selectionVersion === version;
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
      setError(deployError, `Bundle is ${formatBytes(blobSize)}, which exceeds the ${mib} MiB upload limit. Use the CLI for larger bundles.`);
      deploySubmit.disabled = true;
      return;
    }
    setError(deployError, '');
    deploySubmit.disabled = false;
  }

  async function acceptZipFile(file) {
    const current = beginDeploySelection();
    // Pre-built .zip — counting files would require parsing the central
    // directory, so leave the file count unknown and render it as "—".
    let rules;
    try {
      rules = await loadBundleRules();
    } catch (err) {
      if (!current()) return;
      console.error('loadBundleRules failed:', err);
      setError(deployError, 'Failed to load bundle rules from server.');
      return;
    }
    if (!current()) return;
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
      if (!deployState || deployState.uploading) return;
      const file = deployFileInput.files && deployFileInput.files[0];
      if (!file) return;
      if (!file.name.toLowerCase().endsWith('.zip')) {
        beginDeploySelection();
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
    if (!deployState || deployState.uploading) return;
    beginDeploySelection();
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
    const current = beginDeploySelection();
    deploySourceName.textContent = rootEntry.name + '/';
    deployFileCount.textContent = 'Reading…';
    deployBundleSize.textContent = '—';
    deploySummary.hidden = false;

    let rules;
    try {
      rules = await loadBundleRules();
    } catch (err) {
      if (!current()) return;
      console.error('loadBundleRules failed:', err);
      setError(deployError, 'Failed to load bundle rules from server.');
      return;
    }
    if (!current()) return;

    const rejections = new Map();
    const fileList = [];
    try {
      for await (const item of walkFolder(rootEntry, rules, rejections)) fileList.push(item);
    } catch (err) {
      if (!current()) return;
      console.error('walkFolder failed:', err);
      setError(deployError, 'Failed to read folder contents.');
      return;
    }
    if (!current()) return;

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
      if (!current()) return;
      console.error('zip build failed:', err);
      setError(deployError, 'Failed to build zip archive.');
      return;
    }
    if (!current()) return;
    deployState.blob = blob;
    deployState.fileCount = fileList.length;
    deployState.rejections = rejections;
    renderDeploySummary(rootEntry.name + '/', blob.size, fileList.length, rejections, rules);
  }

  function deployErrorMessage(error) {
    if (error.uncertain) return error.message;
    if (/bundle rejected.*(?:valid ZIP|damaged)|upload a valid ZIP bundle/i.test(error.message || '')) {
      return 'This ZIP bundle is invalid or damaged. Choose a new ZIP file or upload the app folder, then deploy again.';
    }
    if (error.deployment?.failure_kind === 'capacity') {
      return 'Not enough capacity to deploy. The current version is preserved. Free capacity, then retry; this attempt will not retry automatically.';
    }
    if (/no app entrypoint found/i.test(error.message || '')) {
      return 'No app entrypoint found. Include app.py, app.R, or ui.R and server.R, or set [app] command in shinyhub.toml. Choose an updated bundle and deploy again.';
    }
    if (/exited during startup/i.test(error.message || '')) {
      return 'The app exited during startup. View app logs to find the error, then choose an updated bundle and deploy again.';
    }
    if (!error.status) return 'Could not confirm deployment. Check your connection and the app overview before retrying; the server may still be deploying.';
    return error.message || 'Deployment failed. Check the app overview and try again.';
  }

  function uploadBundle(slug, blob) {
    const upload = deployState;
    const transfer = uploadDeployment({ slug, blob, csrf: readCookie('csrf_token'),
      onUpload: percent => {
        if (deployState !== upload || upload.serverPhase) return;
        deployProgressBar.value = percent;
        deployProgressText.textContent = `Uploading · ${percent}%`;
      },
      onPhase: event => {
        if (deployState !== upload) return;
        upload.serverPhase = true;
        deployProgressBar.removeAttribute('value');
        deployProgressBar.setAttribute('aria-label', 'Deployment progress');
        if (event.message) {
          deployProgressText.textContent = event.message;
          updateDeployMonitor(event.message);
        }
      },
    });
    upload.xhr = transfer.xhr;
    return transfer.promise;
  }

  deploySubmit.addEventListener('click', async () => {
    if (!deployState || !deployState.blob || deployState.uploading) return;
    if (deployState.uncertain) {
      const slug = deployState.slug;
      closeDeployModal();
      router.navigate(`/apps/${encodeURIComponent(slug)}/deployments`);
      return;
    }
    if (deployState.needsNewBundle) {
      deployFileInput.click();
      return;
    }

    // A successful first deployment ends at the app's health overview.
    if (deployState.completed) {
      const slug = deployState.slug;
      closeDeployModal();
      router.navigate(`/apps/${encodeURIComponent(slug)}`);
      return;
    }

    setError(deployError, '');
    deploySubmit.disabled = true;
    deploySubmit.textContent = 'Deploying…';
    deployErrorLogs.hidden = true;
    deployCancel.textContent = 'Hide progress';
    deployModalClose.setAttribute('aria-label', 'Hide deployment progress');
    deployProgressWrap.hidden = false;
    deployProgressBar.value = 0;
    deployProgressBar.setAttribute('aria-label', 'Upload progress');
    deployProgressText.textContent = '0%';

    const { slug, blob } = deployState;
    const upload = deployState;
    const selectionVersion = upload.selectionVersion;
    const version = sessionVersion;
    upload.uploading = true;
    upload.serverPhase = false;
    updateDeployMonitor('Uploading…');
    deployPick.disabled = deployFileInput.disabled = true;

    let body;
    try {
      body = await uploadBundle(slug, blob);
    } catch (err) {
      if (deployState !== upload) return;
      if (err.code === 'UPLOAD_CANCELLED') {
        return; // closeDeployModal resets state
      }
      if (err.status === 401) { await handleUnauthorized(); return; }
      upload.uncertain = !!err.uncertain;
      setError(deployError, deployErrorMessage(err));
      updateDeployMonitor(upload.uncertain ? 'Deployment needs checking.' : 'Deployment did not complete.');
      upload.needsNewBundle = [400, 413, 422].includes(err.status) && /bundle|entrypoint/i.test(err.message || '');
      deployErrorLogs.hidden = upload.needsNewBundle || err.status === 403 || err.deployment?.failure_kind === 'capacity' || /no app entrypoint found/i.test(err.message || '');
      deployProgressWrap.hidden = true;
      deploySubmit.disabled = false;
      deploySubmit.textContent = upload.uncertain ? 'Check deployment' : upload.needsNewBundle ? 'Choose another bundle' : 'Retry deployment';
      if (!deployModal.hidden) deploySubmit.focus();
      deployCancel.textContent = 'Close';
      if (version === sessionVersion) await loadApps().catch(() => {});
      return;
    } finally {
      if (deployState === upload) {
        upload.uploading = false;
        upload.xhr = null;
        deployModalClose.setAttribute('aria-label', 'Close deploy modal');
        deployPick.disabled = deployFileInput.disabled = false;
      }
    }
    if (version !== sessionVersion) return;
    // A closed viewer still owns a real deployment. Refresh the index when it
    // finishes so a first-deploy card becomes usable without a manual reload.
    await loadApps().catch(() => {});
    if (deployState !== upload || upload.selectionVersion !== selectionVersion) return;
    deployProgressText.textContent = 'Deployed';

    const summaryLines = [...deploymentOutcome(body).details.filter(line => line !== body.warning), ...formatManifestSummary(body && body.manifest)];
    renderDeployResult(deployResult, deployResultList, summaryLines);
    const resultHeading = deployResult.querySelector('.deploy-result-heading');
    const resultDescription = document.getElementById('deploy-result-description');
    const status = body?.status || body?.app?.status;
    if (body.kept_stopped || status === 'stopped') {
      resultHeading.textContent = 'Code deployed. App is stopped.';
      resultDescription.textContent = 'The new version is saved. Start the app from its overview when you’re ready.';
    } else if (body.kept_hibernated || status === 'hibernated') {
      resultHeading.textContent = 'Code deployed. App is sleeping.';
      resultDescription.textContent = 'The new version is saved. Open the app from its overview to wake it.';
    } else if (status === 'running' || status === 'idle') {
      resultHeading.textContent = 'App is ready.';
      resultDescription.textContent = 'Your code is deployed and the app has started.';
    } else {
      resultHeading.textContent = 'Deployment complete.';
      resultDescription.textContent = 'Review the app overview for its current health.';
    }
    const warning = document.getElementById('deploy-warning');
    warning.textContent = typeof body.warning === 'string' ? body.warning : '';
    warning.hidden = !warning.textContent;
    deployResultList.hidden = summaryLines.length === 0;
    deployProgressWrap.hidden = true;
    deployState.completed = true;
    document.getElementById('deploy-step').textContent = 'Deployment complete';
    document.getElementById('deploy-created').hidden = true;
    document.getElementById('deploy-upload-fallback').hidden = true;
    document.querySelector('.deploy-overview-link').hidden = true;
    deploySubmit.disabled = false;
    deploySubmit.textContent = 'View app';
    document.getElementById('deploy-success-logs').hidden = false;
    updateDeployMonitor(resultHeading.textContent);
    if (!deployModal.hidden) resultHeading.focus();
  });

  document.getElementById('deploy-success-logs').addEventListener('click', () => {
    if (!deployState) return;
    const slug = deployState.slug; closeDeployModal(); openLogs(slug);
  });
  document.getElementById('deploy-overview').addEventListener('click', closeDeployModal);
  deployModalClose.addEventListener('click', closeDeployModal);
  deployErrorLogs.addEventListener('click', () => {
    if (!deployState) return;
    const slug = deployState.slug;
    closeDeployModal();
    openLogs(slug);
  });
  deployCancel.addEventListener('click', closeDeployModal);
  deployModal.addEventListener('click', (e) => {
    if (e.target === e.currentTarget) closeDeployModal();
  });
  bindDropzoneEvents();

  newAppButton.addEventListener('click', () => openNewAppModal());
  emptyStateCTA.addEventListener('click', () => openNewAppModal());
  newAppClose.addEventListener('click', closeNewAppModal);
  newAppCancel.addEventListener('click', closeNewAppModal);
  newAppModal.addEventListener('click', e => {
    if (e.target === e.currentTarget) closeNewAppModal();
  });
  newAppForm.addEventListener('submit', submitNewApp);

  function closeProjectEditModal() {
    if (projectEditSave.disabled) return;
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
    updateNewAppURL();
  });
  newAppSlug.addEventListener('input', () => {
    slugEdited = newAppSlug.value.length > 0;
    updateNewAppURL();
  });
  document.getElementById('deploy-cli-setup-copy').addEventListener('click', async (event) => {
    const button = event.currentTarget;
    const status = document.getElementById('deploy-cli-setup-status');
    try {
      await navigator.clipboard.writeText(document.getElementById('deploy-cli-setup-snippet').textContent);
      button.textContent = 'Copied';
      status.textContent = 'Setup commands copied to clipboard.';
    } catch {
      status.textContent = 'Could not copy. Select the setup commands and copy them manually.';
    }
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

  document.querySelector('.login-recovery')?.addEventListener('click', async (event) => {
    const button = event.currentTarget;
    button.disabled = true;
    try {
      const destination = await reconnectForwardAuth(api, loginProviders);
      suppressUnloadGuard = true;
      if (destination === 'upstream') window.location.assign('/login');
      else window.location.reload();
    } catch (error) {
      setError(loginError, error.message);
      button.disabled = false;
    }
  });

  loginForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (loginPending) return;
    loginPending = true;
    loginForm.setAttribute('aria-busy', 'true');
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

      let payload;
      try { payload = await response.json(); } catch {}
      if (!payload?.user) {
        setError(loginError, 'The server returned an incomplete response. Try signing in again.');
        return;
      }
      showLoggedIn(payload);
      passwordInput.value = '';
      passwordVisibility.reset(loginForm);
      // Mirror the bootstrap path: await router.start() so state.apps is
      // populated before handleDeployHash consumes the persisted slug.
      // Without the await + handleDeployHash call here, a logged-out user
      // who landed on /#deploy=<slug> would persist the slug, log in, and
      // then never get the deploy modal (the bootstrap path doesn't run on
      // an interactive login).
      if (await startAuthenticatedRouter(router)) return;
      await handleDeployHash();
      await restorePendingSupportSession();
    } finally {
      loginPending = false;
      loginForm.removeAttribute('aria-busy');
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

  const accountLogout = document.getElementById('account-logout');
  accountLogout.addEventListener('click', () => logoutButton.click());
  logoutButton.addEventListener('click', async () => {
    if (logoutButton.disabled || !confirmDiscardIfDirty()) return;
    logoutButton.disabled = accountLogout.disabled = true;
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
      logoutButton.disabled = accountLogout.disabled = false;
      flashToast('Logout failed: network error', 'error');
      return;
    }
    if (resp.ok || resp.status === 401) {
      sessionController.end();
      // Land on the front door. With auth-aware `/`, a now-sessionless request to
      // `/` serves the branding landing page when configured, otherwise the SPA
      // shell falls through to the login view - so a full navigation does the
      // right thing in both cases without the client knowing the branding config.
      suppressUnloadGuard = true;
      window.location.assign(logoutTarget(loginProviders));
      return;
    }
    logoutButton.disabled = accountLogout.disabled = false;
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
        collapseBtn.title = label;
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

  let loginProviders = null;
  function updateGroupAccessProviderHelp(data) {
    const element = document.getElementById('group-access-sso-warning');
    if (!element) return;
    const warning = groupAccessWarningText(data);
    element.textContent = warning;
    element.hidden = !warning;
  }
  async function loadProviders() {
    try {
      const resp = await api('/api/auth/providers');
      if (!resp.ok) { updateGroupAccessProviderHelp(); return; }
      const data = await resp.json();
      loginProviders = data;
      // Reveal only the SSO buttons the server reports configured; the GitHub and
      // Google buttons start hidden in index.html, so a failed fetch or a
      // native-only server never shows a dead button (see login-providers.js).
      applyLoginProviders(document, data);
      updateGroupAccessProviderHelp(data);
    } catch (e) { updateGroupAccessProviderHelp(); }
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
      // Keep the card live so one opened while an app was hibernating or
      // deploying reflects the transition (m.status is the live app-level status
      // from /metrics; m.deploying flags an executing deploy; m.replicas gives
      // the facts line its readiness count). The index stays scan-first:
      // release/readiness here, detailed CPU/RAM on the app overview.
      syncCardFromModel(slug, m);
      // Detail header (only when the detail view for this slug is visible).
      const detailView = document.getElementById('app-detail-view');
      if (!detailView.hidden && detailApp?.slug === slug && (location.pathname === `/apps/${slug}` || location.pathname.startsWith(`/apps/${slug}/`))) {
        // Keep the header status pill live too, on the same model merge the
        // card badge uses, so an open detail page flips to "Deploying" and
        // back during a deploy instead of freezing at its load-time state.
        const pillEl = document.getElementById('app-detail-status');
        if (pillEl && detailApp && detailApp.slug === slug) {
          updateStatusPill(pillEl, detailApp, m, formatStatus);
          // Same ordering as syncCardFromModel: the pill merge refreshes
          // detailApp.status, so the header kebab has to follow it.
          syncDetailHeaderActions(detailApp);
        }
        if (m.autoscale_status) detailLastEnvelope.autoscale_status = m.autoscale_status;
        if (Number.isInteger(m.effective_autoscale_max_replicas)) {
          detailLastEnvelope.effective_autoscale_max_replicas = m.effective_autoscale_max_replicas;
        }
        const liveApp = detailApp ? { ...detailApp,
          replicas: Number.isInteger(m.replicas_desired) ? m.replicas_desired : detailApp.replicas,
          worker_isolation: m.worker_isolation || detailApp.worker_isolation,
          effective_worker_isolation: m.worker_isolation || detailApp.effective_worker_isolation,
        } : null;
        const autoscaleState = summariseAutoscale(liveApp, detailLastEnvelope);
        renderDetailCapacity(document, m, detailApp?.replicas ?? 1, autoscaleState);
        renderReplicasPanel(m);
        appDetailMount.onLiveMetrics?.(slug, m);

        // Keep the stored envelope in sync with autoscale_status from the poll
        // so renderAutoscaleSummary's cooldown row reflects the latest event
        // without requiring a full GET /api/apps/:slug refetch.
        const autoscaleDl = document.getElementById('autoscale-summary');
        if (autoscaleDl && detailLastEnvelope.autoscale_status && liveApp) {
          renderAutoscaleSummary(autoscaleDl, autoscaleState);
        }
      }
    },
    // A failed background metrics poll used to fail silently — no onError was
    // wired, so the CPU/RAM line just stopped updating with no visible signal.
    // A 401 specifically means the session died while the dashboard was open
    // in the background; log the user out instead of polling a dead session
    // forever. metrics-controller.js reports a non-2xx as `Error('status N')`.
    onError: (slug, err) => {
      if (detailApp?.slug === slug && (location.pathname === `/apps/${slug}` || location.pathname.startsWith(`/apps/${slug}/`))) {
        markDetailCapacityError(document);
        appDetailMount.onMetricsError?.(slug);
      }
      if (err && /status 401/.test(err.message)) {
        handleUnauthorized();
      }
    },
  });

  document.getElementById('app-detail-metrics-retry')?.addEventListener('click', async event => {
    const button = event.currentTarget;
    button.dataset.restoreFocus = 'true';
    button.disabled = true;
    button.textContent = 'Checking…';
    document.getElementById('app-detail-metrics-message').textContent = 'Checking live metrics…';
    await metrics.refresh();
    button.disabled = false;
    button.textContent = 'Retry metrics';
    if (!button.hidden) button.focus({ preventScroll: true });
    delete button.dataset.restoreFocus;
  });

  function renderReplicasPanel(m) {
    const listEl = document.getElementById('overview-replicas-list');
    const capEl = document.getElementById('overview-replicas-cap');
    if (!listEl || !capEl) return;
    const cap = Number(m.sessions_cap || 0);
    const elastic = ['grouped', 'per_session'].includes(m.worker_isolation);
    const unit = elastic ? 'worker' : 'replica';
    const heading = listEl.closest('.overview-replicas')?.querySelector('h2');
    if (heading) heading.textContent = elastic ? 'Workers' : 'Replicas';
    capEl.textContent = cap > 0 ? `(cap ${cap} sessions/${unit})` : '(uncapped)';
    const replicas = Array.isArray(m.replicas) ? m.replicas : [];
    if (replicas.length === 0) {
      listEl.innerHTML = `<li class="replicas-empty">No ${unit}s tracked yet.</li>`;
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
      const { cpuText, cpuNote, ramText, physicalText, attributionNote, note } = metricsText(r);
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
          <span class="replica-measure replica-cpu"${cpuNote ? ` title="${escapeHtml(cpuNote)}"` : ''}><span class="replica-measure-label">CPU</span><span class="replica-measure-value">${cpuDisplay}</span></span>
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
    if (routeErrorView) { routeErrorView.hidden = false; document.getElementById('route-error-heading')?.focus({ preventScroll: true }); }
  }
  function clearRouteError() {
    if (routeErrorView) routeErrorView.hidden = true;
  }
  let announcementEditor = null;
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
  appContextNavigation = installAppContextNavigation({ document, navigate: (path) => router.navigate(path) });
  router.setNavGuard(confirmDiscardIfDirty);
  window.addEventListener('beforeunload', (e) => {
    if (!suppressUnloadGuard && (anySettingsDirty() || (!profileModal.hidden && (profileHasChanges() || profileSaving || photoPreparing)) || announcementEditor?.isDirty() || announcementEditor?.isPending() || tokenManager.isPending() || tokenManager.needsSave() || deployState?.uploading)) { e.preventDefault(); e.returnValue = ''; }
  });

  function updateActiveNav(pathname) {
    sidebarAccount.close();
    syncAppContext(pathname);
    // Only global navigation owns global active state. Content links such as
    // app cards and detail tabs manage their own state.
    for (const el of document.querySelectorAll('#primary-nav .nav-item[data-nav], #administration-nav .nav-item[data-nav]')) {
      const url = new URL(el.href);
      const active = isPrimaryNavActive(url.pathname, pathname, state.user && state.user.role);
      el.classList.toggle('tab-active', active);
      el.classList.toggle('is-parent', el.id === 'tab-apps' && pathname.startsWith('/apps/'));
      if (active) el.setAttribute('aria-current', 'page'); else el.removeAttribute('aria-current');
    }
    // Sidebar app rows own their active state separately (slug-prefix, nested
    // tabs). Runs on every mount (post-allowed-navigation), and also closes the
    // mobile drawer there so a guard-vetoed navigation keeps it open.
    // A large fleet renders only the first page of each sidebar group, so an
    // app opened from the grid or search can sit beyond it; rebuilding
    // renders the window that contains the active row.
    if (!highlightSidebarApp(document.getElementById('sidebar-apps'), pathname)) {
      const routeSlug = pathname.match(/^\/apps\/([^/]+)/);
      if (routeSlug && state.apps && state.apps.some((a) => a.slug === decodeURIComponent(routeSlug[1]))) {
        syncSidebar();
      }
    }
    if (sidebarDrawer) sidebarDrawer.onNavigated();
    // Quick-view log pane: same reasoning as the drawer above. A vetoed
    // navigation never mounts, so a pane the operator is actively reading
    // stays open; an allowed one closes it (and its EventSource) rather than
    // leaving it streaming over whatever page comes next.
    logPaneController.onNavigated();
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
    setSettingsSlug: (slug) => { if (settingsSlug !== slug) { settingsRevision++; accessPanelRevision++; } settingsSlug = slug; },
    clearSettingsDrafts: () => { closeEnvForm(true); clearSettingsDirty(); settingsSlug = null; settingsRevision++; },
    populateGeneralTab,
    populateAutoscaleTab,
    populateAccessPanel,
    refreshEnvList,
    loadSchedules,
    refreshMemberList,
    refreshGroupAccessList,
    // setDetailEnvelope is called by renderOverview in app-detail.js to keep
    // the stored envelope current so onMetrics can refresh the autoscale
    // summary on each 10s poll without a full GET /api/apps/:slug refetch.
    setDetailEnvelope: (env) => { detailLastEnvelope = env; },
    // setDetailApp records the app currently shown on the detail page so the
    // static header kebab (wired once below) knows which app to act on, and
    // renders the header's icon/monogram avatar for that app.
    setDetailApp: (app) => { detailApp = app; renderDetailHeaderAvatar(app); syncDetailHeaderActions(app); syncAppContext(); },
    // restart triggers POST /api/apps/:slug/restart (the same action as the
    // header kebab), exposed so the crash banner's Restart button can reuse it.
    restart: (slug, button) => restart(slug, button),
    start: (slug, button) => startApp(slug, button),
    // flashToast lets app-detail.js report failures (e.g. a failed rollback)
    // through the same accessible, auto-dismissing toast used everywhere else
    // in the dashboard instead of a blocking window.alert().
    flashToast,
    openProjectEditModal,
    openNewAppModal,
  };

  const appDetailMount = mountAppDetail({
    ...ctx,
    openDeployModal,
  });
  const projectDetailMount = mountProjectDetail(ctx);

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
      ['start', acts.showStart && !acts.startIsPrimary],
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
    const canManage = !!app && canManageApp(state.user, app);
    const acts = app ? appCardActions(app, canManage) : null;
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
    if (menu) menu.hidden = !anyShown && (!canManage || !!app?.deploying);
    // A per-app Viewer reaches this page whenever their global role clears
    // the route guard (developer/operator/admin) even though their member
    // role on this specific app cannot deploy; the API always 403s for them.
    // Hide the button rather than inviting a click that can never succeed.
    const dDeploy = document.getElementById('app-detail-deploy');
    if (dDeploy) {
      dDeploy.hidden = !canManage || !!app?.deploying;
      dDeploy.closest('li').hidden = dDeploy.hidden;
    }
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
        onPick: (emoji) => { if (detailApp) withIconSave(() => setEmojiIcon(detailApp, emoji)); },
      }));
    }
    const eInput = document.getElementById('general-icon-emoji-input');
    const eUseBtn = document.getElementById('general-icon-emoji-use-btn');
    if (eUseBtn) {
      eUseBtn.addEventListener('click', () => {
        if (detailApp && eInput) withIconSave(() => setEmojiIcon(detailApp, eInput.value));
      });
    }
    const eClearBtn = document.getElementById('general-icon-emoji-clear-btn');
    if (eClearBtn) {
      eClearBtn.addEventListener('click', () => { if (detailApp) withIconSave(() => clearEmojiIcon(detailApp)); });
    }
  }

  // Hide every top-level page section before mounting a new view so a
  // sibling view never bleeds through. The previous-view unmount() handles
  // this on SPA transitions, but on a direct page load (e.g. reload on
  // /users) there is no previous view to clean up — the sections inherit
  // whatever showLoggedIn left them in.
  function hideAllPageViews() {
    document.getElementById('announcements-view').hidden = true;
    overviewView.hidden = true;
    launchpadView.hidden = true;
    appsView.hidden = true;
    projectDetailView.hidden = true;
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
  router.register('/projects/:slug', (params) => {
    const role = ctx.state.user && ctx.state.user.role;
    if (appsSurfaceForSession(role, ctx.state.canManageApps) === 'viewer') {
      return ctx.navigate('/apps', { replace: true });
    }
    hideAllPageViews();
    return projectDetailMount(params);
  });
  router.register('/identity', () => {
    const usersAccess = resolveAdminOnlyAccess(ctx.state.user);
    if (usersAccess) return ctx.navigate(usersAccess.path, { replace: usersAccess.replace });
    hideAllPageViews();
    return mountUsers({ ...ctx, loadUsers: () => { showIdentityPanel('people'); return peopleDirectory.start(); }, clearUsers: () => { peopleDirectory.stop(); clearIdentityServices(); } });
  });
  // Preserve old bookmarks without preserving a second navigation concept.
  router.register('/users', () => ctx.navigate('/identity', { replace: true }));
  router.register('/tokens', () => {
    hideAllPageViews();
    if (tokensView) tokensView.hidden = false;
    tokenManager.start();
    // Matches the page's own <h1>, so a tab, a bookmark and a history entry
    // name this page instead of falling back to the bare product name.
    return { title: 'API tokens', unmount() { tokenManager.stop(); if (tokensView) tokensView.hidden = true; } };
  });
  router.register('/announcements', () => {
    if (!ctx.state.canManageAnnouncements) return ctx.navigate('/', { replace: true });
    hideAllPageViews();
    announcementEditor = mountAnnouncements({ ...ctx, onUnmount: () => { announcementEditor = null; } });
    return announcementEditor;
  });
  router.register('/workers', () => {
    const workersAccess = resolveAdminOnlyAccess(ctx.state.user);
    if (workersAccess) return ctx.navigate(workersAccess.path, { replace: workersAccess.replace });
    hideAllPageViews();
    return mountWorkers(ctx);
  });
  router.register('/audit-log', (_, search) => {
    const auditAccess = resolveAuditLogAccess(ctx.state.canReadAudit);
    if (auditAccess) return ctx.navigate(auditAccess.path, { replace: auditAccess.replace });
    hideAllPageViews();
    return mountAuditLog({ ...ctx, loadAuditEvents: auditExplorer.load, stopAudit: auditExplorer.stop }, search);
  }, {key: () => 'audit-log'});
  // All three app-detail patterns are the same page for a given app, so they
  // share a key: moving between them is a tab switch, which the mounted view
  // handles in place (see mountAppDetail's update) rather than being torn down
  // and rebuilt. hideAllPageViews() therefore runs only when the visitor is
  // genuinely arriving from another page. Each pattern names the tab it selects
  // once, for the mount and the update alike.
  const appDetailKey = (p) => 'app-detail:' + p.slug;
  const appDetailRoute = (p) => {
    hideAllPageViews();
    return appDetailMount(p);
  };
  router.register('/apps/:slug', appDetailRoute, {
    key: appDetailKey,
    params: (p) => ({ ...p, tab: 'overview' }),
  });
  router.register('/apps/:slug/schedules/:scheduleId', appDetailRoute, {
    key: appDetailKey,
    params: (p) => ({ ...p, tab: 'schedules' }),
  });
  router.register('/apps/:slug/settings/:section', appDetailRoute, { key: appDetailKey, params: settingsRouteParams });
  router.register('/apps/:slug/:tab', appDetailRoute, { key: appDetailKey });

  async function initialize() {
    try {
      await bootstrap();
    } finally {
      // Whatever the outcome, every request after the boot is a fresh one.
      bootPrefetch.discard();
    }
  }

  async function bootstrap() {
    // Persist any /#deploy=<slug> hash before the auth check so the slug
    // survives the login redirect in case the user is not authenticated.
    persistDeployHash();
    await loadProviders();
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
    if (await startAuthenticatedRouter(router)) return;
    await handleDeployHash();
    await restorePendingSupportSession();
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
    const zone = timezone.trim() || document.getElementById('sched-timezone')?.dataset.defaultTimezone || 'the server timezone';
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
    surface.requests.invalidate();
    surface.historyRequests.invalidate();
    if (surface.timer) clearTimeout(surface.timer);
    if (surface.visibilityHandler) document.removeEventListener('visibilitychange', surface.visibilityHandler);
    mountedScheduleSurface = null;
  }

  function scheduleNextPoll(surface, schedules) {
    if (surface.stopped || surface !== mountedScheduleSurface || surface.accessDenied || document.hidden) return;
    if (surface.timer) clearTimeout(surface.timer);
    const active = schedules.some(s => scheduleState(s).key === 'running' || activationState(s).active);
    surface.timer = setTimeout(() => refreshScheduleSurface(surface), active ? 5000 : 30000);
  }

  function loadSchedules(slug, options) {
    let surface = mountedScheduleSurface;
    if (options) {
      stopScheduleSurface();
      surface = {
        slug,
        options: options || {},
        timer: null,
        stopped: false,
        visibilityHandler: null,
        requests: createScheduleRequestGate(),
        historyRequests: createScheduleRequestGate(),
      };
      surface.visibilityHandler = () => {
        if (!document.hidden && !surface.stopped) refreshScheduleSurface(surface);
        else if (surface.timer) clearTimeout(surface.timer);
      };
      document.addEventListener('visibilitychange', surface.visibilityHandler);
      mountedScheduleSurface = surface;
    }
    // Mutations finishing after navigation must not remount an old app.
    if (!surface || surface.stopped || surface.slug !== slug) return;
    const addBtn = document.getElementById('schedules-add-btn');
    if (addBtn) addBtn.hidden = !surface.options.canManage;
    if (!options) return refreshScheduleSurface(surface, {announce: true});
    document.getElementById('schedules-workspace')?.classList.toggle('has-detail', options.selectedScheduleID != null);
    document.getElementById('schedule-detail').hidden = true;
    document.getElementById('schedules-summary').hidden = true;
    document.getElementById('schedules-list').innerHTML = '<p class="schedule-loading">Loading scheduled jobs…</p>';
    document.getElementById('schedules-error').hidden = true;
    refreshScheduleSurface(surface);
    // Return cleanup before the first request settles.
    return () => stopScheduleSurface(surface);
  }

  function clearScheduleAccess(surface) {
    surface.accessDenied = true;
    surface.requests.invalidate();
    surface.historyRequests.invalidate();
    if (surface.timer) clearTimeout(surface.timer);
    surface.schedules = null;
    surface.renderedDetailID = null;
    surface.renderedDetailSignature = null;
    surface.detailRuns = [];
    surface.detailTotal = 0;
    const list = document.getElementById('schedules-list');
    list.replaceChildren();
    list.setAttribute('aria-busy', 'false');
    const detail = document.getElementById('schedule-detail');
    detail.replaceChildren();
    detail.hidden = true;
    document.getElementById('schedules-summary').hidden = true;
    document.getElementById('schedules-add-btn').hidden = true;
    document.getElementById('schedules-surface-status').textContent = '';
    const refresh = document.getElementById('schedules-refresh-btn');
    refresh.disabled = false;
    refresh.textContent = 'Refresh';
    const error = document.getElementById('schedules-error');
    error.hidden = false;
    error.querySelector('p').textContent = 'Your access to schedules changed. Ask an app manager for access.';
    error.querySelector('button').onclick = () => refreshScheduleSurface(surface, {announce: true});
  }

  async function refreshScheduleSurface(surface, {announce = false} = {}) {
    if (!surface || surface.stopped || surface !== mountedScheduleSurface) return;
    const request = surface.requests.begin();
    const current = () => !surface.stopped && surface === mountedScheduleSurface && surface.requests.isCurrent(request);
    const container = document.getElementById('schedules-list');
    const statusEl = document.getElementById('schedules-surface-status');
    const error = document.getElementById('schedules-error');
    const refresh = document.getElementById('schedules-refresh-btn');
    const focused = document.activeElement;
    const focusedScheduleKey = focused?.dataset?.scheduleFocus || null;
    let keepFocus = !!focusedScheduleKey;
    const focusMoved = event => { if (event.target !== focused && !focused?.contains(event.target)) keepFocus = false; };
    if (!container) return;
    document.addEventListener('focusin', focusMoved);
    document.addEventListener('pointerdown', focusMoved, true);
    if (surface.timer) clearTimeout(surface.timer);
    container.setAttribute('aria-busy', 'true');
    if (refresh) { refresh.disabled = true; refresh.textContent = 'Refreshing…'; }
    if (announce && statusEl) statusEl.textContent = 'Refreshing schedule status…';
    try {
      const resp = await api(`/api/apps/${encodeURIComponent(surface.slug)}/schedules`, {signal: request.signal});
      if (!current()) return;
      if (resp.status === 401) { await handleUnauthorized(); return; }
      if (!resp.ok) throw Object.assign(new Error('response'), {status: resp.status});
      const schedBody = await resp.json();
      if (!current()) return;
      const schedules = Array.isArray(schedBody) ? schedBody : schedBody?.items;
      if (!Array.isArray(schedules)) throw new Error('Invalid schedule response');
      surface.accessDenied = false;
      surface.schedules = schedules;
      error.hidden = true;
      renderScheduleSummary(schedules);
      document.getElementById('schedules-summary').hidden = !schedules.length || surface.options.selectedScheduleID != null;
      renderScheduleList(surface, schedules);
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
      if (!current()) return;
      if (keepFocus && focusedScheduleKey) {
        const replacement = Array.from(document.querySelectorAll('[data-schedule-focus]'))
          .find(el => el.dataset.scheduleFocus === focusedScheduleKey);
        replacement?.focus({preventScroll: true});
      }
    } catch (err) {
      if (!current()) return;
      if (err?.status === 403) { clearScheduleAccess(surface); return; }
      if (!surface.schedules) container.replaceChildren();
      error.hidden = false;
      error.querySelector('p').textContent = err?.status === 403
        ? 'Your access to schedules changed. Ask an app manager for access.'
        : `Could not update schedules. ${surface.schedules ? 'The previous status is still shown. ' : ''}Check your connection and try again.`;
      error.querySelector('button').onclick = () => refreshScheduleSurface(surface, {announce: true});
      if (statusEl) statusEl.textContent = '';
    } finally {
      document.removeEventListener('focusin', focusMoved);
      document.removeEventListener('pointerdown', focusMoved, true);
      if (current()) {
        container.setAttribute('aria-busy', 'false');
        if (refresh) { refresh.disabled = false; refresh.textContent = 'Refresh'; }
        scheduleNextPoll(surface, surface.schedules || []);
      }
    }
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
    set('schedules-summary-total', String(summary.total));
    set('schedules-summary-enabled', String(summary.enabled));
    set('schedules-summary-active', String(summary.activeCount));
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
      const lastRun = s.last_run_at ? scheduleDate(s.last_run_at, tz) : 'No run recorded';
      const lastSuccess = Number.isFinite(s.last_success_age_s) ? `Last success ${formatAge(s.last_success_age_s)}` : 'No successful run recorded';
      const nextFire = s.retired ? 'Retired' : s.enabled === false ? 'Paused' : scheduleDate(s.next_fire, tz);
      const selected = String(s.id) === String(surface.options.selectedScheduleID);
      const actions = surface.options.canManage ? `
        <div class="kebab-menu schedule-actions-menu">
          <button type="button" aria-haspopup="menu" aria-expanded="false" aria-label="More actions for ${escapeHtml(s.name)}">
            <svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="3" cy="8" r="1.25"/><circle cx="8" cy="8" r="1.25"/><circle cx="13" cy="8" r="1.25"/></svg>
          </button>
          <ul class="kebab-list" role="menu" hidden>
            ${s.retired ? '' : `<li role="none"><button type="button" role="menuitem" data-action="run" data-id="${s.id}" data-name="${escapeHtml(s.name)}" data-schedule-focus="schedule-${s.id}-run">Run now</button></li>`}
            ${s.retired ? '' : `<li role="none"><button type="button" role="menuitem" data-action="edit" data-schedule='${escapeHtml(JSON.stringify(s))}' data-schedule-focus="schedule-${s.id}-edit">Edit schedule</button></li>`}
            <li role="none" class="schedule-menu-danger"><button type="button" role="menuitem" data-action="delete" data-id="${s.id}" data-name="${escapeHtml(s.name)}" data-schedule-focus="schedule-${s.id}-delete">Delete schedule</button></li>
          </ul>
        </div>` : '';
      return `<tr${selected ? ' class="is-selected" aria-current="true"' : ''}>
        <td data-label="Schedule">
          <div class="schedule-cell-stack">
            <a class="schedule-name-link" data-nav href="/apps/${encodeURIComponent(surface.slug)}/schedules/${s.id}" data-schedule-focus="schedule-${s.id}-open">${escapeHtml(s.name)}</a>
            <span class="schedule-cell-primary">${escapeHtml(scheduleTimingLabel(s.cron_expr))}</span>
            <span class="schedule-definition"><code>${escapeHtml(s.cron_expr)}</code> · ${escapeHtml(tz)}${s.timezone_inherited ? ' (default)' : ''}</span>
            ${dstAdvisoryMarkup(s)}
          </div>
        </td>
        <td data-label="Job status">
          <div class="schedule-cell-stack">
            ${scheduleStatusMarkup(state)}
            <span class="schedule-cell-primary">${escapeHtml(lastRun)}</span>
            ${state.attention ? `<span class="schedule-cell-secondary">${escapeHtml(lastSuccess)}</span>` : ''}
            ${compatibility.key !== 'not-tracked' && compatibility.key !== 'retired' ? `<span class="schedule-compatibility-label is-${compatibility.tone}">Data: ${escapeHtml(compatibility.label)}</span>` : ''}
            ${activation.key !== 'none' && activation.key !== 'never' ? `<span class="schedule-cell-secondary">App rollout: ${escapeHtml(activation.label)}</span>` : ''}
          </div>
        </td>
        <td data-label="Next run">
          <div class="schedule-cell-stack"><span class="schedule-cell-primary">${escapeHtml(nextFire)}</span>${s.enabled === false && !s.retired ? '<span class="schedule-cell-secondary">Manual runs available</span>' : ''}</div>
        </td>
        <td class="table-actions" data-label="Actions"><div class="schedule-row-actions">${actions}</div></td>
      </tr>`;
    }).join('');
    container.innerHTML = `<table>
      <thead><tr><th scope="col">Schedule</th><th scope="col">Job status</th><th scope="col">Next run</th><th scope="col"><span class="sr-only">Actions</span></th></tr></thead>
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
    surface.historyRequests.invalidate();
    const openDiagnostics = [...detail.querySelectorAll('details[open]')].map(node => node.querySelector('summary')?.textContent);
    surface.renderedDetailID = schedule.id;
    surface.renderedDetailSignature = detailSignature;

    const state = scheduleState(schedule);
    const activation = activationState(schedule);
    const compatibility = producerCompatibilityState(schedule);
    const latestActivation = schedule.latest_activation;
    const timezone = schedule.effective_timezone || 'UTC';
    detail.setAttribute('aria-busy', 'true');
    detail.querySelectorAll('.schedule-actions-menu').forEach(menu => kebabControls.get(menu)?.close());
    const compatibilityMarkup = `
      ${schedule.deploy_trigger && schedule.deploy_trigger !== 'never' ? `<section class="schedule-compatibility-panel schedule-compatibility-${compatibility.tone}" aria-labelledby="schedule-compatibility-title">
        <div class="schedule-history-heading">
          <div><h3 id="schedule-compatibility-title">Code and data compatibility</h3><p>${escapeHtml(compatibility.detail)}</p>${schedule.convergence_error ? `<p class="schedule-detail-error">${escapeHtml(schedule.convergence_error)}</p>` : ''}</div>
          <div class="schedule-activation-actions">${scheduleStatusMarkup(compatibility)}${surface.options.canManage && compatibility.attention && schedule.convergence_obligation_id && schedule.convergence_status === 'failed' && schedule.enabled !== false && !schedule.retired ? `<button type="button" class="btn-primary" data-detail-action="retry-convergence">Retry producer</button>` : ''}</div>
        </div>
        <details class="schedule-diagnostics"><summary>Producer details</summary>\n        <dl class="schedule-compatibility-facts">
          <div><dt>Deployment policy</dt><dd>${escapeHtml(deployTriggerLabel(schedule.deploy_trigger))}</dd></div>
          <div><dt>Convergence</dt><dd>${escapeHtml(schedule.convergence_status || (schedule.deploy_trigger_satisfied ? 'satisfied' : 'not requested'))}</dd></div>
          <div><dt>Current code</dt><dd><strong>${escapeHtml(schedule.current_app_version || 'Unknown version')}</strong><code>${escapeHtml(schedule.current_content_digest || 'Digest unavailable')}</code></dd></div>
          <div><dt>Data producer</dt><dd><strong>${escapeHtml(schedule.producer_app_version || 'No proven producer')}</strong><code>${escapeHtml(schedule.producer_content_digest || 'Digest unavailable')}</code></dd></div>
          ${schedule.producer_published_at ? `<div class="schedule-activation-wide"><dt>Published</dt><dd>${escapeHtml(scheduleDate(schedule.producer_published_at, timezone))}</dd></div>` : ''}
          ${schedule.convergence_error ? `<div class="schedule-activation-wide schedule-activation-error"><dt>Last error</dt><dd>${escapeHtml(schedule.convergence_error)}</dd></div>` : ''}
        </dl>\n        </details>
      </section>` : ''}`;
    detail.innerHTML = `
      <div class="schedule-detail-head">
        <a class="schedule-detail-back" data-nav href="/apps/${encodeURIComponent(surface.slug)}/schedules">All schedules</a>
        <div class="schedule-detail-title-row">
          <div><h2 id="schedule-detail-title">${escapeHtml(schedule.name)}</h2><div class="schedule-detail-states">${scheduleStatusMarkup(state)}${compatibility.key !== 'not-tracked' && compatibility.key !== 'retired' ? `<span class="schedule-compatibility-label is-${compatibility.tone}">Data: ${escapeHtml(compatibility.label)}</span>` : ''}</div></div>
          ${surface.options.canManage ? `<div class="schedule-detail-actions">
            ${schedule.retired ? '' : `<button type="button" class="schedule-run-btn" data-detail-action="run" data-schedule-focus="schedule-${schedule.id}-detail-run"><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M5.25 3.5 12 8l-6.75 4.5z"/></svg><span>Run now</span></button>`}
            <div class="kebab-menu schedule-actions-menu" data-schedule-actions-menu>
              <button type="button" aria-haspopup="menu" aria-expanded="false" aria-label="More actions for ${escapeHtml(schedule.name)}"><svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="3" cy="8" r="1.25"/><circle cx="8" cy="8" r="1.25"/><circle cx="13" cy="8" r="1.25"/></svg></button>
              <ul class="kebab-list" role="menu" hidden>
                ${schedule.retired ? '' : `<li role="none"><button type="button" role="menuitem" data-detail-action="edit" data-schedule-focus="schedule-${schedule.id}-detail-edit">Edit schedule</button></li>`}
                <li role="none" class="schedule-menu-danger"><button type="button" role="menuitem" data-detail-action="delete" data-schedule-focus="schedule-${schedule.id}-detail-delete">Delete schedule</button></li>
              </ul>
            </div>
          </div>` : ''}
        </div>
      </div>
      ${schedule.roll_feasibility_advisory ? `<div class="schedule-roll-advisory"><strong>Roll capacity warning</strong><span>${escapeHtml(schedule.roll_feasibility_advisory)}</span></div>` : ''}
      <dl class="schedule-definition-grid">
        <div><dt>Timing</dt><dd><code>${escapeHtml(schedule.cron_expr)}</code></dd></div>
        <div><dt>Timezone</dt><dd>${escapeHtml(timezone)}${schedule.timezone_inherited ? ' <span>(inherited)</span>' : ''}</dd></div>
        <div><dt>Next run</dt><dd>${escapeHtml(schedule.retired ? 'Retired' : schedule.enabled === false ? 'Paused' : scheduleDate(schedule.next_fire, timezone))}</dd></div>
        <div><dt>After success</dt><dd>${escapeHtml(afterSuccessLabel(schedule))}</dd></div>
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
      ${compatibility.attention ? compatibilityMarkup : ''}
      <div class="schedule-history-heading"><div><h3>Run history</h3><p>Newest runs first. Times use ${escapeHtml(timezone)}.</p></div></div>
      <div class="schedule-history" data-schedule-history role="region" aria-label="Schedule run history" tabindex="0"><p class="schedule-loading">Loading run history…</p></div>
      ${!compatibility.attention ? compatibilityMarkup : ''}
      <details class="schedule-diagnostics"><summary>Execution details</summary>
        <dl class="schedule-activation-facts">
          <div><dt>Deployment policy</dt><dd>${escapeHtml(deployTriggerLabel(schedule.deploy_trigger))}</dd></div>
          <div><dt>Timeout</dt><dd>${escapeHtml(formatDuration(schedule.timeout_seconds))}</dd></div>
          <div><dt>Overlapping runs</dt><dd>${escapeHtml(schedule.overlap_policy || 'skip')}</dd></div>
          <div><dt>Missed runs</dt><dd>${escapeHtml(schedule.missed_policy || 'skip')}</dd></div>
          <div class="schedule-activation-wide"><dt>After success</dt><dd>${escapeHtml(afterSuccessDetail(schedule))}</dd></div>
        </dl>
      </details>
      ${schedule.dst_advisory ? `<p class="schedule-roll-advisory">${escapeHtml(schedule.dst_advisory)}</p>` : ''}`;

    for (const node of detail.querySelectorAll('details')) if (openDiagnostics.includes(node.querySelector('summary')?.textContent)) node.open = true;
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
    if (!surface.stopped && surface === mountedScheduleSurface && String(surface.renderedDetailID) === String(schedule.id)) detail.setAttribute('aria-busy', 'false');
  }

  async function loadScheduleRunPage(surface, schedule, offset, replace) {
    const host = document.querySelector('[data-schedule-history]');
    if (!host || surface.stopped || surface !== mountedScheduleSurface) return;
    const request = surface.historyRequests.begin();
    const focused = document.activeElement;
    let keepFocus = host.contains(focused);
    const focusMoved = event => { if (event.target !== focused && !focused?.contains(event.target)) keepFocus = false; };
    document.addEventListener('focusin', focusMoved);
    document.addEventListener('pointerdown', focusMoved, true);
    const current = () => !surface.stopped && surface === mountedScheduleSurface && surface.historyRequests.isCurrent(request)
      && host.isConnected && host === document.querySelector('[data-schedule-history]');
    host.setAttribute('aria-busy', 'true');
    try {
      const response = await api(`/api/apps/${encodeURIComponent(surface.slug)}/schedules/${schedule.id}/runs?limit=10&offset=${offset}`, {signal: request.signal});
      if (!current()) return;
      if (response.status === 401) { await handleUnauthorized(); return; }
      if (response.status === 403) { clearScheduleAccess(surface); return; }
      if (!response.ok) throw new Error('response');
      const body = await response.json();
      if (!current()) return;
      const page = Array.isArray(body) ? body : body?.items;
      if (!Array.isArray(page)) throw new Error('Invalid run history');
      const total = Number.isFinite(body.total) ? body.total : page.length;
      surface.detailTotal = total;
      surface.detailRuns = replace ? page : [...(surface.detailRuns || []), ...page];
      renderScheduleRuns(surface, schedule, total);
    } catch {
      if (!current()) return;
      if (replace) host.innerHTML = '<div class="schedule-load-error" role="alert"><p>Run history could not be loaded.</p><button type="button" class="env-btn-secondary" data-run-history-retry>Try again</button></div>';
      else {
        flashToast('More run history could not be loaded. Try again.', 'error');
        renderScheduleRuns(surface, schedule, surface.detailTotal || (surface.detailRuns || []).length);
      }
      host.querySelector('[data-run-history-retry]')?.addEventListener('click', () => loadScheduleRunPage(surface, schedule, 0, true));
    } finally {
      document.removeEventListener('focusin', focusMoved);
      document.removeEventListener('pointerdown', focusMoved, true);
      if (current()) {
        host.setAttribute('aria-busy', 'false');
        if (keepFocus) host.focus({preventScroll: true});
      }
    }
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
		<td data-label="Activation"${!activationResult && !activationError ? ' class="schedule-run-no-activation"' : ''}><div class="schedule-cell-stack">${activationResult ? scheduleStatusMarkup(activationResult) : '—'}${run.target_generation ? `<span>Generation ${escapeHtml(run.target_generation)}</span>` : ''}${activationError ? `<small class="schedule-run-activation-error"><strong>Activation error:</strong> ${escapeHtml(activationError)}</small>` : ''}</div></td>
        <td class="table-actions" data-label="Actions"><div class="schedule-row-actions">${actions}</div></td>
      </tr>`;
    }).join('');
    const remaining = Math.max(0, total - runs.length);
    host.innerHTML = `<table>
      <thead><tr><th scope="col">Job result</th><th scope="col">Started</th><th scope="col">Duration</th><th scope="col">Trigger</th><th scope="col">Exit</th><th scope="col">Activation</th><th scope="col"><span class="sr-only">Actions</span></th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
    <div class="schedule-history-footer"><span>Showing ${runs.length} of ${total}</span>${remaining ? `<button type="button" class="env-btn-secondary" data-load-more-runs data-schedule-focus="schedule-${schedule.id}-load-more">Load ${Math.min(10, remaining)} more</button>` : ''}</div>`;

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
  // Open the schedule add/edit form modal.
  function openScheduleForm(slug, existing) {
    const modal = document.getElementById('schedule-form-modal');
    const title = document.getElementById('schedule-form-title');
    const form = document.getElementById('schedule-form');
    if (!modal || !form) return;

    // Clone before setting values: select.cloneNode() copies default options,
    // not the current selection. Cloning a populated form resets saved policy.
    const newForm = form.cloneNode(true);
    form.parentNode.replaceChild(newForm, form);
    title.textContent = existing ? 'Edit schedule' : 'Add schedule';
    newForm.reset();

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

    setError(document.getElementById('schedule-form-error'), '');

    // Re-attach cron preview input listener and cancel button.
    const refreshCronHint = () => updateCronPreview(
      newForm.querySelector('#sched-cron').value,
      newForm.querySelector('#sched-timezone').value,
    );
    newForm.querySelector('#sched-cron').addEventListener('input', refreshCronHint);
    newForm.querySelector('#sched-timezone').addEventListener('input', refreshCronHint);
    newForm.querySelector('#schedule-form-cancel')?.addEventListener('click', closeScheduleForm);

    newForm.querySelector('.schedule-advanced').open = !!existing && (existing.on_success === 'roll' || (existing.timeout_seconds && existing.timeout_seconds !== 3600) || (existing.overlap_policy && existing.overlap_policy !== 'skip') || (existing.missed_policy && existing.missed_policy !== 'skip'));

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
      newForm.querySelector('#sched-deploy-trigger-help').textContent = trigger === 'bundle_change' ? 'Deployment waits for a successful producer result from the candidate bundle.' : trigger === 'first_deploy' ? 'Only the first deployment waits for a producer result.' : 'This job runs independently of deployments.';
      newForm.querySelector('#sched-on-success-help').textContent = rolling ? 'A roll replaces serving replicas one at a time after a healthy surge replica starts.' : 'Serving replicas keep running. Apps that cache data may need a restart to use it.';
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
    if (!confirm(`Run "${name || 'this schedule'}" against the current deployed bundle? If another run is active, this schedule’s overlap policy still applies.`)) return;
    const originSurface = mountedScheduleSurface;
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
      flashToast(started.run_id ? `Run #${started.run_id} requested.` : 'Run requested.', 'success');
      if (originSurface === mountedScheduleSurface && !originSurface?.stopped) await router.navigate(`/apps/${encodeURIComponent(slug)}/schedules/${id}`);
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
      flashToast(result.warning || (result.run_id ? `Producer run #${result.run_id} requested.` : `Producer retry queued for ${name}.`), result.warning ? 'error' : 'success');
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
    const url = `/api/apps/${encodeURIComponent(slug)}/schedules/${schedID}/runs/${runID}/logs`;
    logPaneController.open({ titleText: `Run #${runID} logs`, url });
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

  // Wire the schedule form close button and overlay-click dismiss.
  document.getElementById('schedule-form-close')?.addEventListener('click', closeScheduleForm);
  document.getElementById('schedule-form-modal')?.addEventListener('click', e => {
    if (e.target === e.currentTarget) closeScheduleForm();
  });

  initialize();
});
