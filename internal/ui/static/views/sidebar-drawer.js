// Mobile off-canvas sidebar drawer controller.
//
// The open/close/onNavigated logic is isolated here (DOM nodes + a focus-trap
// factory injected) so it is unit-testable without the app.js IIFE — in
// particular the veto-keeps-open behavior: the drawer is closed ONLY by an
// explicit user action (toggle/backdrop/Escape) or the post-mount onNavigated()
// hook. It never closes on a raw nav click, because a guard-vetoed navigation
// never mounts and must leave the drawer open so the user keeps context.
export function createSidebarDrawer(opts) {
  const { body, toggle, backdrop, sidebar, content, createFocusTrap, doc } = opts || {};
  const ownerDoc = doc || (typeof document !== 'undefined' ? document : null);
  let trap = null;

  const isOpen = () => body.classList.contains('sidebar-open');

  function open() {
    if (isOpen()) return;
    body.classList.add('sidebar-open');
    if (toggle) toggle.setAttribute('aria-expanded', 'true');
    if (backdrop) backdrop.hidden = false;
    if (content) content.setAttribute('inert', '');
    if (typeof createFocusTrap === 'function' && sidebar) {
      trap = createFocusTrap(sidebar, ownerDoc);
      trap.activate();
    }
    // Move focus into the drawer so keyboard/screen-reader users land inside it
    // rather than on the hamburger in the (now inert-adjacent) top bar.
    if (sidebar) {
      const first = sidebar.querySelector('a[href], button:not([disabled])');
      if (first && typeof first.focus === 'function') first.focus();
    }
  }

  function close() {
    if (!isOpen()) return;
    body.classList.remove('sidebar-open');
    if (toggle) toggle.setAttribute('aria-expanded', 'false');
    if (backdrop) backdrop.hidden = true;
    if (content) content.removeAttribute('inert');
    if (trap) { trap.release(); trap = null; } // release() restores focus to the opener
  }

  function toggleDrawer() { isOpen() ? close() : open(); }

  // Post-mount hook: close after an allowed navigation. A vetoed navigation
  // never mounts, so this never fires and the drawer stays open.
  function onNavigated() { if (isOpen()) close(); }

  return { open, close, toggle: toggleDrawer, onNavigated, isOpen };
}
