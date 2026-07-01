package sandbox

import (
	"slices"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	got, err := splitCommand([]string{"__sandbox", "--", "uv", "run", "app.py"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !slices.Equal(got, []string{"uv", "run", "app.py"}) {
		t.Errorf("got %v", got)
	}
}

func TestSplitCommand_Errors(t *testing.T) {
	if _, err := splitCommand([]string{"__sandbox", "uv", "run"}); err == nil {
		t.Error("missing '--' must error")
	}
	if _, err := splitCommand([]string{"__sandbox", "--"}); err == nil {
		t.Error("empty command after '--' must error")
	}
}

// scrubEnv removes only the sandbox spec var, so the app never inherits its own
// policy, while leaving every other variable intact.
func TestScrubEnv(t *testing.T) {
	in := []string{"PATH=/usr/bin", EnvVar + "={\"level\":\"standard\"}", "HOME=/home/app", EnvVar + "=dup"}
	got := scrubEnv(in, EnvVar)
	want := []string{"PATH=/usr/bin", "HOME=/home/app"}
	if !slices.Equal(got, want) {
		t.Errorf("scrubEnv = %v, want %v", got, want)
	}
}

func TestScrubEnv_KeepsPrefixCollisions(t *testing.T) {
	// A different var that merely starts with the same letters must survive.
	in := []string{EnvVar + "_EXTRA=keep", EnvVar + "=drop"}
	got := scrubEnv(in, EnvVar)
	if !slices.Equal(got, []string{EnvVar + "_EXTRA=keep"}) {
		t.Errorf("scrubEnv dropped a prefix-collision var: %v", got)
	}
}
