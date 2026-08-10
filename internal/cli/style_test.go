package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// clearColorEnv unsets every variable colorEnabledFor consults so a test starts
// from a known environment regardless of the developer's shell.
func clearColorEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"NO_COLOR", "CLICOLOR", "CLICOLOR_FORCE", "FORCE_COLOR", "TERM"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	old := noColorFlag
	noColorFlag = false
	t.Cleanup(func() { noColorFlag = old })
}

func TestColorEnabledFor(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		noColor bool
		tty     bool
		want    bool
	}{
		{name: "terminal, clean env", tty: true, want: true},
		{name: "not a terminal", tty: false, want: false},
		{name: "--no-color beats a terminal", noColor: true, tty: true, want: false},
		{name: "--no-color beats FORCE_COLOR", noColor: true, env: map[string]string{"FORCE_COLOR": "1"}, tty: true, want: false},
		{name: "NO_COLOR beats a terminal", env: map[string]string{"NO_COLOR": "1"}, tty: true, want: false},
		{name: "NO_COLOR beats FORCE_COLOR", env: map[string]string{"NO_COLOR": "1", "FORCE_COLOR": "1"}, tty: true, want: false},
		{name: "empty NO_COLOR is not set", env: map[string]string{"NO_COLOR": ""}, tty: true, want: true},
		{name: "CLICOLOR=0 disables", env: map[string]string{"CLICOLOR": "0"}, tty: true, want: false},
		{name: "TERM=dumb disables", env: map[string]string{"TERM": "dumb"}, tty: true, want: false},
		{name: "FORCE_COLOR colors a pipe", env: map[string]string{"FORCE_COLOR": "1"}, tty: false, want: true},
		{name: "CLICOLOR_FORCE colors a pipe", env: map[string]string{"CLICOLOR_FORCE": "1"}, tty: false, want: true},
		{name: "FORCE_COLOR=0 does not force", env: map[string]string{"FORCE_COLOR": "0"}, tty: false, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearColorEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			noColorFlag = tc.noColor
			if got := colorEnabledFor(tc.tty); got != tc.want {
				t.Errorf("colorEnabledFor(tty=%v) = %v, want %v", tc.tty, got, tc.want)
			}
		})
	}
}

// TestStylerForNonFileWriterIsInert is the guard that keeps every other test in
// this package deterministic: command output captured into a buffer must never
// gain ANSI escapes or glyphs, whatever the ambient terminal or environment.
func TestStylerForNonFileWriterIsInert(t *testing.T) {
	clearColorEnv(t)
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "1")

	s := stylerFor(&bytes.Buffer{})
	if s.color || s.tty {
		t.Fatalf("buffer styler = %+v, want both switches off", s)
	}
	if got := s.green("running"); got != "running" {
		t.Errorf("green() on a buffer = %q, want unstyled", got)
	}
	if got := s.okPrefix(); got != "" {
		t.Errorf("okPrefix() on a buffer = %q, want empty", got)
	}
}

// TestStylerForNonTTYFileIsInert covers `shinyhub apps list > file`: a real
// *os.File that is not a terminal must not be colorized either.
func TestStylerForNonTTYFileIsInert(t *testing.T) {
	clearColorEnv(t)
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	s := stylerFor(f)
	if s.color || s.tty {
		t.Fatalf("regular-file styler = %+v, want both switches off", s)
	}
}

func TestStylerPaint(t *testing.T) {
	on := styler{color: true}
	if got, want := on.red("boom"), ansiRed+"boom"+ansiReset; got != want {
		t.Errorf("red() = %q, want %q", got, want)
	}
	if got := on.red(""); got != "" {
		t.Errorf("red(\"\") = %q, want empty (no stray escapes)", got)
	}
	off := styler{}
	if got := off.red("boom"); got != "boom" {
		t.Errorf("disabled red() = %q, want unstyled", got)
	}
}

func TestStylerStatusColors(t *testing.T) {
	s := styler{color: true}
	cases := []struct {
		word string
		want string
	}{
		{"running", ansiGreen},
		{"failed", ansiRed},
		{"deploying", ansiYellow},
		{"stopped", ansiDim},
	}
	for _, tc := range cases {
		if got := s.status(tc.word); !strings.HasPrefix(got, tc.want) {
			t.Errorf("status(%q) = %q, want prefix %q", tc.word, got, tc.want)
		}
	}
	// A status this CLI does not know about must render plainly rather than be
	// assigned a meaning it may not have.
	if got := s.status("quiesced"); got != "quiesced" {
		t.Errorf("status(unknown) = %q, want unstyled", got)
	}
}

// TestStatusColorsAreDistinct guards the point of the feature: the four state
// classes must not collapse onto one escape sequence.
func TestStatusColorsAreDistinct(t *testing.T) {
	s := styler{color: true}
	seen := map[string]string{}
	for _, word := range []string{"running", "failed", "deploying", "stopped"} {
		code := strings.SplitAfter(s.status(word), "m")[0]
		if prev, dup := seen[code]; dup {
			t.Errorf("%q and %q both render as %q", prev, word, code)
		}
		seen[code] = word
	}
}

// TestGlyphsSurviveNoColor pins the split between the two switches: NO_COLOR
// asks for no color, not for a different layout, so the marker stays.
func TestGlyphsSurviveNoColor(t *testing.T) {
	s := styler{color: false, tty: true}
	if got := s.okPrefix(); got != "✓ " {
		t.Errorf("okPrefix() with color off = %q, want a plain glyph", got)
	}
	if got := s.failPrefix(); got != "✗ " {
		t.Errorf("failPrefix() with color off = %q, want a plain glyph", got)
	}
}

func TestGlyphASCIIFallback(t *testing.T) {
	s := styler{tty: true, ascii: true}
	if got := s.okPrefix(); got != "v " {
		t.Errorf("ascii okPrefix() = %q, want %q", got, "v ")
	}
	if got := s.failPrefix(); got != "x " {
		t.Errorf("ascii failPrefix() = %q, want %q", got, "x ")
	}
}

func TestUTF8Locale(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "nothing set assumes UTF-8", want: true},
		{name: "LANG UTF-8", env: map[string]string{"LANG": "en_US.UTF-8"}, want: true},
		{name: "LANG utf8 lowercase", env: map[string]string{"LANG": "en_US.utf8"}, want: true},
		{name: "LANG C", env: map[string]string{"LANG": "C"}, want: false},
		{name: "LC_ALL wins over LANG", env: map[string]string{"LC_ALL": "C", "LANG": "en_US.UTF-8"}, want: false},
		{name: "LC_CTYPE wins over LANG", env: map[string]string{"LC_CTYPE": "en_US.UTF-8", "LANG": "C"}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
				t.Setenv(name, "")
				os.Unsetenv(name)
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := utf8Locale(); got != tc.want {
				t.Errorf("utf8Locale() = %v, want %v", got, tc.want)
			}
		})
	}
}
