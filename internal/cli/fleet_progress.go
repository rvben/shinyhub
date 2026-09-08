package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rivo/uniseg"
	"github.com/rvben/shinyhub/internal/fleet"
	"golang.org/x/term"
)

// A single writer owns the entire live region, including ordinary diagnostics.
// Workers publish semantic state; they never move the terminal cursor.
type fleetLiveDisplay struct {
	mu                          sync.Mutex
	out                         io.Writer
	style                       styler
	rows                        []fleetProgressRow
	started                     time.Time
	width, height, drawn, frame int
	size                        func() (int, int, error)
	live                        bool
	stop, stopped               chan struct{}
}

type fleetProgressRow struct {
	slug, phase, detail         string
	started, finished, deadline time.Time
	status                      applyStatus
	warning                     bool
}

type fleetAppProgress struct {
	display *fleetLiveDisplay
	index   int
}

func newFleetLiveDisplay(out io.Writer, diff []fleet.AppDiff) *fleetLiveDisplay {
	s := stylerFor(out)
	if !s.redraw || len(diff) == 0 {
		return nil
	}
	f, ok := out.(*os.File)
	if !ok {
		return nil
	}
	size := func() (int, int, error) { return term.GetSize(int(f.Fd())) }
	width, height, err := size()
	// Leave room for the cursor below the region; never scroll a live row away.
	if err != nil || width < 50 || 2*len(diff)+3 >= height {
		return nil
	}
	d := &fleetLiveDisplay{out: out, style: s, started: time.Now(), width: width, height: height, size: size, live: true}
	for _, app := range diff {
		d.rows = append(d.rows, fleetProgressRow{slug: app.Slug, phase: "Queued"})
	}
	d.start()
	return d
}

func (d *fleetLiveDisplay) start() {
	d.stop, d.stopped = make(chan struct{}), make(chan struct{})
	d.mu.Lock()
	d.drawLocked(time.Now())
	d.mu.Unlock()
	go func() {
		defer close(d.stopped)
		ticker := time.NewTicker(spinnerRedraw)
		defer ticker.Stop()
		for {
			select {
			case <-d.stop:
				return
			case <-ticker.C:
				d.mu.Lock()
				d.drawLocked(time.Now())
				d.mu.Unlock()
			}
		}
	}()
}

func (d *fleetLiveDisplay) close() {
	close(d.stop)
	<-d.stopped
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.live {
		d.drawLocked(time.Now())
	}
	d.live = false
}

func (d *fleetLiveDisplay) clearLocked() {
	if d.drawn == 0 {
		return
	}
	// Each frame ends on the line below the region.
	fmt.Fprintf(d.out, "\x1b[%dA\r\x1b[J", d.drawn)
	d.drawn = 0
}

// Check before any cursor movement, including writes arriving between ticks.
func (d *fleetLiveDisplay) checkSizeLocked() bool {
	if !d.live {
		return false
	}
	if d.size != nil {
		width, height, err := d.size()
		if err != nil || width != d.width || height != d.height {
			// Resizing may reflow old rows. Cursor-up could erase durable diagnostics.
			fmt.Fprintln(d.out, "Fleet progress: terminal resized; continuing with event updates.")
			d.live, d.drawn = false, 0
			return false
		}
	}
	return true
}

func (d *fleetLiveDisplay) drawLocked(now time.Time) {
	if !d.checkSizeLocked() {
		return
	}
	prefix := ""
	if d.drawn > 0 {
		prefix = fmt.Sprintf("\x1b[%dA\r\x1b[J", d.drawn)
	}
	lines := d.lines(now)
	// Erase and repaint in one write to avoid a visible blank frame.
	fmt.Fprint(d.out, prefix+strings.Join(lines, "\n")+"\n")
	d.drawn = len(lines)
	d.frame++
}

func (d *fleetLiveDisplay) lines(now time.Time) []string {
	s := d.style
	completed, failed := 0, 0
	for _, row := range d.rows {
		if !row.finished.IsZero() {
			completed++
		}
		if row.status == statusFailed || row.status == statusConflict {
			failed++
		}
	}
	title := fmt.Sprintf("Applying fleet   %d of %d complete", completed, len(d.rows))
	if completed == len(d.rows) {
		title = fmt.Sprintf("Fleet finished   %d apps", len(d.rows))
	}
	if failed > 0 {
		title += s.red(fmt.Sprintf(" · %d failed", failed))
	}
	title += s.dim(" · " + humanElapsed(now.Sub(d.started)))
	if s.ascii {
		title = strings.ReplaceAll(title, "·", "|")
	}
	lines := []string{title, ""}
	nameWidth := 0
	for _, row := range d.rows {
		if len(row.slug) > nameWidth {
			nameWidth = len(row.slug)
		}
	}
	if nameWidth > d.width/3 {
		nameWidth = d.width / 3
	}
	for _, row := range d.rows {
		mark := s.dim("-")
		phase := row.phase
		elapsed := ""
		if !row.started.IsZero() {
			end := now
			if !row.finished.IsZero() {
				end = row.finished
			}
			elapsed = humanElapsed(end.Sub(row.started))
			frames := spinnerFramesUTF8
			if s.ascii {
				frames = spinnerFramesASCII
			}
			mark = s.yellow(frames[d.frame%len(frames)])
		}
		if !row.finished.IsZero() {
			switch row.status {
			case statusFailed, statusConflict:
				mark, phase = s.red(s.glyphFail()), s.red(phase)
			case statusSkipped:
				mark, phase = s.dim("-"), s.dim(phase)
			default:
				mark = s.green(s.glyphOK())
			}
		} else if row.warning {
			phase = s.yellow(phase)
		}
		name := fleetClip(liveText(row.slug), nameWidth, s.glyphEllipsis())
		name += strings.Repeat(" ", max(0, nameWidth-fleetVisibleWidth(name)))
		line := "  " + mark + " " + name + "  " + phase
		room := min(100, d.width-1)
		if elapsed != "" {
			line = fleetClip(line, room-len(elapsed)-2, s.glyphEllipsis())
			line += strings.Repeat(" ", max(2, room-fleetVisibleWidth(line)-len(elapsed))) + s.dim(elapsed)
		}
		detail := liveText(row.detail)
		if !row.deadline.IsZero() && row.finished.IsZero() {
			timeout := "timeout in " + humanElapsed(max(time.Duration(0), row.deadline.Sub(now)))
			if detail != "" {
				detail = fleetClip(detail, room-4-3-fleetVisibleWidth(timeout), s.glyphEllipsis()) + " · "
			}
			detail += timeout
		}
		if s.ascii {
			detail = strings.ReplaceAll(detail, "·", "|")
		}
		lines = append(lines, line, "    "+s.dim(detail))
	}
	for i := range lines {
		lines[i] = fleetClip(lines[i], d.width-1, s.glyphEllipsis())
	}
	return lines
}

// Dynamic labels are single-line text, never terminal control sequences.
func liveText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
}

func (d *fleetLiveDisplay) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.checkSizeLocked() {
		return d.out.Write(p)
	}
	d.clearLocked()
	n, err := d.out.Write(p)
	if len(p) > 0 && p[len(p)-1] != '\n' {
		fmt.Fprintln(d.out)
	}
	d.drawLocked(time.Now())
	return n, err
}

func fleetDisplayFor(out io.Writer) *fleetLiveDisplay {
	switch w := out.(type) {
	case *fleetLiveDisplay:
		return w
	case *syncWriter:
		return fleetDisplayFor(w.w)
	default:
		return nil
	}
}

func beginFleetApp(out io.Writer, slug string) (io.Writer, func(applyResult)) {
	d := fleetDisplayFor(out)
	if d == nil {
		return out, func(applyResult) {}
	}
	d.mu.Lock()
	index := -1
	for i := range d.rows {
		if d.rows[i].slug == slug {
			index = i
			d.rows[i].started, d.rows[i].phase = time.Now(), "Applying changes"
			break
		}
	}
	d.mu.Unlock()
	if index < 0 {
		return out, func(applyResult) {}
	}
	w := &fleetAppProgress{display: d, index: index}
	return w, func(result applyResult) {
		d.mu.Lock()
		defer d.mu.Unlock()
		row := &d.rows[index]
		row.finished, row.status, row.deadline = time.Now(), result.status, time.Time{}
		previousPhase := row.phase
		row.phase = string(result.status)
		if row.phase != "" {
			row.phase = strings.ToUpper(row.phase[:1]) + row.phase[1:]
		}
		if result.err == nil && result.status != statusSkipped && result.status != statusDeleted {
			for _, refresh := range result.scheduleRefreshes {
				if refresh.RunID > 0 && refresh.Status == "succeeded" {
					row.phase = "Refreshed"
				}
			}
			switch previousPhase {
			case "Healthy":
				row.phase = "Ready"
			case "Stopped", "Parked":
				row.phase = previousPhase
			}
		}
		if result.note != "" {
			row.detail = result.note
		}
		if result.err != nil {
			row.detail = result.err.Error()
		}
	}
}

func (w *fleetAppProgress) Write(p []byte) (int, error) { return w.display.Write(p) }
func (w *fleetAppProgress) update(phase, detail string, deadline time.Time, warning bool) bool {
	d := w.display
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.checkSizeLocked() {
		return false
	}
	row := &d.rows[w.index]
	row.phase, row.detail, row.deadline, row.warning = liveText(phase), detail, deadline, warning
	return true
}

// Returning false asks the caller to emit its normal durable progress event.
func updateFleetProgress(out io.Writer, phase, detail string, deadline time.Time, warning bool) bool {
	switch w := out.(type) {
	case *fleetAppProgress:
		return w.update(phase, detail, deadline, warning)
	case *resultWarningWriter:
		return updateFleetProgress(w.Writer, phase, detail, deadline, warning)
	default:
		return false
	}
}

// Measure grapheme clusters, including emoji presentation selectors and joined
// sequences. Code-point widths can underestimate a row and break cursor tracking.
func fleetVisibleWidth(text string) int {
	var plain strings.Builder
	escape := false
	for _, r := range text {
		if escape {
			if isANSIFinal(r) {
				escape = false
			}
			continue
		}
		if r == '\x1b' {
			escape = true
			continue
		}
		plain.WriteRune(r)
	}
	return uniseg.StringWidth(plain.String())
}

func fleetClip(text string, cells int, ellipsis string) string {
	if cells <= 0 {
		return ""
	}
	if fleetVisibleWidth(text) <= cells {
		return text
	}
	limit := max(0, cells-uniseg.StringWidth(ellipsis))
	var out strings.Builder
	used, painted := 0, strings.Contains(text, "\x1b")
	for len(text) > 0 {
		if text[0] == '\x1b' {
			end := 1
			for end < len(text) && !isANSIFinal(rune(text[end])) {
				end++
			}
			if end < len(text) {
				end++
			}
			out.WriteString(text[:end])
			text = text[end:]
			continue
		}
		end := strings.IndexByte(text, '\x1b')
		if end < 0 {
			end = len(text)
		}
		graphemes := uniseg.NewGraphemes(text[:end])
		for graphemes.Next() {
			if used+graphemes.Width() > limit {
				out.WriteString(ellipsis)
				if painted {
					out.WriteString(ansiReset)
				}
				return out.String()
			}
			used += graphemes.Width()
			out.WriteString(graphemes.Str())
		}
		text = text[end:]
	}
	return out.String()
}
