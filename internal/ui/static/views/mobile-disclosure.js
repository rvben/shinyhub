// One set of controls, expanded on desktop and disclosed on phones. Native
// details keeps the reading order and keyboard behavior intact in either mode.
export function installMobileDisclosures(doc, win) {
  const media = win.matchMedia('(max-width: 640px)');
  const known = new WeakSet();
  const selector = 'details[data-mobile-disclosure]';
  function close(detail, restoreFocus = false) {
    detail.open = false;
    if (restoreFocus) detail.querySelector('summary')?.focus();
  }
  function sync(reset = false) {
    for (const detail of doc.querySelectorAll(selector)) {
      if (!known.has(detail) || reset) {
        const hidesFocus = media.matches && detail.contains(doc.activeElement);
        const focusedSummary = !media.matches && doc.activeElement === detail.querySelector('summary');
        detail.open = !media.matches;
        if (hidesFocus) detail.querySelector('summary')?.focus();
        if (focusedSummary) detail.querySelector('button:not([hidden]), a:not([hidden]), input, select')?.focus();
        known.add(detail);
      }
      if (detail.hasAttribute('data-hide-empty')) {
        const available = [...detail.querySelectorAll('a, button')].some(el => !el.hidden);
        if (detail.hidden === available) detail.hidden = !available;
      }
    }
  }
  function onChange() { sync(true); }
  function onKey(event) {
    if (!media.matches || event.key !== 'Escape') return;
    const detail = event.target.closest(`${selector}[open]`);
    if (detail) { event.preventDefault(); event.stopImmediatePropagation(); close(detail, true); }
  }
  function onClick(event) {
    if (!media.matches) return;
    for (const detail of doc.querySelectorAll('.page-secondary[open], .apps-filter-options[open]')) {
      if (!detail.contains(event.target)) close(detail, detail.contains(doc.activeElement));
      else if (event.target.closest('button, a')) {
        // Keep the opener available while a dialog owns focus, so closing the
        // dialog can restore focus to it. Ordinary actions close the menu.
        if (detail.contains(doc.activeElement) || doc.activeElement === doc.body) close(detail, true);
      }
    }
  }
  sync();
  const observer = new win.MutationObserver(() => sync());
  observer.observe(doc.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['hidden'] });
  media.addEventListener('change', onChange);
  doc.addEventListener('keydown', onKey);
  doc.addEventListener('click', onClick);
  return () => {
    observer.disconnect();
    media.removeEventListener('change', onChange);
    doc.removeEventListener('keydown', onKey);
    doc.removeEventListener('click', onClick);
  };
}
