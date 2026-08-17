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

On a terminal this opens an interactive monitor. Move with the arrow or j/k
keys, press Enter to inspect an app's replicas, / to filter, Space to pause,
Tab to cycle the sort column, ? for all shortcuts, and q to leave. Any other
output form (a pipe, --output json, TERM=dumb) prints a single snapshot and
exits, so this is safe to use in a script.

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
		metricsErr := httpError(cfg.Token, "app metrics", resp, body)
		rows, fallbackErr := fetchTopRowsFromAppList(cfg)
		if fallbackErr == nil {
			return rows, nil
		}
		return nil, fmt.Errorf("%w; app-list fallback also failed: %v", metricsErr, fallbackErr)
	}
	var env struct {
		Metrics map[string]topMetrics `json:"metrics"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return topRowsFrom(env.Metrics), nil
}

// fetchTopRowsFromAppList degrades a metrics outage to an explicit partial
// snapshot. The list endpoint carries status plus desired/actual process counts
// and configured capacity, so a monitor can distinguish "zero running" from
// "metrics unavailable" without inventing CPU/RSS/session readings.
func fetchTopRowsFromAppList(cfg *cliConfig) ([]topRow, error) {
	req, err := http.NewRequest("GET", cfg.Host+"/api/apps?limit=10000", nil)
	if err != nil {
		return nil, fmt.Errorf("build app-list fallback request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, httpError(cfg.Token, "list apps", resp, body)
	}
	var env struct {
		Items []struct {
			Slug            string `json:"slug"`
			Status          string `json:"status"`
			Replicas        int    `json:"replicas"`
			ReplicasRunning int    `json:"replicas_running"`
			WorkersRunning  int    `json:"workers_running"`
			SessionsCeiling int    `json:"sessions_ceiling"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode app-list fallback: %w", err)
	}
	rows := make([]topRow, 0, len(env.Items))
	for _, app := range env.Items {
		rows = append(rows, topRow{
			Slug: app.Slug, Status: app.Status, Replicas: app.Replicas,
			Running: app.ReplicasRunning, Workers: app.WorkersRunning,
			Ceiling: app.SessionsCeiling, MetricsUnavailable: true,
		})
	}
	return rows, nil
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

type topKeyKind uint8

const (
	topKeyRune topKeyKind = iota
	topKeyUp
	topKeyDown
	topKeyPageUp
	topKeyPageDown
	topKeyHome
	topKeyEnd
	topKeyEscape
)

type topKey struct {
	kind topKeyKind
	b    byte
}

// topKeys decodes the small set of terminal escape sequences the monitor uses.
// Escape itself is emitted after a short ambiguity window because it is also
// the first byte of every arrow and page key.
func topKeys(in *os.File) <-chan topKey {
	raw := make(chan byte, 16)
	go func() {
		defer close(raw)
		buf := make([]byte, 1)
		for {
			n, err := in.Read(buf)
			if n == 1 {
				raw <- buf[0]
			}
			if err != nil {
				return
			}
		}
	}()

	ch := make(chan topKey, 16)
	go func() {
		defer close(ch)
		var seq string
		var timer *time.Timer
		var timerC <-chan time.Time
		stopTimer := func() {
			if timer != nil {
				timer.Stop()
			}
			timerC = nil
		}
		flushEscape := func() {
			ch <- topKey{kind: topKeyEscape}
			for i := 1; i < len(seq); i++ {
				ch <- topKey{kind: topKeyRune, b: seq[i]}
			}
			seq = ""
			stopTimer()
		}
		for {
			select {
			case b, ok := <-raw:
				if !ok {
					if seq != "" {
						flushEscape()
					}
					return
				}
				if seq == "" && b != 0x1b {
					ch <- topKey{kind: topKeyRune, b: b}
					continue
				}
				seq += string(b)
				if key, complete, prefix := decodeTopEscape(seq); complete {
					ch <- key
					seq = ""
					stopTimer()
				} else if !prefix {
					flushEscape()
				} else {
					stopTimer()
					timer = time.NewTimer(35 * time.Millisecond)
					timerC = timer.C
				}
			case <-timerC:
				flushEscape()
			}
		}
	}()
	return ch
}

func decodeTopEscape(seq string) (topKey, bool, bool) {
	known := map[string]topKeyKind{
		"\x1b[A": topKeyUp, "\x1b[B": topKeyDown,
		"\x1b[5~": topKeyPageUp, "\x1b[6~": topKeyPageDown,
		"\x1b[H": topKeyHome, "\x1b[1~": topKeyHome,
		"\x1b[F": topKeyEnd, "\x1b[4~": topKeyEnd,
	}
	if kind, ok := known[seq]; ok {
		return topKey{kind: kind}, true, true
	}
	if seq == "\x1b" {
		return topKey{}, false, true
	}
	for candidate := range known {
		if strings.HasPrefix(candidate, seq) {
			return topKey{}, false, true
		}
	}
	return topKey{}, false, false
}

type topLiveState struct {
	by           topSort
	reverse      bool
	selected     string
	filter       string
	filterBefore string
	filtering    bool
	paused       bool
	inspect      bool
	help         bool
}

func (s *topLiveState) visible(rows []topRow, f *topFlags) []topRow {
	return topFilteredRows(topWindowOf(rows, f.limit, f.offset), s.filter)
}

func (s *topLiveState) ensureSelection(rows []topRow, f *topFlags) {
	visible := s.visible(rows, f)
	if len(visible) == 0 {
		s.selected = ""
		return
	}
	if topSelectedIndex(visible, s.selected) < 0 || s.selected == "" {
		s.selected = visible[0].Slug
	}
}

func (s *topLiveState) move(rows []topRow, f *topFlags, delta int) {
	visible := s.visible(rows, f)
	if len(visible) == 0 {
		return
	}
	i := topSelectedIndex(visible, s.selected)
	if i < 0 {
		i = 0
	}
	i += delta
	if i < 0 {
		i = 0
	}
	if i >= len(visible) {
		i = len(visible) - 1
	}
	s.selected = visible[i].Slug
}

func (s *topLiveState) cycleSort() {
	for i, v := range topSortValues {
		if topSort(v) == s.by {
			s.by = topSort(topSortValues[(i+1)%len(topSortValues)])
			return
		}
	}
	s.by = topSortCPU
}

// handleKey mutates only local presentation state. No key in top changes an
// app; operational actions remain explicit shinyhub commands with their normal
// confirmation and error behavior.
func (s *topLiveState) handleKey(key topKey, rows []topRow, f *topFlags, page int) (quit bool) {
	if key.kind == topKeyRune && (key.b == keyCtrlC || key.b == keyCtrlD) {
		return true
	}
	if s.help {
		if key.kind == topKeyEscape || (key.kind == topKeyRune && key.b == '?') {
			s.help = false
		} else if key.kind == topKeyRune && (key.b == 'q' || key.b == 'Q') {
			return true
		}
		return false
	}
	if s.filtering {
		switch {
		case key.kind == topKeyEscape:
			s.filter, s.filtering = s.filterBefore, false
		case key.kind == topKeyRune && (key.b == '\r' || key.b == '\n'):
			s.filtering = false
		case key.kind == topKeyRune && (key.b == 8 || key.b == 127):
			if len(s.filter) > 0 {
				s.filter = s.filter[:len(s.filter)-1]
			}
		case key.kind == topKeyRune && key.b >= 32 && key.b < 127:
			s.filter += string(key.b)
		}
		s.ensureSelection(rows, f)
		return false
	}

	switch key.kind {
	case topKeyUp:
		s.move(rows, f, -1)
	case topKeyDown:
		s.move(rows, f, 1)
	case topKeyPageUp:
		s.move(rows, f, -page)
	case topKeyPageDown:
		s.move(rows, f, page)
	case topKeyHome:
		s.move(rows, f, -len(rows))
	case topKeyEnd:
		s.move(rows, f, len(rows))
	case topKeyEscape:
		if s.filter != "" {
			s.filter = ""
			s.ensureSelection(rows, f)
		} else {
			s.inspect = false
		}
	case topKeyRune:
		switch key.b {
		case 'q', 'Q':
			return true
		case 'k':
			s.move(rows, f, -1)
		case 'j':
			s.move(rows, f, 1)
		case 'g':
			s.move(rows, f, -len(rows))
		case 'G':
			s.move(rows, f, len(rows))
		case '\r', '\n':
			s.inspect = !s.inspect
		case '/':
			s.filterBefore, s.filtering = s.filter, true
		case ' ':
			s.paused = !s.paused
		case '\t':
			s.cycleSort()
		case 'c':
			s.by = topSortCPU
		case 'm':
			s.by = topSortMemory
		case 's':
			s.by = topSortSessions
		case 'n':
			s.by = topSortName
		case 'r':
			s.reverse = !s.reverse
		case '?':
			s.help = true
		}
	}
	return false
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

	var keys <-chan topKey
	if raw {
		keys = topKeys(os.Stdin)
	}

	s := stylerFor(out)
	lastErr := ""
	shown := first
	state := topLiveState{by: by}
	state.ensureSelection(shown.rows, f)

	paint := func() {
		width, height, err := term.GetSize(int(out.Fd()))
		if err != nil {
			width, height = 0, 0
		}
		tt.paint(s, topView{
			Host:      cfg.Host,
			At:        shown.at,
			Rows:      shown.rows,
			Limit:     f.limit,
			Offset:    f.offset,
			Sort:      state.by,
			Reverse:   state.reverse,
			Interval:  f.interval,
			Live:      true,
			Keys:      raw,
			Width:     width,
			Height:    height,
			Err:       lastErr,
			Age:       time.Since(shown.at),
			Selected:  state.selected,
			Filter:    state.filter,
			Filtering: state.filtering,
			Paused:    state.paused,
			Inspect:   state.inspect,
			Help:      state.help,
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

		case key, ok := <-keys:
			if !ok {
				// stdin closed: keep watching, just without shortcuts.
				keys, raw = nil, false
				paint()
				continue
			}
			wasPaused := state.paused
			page := 10
			if _, height, err := term.GetSize(int(out.Fd())); err == nil && height > 12 {
				page = height - 12
			}
			if state.handleKey(key, shown.rows, f, page) {
				return nil
			}
			sortTopRows(shown.rows, state.by, state.reverse)
			state.ensureSelection(shown.rows, f)
			// Resuming should not make the operator wait for the next ticker edge.
			if wasPaused && !state.paused && !inFlight {
				inFlight = true
				go func() { results <- pollTopRows(cfg) }()
			}
			paint()

		case <-tick.C:
			// A server slower than the interval simply refreshes less often;
			// stacking polls on it would make a struggling server worse.
			if !state.paused && !inFlight {
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
			} else if !state.paused {
				shown, lastErr = p, ""
			}
			sortTopRows(shown.rows, state.by, state.reverse)
			state.ensureSelection(shown.rows, f)
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
