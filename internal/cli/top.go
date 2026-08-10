package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// topMinInterval floors the refresh rate. Each poll makes the server sample
// every replica of every visible app, and the sampler's own process-group scan
// is only refreshed once a second, so a faster loop costs the server more
// without showing the operator anything new.
const topMinInterval = time.Second

// Terminal control the live view uses. The alternate screen keeps the frame out
// of the scrollback, so quitting leaves the shell exactly as it was found.
const (
	altScreenOn  = "\x1b[?1049h"
	altScreenOff = "\x1b[?1049l"
	cursorHide   = "\x1b[?25l"
	cursorShow   = "\x1b[?25h"
	cursorHome   = "\x1b[H"
	eraseBelow   = "\x1b[J"
)

// Keys the live view answers to. Escape is deliberately absent: an arrow key
// arrives as an escape sequence, and quitting on it would make the view
// impossible to keep open.
const (
	keyCtrlC = 3
	keyCtrlD = 4
)

type topFlags struct {
	listFlags
	interval time.Duration
	sortBy   string
}

func newTopCmd() *cobra.Command {
	f := &topFlags{}
	cmd := &cobra.Command{
		Use:   "top",
		Short: "Live CPU, memory and session usage for every app",
		Long: `Watch what the whole server is doing, one line per app.

On a terminal this repaints in place until you quit: press c, m, s or n to sort
by CPU, memory, sessions or name, r to reverse the order, and q to leave. Any
other output form (a pipe, --output json, TERM=dumb) prints a single snapshot
and exits, so this is safe to use in a script.

Figures come from the same live sample the dashboard shows and cover each app's
whole process group. A figure the server could not measure prints as "-" rather
than as zero, and a total missing a running replica's contribution is marked as
a lower bound.

--fields applies to JSON output; the live table has a fixed layout.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTop(cmd, f)
		},
	}
	addListFlags(cmd, &f.listFlags)
	cmd.Flags().DurationVar(&f.interval, "interval", 2*time.Second,
		"How often the live view refreshes (minimum 1s)")
	cmd.Flags().StringVar(&f.sortBy, "sort", string(topSortCPU),
		"Column to sort by: "+strings.Join(topSortValues, ", "))
	return cmd
}

func runTop(cmd *cobra.Command, f *topFlags) error {
	format, err := resolveFormat(f.jsonOutput, false)
	if err != nil {
		return err
	}
	by, err := parseTopSort(f.sortBy)
	if err != nil {
		return err
	}
	if f.interval < topMinInterval {
		return validationErr(
			fmt.Sprintf("--interval %s is below the %s minimum", f.interval, topMinInterval),
			"each refresh samples every replica on the server; a faster loop costs more than it shows")
	}
	// The window is checked before the first request, not inside a frame: the
	// live view redraws forever, and a value it silently ignored would be a flag
	// that appears to work and never does.
	if err := validateWindow(&f.listFlags); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	first := pollTopRows(cfg)
	if first.err != nil {
		return first.err
	}
	sortTopRows(first.rows, by, false)

	out := cmd.OutOrStdout()
	// redraw is the honest test for the live form: it requires a real terminal
	// that can interpret cursor control, which rules out a pipe, a file and
	// TERM=dumb in one condition.
	if outFile, ok := out.(*os.File); ok && format == formatTable && stylerFor(out).redraw {
		return runTopLive(cmd, outFile, cfg, f, by, first)
	}
	return renderTopOnce(cmd, f, format, cfg.Host, first, by)
}

// renderTopOnce prints a single snapshot: the JSON document a script consumes,
// or one table for a pipe or a terminal that cannot repaint.
func renderTopOnce(cmd *cobra.Command, f *topFlags, format outputFormat,
	host string, p topPoll, by topSort) error {
	extra := map[string]any{
		"host":        host,
		"captured_at": p.at.UTC().Format(time.RFC3339),
		"totals":      topTotalsMap(topTotalsFor(p.rows)),
	}
	return renderListTo(cmd.OutOrStdout(), cmd.ErrOrStderr(), format, &f.listFlags,
		topItems(p.rows), extra, func(w io.Writer, _ []map[string]any) {
			// The table is rendered from the typed rows, not from the projected
			// maps: --fields shapes the JSON document, and applying it here would
			// blank columns of a fixed layout rather than narrow it.
			renderTop(w, stylerFor(w), topView{
				Host:   host,
				At:     p.at,
				Rows:   p.rows,
				Limit:  f.limit,
				Offset: f.offset,
				Sort:   by,
			})
		})
}

// topWindowOf applies --limit/--offset to the rows themselves, so the live view
// and the piped snapshot show the same window of the same order. runTop has
// already rejected a window that cannot be satisfied; the clamping here is what
// keeps a window past the end of a shrinking fleet from panicking mid-frame.
func topWindowOf(rows []topRow, limit, offset int) []topRow {
	start := offset
	if start < 0 {
		start = 0
	}
	if start > len(rows) {
		start = len(rows)
	}
	end := len(rows)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return rows[start:end]
}

// topPoll is one refresh: what it read, when it read it, and why it failed.
//
// The capture time travels with the rows rather than being stamped at paint
// time, so a frame drawn from a sample taken thirty seconds ago says so instead
// of wearing the current clock.
type topPoll struct {
	rows []topRow
	at   time.Time
	err  error
}

// pollTopRows is fetchTopRows stamped with the moment its answer arrived.
func pollTopRows(cfg *cliConfig) topPoll {
	rows, err := fetchTopRows(cfg)
	return topPoll{rows: rows, at: time.Now(), err: err}
}

// fetchTopRows reads every app the caller may see in one request. The dashboard
// poll uses the same endpoint, so the live view costs the server no more than a
// browser tab left open on it.
func fetchTopRows(cfg *cliConfig) ([]topRow, error) {
	req, err := http.NewRequest("GET", cfg.Host+"/api/apps/metrics", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, httpError(cfg.Token, "app metrics", resp, body)
	}
	var env struct {
		Metrics map[string]topMetrics `json:"metrics"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return topRowsFrom(env.Metrics), nil
}

// topTerminal owns every terminal mutation the live view makes, so undoing them
// is one call that any exit path can make and that is safe to repeat.
type topTerminal struct {
	out      *os.File
	in       *os.File
	oldState *term.State
	restored bool
}

// enter switches to the alternate screen and, when stdin is a terminal, puts it
// in raw mode so single keystrokes arrive without a newline.
//
// Raw mode is best-effort. With stdin redirected there are no keystrokes to
// read, which costs the shortcuts but not the view, so the frame still paints
// and Ctrl-C still reaches the process as a signal.
func (t *topTerminal) enter() bool {
	raw := false
	if t.in != nil && term.IsTerminal(int(t.in.Fd())) {
		if st, err := term.MakeRaw(int(t.in.Fd())); err == nil {
			t.oldState, raw = st, true
		}
	}
	fmt.Fprint(t.out, altScreenOn+cursorHide)
	return raw
}

// restore undoes enter. It runs termios first: if writing to the terminal fails
// the shell is at least left able to echo again.
func (t *topTerminal) restore() {
	if t.restored {
		return
	}
	t.restored = true
	if t.oldState != nil {
		_ = term.Restore(int(t.in.Fd()), t.oldState)
	}
	fmt.Fprint(t.out, cursorShow+altScreenOff)
}

// paint writes one frame in place. Each line is erased to its end before the
// next begins and the region below the frame is cleared afterwards, so a frame
// that shrinks leaves nothing of the one before it without the whole-screen
// clear that would flicker.
//
// The carriage returns are not decoration: raw mode turns off output
// translation, so a bare newline moves down a line without returning to
// column one.
func (t *topTerminal) paint(s styler, v topView) {
	var buf strings.Builder
	renderTop(&buf, s, v)
	frame := strings.ReplaceAll(buf.String(), "\n", "\x1b[K\r\n")
	fmt.Fprint(t.out, cursorHome+frame+eraseBelow)
}

// topKeys reads stdin one byte at a time. The channel closes when stdin does,
// which is how a live view survives having its input taken away rather than
// spinning on a dead reader.
func topKeys(in *os.File) <-chan byte {
	ch := make(chan byte, 8)
	go func() {
		defer close(ch)
		buf := make([]byte, 1)
		for {
			n, err := in.Read(buf)
			if n == 1 {
				ch <- buf[0]
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}

// runTopLive drives the repainting view until the user quits, the process is
// signalled, or the server answers with something retrying cannot fix.
func runTopLive(cmd *cobra.Command, out *os.File, cfg *cliConfig, f *topFlags,
	by topSort, first topPoll) error {
	tt := &topTerminal{out: out, in: os.Stdin}
	raw := tt.enter()
	defer tt.restore()

	// A signal must leave the terminal usable, so restore runs before the
	// process is allowed to die. Raw mode suppresses the SIGINT that Ctrl-C
	// would normally raise; this covers the other senders (SIGTERM, and Ctrl-C
	// itself when raw mode could not be entered).
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var keys <-chan byte
	if raw {
		keys = topKeys(os.Stdin)
	}

	s := stylerFor(out)
	reverse := false
	lastErr := ""
	shown := first

	paint := func() {
		width, height, err := term.GetSize(int(out.Fd()))
		if err != nil {
			width, height = 0, 0
		}
		tt.paint(s, topView{
			Host:     cfg.Host,
			At:       shown.at,
			Rows:     shown.rows,
			Limit:    f.limit,
			Offset:   f.offset,
			Sort:     by,
			Reverse:  reverse,
			Interval: f.interval,
			Live:     true,
			Keys:     raw,
			Width:    width,
			Height:   height,
			Err:      lastErr,
			Age:      time.Since(shown.at),
		})
	}
	paint()

	// Refreshes run off the event loop. A poll can take as long as the HTTP
	// client's timeout allows, and a loop that waited on it inline would stop
	// repainting and stop answering keys - q included - for as long as an
	// unresponsive server took to fail. Delivered as a message instead, a slow
	// server costs freshness and nothing else.
	//
	// The buffer is what lets quitting be immediate: only one poll is ever in
	// flight, so its send always completes and the goroutine finishes on its own
	// even though nothing is left to receive it.
	results := make(chan topPoll, 1)
	inFlight := false

	tick := time.NewTicker(f.interval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case b, ok := <-keys:
			if !ok {
				// stdin closed: keep watching, just without shortcuts.
				keys, raw = nil, false
				paint()
				continue
			}
			switch b {
			case 'q', 'Q', keyCtrlC, keyCtrlD:
				return nil
			case 'c':
				by = topSortCPU
			case 'm':
				by = topSortMemory
			case 's':
				by = topSortSessions
			case 'n':
				by = topSortName
			case 'r':
				reverse = !reverse
			default:
				continue
			}
			sortTopRows(shown.rows, by, reverse)
			paint()

		case <-tick.C:
			// A server slower than the interval simply refreshes less often;
			// stacking polls on it would make a struggling server worse.
			if !inFlight {
				inFlight = true
				go func() { results <- pollTopRows(cfg) }()
			}
			// Repaint regardless, so the age beside a failure keeps counting up.
			paint()

		case p := <-results:
			inFlight = false
			if p.err != nil {
				if topFatal(p.err) {
					return p.err
				}
				lastErr = "last refresh failed: " + p.err.Error()
			} else {
				shown, lastErr = p, ""
			}
			sortTopRows(shown.rows, by, reverse)
			paint()
		}
	}
}

// topFatal reports whether a failed refresh is worth quitting over.
//
// A refused connection or a 502 is a server that may well be back before the
// next tick, and a monitor that exits the moment one arrives is worse than no
// monitor; those keep the last good frame on screen with the failure shown
// under it. A 401 or a 403 will answer identically forever, so retrying one
// silently would leave a frozen screen and no explanation of why it stopped
// moving.
func topFatal(err error) bool {
	var status *httpStatusError
	if !errors.As(err, &status) {
		return false
	}
	if status.Status < 400 || status.Status >= 500 {
		return false
	}
	switch status.Status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return false
	}
	return true
}
