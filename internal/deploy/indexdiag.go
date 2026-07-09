package deploy

import (
	"fmt"
	"strings"
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
// variables are fully masked and URL userinfo (https://user:pass@host) is
// replaced with "***". Non-index variables are excluded. Order follows env.
func collectIndexEnv(env []string) []string {
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
		out = append(out, key+"="+redactURLUserinfo(val))
	}
	return out
}

// redactURLUserinfo masks the userinfo component of every URL in a
// (possibly space-separated, e.g. UV_INDEX) value: "https://u:p@host/x"
// becomes "https://***@host/x". Tokens without userinfo pass unchanged.
func redactURLUserinfo(val string) string {
	tokens := strings.Fields(val)
	for i, tok := range tokens {
		scheme, rest, ok := strings.Cut(tok, "://")
		if !ok {
			continue
		}
		// Userinfo ends at the first "@" before the first "/" of the authority.
		authorityEnd := len(rest)
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			authorityEnd = slash
		}
		if at := strings.LastIndexByte(rest[:authorityEnd], '@'); at >= 0 {
			tokens[i] = scheme + "://***@" + rest[at+1:]
		}
	}
	if len(tokens) == 0 {
		return val
	}
	return strings.Join(tokens, " ")
}

// indexResolutionHint annotates a failed build step whose output carries uv's
// registry-miss signature ("<pkg> was not found in the package registry") with
// the package-index configuration the build actually saw, redacted. The
// distinction matters operationally: "no configuration reached this build"
// points at the platform env plumbing (service env, per-app env vars), while a
// listed configuration points at the index content or the dependency name.
// Errors without the signature, and nil, pass through unchanged.
func indexResolutionHint(out []byte, err error, env []string) error {
	if err == nil || !strings.Contains(strings.ToLower(string(out)), "not found in the package registry") {
		return err
	}
	if indexes := collectIndexEnv(env); len(indexes) > 0 {
		return fmt.Errorf("%w (package-index configuration seen by this build: %s)",
			err, strings.Join(indexes, ", "))
	}
	return fmt.Errorf("%w (no package-index configuration reached this build; if this dependency lives on a private index, set UV_EXTRA_INDEX_URL in the service environment or as a per-app env var - see docs/environment.md)", err)
}
