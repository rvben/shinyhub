const ESCAPE = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };

// Escapes a value for interpolation into HTML text or a quoted attribute.
export function escapeHtml(value) {
  return String(value).replace(/[&<>"']/g, (c) => ESCAPE[c]);
}
