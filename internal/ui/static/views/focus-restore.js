// Rebuilding a region replaces the element that held keyboard focus, and a
// detached element cannot hold it: the browser drops focus to <body>, so the
// next Tab starts again from the top of the page and a screen reader loses its
// place. Someone who was three cards down the grid when a save rebuilt it has
// no way back except to tab through everything above.
//
// A region that rebuilds itself therefore tags its focusable controls with
// data-focus-key, reads the key of the focused control before the rebuild, and
// hands focus back to whichever control carries that key afterwards.
//
// The key names what a control DOES ("app:demo:menu"), never where it sits, so
// it survives a rebuild that reorders, regroups or filters the region around
// it. A key with no match after the rebuild means that control is genuinely
// gone (the app was deleted, the filter now excludes it); restoreFocus reports
// that with false so the caller can choose a fallback rather than guess.

// focusedKey returns the data-focus-key of the focused element when focus is
// inside root, and null when it is anywhere else. A caller that captures null
// had no focus to preserve, so it must not move focus during the rebuild.
export function focusedKey(root) {
  if (!root) return null;
  const active = (root.ownerDocument || document).activeElement;
  return active && root.contains(active) ? active.dataset.focusKey || null : null;
}

// siblingKey rewrites a "<kind>:<subject>:<control>" key to name a different
// control on the same subject, so a caller whose exact control did not survive
// the rebuild can fall back to one that did: a card's "Deploy first release"
// button disappears the moment that deploy succeeds, but the card is still
// there and its title link can hold the focus. Returns null for a key that does
// not name a control, which restoreFocus treats as "nothing to restore".
export function siblingKey(key, control) {
  const match = /^(.+):[^:]+$/.exec(key || '');
  return match ? `${match[1]}:${control}` : null;
}

// restoreFocus gives focus to the control in root carrying key, and reports
// whether it found one. preventScroll keeps the rebuild from also scrolling the
// page: the control is where it was, and jumping the viewport to it would be a
// second surprise on top of the re-render.
export function restoreFocus(root, key) {
  if (!root || !key) return false;
  const match = [...root.querySelectorAll('[data-focus-key]')]
    .find((node) => node.dataset.focusKey === key);
  if (!match) return false;
  match.focus({ preventScroll: true });
  return true;
}
