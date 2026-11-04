// Native popovers escape the rail without escaping the phone drawer's DOM
// focus boundary. Account and Help share interaction behavior, not contents.
export function installSidebarAccount(doc, win) {
  const entries = [['identity-card', 'account-menu'], ['help-button', 'help-menu']].map(([triggerId, menuId]) => ({
    trigger: doc.getElementById(triggerId), menu: doc.getElementById(menuId), open: false,
  }));
  function closeEntry(entry, restoreFocus = true) {
    if (!entry.open) return;
    entry.menu.hidePopover();
    if (restoreFocus) entry.trigger.focus({ preventScroll: true });
  }
  const close = () => entries.forEach(entry => closeEntry(entry));
  for (const entry of entries) {
    const { trigger, menu } = entry;
    menu.addEventListener('beforetoggle', event => {
      entry.open = event.newState === 'open';
      trigger.setAttribute('aria-expanded', String(entry.open));
      if (!entry.open) return;
      for (const other of entries) if (other !== entry) closeEntry(other, false);
      const rect = trigger.getBoundingClientRect();
      const width = Math.min(260, win.innerWidth - 24);
      menu.style.left = `${Math.max(12, Math.min(rect.left, win.innerWidth - width - 12))}px`;
      menu.style.bottom = `${Math.max(12, win.innerHeight - rect.top + 8)}px`;
      menu.style.maxHeight = `${Math.max(44, rect.top - 20)}px`;
    });
    // A dialog must capture the visible opener rather than its hidden menu item.
    menu.addEventListener('click', event => {
      if (event.target.closest('a,button')) closeEntry(entry);
    }, true);
    menu.addEventListener('focusout', event => {
      if (event.relatedTarget && !menu.contains(event.relatedTarget) && event.relatedTarget !== trigger) closeEntry(entry, false);
    });
  }
  doc.addEventListener('keydown', event => {
    if (event.key !== 'Escape' || !entries.some(entry => entry.open)) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    close();
  }, true);
  win.addEventListener('resize', close);
  return { close };
}
