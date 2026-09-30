// A cold dashboard load cannot ask the API anything until every module has
// been fetched and evaluated, and then it asks in two serialized rounds: the
// session (/api/auth/me) first, then the lists that depend on who the user is.
// The shell therefore starts those same GETs from a small inline script while
// it is still parsing (index.html), and parks the in-flight responses on
// window.__shinyhubBoot as { t: Date.now() at start, r: { path: Promise } }.
// The shell writes each late-arriving path through boot.r, so setting it to
// null both stops new parking and releases every response already parked.
//
// createBootPrefetch hands those responses to the first callers that ask for
// the same path, so the dashboard's own requests find their answer already
// on the way instead of starting a new round trip. A prefetched answer is only
// ever a stand-in for the request the dashboard would have made a moment
// later, so it is dropped the moment it could be out of date: on discard()
// (the caller's boot finished, or it is about to change server state) and
// once maxAgeMs have passed since the shell started it. A prefetch that failed
// at the network level falls back to a real request rather than surfacing a
// failure the caller's own request might not have had.
//
// Each caller gets its own clone (a Response body can be read only once); the
// parked original is never read, so any number of callers can take the same
// path while it is live. Dropping the set nulls boot.r rather than a private
// alias, because the page keeps window.__shinyhubBoot alive and an unread
// parked response would otherwise hold its body for the tab's lifetime.
export function createBootPrefetch(boot, { now = () => Date.now(), maxAgeMs = 10000 } = {}) {
  const valid = Boolean(boot && typeof boot.t === 'number' && boot.r && typeof boot.r === 'object');

  function drop() {
    if (valid) boot.r = null;
  }

  function take(path) {
    const entries = valid ? boot.r : null;
    if (!entries || !Object.prototype.hasOwnProperty.call(entries, path)) return null;
    if (now() - boot.t > maxAgeMs) {
      drop();
      return null;
    }
    return Promise.resolve(entries[path]);
  }

  return {
    // fetch returns the prefetched response for path when one is live, and
    // otherwise (or if the prefetch failed) fetchFn(path, init).
    fetch(path, init, fetchFn) {
      const pending = take(path);
      if (!pending) return fetchFn(path, init);
      return pending.then((response) => response.clone(), () => fetchFn(path, init));
    },
    discard: drop,
  };
}
