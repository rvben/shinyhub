// Package envui renders config-owned deployment identity without polling or database state.
package envui

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/url"
	"strconv"
	"strings"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/favicon"
	"github.com/rvben/shinyhub/internal/hubroute"
	"github.com/rvben/shinyhub/internal/slug"
)

//go:embed assets/environment.js
var Script string
var CSPHash = func() string {
	sum := sha256.Sum256([]byte(Script))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

func IconURL(e *config.EnvironmentConfig) string {
	return favicon.PlatformURL + "?environment=" + e.IconKey()
}

// ProductionHref preserves application state but never carries platform launch credentials.
// raw query pairs are filtered without re-encoding the remaining application values.
func ProductionHref(e *config.EnvironmentConfig, u *url.URL) string {
	if e == nil || e.ProductionURL == "" {
		return ""
	}
	target := strings.TrimRight(e.ProductionURL, "/")
	if u == nil || u.Host != "" || u.IsAbs() {
		return target + "/"
	}
	rest := strings.TrimPrefix(u.Path, "/app/")
	if rest != u.Path {
		name, tail, _ := strings.Cut(rest, "/")
		if slug.Valid(name) {
			if tail == ".shinyhub" || strings.HasPrefix(tail, ".shinyhub/") {
				return target + "/app/" + name + "/"
			}
			var pairs []string
			for _, pair := range strings.Split(u.RawQuery, "&") {
				key, _, _ := strings.Cut(pair, "=")
				key, err := url.QueryUnescape(key)
				if err == nil && strings.HasPrefix(key, "__shinyhub_") {
					continue
				}
				pairs = append(pairs, pair)
			}
			result := target + u.EscapedPath()
			if query := strings.Join(pairs, "&"); query != "" {
				result += "?" + query
			}
			if u.Fragment != "" {
				result += "#" + u.EscapedFragment()
			}
			return result
		}
	}
	if u.Path != "/login" && hubroute.IsUIPath(u.Path) {
		return target + u.EscapedPath()
	}
	return target + "/"
}

func Snippet(e *config.EnvironmentConfig, u *url.URL, iconAllowed bool) string {
	if e == nil {
		return ""
	}
	routes, _ := json.Marshal(hubroute.ExactUIRoutes())
	icon := ""
	if iconAllowed {
		icon = IconURL(e)
	}
	esc := escapeAttribute
	return `<script id="shinyhub-environment-loader" data-label="` + esc(e.Label) + `" data-color="` + esc(e.EffectiveColor()) + `" data-text-color="` + esc(e.TextColor()) + `" data-message="` + esc(e.Message) + `" data-production-url="` + esc(e.ProductionURL) + `" data-production-href="` + esc(ProductionHref(e, u)) + `" data-icon="` + esc(icon) + `" data-ui-routes="` + esc(string(routes)) + `">` + Script + `</script>`
}

// Attribute entities are ASCII even when an app declares a legacy charset.
func escapeAttribute(text string) string {
	var out strings.Builder
	for _, r := range html.EscapeString(text) {
		if r > 127 {
			out.WriteString("&#" + strconv.Itoa(int(r)) + ";")
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}
