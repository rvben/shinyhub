package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestFailMarkSurvivesAnASCIILocale pins the reason failMark exists. glyphPaint
// matches the Unicode glyph literally, so composing it with glyphFail silently
// stops painting the moment the locale is not UTF-8: glyphFail returns "x",
// glyphPaint falls through its switch, and the marker loses its color exactly
// where it is hardest to notice.
func TestFailMarkSurvivesAnASCIILocale(t *testing.T) {
	s := styler{color: true, ascii: true}

	if got := s.failMark(); got != ansiRed+"x"+ansiReset {
		t.Errorf("failMark() = %q, want a painted ASCII cross", got)
	}
	if got := s.glyphPaint(s.glyphFail()); got != "x" {
		t.Fatalf("glyphPaint(glyphFail()) = %q; if this now paints, failMark's "+
			"documented reason for existing is stale", got)
	}
}

// TestFailMarkIsPresentOffATerminal separates failMark from failPrefix. A
// problem list is a list because every item carries a marker, so the marker has
// to survive being piped; failPrefix is an ornament on a sentence and does not.
func TestFailMarkIsPresentOffATerminal(t *testing.T) {
	s := styler{}

	if got := s.failMark(); got != "✗" {
		t.Errorf("failMark() off a terminal = %q, want a bare glyph", got)
	}
	if got := s.failPrefix(); got != "" {
		t.Errorf("failPrefix() off a terminal = %q, want empty", got)
	}
}

// TestEnvPlanTextIsPlainOffATerminal is the byte-compatibility guarantee for the
// env apply plan: a script or a diff of captured output sees exactly what it saw
// before the markers were painted.
func TestEnvPlanTextIsPlainOffATerminal(t *testing.T) {
	var buf bytes.Buffer
	plan := envApplyPlan{
		Adds:    []envApplyOp{{Key: "API_URL"}, {Key: "API_TOKEN", Secret: true}},
		Updates: []envApplyOp{{Key: "LOG_LEVEL", Reason: "value differs"}},
		Deletes: []envApplyOp{{Key: "OLD_FLAG"}},
	}
	renderEnvPlanText(&buf, plan, true, 0)

	want := strings.Join([]string{
		"Plan (dry run, no changes applied):",
		"  + API_URL",
		"  + API_TOKEN (secret)",
		"  ~ LOG_LEVEL [value differs]",
		"  - OLD_FLAG",
		"",
	}, "\n")
	if got := buf.String(); got != want {
		t.Errorf("plan text =\n%q\nwant\n%q", got, want)
	}
}

// TestEnvPlanTextHasNoDash guards the user-facing string against an em or en
// dash, which this project does not use in output it prints to people.
func TestEnvPlanTextHasNoDash(t *testing.T) {
	var buf bytes.Buffer
	renderEnvPlanText(&buf, envApplyPlan{}, true, 0)

	for _, bad := range []string{"\u2014", "\u2013"} {
		if strings.Contains(buf.String(), bad) {
			t.Errorf("plan text contains %q: %q", bad, buf.String())
		}
	}
}

// TestEnvPlanTextPaintsMarkers exercises the painted path, which stylerFor can
// never produce for a bytes.Buffer, and checks each marker gets the color its
// meaning calls for rather than all three sharing one.
func TestEnvPlanTextPaintsMarkers(t *testing.T) {
	var buf bytes.Buffer
	plan := envApplyPlan{
		Adds:    []envApplyOp{{Key: "API_URL"}},
		Updates: []envApplyOp{{Key: "LOG_LEVEL", Reason: "value differs"}},
		Deletes: []envApplyOp{{Key: "OLD_FLAG"}},
	}
	renderEnvPlanTextWith(&buf, styler{color: true}, plan, true, 0)

	for _, want := range []string{
		"  " + ansiGreen + "+" + ansiReset + " API_URL",
		"  " + ansiGreen + "~" + ansiReset + " LOG_LEVEL",
		"  " + ansiDim + "-" + ansiReset + " OLD_FLAG",
		ansiDim + "[value differs]" + ansiReset,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("painted plan missing %q:\n%q", want, buf.String())
		}
	}
}

// TestFleetProgressLinesArePlainOffATerminal pins the exact text of the two
// fleet progress lines. Both now compose their status word and elapsed counter
// through the styler, and the existing loop tests either discard the output or
// assert a substring, so without this nothing would notice the piped form
// drifting from what it printed before.
func TestFleetProgressLinesArePlainOffATerminal(t *testing.T) {
	var ready bytes.Buffer
	var calls int
	poll := func() (bool, string, error) {
		calls++
		return calls >= 3, "starting", nil
	}
	if err := waitForFleetHealthLoop("demo", 120*time.Second, time.Second, time.Second,
		poll, stepClock(time.Second), func(time.Duration) {}, &ready); err != nil {
		t.Fatalf("ready app must return nil, got %v", err)
	}
	if !strings.Contains(ready.String(), "  demo: healthy after 3s\n") {
		t.Errorf("ready line drifted:\n%q", ready.String())
	}
	if !strings.Contains(ready.String(), "  demo: still starting (1s/2m0s)\n") {
		t.Errorf("waiting line drifted:\n%q", ready.String())
	}
	if i := strings.IndexByte(ready.String(), 0x1b); i >= 0 {
		t.Errorf("piped progress carries an escape at %d: %q", i, ready.String())
	}
}

// TestFirstFireProgressLineIsPlainOffATerminal is the same guarantee for the
// first-fire wait.
func TestFirstFireProgressLineIsPlainOffATerminal(t *testing.T) {
	var out bytes.Buffer
	cur := time.Unix(0, 0)
	now := func() time.Time { return cur }
	sleep := func(d time.Duration) { cur = cur.Add(d) }
	poll := func() (string, error) { return "running", nil }

	_, _ = waitForFirstFireLoop(poll, 5*time.Second, time.Second, time.Second,
		now, sleep, &out, "warm")

	if !strings.Contains(out.String(), "  warm: first-fire still running (1s/5s)\n") {
		t.Errorf("first-fire progress line drifted:\n%q", out.String())
	}
	if i := strings.IndexByte(out.String(), 0x1b); i >= 0 {
		t.Errorf("piped progress carries an escape at %d: %q", i, out.String())
	}
}
