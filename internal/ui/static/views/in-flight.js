// A mutating form or button click that awaits a network round trip can fire
// twice in the same tick: JS runs synchronously up to the first `await`, so
// nothing has visibly changed about the control yet, and a second click (or
// a stray Enter-key resubmit) reaches the handler again before the button
// has disabled. Two token-creation requests in the twenty milliseconds
// before the first response arrives create two tokens; the second one's
// secret is never shown, so it becomes a live credential nobody can see.
//
// runInFlight disables the submitter and marks it aria-busy before the
// action starts, treats a call that arrives while one is already pending on
// the same submitter as a no-op, and always restores the submitter
// afterwards, whether the action resolved or threw. A browser blurs a
// control the instant it is disabled, so it also restores focus to the
// submitter once it is usable again, if that is where focus was when the
// call began.

const pending = new WeakSet();

export function isInFlight(submitter) {
  return !!submitter && pending.has(submitter);
}

export async function runInFlight(submitter, action) {
  if (submitter && pending.has(submitter)) return undefined;
  if (submitter) pending.add(submitter);

  const doc = submitter && submitter.ownerDocument;
  const hadFocus = !!doc && doc.activeElement === submitter;
  const wasDisabled = !!submitter && submitter.disabled;
  if (submitter) {
    submitter.disabled = true;
    submitter.setAttribute('aria-busy', 'true');
  }

  try {
    return await action();
  } finally {
    if (submitter) {
      pending.delete(submitter);
      submitter.removeAttribute('aria-busy');
      if (!wasDisabled) {
        submitter.disabled = false;
        // Only reclaim focus if it is still sitting where the browser's
        // blur-on-disable left it (<body>). If the action itself moved focus
        // (closed a modal, navigated), or the user tabbed elsewhere while the
        // request was in flight, leave it alone.
        if (hadFocus && doc && doc.activeElement === doc.body) {
          submitter.focus({ preventScroll: true });
        }
      }
    }
  }
}
