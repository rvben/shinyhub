// The About dialog: which ShinyHub is this, and what can it run.
//
// This is the one surface that names the software rather than the operator's
// brand. It stays "ShinyHub" on a white-labeled instance, because the reader
// here is someone filing a bug or inheriting the server, not the anonymous
// visitor the branding is for. Deliberately NOT a `.brand` slot: views/
// branding.js rewrites those, and rewriting this one would defeat its purpose.
//
// It sits behind the login and behind a click, so an anonymous visitor still
// sees only the operator's brand.
//
// DOM-free by default: the render helper takes an explicit document so it is
// unit-testable with jsdom (jstests/about.test.js). app.js owns the wiring.

// The runtimes ShinyHub can launch, with the executable each one needs. The
// launcher is named because "R is not installed" is only actionable once you
// know the server went looking for Rscript.
const RUNTIMES = [
  { key: 'python', label: 'Python', launcher: 'uv' },
  { key: 'r', label: 'R', launcher: 'Rscript' },
];

const RUNTIME_STATE_TEXT = {
  ready: 'Ready',
  missing: 'Not installed',
  unknown: 'Unknown',
};

// serverIdentity maps a /api/server-info payload to the identity lines. Pure,
// so the "what do we show when the server did not answer" rules are testable
// without a DOM.
export function serverIdentity(info) {
  const obj = info && typeof info === 'object' ? info : {};
  const raw = typeof obj.version === 'string' ? obj.version.trim() : '';
  const commit = typeof obj.commit === 'string' ? obj.commit.trim() : '';
  // A numeric version gets the conventional "v" prefix. Anything else is shown
  // as the server sent it, so an unstamped build still reads honestly as "dev"
  // rather than "vdev".
  const version = raw ? (/^\d/.test(raw) ? `v${raw}` : raw) : '';
  return {
    version,
    commit,
    known: !!version,
    // An unreachable server, or one old enough to predate /api/server-info,
    // leaves the version genuinely unknown. Say so: a blank line would read as
    // "this build has no version", which is a different and false claim.
    versionText: version || 'Version unavailable',
    buildText: commit ? `Build ${commit}` : '',
  };
}

// runtimeRows reports what the host can start. A server that did not answer, or
// one too old to report runtimes, yields "unknown" rather than "not installed":
// those are different facts, and rendering the second would tell a developer
// their R app cannot deploy when nobody ever checked.
export function runtimeRows(info) {
  const obj = info && typeof info === 'object' ? info : {};
  const reported = obj.runtimes && typeof obj.runtimes === 'object' ? obj.runtimes : null;
  return RUNTIMES.map((rt) => {
    const raw = reported ? reported[rt.key] : undefined;
    const state = typeof raw === 'boolean' ? (raw ? 'ready' : 'missing') : 'unknown';
    return {
      key: rt.key,
      label: rt.label,
      launcher: rt.launcher,
      state,
      stateText: RUNTIME_STATE_TEXT[state],
    };
  });
}

// renderAbout writes the dialog contents into `doc`. Returns the resolved
// identity so callers and tests can assert what was shown, or null when the
// shell has no About dialog.
export function renderAbout(doc, info) {
  const versionEl = doc.getElementById('about-version');
  if (!versionEl) return null;
  const identity = serverIdentity(info);
  versionEl.textContent = identity.versionText;
  // An unknown version is a caveat, not a version number, so it drops the
  // emphasis the real thing carries.
  versionEl.classList.toggle('is-unknown', !identity.known);

  // The commit pins down which build is actually running, which is what
  // confirms a hotfix reached a host. It is absent on an unstamped build, where
  // an empty line would just look broken.
  const buildEl = doc.getElementById('about-build');
  if (buildEl) {
    buildEl.textContent = identity.buildText;
    buildEl.hidden = !identity.buildText;
  }

  const runtimesEl = doc.getElementById('about-runtimes');
  if (runtimesEl) renderRuntimes(doc, runtimesEl, runtimeRows(info));

  return identity;
}

function renderRuntimes(doc, container, rows) {
  container.replaceChildren();
  for (const row of rows) {
    const term = doc.createElement('dt');
    term.className = 'about-runtime-name';
    term.textContent = row.label;
    const launcher = doc.createElement('span');
    launcher.className = 'about-runtime-launcher';
    launcher.textContent = row.launcher;
    term.appendChild(launcher);

    const value = doc.createElement('dd');
    value.className = `about-runtime-state is-${row.state}`;
    value.textContent = row.stateText;

    container.append(term, value);
  }
}

// createServerInfoLoader returns a function that fetches /api/server-info at
// most once per session. `fetchImpl` is injected so tests can count calls.
export function createServerInfoLoader(fetchImpl) {
  let cached = null;
  let inflight = null;
  return function loadServerInfo() {
    if (cached) return Promise.resolve(cached);
    if (inflight) return inflight;
    inflight = (async () => {
      try {
        const res = await fetchImpl('/api/server-info');
        if (!res || !res.ok) return null;
        const info = await res.json();
        // Cache a real answer only. Caching the failure would pin
        // "version unavailable" for the rest of the session over one blip, and
        // the next dialog open is a free chance to get it right.
        if (info && typeof info === 'object') cached = info;
        return cached;
      } catch {
        return null;
      } finally {
        inflight = null;
      }
    })();
    return inflight;
  };
}
