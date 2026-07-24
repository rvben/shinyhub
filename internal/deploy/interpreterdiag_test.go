package deploy

import (
	"errors"
	"strings"
	"testing"
)

func TestInterpreterResolutionHint(t *testing.T) {
	base := errors.New("uv sync: exit status 2")

	t.Run("nil passes through", func(t *testing.T) {
		if got := interpreterResolutionHint([]byte("python-build-standalone"), nil); got != nil {
			t.Errorf("nil error must stay nil, got %v", got)
		}
	})

	t.Run("unrelated build output is unchanged", func(t *testing.T) {
		out := []byte("error: failed to resolve dependencies for pandas")
		got := interpreterResolutionHint(out, base)
		if got.Error() != base.Error() {
			t.Errorf("non-interpreter failure must pass through unchanged, got %q", got)
		}
	})

	t.Run("blocked managed download gains the hint and keeps the cause", func(t *testing.T) {
		out := []byte("error: Failed to download https://github.com/astral-sh/python-build-standalone/releases/download/x.tar.gz (403)")
		got := interpreterResolutionHint(out, base)
		if !errors.Is(got, base) {
			t.Error("wrapped error must still unwrap to the original cause")
		}
		msg := got.Error()
		for _, want := range []string{"build.python_preference", "build.python_install_mirror", "docs/environment.md"} {
			if !strings.Contains(msg, want) {
				t.Errorf("hint must name %q, got %q", want, msg)
			}
		}
	})

	t.Run("no interpreter found gains the hint", func(t *testing.T) {
		out := []byte("error: No interpreter found for Python >=3.12 in managed installations or search path")
		got := interpreterResolutionHint(out, base)
		if !strings.Contains(got.Error(), "build.python_preference") {
			t.Errorf("hint must name the config knob, got %q", got)
		}
	})
}
