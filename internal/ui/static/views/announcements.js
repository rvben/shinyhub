// Platform announcement management. Server capabilities and revisions are the
// authority; this view never infers publication from a successful preview.
export function localDateValue(value) {
  if (!value) return '';
  const date = new Date(value);
  return `${date.getFullYear()}-${String(date.getMonth()+1).padStart(2,'0')}-${String(date.getDate()).padStart(2,'0')}T${String(date.getHours()).padStart(2,'0')}:${String(date.getMinutes()).padStart(2,'0')}`;
}
export function readAnnouncementForm(form) {
  const data = new FormData(form);
  const instant = (field) => {
    const value = data.get(field);
    if (!value) return null;
    const date = new Date(value);
    if (!Number.isFinite(date.getTime()) || localDateValue(date.toISOString())!==value) throw new Error('Choose a valid local date and time. This time may fall in a daylight-saving gap.');
    return date.toISOString();
  };
  return { title:String(data.get('title')||'').trim(), message:String(data.get('message')||'').trim(), details_url:String(data.get('details_url')||'').trim(), severity:data.get('severity'), dismissible:data.get('dismissible')==='on', starts_at:instant('starts_at'), ends_at:instant('ends_at') };
}
export function mountAnnouncements(ctx) {
  const doc = ctx.document || document;
  const view = doc.getElementById('announcements-view');
  view.hidden = false;
  view.innerHTML = `
    <div class="toolbar"><h1>Announcements</h1><button type="button" class="btn-primary" data-new>New announcement</button></div>
    <p class="announcements-intro">Let everyone know about maintenance, interruptions, and platform changes.</p>
    <div class="announcements-workspace">
      <section class="announcements-list" aria-label="Saved announcements">
        <div class="announcements-list-tools"><label for="announcement-filter">Show</label><select id="announcement-filter"><option value="current">Current and planned</option><option value="all">All announcements</option><option value="archived">Archived</option></select><button type="button" data-refresh>Refresh</button></div>
        <p data-list-error class="error" role="alert" hidden></p><div data-list aria-live="polite"></div><button type="button" data-more hidden>Load more</button>
      </section>
      <section class="announcement-editor" aria-labelledby="announcement-editor-heading">
        <div data-editor-empty class="announcements-editor-empty"><h2>Keep users informed</h2><p>Create a notice now or schedule it ahead of maintenance. Users can read it in ShinyHub and inside their apps.</p><button type="button" data-first-new>Create announcement</button></div>
        <form data-editor hidden>
          <div class="announcement-editor-heading"><h2 id="announcement-editor-heading">New announcement</h2><span data-dirty class="settings-dirty" hidden>Unsaved changes</span></div>
          <p class="announcement-public-note">This content is public. It appears before sign-in and in hosted apps.</p>
          <label for="announcement-title">Title</label><input id="announcement-title" name="title" required maxlength="120" aria-describedby="announcement-title-count"><p id="announcement-title-count" class="hint"></p>
          <label for="announcement-message">Message</label><textarea id="announcement-message" name="message" required maxlength="600" rows="4" aria-describedby="announcement-message-count"></textarea><p id="announcement-message-count" class="hint"></p>
          <label for="announcement-link">Details link <span class="announcement-optional">(optional)</span></label><input id="announcement-link" name="details_url" type="url" maxlength="2048" placeholder="https://status.example.com/maintenance">
          <div class="announcement-fields"><div><label for="announcement-severity">Severity</label><select id="announcement-severity" name="severity"><option value="information">Information</option><option value="warning">Warning</option><option value="critical">Critical</option></select></div><label class="announcement-checkbox"><input name="dismissible" type="checkbox" checked>Allow users to dismiss</label></div>
          <h3>Visibility window</h3><p class="hint">These dates control when the notice appears. Describe the maintenance window in the message.</p>
          <p class="hint" data-timezone></p>
          <div class="announcement-fields"><div><label for="announcement-start">Show from <span class="announcement-optional">(optional)</span></label><input id="announcement-start" name="starts_at" type="datetime-local"></div><div><label for="announcement-end">Show until <span class="announcement-optional">(optional)</span></label><input id="announcement-end" name="ends_at" type="datetime-local"></div></div>
          <p class="hint" data-window></p><p data-live-note class="announcement-public-note" hidden>Saving updates the public notice and makes changed content reappear after dismissal.</p>
          <h3>Preview</h3><div data-preview></div>
          <p data-editor-error class="error" role="alert" hidden></p><p data-success role="status" hidden></p>
          <button type="button" data-reload hidden>Reload saved version</button>
          <div class="announcement-actions"><button type="submit" class="btn-primary" data-save>Save draft</button><button type="button" data-publish>Publish now</button><button type="button" data-schedule>Schedule announcement</button><button type="button" data-disable hidden>Disable</button><button type="button" data-archive hidden>Archive</button><button type="button" data-cancel>Close editor</button></div>
        </form>
      </section>
    </div>`;
  const q = (selector) => view.querySelector(selector);
  const form = q('[data-editor]');
  for (const button of view.querySelectorAll('button:not(.btn-primary)')) button.classList.add('btn-row');
  let saved = null, baseline = '', pending = false, disposed = false, rows = [], offset = 0, hasMore = false, loading = false;
  const valueSnapshot = () => JSON.stringify(Array.from(new FormData(form).entries()));
  const isDirty = () => !form.hidden && valueSnapshot() !== baseline;
  const allowLeave = () => !pending && (!isDirty() || (ctx.confirm || window.confirm)('Discard unsaved announcement changes?'));
  function error(message) {q('[data-editor-error]').textContent=message;q('[data-editor-error]').hidden=!message;}
  function refreshPreview() {
    q('[data-dirty]').hidden=!isDirty();
    q('#announcement-title-count').textContent=`${Array.from(form.elements.title.value).length} / 120 characters`;
    q('#announcement-message-count').textContent=`${Array.from(form.elements.message.value).length} / 600 characters`;
    try {
      const a=readAnnouncementForm(form);
      for(const field of ['starts_at','ends_at']) if(saved?.[field] && form.elements[field].value===localDateValue(saved[field])) a[field]=saved[field];
      const renderer=ctx.preview || window.ShinyHubAnnouncements?.preview;
      if(renderer) renderer(q('[data-preview]'),{...a,title:a.title||'Announcement title',message:a.message||'Explain what will happen and what users should do.'});
      q('[data-window]').textContent=`${a.starts_at ? `Starts ${new Date(a.starts_at).toLocaleString()} (${a.starts_at})` : 'Publish now uses the current time.'} ${a.ends_at ? `Ends ${new Date(a.ends_at).toLocaleString()} (${a.ends_at}).` : 'No automatic expiry.'}`;
    } catch (e) {q('[data-window]').textContent=e.message;}
  }
  function open(a=null) {
    if(!allowLeave()) return;
    saved=a;form.reset();form.hidden=false;q('[data-editor-empty]').hidden=true;error('');q('[data-success]').hidden=true;q('[data-reload]').hidden=true;
    for(const name of ['title','message','details_url']) form.elements[name].value=a?.[name]||'';
    form.elements.severity.value=a?.severity||'information';form.elements.dismissible.checked=a?.dismissible??true;
    form.elements.starts_at.value=localDateValue(a?.starts_at);form.elements.ends_at.value=localDateValue(a?.ends_at);
    q('#announcement-editor-heading').textContent=a?'Edit announcement':'New announcement';
    q('[data-save]').textContent=a && a.publication!=='draft'?'Save changes':'Save draft';
    q('[data-disable]').hidden=!a || a.publication!=='published';q('[data-archive]').hidden=!a || a.publication==='archived';
    q('[data-publish]').hidden=a?.publication==='archived';q('[data-schedule]').hidden=a?.publication==='archived';q('[data-live-note]').hidden=a?.status!=='active';
    baseline=valueSnapshot();refreshPreview();paintList();form.elements.title.focus({preventScroll:true});
  }
  function close() {if(!allowLeave())return;form.hidden=true;q('[data-editor-empty]').hidden=false;saved=null;baseline='';paintList();q('[data-new]').focus();}
  function paintList() {
    const list=q('[data-list]');list.replaceChildren();
    const filter=q('#announcement-filter').value;
    const shown=rows.filter(a=>filter==='all' || (filter==='archived'?a.status==='archived':!['expired','archived'].includes(a.status)));
    if(!shown.length){const empty=doc.createElement('p');empty.className='announcements-empty';empty.textContent=loading?'Loading announcements…':hasMore?'No matching announcements on this page. Load more to see older notices.':'No announcements here yet.';list.append(empty);}
    for(const a of shown){
      const button=doc.createElement('button');button.type='button';button.className='announcement-row';button.setAttribute('aria-pressed',String(saved?.id===a.id));
      const status=doc.createElement('span');status.className=`announcement-status announcement-${a.status}`;status.textContent=a.status[0].toUpperCase()+a.status.slice(1);
      const title=doc.createElement('strong');title.textContent=a.title;
      const detail=doc.createElement('span');detail.className='announcement-row-meta';detail.textContent=`${a.severity[0].toUpperCase()+a.severity.slice(1)} · ${a.starts_at ? new Date(a.starts_at).toLocaleString() : 'Unscheduled'}`;
      const update=doc.createElement('span');update.className='announcement-row-meta';update.textContent=`Updated ${new Date(a.updated_at).toLocaleString()}`;
      button.append(status,title,detail,update);button.addEventListener('click',()=>open(a));list.append(button);
    }
    q('[data-more]').hidden=!hasMore;
  }
  async function load(reset=true) {
    if(loading)return;loading=true;q('[data-refresh]').disabled=true;q('[data-more]').disabled=true;q('[data-list-error]').hidden=true;
    if(reset){offset=0;rows=[];hasMore=false;}paintList();
    try{const response=await ctx.api(`/api/announcements?limit=50&offset=${offset}`);if(!response.ok)throw new Error('Could not load announcements. Try Refresh.');const payload=await response.json();if(disposed)return;rows.push(...payload.announcements);offset+=payload.announcements.length;hasMore=payload.has_more;}
    catch(e){if(!disposed){q('[data-list-error]').textContent=e.message;q('[data-list-error]').hidden=false;}}
    finally{loading=false;if(!disposed){q('[data-refresh]').disabled=false;q('[data-more]').disabled=false;paintList();}}
  }
  async function save(action) {
    if(pending || !form.reportValidity())return;
    error('');q('[data-success]').hidden=true;q('[data-reload]').hidden=true;
    let body;
    try{
      body=readAnnouncementForm(form);
      for(const field of ['starts_at','ends_at']) if(saved?.[field] && form.elements[field].value===localDateValue(saved[field])) body[field]=saved[field];
      body.publication=saved?.publication||'draft';
      if(action==='publish'){body.publication='published';body.starts_at=new Date().toISOString();}
      if(action==='schedule'){if(!body.starts_at || Date.parse(body.starts_at)<=Date.now())throw new Error('Choose a future “Show from” time to schedule this announcement.');body.publication='published';}
      if(action==='disable')body.publication='disabled';if(action==='archive')body.publication='archived';
      if(body.ends_at && body.starts_at && Date.parse(body.ends_at)<=Date.parse(body.starts_at))throw new Error('“Show until” must be after “Show from”.');
      if(saved)body.expected_revision=saved.revision;
    }catch(e){error(e.message);return;}
    pending=true;for(const el of view.querySelectorAll('button,input,select,textarea'))el.disabled=true;
    const saveButton=q('[data-save]');const previousLabel=saveButton.textContent;saveButton.textContent='Saving…';
    try{
      const response=await ctx.api(saved?`/api/announcements/${saved.id}`:'/api/announcements',{method:saved?'PATCH':'POST',body:JSON.stringify(body)});
      const payload=await response.json();if(disposed)return;
      if(!response.ok){if(response.status===409){q('[data-reload]').hidden=false;throw new Error('Another administrator changed this announcement. Your edits are still here. Reload the saved version before trying again.');}throw new Error(payload.error||'Could not save the announcement. Try again.');}
      for(const el of view.querySelectorAll('button,input,select,textarea'))el.disabled=false;
      saved=payload;baseline=valueSnapshot();pending=false;
      // Open only after clearing dirty state; the saved server values own the editor.
      open(payload);q('[data-success]').textContent=`Announcement ${payload.status==='active'?'published':payload.status==='scheduled'?'scheduled':'saved'}.`;q('[data-success]').hidden=false;
      window.dispatchEvent(new Event('shinyhub:announcements-changed'));load();
    }catch(e){if(!disposed)error(e.message);}
    finally{pending=false;if(!disposed){for(const el of view.querySelectorAll('button,input,select,textarea'))el.disabled=false;saveButton.textContent=saved && saved.publication!=='draft'?'Save changes':previousLabel;refreshPreview();}}
  }
  form.addEventListener('input',()=>{q('[data-success]').hidden=true;refreshPreview();});
  form.elements.severity.addEventListener('change',()=>{form.elements.dismissible.checked=form.elements.severity.value!=='critical';refreshPreview();});
  form.addEventListener('submit',event=>{event.preventDefault();save('save');});
  for(const action of ['publish','schedule','disable','archive'])q(`[data-${action}]`).addEventListener('click',()=>save(action));
  q('[data-new]').addEventListener('click',()=>open());q('[data-first-new]').addEventListener('click',()=>open());q('[data-cancel]').addEventListener('click',close);
  q('[data-refresh]').addEventListener('click',()=>load());q('[data-more]').addEventListener('click',()=>load(false));q('#announcement-filter').addEventListener('change',paintList);
  q('[data-reload]').addEventListener('click',async()=>{if(!saved || !allowLeave())return;try{const response=await ctx.api(`/api/announcements/${saved.id}`);if(disposed)return;if(response.ok){baseline=valueSnapshot();open(await response.json());}else error('Could not reload the saved announcement. Try again.');}catch{if(!disposed)error('Could not reload the saved announcement. Try again.');}});
  q('[data-timezone]').textContent=`Times are entered in ${Intl.DateTimeFormat().resolvedOptions().timeZone}. The exact UTC timestamps appear below.`;
  const themeObserver=new MutationObserver(()=>{if(!form.hidden)refreshPreview();});themeObserver.observe(doc.documentElement,{attributes:true,attributeFilter:['data-theme']});
  ctx.updateActiveNav?.(location.pathname);load();
  return {title:'Announcements',isDirty,allowLeave,unmount(){disposed=true;themeObserver.disconnect();view.hidden=true;ctx.onUnmount?.();}};
}
