// Package slug holds the canonical slug-validation regex shared by the API
// server, CLI, and any future surface that needs to validate or display the
// rules. Slugs become DNS-style identifiers (subdomains, path segments,
// filesystem dirs), so the rules mirror RFC 1123 hostname labels: lowercase
// alphanumerics and hyphens, must start and end with an alphanumeric, and at
// most 63 characters.
package slug

import "regexp"

// Pattern is the canonical regex (without anchors) used by both the Go and
// the SPA validators. Keep this string in sync with the SLUG_RE constant in
// internal/ui/static/app.js and the `pattern=` attribute in index.html.
const Pattern = `[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?`

// AnchoredPattern wraps Pattern with start/end anchors for use as a full match.
const AnchoredPattern = `^` + Pattern + `$`

// HumanRule is the user-facing description of the rule, suitable for error
// messages and UI hints.
const HumanRule = "1–63 lowercase letters, digits, or hyphens; must start and end with a letter or digit"

// MaxLen is the maximum slug length, mirroring the regex bound.
const MaxLen = 63

// Re is the compiled validator. Use Re.MatchString(s) for fast checks.
var Re = regexp.MustCompile(AnchoredPattern)

// Valid reports whether s is a syntactically valid slug.
func Valid(s string) bool { return Re.MatchString(s) }
