// Schedule list surface helpers. Pure functions + an HTML-string builder
// (the schedule table in app.js is rendered as template strings, not DOM
// nodes), kept here so the DST advisory wiring is unit-testable under jsdom.
//
// The server computes the DST fall-back double-fire advisory and returns it on
// the schedule DTO as `dst_advisory`. This module only decides how to surface
// that string in the list; it never recomputes the advisory.

// Short label shown on the inline warning badge. The full advisory rides in
// the title + aria-label so the cell stays compact.
export const DST_ADVISORY_LABEL = 'fires twice (DST)';

// Returns the trimmed advisory text when the schedule carries one, else null
// so callers branch on a single value.
export function dstAdvisoryText(schedule) {
  if (!schedule || typeof schedule.dst_advisory !== 'string') return null;
  const text = schedule.dst_advisory.trim();
  return text.length > 0 ? text : null;
}

const ESCAPE = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };

function escapeHtml(value) {
  return String(value).replace(/[&<>"']/g, (c) => ESCAPE[c]);
}

// Returns an inline warning badge as an HTML string for embedding in the cron
// cell, or "" when the schedule has no advisory. The full advisory is escaped
// into both the title (hover) and aria-label (screen readers).
export function dstAdvisoryMarkup(schedule) {
  const text = dstAdvisoryText(schedule);
  if (text === null) return '';
  const safe = escapeHtml(text);
  return `<span class="dst-advisory" title="${safe}" aria-label="${safe}">⚠ ${DST_ADVISORY_LABEL}</span>`;
}
