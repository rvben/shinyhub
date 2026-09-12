// Package apporigin holds the single source of truth for what the app origin
// serves. The app origin is a narrow virtual host: it exposes app proxy
// traffic, liveness probes, and the public platform favicon, and nothing else.
//
// The list lives here rather than inline in the boundary handler because a
// second implementation of it runs at the Cloudflare edge
// (deploy/cloudflare-demo/src/edge-policy.ts), where rejecting a path the
// server would 404 anyway saves waking a sleeping container.
// TestDemoWorkerAppOriginMatchesServer pins that copy to this one.
package apporigin

import (
	"strings"

	"github.com/rvben/shinyhub/internal/favicon"
)

// Prefixes are the path prefixes the app origin serves.
func Prefixes() []string {
	return []string{"/app/"}
}

// ExactPaths are the paths the app origin serves by exact match.
func ExactPaths() []string {
	return []string{"/healthz", "/readyz", favicon.RootURL}
}

// Admits reports whether the app origin serves path. Everything else belongs to
// the control origin and is 404ed on a host whose JavaScript is not trusted.
func Admits(path string) bool {
	for _, exact := range ExactPaths() {
		if path == exact {
			return true
		}
	}
	for _, prefix := range Prefixes() {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
