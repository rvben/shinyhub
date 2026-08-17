package cli

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// topSort names the column a top view is ordered by.
type topSort string

const (
	topSortCPU      topSort = "cpu"
	topSortMemory   topSort = "mem"
	topSortSessions topSort = "sessions"
	topSortName     topSort = "name"
)

// topSortValues is the accepted --sort vocabulary, in the order the footer
// lists the keys that select each one.
var topSortValues = []string{
	string(topSortCPU), string(topSortMemory), string(topSortSessions), string(topSortName),
}

func parseTopSort(v string) (topSort, error) {
	switch topSort(v) {
	case topSortCPU, topSortMemory, topSortSessions, topSortName:
		return topSort(v), nil
	}
	return "", validationErr(fmt.Sprintf("unknown sort column %q", v),
		"valid columns: "+strings.Join(topSortValues, ", "))
}

// topMetrics is the subset of one app's GET /api/apps/metrics entry this view
// reads.
type topMetrics struct {
	Status          string       `json:"status"`
	DesiredStatus   string       `json:"desired_status"`
	Deploying       bool         `json:"deploying"`
	SessionsCap     int          `json:"sessions_cap"`
	SessionsCeiling int          `json:"sessions_ceiling"`
	ReplicasDesired int          `json:"replicas_desired"`
	ReplicasRunning int          `json:"replicas_running"`
	WorkersRunning  int          `json:"workers_running"`
	WorkerIsolation string       `json:"worker_isolation"`
	MaxWorkers      int          `json:"max_workers"`
	Replicas        []topReplica `json:"replicas"`
}

// topReplica mirrors the server's per-replica row. CPUPercent is a pointer
// because the server distinguishes "this replica is idle" from "no rate could be
// computed for it yet", and MetricsAvailable says whether the sample succeeded
// at all - a replica on a tier with no PID reports neither CPU nor memory.
type topReplica struct {
	Index            int      `json:"index"`
	Status           string   `json:"status"`
	PID              *int     `json:"pid"`
	CPUPercent       *float64 `json:"cpu_percent"`
	RSSBytes         int64    `json:"rss_bytes"`
	Sessions         int64    `json:"sessions"`
	Tier             string   `json:"tier"`
	Provider         string   `json:"provider"`
	Reason           string   `json:"reason"`
	MetricsAvailable bool     `json:"metrics_available"`
}

// topRow is one app's line in the view.
//
// Every measured quantity is a pointer so that "no replica could report this"
// stays distinct from "the replicas reported zero". An app whose sampler is
// failing must not render as an idle app, because idle is the one reading an
// operator scanning this table will not stop on.
//
// The Partial flags mark a total that is real but incomplete: some running
// replica contributed nothing to it, so the true figure is higher. The view
// renders those with a "at least" marker rather than silently presenting a
// lower bound as the whole.
type topRow struct {
	Slug               string
	Status             string
	Running            int
	Workers            int
	Replicas           int
	MetricsUnavailable bool

	CPUPercent *float64
	CPUPartial bool
	RSSBytes   *int64
	RSSPartial bool
	Sessions   *int64
	// Ceiling is the app's admission ceiling: how many sessions it will accept
	// before rejecting with 503. 0 means uncapped.
	Ceiling int
	// ReplicaRows backs the selected-app inspector in the interactive view. The
	// aggregate table and machine-readable output remain one row per app.
	ReplicaRows []topReplica
}

// topRowFor folds one app's replicas into the single line the view shows.
//
// Only a running replica can contribute: a stopped one holds no memory and
// burns no CPU, which is a measured zero rather than a gap. A running replica
// that could not be sampled contributes nothing and sets the partial flag, so
// the sum is reported as a floor. When no running replica reported at all the
// figure is absent rather than zero.
func topRowFor(slug string, m topMetrics) topRow {
	replicas := m.ReplicasDesired
	if replicas <= 0 {
		replicas = len(m.Replicas)
	}
	row := topRow{
		Slug: slug, Status: m.Status, Replicas: replicas, Workers: m.WorkersRunning,
		ReplicaRows: append([]topReplica(nil), m.Replicas...),
	}
	if m.Deploying {
		row.Status = "deploying"
	}

	var (
		cpu       float64
		rss       int64
		sessions  int64
		cpuKnown  bool
		rssKnown  bool
		sessKnown bool
	)
	for _, r := range m.Replicas {
		// A negative session count is the proxy's "this slot is empty", not a
		// reading of zero.
		if r.Sessions >= 0 {
			sessions += r.Sessions
			sessKnown = true
		}
		if r.Status != "running" {
			continue
		}
		row.Running++
		if !r.MetricsAvailable {
			row.CPUPartial, row.RSSPartial = true, true
			continue
		}
		rss += r.RSSBytes
		rssKnown = true
		if r.CPUPercent == nil {
			row.CPUPartial = true
			continue
		}
		cpu += *r.CPUPercent
		cpuKnown = true
	}
	if m.ReplicasRunning > row.Running {
		row.Running = m.ReplicasRunning
	}

	if cpuKnown {
		row.CPUPercent = &cpu
	}
	if rssKnown {
		row.RSSBytes = &rss
	}
	if sessKnown {
		row.Sessions = &sessions
	}
	row.Ceiling = topCeiling(m, row.Replicas)
	return row
}

// topCeiling computes how many sessions the app admits before it starts
// rejecting. An elastic pool multiplies its per-worker cap by the worker
// ceiling; a multiplex pool multiplies its per-replica cap by the replicas it
// runs. An uncapped pool has no ceiling to report.
func topCeiling(m topMetrics, replicas int) int {
	if m.SessionsCeiling > 0 {
		return m.SessionsCeiling
	}
	if m.SessionsCap <= 0 {
		return 0
	}
	if m.MaxWorkers > 0 {
		return m.MaxWorkers * m.SessionsCap
	}
	return replicas * m.SessionsCap
}

// topRowsFrom builds one row per app. The result is in no particular order;
// sortTopRows decides that.
func topRowsFrom(metrics map[string]topMetrics) []topRow {
	rows := make([]topRow, 0, len(metrics))
	for slug, m := range metrics {
		rows = append(rows, topRowFor(slug, m))
	}
	return rows
}

// sortTopRows orders rows by the chosen column, largest first for the measured
// columns because the view exists to surface the heaviest apps.
//
// A row with no value for that column is never ranked against rows that have
// one: it sorts to the end in both directions, since "not measured" is not a
// quantity and reversing the order does not turn it into the smallest one. Rows
// that tie, and rows with nothing to rank, fall back to the slug, so a table
// that measured the same numbers twice does not reshuffle between ticks.
func sortTopRows(rows []topRow, by topSort, reverse bool) {
	rank := func(r topRow) (float64, bool) {
		switch by {
		case topSortCPU:
			if r.CPUPercent == nil {
				return 0, false
			}
			return *r.CPUPercent, true
		case topSortMemory:
			if r.RSSBytes == nil {
				return 0, false
			}
			return float64(*r.RSSBytes), true
		case topSortSessions:
			if r.Sessions == nil {
				return 0, false
			}
			return float64(*r.Sessions), true
		}
		return 0, false
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if by == topSortName {
			if reverse {
				return a.Slug > b.Slug
			}
			return a.Slug < b.Slug
		}
		av, aok := rank(a)
		bv, bok := rank(b)
		if aok != bok {
			return aok
		}
		if !aok || av == bv {
			return a.Slug < b.Slug
		}
		if reverse {
			return av < bv
		}
		return av > bv
	})
}

// topTotals is the fleet-wide summary line. It carries the same absent/partial
// distinction as a row: an unmeasured app makes the fleet total a floor, not a
// smaller number.
type topTotals struct {
	Apps            int
	Running         int
	Replicas        int
	ReplicasRunning int
	CPUPercent      *float64
	CPUPartial      bool
	RSSBytes        *int64
	RSSPartial      bool
	Sessions        *int64
}

func topTotalsFor(rows []topRow) topTotals {
	t := topTotals{Apps: len(rows)}
	var (
		cpu       float64
		rss       int64
		sessions  int64
		cpuKnown  bool
		rssKnown  bool
		sessKnown bool
	)
	for _, r := range rows {
		t.Replicas += r.Replicas
		t.ReplicasRunning += r.Running
		if r.Running > 0 {
			t.Running++
		}
		if r.CPUPercent != nil {
			cpu += *r.CPUPercent
			cpuKnown = true
		}
		if r.RSSBytes != nil {
			rss += *r.RSSBytes
			rssKnown = true
		}
		if r.Sessions != nil {
			sessions += *r.Sessions
			sessKnown = true
		}
		// A running app that reported nothing is missing from the total just as
		// surely as a half-reported one.
		if r.CPUPartial || (r.Running > 0 && r.CPUPercent == nil) {
			t.CPUPartial = true
		}
		if r.RSSPartial || (r.Running > 0 && r.RSSBytes == nil) {
			t.RSSPartial = true
		}
	}
	if cpuKnown {
		t.CPUPercent = &cpu
	}
	if rssKnown {
		t.RSSBytes = &rss
	}
	if sessKnown {
		t.Sessions = &sessions
	}
	return t
}

// topView is everything renderTop needs to draw one frame. It is a plain value:
// the renderer never reads a clock, a socket or a terminal, so a frame can be
// reproduced exactly in a test.
type topView struct {
	Host string
	// At is when the displayed sample was CAPTURED, never when the frame was
	// painted. A repaint that re-stamped the clock would advance it over data
	// that had not changed, which is precisely the signal an operator uses to
	// tell a live table from a frozen one.
	At time.Time
	// Rows is every app the account can see, never a page of them. The summary
	// line is computed from all of it, so a --limit'd view still reports the
	// whole server's load: that is the figure the line claims to answer, and the
	// one that decides whether there is room for another app.
	Rows []topRow
	// Limit and Offset window which of Rows is drawn. A zero limit draws them
	// all. The window lives here rather than in the slice the caller passes so
	// that the displayed page and the fleet it came from cannot drift apart.
	Limit    int
	Offset   int
	Sort     topSort
	Reverse  bool
	Interval time.Duration
	// Live marks the repainting form. When false the frame is a one-shot report,
	// so it carries no refresh interval and no key legend.
	Live bool
	// Keys marks that keystrokes are being read. False when stdin is not a
	// terminal, where advertising keys that do nothing would be a lie.
	Keys bool
	// Width and Height bound the frame to the terminal. 0 means unbounded.
	Width  int
	Height int
	// Err is the last poll failure, kept on screen while the view retries.
	Err string
	// Age is how old the displayed sample is. A stopped clock is the honest
	// signal but an easy one to miss; once the age passes topStaleAfter the view
	// states how far behind it has fallen in words. A one-shot frame leaves this
	// zero: it is printed the moment it is captured.
	Age time.Duration
	// Interactive state. These fields are ignored by snapshot rendering.
	Selected  string
	Filter    string
	Filtering bool
	Paused    bool
	Inspect   bool
	Help      bool
}

// topMore renders the count in the "N more app(s)" notices. plural builds a
// bare noun phrase ("1 app"), which cannot take the "more" these lines need
// without reading as "more 1 app".
func topMore(n int) string {
	if n == 1 {
		return "1 more app"
	}
	return fmt.Sprintf("%d more apps", n)
}

// topAtLeastMark prefixes a figure the view knows is incomplete.
func topAtLeastMark(s styler) string {
	if s.ascii {
		return ">="
	}
	return "≥"
}

func topCPUText(s styler, v *float64, partial bool) string {
	if v == nil {
		return "-"
	}
	out := fmt.Sprintf("%.1f", *v)
	if partial {
		return topAtLeastMark(s) + out
	}
	return out
}

func topRSSText(s styler, v *int64, partial bool) string {
	if v == nil {
		return "-"
	}
	out := humanBytes(*v)
	if partial {
		return topAtLeastMark(s) + out
	}
	return out
}

// topPercentText is topCPUText carrying its unit, which is attached only when
// there is a figure to attach it to. "-%" reads as a broken format string; "-"
// reads as the missing measurement it is.
func topPercentText(s styler, v *float64, partial bool) string {
	if v == nil {
		return topCPUText(s, v, partial)
	}
	return topCPUText(s, v, partial) + "%"
}

// topSessionsSummary states the session count even when there is none to state.
// Dropping the field would leave the summary silently shorter, which reads as a
// server with nothing worth saying about sessions rather than one whose session
// counts could not be read.
func topSessionsSummary(t topTotals) string {
	if t.Sessions == nil {
		return "- sessions"
	}
	if *t.Sessions == 1 {
		return "1 session"
	}
	return fmt.Sprintf("%d sessions", *t.Sessions)
}

// topSessionsText renders the live session count against the ceiling it is
// heading for, so the number that predicts a 503 is on screen next to the one
// that causes it.
func topSessionsText(r topRow) string {
	if r.Sessions == nil {
		return "-"
	}
	if r.Ceiling > 0 {
		return fmt.Sprintf("%d/%d", *r.Sessions, r.Ceiling)
	}
	return fmt.Sprintf("%d", *r.Sessions)
}

// topSortLabel describes the order in the words the order is actually in, so
// the name column does not claim to be "descending" when it is showing a to z.
func topSortLabel(by topSort, reverse bool) string {
	if by == topSortName {
		if reverse {
			return "name z-a"
		}
		return "name a-z"
	}
	if reverse {
		return string(by) + " low to high"
	}
	return string(by) + " high to low"
}

// renderTop writes one complete frame. It emits plain lines terminated by "\n";
// turning those into the escapes a repainting terminal needs is the live shell's
// job, which keeps this function reproducible in a buffer.
func renderTop(w io.Writer, s styler, v topView) {
	totals := topTotalsFor(v.Rows)

	head := []string{
		fmt.Sprintf("shinyhub top  %s  %s", s.dim(v.Host), s.dim("sampled "+v.At.Format("15:04:05"))),
		s.dim(topSummary(s, totals, v)),
		"",
	}
	if v.Live && v.Keys {
		head = topTUIHeader(s, totals, v)
	}

	var tail []string
	tail = append(tail, "")
	if line := topStatusLine(s, v); line != "" {
		tail = append(tail, line)
	}
	if totals.CPUPartial || totals.RSSPartial || topAnyPartial(v.Rows) {
		tail = append(tail, s.dim(fmt.Sprintf(
			"%s means at least: a running replica has not reported yet, so the real figure is higher",
			topAtLeastMark(s))))
	}
	tail = append(tail, s.dim(topFooter(v)))

	body := topBody(s, v, len(head)+len(tail))

	ellipsis := s.glyphEllipsis()
	for _, line := range append(append(head, body...), tail...) {
		fmt.Fprintln(w, truncateVisible(line, v.Width, ellipsis))
	}
}

// topTUIHeader gives the interactive form the meter bank of a process monitor.
// Snapshot output deliberately keeps the compact prose summary: it is a report,
// while this is the stable control surface an operator watches.
func topTUIHeader(s styler, t topTotals, v topView) []string {
	state := s.green("LIVE")
	if v.Paused {
		state = s.yellow("PAUSED")
	}
	title := fmt.Sprintf("shinyhub top  %s", s.dim(v.Host))
	clock := fmt.Sprintf("%s  sampled %s  every %s", state, v.At.Format("15:04:05"), v.Interval)
	lines := topTUIPair(title, clock, v.Width)

	cpu := topMeter(s, "CPU", topFloat(t.CPUPercent), 100, topPercentText(s, t.CPUPercent, t.CPUPartial), 18, false)
	apps := topMeter(s, "Apps", float64(t.Running), float64(t.Apps),
		fmt.Sprintf("%d/%d running", t.Running, t.Apps), 18, true)
	lines = append(lines, topTUIPair(cpu, apps, v.Width)...)

	memory := fmt.Sprintf("Memory   %-18s", topRSSText(s, t.RSSBytes, t.RSSPartial))
	replicas := topMeter(s, "Replicas", float64(t.ReplicasRunning), float64(t.Replicas),
		fmt.Sprintf("%d/%d running", t.ReplicasRunning, t.Replicas), 18, true)
	lines = append(lines, topTUIPair(memory, replicas, v.Width)...)

	sessionValue := "-"
	if t.Sessions != nil {
		sessionValue = fmt.Sprintf("%d active", *t.Sessions)
	}
	sessions := fmt.Sprintf("Sessions %-18s", sessionValue)
	order := "Order    " + topSortLabel(v.Sort, v.Reverse)
	lines = append(lines, topTUIPair(sessions, order, v.Width)...)
	if v.Filtering {
		lines = append(lines, s.green("Filter: ")+v.Filter+"_")
	} else if v.Filter != "" {
		lines = append(lines, s.dim("Filter: ")+v.Filter+s.dim("  (Esc clears)"))
	}
	return append(lines, "")
}

func topFloat(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// topTUIPair uses two banks on ordinary terminals and stacks them on narrow
// ones. Structural reflow keeps the right-hand state visible instead of merely
// truncating it away.
func topTUIPair(left, right string, width int) []string {
	const split = 46
	if (width > 0 && width < 96) || visibleWidth(left) >= split-2 {
		return []string{left, right}
	}
	padding := split - visibleWidth(left)
	if padding < 2 {
		padding = 2
	}
	return []string{left + strings.Repeat(" ", padding) + right}
}

func topMeter(s styler, label string, value, maximum float64, text string, width int, highIsGood bool) string {
	ratio := 0.0
	if maximum > 0 {
		ratio = value / maximum
	}
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	filled := int(ratio*float64(width) + 0.5)
	fill, empty := "█", "·"
	if s.ascii {
		fill, empty = "|", "."
	}
	bar := strings.Repeat(fill, filled) + strings.Repeat(empty, width-filled)
	if highIsGood {
		switch {
		case ratio >= 1:
			bar = s.green(bar)
		case ratio >= .8:
			bar = s.yellow(bar)
		default:
			bar = s.red(bar)
		}
	} else {
		switch {
		case ratio >= 1:
			bar = s.red(bar)
		case ratio >= .8:
			bar = s.yellow(bar)
		default:
			bar = s.green(bar)
		}
	}
	return fmt.Sprintf("%-8s [%s] %s", label, bar, text)
}

// topSummary is the fleet line: how much of the host these apps are using
// altogether, which is the number that decides whether there is room for one
// more.
func topSummary(s styler, t topTotals, v topView) string {
	parts := []string{
		plural(t.Apps, "app"),
		fmt.Sprintf("%d running", t.Running),
		"cpu " + topPercentText(s, t.CPUPercent, t.CPUPartial),
		"rss " + topRSSText(s, t.RSSBytes, t.RSSPartial),
		topSessionsSummary(t),
	}
	if v.Live {
		parts = append(parts, "every "+v.Interval.String())
	}
	return strings.Join(parts, " · ")
}

// topStaleAfter is how old the displayed sample may get before the view says so.
// One missed refresh is the threshold: a poll still in flight when the next tick
// arrives is ordinary, but a sample two intervals old means a refresh did not
// land. The floor keeps a sub-second age from being announced as "0s old".
func topStaleAfter(v topView) time.Duration {
	if d := 2 * v.Interval; d > time.Second {
		return d
	}
	return time.Second
}

// topStatusLine qualifies the numbers above it: why the last refresh failed, how
// far behind the displayed sample has fallen, or both. It is empty while the
// view is current and nothing has failed.
//
// A stale view with no error is the case that most needs saying. Nothing has
// gone wrong yet - a request is simply still out, and an unanswered one can hang
// for the whole HTTP timeout - so the only other tell is a timestamp that
// stopped moving, which is exactly what an operator watching a table for
// movement will not notice.
func topStatusLine(s styler, v topView) string {
	if v.Paused {
		return s.yellow("paused") + s.dim(" · refresh disabled; press Space to resume")
	}
	stale := ""
	if v.Age >= topStaleAfter(v) {
		stale = fmt.Sprintf("showing a sample %s old", v.Age.Round(time.Second))
	}
	switch {
	case v.Err != "" && stale != "":
		return s.failMark() + " " + s.red(v.Err+" · "+stale)
	case v.Err != "":
		return s.failMark() + " " + s.red(v.Err)
	case stale != "":
		return s.dim("waiting for a refresh · " + stale)
	}
	return ""
}

func topFooter(v topView) string {
	sorted := "sorted by " + topSortLabel(v.Sort, v.Reverse)
	if !v.Live || !v.Keys {
		return sorted
	}
	return "↑↓/jk move  Enter inspect  / filter  Space pause  Tab sort  ? help  q quit"
}

func topAnyPartial(rows []topRow) bool {
	for _, r := range rows {
		if r.CPUPartial || r.RSSPartial {
			return true
		}
	}
	return false
}

// topBody renders the table, clipped to the rows that fit above the footer.
// A clip is never silent: the line it costs says how many apps are off screen.
func topBody(s styler, v topView, overhead int) []string {
	if len(v.Rows) == 0 {
		return []string{s.dim("No apps visible to this account.")}
	}
	if v.Live && v.Keys {
		return topTUIBody(s, v, overhead)
	}

	page := topWindowOf(v.Rows, v.Limit, v.Offset)

	// A page the operator asked for is still a page: without this line a
	// --limit'd live view shows some apps and looks like the whole server. Only
	// the live view needs saying, because the one-shot forms already carry the
	// envelope's total and renderListTo's "showing X of Y" hint on stderr, and
	// printing both would say the same thing twice.
	windowed := ""
	if v.Live && len(page) < len(v.Rows) {
		windowed = s.dim(fmt.Sprintf("%s %s, outside --limit/--offset",
			s.glyphEllipsis(), topMore(len(v.Rows)-len(page))))
	}

	shown, hidden := page, 0
	if v.Height > 0 {
		// The budget covers the table's own header row and any note printed
		// under it, so no notice can push the footer off the screen.
		room := v.Height - overhead - 1
		if windowed != "" {
			room--
		}
		if room < 1 {
			room = 1
		}
		if len(shown) > room {
			// The clip costs one more line to announce itself.
			keep := room - 1
			if keep < 1 {
				keep = 1
			}
			hidden = len(shown) - keep
			shown = shown[:keep]
		}
	}

	t := newTable("APP", "STATUS", "REPLICAS", "CPU%", "RSS", "SESSIONS").
		alignRight(2, 3, 4, 5)
	for _, r := range shown {
		sessions := txt(topSessionsText(r))
		// At the ceiling the app is rejecting new sessions. The digits already
		// say so (10/10); the color is emphasis on a fact that is legible
		// without it.
		if r.Sessions != nil && r.Ceiling > 0 && *r.Sessions >= int64(r.Ceiling) {
			sessions = alertTxt(topSessionsText(r))
		}
		t.row(
			txt(r.Slug),
			statusTxt(r.Status),
			txt(fmt.Sprintf("%d/%d", r.Running, r.Replicas)),
			txt(topCPUText(s, r.CPUPercent, r.CPUPartial)),
			txt(topRSSText(s, r.RSSBytes, r.RSSPartial)),
			sessions,
		)
	}

	var buf bytes.Buffer
	t.renderWith(&buf, s)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if hidden > 0 {
		lines = append(lines, s.dim(fmt.Sprintf("%s %s, hidden by the terminal height",
			s.glyphEllipsis(), topMore(hidden))))
	}
	if windowed != "" {
		lines = append(lines, windowed)
	}
	return lines
}

func topFilteredRows(rows []topRow, filter string) []topRow {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return rows
	}
	out := make([]topRow, 0, len(rows))
	for _, r := range rows {
		if strings.Contains(strings.ToLower(r.Slug), filter) ||
			strings.Contains(strings.ToLower(r.Status), filter) {
			out = append(out, r)
		}
	}
	return out
}

func topSelectedIndex(rows []topRow, selected string) int {
	for i := range rows {
		if rows[i].Slug == selected {
			return i
		}
	}
	return -1
}

func topSortHeader(label string, column, active topSort, reverse bool, ascii bool) string {
	if column != active {
		return label
	}
	arrow := "▼"
	if reverse {
		arrow = "▲"
	}
	if ascii {
		arrow = "v"
		if reverse {
			arrow = "^"
		}
	}
	return label + arrow
}

// topTUIBody is a selectable, scrolling process table. The selected slug, not
// a row number, is the identity so sorting and refreshes do not make the cursor
// jump to a different app.
func topTUIBody(s styler, v topView, overhead int) []string {
	if v.Help {
		lines := topHelpBody(s)
		if v.Height > 0 {
			room := v.Height - overhead
			if room <= 0 {
				return nil
			}
			if len(lines) > room {
				lines = lines[:room]
			}
		}
		return lines
	}
	rows := topFilteredRows(topWindowOf(v.Rows, v.Limit, v.Offset), v.Filter)
	if len(rows) == 0 {
		return []string{s.dim("No apps match ") + fmt.Sprintf("%q", v.Filter) + s.dim(". Press Esc to clear the filter.")}
	}

	selected := topSelectedIndex(rows, v.Selected)
	if selected < 0 {
		selected = 0
	}
	detail := []string(nil)
	if v.Inspect && selected >= 0 {
		detail = topInspector(s, rows[selected])
		if v.Height > 0 {
			maxDetail := v.Height - overhead - 4
			switch {
			case maxDetail <= 0:
				detail = nil
			case len(detail) > maxDetail:
				detail = append(detail[:maxDetail-1], s.dim("  … more replicas; enlarge the terminal to inspect them"))
			}
		}
	}

	room := len(rows)
	if v.Height > 0 {
		// One line belongs to the table header. When scrolling is needed, another
		// belongs to the position indicator. Inspector lines are protected too.
		room = v.Height - overhead - 1 - len(detail)
		if room < len(rows) {
			room--
		}
		if room < 1 {
			room = 1
		}
	}
	start := 0
	if selected >= room {
		start = selected - room + 1
	}
	if start+room > len(rows) {
		start = len(rows) - room
	}
	if start < 0 {
		start = 0
	}
	end := start + room
	if end > len(rows) {
		end = len(rows)
	}

	t := newTable(" ", topSortHeader("APP", topSortName, v.Sort, v.Reverse, s.ascii), "STATUS", "REPLICAS",
		topSortHeader("CPU%", topSortCPU, v.Sort, v.Reverse, s.ascii),
		topSortHeader("RSS", topSortMemory, v.Sort, v.Reverse, s.ascii),
		topSortHeader("SESSIONS", topSortSessions, v.Sort, v.Reverse, s.ascii)).
		alignRight(3, 4, 5, 6)
	for i := start; i < end; i++ {
		r := rows[i]
		isSelected := i == selected
		marker := ""
		if isSelected {
			marker = ">"
		}
		status := statusTxt(r.Status)
		sessions := txt(topSessionsText(r))
		if isSelected {
			// An outer reverse-video run must not contain inner resets, or the
			// selection would disappear halfway across the row.
			status = txt(r.Status)
		} else if r.Sessions != nil && r.Ceiling > 0 && *r.Sessions >= int64(r.Ceiling) {
			sessions = alertTxt(topSessionsText(r))
		}
		markerCell := markTxt(marker)
		if isSelected {
			markerCell = txt(marker)
		}
		t.row(markerCell, txt(r.Slug), status,
			txt(fmt.Sprintf("%d/%d", r.Running, r.Replicas)),
			txt(topCPUText(s, r.CPUPercent, r.CPUPartial)),
			txt(topRSSText(s, r.RSSBytes, r.RSSPartial)), sessions)
		if isSelected {
			t.paintRow(styler.reverse)
		}
	}

	var buf bytes.Buffer
	t.renderWith(&buf, s)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(rows) > room {
		lines = append(lines, s.dim(fmt.Sprintf("rows %d-%d of %d", start+1, end, len(rows))))
	}
	return append(lines, detail...)
}

func topInspector(s styler, r topRow) []string {
	lines := []string{"", fmt.Sprintf("%s  %s · %d/%d replicas · sessions %s",
		s.green(r.Slug), s.status(r.Status), r.Running, r.Replicas, topSessionsText(r))}
	if len(r.ReplicaRows) == 0 {
		return append(lines, s.dim("  No replicas reported."))
	}
	t := newTable("#", "STATUS", "PID", "CPU%", "RSS", "SESS", "PLACEMENT").
		alignRight(0, 2, 3, 4, 5).indent(2)
	for _, replica := range r.ReplicaRows {
		pid := "-"
		if replica.PID != nil {
			pid = fmt.Sprintf("%d", *replica.PID)
		}
		cpu := "-"
		if replica.CPUPercent != nil {
			cpu = fmt.Sprintf("%.1f", *replica.CPUPercent)
		}
		placement := strings.Trim(replica.Tier+"/"+replica.Provider, "/")
		t.row(txt(replica.Index), statusTxt(replica.Status), dimTxt(pid), txt(cpu),
			txt(humanBytes(replica.RSSBytes)), txt(replica.Sessions), dimTxt(placement)).
			note(replica.Reason)
	}
	var buf bytes.Buffer
	t.renderWith(&buf, s)
	return append(lines, strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")...)
}

func topHelpBody(s styler) []string {
	return []string{
		s.green("Keyboard"),
		"  ↑ / k, ↓ / j      move selection",
		"  PgUp / PgDn       move one page",
		"  Home / End        first or last app",
		"  Enter             inspect selected app's replicas",
		"  /                 filter by app or status",
		"  Esc               cancel or clear filter",
		"  Space             pause or resume refreshes",
		"  Tab               cycle sort column",
		"  c / m / s / n     sort by CPU, memory, sessions, or name",
		"  r                 reverse sort order",
		"  ?                 close this help",
		"  q                 quit",
	}
}

// truncateVisible cuts line to at most width printed columns, replacing the
// last one with an ellipsis so a clipped slug is visibly clipped rather than
// reading as a shorter name that exists.
//
// Escape sequences are copied through and never counted: they occupy no columns,
// and measuring them would clip a line that fits. A reset is appended whenever
// the line was painted, because the cut can land inside a colored run and the
// color would otherwise bleed across the rest of the frame.
func truncateVisible(line string, width int, ellipsis string) string {
	if width <= 0 || visibleWidth(line) <= width {
		return line
	}
	limit := width - utf8.RuneCountInString(ellipsis)
	if limit < 0 {
		limit = 0
	}

	var b strings.Builder
	kept, painted, inEscape := 0, false, false
	for _, r := range line {
		switch {
		case inEscape:
			b.WriteRune(r)
			if isANSIFinal(r) {
				inEscape = false
			}
		case r == 0x1b:
			painted, inEscape = true, true
			b.WriteRune(r)
		default:
			if kept == limit {
				b.WriteString(ellipsis)
				if painted {
					b.WriteString(ansiReset)
				}
				return b.String()
			}
			b.WriteRune(r)
			kept++
		}
	}
	b.WriteString(ellipsis)
	if painted {
		b.WriteString(ansiReset)
	}
	return b.String()
}

// visibleWidth counts the columns a rendered line occupies, skipping ANSI
// escape sequences.
func visibleWidth(line string) int {
	n, inEscape := 0, false
	for _, r := range line {
		switch {
		case inEscape:
			if isANSIFinal(r) {
				inEscape = false
			}
		case r == 0x1b:
			inEscape = true
		default:
			n++
		}
	}
	return n
}

// isANSIFinal reports whether r ends an escape sequence. Every sequence this
// package emits is a CSI ending in a letter.
func isANSIFinal(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// topItems is the machine-readable form of the same rows. Absent figures stay
// absent: they encode as null, never as 0, and the partial flags say when a
// number is a floor rather than a total.
func topItems(rows []topRow) []map[string]any {
	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		items = append(items, map[string]any{
			"slug":                r.Slug,
			"status":              r.Status,
			"replicas_running":    r.Running,
			"workers_running":     r.Workers,
			"replicas_desired":    r.Replicas,
			"replicas_total":      r.Replicas,
			"metrics_unavailable": r.MetricsUnavailable,
			"cpu_percent":         r.CPUPercent,
			"cpu_percent_partial": r.CPUPartial,
			"rss_bytes":           r.RSSBytes,
			"rss_bytes_partial":   r.RSSPartial,
			"sessions":            r.Sessions,
			"sessions_ceiling":    r.Ceiling,
		})
	}
	return items
}

// topTotalsMap is the envelope-level summary of the same figures.
func topTotalsMap(t topTotals) map[string]any {
	return map[string]any{
		"apps":                t.Apps,
		"running":             t.Running,
		"cpu_percent":         t.CPUPercent,
		"cpu_percent_partial": t.CPUPartial,
		"rss_bytes":           t.RSSBytes,
		"rss_bytes_partial":   t.RSSPartial,
		"sessions":            t.Sessions,
	}
}
