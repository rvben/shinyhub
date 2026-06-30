package deployfail

import (
	"errors"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want Kind
	}{
		{"r runtime missing", `all replicas failed health check: replica 0: start: exec: "Rscript": executable file not found in $PATH`, RuntimeMissing},
		{"python runtime missing via uv", `uv sync: exec: "uv": executable file not found in $PATH`, RuntimeMissing},
		{"build failed uv sync", `uv sync: error: failed to resolve dependencies for pandas`, BuildFailed},
		{"build failed renv", `renv restore: error: package 'shiny' is not available`, BuildFailed},
		{"bundle invalid no entrypoint", `no app.py or app.R found in /data/apps/x/versions/1`, BundleInvalid},
		{"bundle invalid bad manifest", `read manifest: toml: line 3: expected '='`, BundleInvalid},
		{"bundle invalid manifest command", `manifest [app] command: empty command`, BundleInvalid},
		{"readiness timeout", `all replicas failed health check: replica 0: health: app at http://127.0.0.1:1/ did not become healthy within 120s`, ReadinessTimeout},
		{"crashed", `all replicas failed health check: replica 0: health: app at http://127.0.0.1:1/ crashed on startup before becoming healthy`, Crashed},
		{"mixed crash and timeout prefers crashed", `all replicas failed health check: replica 0: health: app at x crashed on startup before becoming healthy` + "\n" + `replica 1: health: app at y did not become healthy within 120s`, Crashed},
		{"unclassified 5xx", `internal error: database is locked`, ServerError},
		{"build timeout", `uv sync: build exceeded the build timeout: context deadline exceeded`, BuildFailed},
		{"renv build timeout", `renv restore: build exceeded the build timeout: context deadline exceeded`, BuildFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(errors.New(tc.msg)); got != tc.want {
				t.Errorf("Classify(%q) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
}

func TestClassifyNilIsEmpty(t *testing.T) {
	if got := Classify(nil); got != "" {
		t.Errorf("Classify(nil) = %q, want empty", got)
	}
}

func TestMentionsMissingExecutable(t *testing.T) {
	if !MentionsMissingExecutable(`exec: "uv": executable file not found in $PATH`, "uv") {
		t.Error("should detect a quoted missing executable")
	}
	if MentionsMissingExecutable(`uv sync: resolution failed`, "uv") {
		t.Error("a build error that merely mentions uv is not a missing executable")
	}
}
