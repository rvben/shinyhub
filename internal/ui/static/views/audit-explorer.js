import {AUDIT_ACTIONS, auditActionLabel, auditListPath, auditViewPath, auditValidation, auditRangeSuffix} from './audit-log.js';
import {auditDetailEntries} from './audit-detail.js';
import {requestJSON} from './json-request.js';
import {bindFilterBar, createListState, setFeedback} from './page-components.js';

// A mounted investigation owns its data and pending reads. Nothing from a
// departed route or former session may become visible again.
export function createAuditExplorer({document, state, api, navigate, onUnauthorized, copy, origin,
  requestOptions = {}}) {
  const get = id => document.getElementById(id);
  const table = get('audit-table'), body = get('audit-body'), loading = get('audit-loading');
  const search = get('audit-search'), action = get('audit-action-filter');
  const since = get('audit-since'), until = get('audit-until');
  const list = createListState({content:table,loading,error:get('audit-error'),empty:get('audit-empty')});
  let active = false, session, userID, revision = 0, controller, loadedKey = null;
  let sourceSearch, selection = {}, page = 0, inventory = null, expanded = null, busy = false;
  const current = () => active && state.canReadAudit === true && state.user?.id === userID
    && (state.sessionIdentity ?? state.user) === session;
  const message = (id,text) => setFeedback(get(id),text,{error:true});
  const filtered = () => ['event','run','action','since','until','q'].some(key => selection[key]);
  const keyFor = (selection, page) => auditListPath(page, selection);
  function clearData() {
    body.replaceChildren(); list.clear(); inventory = null; loadedKey = null; expanded = null;
    for (const id of ['audit-empty','audit-failure','audit-pagination','audit-pagination-bottom']) get(id).hidden = true;
    get('audit-range').textContent = ''; get('audit-announcement').textContent = '';
  }
  function stop() {
    active = false; revision++; controller?.abort(); controller = null; busy = false;
    clearData(); list.finish();
    get('audit-refresh').removeAttribute('aria-disabled');
    document.removeEventListener('session-capabilities-change', checkAccess);
  }
  function checkAccess() {
    if (current()) return true;
    if (active) { stop(); navigate('/', {replace:true}); }
    return false;
  }
  function syncControls() {
    const browse = !selection.event && !selection.run;
    get('audit-filters').hidden = !browse;
    search.value = browse ? (selection.q || '') : '';
    // Preserve forward-compatible action links even before this UI knows them.
    if (selection.action && ![...action.options].some(o => o.value === selection.action)) {
      action.add(new document.defaultView.Option(auditActionLabel(selection.action), selection.action));
    }
    action.value = browse ? (selection.action || '') : '';
    since.value = browse ? (selection.since || '') : ''; until.value = browse ? (selection.until || '') : '';
    get('audit-range-clear').hidden = !(since.value || until.value);
    const context = get('audit-context'); context.hidden = !filtered();
    get('audit-context-copy').textContent = selection.event ? `Event #${selection.event}`
      : selection.run ? `Events from run ${selection.run}`
      : [selection.q ? `Search: “${selection.q}”` : '', selection.action ? auditActionLabel(selection.action) : '',
        auditRangeSuffix(selection).trim()].filter(Boolean).join(' · ');
  }
  async function load(nextPage, nextSelection, rawSearch = sourceSearch) {
    sourceSearch = rawSearch;
    if (!active) {
      active = true; session = state.sessionIdentity ?? state.user; userID = state.user?.id;
      document.addEventListener('session-capabilities-change', checkAccess);
    }
    if (!checkAccess()) return;
    const version = ++revision; controller?.abort(); controller = new AbortController();
    selection = {...nextSelection}; page = nextPage; syncControls();
    // Do not silently broaden an invalid shared date range into all events.
    const raw = rawSearch === undefined ? null : new URLSearchParams(rawSearch);
    const validation = auditValidation(selection.event || selection.run ? {} : raw
      ? {...selection, since:raw.get('since') || '', until:raw.get('until') || ''} : selection);
    message('audit-validation', validation);
    if (validation) { clearData(); busy = false; get('audit-refresh').removeAttribute('aria-disabled'); return; }
    const key = keyFor(selection, page), retained = loadedKey === key, previousKey = loadedKey;
    if (!retained) clearData();
    busy = true; message('audit-error', ''); get('audit-failure').hidden = true;
    get('audit-refresh').setAttribute('aria-disabled', 'true');
    list.pending({retain:retained,message:retained ? 'Refreshing events…' : 'Loading events…'});
    loading.hidden = false;
    try {
      const response = await requestJSON(api, key, {signal:controller.signal, cache:'no-store'}, requestOptions);
      if (!current() || version !== revision) return;
      if (response.status === 401) { stop(); onUnauthorized(); return; }
      if (response.status === 403) { stop(); navigate('/', {replace:true}); return; }
      if (!response.ok) throw new Error();
      const payload = await response.json();
      if (!current() || version !== revision) return;
      if (!Array.isArray(payload?.events) || !Number.isSafeInteger(payload.total) || payload.total < 0
        || typeof payload.has_more !== 'boolean' || payload.events.some(e => !e || !Number.isSafeInteger(e.id)
        || e.id < 1 || typeof e.action !== 'string' || typeof e.resource_type !== 'string' || typeof e.resource_id !== 'string')) throw new Error();
      inventory = payload; loadedKey = key; render();
      if (previousKey && previousKey !== key) {
        const target = get(selection.event ? 'audit-heading' : 'audit-range');
        target.tabIndex = -1; target.focus({preventScroll:false});
      }
      get('audit-announcement').textContent = `Events loaded. ${get('audit-range').textContent}`;
    } catch {
      if (!current() || version !== revision) return;
      get('audit-failure').hidden = false;
      list.fail(retained ? 'Could not refresh events. Showing the last loaded results. Try again.'
        : 'Could not load these events. Your filters are preserved. Try again.',{retain:retained});
    } finally {
      if (current() && version === revision) {
        busy = false; list.finish();
        get('audit-refresh').removeAttribute('aria-disabled');
      }
    }
  }
  function node(tag, className, text) {
    const el = document.createElement(tag); if (className) el.className = className;
    if (text !== undefined) el.textContent = text; return el;
  }
  function actorName(event) {
    return event.username || (event.user_id ? `Former user #${event.user_id}` : 'System or unauthenticated');
  }
  function timestamp(event) {
    const date = new Date(event.created_at);
    if (!event.created_at || !Number.isFinite(date.getTime())) return {date:'Time unavailable',time:''};
    return {date:new Intl.DateTimeFormat(undefined,{year:'numeric',month:'short',day:'numeric',timeZone:'UTC'}).format(date),
      time:new Intl.DateTimeFormat(undefined,{hour:'2-digit',minute:'2-digit',second:'2-digit',hourCycle:'h23',timeZone:'UTC'}).format(date)};
  }
  function render() {
    const focused = document.activeElement?.dataset.auditFocus;
    body.replaceChildren();
    const events = inventory.events;
    list.results({total:events.length});
    const exact = !!selection.event;
    get('audit-range').textContent = events.length ? (exact ? '1 event' : `${page * 100 + 1}-${page * 100 + events.length} of ${inventory.total.toLocaleString()} events · Newest first`)
      : '0 events';
    if (!events.length) {
      get('audit-empty-heading').textContent = exact ? 'Event not available' : filtered() ? 'No matching events' : 'No events yet';
      get('audit-empty-copy').textContent = exact ? `Event #${selection.event} could not be found. It may no longer be retained.`
        : filtered() ? 'Try a different search, action or date range, or view all events.' : 'Recorded activity will appear here as people and automation use ShinyHub.';
      get('audit-empty-clear').hidden = !filtered();
    }
    for (const event of events) {
      const open = expanded === event.id || (exact && expanded === null);
      const row = node('tr', 'audit-event-row'); row.id = `audit-event-${event.id}`;
      if (open) row.classList.add('audit-event-open');
      const eventCell = node('td','audit-event-main');
      const toggle = node('button','audit-event-title');
      toggle.append(node('span','sr-only',`${open ? 'Hide' : 'Inspect'} `),
        node('span','audit-action-label',auditActionLabel(event.action) || 'Unknown action'));
      toggle.type = 'button'; toggle.setAttribute('aria-expanded', String(open));
      toggle.setAttribute('aria-controls',`audit-detail-${event.id}`);
      toggle.dataset.auditFocus = String(event.id);
      toggle.addEventListener('click',()=>{ expanded = open ? -1 : event.id; render(); });
      toggle.append(' ', node('span','audit-resource',[auditActionLabel(event.resource_type),event.resource_id].filter(Boolean).join(' · ') || 'No resource recorded'),
        node('span','sr-only',` event #${event.id}`));
      eventCell.append(toggle);
      const entries = auditDetailEntries(event);
      const changes = entries.filter(entry => entry.isChange);
      if (changes.length) eventCell.append(node('span','audit-change',
        `${changes.length} ${changes.length === 1 ? 'field' : 'fields'} changed · ${changes.map(entry => entry.label).join(', ')}`));
      if (entries.some(entry=>entry.isError)) eventCell.append(node('span','audit-detail-error','Some event details could not be recorded.'));
      const actorCell = node('td','audit-actor'); actorCell.append(node('span','audit-actor-name',actorName(event)));
      if (event.principal_type === 'service_account' || event.credential_name) actorCell.append(node('span','audit-secondary',event.credential_name || 'Service account'));
      const timeCell = node('td','audit-event-time'), stamp = timestamp(event), time = node('time','',stamp.date);
      if (Number.isFinite(new Date(event.created_at).getTime())) time.dateTime = event.created_at;
      timeCell.append(time,node('span','audit-secondary',stamp.time));
      const linkCell = node('td','audit-event-link'), link = node('a','audit-event-permalink',`#${event.id}`);
      link.href = auditViewPath({event:String(event.id)}); link.dataset.nav = '';
      link.setAttribute('aria-label',`Open audit event #${event.id}`); linkCell.append(link);
      row.append(eventCell,actorCell,timeCell,linkCell); body.append(row);
      const detailRow = node('tr','audit-detail-row'); detailRow.hidden = !open;
      const detailCell = node('td'); detailCell.colSpan = 4;
      const detail = node('section','audit-event-detail'); detail.id = `audit-detail-${event.id}`;
      detail.setAttribute('aria-label',`Details for event #${event.id}`);
      detail.addEventListener('keydown',eventKey=>{if(eventKey.key==='Escape'){eventKey.preventDefault();expanded=-1;render();body.querySelector(`[data-audit-focus="${event.id}"]`)?.focus({preventScroll:true});}});
      const head = node('div','audit-detail-head'); head.append(node('h2','',`Event #${event.id}`));
      const copyGroup = node('div','audit-copy');
      const copyButton = node('button','ov-btn','Copy event link'); copyButton.type='button';
      const copyStatus = node('span','audit-copy-status');
      copyStatus.setAttribute('role','status'); copyStatus.setAttribute('aria-live','polite'); copyStatus.setAttribute('aria-atomic','true');
      let copying = false;
      copyButton.addEventListener('click',async()=>{
        if (copying) return;
        const copyRevision = revision;
        const stillVisible = () => current() && revision === copyRevision && copyButton.isConnected;
        copying = true; copyButton.setAttribute('aria-disabled','true'); copyButton.textContent='Copying…';
        setFeedback(copyStatus,'',{collapse:false});
        try {
          await copy(origin + auditViewPath({event:String(event.id)}));
          if(stillVisible()) setFeedback(copyStatus,'Link copied.');
        } catch {
          if(stillVisible()) {
            setFeedback(copyStatus,'Could not copy. Open the event number and copy its address.',{error:true,role:'status'});
          }
        } finally {
          copying = false; copyButton.removeAttribute('aria-disabled'); copyButton.textContent='Copy event link';
        }
      }); copyGroup.append(copyButton,copyStatus); head.append(copyGroup); detail.append(head);
      const facts = node('dl','audit-facts');
      const metadata = [{label:'Action',value:event.action},{label:'Actor',value:actorName(event)},
        {label:'Recorded (UTC)',value:stamp.time ? `${stamp.date}, ${stamp.time}` : stamp.date},
        {label:'Resource',value:[event.resource_type,event.resource_id].filter(Boolean).join(' · ') || 'Not recorded'},
        {label:'IP address',value:event.ip_address || 'Not recorded'}];
      if(event.credential_name) metadata.push({label:'Credential',value:event.credential_name});
      if(event.credential_type) metadata.push({label:'Credential type',value:event.credential_type});
      if(event.credential_id) metadata.push({label:'Credential ID',value:String(event.credential_id)});
      for (const entry of metadata) facts.append(node('dt','',entry.label),node('dd','',entry.value));
      detail.append(facts);
      const recorded = node('div','audit-recorded'); recorded.append(node('h3','','Recorded details'));
      if(entries.length) {
        const values=node('dl','audit-facts');
        for(const entry of entries) values.append(node('dt','',entry.label),node('dd',entry.isError ? 'audit-detail-error' : '',entry.value));
        recorded.append(values);
      } else recorded.append(node('p','audit-secondary','No additional details were recorded for this event.'));
      detail.append(recorded);
      if(event.run_id) {
        const run=node('a','audit-related','View events from this run');run.href=auditViewPath({run:event.run_id});run.dataset.nav='';detail.append(run);
      }
      if(event.resource_type==='app' && /^[a-zA-Z0-9][a-zA-Z0-9_-]*$/.test(event.resource_id) && event.action!=='delete_app') {
        const app=node('a','audit-related','Open app');app.href='/apps/'+encodeURIComponent(event.resource_id);app.dataset.nav='';detail.append(app);
      }
      detailCell.append(detail);detailRow.append(detailCell);body.append(detailRow);
    }
    for (const suffix of ['','-bottom']) {
      get('audit-prev'+suffix).disabled=page===0;
      get('audit-next'+suffix).disabled=!inventory.has_more;
    }
    get('audit-pagination').hidden=exact || (!inventory.has_more && page===0);
    get('audit-pagination-bottom').hidden=exact || events.length===0 || (!inventory.has_more && page===0);
    if(focused) body.querySelector(`[data-audit-focus="${focused}"]`)?.focus({preventScroll:true});
  }
  function go(nextSelection,nextPage=0) { navigate(auditViewPath(nextSelection,nextPage)); }
  function apply(event) {
    event?.preventDefault();
    const draft={event:'',run:'',q:search.value.trim(),action:action.value,since:since.value,until:until.value};
    const error=auditValidation(draft);message('audit-validation',error);if(error)return;
    go(draft);
  }
  // One binding per shell; mount/unmount only changes the controller's scope.
  for (const name of [...AUDIT_ACTIONS].sort((a,b)=>auditActionLabel(a).localeCompare(auditActionLabel(b)))) {
    action.add(new document.defaultView.Option(auditActionLabel(name),name));
  }
  bindFilterBar({root:get('audit-filters'),fields:{search,action,since,until},onApply:(_,event)=>apply(event)});
  get('audit-range-clear').addEventListener('click',()=>{since.value='';until.value='';apply();since.focus();});
  get('audit-refresh').addEventListener('click',()=>{if(active&&!busy)load(page,selection);});
  get('audit-retry').addEventListener('click',()=>{if(active&&!busy)load(page,selection);});
  for(const suffix of ['','-bottom']) {
    get('audit-prev'+suffix).addEventListener('click',()=>{if(active&&page>0)go(selection,page-1);});
    get('audit-next'+suffix).addEventListener('click',()=>{if(active&&inventory?.has_more)go(selection,page+1);});
  }
  return {load,stop};
}
