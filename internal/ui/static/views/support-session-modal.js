// Owns the security-sensitive pending/dismiss contract for support-session
// confirmation. Keeping it separate makes the behavior executable in tests
// instead of relying on source-string assertions in the SPA bundle.
export function createSupportSessionModalLock({ modal, closeButton, cancelButton }) {
  let pending = false;

  function setPending(next) {
    pending = !!next;
    closeButton.disabled = pending;
    cancelButton.disabled = pending;
    if (pending) modal.setAttribute('aria-busy', 'true');
    else modal.removeAttribute('aria-busy');
  }

  function requestDismiss(dismiss) {
    if (pending) return false;
    dismiss();
    return true;
  }

  return { setPending, requestDismiss, isPending: () => pending };
}

export const SUPPORT_DRAFT_KEY = 'pendingSupportSession';

// Orders async eligible-app loads even when two requests target the same user.
// Closing the dialog invalidates the generation and aborts the in-flight fetch.
export function createSupportAppRequestGate() {
  let generation = 0;
  let controller = null;
  return {
    begin() {
      controller?.abort();
      controller = new AbortController();
      return { generation: ++generation, signal: controller.signal };
    },
    isCurrent(candidate) { return candidate === generation; },
    invalidate() {
      controller?.abort();
      controller = null;
      generation++;
    },
  };
}

export function saveSupportDraft(storage, draft) {
  try {
    storage.setItem(SUPPORT_DRAFT_KEY, JSON.stringify(draft));
    return true;
  } catch {
    return false;
  }
}

export function consumeSupportDraft(storage) {
  let draft = null;
  try {
    draft = JSON.parse(storage.getItem(SUPPORT_DRAFT_KEY) || 'null');
    storage.removeItem(SUPPORT_DRAFT_KEY);
  } catch {
    try { storage.removeItem(SUPPORT_DRAFT_KEY); } catch { /* unavailable */ }
  }
  return draft;
}

export function isFreshSupportDraft(draft, now = Date.now(), maxAgeMS = 10 * 60 * 1000) {
  return !!draft && Number.isFinite(draft.saved_at) && draft.saved_at <= now && now - draft.saved_at <= maxAgeMS;
}

// Restores the immutable app scope after a reauthentication round trip. If
// eligibility changed, require an explicit replacement choice instead of
// silently falling back to the first app in the list.
export function restoreSupportAppSelection(select, submit, appSlug, { focusOnMissing = false } = {}) {
  const matchingOption = [...select.options].find(option => option.value === appSlug && !option.disabled);
  if (matchingOption) {
    select.value = appSlug;
    select.removeAttribute('aria-invalid');
    select.removeAttribute('aria-errormessage');
    return true;
  }
  const placeholder = select.ownerDocument.createElement('option');
  placeholder.value = '';
  placeholder.textContent = 'Choose an app — previous selection unavailable';
  placeholder.disabled = true;
  placeholder.selected = true;
  select.prepend(placeholder);
  select.value = '';
  select.setAttribute('aria-invalid', 'true');
  select.setAttribute('aria-errormessage', 'support-session-error');
  submit.disabled = true;
  if (focusOnMissing) select.focus();
  return false;
}

export function clearSupportAppOwnedError(select, error) {
  const ownsError = select.getAttribute('aria-invalid') === 'true';
  select.removeAttribute('aria-invalid');
  select.removeAttribute('aria-errormessage');
  if (ownsError) {
    error.textContent = '';
    error.hidden = true;
  }
  return ownsError;
}

export function restoreFailedActionFocus(action, ownedFocus) {
  if (!ownedFocus) return false;
  const active = action.ownerDocument.activeElement;
  if (active !== action && active !== action.ownerDocument.body) return false;
  action.focus();
  return true;
}
