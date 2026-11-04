// Publication, access and revisions come from the server. Preview never publishes.
import {createPageHeader, createFilterBar, bindFilterBar, setFeedback} from './page-components.js';
import {requestJSON} from './json-request.js';
import {focusedKey, restoreFocus} from './focus-restore.js';

export function localDateValue(value) {
  if (!value) return '';
  const date = new Date(value);
  return `${date.getFullYear()}-${String(date.getMonth()+1).padStart(2,'0')}-${String(date.getDate()).padStart(2,'0')}T${String(date.getHours()).padStart(2,'0')}:${String(date.getMinutes()).padStart(2,'0')}`;
}
export function readAnnouncementForm(form) {
  const data = {get: field => field === 'dismissible' ? (form.elements.dismissible.checked ? 'on' : null) : form.elements[field]?.value};
  const instant = field => {
    const value = data.get(field);
    if (!value) return null;
    const date = new Date(value);
    if (!Number.isFinite(date.getTime()) || localDateValue(date.toISOString()) !== value) throw new Error('Choose a valid local date and time. This time may fall in a daylight-saving gap.');
    return date.toISOString();
  };
  return {title:String(data.get('title')||'').trim(),message:String(data.get('message')||'').trim(),details_url:String(data.get('details_url')||'').trim(),severity:data.get('severity'),dismissible:data.get('dismissible')==='on',starts_at:instant('starts_at'),ends_at:instant('ends_at')};
}
export function mountAnnouncements(ctx) {
  const doc = ctx.document || document, win = doc.defaultView;
  const view = doc.getElementById('announcements-view');
  view.hidden = false;
  view.innerHTML = `
    <button type="button" class="announcement-back btn-row" data-cancel hidden>Back to announcements</button>
    <div data-header-slot><div class="toolbar-actions"><details class="mobile-disclosure page-secondary" data-mobile-disclosure open><summary>More<span class="sr-only"> page actions</span></summary><div class="page-secondary-panel"><button type="button" class="toolbar-btn-quiet" data-refresh>Refresh</button></div></details><button type="button" class="btn-primary" data-new>New announcement</button></div></div>
    <section data-inventory aria-label="Saved announcements">
      <div class="announcements-list-tools"><label for="announcement-filter">Status</label><select id="announcement-filter"><option value="current">Current and planned</option><option value="all">All announcements</option><option value="archived">Archived</option></select><span data-count class="list-status"></span></div>
      <p data-list-error class="error" role="alert" hidden></p>
      <div data-loading class="inventory-loading" role="status" hidden><span class="sr-only">Loading announcements…</span><div aria-hidden="true"><span></span><span></span><span></span></div></div>
      <div data-list></div><button type="button" class="btn-row" data-more hidden>Load more</button>
    </section>
    <section class="announcement-editor" data-editor-panel hidden>
      <span data-dirty class="settings-dirty" hidden>Unsaved changes</span>
      <form data-editor>
        <div class="announcement-compose">
          <div class="announcement-fields-main">
            <label for="announcement-title">Title</label><input id="announcement-title" name="title" required aria-describedby="announcement-title-count announcement-title-error"><p id="announcement-title-count" class="hint"></p><p id="announcement-title-error" class="error" role="alert" hidden></p>
            <label for="announcement-message">Message</label><textarea id="announcement-message" name="message" required rows="4" aria-describedby="announcement-message-count announcement-message-error"></textarea><p id="announcement-message-count" class="hint"></p><p id="announcement-message-error" class="error" role="alert" hidden></p>
            <label for="announcement-link">Details link <span class="announcement-optional">(optional)</span></label><input id="announcement-link" name="details_url" type="url" maxlength="2048" placeholder="https://status.example.com/maintenance">
            <div class="announcement-fields"><div><label for="announcement-severity">Severity</label><select id="announcement-severity" name="severity"><option value="information">Information</option><option value="warning">Warning</option><option value="critical">Critical</option></select></div><label class="announcement-checkbox"><input name="dismissible" type="checkbox" checked>Allow dismissal</label></div>
            <div class="announcement-delivery">
              <h2>Publication</h2>
              <label for="announcement-delivery">When to publish</label><select id="announcement-delivery" name="delivery"><option value="draft">Save as draft</option><option value="now">Publish now</option><option value="schedule">Schedule for later</option><option value="keep" hidden>Keep current publication</option></select>
              <div data-start-field hidden><label for="announcement-start">Show from</label><input id="announcement-start" name="starts_at" type="datetime-local"></div>
              <label for="announcement-end">Show until <span class="announcement-optional">(optional)</span></label><input id="announcement-end" name="ends_at" type="datetime-local">
              <p class="hint" data-timezone></p><p class="hint" data-window></p>
              <p data-live-note class="announcement-public-note" hidden>Saving changes updates the public notice. Updated content reappears for people who dismissed it.</p>
            </div>
          </div>
          <aside class="announcement-preview" aria-labelledby="announcement-preview-heading"><h2 id="announcement-preview-heading">Preview</h2><p class="hint">How your notice will appear to users.</p><div data-preview></div><p class="hint" data-preview-note>Unsaved changes appear only in this preview.</p></aside>
        </div>
        <div class="announcement-editor-feedback"><p data-editor-error class="error" role="alert" hidden></p><p data-success role="status" hidden></p><button type="button" class="btn-row" data-reload hidden>Reload saved version</button></div>
        <div class="announcement-actions"><button type="submit" class="btn-primary" data-save>Save draft</button><button type="button" class="btn-primary" data-retry hidden>Retry save</button><button type="button" class="btn-row" data-close>Close editor</button><details data-extra class="announcement-more" hidden><summary>More actions</summary><div><button type="button" class="btn-row" data-disable>Disable announcement</button><button type="button" class="btn-row" data-archive>Archive announcement</button></div></details></div>
      </form>
    </section>`;
  const q = selector => view.querySelector(selector);
  q('[data-header-slot]').replaceWith(createPageHeader(doc,{title:'Announcements',headingId:'announcements-heading',description:'Manage public notices about maintenance and platform changes.',actions:q('.toolbar-actions')}));
  view.setAttribute('aria-labelledby','announcements-heading');
  const heading = q('#announcements-heading'), headingLine = doc.createElement('div');
  headingLine.className = 'announcement-heading-line'; heading.replaceWith(headingLine);
  headingLine.append(heading,q('[data-dirty]'));
  function setPageTitle(title,editing) {
    heading.textContent = title;
    q('.page-description').textContent = editing ? 'Visible to everyone when published, including visitors before sign-in and users in hosted apps.' : 'Manage public notices about maintenance and platform changes.';
    q('[data-cancel]').hidden = !editing;
    const branding = win.__SHINYHUB_BRANDING__ || {};
    doc.title = `${branding.environment ? `[${branding.environment.label}] ` : ''}${title} · ${branding.site_title || 'ShinyHub'}`;
  }

  const tools = q('.announcements-list-tools');
  tools.replaceWith(createFilterBar(doc,{label:'Filter announcements',className:'announcements-list-tools',controls:[...tools.children]}));
  const form = q('[data-editor]'), panel = q('[data-editor-panel]'), inventory = q('[data-inventory]');
  const requests = new Set();
  const {now = Date.now,setTimer = setTimeout,clearTimer = clearTimeout} = ctx.clock || {};
  let statusTimer = null;
  let saved = null, baseline = '', pending = false, disposed = false, rows = [], offset = 0, hasMore = false, loading = false, loaded = false, uncertain = null, conflict = false, opener = null;
  const snapshot = () => JSON.stringify([...form.elements].filter(el => el.name).map(el => [el.name,el.type === 'checkbox' ? el.checked : el.value]));
  const isDirty = () => !panel.hidden && (pending || !!uncertain || snapshot() !== baseline);
  const allowLeave = () => !pending && (!isDirty() || (ctx.confirm || win.confirm)(uncertain ? 'Leave this unconfirmed save? It may already be saved. Retry the save to confirm it without creating a duplicate.' : 'Discard unsaved announcement changes?'));
  const error = message => setFeedback(q('[data-editor-error]'),message,{error:true});
  const dateText = value => value ? new Date(value).toLocaleString(undefined,{dateStyle:'medium',timeStyle:'short'}) : '';
  const notify = () => win.dispatchEvent(new win.Event('shinyhub:announcements-changed'));
  async function request(path,options={}) {
    const controller = new AbortController(); requests.add(controller);
    try { return await requestJSON(ctx.api,path,{...options,signal:controller.signal},ctx.requestOptions); }
    finally { requests.delete(controller); }
  }
  async function read(response) {
    if (response.status === 401) { ctx.onUnauthorized?.(); throw new Error('Your session ended. Sign in again.'); }
    if (response.status === 403) throw new Error('You no longer have access to manage announcements.');
    const body = await response.json();
    if (!response.ok) {
      const failure = new Error(body?.error || 'The request could not be completed. Try again.');
      failure.code = body?.code;
      throw failure;
    }
    return body;
  }
  function setBusy(value) {
    pending = value; form.setAttribute('aria-busy',String(value));
    for (const el of form.querySelectorAll('button,input,select,textarea')) el.disabled = value;
    if (!value && uncertain) for (const el of form.querySelectorAll('input,select,textarea')) el.disabled = true;
    q('[data-cancel]').disabled = value;
    if (!value) syncDelivery();
  }
  function syncDelivery() {
    q('[data-dirty]').textContent = uncertain ? 'Save unconfirmed' : 'Unsaved changes';
    const delivery = form.elements.delivery.value;
    q('[data-start-field]').hidden = delivery !== 'schedule' && !(delivery === 'keep' && saved?.starts_at);
    form.elements.starts_at.required = delivery === 'schedule';
    q('[data-save]').textContent = pending ? 'Saving…' : delivery === 'now' ? 'Publish now' : delivery === 'schedule' ? 'Schedule announcement' : saved ? 'Save changes' : 'Save draft';
    q('[data-save]').disabled = pending || !!uncertain || conflict;
    for (const control of [q('[data-disable]'),q('[data-archive]'),form.elements.delivery]) control.disabled = pending || !!uncertain || conflict || saved?.publication === 'archived';
    q('[data-save]').hidden = !!uncertain;
    q('[data-retry]').hidden = !uncertain;
    q('[data-retry]').disabled = pending;
    q('[data-retry]').textContent = pending ? 'Retrying…' : 'Retry save';
  }
  function formBody() {
    const body = readAnnouncementForm(form);
    for (const field of ['starts_at','ends_at']) if (saved?.[field] && form.elements[field].value === localDateValue(saved[field])) body[field] = saved[field];
    return body;
  }
  function validateText() {
    for (const [field,limit] of [['title',120],['message',600]]) {
      const input = form.elements[field], length = Array.from(input.value).length;
      const message = length > limit ? `Use ${limit} characters or fewer.` : '';
      input.setCustomValidity(message);
      if (message) input.setAttribute('aria-invalid','true'); else input.removeAttribute('aria-invalid');
      q(`#announcement-${field}-count`).textContent = `${length} / ${limit} characters`;
      setFeedback(q(`#announcement-${field}-error`),message,{error:true});
    }
  }
  function refreshPreview() {
    q('[data-dirty]').hidden = !isDirty(); syncDelivery();
    validateText();
    try {
      const body = formBody();
      const renderer = ctx.preview || win.ShinyHubAnnouncements?.preview;
      if (renderer) renderer(q('[data-preview]'),{...body,title:body.title||'Announcement title',message:body.message||'Explain what will happen and what users should do.'});
      q('[data-window]').textContent = body.ends_at ? `Automatically hides ${dateText(body.ends_at)}.` : 'No automatic expiry.';
    } catch (failure) { q('[data-window]').textContent = failure.message; }
  }
  function fill(a) {
    saved = a; uncertain = null; conflict = false; form.reset(); error(''); setFeedback(q('[data-success]'),''); q('[data-reload]').hidden = true; q('[data-extra]').open = false;
    for (const control of form.querySelectorAll('button,input,select,textarea')) control.disabled = false;
    for (const name of ['title','message','details_url']) form.elements[name].value = a?.[name] || '';
    form.elements.severity.value = a?.severity || 'information'; form.elements.dismissible.checked = a?.dismissible ?? true;
    form.elements.starts_at.value = localDateValue(a?.starts_at); form.elements.ends_at.value = localDateValue(a?.ends_at);
    const keep = form.elements.delivery.querySelector('[value="keep"]'); keep.hidden = !a || a.publication === 'draft';
    keep.textContent = a?.status === 'scheduled' ? 'Keep scheduled' : a?.status === 'active' ? 'Keep published' : 'Keep current publication';
    form.elements.delivery.value = keep.hidden ? 'draft' : 'keep';
    setPageTitle(a ? 'Edit announcement' : 'New announcement',true);
    q('[data-extra]').hidden = !a || a.publication === 'archived'; q('[data-disable]').hidden = a?.publication !== 'published';
    q('[data-live-note]').hidden = a?.status !== 'active';
    baseline = snapshot(); refreshPreview();
  }
  function open(a=null,trigger=null) {
    if (!allowLeave()) return;
    opener = trigger; inventory.hidden = true; panel.hidden = false; q('.page-actions').hidden = true;
    fill(a); form.elements.title.focus();
  }
  function close() {
    if (!allowLeave()) return;
    const id = saved?.id; saved = null; uncertain = null; conflict = false; baseline = '';
    panel.hidden = true; inventory.hidden = false; q('.page-actions').hidden = false; form.reset(); error('');
    setPageTitle('Announcements',false); q('[data-dirty]').hidden = true;
    const target = id && [...view.querySelectorAll('[data-announcement-id]')].find(el => el.dataset.announcementId === id);
    (target || (opener?.isConnected && opener) || q('[data-new]')).focus();
  }
  function paintList() {
    const list = q('[data-list]'), focus = focusedKey(list,doc);
    list.replaceChildren();
    const filter = q('#announcement-filter').value;
    const shown = rows.filter(a => filter === 'all' || (filter === 'archived' ? a.status === 'archived' : !['expired','archived'].includes(a.status)));
    q('[data-count]').textContent = loaded ? `${shown.length}${hasMore ? '+' : ''} ${shown.length === 1 ? 'announcement' : 'announcements'}` : '';
    if (!shown.length && loaded) {
      const empty = doc.createElement('div'); empty.className = 'announcements-empty';
      const heading = doc.createElement('h2'); heading.textContent = !rows.length ? 'No announcements yet' : filter === 'archived' ? 'No archived announcements' : 'No announcements in this view';
      const text = doc.createElement('p'); text.textContent = hasMore ? 'Load more to check older announcements.' : !rows.length ? 'Create a notice when users need to know about maintenance or a platform change.' : 'Choose All announcements to see saved notices in other states.';
      empty.append(heading,text); list.append(empty);
    }
    if (shown.length) {
      const table = doc.createElement('table'); table.className = 'announcement-table'; table.setAttribute('aria-label','Saved announcements');
      table.innerHTML = '<thead><tr><th scope="col">Announcement</th><th scope="col">Status</th><th scope="col">Visibility</th></tr></thead>';
      const body = doc.createElement('tbody'); table.append(body);
      for (const a of shown) {
        const row = doc.createElement('tr');
        const title = doc.createElement('td');
        const button = doc.createElement('button'); button.type = 'button'; button.className = 'announcement-title'; button.textContent = a.title; button.dataset.announcementId = a.id; button.dataset.focusKey = `announcement:${a.id}`; button.addEventListener('click',() => open(a,button));
        const update = doc.createElement('span'); update.className = 'announcement-row-meta'; update.textContent = `Updated ${dateText(a.updated_at)}`; title.append(button,update);
        const status = doc.createElement('td'); status.dataset.label = 'Status';
        const label = doc.createElement('span'); label.className = `announcement-status announcement-${a.status}`; label.textContent = a.status[0].toUpperCase()+a.status.slice(1);
        const severity = doc.createElement('span'); severity.className = 'announcement-row-meta'; severity.textContent = a.severity[0].toUpperCase()+a.severity.slice(1); status.append(label,severity);
        const visibility = doc.createElement('td'); visibility.dataset.label = 'Visibility';
        const start = doc.createElement('span'); start.textContent = a.publication === 'draft' ? 'Not published' : a.status === 'archived' || a.status === 'disabled' ? 'Not visible' : `From ${dateText(a.starts_at)}`;
        const end = doc.createElement('span'); end.className = 'announcement-row-meta'; end.textContent = a.ends_at ? `Until ${dateText(a.ends_at)}` : a.publication === 'published' ? 'No expiry' : '';
        visibility.append(start,end); row.append(title,status,visibility); body.append(row);
      }
      list.append(table);
    }
    q('[data-more]').hidden = !hasMore;
    if (focus && !restoreFocus(list,focus)) q('#announcement-filter').focus();
  }
  function currentStatus(a,timestamp=now()) {
    if (a.publication !== 'published') return a.status;
    return Date.parse(a.ends_at) <= timestamp ? 'expired' : Date.parse(a.starts_at) > timestamp ? 'scheduled' : 'active';
  }
  function updateStatuses() {
    if (statusTimer !== null) clearTimer(statusTimer);
    statusTimer = null;
    if (disposed || doc.hidden) return;
    let changed = false, delay = Infinity;
    const timestamp = now();
    rows = rows.map(a => {
      if (a.publication !== 'published') return a;
      const start = Date.parse(a.starts_at), end = Date.parse(a.ends_at), status = currentStatus(a,timestamp);
      for (const boundary of [start,end]) if (boundary > timestamp) delay = Math.min(delay,boundary-timestamp);
      if (a.status === status) return a;
      changed = true; return {...a,status};
    });
    if (changed) paintList();
    if (Number.isFinite(delay)) statusTimer = setTimer(updateStatuses,Math.min(delay,60000));
  }
  function parseList(body) {
    if (!Array.isArray(body?.announcements) || body.announcements.some(a => !a?.id || typeof a.title !== 'string' || typeof a.status !== 'string' || typeof a.severity !== 'string')) throw new Error('Could not read announcements. Try Refresh.');
    return body;
  }
  async function load(reset=true) {
    if (loading || disposed) return;
    loading = true; q('[data-refresh]').disabled = true; q('[data-more]').disabled = true; inventory.setAttribute('aria-busy','true'); q('[data-loading]').hidden = loaded;
    try {
      const body = parseList(await read(await request(`/api/announcements?limit=50&offset=${reset ? 0 : offset}`)));
      if (disposed) return;
      rows = reset ? body.announcements : [...rows,...body.announcements]; offset = rows.length; hasMore = body.has_more; loaded = true;
      setFeedback(q('[data-list-error]'),''); paintList(); updateStatuses();
    } catch (failure) {
      if (!disposed) setFeedback(q('[data-list-error]'),loaded ? 'Could not refresh announcements. The last loaded list is still shown. Try Refresh.' : failure.message,{error:true});
    } finally {
      if (!disposed) { loading = false; q('[data-refresh]').disabled = false; q('[data-more]').disabled = false; q('[data-loading]').hidden = true; inventory.removeAttribute('aria-busy'); }
    }
  }
  function accept(a) {
    // Replay confirms the original mutation; current visibility follows time.
    a = {...a,status:currentStatus(a)};
    fill(a); setFeedback(q('[data-success]'),`Announcement ${a.status === 'active' ? 'published' : a.status === 'scheduled' ? 'scheduled' : 'saved'}.`); notify();
    const index = rows.findIndex(row => row.id === a.id); if (index < 0) { rows.unshift(a); offset++; } else rows[index] = a;
    loaded = true; paintList(); updateStatuses(); q('[data-save]').focus();
  }
  async function save(action) {
    if (pending || uncertain || conflict) return;
    validateText();
    if (!form.reportValidity()) return;
    error(''); setFeedback(q('[data-success]'),'');
    let body;
    try {
      body = formBody(); body.publication = saved?.publication || 'draft';
      if (action === 'now') { body.publication = 'published'; body.starts_at = new Date().toISOString(); }
      if (action === 'draft') body.publication = 'draft';
      if (action === 'schedule') {
        if (!body.starts_at || Date.parse(body.starts_at) <= Date.now()) throw new Error('Choose a future Show from time to schedule this announcement.');
        body.publication = 'published';
      }
      if (action === 'disable') body.publication = 'disabled';
      if (action === 'archive') body.publication = 'archived';
      const start = body.starts_at || (body.publication === 'published' ? new Date().toISOString() : null);
      if (start && body.ends_at && Date.parse(body.ends_at) <= Date.parse(start)) throw new Error('Show until must be after the notice starts.');
      if (saved) body.expected_revision = saved.revision;
    } catch (failure) { error(failure.message); return; }
    await submitAttempt({
      path:saved ? `/api/announcements/${saved.id}` : '/api/announcements',
      method:saved ? 'PATCH' : 'POST',
      body:JSON.stringify(body),
      key:win.crypto.randomUUID(),
    });
  }
  async function submitAttempt(attempt) {
    if (pending || disposed) return;
    error(''); setFeedback(q('[data-success]'),''); setBusy(true); syncDelivery();
    let response;
    try {
      response = await request(attempt.path,{method:attempt.method,body:attempt.body,headers:{'Idempotency-Key':attempt.key}});
      const result = await read(response); if (disposed) return;
      if (!result?.id || typeof result.revision !== 'number') throw new Error('Unconfirmed save');
      uncertain = null; setBusy(false);
      accept(result);
    } catch (failure) {
      if (disposed) return;
      if (response?.status >= 400 && response.status < 500) {
        uncertain = null;
        if (response.status === 409 && failure.code !== 'idempotency_key_conflict' && saved) {
          conflict = true; q('[data-reload]').hidden = false;
          error('Another administrator changed this announcement. Your edits are still here. Reload the saved version before saving again.');
        } else error(failure.message);
      } else {
        // Preserve the exact request, including publication time and key. The
        // server atomically replays a completed save or commits it only once.
        uncertain = attempt;
        error('The save could not be confirmed. Your edits are preserved. Retry the save to finish safely.');
      }
    } finally {
      if (!disposed) {
        setBusy(false); q('[data-dirty]').hidden = !isDirty();
        if (uncertain) q('[data-retry]').focus();
      }
    }
  }
  async function reloadSaved() {
    if (!saved || pending || !allowLeave()) return;
    setBusy(true);
    try { const a = await read(await request(`/api/announcements/${saved.id}`)); if (!disposed) { setBusy(false); fill(a); form.elements.title.focus(); } }
    catch { if (!disposed) error('Could not reload the saved announcement. Your edits are still here. Try again.'); }
    finally { if (!disposed) setBusy(false); }
  }
  form.addEventListener('input',() => { setFeedback(q('[data-success]'),''); refreshPreview(); });
  form.addEventListener('change',refreshPreview);
  form.elements.severity.addEventListener('change',() => { form.elements.dismissible.checked = form.elements.severity.value !== 'critical'; refreshPreview(); });
  form.addEventListener('submit',event => { event.preventDefault(); save(form.elements.delivery.value); });
  for (const action of ['disable','archive']) q(`[data-${action}]`).addEventListener('click',() => save(action));
  q('[data-new]').addEventListener('click',event => open(null,event.currentTarget));
  q('[data-cancel]').addEventListener('click',close); q('[data-close]').addEventListener('click',close);
  q('[data-refresh]').addEventListener('click',() => load()); q('[data-more]').addEventListener('click',() => load(false));
  q('[data-reload]').addEventListener('click',reloadSaved); q('[data-retry]').addEventListener('click',() => { if (uncertain) submitAttempt(uncertain); });
  const filterBar = bindFilterBar({root:q('.announcements-list-tools'),fields:{status:q('#announcement-filter')},onChange:paintList});
  q('[data-timezone]').textContent = `Times use ${Intl.DateTimeFormat().resolvedOptions().timeZone}.`;
  const themeObserver = new win.MutationObserver(() => { if (!panel.hidden) refreshPreview(); }); themeObserver.observe(doc.documentElement,{attributes:true,attributeFilter:['data-theme']});
  doc.addEventListener('visibilitychange',updateStatuses); win.addEventListener('pageshow',updateStatuses);
  ctx.updateActiveNav?.(win.location.pathname); load();
  return {get title() { return heading.textContent; },isDirty,isPending:() => pending,allowLeave,unmount() { disposed = true; if (statusTimer !== null) clearTimer(statusTimer); doc.removeEventListener('visibilitychange',updateStatuses); win.removeEventListener('pageshow',updateStatuses); for (const request of requests) request.abort(); requests.clear(); filterBar.dispose(); themeObserver.disconnect(); form.reset(); view.hidden = true; ctx.onUnmount?.(); }};
}
