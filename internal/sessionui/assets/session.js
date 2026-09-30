// Runs inside an app we did not write. Keep the app's DOM and work intact,
// never reload on an auth/network failure, and scope recovery UI to a shadow.
try {
  const tag = document.currentScript;
  if (tag && typeof window.fetch === 'function' && typeof AbortController === 'function') {
    const endpoint = tag.getAttribute('data-session-url');
    const signInURL = new URL(tag.getAttribute('data-sign-in-url'), window.location.href);
    if (endpoint && ['http:', 'https:'].includes(signInURL.protocol) && !document.getElementById('shinyhub-session-notice')) {
      function notice(denied = false) {
        if (document.getElementById('shinyhub-session-notice')) return;
        const host = document.createElement('aside');
        host.id = 'shinyhub-session-notice';
        const root = host.attachShadow({ mode: 'closed' });
        const style = document.createElement('style');
        style.textContent = ':host{position:fixed;inset:auto 12px 12px;z-index:2147483647;display:block;max-width:640px;margin-inline:auto;color:#eef1f5;background:#19212c;border-radius:12px;box-shadow:0 8px 28px #0004;font:16px/1.5 system-ui,sans-serif;color-scheme:dark}.notice{padding:16px 20px}p{margin:0 0 10px;overflow-wrap:anywhere}a{color:#b7d8ff;text-underline-offset:3px}a:focus-visible{outline:2px solid currentColor;outline-offset:4px}@media(forced-colors:active){:host{border:1px solid CanvasText}}';
        const content = document.createElement('div');
        content.className = 'notice';
        content.setAttribute('role', 'status');
        content.setAttribute('aria-live', 'polite');
        const message = document.createElement('p');
        message.textContent = denied ? 'Access to this app has changed. This page is kept open so you can copy your work.' : 'Your sign-in session ended. This page is kept open so you can copy your work.';
        const link = document.createElement('a');
        link.href = signInURL.href;
        link.target = '_blank';
        link.rel = 'noopener';
        link.textContent = denied ? 'Open app access in a new tab' : 'Sign in again in a new tab';
        content.append(message, link);
        root.append(style, content);
        document.body.append(host);
      }
      const controller = createSessionController({
        endpoint, request: (path, options) => window.fetch(path, options),
        onExpired: () => notice(), onDenied: () => notice(true),
      });
      controller.start({ user: { id: Number(tag.getAttribute('data-user-id')) } });
      controller.check();
    }
  }
} catch {
  // Unsupported or restricted browser APIs must not break the hosted app.
}
