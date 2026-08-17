// App-card lifecycle feedback is deliberately isolated from app.js so its
// confirmation, progress, recovery, focus, and untrusted-name handling can be
// exercised without booting the complete application shell.

export function restartConfirmationCopy(appName) {
  return {
    title: `Restart ${String(appName || 'this app')}?`,
    message: 'Active viewer sessions will disconnect. ShinyHub will bring the app back automatically and report progress here.',
  };
}

export async function requestAppRestart(api, slug) {
  let response;
  try {
    response = await api(`/api/apps/${encodeURIComponent(slug)}/restart`, { method: 'POST' });
  } catch {
    return {
      kind: 'network-error',
      message: "ShinyHub couldn't reach the server. Check your connection, then try again.",
    };
  }

  if (response.status === 401) return { kind: 'unauthorized' };

  let body = null;
  try { body = await response.json(); } catch { /* non-JSON */ }
  if (!response.ok) {
    return {
      kind: 'error',
      message: (body && body.error) || 'The app did not restart. Review its logs, then try again.',
    };
  }
  return { kind: 'success', app: body && typeof body === 'object' ? body : null };
}

function actionButton(doc, label, dataName, className = '') {
  const button = doc.createElement('button');
  button.type = 'button';
  button.className = `app-card-lifecycle-action${className ? ` ${className}` : ''}`;
  button.textContent = label;
  button.dataset[dataName] = '';
  return button;
}

export function createAppCardLifecycle(doc, options = {}) {
  const root = doc.createElement('div');
  root.className = 'app-card-lifecycle';

  const feedback = doc.createElement('div');
  feedback.className = 'app-card-feedback';
  feedback.hidden = true;
  feedback.setAttribute('aria-atomic', 'true');

  const prompt = doc.createElement('div');
  prompt.className = 'app-card-confirm';
  prompt.hidden = true;

  const promptCopy = doc.createElement('div');
  promptCopy.className = 'app-card-confirm-copy';
  const promptTitle = doc.createElement('strong');
  const promptMessage = doc.createElement('span');
  promptCopy.append(promptTitle, promptMessage);

  const promptActions = doc.createElement('div');
  promptActions.className = 'app-card-confirm-actions';
  const cancel = actionButton(doc, 'Cancel', 'lifecycleCancel');
  const confirm = actionButton(doc, 'Restart app', 'lifecycleConfirm', 'is-primary');
  promptActions.append(cancel, confirm);
  prompt.append(promptCopy, promptActions);
  root.append(feedback, prompt);

  let returnFocus = null;

  function hidePrompt(restoreFocus) {
    prompt.hidden = true;
    if (restoreFocus && returnFocus && returnFocus.isConnected && !returnFocus.disabled) {
      returnFocus.focus();
    }
    returnFocus = null;
  }

  cancel.addEventListener('click', () => hidePrompt(true));
  confirm.addEventListener('click', () => {
    hidePrompt(false);
    if (options.onConfirm) options.onConfirm();
  });

  function showConfirmation(focusTarget = null) {
    const copy = restartConfirmationCopy(options.appName);
    promptTitle.textContent = copy.title;
    promptMessage.textContent = copy.message;
    feedback.hidden = true;
    feedback.textContent = '';
    returnFocus = focusTarget;
    prompt.hidden = false;
    confirm.focus();
  }

  function setPhase(phase, detail = '') {
    hidePrompt(false);
    feedback.textContent = '';
    feedback.className = 'app-card-feedback';

    if (!['pending', 'success', 'error'].includes(phase)) {
      feedback.hidden = true;
      feedback.removeAttribute('role');
      feedback.removeAttribute('aria-live');
      delete feedback.dataset.phase;
      return;
    }

    const content = doc.createElement('div');
    content.className = 'app-card-feedback-copy';
    const title = doc.createElement('strong');
    const message = doc.createElement('span');
    content.append(title, message);

    feedback.dataset.phase = phase;
    feedback.classList.add(`is-${phase}`);
    feedback.hidden = false;

    if (phase === 'pending') {
      feedback.setAttribute('role', 'status');
      feedback.setAttribute('aria-live', 'polite');
      title.textContent = 'Restarting app…';
      message.textContent = 'Health checks are running. This usually takes less than a minute.';
    } else if (phase === 'success') {
      feedback.setAttribute('role', 'status');
      feedback.setAttribute('aria-live', 'polite');
      title.textContent = 'Restart complete';
      message.textContent = 'The app is healthy and accepting sessions.';
    } else {
      feedback.setAttribute('role', 'alert');
      feedback.setAttribute('aria-live', 'assertive');
      title.textContent = 'Restart failed';
      message.textContent = detail || 'The app did not restart. Review its logs, then try again.';
    }

    feedback.appendChild(content);

    if (phase === 'error') {
      const actions = doc.createElement('div');
      actions.className = 'app-card-feedback-actions';
      const retry = actionButton(doc, 'Retry', 'lifecycleRetry', 'is-primary');
      const logs = actionButton(doc, 'View logs', 'lifecycleLogs');
      retry.addEventListener('click', () => options.onRetry && options.onRetry());
      logs.addEventListener('click', () => options.onViewLogs && options.onViewLogs());
      actions.append(retry, logs);
      feedback.appendChild(actions);
    }
  }

  function clear() {
    hidePrompt(false);
    setPhase('idle');
  }

  return {
    root,
    feedback,
    prompt,
    showConfirmation,
    setPhase,
    clear,
  };
}
