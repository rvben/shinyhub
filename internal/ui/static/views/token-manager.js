import {tokenListModels, renderTokenList, tokenDate, updateTokenTimes} from './tokens.js';
import {normalizeCLIUserCode, validCLIUserCode, cliConnectDeviceLabel, legacyCLIConnectLinkDetected} from './cli-connect.js';
import {tokenUsageCommand} from './deploy-commands.js';
import {requestJSON} from './json-request.js';
import {createListState, setFeedback} from './page-components.js';
import {focusedKey, restoreFocus} from './focus-restore.js';

// Requests and revealed credentials belong to one route and one session.
// Only metadata survives a successful creation; closing always clears the value.
export function createTokenManager({document, state, api, onUnauthorized, activate, release, copy,
  search = () => '', origin = document.defaultView?.location.origin || '', clock = {}, requestOptions = {}}) {
  const get = id => document.getElementById(id);
  const view = get('tokens-view'), content = get('tokens-list'), refresh = get('tokens-refresh');
  const error = get('tokens-error'), status = get('tokens-status'), count = get('tokens-count');
  const modal = get('new-token-modal'), form = get('new-token-form'), name = get('new-token-name');
  const expiry = get('new-token-expiry'), submit = get('new-token-submit'), check = get('new-token-check');
  const reveal = get('token-reveal'), value = get('token-reveal-value'), copyButton = get('token-reveal-copy');
  const cliPanel = get('cli-connect-panel'), cliForm = get('cli-connect-form'), code = get('cli-connect-code-input');
  const cliButton = get('cli-connect-approve');
  const dismiss = get('token-reveal-dismiss'), keep = get('token-reveal-keep');
  const usageExamples = [
    {button:get('token-usage-copy'),code:get('token-usage-command'),status:get('token-usage-status'),label:'Copy command',copied:'Command copied.',pending:false},
    {button:get('token-api-copy'),code:get('token-api-header'),status:get('token-api-status'),label:'Copy header',copied:'Header copied.',pending:false},
  ];
  const {now = Date.now, setTimer = setTimeout, clearTimer = clearTimeout} = clock;
  const list = createListState({content,loading:get('tokens-loading'),error,busy:view});
  let active = false, generation = 0, reads = 0, owner, session, mutable = true;
  let inventory = null, reading = false, pending = '', uncertain = null, confirming = null, copying = false;
  let revealRevision = 0, timer = null;
  const requests = new Set();
  const current = (version = generation) => active && version === generation && state.user?.id === owner
    && (state.sessionIdentity ?? state.user) === session;
  const canMutate = () => current() && state.user?.can_manage_self !== false && !pending;
  const showError = message => setFeedback(get('new-token-error'),message,{error:true});
  const announce = message => setFeedback(status,message);
  const needsSave = () => current() && !reveal.hidden && !!value.textContent;

  async function request(path, options = {}) {
    const controller = new AbortController(); requests.add(controller);
    try { return await requestJSON(api,path,{...options,signal:controller.signal},requestOptions); }
    finally { requests.delete(controller); }
  }
  async function authorized(response) {
    if (response.status !== 401) return true;
    stop(); await onUnauthorized(); return false;
  }
  function parseInventory(body) {
    const items = Array.isArray(body) ? body : body?.items;
    if (!Array.isArray(items) || items.some(item => !Number.isSafeInteger(item?.id) || item.id < 1
      || typeof item.name !== 'string' || typeof item.created_at !== 'string')) throw new Error('Invalid inventory');
    return items;
  }
  function controls() {
    refresh.disabled = reading || !!pending;
    get('new-token-button').hidden = !mutable;
    get('new-token-button').disabled = !!pending;
    cliPanel.hidden = !mutable;
  }
  function begin(kind) {
    // An inventory read begun before a mutation must not restore a token
    // after revocation or replace freshly created metadata with an old list.
    reads++; reading = false; pending = kind; list.finish();
  }
  function updateCount(models) {
    const expired = models.filter(model => model.expired).length;
    count.textContent = `${models.length} ${models.length === 1 ? 'token' : 'tokens'}${expired ? ` · ${expired} expired` : ''}`;
    count.hidden = false;
  }
  function clearClock() {
    if (timer !== null) clearTimer(timer);
    timer = null;
  }
  function scheduleClock() {
    clearClock();
    if (!current() || !inventory?.length || document.hidden) return;
    const version = generation, timestamp = now();
    let delay = 60000;
    for (const item of inventory) {
      const remaining = Date.parse(item.expires_at) - timestamp;
      if (remaining > 0) delay = Math.min(delay,remaining);
    }
    timer = setTimer(() => {
      if (!current(version)) return;
      timer = null; refreshTimes();
    },Math.max(1,delay));
  }
  function refreshTimes() {
    if (!current()) return;
    if (document.hidden) { clearClock(); return; }
    if (inventory !== null) {
      const models = tokenListModels(inventory,now());
      updateTokenTimes(content,models); updateCount(models);
    }
    scheduleClock();
  }
  function render() {
    const focus = focusedKey(content);
    const models = tokenListModels(inventory || [],now());
    renderTokenList(content,models,document,{mutable,confirming});
    updateCount(models);
    list.results({total:1}); // The renderer includes the actionable empty state.
    restoreFocus(content,focus); scheduleClock();
  }
  async function load() {
    if (!current() || pending) return;
    const version = generation, read = ++reads;
    reading = true; controls(); list.pending({retain:inventory !== null});
    try {
      const response = await request('/api/tokens',{cache:'no-store'});
      if (!current(version) || read !== reads || !await authorized(response)) return;
      if (response.status === 403) {
        inventory = null; clearClock(); content.replaceChildren(); count.hidden = true;
        list.fail('This account does not have access to personal API tokens.'); return;
      }
      if (!response.ok) throw new Error();
      const items = parseInventory(await response.json());
      if (!current(version) || read !== reads) return;
      inventory = items;
      if (confirming && !inventory.some(item => item.id === confirming.id)) confirming = null;
      setFeedback(error,'',{error:true}); render();
    } catch {
      if (current(version) && read === reads) list.fail(inventory === null
        ? 'Could not load tokens. Check your connection and select Refresh to try again.'
        : 'Could not refresh tokens. Showing the last loaded list. Select Refresh to try again.',{retain:inventory !== null});
    } finally {
      if (current(version) && read === reads) { reading = false; list.finish(); controls(); }
    }
  }
  function formState() {
    const busy = pending === 'create' || pending === 'check';
    form.setAttribute('aria-busy',String(busy));
    for (const control of form.elements) control.disabled = busy;
    name.disabled = expiry.disabled = busy || !!uncertain;
    submit.disabled = busy || !!uncertain;
    get('new-token-close').disabled = busy;
    submit.textContent = pending === 'create' ? 'Creating token…' : 'Create token';
    check.hidden = !uncertain;
    check.textContent = pending === 'check' ? 'Checking…' : uncertain?.found ? 'View tokens' : 'Check token status';
    controls();
  }
  function clearReveal() {
    revealRevision++; copying = false; value.textContent = ''; reveal.hidden = true; dismiss.hidden = true;
    get('token-reveal-detail').textContent = '';
    setFeedback(get('token-reveal-status'),'');
    copyButton.disabled = false; copyButton.querySelector('.copy-label').textContent = 'Copy';
  }
  function open() {
    if (!canMutate() || needsSave()) return;
    form.reset(); clearReveal(); form.hidden = false; showError('');
    get('new-token-heading').textContent = 'New API token';
    if (uncertain) { name.value = uncertain.name; expiry.value = String(uncertain.days); showUncertain(); }
    formState(); modal.hidden = false; activate();
    (uncertain ? check : name).focus();
  }
  function close(force = false) {
    if (force !== true && (pending === 'create' || pending === 'check')) return;
    if (force !== true && needsSave()) { dismiss.hidden = false; keep.focus(); return; }
    const visible = !modal.hidden;
    modal.hidden = true; clearReveal(); form.reset(); form.hidden = false; showError('');
    if (visible) release();
  }
  function showUncertain() {
    showError('Creation could not be confirmed. The token may already exist, and its value cannot be recovered. Check its status before creating another.');
  }
  async function create(event) {
    event.preventDefault();
    if (!canMutate() || uncertain || form.hidden) return;
    name.value = name.value.trim();
    if (!form.reportValidity()) return;
    const version = generation, tokenName = name.value, days = Number(expiry.value);
    begin('create'); showError(''); formState();
    try {
      const response = await request('/api/tokens',{method:'POST',body:JSON.stringify({name:tokenName,expires_in_days:days})});
      if (!current(version) || !await authorized(response)) return;
      if (response.status >= 500 || response.status === 408) throw new Error();
      if (!response.ok) {
        showError(response.status === 409 ? 'You already have a token with this name. Choose another name.'
          : response.status === 403 ? 'This account cannot create personal tokens.' : 'Could not create this token. Check the name and try again.');
        return;
      }
      const body = await response.json();
      if (!current(version)) return;
      if (typeof body?.token !== 'string' || !body.token || !Number.isSafeInteger(body.id)
        || typeof body.name !== 'string' || !tokenDate(body.expires_at)) throw new Error();
      form.hidden = true; reveal.hidden = false; value.textContent = body.token;
      get('new-token-heading').textContent = 'Token created';
      get('token-reveal-detail').textContent = `${body.name} · Expires ${tokenDate(body.expires_at)}`;
      copyButton.focus();
    } catch {
      if (current(version)) { uncertain = {name:tokenName,days}; showUncertain(); }
    } finally {
      if (current(version)) {
        pending = ''; formState();
        if (uncertain) check.focus();
        else if (!get('new-token-error').hidden) name.focus();
        load();
      }
    }
  }
  async function checkCreation() {
    if (!canMutate() || !uncertain) return;
    if (uncertain.found) { const id = uncertain.found; uncertain = null; close(); restoreFocus(content,`token:${id}:revoke`); return; }
    const version = generation; begin('check'); formState();
    try {
      const response = await request('/api/tokens',{cache:'no-store'});
      if (!current(version) || !await authorized(response)) return;
      if (!response.ok) throw new Error();
      const items = parseInventory(await response.json());
      if (!current(version)) return;
      inventory = items;
      const existing = inventory.find(item => item.name === uncertain.name);
      if (existing) {
        uncertain.found = existing.id;
        showError('This token exists, but its value cannot be retrieved. View tokens, revoke it, then create a replacement.');
      } else {
        uncertain = null; showError('No token with this name was found. You can try creating it again.');
      }
      setFeedback(error,'',{error:true}); render();
    } catch {
      if (current(version)) showError('Could not check token status. Check your connection and try again.');
    } finally {
      if (current(version)) { pending = ''; formState(); (uncertain ? check : name).focus(); }
    }
  }
  async function copyToken() {
    if (copying || !current() || reveal.hidden || !value.textContent) return;
    const version = generation, revision = revealRevision;
    copying = true; copyButton.disabled = true;
    const feedback = get('token-reveal-status');
    setFeedback(feedback,'');
    try {
      await copy(value.textContent);
      if (current(version) && revision === revealRevision) {
        copyButton.querySelector('.copy-label').textContent = 'Copied'; setFeedback(feedback,'Token copied. Store it somewhere safe.');
      }
    } catch {
      if (current(version) && revision === revealRevision) setFeedback(feedback,'Could not copy. Select the token above and copy it manually.',{error:true,role:'status'});
    } finally {
      if (current(version) && revision === revealRevision) { copying = false; copyButton.disabled = false; if (dismiss.hidden) copyButton.focus(); }
    }
  }
  async function copyUsage(example) {
    if (!current() || example.pending) return;
    const version = generation; example.pending = true; example.button.disabled = true;
    example.button.textContent = example.label; example.status.classList.add('sr-only'); setFeedback(example.status,'');
    try {
      await copy(example.code.textContent);
      if (current(version)) {
        example.button.textContent = 'Copied'; setFeedback(example.status,example.copied);
      }
    } catch {
      if (current(version)) {
        example.status.classList.remove('sr-only');
        setFeedback(example.status,'Could not copy. Select the example and copy it manually.',{error:true,role:'status'});
      }
    } finally {
      if (current(version)) { example.pending = false; example.button.disabled = false; }
    }
  }
  function openCLI() { if (!canMutate()) return; cliPanel.open = true; code.focus(); }
  async function connect(event) {
    event.preventDefault(); if (!canMutate()) return;
    const userCode = normalizeCLIUserCode(code.value);
    const cliError = get('cli-connect-error'), success = get('cli-connect-success');
    setFeedback(cliError,'',{error:true}); setFeedback(success,'');
    if (!validCLIUserCode(userCode)) {
      code.setAttribute('aria-invalid','true'); setFeedback(cliError,'Enter the eight-character code shown in your terminal.',{error:true}); code.focus(); return;
    }
    code.value = userCode; code.removeAttribute('aria-invalid');
    const version = generation; begin('cli'); controls();
    cliForm.setAttribute('aria-busy','true'); code.disabled = cliButton.disabled = true; cliButton.textContent = 'Connecting…';
    try {
      const response = await request('/api/auth/cli-connect/approve',{method:'POST',body:JSON.stringify({user_code:userCode})});
      if (!current(version) || !await authorized(response)) return;
      if (!response.ok) {
        if (response.status >= 500 || response.status === 408) throw new Error();
        setFeedback(cliError,response.status === 400
          ? 'This code is invalid, expired or already used. Run shinyhub connect again to get a new code.'
          : response.status === 403 ? 'This account cannot connect a CLI.' : 'Could not connect. Check the code and try again.',{error:true});
        return;
      }
      const body = await response.json();
      if (!current(version)) return;
      if (typeof body?.name !== 'string' || !tokenDate(body.expires_at)) throw new Error();
      code.value = '';
      setFeedback(success,`Connected ${cliConnectDeviceLabel(body.name)}. Expires ${tokenDate(body.expires_at)}. Return to your terminal to finish.`);
    } catch {
      if (current(version)) setFeedback(cliError,'Connection could not be confirmed. Check your terminal and refresh tokens before trying again.',{error:true});
    } finally {
      if (current(version)) {
        pending = ''; code.disabled = cliButton.disabled = false; cliButton.textContent = 'Connect CLI'; cliForm.removeAttribute('aria-busy'); controls();
        code.focus(); load();
      }
    }
  }
  async function revoke() {
    if (!canMutate() || !confirming) return;
    const version = generation, target = confirming;
    const ordered = tokenListModels(inventory);
    const index = ordered.findIndex(item => item.id === target.id);
    const neighbor = ordered[index + 1] || ordered[index - 1];
    let revoked = false;
    begin('revoke'); confirming = {...target,pending:true,error:''}; render(); controls();
    try {
      const response = await request(`/api/tokens/${target.id}`,{method:'DELETE'});
      if (!current(version) || !await authorized(response)) return;
      if (!response.ok && response.status !== 404) throw new Error();
      inventory = inventory.filter(item => item.id !== target.id); confirming = null;
      render(); announce(`Token “${target.name}” revoked.`); revoked = true;
    } catch {
      if (current(version)) {
        confirming = {...target,error:'Revocation could not be confirmed. This token may still work. Try again to make sure access is removed.'};
        render(); restoreFocus(content,`token:${target.id}:confirm`);
      }
    } finally {
      if (current(version)) {
        pending = ''; controls();
        if (revoked && !restoreFocus(content,neighbor && `token:${neighbor.id}:revoke`)) get('new-token-button').focus();
      }
    }
  }
  function listClick(event) {
    const button = event.target.closest('button'); if (!button || !canMutate()) return;
    if (button.hasAttribute('data-token-connect')) return openCLI();
    if (button.hasAttribute('data-token-confirm')) return revoke();
    if (button.hasAttribute('data-token-cancel')) {
      const id = confirming?.id; confirming = null; render(); restoreFocus(content,`token:${id}:revoke`); return;
    }
    if (button.dataset.tokenId) {
      const target = inventory?.find(item => String(item.id) === button.dataset.tokenId);
      if (target) { confirming = {id:target.id,name:target.name}; render(); restoreFocus(content,`token:${target.id}:cancel`); }
    }
  }
  function capabilitiesChanged() {
    if (!current()) { stop(); return; }
    if (mutable !== (state.user?.can_manage_self !== false)) start();
  }
  function stop() {
    active = false; generation++; reads++; clearClock();
    for (const controller of requests) controller.abort(); requests.clear();
    pending = ''; reading = false; inventory = null; uncertain = null; confirming = null;
    close(true); formState(); list.clear(); content.replaceChildren(); count.hidden = true; announce('');
    code.value = ''; code.disabled = cliButton.disabled = false; cliButton.textContent = 'Connect CLI'; cliForm.removeAttribute('aria-busy'); code.removeAttribute('aria-invalid');
    setFeedback(get('cli-connect-error'),''); setFeedback(get('cli-connect-success'),''); cliPanel.open = false;
    document.removeEventListener('session-capabilities-change',capabilitiesChanged);
    document.removeEventListener('visibilitychange',refreshTimes);
    document.defaultView?.removeEventListener('pageshow',refreshTimes);
    get('token-usage').open = false; get('token-usage-command').textContent = '';
    for (const example of usageExamples) {
      example.pending = false; example.button.disabled = false; example.button.textContent = example.label;
      example.status.classList.add('sr-only'); setFeedback(example.status,'');
    }
  }
  function start() {
    stop(); active = true; owner = state.user?.id; session = state.sessionIdentity ?? state.user;
    mutable = state.user?.can_manage_self !== false; controls();
    get('token-usage-command').textContent = tokenUsageCommand(origin);
    const legacy = legacyCLIConnectLinkDetected(search()); get('cli-connect-legacy-notice').hidden = !legacy; cliPanel.open = legacy;
    document.addEventListener('session-capabilities-change',capabilitiesChanged);
    document.addEventListener('visibilitychange',refreshTimes);
    document.defaultView?.addEventListener('pageshow',refreshTimes);
    return load();
  }
  get('new-token-button').addEventListener('click',open);
  for (const id of ['new-token-close','new-token-cancel']) get(id).addEventListener('click',() => close());
  get('token-reveal-done').addEventListener('click',() => { if (needsSave()) close(true); });
  get('token-reveal-discard').addEventListener('click',() => { if (needsSave() && !dismiss.hidden) close(true); });
  keep.addEventListener('click',() => { dismiss.hidden = true; (copyButton.disabled ? value.closest('pre') : copyButton).focus(); });
  for (const example of usageExamples) example.button.addEventListener('click',() => copyUsage(example));
  modal.addEventListener('click',event => { if (event.target === modal) close(); });
  form.addEventListener('submit',create); check.addEventListener('click',checkCreation); copyButton.addEventListener('click',copyToken);
  content.addEventListener('click',listClick); refresh.addEventListener('click',load); cliForm.addEventListener('submit',connect);
  code.addEventListener('input',() => { code.removeAttribute('aria-invalid'); setFeedback(get('cli-connect-success'),''); });
  return {start,stop,load,open,close,create,connect,checkCreation,
    isPending:() => !!pending, needsSave,
    allowLeave() {
      if (pending) { announce('Wait for the token operation to finish.'); return false; }
      if (needsSave()) { close(); return false; }
      return true;
    },
  };
}
