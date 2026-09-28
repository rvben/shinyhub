package deploy

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/rvben/shinyhub/internal/deployevent"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/spanerr"
)

// indexEnvExact are the package-index configuration variables recognized for
// build diagnostics: the same set SanitizedEnv allow-lists for dependency
// resolution (uv, pip, renv). Credential-bearing variables are matched by
// suffix below and always masked.
var indexEnvExact = map[string]struct{}{
	"UV_DEFAULT_INDEX": {}, "UV_INDEX": {}, "UV_INDEX_URL": {}, "UV_EXTRA_INDEX_URL": {},
	"UV_INDEX_STRATEGY": {}, "UV_FIND_LINKS": {},
	"PIP_INDEX_URL": {}, "PIP_EXTRA_INDEX_URL": {},
	"RENV_CONFIG_REPOS_OVERRIDE": {},
}

// isUvIndexCredentialKey matches uv's per-named-index credential variables,
// UV_INDEX_{NAME}_USERNAME / UV_INDEX_{NAME}_PASSWORD.
func isUvIndexCredentialKey(key string) bool {
	rest, ok := strings.CutPrefix(key, "UV_INDEX_")
	if !ok || rest == "" {
		return false
	}
	return strings.HasSuffix(rest, "_USERNAME") || strings.HasSuffix(rest, "_PASSWORD")
}

// collectIndexEnv extracts the package-index configuration from an environment
// as redacted KEY=value strings, safe for logs and error messages: credential
// variables are fully masked, and in every URL the userinfo
// (https://user:pass@host) and each query value are replaced with "***"
// (see spanerr.RedactURLs). redact, when non-nil, masks further values (see
// requirementsRedactor). Non-index variables are excluded. Order follows env.
func collectIndexEnv(env []string, redact func(string) string) []string {
	var out []string
	for _, e := range env {
		key, val, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		if isUvIndexCredentialKey(key) {
			out = append(out, key+"=***")
			continue
		}
		if _, ok := indexEnvExact[key]; !ok {
			continue
		}
		if key == "UV_FIND_LINKS" {
			val = redactFindLinks(val)
		} else {
			val = spanerr.RedactURLs(val)
		}
		if redact != nil {
			val = redact(val)
		}
		out = append(out, key+"="+val)
	}
	return out
}

// schemePrefix matches the start of a URL.
var schemePrefix = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)

// redactFindLinks redacts a comma-separated UV_FIND_LINKS value. A comma may
// also sit inside a query value, so the list is only split where the next
// entry starts a new URL; anything else after a comma stays with the URL
// before it and is masked with its query.
func redactFindLinks(val string) string {
	var chunks []string
	for i, part := range strings.Split(val, ",") {
		if i == 0 || schemePrefix.MatchString(part) {
			chunks = append(chunks, part)
		} else {
			chunks[len(chunks)-1] += "," + part
		}
	}
	for i, c := range chunks {
		chunks[i] = spanerr.RedactURLs(c)
	}
	return strings.Join(chunks, ",")
}

// requirementsRedactor returns a function that masks the values dir's
// requirements.txt expanded for ${NAME} references against env. Those are
// usually credentials and can sit anywhere in a URL (a query-string token),
// beyond the reach of userinfo redaction. It masks nothing when the file is
// absent or unreadable; the build step reports an unreadable file itself.
func requirementsRedactor(dir string, env []string) func(string) string {
	idx, err := process.ReadRequirementsIndex(dir, env)
	if err != nil {
		return nil
	}
	return idx.Redact
}

// indexResolutionHint annotates a failed build step whose output carries uv's
// registry-miss signature ("<pkg> was not found in the package registry") with
// the package-index configuration the build actually saw, redacted. The
// distinction matters operationally: "no configuration reached this build"
// points at the platform env plumbing (service env, per-app env vars), while a
// listed configuration points at the index content or the dependency name.
// Errors without the signature, and nil, pass through unchanged. redact is
// passed to collectIndexEnv.
func indexResolutionHint(out []byte, err error, env []string, redact func(string) string) error {
	if err == nil || !strings.Contains(strings.ToLower(string(out)), "not found in the package registry") {
		return err
	}
	if indexes := collectIndexEnv(env, redact); len(indexes) > 0 {
		return fmt.Errorf("%w (package-index configuration seen by this build: %s)",
			err, strings.Join(indexes, ", "))
	}
	return fmt.Errorf("%w (no package-index configuration reached this build; if this dependency lives on a private index, set UV_EXTRA_INDEX_URL in the service environment or as a per-app env var - see docs/environment.md)", err)
}

// effectiveEnv reduces env to the entries a process would see: the last
// occurrence of each key, in first-appearance order of the keys.
func effectiveEnv(env []string) []string {
	last := make(map[string]int, len(env))
	for i, e := range env {
		k, _, _ := strings.Cut(e, "=")
		last[k] = i
	}
	out := make([]string, 0, len(last))
	for i, e := range env {
		if k, _, _ := strings.Cut(e, "="); last[k] == i {
			out = append(out, e)
		}
	}
	return out
}

// reportRequirementsIndexOverride warns, on the deploy stream, when the
// bundle's requirements.txt names a default index that replaces one the
// server or the app configures. That is the bundle's call to make, as it is
// for pip, but an operator who set a server-wide mirror should see it happen
// rather than discover it from where packages came from. The URL is redacted,
// including any value a ${NAME} reference expanded into it.
// The check is advisory: a requirements file that cannot be read fails the
// build or the launch, which report it themselves.
func reportRequirementsIndexOverride(p Params) {
	appEnv, _ := p.Manager.ResolveAppEnv(p.Slug)
	base := append(process.SanitizedEnv(), appEnv...)
	idx, err := process.ReadRequirementsIndex(p.BundleDir, base)
	if err != nil {
		return
	}
	replaced := idx.Overrides(base)
	if len(replaced) == 0 {
		return
	}
	msg := fmt.Sprintf("requirements.txt sets --index-url %s, which replaces the configured %s",
		idx.Redact(spanerr.RedactURLs(idx.DefaultIndex)), strings.Join(replaced, " and "))
	slog.Warn("deploy: requirements index replaces configured index", "slug", p.Slug, "detail", msg)
	p.report(deployevent.Phase("bundle", deployevent.StatusWarning, msg))
}
