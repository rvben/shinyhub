package process

import (
	"strings"
	"testing"
)

func envHas(env []string, key string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, key+"=") {
			return true
		}
	}
	return false
}

// TestSanitizedEnv_AllowListDropsSecretsKeepsEssentials proves the env base for
// app-controlled code is an allow-list: OS/runtime essentials pass through while
// arbitrary server-process secrets (cloud credentials, tokens, SHINYHUB_*) are
// dropped (SEC-H2).
func TestSanitizedEnv_AllowListDropsSecretsKeepsEssentials(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/home/app")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("HTTPS_PROXY", "http://proxy:8080")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "leak-me")
	t.Setenv("GITHUB_TOKEN", "leak-me-too")
	t.Setenv("SHINYHUB_AUTH_SECRET", "server-secret")

	env := SanitizedEnv()

	for _, keep := range []string{"PATH", "HOME", "LC_ALL", "HTTPS_PROXY"} {
		if !envHas(env, keep) {
			t.Errorf("allow-list dropped essential %s", keep)
		}
	}
	for _, drop := range []string{"AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "SHINYHUB_AUTH_SECRET"} {
		if envHas(env, drop) {
			t.Errorf("allow-list leaked secret %s into app env", drop)
		}
	}
}

// An operator-set UV_PYTHON_INSTALL_DIR (shared managed-Python store) is a
// non-secret tool knob that must reach uv on every app-controlled path -
// builds, hooks, and launches - not only where the build sandbox re-exports
// it, so it belongs on the exact allow-list.
func TestSanitizedEnv_AllowsUvPythonInstallDir(t *testing.T) {
	t.Setenv("UV_PYTHON_INSTALL_DIR", "/opt/shinyhub/uv-python")
	if !envHas(SanitizedEnv(), "UV_PYTHON_INSTALL_DIR") {
		t.Error("allow-list dropped UV_PYTHON_INSTALL_DIR")
	}
}

// Package-index configuration (private registries: corp Nexus/Artifactory
// PyPI mirrors, private CRAN) is same-class as the proxy and TLS-trust vars
// already allow-listed: its sole purpose is to be consumed by dependency
// resolution in app builds, unlike control-plane secrets (AWS/GCP creds,
// tokens) that the allow-list exists to block. Dropping these vars makes uv
// silently fall back to PyPI-only and every private-index dep fails with
// "not found in the package registry".
func TestSanitizedEnv_AllowsPackageIndexVars(t *testing.T) {
	indexVars := []string{
		// uv current names.
		"UV_DEFAULT_INDEX", "UV_INDEX", "UV_FIND_LINKS",
		// uv deprecated-but-honored names.
		"UV_INDEX_URL", "UV_EXTRA_INDEX_URL",
		// Named-index credentials and behavior (UV_INDEX_ prefix family).
		"UV_INDEX_CORP_USERNAME", "UV_INDEX_CORP_PASSWORD", "UV_INDEX_STRATEGY",
		// pip equivalents (build backends that shell out to pip).
		"PIP_INDEX_URL", "PIP_EXTRA_INDEX_URL",
		// renv private-repo override.
		"RENV_CONFIG_REPOS_OVERRIDE",
	}
	// Only uv's recognized UV_INDEX_* names pass: an unrelated server secret
	// that merely shares the prefix must stay blocked.
	notIndexVars := []string{"UV_INDEXING_UNRELATED", "UV_INDEX_TOKEN", "UV_INDEX_INTERNAL_SECRET"}
	for _, v := range append(append([]string{}, indexVars...), notIndexVars...) {
		t.Setenv(v, "https://nexus.example.com/repository/pypi/simple")
	}

	env := SanitizedEnv()
	for _, v := range indexVars {
		if !envHas(env, v) {
			t.Errorf("allow-list dropped package-index var %s", v)
		}
	}
	for _, v := range notIndexVars {
		if envHas(env, v) {
			t.Errorf("allow-list leaked non-index var %s", v)
		}
	}
}

// TestSanitizedEnv_EscapeHatch proves an operator can pass an extra variable
// through via SHINYHUB_APP_ENV_ALLOW without exposing the whole environment.
func TestSanitizedEnv_EscapeHatch(t *testing.T) {
	t.Setenv("MY_CUSTOM_APP_VAR", "value")
	t.Setenv("ANOTHER_SECRET", "nope")
	t.Setenv("SHINYHUB_APP_ENV_ALLOW", "MY_CUSTOM_APP_VAR, SPACED_NAME")

	env := SanitizedEnv()
	if !envHas(env, "MY_CUSTOM_APP_VAR") {
		t.Error("escape hatch did not allow MY_CUSTOM_APP_VAR")
	}
	if envHas(env, "ANOTHER_SECRET") {
		t.Error("escape hatch leaked a non-listed var")
	}
	if envHas(env, "SHINYHUB_APP_ENV_ALLOW") {
		t.Error("the allow-list control var itself must not pass through")
	}
}
