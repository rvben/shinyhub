export const DEPLOY_EVENTS = 'application/x-shinyhub-deploy-events+json';

// Negotiate the server's existing progress protocol while retaining upload
// progress and compatibility with servers returning one JSON response.
export function uploadDeployment({ slug, blob, csrf, onUpload = () => {}, onPhase = () => {},
  createXHR = () => new XMLHttpRequest(), idleMs = 90000 }) {
  const xhr = createXHR();
  let timer, settled = false, offset = 0, pending = '', result, terminalError, published = false;
  const warnings = [];
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  const finish = (error, body) => {
    if (settled) return;
    settled = true; clearTimeout(timer);
    if (error) reject(error);
    else resolve({ ...body, progress_warnings: warnings });
  };
  const unknown = message => Object.assign(new Error(`${message} The deployment outcome is unknown. Check the app’s deployment history before deploying again.`), { uncertain: true });
  const touch = () => {
    clearTimeout(timer);
    if (!settled) timer = setTimeout(() => {
      if (terminalError) finish(terminalError);
      else if (result) finish(null, result);
      else finish(unknown(`No deployment progress has arrived for ${Math.round(idleMs / 1000)} seconds.`));
      xhr.abort();
    }, idleMs);
  };
  const receive = event => {
    if (result || terminalError) return;
    if (event.type === 'phase') {
      if (['commit', 'handoff'].includes(event.phase) && event.status === 'completed') published = true;
      if (event.status === 'warning' && event.message) warnings.push(event.message);
      onPhase(event);
    } else if (event.type === 'result') {
      if (!event.result || typeof event.result !== 'object' || Array.isArray(event.result)) throw new Error('Invalid deployment result');
      result = event.result;
    } else if (event.type === 'error') {
      terminalError = Object.assign(new Error(event.message || 'Deployment failed. Review the app logs for details.'), {
        status: event.status_code,
        uncertain: published || ['commit', 'configuration'].includes(event.phase),
      });
      if (terminalError.uncertain) terminalError.message += ' Check deployment history before deploying again.';
    }
  };
  const streaming = () => (xhr.getResponseHeader('Content-Type') || '').split(';')[0].trim() === DEPLOY_EVENTS;
  const read = final => {
    if (!streaming() || settled) return;
    pending += xhr.responseText.slice(offset); offset = xhr.responseText.length;
    const lines = pending.split('\n'); pending = lines.pop();
    if (final && pending.trim()) { lines.push(pending); pending = ''; }
    if (pending.length > 1024 * 1024) throw new Error('Oversized deployment progress record');
    for (const line of lines) if (line.trim()) receive(JSON.parse(line));
  };
  xhr.open('POST', `/api/apps/${encodeURIComponent(slug)}/deploy`, true);
  xhr.withCredentials = true;
  xhr.setRequestHeader('X-Shinyhub-Deploy-Channel', 'dashboard');
  xhr.setRequestHeader('Accept', DEPLOY_EVENTS);
  if (csrf) xhr.setRequestHeader('X-CSRF-Token', csrf);
  xhr.upload.addEventListener('progress', event => { touch(); if (event.lengthComputable) onUpload(Math.floor(event.loaded / event.total * 100)); });
  xhr.upload.addEventListener('load', () => { touch(); onPhase({ type: 'phase', phase: 'prepare', status: 'started', message: 'Upload complete. Waiting for the server to prepare the app…' }); });
  xhr.addEventListener('progress', () => {
    touch();
    try { read(false); } catch { finish(unknown('Could not read deployment progress.')); xhr.abort(); }
  });
  xhr.addEventListener('load', () => {
    try {
      if (xhr.status < 200 || xhr.status >= 300) {
        let message = `Deployment request failed (${xhr.status}).`;
        try { message = JSON.parse(xhr.responseText).error || message; } catch {}
        finish(Object.assign(new Error(message), { status: xhr.status, uncertain: xhr.status >= 500 }));
      } else if (streaming()) {
        read(true);
        if (terminalError) finish(terminalError);
        else if (result) finish(null, result);
        else finish(unknown('Deployment progress ended without a result.'));
      } else {
        const body = JSON.parse(xhr.responseText);
        if (!body || typeof body !== 'object' || Array.isArray(body)) throw new Error('Invalid result');
        finish(null, body);
      }
    } catch { finish(unknown('Could not read the deployment result.')); }
  });
  for (const event of ['error', 'timeout', 'abort']) xhr.addEventListener(event, () => {
    if (terminalError) finish(terminalError);
    else if (result) finish(null, result);
    else finish(unknown('The deployment connection was interrupted.'));
  });
  const form = new FormData(); form.append('bundle', blob, 'bundle.zip');
  touch();
  try { xhr.send(form); } catch { finish(unknown('The deployment request could not be sent.')); }
  return { promise, xhr };
}

export function deploymentOutcome(body) {
  const details = [...(body.progress_warnings || [])];
  if (body.warning) details.push(body.warning);
  if (body.hooks_skipped > 0) details.push(`${body.hooks_skipped} post-deploy hook${body.hooks_skipped === 1 ? ' was' : 's were'} skipped by this runtime. Review any required setup before opening the app.`);
  for (const convergence of body.schedule_convergence || []) {
    if (convergence.warning) details.push(`${convergence.schedule}: ${convergence.warning}`);
    else if (convergence.status !== 'satisfied') details.push(`${convergence.schedule}: scheduled setup is ${convergence.status || 'pending'}. Check Schedules for progress.`);
  }
  if (body.handoff) details.push(body.drain_timeout_seconds > 0
    ? `Existing sessions can continue for up to ${body.drain_timeout_seconds} seconds before the previous version closes.`
    : 'Existing sessions are being drained from the previous version.');
  if (body.kept_stopped || body.status === 'stopped') return { title: 'Version deployed; app remains stopped.', action: 'View app', openable: false, details };
  if (body.kept_hibernated || body.status === 'hibernated') return { title: 'Version deployed; opens on demand.', action: 'Open app', openable: true, details };
  if (body.status === 'running') return { title: 'Now serving the deployed version.', action: 'Open app', openable: true, details };
  if (body.status === 'idle') return { title: 'Version deployed; ready to open.', action: 'Open app', openable: true, details };
  return { title: 'Version deployed. Check the app’s readiness.', action: 'View app', openable: false, details };
}
