// Render-pacing view helpers: the Configuration tab's pacing control, kept pure
// and testable. The wiring inside app.js is pinned by
// internal/ui/contract_test.go because the SPA bundle cannot be imported under
// jsdom.
//
// Pacing is a single number, apps.render_seconds, but it is the one setting an
// operator cannot sanity-check by eye: whether 1.2 is reasonable depends on the
// host's cores and on the session cap the app already carries. So the control
// pairs the input with the server's own advisory (the render_pacing block that
// GET and PATCH /api/apps/:slug both return) rather than making the operator
// derive the arithmetic.

// RENDER_SECONDS_MAX mirrors the server-side bound in handlePatchApp
// (internal/api/apps.go): render_seconds must be between 0 and 600. Duplicated
// deliberately so the form rejects an out-of-range value before the round trip;
// the server remains the authority.
export const RENDER_SECONDS_MAX = 600;

// parseRenderSeconds validates the raw input value and returns either a finite
// number to send or the message to show.
//
// An empty field is deliberately NOT read as 0. Blank and zero look the same in
// a number input but mean different things to an operator ("I have not decided"
// versus "pacing off"), and silently persisting a 0 for a blank would turn an
// accidental clear into a config change. Blank is rejected with a message that
// names the value that actually turns pacing off.
export function parseRenderSeconds(raw) {
  const text = String(raw ?? '').trim();
  if (text === '') {
    return { ok: false, error: 'Enter a render cost in seconds. Use 0 to turn pacing off.' };
  }
  const value = Number(text);
  if (!Number.isFinite(value)) {
    return { ok: false, error: 'Render cost must be a number.' };
  }
  if (value < 0 || value > RENDER_SECONDS_MAX) {
    return { ok: false, error: `Render cost must be between 0 and ${RENDER_SECONDS_MAX} seconds.` };
  }
  return { ok: true, value };
}

// formatSeconds renders a render_seconds value without trailing zeros, so 1.5
// stays "1.5" and 2.0 reads "2" rather than "2.0".
function formatSeconds(value) {
  return String(Number(value.toFixed(3)));
}

// renderPacingAdvice returns the advisory line for the pacing control, given the
// value currently in the input and the server's render_pacing block (absent when
// the SAVED value is 0, since the server computes the block from what is
// persisted).
//
// The block is deliberately treated as describing the saved state, not the typed
// one. While an operator is mid-edit the two disagree, and quoting a suggestion
// derived from the old number against the new one would be worse than quoting
// nothing: it reads as a prediction. So a typed value that differs from the
// block's says only what saving will do.
export function renderPacingAdvice(typedSeconds, block) {
  const typed = Number(typedSeconds);
  const b = block && typeof block === 'object' ? block : null;

  if (!Number.isFinite(typed)) return '';
  if (typed <= 0) {
    return b
      ? 'Saving 0 turns pacing off. Page loads will be admitted as fast as they arrive.'
      : 'Pacing is off. Page loads are admitted as fast as they arrive.';
  }
  if (!b) {
    return `Saving turns pacing on at ${formatSeconds(typed)}s per render.`;
  }
  const saved = Number(b.render_seconds);
  if (Number.isFinite(saved) && Math.abs(saved - typed) > 1e-9) {
    return `Saving changes pacing from ${formatSeconds(saved)}s to ${formatSeconds(typed)}s per render.`;
  }

  const cores = Number(b.effective_cores);
  const source = b.cores_source ? ` (${b.cores_source})` : '';
  const suggested = Number(b.suggested_max_sessions_per_replica);
  const current = Number(b.current_effective_max_sessions_per_replica);
  const cadence = Number(b.cadence_assumption_seconds);

  const parts = [];
  if (Number.isFinite(cores) && cores > 0) {
    parts.push(`Paced for ~${formatSeconds(cores)} cores${source}.`);
  } else {
    parts.push('Pacing is on.');
  }
  if (Number.isFinite(suggested) && suggested > 0 && Number.isFinite(current)) {
    // A current cap of 0 is unlimited (the app inherits a runtime default of 0),
    // which exceeds any finite suggestion. Comparing it numerically would read it
    // as the tightest cap possible and congratulate the least protected app there
    // is, so it takes the lowering branch and is named rather than printed as 0.
    if (current <= 0) {
      parts.push(
        `For sustained load, consider setting max sessions per replica to ${suggested} (currently unlimited).`,
      );
    } else if (suggested < current) {
      parts.push(
        `For sustained load, consider lowering max sessions per replica to ${suggested} (currently ${current}).`,
      );
    } else {
      parts.push(`The current max of ${current} sessions per replica is within what this host sustains.`);
    }
    if (Number.isFinite(cadence) && cadence > 0) {
      parts.push(`Assumes one render per session every ${formatSeconds(cadence)}s.`);
    }
  }
  return parts.join(' ');
}
