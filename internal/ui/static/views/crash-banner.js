// crashBanner builds the prominent failure banner shown at the top of a crashed
// app's detail Overview: the reason the app went down (a boot error + the tail of
// the app log, e.g. a Python traceback) and a one-click Restart for managers.
//
// It returns null when the app is not crashed, so the caller can unconditionally
// call it and append the result. DOM helper: an explicit document is injected so
// it stays jsdom-testable, matching app-card-badge.js and friends.
export function crashBanner(doc, app, opts) {
  if (!app || app.status !== 'crashed') return null;
  const canManage = !!(opts && opts.canManage);
  const onRestart = opts && opts.onRestart;

  const banner = doc.createElement('div');
  banner.className = 'crash-banner';
  banner.setAttribute('role', 'alert');

  const title = doc.createElement('div');
  title.className = 'crash-banner-title';
  title.textContent = 'This app crashed';
  banner.appendChild(title);

  const reason = (app.last_error || '').trim();
  const reasonEl = doc.createElement(reason ? 'pre' : 'p');
  reasonEl.className = 'crash-banner-reason';
  reasonEl.textContent = reason || 'Its replicas could not be started.';
  banner.appendChild(reasonEl);

  if (canManage) {
    const btn = doc.createElement('button');
    btn.type = 'button';
    btn.className = 'btn btn-primary crash-banner-restart';
    btn.textContent = 'Restart';
    if (onRestart) btn.addEventListener('click', onRestart);
    banner.appendChild(btn);
  }
  return banner;
}
