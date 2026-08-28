// Package favicon owns the browser-tab identity shared by ShinyHub's control
// surface, its app lifecycle pages, and proxied apps.
package favicon

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"

	xhtml "golang.org/x/net/html"
)

const (
	// RootURL is the conventional favicon URL on the control origin. Besides
	// explicit links, browsers request it for operator-supplied landing pages
	// that do not declare an icon of their own.
	RootURL = "/favicon.ico"

	// PlatformURL is reachable on both the control and isolated app origins.
	// The app-origin boundary intentionally admits only /app/*, so app-facing
	// ShinyHub pages use this path rather than RootURL.
	PlatformURL = "/app/.shinyhub/favicon.ico"

	// AppSuffix is the per-app icon endpoint. Keeping it below the app slug
	// means it is same-origin in both single- and split-origin deployments.
	AppSuffix = "/.shinyhub/favicon"
)

// AppURL returns the same-origin favicon endpoint for slug.
func AppURL(slug string) string {
	return "/app/" + url.PathEscape(slug) + AppSuffix
}

// Link renders a favicon link with an escaped browser-ready URL.
func Link(href string) string {
	return `<link rel="icon" href="` + html.EscapeString(href) + `">`
}

// Title renders a browser title with escaped text.
func Title(text string) string {
	return `<title>` + html.EscapeString(text) + `</title>`
}

// Ensure inserts a favicon link immediately before </head> unless the page
// already declares a rel=icon link. Existing app-authored identity always wins.
// The original bytes are returned unchanged when no insertion point exists.
func Ensure(page []byte, href string) ([]byte, bool) {
	if href == "" || HasIcon(page) {
		return page, false
	}
	idx := lastIndexFold(page, []byte("</head>"))
	if idx < 0 {
		return page, false
	}
	snippet := []byte(Link(href) + "\n")
	out := make([]byte, 0, len(page)+len(snippet))
	out = append(out, page[:idx]...)
	out = append(out, snippet...)
	out = append(out, page[idx:]...)
	return out, true
}

// EnsureTitle inserts a title immediately before </head> unless the page
// already declares one. Existing app-authored title text always wins.
func EnsureTitle(page []byte, text string) ([]byte, bool) {
	if text == "" {
		return page, false
	}
	if hasTitle(page) {
		return page, false
	}
	idx := lastIndexFold(page, []byte("</head>"))
	if idx < 0 {
		return page, false
	}
	snippet := []byte(Title(text) + "\n")
	out := make([]byte, 0, len(page)+len(snippet))
	out = append(out, page[:idx]...)
	out = append(out, snippet...)
	out = append(out, page[idx:]...)
	return out, true
}

// SetTitle replaces the first title element, or inserts one when absent. It is
// for ShinyHub-authored lifecycle pages; proxied app HTML must use EnsureTitle
// so an app's own title is never altered.
func SetTitle(page []byte, text string) ([]byte, bool) {
	if text == "" {
		return page, false
	}
	start, end, ok := titleElementBounds(page)
	if !ok {
		if hasTitle(page) {
			return page, false
		}
		return EnsureTitle(page, text)
	}
	replacement := []byte(Title(text))
	out := make([]byte, 0, len(page)-(end-start)+len(replacement))
	out = append(out, page[:start]...)
	out = append(out, replacement...)
	out = append(out, page[end:]...)
	return out, true
}

func hasTitle(page []byte) bool {
	z := xhtml.NewTokenizer(bytes.NewReader(page))
	for {
		switch z.Next() {
		case xhtml.ErrorToken:
			return false
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			tok := z.Token()
			if strings.EqualFold(tok.Data, "title") {
				return true
			}
		}
	}
}

// titleElementBounds returns the raw byte range of the first title element.
// The tokenizer keeps detection out of comments and scripts while the raw
// offsets let callers preserve every unrelated source byte exactly.
func titleElementBounds(page []byte) (int, int, bool) {
	z := xhtml.NewTokenizer(bytes.NewReader(page))
	offset := 0
	start := -1
	for {
		typ := z.Next()
		raw := z.Raw()
		tokenStart := offset
		offset += len(raw)
		switch typ {
		case xhtml.ErrorToken:
			return 0, 0, false
		case xhtml.StartTagToken:
			tok := z.Token()
			if start < 0 && strings.EqualFold(tok.Data, "title") {
				start = tokenStart
			}
		case xhtml.SelfClosingTagToken:
			tok := z.Token()
			if strings.EqualFold(tok.Data, "title") {
				return tokenStart, offset, true
			}
		case xhtml.EndTagToken:
			tok := z.Token()
			if start >= 0 && strings.EqualFold(tok.Data, "title") {
				return start, offset, true
			}
		}
	}
}

// HasIcon reports whether page has an HTML link whose rel token list contains
// "icon". Parsing only for detection lets Ensure preserve the source bytes and
// tolerate attribute ordering, quoting, case, and additional rel tokens.
func HasIcon(page []byte) bool {
	z := xhtml.NewTokenizer(bytes.NewReader(page))
	for {
		switch z.Next() {
		case xhtml.ErrorToken:
			return false
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			tok := z.Token()
			if !strings.EqualFold(tok.Data, "link") {
				continue
			}
			for _, attr := range tok.Attr {
				if !strings.EqualFold(attr.Key, "rel") {
					continue
				}
				for _, rel := range strings.Fields(attr.Val) {
					if strings.EqualFold(rel, "icon") {
						return true
					}
				}
			}
		}
	}
}

// EmojiSVG turns a validated app emoji into a compact, script-free SVG. The
// browser paints it with its native color-emoji font, so the tab matches the
// emoji the user selected elsewhere in ShinyHub.
func EmojiSVG(emoji string) []byte {
	return []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">` +
		`<text x="32" y="34" text-anchor="middle" dominant-baseline="central" font-size="52" ` +
		`font-family="Apple Color Emoji,Segoe UI Emoji,Noto Color Emoji,sans-serif">` +
		html.EscapeString(emoji) + `</text></svg>`)
}

// Write serves icon bytes with content validation and revalidation caching.
// SVG is sandboxed so direct navigation cannot execute embedded script.
func Write(w http.ResponseWriter, r *http.Request, mime string, data []byte) {
	etag := fmt.Sprintf("\"%x\"", sha256.Sum256(data))
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func lastIndexFold(haystack, needle []byte) int {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return -1
	}
	for i := len(haystack) - len(needle); i >= 0; i-- {
		if bytes.EqualFold(haystack[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
}
