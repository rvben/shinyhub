package access

import (
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

type neverDeployedStore interface {
	GetAppBySlug(slug string) (*db.App, error)
	GetMemberRole(slug string, userID int64) (string, error)
}

// NeverDeployedMiddleware short-circuits /app/<slug>/ requests for apps that
// have never been deployed. Managers (owner, admin, operator, or member with
// role="manager") see CLI instructions plus a browser-deploy hand-off for the
// first deploy; everyone else sees a warm "being prepared" notice. When the
// app already has at least one deployment, the request is forwarded unchanged.
//
// It must be installed between the access middleware and the proxy, so that
// by the time this handler runs the caller has already been authorized to
// see the app at all.
func NeverDeployedMiddleware(st neverDeployedStore, jwtSecret string, revoked auth.RevocationChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slug := extractSlug(r.URL.Path)
			if slug == "" {
				next.ServeHTTP(w, r)
				return
			}
			app, err := st.GetAppBySlug(slug)
			if err != nil || app == nil || app.DeployCount > 0 {
				next.ServeHTTP(w, r)
				return
			}
			user := extractUser(r, jwtSecret, revoked)
			manager := canManageApp(st, app, user)

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(renderNeverDeployedPage(app, user, manager, requestOrigin(r))))
		})
	}
}

func canManageApp(st neverDeployedStore, app *db.App, user *auth.ContextUser) bool {
	if user == nil || app == nil {
		return false
	}
	if user.Role == "admin" || user.Role == "operator" || app.OwnerID == user.ID {
		return true
	}
	role, err := st.GetMemberRole(app.Slug, user.ID)
	return err == nil && role == "manager"
}

// requestOrigin reconstructs the public origin (scheme://host) the user
// reached us on, preferring X-Forwarded-* headers when a trusted proxy set
// them.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		scheme = strings.TrimSpace(strings.SplitN(v, ",", 2)[0])
	}
	host := r.Host
	if v := r.Header.Get("X-Forwarded-Host"); v != "" {
		host = strings.TrimSpace(strings.SplitN(v, ",", 2)[0])
	}
	return scheme + "://" + host
}

const clipboardIconSVG = `<svg class="copy-icon-clipboard" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="4" y="3" width="8" height="10" rx="1.2"/><path d="M6.5 3V2.25a.75.75 0 0 1 .75-.75h1.5a.75.75 0 0 1 .75.75V3"/></svg>`

const checkIconSVG = `<svg class="copy-icon-check" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3.5 8.5 6.5 11.5 12.5 5"/></svg>`

const sampleAppPy = `from shiny import App, ui, render

app_ui = ui.page_fluid(
    ui.h2("Hello ShinyHub"),
    ui.output_text("greeting"),
)

def server(input, output, session):
    @output
    @render.text
    def greeting():
        return "Your first deploy is live!"

app = App(app_ui, server)
`

const sampleRequirementsTxt = "shiny>=1.0\n"

func renderNeverDeployedPage(app *db.App, user *auth.ContextUser, manager bool, origin string) string {
	appName := html.EscapeString(app.Name)
	slug := html.EscapeString(app.Slug)

	var body string
	if manager {
		username := "<your-name>"
		if user != nil && user.Username != "" {
			username = html.EscapeString(user.Username)
		}
		snippet := fmt.Sprintf(
			"shiny login --host %s --username %s\nshiny deploy --slug %s .",
			html.EscapeString(origin), username, slug,
		)

		body = fmt.Sprintf(`
<p class="emptystate-eyebrow"><span class="sparkle" aria-hidden="true"></span>Awaiting first deploy</p>
<h1><strong>%s</strong> is ready for its first bundle.</h1>
<p class="lead">This app has been created but no code has been uploaded yet. Deploy it from your terminal, or hand off to the browser and drop in a folder.</p>

<div class="snippet">
  <pre><code id="snippet">%s</code></pre>
  <button type="button" id="copy" class="copy-btn" aria-label="Copy CLI commands to clipboard">
    %s%s<span class="copy-label" aria-hidden="true">Copy</span>
    <span class="sr-only" id="copy-status" aria-live="polite"></span>
  </button>
</div>

<div class="emptystate-actions">
  <a class="emptystate-btn emptystate-btn-primary" href="%s">Deploy from browser</a>
  <a class="emptystate-btn" href="%s">Back to dashboard</a>
</div>

<details class="emptystate-scaffold">
  <summary>What should my bundle contain?</summary>
  <div class="emptystate-scaffold-body">
    <p>A minimal Python Shiny app needs two files at the root of your folder:</p>
    <div>
      <p class="emptystate-scaffold-file-label">app.py</p>
      <div class="snippet">
        <pre><code id="scaffold-app">%s</code></pre>
        <button type="button" class="copy-btn" data-copy-target="scaffold-app" aria-label="Copy app.py example to clipboard">
          %s%s<span class="copy-label" aria-hidden="true">Copy</span>
          <span class="sr-only" aria-live="polite"></span>
        </button>
      </div>
    </div>
    <div>
      <p class="emptystate-scaffold-file-label">requirements.txt</p>
      <div class="snippet">
        <pre><code id="scaffold-reqs">%s</code></pre>
        <button type="button" class="copy-btn" data-copy-target="scaffold-reqs" aria-label="Copy requirements.txt example to clipboard">
          %s%s<span class="copy-label" aria-hidden="true">Copy</span>
          <span class="sr-only" aria-live="polite"></span>
        </button>
      </div>
    </div>
    <p>R Shiny works too — use <code>app.R</code> at the root and list dependencies in <code>renv.lock</code> or <code>DESCRIPTION</code>.</p>
  </div>
</details>
<script>
(function() {
  function wireCopy(btn, sourceId) {
    btn.addEventListener('click', async function() {
      var src = document.getElementById(sourceId);
      if (!src) return;
      try {
        await navigator.clipboard.writeText(src.textContent);
        btn.classList.add('is-copied');
        var label = btn.querySelector('.copy-label');
        if (label) label.textContent = 'Copied';
        var status = btn.querySelector('[aria-live]');
        if (status) status.textContent = 'Copied to clipboard';
        setTimeout(function() {
          btn.classList.remove('is-copied');
          if (label) label.textContent = 'Copy';
          if (status) status.textContent = '';
        }, 1800);
      } catch (e) { /* clipboard blocked; user can select text */ }
    });
  }
  wireCopy(document.getElementById('copy'), 'snippet');
  document.querySelectorAll('.copy-btn[data-copy-target]').forEach(function(btn) {
    wireCopy(btn, btn.dataset.copyTarget);
  });
})();
</script>`,
			appName,
			html.EscapeString(snippet), clipboardIconSVG, checkIconSVG,
			html.EscapeString(origin+"/#deploy="+app.Slug),
			html.EscapeString(origin+"/"),
			html.EscapeString(sampleAppPy), clipboardIconSVG, checkIconSVG,
			html.EscapeString(sampleRequirementsTxt), clipboardIconSVG, checkIconSVG,
		)
	} else {
		body = fmt.Sprintf(`
<p class="emptystate-eyebrow"><span class="sparkle" aria-hidden="true"></span>Coming soon</p>
<h1><strong>%s</strong> is being prepared by its owner.</h1>
<p class="lead">No code has been uploaded yet. Check back soon — once a bundle is deployed, this page will load the running app.</p>`,
			appName,
		)
	}

	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>` + appName + ` · ShinyHub</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Manrope:wght@300;400;500;600;700;800&family=Space+Mono:wght@400;700&display=swap" rel="stylesheet">
<link rel="stylesheet" href="/static/style.css">
</head>
<body>
<div class="bg-stars" aria-hidden="true"></div>
<main class="emptystate-page">
<div class="emptystate-card">` + body + `</div>
</main>
</body>
</html>`
}
