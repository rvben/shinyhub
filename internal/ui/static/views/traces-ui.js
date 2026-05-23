// Traces panel rendering helpers. Pure functions + DOM builders that take an
// explicit document (same contract as fleet-ui.js / deploy-summary.js) so the
// whole module is unit-testable under jsdom. app-detail.js wires these into the
// live traces table; the wiring is pinned by string-search in contract_test.go.

const TRACE_HEX_SHORT = 8;

// shortHex renders a display-only prefix of a trace/span id. Returns '' for an
// absent id so callers can branch on a single value.
export function shortHex(s) {
  if (!s) return '';
  if (s.length <= 12) return s;
  return s.slice(0, TRACE_HEX_SHORT) + '…';
}

// formatTraceWhen renders a span's start time for the "When" column. The ring
// buffer can span days, so a same-day span shows time only while any other day
// includes the date - otherwise yesterday's 09:30 error is indistinguishable
// from today's. now is injected for deterministic tests.
export function formatTraceWhen(startedAt, now = new Date()) {
  if (!startedAt) return '—';
  const d = new Date(startedAt);
  if (Number.isNaN(d.getTime())) return '—';
  const sameDay =
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate();
  return sameDay ? d.toLocaleTimeString() : d.toLocaleString();
}

// formatPollStatus describes how fresh the last successful poll is, for the
// traces-status element. Returns '' for an absent/invalid timestamp so the
// element stays blank before the first load. now is injected for tests.
export function formatPollStatus(lastUpdated, now = new Date()) {
  if (!lastUpdated) return '';
  const then = lastUpdated instanceof Date ? lastUpdated : new Date(lastUpdated);
  if (Number.isNaN(then.getTime())) return '';
  const secs = Math.max(0, Math.round((now.getTime() - then.getTime()) / 1000));
  if (secs < 5) return 'updated just now';
  if (secs < 60) return `updated ${secs}s ago`;
  return `updated ${Math.floor(secs / 60)}m ago`;
}

export const UNSAMPLED_TITLE =
  'This request was not sampled, so no trace was exported to your backend. ' +
  'A deep link would resolve to "trace not found", so the id is shown without one.';

// appendTraceCell fills the Trace column for one span:
//   - no trace id            -> em-dash placeholder
//   - unsampled span         -> muted short id tagged "(unsampled)", never a
//                               deep link (the backend never received it)
//   - sampled + link tmpl    -> anchor deep-linking into the backend
//   - sampled, no link tmpl  -> short id in <code>
// Using DOM properties (textContent / a.href) keeps the markup inert, so no
// manual escaping is needed.
function appendTraceCell(doc, td, span, linkTpl) {
  if (!span.trace_id) {
    td.textContent = '—';
    return;
  }
  const short = shortHex(span.trace_id);
  if (span.sampled === false) {
    const code = doc.createElement('code');
    code.className = 'trace-unsampled';
    code.textContent = short;
    code.title = UNSAMPLED_TITLE;
    const tag = doc.createElement('span');
    tag.className = 'trace-unsampled-tag';
    tag.textContent = ' (unsampled)';
    tag.title = UNSAMPLED_TITLE;
    td.append(code, tag);
    return;
  }
  if (linkTpl) {
    const a = doc.createElement('a');
    a.href = linkTpl.replace('{trace_id}', span.trace_id);
    a.target = '_blank';
    a.rel = 'noopener';
    a.textContent = short;
    td.appendChild(a);
    return;
  }
  const code = doc.createElement('code');
  code.textContent = short;
  td.appendChild(code);
}

// makeTraceRow builds a complete <tr> for one span. 5xx or errored spans get
// the error row class. now is injected so the When column is deterministic.
export function makeTraceRow(doc, span, linkTpl, now = new Date()) {
  const tr = doc.createElement('tr');
  tr.className = (span.status >= 500 || span.error)
    ? 'replica-row replica-row-error'
    : 'replica-row';

  const tdWhen = doc.createElement('td');
  tdWhen.textContent = formatTraceWhen(span.started_at, now);

  const tdMethod = doc.createElement('td');
  tdMethod.textContent = span.method || '';

  const tdPath = doc.createElement('td');
  const code = doc.createElement('code');
  code.textContent = span.path || '';
  tdPath.appendChild(code);

  const tdStatus = doc.createElement('td');
  tdStatus.textContent = span.status ? String(span.status) : '—';

  const tdDur = doc.createElement('td');
  tdDur.textContent = `${span.duration_ms} ms`;

  const tdReplica = doc.createElement('td');
  tdReplica.textContent = span.replica >= 0 ? '#' + span.replica : '—';

  const tdTrace = doc.createElement('td');
  appendTraceCell(doc, tdTrace, span, linkTpl);

  tr.append(tdWhen, tdMethod, tdPath, tdStatus, tdDur, tdReplica, tdTrace);
  return tr;
}
