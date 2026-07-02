// WAI-ARIA tablist keyboard navigation.
//
// createTablistNav wires the arrow-key interaction pattern onto a [role=tablist]
// element: ArrowRight/Down and ArrowLeft/Up move between visible [role=tab]
// children (wrapping), Home/End jump to the first/last visible tab, and hidden
// tabs are skipped. Activation follows focus (the SPA model: each tab is a
// route), so the destination tab is focused and then activated via onActivate.
// A roving tabindex keeps only the focused tab in the page Tab order.
//
// nextTabIndex is the pure index resolver, unit-tested independently of the DOM.

const NAV_KEYS = new Set(['ArrowRight', 'ArrowLeft', 'ArrowUp', 'ArrowDown', 'Home', 'End']);

// nextTabIndex returns the destination tab index for key given the hidden-flag
// array and the currently focused index, or -1 when the key does not move focus
// (non-navigation key, or no visible tabs). Movement wraps and skips hidden tabs.
export function nextTabIndex(hidden, current, key) {
  const visible = [];
  for (let i = 0; i < hidden.length; i++) {
    if (!hidden[i]) visible.push(i);
  }
  if (visible.length === 0) return -1;
  const n = visible.length;
  const pos = visible.indexOf(current);
  switch (key) {
    case 'ArrowRight':
    case 'ArrowDown':
      return pos === -1 ? visible[0] : visible[(pos + 1) % n];
    case 'ArrowLeft':
    case 'ArrowUp':
      return pos === -1 ? visible[n - 1] : visible[(pos - 1 + n) % n];
    case 'Home':
      return visible[0];
    case 'End':
      return visible[n - 1];
    default:
      return -1;
  }
}

export function createTablistNav(tablist, doc, opts = {}) {
  const ownerDoc = doc || (typeof document !== 'undefined' ? document : null);
  const onActivate = typeof opts.onActivate === 'function' ? opts.onActivate : () => {};
  if (!tablist || !ownerDoc) return { destroy() {} };

  const handler = (e) => {
    if (!NAV_KEYS.has(e.key)) return;
    const tabs = Array.from(tablist.querySelectorAll('[role="tab"]'));
    const current = tabs.indexOf(ownerDoc.activeElement);
    if (current === -1) return; // focus isn't on a tab; leave the event alone
    const dest = nextTabIndex(tabs.map((t) => t.hidden), current, e.key);
    if (dest === -1) return;
    // Handled: stop the arrow from also scrolling the tab strip / page.
    e.preventDefault();
    if (dest === current) return; // already on the only/target tab
    const el = tabs[dest];
    for (const t of tabs) t.setAttribute('tabindex', t === el ? '0' : '-1');
    el.focus();
    onActivate(el);
  };

  tablist.addEventListener('keydown', handler);
  return {
    destroy() {
      tablist.removeEventListener('keydown', handler);
    },
  };
}
