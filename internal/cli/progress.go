package cli

import (
	"fmt"
	"io"
	"time"
)

// spinnerRedraw is how often the animated form repaints. Fast enough to read as
// motion, slow enough to cost nothing.
const spinnerRedraw = 100 * time.Millisecond

// eraseLine returns the cursor to the start of the line and clears it. Only
// ever written to a writer whose styler reports redraw, so a terminal that
// cannot interpret it never receives it.
const eraseLine = "\r\x1b[K"

var (
	spinnerFramesUTF8  = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	spinnerFramesASCII = []string{"|", "/", "-", "\\"}
)

// progress reports a wait whose length is not known in advance. On a terminal
// it is a single self-rewriting line carrying a spinner and the elapsed time,
// so a slow deploy is visibly alive and its cost is legible while it happens.
// Anywhere else - a pipe, a CI log, a dumb terminal - it degrades to exactly
// the line-per-step output this CLI printed before there was a spinner, so
// nothing that reads that output has to change.
type progress struct {
	w       io.Writer
	s       styler
	label   string
	started time.Time
	frame   int
	stopCh  chan struct{}
	doneCh  chan struct{}
}

// newProgress starts a wait labelled label. On a non-redrawing writer the label
// is printed immediately with no newline, and step() extends it with dots, which
// is the pre-spinner behavior verbatim.
func newProgress(w io.Writer, label string) *progress {
	p := &progress{w: w, s: stylerFor(w), label: label, started: time.Now()}
	if !p.s.redraw {
		fmt.Fprint(w, label)
	}
	return p
}

func (p *progress) frames() []string {
	if p.s.ascii {
		return spinnerFramesASCII
	}
	return spinnerFramesUTF8
}

// draw repaints the animated line in place.
func (p *progress) draw() {
	f := p.frames()
	fmt.Fprintf(p.w, "%s%s %s %s", eraseLine,
		p.s.yellow(f[p.frame%len(f)]), p.label, p.s.dim(humanElapsed(time.Since(p.started))))
	p.frame++
}

// step marks one unit of work done and waits d before the next one, keeping the
// spinner turning meanwhile.
func (p *progress) step(d time.Duration) {
	if !p.s.redraw {
		fmt.Fprint(p.w, ".")
		time.Sleep(d)
		return
	}
	for waited := time.Duration(0); waited < d; waited += spinnerRedraw {
		p.draw()
		if rest := d - waited; rest < spinnerRedraw {
			time.Sleep(rest)
		} else {
			time.Sleep(spinnerRedraw)
		}
	}
}

// start animates in the background, for a wait the caller cannot break into
// steps - a single blocking request. Every exit path must reach done or stop,
// which join the goroutine before returning.
func (p *progress) start() {
	if !p.s.redraw || p.stopCh != nil {
		return
	}
	p.stopCh, p.doneCh = make(chan struct{}), make(chan struct{})
	stop, done := p.stopCh, p.doneCh
	go func() {
		defer close(done)
		t := time.NewTicker(spinnerRedraw)
		defer t.Stop()
		p.draw()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				p.draw()
			}
		}
	}()
}

// halt stops background animation, if any, and waits for the last repaint to
// finish so nothing writes to w after this returns.
func (p *progress) halt() {
	if p.stopCh == nil {
		return
	}
	close(p.stopCh)
	<-p.doneCh
	p.stopCh, p.doneCh = nil, nil
}

// done ends the wait successfully. suffix completes the line on a non-redrawing
// writer (it is appended to the label already printed there); settled is the
// whole sentence the animated form collapses to, which carries the elapsed time.
func (p *progress) done(suffix, settled string) {
	p.halt()
	if !p.s.redraw {
		fmt.Fprintln(p.w, suffix)
		return
	}
	fmt.Fprintf(p.w, "%s%s%s %s\n", eraseLine, p.s.okPrefix(), settled,
		p.s.dim("("+humanElapsed(time.Since(p.started))+")"))
}

// note interrupts the wait with a full line of its own (a warning the operator
// should see before the wait ends) and lets the progress line resume beneath
// it. On a non-redrawing writer the label is reprinted so the dots that follow
// still read as belonging to the wait.
func (p *progress) note(msg string) {
	p.halt()
	if !p.s.redraw {
		fmt.Fprintf(p.w, "\n%s\n%s", msg, p.label)
		return
	}
	fmt.Fprintf(p.w, "%s%s\n", eraseLine, msg)
}

// stop ends the wait without a verdict: the caller is about to print its own
// failure or its own success line, so this only closes off the progress line.
func (p *progress) stop() {
	p.halt()
	if !p.s.redraw {
		fmt.Fprintln(p.w)
		return
	}
	fmt.Fprint(p.w, eraseLine)
}

// elapsed reports how long this wait has been running.
func (p *progress) elapsed() time.Duration { return time.Since(p.started) }

// humanElapsed renders a duration the way a person reads a stopwatch: whole
// seconds under a minute, then m/s. Sub-second waits still read as "0s" rather
// than as a burst of digits nobody needed.
func humanElapsed(d time.Duration) string {
	secs := int(d.Round(time.Second).Seconds())
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	return fmt.Sprintf("%dm%02ds", secs/60, secs%60)
}
