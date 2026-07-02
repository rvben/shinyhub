// API Tokens view — pure list models + a DOM renderer for the /tokens page.
//
// Self-contained (no imports) so it is unit-testable with jsdom, following the
// repo's testable-view pattern. relativeLabel mirrors deployment-row.js's
// relativeTime; kept local to avoid a cross-module import (jsdom-tested views
// stay leaf modules).

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

// tokenListModels turns the raw /api/tokens array ({id, name, created_at}) into
// view models sorted newest-first, each with a human "created X ago" label and
// the raw ISO for a tooltip. Returns [] for null/empty input.
export function tokenListModels(tokens, now = Date.now()) {
  if (!Array.isArray(tokens)) return [];
  return tokens
    .slice()
    .sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime())
    .map((t) => ({
      id: t.id,
      name: t.name,
      createdLabel: relativeLabel(t.created_at, now),
      createdISO: t.created_at,
    }));
}

// renderTokenList replaces container's contents with one row per model (or a
// friendly empty state). Each row carries a Revoke button tagged with
// data-token-id / data-token-name so the caller can wire deletion by delegation.
// Names are set via textContent, so a hostile token name cannot inject markup.
export function renderTokenList(container, models, doc) {
  const d = doc || (typeof document !== 'undefined' ? document : null);
  if (!container || !d) return;
  container.textContent = '';

  if (!models || models.length === 0) {
    const empty = d.createElement('p');
    empty.className = 'tokens-empty';
    empty.setAttribute('data-tokens-empty', '');
    empty.textContent = 'No API tokens yet — create one to use the CLI or API.';
    container.appendChild(empty);
    return;
  }

  for (const m of models) {
    const row = d.createElement('div');
    row.className = 'token-row';
    row.setAttribute('data-token-row', '');

    const meta = d.createElement('div');
    meta.className = 'token-meta';
    const name = d.createElement('span');
    name.className = 'token-name';
    name.textContent = m.name;
    const created = d.createElement('span');
    created.className = 'token-created';
    created.textContent = `created ${m.createdLabel}`;
    created.title = m.createdISO;
    meta.appendChild(name);
    meta.appendChild(created);

    const revoke = d.createElement('button');
    revoke.type = 'button';
    revoke.className = 'btn-danger token-revoke';
    revoke.textContent = 'Revoke';
    revoke.setAttribute('data-token-id', String(m.id));
    revoke.setAttribute('data-token-name', m.name);
    revoke.setAttribute('aria-label', `Revoke token ${m.name}`);

    row.appendChild(meta);
    row.appendChild(revoke);
    container.appendChild(row);
  }
}
