// Quick-view log pane controller.
//
// Streams SSE log output into the full-viewport overlay used for a card's
// "View logs" kebab entry, the deploy modal's re-entry link, and a schedule
// run's history logs. Isolated here (DOM nodes + EventSource/focus-trap
// factories injected) so it is unit-testable without the app.js IIFE, and so
// both call sites share one implementation instead of duplicating it.
//
// Two behaviors are deliberately borrowed from the Logs tab viewer
// (views/logs-ui.js) rather than reinvented:
//   - Rendered lines are capped at MAX_RENDERED_LOG_ENTRIES using the same
//     appendBoundedLogEntry helper, so both viewers bound memory identically.
//   - onerror never closes the EventSource. The browser retries a dropped SSE
//     connection on its own; closing it here would turn a transient network
//     blip into a silently dead stream. Only a status line (a plain
//     role="status" region, not a stream of appended lines) reflects the
//     disconnected/reconnected state, so screen readers get one throttled
//     announcement instead of one per log line.
import { MAX_RENDERED_LOG_ENTRIES, appendBoundedLogEntry } from './logs-ui.js';

export { MAX_RENDERED_LOG_ENTRIES };

export function createLogPane(opts) {
  const {
    pane,
    title,
    body,
    status,
    closeButton,
    EventSourceClass = (typeof EventSource !== 'undefined' ? EventSource : null),
    createFocusTrap,
    doc,
    setHidden: setHiddenFn,
  } = opts || {};
  const ownerDoc = doc || (typeof document !== 'undefined' ? document : null);
  const hide = (el, hidden) => {
    if (!el) return;
    if (setHiddenFn) setHiddenFn(el, hidden);
    else el.hidden = hidden;
  };

  let es = null;
  let lines = [];
  let trap = null;

  function isOpen() {
    return !!pane && !pane.hidden;
  }

  function render() {
    if (body) body.textContent = lines.length ? `${lines.join('\n')}\n` : '';
  }

  function setStatus(text, cls) {
    if (!status) return;
    status.textContent = text || '';
    status.hidden = !text;
    status.className = cls ? `log-pane-status ${cls}` : 'log-pane-status';
  }

  function appendLine(text) {
    // Only follow new output if the viewer was already scrolled to the
    // bottom, so scrolling up to read earlier lines is not yanked back down
    // by the next message.
    const atBottom = body
      ? body.scrollHeight - Math.ceil(body.scrollTop) <= body.clientHeight + 1
      : true;
    appendBoundedLogEntry(lines, text, MAX_RENDERED_LOG_ENTRIES);
    render();
    if (body && atBottom) body.scrollTop = body.scrollHeight;
  }

  function stopStream() {
    if (!es) return;
    es.onopen = null;
    es.onmessage = null;
    es.onerror = null;
    es.close();
    es = null;
  }

  // open replaces any stream already showing in the pane (a second "View
  // logs" click for a different app, or the deploy modal's re-entry link,
  // must not leak the previous EventSource) and starts a fresh one.
  function open({ titleText, url, withCredentials }) {
    stopStream();
    lines = [];
    render();
    setStatus('', '');
    if (title) title.textContent = titleText || '';
    hide(pane, false);

    if (typeof createFocusTrap === 'function') {
      if (!trap) trap = createFocusTrap(pane, ownerDoc);
      trap.activate();
    }
    if (closeButton && typeof closeButton.focus === 'function') closeButton.focus();

    if (!EventSourceClass || !url) return;
    es = withCredentials
      ? new EventSourceClass(url, { withCredentials: true })
      : new EventSourceClass(url);
    es.onopen = () => setStatus('', '');
    es.onmessage = (event) => appendLine(event.data);
    es.onerror = () => setStatus('Log stream disconnected, reconnecting…', 'is-reconnecting');
  }

  function close() {
    stopStream();
    setStatus('', '');
    if (trap) {
      trap.release();
      trap = null;
    }
    if (pane) hide(pane, true);
  }

  // Post-mount hook: close after an allowed navigation (mirrors
  // sidebar-drawer.js's onNavigated). A vetoed navigation never mounts, so
  // this never fires and the pane stays open with its stream intact.
  function onNavigated() {
    if (isOpen()) close();
  }

  return { open, close, onNavigated, isOpen };
}
