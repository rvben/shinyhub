export function supportSessionRemaining(expiresAt, now = Date.now()) {
  const deadline = new Date(expiresAt).getTime();
  if (!Number.isFinite(deadline)) return { seconds: 0, label: 'Expired' };
  const seconds = Math.max(0, Math.ceil((deadline - now) / 1000));
  if (seconds === 0) return { seconds, label: 'Expired' };
  const minutes = Math.floor(seconds / 60);
  return { seconds, label: `${minutes}:${String(seconds % 60).padStart(2, '0')}` };
}

export function supportSessionThreshold(previousSeconds, seconds) {
  if (previousSeconds > 300 && seconds <= 300) return 'Five minutes remain in the support session.';
  if (previousSeconds > 60 && seconds <= 60) return 'One minute remains in the support session.';
  if (previousSeconds > 0 && seconds === 0) return 'The support session has expired.';
  return '';
}

function validActiveSession(value) {
  if (!value || typeof value !== 'object') return null;
  const expires = new Date(value.expires_at).getTime();
  if (typeof value.subject_username !== 'string' || !value.subject_username ||
      typeof value.app_slug !== 'string' || !value.app_slug || !Number.isFinite(expires) ||
      !Number.isInteger(value.remaining_seconds) || value.remaining_seconds < 0) return null;
  return value;
}

export function createSupportSessionRecovery({
  root,
  request,
  onUnauthorized = () => {},
  onEnded = () => {},
  onStatusCleared = () => {},
  now = () => Date.now(),
  setIntervalFn = setInterval,
  clearIntervalFn = clearInterval,
}) {
  if (!root) return { load: async () => {}, clear: () => {}, destroy: () => {} };

  const subject = root.querySelector('[data-support-current-subject]');
  const app = root.querySelector('[data-support-current-app]');
  const heading = root.querySelector('[data-support-current-heading]');
  const details = root.querySelector('[data-support-current-details]');
  const unavailable = root.querySelector('[data-support-current-unavailable]');
  const meta = root.querySelector('[data-support-current-meta]');
  const phase = root.querySelector('[data-support-current-phase]');
  const countdown = root.querySelector('[data-support-current-countdown]');
  const deadline = root.querySelector('[data-support-current-deadline]');
  const resume = root.querySelector('[data-support-current-resume]');
  const retry = root.querySelector('[data-support-current-retry]');
  const end = root.querySelector('[data-support-current-end]');
  const error = root.querySelector('[data-support-current-error]');
  const live = root.ownerDocument.getElementById('support-session-recovery-status');
  let active = null;
  let timer = null;
  let generation = 0;
  let priorSeconds = 0;
  let ending = false;
  let statusUnknown = false;
  let clientDeadline = 0;
  let endFailed = false;

  function stopTimer() {
    if (timer !== null) clearIntervalFn(timer);
    timer = null;
  }

  function setError(message) {
    error.textContent = message || '';
    error.hidden = !message;
  }

  function setEnding(isEnding) {
    ending = isEnding;
    root.setAttribute('aria-busy', String(isEnding));
    end.disabled = isEnding;
    retry.disabled = isEnding;
    end.textContent = isEnding ? 'Ending…' : (endFailed
      ? 'Try ending again'
      : (statusUnknown ? 'End current session' : 'End session'));
    if (isEnding) {
      resume.setAttribute('aria-disabled', 'true');
      resume.tabIndex = -1;
    } else {
      resume.removeAttribute('aria-disabled');
      resume.removeAttribute('tabindex');
    }
  }

  function hide() {
    stopTimer();
    active = null;
    ending = false;
    statusUnknown = false;
    clientDeadline = 0;
    endFailed = false;
    root.hidden = true;
    root.removeAttribute('aria-busy');
    resume.hidden = true;
    resume.removeAttribute('href');
    retry.hidden = true;
    setError('');
    end.disabled = false;
    end.textContent = 'End session';
  }

  function tick() {
    if (!active || ending) return;
    const remaining = supportSessionRemaining(clientDeadline, now());
    countdown.textContent = remaining.label;
    const announcement = supportSessionThreshold(priorSeconds, remaining.seconds);
    priorSeconds = remaining.seconds;
    if (announcement && live) live.textContent = announcement;
    if (remaining.seconds === 0) {
      stopTimer();
      load();
    }
  }

  function render(session, announce) {
    active = session;
    statusUnknown = false;
    endFailed = false;
    heading.textContent = 'Active support session';
    details.hidden = false;
    unavailable.hidden = true;
    meta.hidden = false;
    retry.hidden = true;
    subject.textContent = session.subject_username;
    app.textContent = session.app_slug;
    phase.textContent = session.resumable ? 'Active' : 'Launch pending';
    deadline.dateTime = new Date(session.expires_at).toISOString();
    try {
      deadline.title = new Intl.DateTimeFormat(undefined, {
        dateStyle: 'medium', timeStyle: 'short',
      }).format(new Date(session.expires_at));
    } catch {
      deadline.title = new Date(session.expires_at).toLocaleString();
    }
    let resumeURL = null;
    if (typeof session.app_url === 'string' && session.app_url !== '') {
      try {
        const parsed = new URL(session.app_url, root.ownerDocument.baseURI);
        if (parsed.protocol === 'https:' || parsed.protocol === 'http:') resumeURL = parsed.href;
      } catch { /* malformed URLs remain unavailable */ }
    }
    const canResume = session.resumable === true && resumeURL !== null;
    resume.hidden = !canResume;
    if (canResume) resume.href = resumeURL;
    else resume.removeAttribute('href');
    setError('');
    setEnding(false);
    root.hidden = false;
    stopTimer();
    clientDeadline = now() + (session.remaining_seconds * 1000);
    priorSeconds = supportSessionRemaining(clientDeadline, now()).seconds;
    tick();
    if (!root.hidden) timer = setIntervalFn(tick, 1000);
    if (announce && live) {
      live.textContent = `Active support session as ${session.subject_username} in ${session.app_slug}.`;
    }
  }

  function showUnavailable(message, announce) {
    stopTimer();
    active = null;
    statusUnknown = true;
    clientDeadline = 0;
    endFailed = false;
    heading.textContent = 'Support session status unavailable';
    details.hidden = true;
    unavailable.hidden = false;
    meta.hidden = true;
    resume.hidden = true;
    resume.removeAttribute('href');
    retry.hidden = false;
    setError(message);
    setEnding(false);
    root.hidden = false;
    if (announce && live) {
      live.textContent = 'Support session status is unavailable. Retry the status check or end any current session.';
    }
  }

  async function load({ announce = false, interactive = false } = {}) {
    if (ending) return;
    const requestGeneration = ++generation;
    let response;
    try {
      response = await request('/api/support-sessions/current');
    } catch {
      if (requestGeneration !== generation) return;
      if (active) setError('Support-session status could not be refreshed. The displayed deadline still applies.');
      else showUnavailable('Check your connection, retry the status check, or end any current session as a precaution.', announce);
      return;
    }
    if (requestGeneration !== generation) return;
    if (response.status === 401) {
      hide();
      await onUnauthorized();
      return;
    }
    if (response.status === 404) {
      hide();
      return;
    }
    if (!response.ok) {
      if (active) setError('Support-session status could not be refreshed. The displayed deadline still applies.');
      else showUnavailable('Retry the status check, or end any current session as a precaution.', announce);
      return;
    }
    let payload;
    try {
      payload = await response.json();
    } catch {
      if (active) setError('Support-session status could not be refreshed. The displayed deadline still applies.');
      else showUnavailable('Retry the status check, or end any current session as a precaution.', announce);
      return;
    }
    if (payload?.active === null) {
      hide();
      if (interactive && live) live.textContent = 'No active support session was found.';
      if (interactive) onStatusCleared();
      return;
    }
    const session = validActiveSession(payload?.active);
    if (!session) {
      if (active) setError('Support-session status could not be refreshed. The displayed deadline still applies.');
      else showUnavailable('Retry the status check, or end any current session as a precaution.', announce);
      return;
    }
    render(session, announce);
  }

  async function retryStatus() {
    if (retry.disabled) return;
    root.setAttribute('aria-busy', 'true');
    retry.disabled = true;
    retry.textContent = 'Checking…';
    await load({ announce: true, interactive: true });
    if (root.hidden) return;
    root.setAttribute('aria-busy', 'false');
    retry.textContent = 'Retry status';
    retry.disabled = false;
    if (retry.hidden) {
      heading.tabIndex = -1;
      heading.focus({ preventScroll: true });
    } else {
      retry.focus({ preventScroll: true });
    }
  }

  async function endCurrent() {
    if ((!active && !statusUnknown) || end.disabled) return;
    const requestGeneration = ++generation;
    const endedSubject = active?.subject_username || '';
    endFailed = false;
    setError('');
    setEnding(true);
    let response;
    try {
      response = await request('/api/support-sessions/current', { method: 'DELETE' });
    } catch {
      if (requestGeneration !== generation) return;
      setError('The session could not be ended. Check your connection and try again; automatic expiry remains in force.');
      endFailed = true;
      setEnding(false);
      end.focus();
      return;
    }
    if (requestGeneration !== generation) return;
    if (response.status === 401) {
      hide();
      await onUnauthorized();
      return;
    }
    if (!response.ok) {
      setError('The session could not be ended. Try again; automatic expiry remains in force.');
      endFailed = true;
      setEnding(false);
      end.focus();
      return;
    }
    hide();
    if (live) live.textContent = endedSubject
      ? `Support session as ${endedSubject} ended.`
      : 'Any current support session has been ended.';
    onEnded();
  }

  end.addEventListener('click', endCurrent);
  retry.addEventListener('click', retryStatus);

  return {
    load,
    clear() {
      generation += 1;
      hide();
    },
    destroy() {
      generation += 1;
      hide();
      end.removeEventListener('click', endCurrent);
      retry.removeEventListener('click', retryStatus);
    },
  };
}
