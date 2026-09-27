// Bounded rendering for long app lists. The dashboard fetches the whole fleet
// once (search, sort and segment filters run client-side over it), but a
// fleet of thousands rendered as one card and one sidebar row per app builds
// a DOM of tens of thousands of nodes and stalls the main thread on every
// rebuild. Each group therefore renders a first page and a "Show more" button
// that reveals the next page; a fleet whose groups fit in one page renders
// exactly as before, with no button.

export const GRID_PAGE = 48;
export const SIDEBAR_PAGE = 50;

// How many items of a group to render: at least one page, more once the user
// has asked for more, never past the end. `pinned` names an index that must be
// visible (the app the current route points at), so a deep link to the
// 900th app still highlights its row.
export function windowCount(total, shown, page, pinned = -1) {
  const n = Math.max(0, total | 0);
  let limit = Math.max(page, shown | 0);
  if (pinned >= limit) limit = (Math.floor(pinned / page) + 1) * page;
  return Math.min(n, limit);
}

export function showMoreLabel(hidden, page, compact = false) {
  const next = Math.min(hidden, page);
  if (compact) return `Show ${next} more`;
  return `Show ${next} more (${hidden.toLocaleString('en-US')} not shown)`;
}

// Build the "Show more" button for a group with `hidden` unrendered items.
// Returns null when nothing is hidden, so callers append unconditionally.
// `compact` (the narrow sidebar) keeps the visible label to one line and moves
// the remainder into the tooltip and the accessible name.
export function createShowMore(doc, { hidden, page, className, focusKey, onMore, compact = false }) {
  if (hidden <= 0) return null;
  const button = doc.createElement('button');
  button.type = 'button';
  button.className = className;
  if (focusKey) button.dataset.focusKey = focusKey;
  button.textContent = showMoreLabel(hidden, page, compact);
  if (compact) {
    button.title = showMoreLabel(hidden, page);
    button.setAttribute('aria-label', button.title);
  }
  button.addEventListener('click', onMore);
  return button;
}
