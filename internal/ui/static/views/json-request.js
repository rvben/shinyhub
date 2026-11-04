// Bound both the connection and body read. A response with stalled JSON must
// not leave an operator's form locked indefinitely.
export async function requestJSON(api, path, options = {}, {timeoutMs = 15000, setTimer = setTimeout, clearTimer = clearTimeout} = {}) {
  const controller = new AbortController();
  const abort = () => controller.abort();
  options.signal?.addEventListener('abort', abort, {once: true});
  if (options.signal?.aborted) controller.abort();
  let timer;
  try {
    if (controller.signal.aborted) throw new Error('Request stopped before it could be confirmed.');
    return await Promise.race([
      new Promise((_, reject) => {
        const stopped = () => reject(new Error('Request stopped before it could be confirmed.'));
        if (controller.signal.aborted) stopped();
        else controller.signal.addEventListener('abort', stopped, {once: true});
        timer = setTimer(abort, timeoutMs);
      }),
      (async () => {
        const response = await api(path, {...options, signal: controller.signal});
        let data, parseError;
        if (response.status !== 204) {
          try { data = await response.json(); } catch (failure) { parseError = failure; }
        }
        return {ok: response.ok, status: response.status, json: async () => {
          if (parseError) throw parseError;
          return data;
        }};
      })(),
    ]);
  } finally {
    clearTimer(timer);
    options.signal?.removeEventListener('abort', abort);
  }
}
