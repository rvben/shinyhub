export const DEMO_SESSION_PATH = "/__demo/session";
export const DEMO_STYLE_PATH = "/__demo/assets/v1/login.css";
export const DEMO_SCRIPT_PATH = "/__demo/assets/v1/login.js";

export const demoLoginStyles = String.raw`
.demo-login-box {
  max-width: 456px;
}

.demo-entry {
  display: flex;
  flex-direction: column;
  gap: 0.85rem;
}

.demo-entry-meta,
.demo-entry-notes {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.5rem 0.85rem;
}

.demo-entry-meta {
  justify-content: space-between;
  color: var(--text-soft);
  font-size: 0.75rem;
  font-weight: 600;
}

.demo-live-status {
  display: inline-flex;
  align-items: center;
  gap: 0.45rem;
  color: var(--green);
}

.demo-live-dot {
  width: 0.45rem;
  height: 0.45rem;
  border-radius: 99px;
  background: currentColor;
  box-shadow: 0 0 0 3px color-mix(in srgb, currentColor 14%, transparent);
}

.demo-entry h2 {
  margin: 0.15rem 0 0;
  color: var(--text);
  font-size: 1.42rem;
  font-weight: 700;
  line-height: 1.2;
  letter-spacing: -0.025em;
  text-wrap: balance;
}

.demo-entry-copy {
  max-width: 43ch;
  margin: 0;
  color: var(--text-soft);
  font-size: 0.88rem;
  line-height: 1.55;
  text-wrap: pretty;
}

.demo-entry-form {
  display: block !important;
  margin-top: 0.25rem;
}

.demo-login-box button.demo-entry-button {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  width: 100%;
  min-height: 44px;
  padding: 0.78rem 0.95rem 0.78rem 1.05rem;
  border: 0;
  border-radius: var(--radius);
  background: var(--brand-primary);
  color: var(--bg-deep);
  box-shadow: 0 4px 14px color-mix(in srgb, var(--brand-primary) 24%, transparent);
  cursor: pointer;
  font: 700 0.9rem/1.2 var(--font);
  letter-spacing: -0.005em;
  transition: background-color 180ms cubic-bezier(.22,1,.36,1),
              transform 180ms cubic-bezier(.22,1,.36,1),
              box-shadow 180ms cubic-bezier(.22,1,.36,1);
}

.demo-login-box button.demo-entry-button svg {
  flex: none;
  transition: transform 180ms cubic-bezier(.22,1,.36,1);
}

.demo-login-box button.demo-entry-button:hover:not(:disabled) {
  background: var(--electric);
  transform: translateY(-1px);
  box-shadow: 0 6px 14px color-mix(in srgb, var(--brand-primary) 30%, transparent);
}

.demo-login-box button.demo-entry-button:hover:not(:disabled) svg {
  transform: translateX(2px);
}

.demo-login-box button.demo-entry-button:focus-visible,
.demo-manual-toggle:focus-visible {
  outline: 3px solid color-mix(in srgb, var(--brand-primary) 34%, transparent);
  outline-offset: 3px;
}

.demo-login-box button.demo-entry-button:disabled {
  cursor: wait;
  opacity: 0.72;
}

.demo-entry-notes {
  color: var(--text-soft);
  font-size: 0.72rem;
  line-height: 1.35;
}

.demo-entry-notes span {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
}

.demo-entry-notes svg {
  color: var(--green);
}

.demo-entry-error {
  margin: 0;
  padding: 0.65rem 0.75rem;
  border: 1px solid var(--red);
  border-radius: var(--radius);
  background: var(--red-bg);
  color: var(--text);
  font-size: 0.8rem;
  line-height: 1.45;
}

.demo-manual-toggle {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 0.4rem;
  width: 100%;
  margin: 1.15rem 0 0;
  padding: 0.5rem;
  border: 0;
  border-top: 1px solid var(--line);
  border-radius: 0;
  background: transparent;
  color: var(--text-soft);
  cursor: pointer;
  font: 500 0.78rem/1.4 var(--font);
}

.demo-manual-toggle:hover {
  color: var(--cyan-bright);
}

.demo-manual-toggle svg {
  transition: transform 180ms cubic-bezier(.22,1,.36,1);
}

.demo-login-box.demo-manual-open .demo-manual-toggle svg {
  transform: rotate(180deg);
}

.demo-login-box #login-form,
.demo-login-box #login-error,
.demo-login-box .login-separator,
.demo-login-box .github-login,
.demo-login-box .google-login,
.demo-login-box .oidc-login {
  display: none;
}

.demo-login-box.demo-manual-open #login-form {
  display: grid;
  margin-top: 0.75rem;
}

.demo-login-box.demo-manual-open #login-error:not([hidden]) {
  display: block;
}

.demo-login-box.demo-manual-open .login-separator:not([hidden]) {
  display: block;
}

.demo-login-box.demo-manual-open .github-login:not([hidden]),
.demo-login-box.demo-manual-open .google-login:not([hidden]),
.demo-login-box.demo-manual-open .oidc-login:not([hidden]) {
  display: flex;
}

@media (max-width: 520px) {
  .demo-login-box {
    padding: 2rem 1.35rem !important;
  }

  .demo-entry-meta {
    align-items: flex-start;
    flex-direction: column;
  }
}

@media (prefers-reduced-motion: reduce) {
  .demo-login-box button.demo-entry-button,
  .demo-login-box button.demo-entry-button svg,
  .demo-manual-toggle svg {
    transition: none;
  }

  .demo-login-box button.demo-entry-button:hover:not(:disabled) {
    transform: none;
  }
}
`;

export const demoLoginScript = String.raw`
(() => {
  const toggle = document.querySelector('.demo-manual-toggle');
  const box = toggle?.closest('.demo-login-box');
  const entryForm = box?.querySelector('.demo-entry-form');
  if (!box || !toggle || !entryForm) return;

  toggle.addEventListener('click', () => {
    const open = !box.classList.contains('demo-manual-open');
    box.classList.toggle('demo-manual-open', open);
    toggle.setAttribute('aria-expanded', String(open));
    if (open) box.querySelector('#login-username')?.focus();
  });

  entryForm.addEventListener('submit', () => {
    const button = entryForm.querySelector('.demo-entry-button');
    if (!button) return;
    button.disabled = true;
    button.setAttribute('aria-busy', 'true');
    const label = button.querySelector('.demo-entry-button-label');
    if (label) label.textContent = 'Opening workspace…';
  });
})();
`;

function entryMarkup(showError: boolean): string {
  const error = showError
    ? `<p class="demo-entry-error" role="alert">The demo could not open just now. Please try again.</p>`
    : "";

  return `
    <section class="demo-entry" aria-labelledby="demo-entry-title">
      <div class="demo-entry-meta">
        <span class="demo-live-status"><span class="demo-live-dot" aria-hidden="true"></span>Live workspace</span>
        <span>5 interactive apps</span>
      </div>
      <h2 id="demo-entry-title">Explore ShinyHub, already running.</h2>
      <p class="demo-entry-copy">Step into a real read-only control plane and launch working Python, R, Dash, and Streamlit applications.</p>
      ${error}
      <form class="demo-entry-form" method="post" action="${DEMO_SESSION_PATH}">
        <button class="demo-entry-button" type="submit">
          <span class="demo-entry-button-label">Enter the live demo</span>
          <svg aria-hidden="true" width="18" height="18" viewBox="0 0 18 18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M3.75 9h10.5M10 4.75 14.25 9 10 13.25"/></svg>
        </button>
      </form>
      <div class="demo-entry-notes" aria-label="Demo access details">
        <span><svg aria-hidden="true" width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m3 8 3 3 7-7"/></svg>No signup</span>
        <span><svg aria-hidden="true" width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m3 8 3 3 7-7"/></svg>Read-only access</span>
        <span><svg aria-hidden="true" width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m3 8 3 3 7-7"/></svg>Resets automatically</span>
      </div>
    </section>
    <button class="demo-manual-toggle" type="button" aria-expanded="false" aria-controls="login-form">
      Sign in manually
      <svg aria-hidden="true" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="m4 6 4 4 4-4"/></svg>
    </button>`;
}

export function decorateDemoLogin(response: Response, showError: boolean): Response {
  return new HTMLRewriter()
    .on("head", {
      element(element) {
        element.append(`<link rel="stylesheet" href="${DEMO_STYLE_PATH}">`, { html: true });
      },
    })
    .on(".login-box", {
      element(element) {
        const classes = element.getAttribute("class") ?? "";
        element.setAttribute("class", `${classes} demo-login-box`.trim());
      },
    })
    .on("#login-heading", {
      element(element) {
        element.setInnerContent("Explore the ShinyHub demo");
      },
    })
    .on(".login-brand", {
      element(element) {
        element.after(entryMarkup(showError), { html: true });
      },
    })
    .on("body", {
      element(element) {
        element.append(`<script src="${DEMO_SCRIPT_PATH}"></script>`, { html: true });
      },
    })
    .transform(response);
}
