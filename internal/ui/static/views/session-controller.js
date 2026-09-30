// The session cookie remains HttpOnly. Only server-provided timing metadata is
// kept here; no credential or account data is persisted in browser storage.
export function createSessionController({
  request, onExpired, onSession = () => {}, document: doc = document,
  window: win = window, now = () => Date.now(),
  setTimer = (fn, ms) => setTimeout(fn, ms), clearTimer = id => clearTimeout(id),
}) {
  let active = false;
  let generation = 0;
  let timer = null;
  let pending = null;
  let refreshAfter = 300000;
  let nextRefresh = 0;
  let failures = 0;
  let channel = null;

  function cancelTimer() {
    if (timer !== null) clearTimer(timer);
    timer = null;
  }

  function schedule(delay) {
    cancelTimer();
    nextRefresh = now() + delay;
    if (active && !doc.hidden) timer = setTimer(check, delay);
  }

  function timing(session) {
    const seconds = Number(session?.refresh_after_seconds);
    // No browser session (e.g. forward-auth): periodically verify the upstream
    // identity, but do not infer a ShinyHub expiry from a local clock.
    refreshAfter = Number.isFinite(seconds) && seconds > 0
      ? Math.max(1000, seconds * 1000) : 300000;
    const remaining = Date.parse(session?.expires_at) - Date.parse(session?.server_time);
    return Number.isFinite(remaining) ? Math.min(refreshAfter, Math.max(1000, remaining)) : refreshAfter;
  }

  function stop() {
    active = false;
    generation++;
    cancelTimer();
    pending?.abort();
    pending = null;
    channel?.close();
    channel = null;
    doc.removeEventListener('visibilitychange', resume);
    win.removeEventListener('online', resume);
    win.removeEventListener('pageshow', resume);
  }

  function start(payload) {
    stop();
    active = true;
    failures = 0;
    if (win.BroadcastChannel) {
      try {
        channel = new win.BroadcastChannel('shinyhub-session');
        channel.onmessage = event => {
          if (event.data === 'ended' && active) {
            stop();
            onExpired();
          }
        };
      } catch {
        // Some browser privacy modes expose the API but deny its use.
        channel = null;
      }
    }
    doc.addEventListener('visibilitychange', resume);
    win.addEventListener('online', resume);
    win.addEventListener('pageshow', resume);
    schedule(timing(payload?.session));
  }

  async function check() {
    cancelTimer();
    if (!active || doc.hidden || pending) return;
    const current = generation;
    const controller = new AbortController();
    pending = controller;
    const timeout = setTimer(() => controller.abort(), 15000);
    let delay = refreshAfter;
    try {
      const response = await request('/api/auth/me', { cache: 'no-store', signal: controller.signal });
      if (!active || current !== generation) return;
      if (response.status === 401) {
        end();
        onExpired();
        return;
      }
      if (!response.ok) throw new Error(`status ${response.status}`);
      const payload = await response.json();
      if (!active || current !== generation) return;
      if (!payload?.user) throw new Error('missing session user');
      failures = 0;
      delay = timing(payload.session);
      onSession(payload);
    } catch {
      if (!active || current !== generation) return;
      // A timeout, offline connection, or server error is not proof of logout.
      // Retry within the renewal window, with bounded backoff.
      failures++;
      delay = Math.min(refreshAfter, 30000, 1000 * 2 ** Math.min(failures, 5));
    } finally {
      clearTimer(timeout);
      if (current === generation) {
        pending = null;
        if (active) schedule(delay);
      }
    }
  }

  function resume(event) {
    if (!active) return;
    if (doc.hidden) {
      cancelTimer();
      return;
    }
    // Returning to a tab, restoring a cached page, and reconnecting all need a
    // fresh identity verdict before the operator resumes work.
    if (event?.type === 'online' || event?.type === 'pageshow' || event?.type === 'visibilitychange' || now() >= nextRefresh) check();
    else schedule(Math.max(0, nextRefresh - now()));
  }

  function end() {
    try { channel?.postMessage('ended'); }
    catch { /* Cross-tab notification is best effort; local logout still runs. */ }
    finally { stop(); }
  }

  return { start, stop, check, end };
}
