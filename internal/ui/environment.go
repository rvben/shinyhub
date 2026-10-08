package ui

import (
	"github.com/rvben/shinyhub/internal/appnav"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/envui"
	"github.com/rvben/shinyhub/internal/favicon"
	"net/url"
)

// DecorateEnvironment is only for platform-owned HTML, never custom landing HTML.
func DecorateEnvironment(page []byte, e *config.EnvironmentConfig, u *url.URL) []byte {
	if e == nil {
		return page
	}
	page, _ = favicon.PrefixTitle(page, e.Prefix(), "ShinyHub")
	page, _ = favicon.ReplaceIcons(page, envui.IconURL(e))
	out, _ := appnav.SpliceIntoBody(page, envui.Snippet(e, u, true))
	return out
}
