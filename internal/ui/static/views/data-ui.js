// Data is scoped to the fetched app and this panel's lifetime. In particular,
// late upload/fetch callbacks must never act on the next app's Data page.
export function dataBytes(value) {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return 'Unavailable';
  if (value === 0) return '0 B';
  const unit = Math.min(4, Math.floor(Math.log(value) / Math.log(1024)));
  return `${(value / 1024 ** unit).toLocaleString(undefined, {maximumFractionDigits: unit ? 1 : 0})} ${['B', 'KiB', 'MiB', 'GiB', 'TiB'][unit]}`;
}
const cleanPath = path => path.split('/').filter(part => part && part !== '.').join('/');
export function validateDataPath(path) {
  if (!path || new TextEncoder().encode(path).length > 512 || path.startsWith('/') || path.endsWith('/') || path.includes('\0') || path.split('/').includes('..') || !cleanPath(path) || cleanPath(path).split('/')[0].startsWith('.shinyhub-')) {
    return 'Use a relative file path of at most 512 bytes, without parent directories or a reserved .shinyhub- prefix.';
  }
  return '';
}
const encodePath = path => path.split('/').map(encodeURIComponent).join('/');

export function createDataViewer({panel, app, api, canManage = false, onUnauthorized = () => {}, runtimeMode = app.runtime_mode, xhrFactory, confirm, readCookie}) {
  const doc = panel.ownerDocument;
  const win = doc.defaultView;
  const slug = app.slug;
  const base = `/api/apps/${encodeURIComponent(slug)}`;
  const ask = confirm || (message => win.confirm(message));
  const cookie = readCookie || (name => {
    const raw = doc.cookie.split(';').map(part => part.trim()).find(part => part.startsWith(`${name}=`));
    try { return raw ? decodeURIComponent(raw.slice(name.length + 1)) : ''; } catch { return ''; }
  });
  let alive = true, files = [], mounts = [], loaded = false, mountLoaded = false, visible = 50, writable = canManage;
  let filesRequest, mountsRequest, xhr, uploading = false;
  const writes = new Set();
  const pendingMutations = new Set();
  panel.classList.add('data-surface');
  panel.innerHTML = `
    <header class="app-tab-heading"><div><h2>Data</h2><p>Persistent files for this app, kept across deployments.</p></div><div class="data-heading-actions"><button type="button" class="env-btn-secondary" id="data-refresh">Refresh</button><button type="button" class="btn-primary" id="data-upload-toggle" aria-expanded="false" aria-controls="data-upload-form" ${canManage ? '' : 'hidden'}>Upload file</button></div></header>
    <dl class="data-storage" aria-label="Storage"><div><dt>Files</dt><dd id="data-file-total">—</dd></div><div><dt>Data size</dt><dd id="data-file-size">—</dd></div><div><dt>App storage</dt><dd id="data-app-storage">—</dd></div></dl>
    <p class="data-help data-storage-note">App storage includes all deployment bundles, dependencies, and data files.</p>
    <form id="data-upload-form" class="data-form" hidden>
      <h3>Upload file</h3><div class="data-form-grid"><div><label for="data-file-input">File</label><input id="data-file-input" type="file" required></div><div><label for="data-dest-input">Destination path <span class="data-optional">(optional)</span></label><input id="data-dest-input" type="text" placeholder="Use the filename" autocomplete="off" spellcheck="false" aria-describedby="data-dest-help data-destination"></div></div>
      <p id="data-destination" class="data-destination">Choose a file to see its destination.</p><p id="data-dest-help" class="data-help">An existing file at this path will be replaced. Apps that cache data may need a restart.</p>
      <label class="data-check"><input type="checkbox" id="data-restart-input"><span>Restart the app after upload <small>Only restarts a running app; stopped apps stay stopped.</small></span></label>
      <progress id="data-progress" max="100" value="0" aria-label="Upload progress" hidden></progress>
      <p id="data-upload-status" class="data-status" role="status" aria-live="polite" hidden></p>
      <div class="data-form-actions"><button id="data-upload-btn" type="submit" class="btn-primary">Upload</button><button type="button" class="env-btn-secondary" id="data-upload-cancel">Close</button></div>
    </form>
    <div class="data-files-heading"><h3>Files</h3><div><label class="sr-only" for="data-filter">Filter files by path</label><input id="data-filter" type="search" placeholder="Filter files by path" autocomplete="off"></div></div>
    <div id="data-error" class="data-feedback" hidden><p class="error" role="alert"></p><button type="button" class="env-btn-secondary" id="data-retry">Retry files</button></div>
    <p id="data-list-status" class="data-help" role="status">Loading files…</p>
    <table id="data-list-table" class="data-table" hidden><caption class="sr-only">App data files</caption><thead><tr><th scope="col">Path</th><th scope="col">Size</th><th scope="col">Modified</th><th scope="col"><span class="sr-only">Actions</span></th></tr></thead><tbody id="data-list"></tbody></table>
    <div class="data-list-footer"><span id="data-file-count" class="data-help" role="status"></span><button id="data-more" type="button" class="env-btn-secondary" hidden>Show 50 more</button></div>
    <section class="data-mounts" aria-labelledby="data-mounts-title"><div class="data-section-heading"><div><h3 id="data-mounts-title">Shared mounts</h3><p class="data-help">Use another app’s data at <code>data/shared/&lt;source-slug&gt;/</code>.</p></div><button type="button" id="data-mount-toggle" class="env-btn-secondary" aria-expanded="false" aria-controls="data-mount-form" ${canManage ? '' : 'hidden'}>Add mount</button></div>
      <p id="data-runtime-note" class="data-help"></p>
      <form id="data-mount-form" class="data-form" hidden><label for="data-source-input">Source app slug</label><div class="data-mount-fields"><input id="data-source-input" type="text" required autocomplete="off" spellcheck="false" placeholder="e.g. warehouse" aria-describedby="data-source-help"><button id="data-mount-btn" class="btn-primary" type="submit">Add mount</button><button id="data-mount-cancel" class="env-btn-secondary" type="button">Close</button></div><p id="data-source-help" class="data-help">You need access to the source app. New mounts apply on the next app start; running apps are not restarted.</p><p id="data-mount-form-status" class="data-status" role="status" hidden></p></form>
      <div id="data-mount-error" class="data-feedback" hidden><p class="error" role="alert"></p><button type="button" id="data-mount-retry" class="env-btn-secondary">Retry mounts</button></div><p id="data-mount-status" class="data-help" role="status">Loading mounts…</p><ul id="shared-data-list" class="data-mount-list"></ul>
    </section>`;
  const el = id => panel.querySelector(`#${id}`);
  const text = (id, value) => { el(id).textContent = value; };
  const status = (id, message, error = false) => {
    const node = el(id); node.textContent = message; node.hidden = !message;
    node.classList.toggle('error', error);
  };
  const feedback = (id, message) => {
    const node = el(id); node.hidden = !message; node.querySelector('p').textContent = message;
  };
  text('data-runtime-note', runtimeMode === 'docker'
    ? 'The Docker runtime enforces read-only access to shared mounts.'
    : runtimeMode === 'native' ? 'Read-only is a convention under the native runtime; writes are not blocked. The Docker runtime enforces read-only access.'
      : 'Read-only is enforced by the Docker runtime. Under the native runtime it is a convention; writes are not blocked.');
  function lockWrites() {
    writable = false;
    for (const id of ['data-upload-toggle', 'data-upload-form', 'data-mount-toggle', 'data-mount-form']) el(id).hidden = true;
    renderFiles(); renderMounts();
  }
  function revokeAccess() {
    filesRequest?.abort(); mountsRequest?.abort();
    files = []; mounts = []; loaded = false; mountLoaded = false;
    for (const id of ['data-file-total', 'data-file-size', 'data-app-storage']) text(id, 'Unavailable');
    lockWrites();
    el('data-retry').disabled = false; el('data-mount-retry').disabled = false;
  }
  async function bodyOf(response) { try { return await response.json(); } catch { return {}; } }
  function messageOf(body, fallback) { return typeof body?.error === 'string' ? body.error : fallback; }
  function loadError(error, label, hasPrevious) {
    const detail = error instanceof TypeError ? 'Check your connection and try again.' : error.message || 'Try again.';
    return `Could not ${hasPrevious ? 'refresh' : 'load'} ${label}. ${hasPrevious ? 'Showing the last loaded list. ' : ''}${detail}`;
  }
  async function refreshFiles() {
    filesRequest?.abort();
    const request = new AbortController(); filesRequest = request;
    const current = () => alive && filesRequest === request && !request.signal.aborted;
    el('data-retry').disabled = true;
    try {
      const response = await api(`${base}/data`, {signal: request.signal});
      if (!current()) return;
      if (response.status === 401) { onUnauthorized(); return; }
      const body = await bodyOf(response);
      if (!current()) return;
      if (!response.ok) {
        if (response.status === 403) { revokeAccess(); feedback('data-error', messageOf(body, 'You no longer have access to these files.')); return; }
        throw new Error(messageOf(body, 'Could not refresh files. Previously loaded files are still shown.'));
      }
      if (!Array.isArray(body?.items) && !Array.isArray(body)) throw new Error('The server returned an invalid file list. Try again.');
      files = (Array.isArray(body) ? body : body.items).filter(file => typeof file.path === 'string'); loaded = true;
      text('data-file-total', files.length.toLocaleString());
      text('data-file-size', dataBytes(body.data_bytes));
      text('data-app-storage', `${dataBytes(body.used_bytes)}${typeof body.quota_mb === 'number' ? body.quota_mb === 0 ? ' · No limit' : ` / ${body.quota_mb.toLocaleString()} MiB` : ''}`);
      feedback('data-error', ''); renderFiles(); updateDestination();
    } catch (error) {
      if (current()) { feedback('data-error', loadError(error, 'files', loaded)); if (!loaded) status('data-list-status', 'Files could not be loaded.'); }
    } finally { if (current()) el('data-retry').disabled = false; }
  }
  function renderFiles() {
    const needle = el('data-filter').value.trim().toLocaleLowerCase();
    const matched = files.filter(file => file.path.toLocaleLowerCase().includes(needle));
    const rows = matched.slice(0, visible);
    el('data-list').replaceChildren();
    el('data-list-table').hidden = !rows.length;
    status('data-list-status', rows.length ? '' : loaded ? needle ? 'No files match this path.' : writable ? 'No data files yet. Upload a file to make it available to this app.' : 'No data files yet.' : 'Files could not be loaded.');
    text('data-file-count', loaded ? needle ? `${matched.length.toLocaleString()} matching · ${files.length.toLocaleString()} total` : `${Math.min(visible, matched.length).toLocaleString()} of ${files.length.toLocaleString()} files` : '');
    el('data-more').hidden = matched.length <= visible;
    el('data-more').textContent = `Show ${Math.min(50, matched.length - visible)} more`;
    for (const file of rows) {
      const row = doc.createElement('tr');
      const path = doc.createElement('td'); path.dataset.label = 'Path'; path.className = 'data-file-path';
      const name = doc.createElement(writable ? 'a' : 'span'); name.textContent = file.path;
      if (writable) { name.href = `${base}/data/${encodePath(file.path)}`; name.setAttribute('download', ''); name.title = `Download ${file.path}`; }
      path.append(name);
      const size = doc.createElement('td'); size.dataset.label = 'Size'; size.className = 'data-file-size'; size.textContent = dataBytes(file.size);
      const modified = doc.createElement('td'); modified.dataset.label = 'Modified';
      const date = typeof file.modified_at === 'number' && file.modified_at > 0 ? new Date(file.modified_at * 1000) : null;
      if (date && !Number.isNaN(date.getTime())) { const time = doc.createElement('time'); time.dateTime = date.toISOString(); time.textContent = date.toLocaleString(undefined, {dateStyle: 'medium', timeStyle: 'short'}); time.title = date.toLocaleString(); modified.append(time); } else modified.textContent = 'Unavailable';
      const actions = doc.createElement('td'); actions.className = 'data-row-actions';
      if (writable) {
        const button = doc.createElement('button'); button.type = 'button'; button.className = 'env-btn-danger'; button.textContent = 'Delete'; button.setAttribute('aria-label', `Delete ${file.path}`);
        button.dataset.mutationUrl = `${base}/data/${encodePath(file.path)}`; button.disabled = pendingMutations.has(button.dataset.mutationUrl);
        button.addEventListener('click', () => {
          if (pendingMutations.has(button.dataset.mutationUrl)) return;
          if (!ask(`Delete ${file.path}? This permanently deletes the file.`)) return;
          mutate(`${base}/data/${encodePath(file.path)}`, {method: 'DELETE'}, button, 'data-error', async () => { await refreshFiles(); });
        }); actions.append(button);
      }
      row.append(path, size, modified, actions); el('data-list').append(row);
    }
  }
  async function refreshMounts() {
    mountsRequest?.abort();
    const request = new AbortController(); mountsRequest = request;
    const current = () => alive && mountsRequest === request && !request.signal.aborted;
    el('data-mount-retry').disabled = true;
    try {
      const response = await api(`${base}/shared-data`, {signal: request.signal});
      if (!current()) return;
      if (response.status === 401) { onUnauthorized(); return; }
      const body = await bodyOf(response);
      if (!current()) return;
      if (!response.ok) {
        if (response.status === 403) { revokeAccess(); feedback('data-mount-error', messageOf(body, 'You no longer have access to these mounts.')); return; }
        throw new Error(messageOf(body, 'Could not refresh mounts. Previously loaded mounts are still shown.'));
      }
      if (!Array.isArray(body?.items) && !Array.isArray(body)) throw new Error('The server returned an invalid mount list. Try again.');
      mounts = (Array.isArray(body) ? body : body.items).filter(mount => typeof mount.source_slug === 'string'); mountLoaded = true;
      feedback('data-mount-error', ''); renderMounts();
    } catch (error) { if (current()) { feedback('data-mount-error', loadError(error, 'mounts', mountLoaded)); if (!mountLoaded) status('data-mount-status', 'Mounts could not be loaded.'); } }
    finally { if (current()) el('data-mount-retry').disabled = false; }
  }
  function renderMounts() {
    el('shared-data-list').replaceChildren();
    status('data-mount-status', mountLoaded ? mounts.length ? '' : 'No shared mounts configured.' : 'Mounts could not be loaded.');
    for (const mount of mounts) {
      const item = doc.createElement('li');
      const identity = doc.createElement('div'); const source = doc.createElement('strong'); source.textContent = mount.source_slug;
      const path = doc.createElement('code'); path.textContent = `data/shared/${mount.source_slug}/`; identity.append(source, path); item.append(identity);
      if (writable) {
        const button = doc.createElement('button'); button.type = 'button'; button.className = 'env-btn-danger'; button.textContent = 'Unmount'; button.setAttribute('aria-label', `Unmount ${mount.source_slug}`);
        button.dataset.mutationUrl = `${base}/shared-data/${encodeURIComponent(mount.source_slug)}?restart=true`; button.disabled = pendingMutations.has(button.dataset.mutationUrl);
        button.addEventListener('click', () => {
          if (pendingMutations.has(button.dataset.mutationUrl)) return;
          if (!ask(`Unmount data from ${mount.source_slug}? A running app will restart and active sessions will disconnect. Stopped apps stay stopped. Active scheduled jobs must finish or be cancelled first.`)) return;
          mutate(`${base}/shared-data/${encodeURIComponent(mount.source_slug)}?restart=true`, {method: 'DELETE'}, button, 'data-mount-error', async (body, statusCode) => {
            if (statusCode !== 204 && body.unmounted !== true) throw new Error('Unmount was not confirmed');
            mounts = mounts.filter(item => item.source_slug !== mount.source_slug); renderMounts();
            await refreshMounts();
            if (alive) status('data-mount-status', body.restart_error
              ? `Unmounted ${mount.source_slug}. App restart failed: ${body.restart_error}. The app is stopped; start it when ready.`
              : body.restarted ? `Unmounted ${mount.source_slug}. App restarted.` : `Unmounted ${mount.source_slug}.`, !!body.restart_error);
          });
        }); item.append(button);
      }
      el('shared-data-list').append(item);
    }
  }
  async function mutate(url, options, button, errorID, success) {
    if (!alive || !writable || button.disabled || pendingMutations.has(url)) return;
    const controller = new AbortController(); writes.add(controller); pendingMutations.add(url); button.disabled = true;
    const report = message => errorID.endsWith('form-status') ? status(errorID, message, true) : feedback(errorID, message);
    report('');
    try {
      const response = await api(url, {...options, signal: controller.signal});
      if (!alive) return;
      if (response.status === 401) { onUnauthorized(); return; }
      const body = await bodyOf(response);
      if (!alive) return;
      if (!response.ok) { report(messageOf(body, 'The change could not be saved. Try again.')); return; }
      await success(body, response.status);
    } catch { if (alive) report('The change could not be confirmed. Refresh before trying again.'); }
    finally {
      writes.delete(controller); pendingMutations.delete(url);
      if (alive) {
        button.disabled = false;
        for (const current of panel.querySelectorAll('button[data-mutation-url]')) {
          current.disabled = pendingMutations.has(current.dataset.mutationUrl);
        }
      }
    }
  }
  function toggleForm(formID, buttonID, open) {
    el(formID).hidden = !open; el(buttonID).setAttribute('aria-expanded', String(open));
    if (open) el(formID).querySelector('input').focus(); else el(buttonID).focus();
  }
  const destination = () => el('data-dest-input').value.trim() || el('data-file-input').files?.[0]?.name || '';
  function updateDestination() {
    const path = cleanPath(destination()); const exists = files.some(file => file.path === path);
    text('data-destination', path ? `${exists ? 'Replaces' : 'Saves to'} data/${path}` : 'Choose a file to see its destination.');
    text('data-upload-btn', exists ? 'Replace file' : 'Upload');
  }
  function setUploading(value) {
    uploading = value;
    for (const id of ['data-file-input', 'data-dest-input', 'data-restart-input', 'data-upload-btn', 'data-upload-cancel', 'data-upload-toggle']) el(id).disabled = value;
    el('data-progress').hidden = !value;
  }
  el('data-upload-form').addEventListener('submit', event => {
    event.preventDefault();
    if (!alive || !writable || uploading) return;
    const file = el('data-file-input').files?.[0]; if (!file) return;
    const rawPath = destination(); const invalid = validateDataPath(rawPath);
    const path = cleanPath(rawPath);
    if (invalid || !file.size) { status('data-upload-status', invalid || 'Choose a file that is not empty.', true); return; }
    const restart = el('data-restart-input').checked;
    const request = xhrFactory ? xhrFactory() : new win.XMLHttpRequest(); xhr = request;
    const current = () => alive && xhr === request;
    let settled = false;
    const finish = (message, error) => { if (!current() || settled) return; settled = true; setUploading(false); status('data-upload-status', message, error); };
    setUploading(true); el('data-progress').value = 0; status('data-upload-status', 'Uploading…');
    request.open('PUT', `${base}/data/${encodePath(path)}${restart ? '?restart=true' : ''}`);
    request.withCredentials = true;
    request.setRequestHeader('Content-Type', 'application/octet-stream');
    request.setRequestHeader('X-CSRF-Token', cookie('csrf_token'));
    request.upload.addEventListener('progress', event => {
      if (!current() || settled) return;
      if (event.lengthComputable) { el('data-progress').value = Math.round(event.loaded / event.total * 100); status('data-upload-status', event.loaded >= event.total ? 'Upload sent. Waiting for the server to save the file…' : `Uploading… ${el('data-progress').value}%`); }
      else el('data-progress').removeAttribute('value');
    });
    request.onload = () => {
      if (!current()) return;
      let body = {}; try { body = JSON.parse(request.responseText); } catch { /* A failed proxy may return plain text. */ }
      if (request.status === 401) { finish('Your session expired. Sign in again.', true); onUnauthorized(); return; }
      if (request.status < 200 || request.status >= 300) {
        const message = request.status === 413 && typeof body.QuotaBytes === 'number'
          ? `Upload would exceed the ${dataBytes(body.QuotaBytes)} app storage limit by ${dataBytes(Math.max(0, body.WouldBeBytes - body.QuotaBytes))}. The existing file was kept.`
          : messageOf(body, request.status === 0 ? 'Upload could not be confirmed. Refresh files before retrying.' : `Upload failed (${request.status}). Try again.`);
        finish(message, true); return;
      }
      if (typeof body.path !== 'string' || typeof body.size !== 'number') { finish('The server response could not confirm the saved file. Refresh files before retrying.', true); return; }
      finish(body.restart_error ? `File saved. App restart failed: ${body.restart_error}` : body.restarted ? 'File saved. App restarted.' : restart ? 'File saved. App was not restarted.' : 'File saved.', !!body.restart_error);
      el('data-upload-form').reset(); updateDestination();
      doc.dispatchEvent(new win.CustomEvent('shinyhub:mutated', {detail: {slug}}));
      refreshFiles();
    };
    const unknown = () => finish('Upload could not be confirmed. Refresh files before retrying.', true);
    request.onerror = unknown; request.ontimeout = unknown; request.onabort = unknown;
    try { request.send(file); } catch { unknown(); }
  });
  el('data-mount-form').addEventListener('submit', event => {
    event.preventDefault(); const source = el('data-source-input').value.trim();
    if (!source) return;
    if (source === slug) { status('data-mount-form-status', 'Choose another app as the source.', true); return; }
    mutate(`${base}/shared-data`, {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({source_slug: source})}, el('data-mount-btn'), 'data-mount-form-status', async body => {
      if (el('data-source-input').value.trim() === source) el('data-source-input').value = '';
      status('data-mount-form-status', `Mount configuration saved. Running apps are not restarted.${body.warning ? ` ${body.warning}` : ''}`);
      await refreshMounts();
    });
  });
  el('data-filter').addEventListener('input', () => { visible = 50; renderFiles(); });
  el('data-more').addEventListener('click', () => { visible += 50; renderFiles(); });
  el('data-file-input').addEventListener('change', updateDestination);
  el('data-dest-input').addEventListener('input', updateDestination);
  el('data-retry').addEventListener('click', refreshFiles);
  el('data-mount-retry').addEventListener('click', refreshMounts);
  el('data-refresh').addEventListener('click', async () => {
    el('data-refresh').disabled = true;
    await Promise.all([refreshFiles(), refreshMounts()]);
    if (alive) el('data-refresh').disabled = false;
  });
  el('data-upload-toggle').addEventListener('click', () => toggleForm('data-upload-form', 'data-upload-toggle', el('data-upload-form').hidden));
  el('data-upload-cancel').addEventListener('click', () => toggleForm('data-upload-form', 'data-upload-toggle', false));
  el('data-mount-toggle').addEventListener('click', () => toggleForm('data-mount-form', 'data-mount-toggle', el('data-mount-form').hidden));
  el('data-mount-cancel').addEventListener('click', () => toggleForm('data-mount-form', 'data-mount-toggle', false));
  refreshFiles(); refreshMounts();
  return () => { alive = false; filesRequest?.abort(); mountsRequest?.abort(); for (const controller of writes) controller.abort(); xhr?.abort(); };
}
