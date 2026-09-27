// A cold dashboard load fires more than one independent caller that wants the
// same read-only list at once: showLoggedIn's sidebar index and the view the
// router is about to mount (Overview, the apps grid, the project detail page)
// each fetch /api/apps on their own, unaware of each other. Without
// coalescing that is two identical unpaginated GETs of the full app list in
// the same navigation instead of one.
//
// createGETCoalescer returns a function that shares one underlying call to
// fetchFn per URL across everyone who asks for it while it is in flight, then
// clears itself so the next navigation issues a fresh request rather than
// serving a stale one forever. Only GET is ever a candidate here - callers
// decide that themselves by never routing a mutating request through this
// helper - so nothing about a POST/PUT/DELETE is ever deduplicated.
//
// Each caller gets its own clone of the resolved response (a Response body
// can only be read once), and a caller's own AbortSignal only stops that
// caller from waiting - it never cancels the shared fetch, since a sibling
// caller may still need the result. fetchFn's result must support .clone()
// the way the real Fetch API's Response does.
export function createGETCoalescer(fetchFn) {
  const inFlight = new Map();

  return function coalescedGET(path, init = {}) {
    const { signal, ...fetchInit } = init;

    let entry = inFlight.get(path);
    if (!entry) {
      entry = Promise.resolve(fetchFn(path, fetchInit)).finally(() => {
        if (inFlight.get(path) === entry) inFlight.delete(path);
      });
      inFlight.set(path, entry);
    }
    const shared = entry.then((response) => response.clone());

    if (!signal) return shared;
    if (signal.aborted) return Promise.reject(abortError());
    return new Promise((resolve, reject) => {
      const onAbort = () => reject(abortError());
      signal.addEventListener('abort', onAbort, { once: true });
      shared.then(
        (response) => { signal.removeEventListener('abort', onAbort); resolve(response); },
        (error) => { signal.removeEventListener('abort', onAbort); reject(error); },
      );
    });
  };
}

function abortError() {
  return new DOMException('Aborted', 'AbortError');
}
