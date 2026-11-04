// Token inventory presentation. Credentials never belong in these models.
// relativeLabel formats an ISO timestamp as a short "Xs/m/h/d ago" string.
function relativeLabel(iso, now) {
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return '';
  const diff = Math.floor((now - t) / 1000);
  if (diff < 0) return 'just now';
  if (diff < 60) return `${diff}s ago`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}

// futureLabel formats an ISO timestamp ahead of now as "in Xs/m/h/d".
function futureLabel(iso, now) {
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return '';
  const diff = Math.floor((t - now) / 1000);
  if (diff < 60) return `in ${diff}s`;
  if (diff < 3600) return `in ${Math.floor(diff / 60)}m`;
  if (diff < 86400) return `in ${Math.floor(diff / 3600)}h`;
  return `in ${Math.ceil(diff / 86400)}d`;
}

// tokenListModels turns the raw /api/tokens array
// ({id, name, created_at, expires_at, last_used_at}) into view models sorted
// newest-first, each with human labels: "created X ago", an expiry label
// ("expires in Xd" / "expired" / "" for never), an expired flag, and a
// last-use label ("last used X ago" / "never used"). Returns [] for
// null/empty input.
export function tokenListModels(tokens, now = Date.now()) {
  if (!Array.isArray(tokens)) return [];
  return tokens
    .slice()
    .sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime())
    .map((t) => {
      const expiresMs = t.expires_at ? new Date(t.expires_at).getTime() : NaN;
      const expired = Number.isFinite(expiresMs) && expiresMs <= now;
      let expiresLabel = '';
      if (Number.isFinite(expiresMs)) {
        expiresLabel = expired ? 'expired' : `expires ${futureLabel(t.expires_at, now)}`;
      }
      return {
        id: t.id,
        name: t.name,
        createdLabel: relativeLabel(t.created_at, now),
        createdISO: t.created_at,
        expiresLabel,
        expiresISO: t.expires_at || '',
        expired,
        expiresDate: tokenDate(t.expires_at),
        lastUsedDate: tokenDate(t.last_used_at),
        lastUsedLabel: t.last_used_at ? `last used ${relativeLabel(t.last_used_at, now)}` : 'never used',
        lastUsedISO: t.last_used_at || '',
      };
    });
}

export function tokenDate(value) {
  const date = value ? new Date(value) : null;
  return date && Number.isFinite(date.getTime())
    ? new Intl.DateTimeFormat(undefined, {year:'numeric',month:'short',day:'numeric'}).format(date) : '';
}

function sentence(label, prefix = '') {
  const text = prefix && label.startsWith(prefix) ? label.slice(prefix.length) : label;
  return text ? text[0].toUpperCase() + text.slice(1) : '';
}

// Time passing changes labels, not the controls or confirmation in progress.
export function updateTokenTimes(container, models) {
  const byID = new Map(models.map(model => [String(model.id),model]));
  for (const row of container.querySelectorAll('[data-token-row]')) {
    const model = byID.get(row.dataset.tokenRow); if (!model) continue;
    const update = (selector,text) => {
      const node = row.querySelector(selector);
      if (node.textContent !== text) node.textContent = text;
    };
    update('.token-created',model.createdLabel ? `Created ${model.createdLabel}` : 'Created time unavailable');
    update('.token-expires',sentence(model.expiresLabel,'expires ') || (model.expiresISO ? 'Date unavailable' : 'No expiry'));
    update('.token-last-used',sentence(model.lastUsedLabel,'last used '));
    row.classList.toggle('token-expired',model.expired);
    row.querySelector('.token-expires').classList.toggle('token-expires-past',model.expired);
  }
}

// The controller owns requests and confirmation state; this renderer only
// presents safe metadata and native controls. It never receives a raw token.
export function renderTokenList(container, models, doc, options = {}) {
  const d = doc || container?.ownerDocument;
  if (!container || !d) return;
  const node = (tag, className, value) => {
    const el = d.createElement(tag); if (className) el.className = className;
    if (value !== undefined) el.textContent = value;
    return el;
  };
  container.replaceChildren();
  if (!models?.length) {
    const empty = node('div','tokens-empty'); empty.dataset.tokensEmpty = '';
    empty.append(node('h3','','No API tokens yet'));
    empty.append(node('p','', options.mutable === false
      ? 'This shared account cannot create API tokens.'
      : 'Create a token for scripts and integrations, or connect your CLI with a verification code.'));
    if (options.mutable !== false) {
      const connect = node('button','ov-btn','Connect your CLI'); connect.type = 'button'; connect.dataset.tokenConnect = '';
      empty.append(connect);
    }
    container.append(empty); return;
  }
  const table = node('table','token-table'); table.setAttribute('aria-label','Personal API tokens');
  const head = table.createTHead().insertRow();
  for (const title of ['Token','Expires','Last used','Actions']) {
    const th = node('th','',title); th.scope = 'col'; head.append(th);
  }
  const body = table.createTBody();
  for (const m of models) {
    const row = body.insertRow(); row.className = m.expired ? 'token-row token-expired' : 'token-row'; row.dataset.tokenRow = String(m.id);
    const meta = row.insertCell(); meta.className = 'token-meta'; meta.dataset.label = 'Token';
    meta.append(node('span','token-name',m.name),node('span','token-created',m.createdLabel ? `Created ${m.createdLabel}` : 'Created time unavailable'));
    meta.lastElementChild.title = m.createdISO || '';
    const expires = row.insertCell(); expires.dataset.label = 'Expires';
    expires.append(node('span',m.expired ? 'token-expires token-expires-past' : 'token-expires',sentence(m.expiresLabel,'expires ') || (m.expiresISO ? 'Date unavailable' : 'No expiry')));
    if (m.expiresDate) expires.append(node('time','token-secondary',m.expiresDate));
    if (m.expiresISO && m.expiresDate) expires.lastElementChild.dateTime = m.expiresISO;
    const used = row.insertCell(); used.dataset.label = 'Last used';
    used.append(node('span','token-last-used',sentence(m.lastUsedLabel,'last used ')));
    if (m.lastUsedDate) { const time = node('time','token-secondary',m.lastUsedDate); time.dateTime = m.lastUsedISO; used.append(time); }
    const actions = row.insertCell(); actions.className = 'token-actions';
    if (options.mutable !== false) {
      const revoke = node('button','btn-row token-revoke','Revoke'); revoke.type = 'button';
      revoke.dataset.tokenId = String(m.id); revoke.dataset.tokenName = m.name;
      revoke.dataset.focusKey = `token:${m.id}:revoke`;
      revoke.setAttribute('aria-label',`Revoke token ${m.name}`);
      revoke.setAttribute('aria-expanded',String(options.confirming?.id === m.id));
      revoke.setAttribute('aria-controls',`token-confirm-${m.id}`);
      revoke.disabled = !!options.confirming?.pending;
      actions.append(revoke);
    }
    if (options.confirming?.id === m.id) {
      const state = options.confirming;
      const confirmRow = body.insertRow(); confirmRow.className = 'token-confirm-row';
      const cell = confirmRow.insertCell(); cell.colSpan = 4;
      const panel = node('section','token-confirm'); panel.id = `token-confirm-${m.id}`;
      panel.setAttribute('aria-label',`Revoke token ${m.name}`); panel.setAttribute('aria-busy',String(!!state.pending));
      panel.append(node('p','token-confirm-title',`Revoke “${m.name}”?`),node('p','','Any CLI or integration using this token will lose access immediately.'));
      const error = node('p','error',state.error || ''); error.hidden = !state.error; error.setAttribute('role','alert'); panel.append(error);
      const buttons = node('div','token-confirm-actions');
      const cancel = node('button','btn-row','Cancel'); cancel.type = 'button'; cancel.dataset.tokenCancel = ''; cancel.dataset.focusKey = `token:${m.id}:cancel`;
      const confirm = node('button','btn-danger',state.pending ? 'Revoking…' : 'Revoke token'); confirm.type = 'button'; confirm.dataset.tokenConfirm = String(m.id); confirm.dataset.focusKey = `token:${m.id}:confirm`;
      cancel.disabled = confirm.disabled = !!state.pending; buttons.append(cancel,confirm); panel.append(buttons); cell.append(panel);
    }
  }
  container.append(table);
}
