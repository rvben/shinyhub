package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestHumanElapsed(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{400 * time.Millisecond, "0s"},
		{12 * time.Second, "12s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m00s"},
		{64 * time.Second, "1m04s"},
		{11*time.Minute + 5*time.Second, "11m05s"},
	}
	for _, tc := range cases {
		if got := humanElapsed(tc.in); got != tc.want {
			t.Errorf("humanElapsed(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestProgressNonRedrawIsThePreSpinnerByteSequence is the compatibility gate for
// the spinner: against anything that is not a redrawing terminal - a pipe, a CI
// log, a file - the wait must emit exactly the label-dots-verdict bytes this CLI
// printed before there was a spinner, with no cursor control and no color.
func TestProgressNonRedrawIsThePreSpinnerByteSequence(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, "Waiting for demo to be healthy")
	p.step(0)
	p.step(0)
	p.done(" ready.", "demo is healthy")

	want := "Waiting for demo to be healthy.. ready.\n"
	if got := buf.String(); got != want {
		t.Errorf("piped progress = %q, want %q", got, want)
	}
}

// TestProgressNonRedrawStopClosesTheLine covers the failure path: the caller is
// about to print its own error, so the wait only terminates the line it opened.
func TestProgressNonRedrawStopClosesTheLine(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, "Deploying demo")
	p.step(0)
	p.stop()

	want := "Deploying demo.\n"
	if got := buf.String(); got != want {
		t.Errorf("piped progress = %q, want %q", got, want)
	}
}

// redrawProgress builds a progress bound to a redrawing terminal. stylerFor
// deliberately refuses to style anything that is not one, so the animated path
// is only reachable from a test with an explicit styler.
func redrawProgress(w *bytes.Buffer, label string) *progress {
	return &progress{
		w:       w,
		s:       styler{color: true, tty: true, redraw: true},
		label:   label,
		started: time.Now(),
	}
}

// TestProgressRedrawRewritesOneLine pins the animated form: every repaint is
// preceded by an erase so the wait occupies a single line, and the line carries
// the label plus the running elapsed time.
func TestProgressRedrawRewritesOneLine(t *testing.T) {
	var buf bytes.Buffer
	p := redrawProgress(&buf, "Deploying demo")
	p.draw()
	p.draw()

	got := buf.String()
	if n := strings.Count(got, eraseLine); n != 2 {
		t.Errorf("%d erase sequences for 2 repaints, wanted 2: %q", n, got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("animated repaint emitted a newline, so it is not rewriting one line: %q", got)
	}
	if !strings.Contains(got, "Deploying demo") || !strings.Contains(got, "0s") {
		t.Errorf("repaint = %q, want the label and the elapsed time", got)
	}
	// Successive frames must differ, or the "spinner" is a static character.
	if p.frame != 2 {
		t.Errorf("frame counter = %d after 2 draws, want 2", p.frame)
	}
	if strings.Count(got, spinnerFramesUTF8[0]) != 1 || strings.Count(got, spinnerFramesUTF8[1]) != 1 {
		t.Errorf("repaints did not advance the spinner frame: %q", got)
	}
}

// TestProgressRedrawDoneCollapsesToAVerdict covers what the operator is left
// with once the wait ends: the animation is erased and replaced by one settled
// sentence carrying the elapsed cost.
func TestProgressRedrawDoneCollapsesToAVerdict(t *testing.T) {
	var buf bytes.Buffer
	p := redrawProgress(&buf, "Deploying demo")
	p.draw()
	p.done(" ready.", "demo is healthy")

	got := buf.String()
	line := got[strings.LastIndex(got, eraseLine)+len(eraseLine):]
	if !strings.HasSuffix(line, "\n") {
		t.Fatalf("settled line is not terminated: %q", line)
	}
	plain := stripANSI(strings.TrimRight(line, "\n"))
	if plain != "✓ demo is healthy (0s)" {
		t.Errorf("settled line = %q, want %q", plain, "✓ demo is healthy (0s)")
	}
	// The non-redraw suffix belongs to the other form and must not leak here.
	if strings.Contains(got, "ready.") {
		t.Errorf("animated form printed the piped suffix: %q", got)
	}
}

// TestProgressBackgroundAnimationJoins guards the goroutine contract: halt must
// wait for the last repaint, so nothing writes to the writer after the caller
// has moved on to printing its own output.
func TestProgressBackgroundAnimationJoins(t *testing.T) {
	var buf bytes.Buffer
	p := redrawProgress(&buf, "Deploying demo")
	p.start()
	time.Sleep(2 * spinnerRedraw)
	p.stop()

	settled := buf.Len()
	time.Sleep(2 * spinnerRedraw)
	if buf.Len() != settled {
		t.Errorf("writer grew from %d to %d bytes after stop: the animation goroutine outlived it",
			settled, buf.Len())
	}
	if !strings.HasSuffix(buf.String(), eraseLine) {
		t.Error("stop did not erase the animated line")
	}
	if p.stopCh != nil {
		t.Error("stop left the goroutine channels in place")
	}
}

// TestProgressStartIsInertOffATerminal is why the non-redraw byte sequence above
// holds even for a command that animates a single blocking request: start must
// spawn nothing at all when the writer cannot redraw.
func TestProgressStartIsInertOffATerminal(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, "Deploying demo")
	p.start()
	if p.stopCh != nil {
		t.Fatal("start spawned an animation goroutine for a non-redrawing writer")
	}
	p.stop()
	if got, want := buf.String(), "Deploying demo\n"; got != want {
		t.Errorf("piped progress = %q, want %q", got, want)
	}
}

func TestProgressASCIIFrames(t *testing.T) {
	var buf bytes.Buffer
	p := redrawProgress(&buf, "Deploying demo")
	p.s.ascii = true
	p.draw()

	if !strings.Contains(buf.String(), spinnerFramesASCII[0]) {
		t.Errorf("ascii repaint = %q, want an ASCII frame", buf.String())
	}
	for _, f := range spinnerFramesUTF8 {
		if strings.Contains(buf.String(), f) {
			t.Errorf("ascii repaint carries the Unicode frame %q: %q", f, buf.String())
		}
	}
}
