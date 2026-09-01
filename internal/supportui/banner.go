// Package supportui owns the non-optional safety banner injected while an
// administrator is troubleshooting an app as another user.
package supportui

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"html"
	"strconv"
	"time"
)

//go:embed assets/banner.js
var bannerScript string

// CSPHash admits exactly the embedded script without enabling arbitrary inline
// JavaScript in an app-authored Content-Security-Policy.
var CSPHash = func() string {
	sum := sha256.Sum256([]byte(bannerScript))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

// Snippet renders the invariant script body with all request-specific values
// confined to escaped data attributes.
func Snippet(slug, actor, subject, sessionID string, expiresAt time.Time) string {
	stopURL := `/app/` + html.EscapeString(slug) + `/.shinyhub/support-session/stop`
	deadline := expiresAt.UTC().Format(time.RFC3339)
	// The plain DOM rail is intentionally present before JavaScript runs. It
	// remains usable when scripting is disabled and under policies that block
	// dynamic styling; the enhanced shadow-root rail replaces it only after it
	// has mounted successfully.
	fallback := `<div id="shinyhub-support-session-fallback" role="region" aria-label="Active ShinyHub support session"` +
		` style="display:block!important;position:sticky!important;top:0!important;z-index:2147483647!important;padding:12px 16px!important;background:#21170a!important;color:#fff8e7!important;border-bottom:1px solid #a86608!important;font:600 14px/1.5 sans-serif!important;overflow-wrap:anywhere!important">` +
		`<strong>Support session · Viewing as ` + html.EscapeString(subject) + `</strong> · ` +
		html.EscapeString(actor) + ` is the administrator. Actions can change ` + html.EscapeString(subject) + `'s data. ` +
		`Ends at <time datetime="` + html.EscapeString(deadline) + `">` + html.EscapeString(deadline) + `</time>. ` +
		`<form method="post" action="` + stopURL + `" style="display:inline!important"><button type="submit" style="display:inline-block!important;min-height:44px!important">End support session</button></form></div>`
	return fallback + `<script id="shinyhub-support-session-loader"` +
		` data-stop-url="` + stopURL + `"` +
		` data-actor="` + html.EscapeString(actor) + `"` +
		` data-subject="` + html.EscapeString(subject) + `"` +
		` data-session-id="` + html.EscapeString(sessionID) + `"` +
		` data-expires-at="` + strconv.FormatInt(expiresAt.UnixMilli(), 10) + `">` +
		bannerScript + `</script>`
}

// BlockedPage is the no-JavaScript fail-closed surface used when an app's
// response cannot safely accept the mandatory banner (for example script-src
// 'none', malformed HTML, forced compression, or an oversized shell).
func BlockedPage(slug, actor, subject string, expiresAt time.Time) string {
	return blockedPage(slug, actor, subject,
		"ShinyHub cannot show this app because its response prevents the required safety banner from loading.", "", expiresAt, true)
}

// ScopeBlockedPage prevents a deleted-and-recreated slug from silently
// inheriting a capability that was approved for a different app record.
func ScopeBlockedPage(slug, actor, subject string, expiresAt time.Time) string {
	return blockedPage(slug, actor, subject,
		"The app at this address is no longer the app approved for this support session. The replacement app has not been displayed.", "", expiresAt, true)
}

// InactivePage is used when the durable activation lease can no longer be
// observed, including a launch reaped after no browser request arrived.
func InactivePage(slug, actor, subject string, expiresAt time.Time) string {
	return blockedPage(slug, actor, subject,
		"This support session is no longer active. The app has not been displayed.", "", expiresAt, false)
}

// BlockedPageWithError retains the fail-closed end/retry control when a stop
// attempt fails; a transient database error must never replace safety chrome
// with an unlabelled plain-text response.
func BlockedPageWithError(slug, actor, subject, message string, expiresAt time.Time) string {
	return blockedPage(slug, actor, subject,
		"ShinyHub cannot show this app because its response prevents the required safety banner from loading.", message, expiresAt, true)
}

func blockedPage(slug, actor, subject, explanation, message string, expiresAt time.Time, active bool) string {
	errorHTML := ""
	if message != "" {
		errorHTML = `<p role="alert" style="color:#f87171"><strong>` + html.EscapeString(message) + `</strong></p>`
	}
	deadline := expiresAt.UTC().Format(time.RFC3339)
	identityCopy := `The support identity was <strong>` + html.EscapeString(subject) + `</strong>.`
	deadlineCopy := `Its original deadline was <time datetime="` + html.EscapeString(deadline) + `">` + html.EscapeString(deadline) + `</time>.`
	actorCopy := `<strong>` + html.EscapeString(actor) + `</strong> was the administrator. The app has not been displayed.`
	pageState := "ended"
	actionCopy := "Clear session and return"
	if active {
		identityCopy = `The active support identity is <strong>` + html.EscapeString(subject) + `</strong>.`
		deadlineCopy = `It expires automatically by <time datetime="` + html.EscapeString(deadline) + `">` + html.EscapeString(deadline) + `</time>.`
		actorCopy = `<strong>` + html.EscapeString(actor) + `</strong> remains the administrator. The app has not been displayed.`
		pageState = "paused"
		actionCopy = "End support session"
	}
	return `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>Support session ` + pageState + ` · ShinyHub</title><style>` +
		`:root{color-scheme:dark;font-family:ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#0b111a;color:#f8fafc}` +
		`body{min-height:100vh;margin:0;display:grid;place-items:center;padding:24px;box-sizing:border-box}` +
		`main{max-width:620px;background:#121b28;border:1px solid #a86608;border-radius:14px;padding:28px;box-shadow:0 24px 70px rgba(0,0,0,.45);overflow-wrap:anywhere}` +
		`h1{font-size:24px;letter-spacing:-.02em;margin:0 0 12px;color:#fbbf24}p{line-height:1.6;color:#dce5f2}strong{color:#fff}` +
		`button{min-height:44px;margin-top:12px;border:0;border-radius:8px;background:#f59e0b;color:#241604;padding:10px 14px;font:700 14px inherit;cursor:pointer}` +
		`button:focus-visible{outline:3px solid #fff8e7;outline-offset:3px}</style><main><h1>Support session ` + pageState + `</h1>` +
		`<p>` + html.EscapeString(explanation) + `</p><p>` + identityCopy + `</p>` +
		`<p>` + deadlineCopy + `</p>` +
		`<p>` + actorCopy + `</p>` +
		errorHTML +
		`<form method="post" action="/app/` + html.EscapeString(slug) + `/.shinyhub/support-session/stop"><button type="submit">` + actionCopy + `</button></form>` +
		`</main></html>`
}
