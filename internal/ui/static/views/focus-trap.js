// Focus management for modal dialogs.
//
// createFocusTrap confines Tab/Shift+Tab focus to a container while a modal is
// open and restores focus to the element that opened it on release. Escape
// dismissal is intentionally left to the app's single global keydown handler so
// closing logic lives in one place.

const FOCUSABLE_SELECTOR = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',');

// focusableElements returns the tabbable descendants of container in document
// order, skipping disabled controls, aria-hidden nodes, and anything inside a
// [hidden] subtree (e.g. a not-yet-revealed hand-off section).
export function focusableElements(container) {
  if (!container || typeof container.querySelectorAll !== 'function') return [];
  return Array.from(container.querySelectorAll(FOCUSABLE_SELECTOR)).filter((el) => {
    if (el.hasAttribute('disabled')) return false;
    if (el.getAttribute('aria-hidden') === 'true') return false;
    for (let node = el; node && node !== container.parentElement; node = node.parentElement) {
      if (node.hidden) return false;
    }
    return true;
  });
}

export function createFocusTrap(container, doc) {
  const ownerDoc = doc || (typeof document !== 'undefined' ? document : null);
  let handler = null;
  let returnTo = null;

  return {
    // activate captures the currently-focused element (to restore later) and
    // starts intercepting Tab so focus wraps within the container.
    activate() {
      if (handler || !ownerDoc) return;
      returnTo = ownerDoc.activeElement;
      handler = (e) => {
        if (e.key !== 'Tab') return;
        const items = focusableElements(container);
        if (items.length === 0) {
          e.preventDefault();
          return;
        }
        const first = items[0];
        const last = items[items.length - 1];
        const active = ownerDoc.activeElement;
        const outside = !container.contains(active);
        if (e.shiftKey) {
          if (active === first || outside) {
            e.preventDefault();
            last.focus();
          }
        } else if (active === last || outside) {
          e.preventDefault();
          first.focus();
        }
      };
      ownerDoc.addEventListener('keydown', handler, true);
    },

    // release stops trapping and returns focus to where it was on activate.
    release() {
      if (handler && ownerDoc) {
        ownerDoc.removeEventListener('keydown', handler, true);
      }
      handler = null;
      if (returnTo && typeof returnTo.focus === 'function') {
        returnTo.focus();
      }
      returnTo = null;
    },
  };
}
