// wireKebab wires a kebab "⋯" toggle to its dropdown list: click toggles
// open/closed, Escape and outside-click close it, clicking any menu item closes
// it, and aria-expanded stays in sync. The optional container gets the
// `kebab-open` class while open so CSS can lift it above its neighbours (the
// dropdown is intentionally allowed to overflow its card). This is the single
// wiring path for the dashboard card kebab, the app-detail header kebab, and the
// schedule row and detail menus, so a keyboard fix made here reaches all four.
//
// The document-level listeners come from the button's own document rather than
// the global one, so the whole menu can be exercised inside jsdom.

// Whether closing the menu should hand the keyboard back to the toggle.
//
// A menu item's own click handler runs before the one that closes the list, and
// two of them place focus deliberately: Restart opens a confirmation on the card
// and focuses it, and the Sleep/Start path disables the item it was given, which
// drops focus to <body>. So "did the handler park focus somewhere real" is the
// question, and only two answers mean no: focus is still inside the list we are
// about to hide, or it has already fallen to <body>/nothing. Hiding an element
// that holds focus sends the keyboard to the top of the document, which is what
// this prevents; stealing focus from a confirmation the visitor just opened
// would be the same bug pointed the other way.
export function shouldReturnFocus(list, active) {
  if (!list) return false;
  const body = list.ownerDocument ? list.ownerDocument.body : null;
  if (!active || active === body) return true;
  return list.contains(active);
}

// Returns a handle so a caller that hides the menu (because the app's state no
// longer offers any action) can close it through the same setOpen that opened
// it, instead of writing `hidden`/aria-expanded/kebab-open a second time.
export function wireKebab(button, list, container) {
  if (!button || !list) return null;
  const doc = button.ownerDocument;
  const availableItems = () => [...list.querySelectorAll('button:not([disabled])')]
    .filter(item => !item.closest('[hidden]'));
  function onDocClick(e) {
    if (!list.contains(e.target) && !button.contains(e.target)) setOpen(false);
  }
  function onKey(e) {
    if (e.key === 'Escape') {
      e.preventDefault();
      setOpen(false);
      button.focus();
      return;
    }
    if (e.key === 'Tab') {
      setOpen(false);
      return;
    }
    const items = availableItems();
    if (!items.length || !list.contains(doc.activeElement)) return;
    const current = Math.max(0, items.indexOf(doc.activeElement));
    let next = null;
    if (e.key === 'ArrowDown') next = items[(current + 1) % items.length];
    if (e.key === 'ArrowUp') next = items[(current - 1 + items.length) % items.length];
    if (e.key === 'Home') next = items[0];
    if (e.key === 'End') next = items.at(-1);
    if (next) {
      e.preventDefault();
      next.focus();
    }
  }
  function setOpen(open, focus = '') {
    list.hidden = !open;
    button.setAttribute('aria-expanded', String(open));
    if (container) container.classList.toggle('kebab-open', open);
    if (open) {
      doc.addEventListener('click', onDocClick, true);
      doc.addEventListener('keydown', onKey, true);
      const items = availableItems();
      if (focus === 'first') items[0]?.focus();
      if (focus === 'last') items.at(-1)?.focus();
    } else {
      doc.removeEventListener('click', onDocClick, true);
      doc.removeEventListener('keydown', onKey, true);
    }
  }
  button.addEventListener('click', (e) => {
    e.stopPropagation();
    const opening = list.hidden;
    setOpen(opening, opening ? 'first' : '');
  });
  button.addEventListener('keydown', (e) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    setOpen(true, e.key === 'ArrowDown' ? 'first' : 'last');
  });
  list.addEventListener('click', (e) => {
    if (!e.target.closest('button')) return;
    // Read focus before hiding the list, because hiding is what destroys it.
    const reclaim = shouldReturnFocus(list, doc.activeElement);
    setOpen(false);
    if (reclaim && button.isConnected && !button.disabled) button.focus();
  });
  return { close: () => setOpen(false) };
}
