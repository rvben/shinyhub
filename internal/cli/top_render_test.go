package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

// runningReplica is a replica that reported both figures successfully.
func runningReplica(cpu float64, rss, sessions int64) topReplica {
	return topReplica{
		Status: "running", CPUPercent: f64(cpu), RSSBytes: rss,
		Sessions: sessions, MetricsAvailable: true,
	}
}

func renderToString(v topView) string {
	var buf bytes.Buffer
	renderTop(&buf, styler{}, v)
	return buf.String()
}

// The figure an operator reads is the app's whole footprint, so every running
// replica is added up. Reporting one replica (which the legacy top-level
// cpu_percent/rss_bytes fields do) would understate a scaled-out app by exactly
// the factor that makes it worth watching.
func TestTopRow_SumsEveryRunningReplica(t *testing.T) {
	row := topRowFor("web", topMetrics{
		Status: "running",
		Replicas: []topReplica{
			runningReplica(30, 100, 2),
			runningReplica(12.5, 50, 3),
		},
	})

	if row.CPUPercent == nil || *row.CPUPercent != 42.5 {
		t.Errorf("cpu = %v, want 42.5 (30 + 12.5 across both replicas)", row.CPUPercent)
	}
	if row.RSSBytes == nil || *row.RSSBytes != 150 {
		t.Errorf("rss = %v, want 150", row.RSSBytes)
	}
	if row.Sessions == nil || *row.Sessions != 5 {
		t.Errorf("sessions = %v, want 5", row.Sessions)
	}
	if row.Running != 2 || row.Replicas != 2 {
		t.Errorf("running/replicas = %d/%d, want 2/2", row.Running, row.Replicas)
	}
	if row.CPUPartial || row.RSSPartial {
		t.Error("row marked partial although every running replica reported")
	}
}

// A stopped replica is a measured zero, not a gap: it holds no memory and burns
// no CPU. Marking the app partial for it would put an "at least" marker on a
// figure that is exactly right, training the operator to ignore the marker.
func TestTopRow_StoppedReplicaIsNotAGap(t *testing.T) {
	row := topRowFor("web", topMetrics{
		Status: "running",
		Replicas: []topReplica{
			runningReplica(10, 100, 1),
			{Status: "stopped", Sessions: 0},
		},
	})

	if row.CPUPartial || row.RSSPartial {
		t.Error("a stopped replica marked the row partial; it contributes a real zero")
	}
	if row.Running != 1 {
		t.Errorf("running = %d, want 1", row.Running)
	}
	if row.CPUPercent == nil || *row.CPUPercent != 10 {
		t.Errorf("cpu = %v, want 10", row.CPUPercent)
	}
}

// A running replica the sampler could not read makes the sum a floor. The
// number stays useful, but it must not be presented as the whole, because the
// missing replica could be the one at 100%.
func TestTopRow_UnsampledRunningReplicaMakesTheSumAFloor(t *testing.T) {
	row := topRowFor("web", topMetrics{
		Status: "running",
		Replicas: []topReplica{
			runningReplica(10, 100, 1),
			{Status: "running", Sessions: 1, MetricsAvailable: false},
		},
	})

	if !row.CPUPartial || !row.RSSPartial {
		t.Errorf("partial flags = cpu:%v rss:%v, want both true: one running replica reported nothing",
			row.CPUPartial, row.RSSPartial)
	}
	if row.CPUPercent == nil || *row.CPUPercent != 10 {
		t.Errorf("cpu = %v, want the 10 that WAS measured; dropping it would discard a real reading",
			row.CPUPercent)
	}
	if row.Running != 2 {
		t.Errorf("running = %d, want 2: a replica that cannot be sampled is still running", row.Running)
	}
}

// A replica with memory but no CPU rate yet (the first sample after a start) is
// half a reading. Only CPU is a floor; RSS was measured exactly.
func TestTopRow_MissingRateDoesNotTaintMemory(t *testing.T) {
	row := topRowFor("web", topMetrics{
		Status: "running",
		Replicas: []topReplica{
			{Status: "running", CPUPercent: nil, RSSBytes: 400, Sessions: 0, MetricsAvailable: true},
		},
	})

	if !row.CPUPartial {
		t.Error("cpu not marked partial although no rate was available")
	}
	if row.CPUPercent != nil {
		t.Errorf("cpu = %v, want absent: no replica produced a rate, and 0 would read as idle", row.CPUPercent)
	}
	if row.RSSPartial {
		t.Error("rss marked partial although the replica reported its memory exactly")
	}
	if row.RSSBytes == nil || *row.RSSBytes != 400 {
		t.Errorf("rss = %v, want 400", row.RSSBytes)
	}
}

// An app whose whole pool failed to report has no figure at all. Zero is the
// single most dangerous value to invent here: it is the reading an operator
// scanning for a runaway app will skip past.
func TestTopRow_NothingMeasuredRendersAbsentNotZero(t *testing.T) {
	row := topRowFor("web", topMetrics{
		Status: "running",
		Replicas: []topReplica{
			{Status: "running", Sessions: -1, MetricsAvailable: false},
		},
	})

	if row.CPUPercent != nil || row.RSSBytes != nil {
		t.Errorf("cpu=%v rss=%v, want both absent", row.CPUPercent, row.RSSBytes)
	}
	if row.Sessions != nil {
		t.Errorf("sessions = %v, want absent: -1 is the proxy's empty slot, not a count of zero",
			row.Sessions)
	}
	if got := topCPUText(styler{}, row.CPUPercent, row.CPUPartial); got != "-" {
		t.Errorf("cpu text = %q, want %q", got, "-")
	}
	if got := topSessionsText(row); got != "-" {
		t.Errorf("sessions text = %q, want %q", got, "-")
	}
}

// A deploy in flight is what the operator needs to see; the app's steady-state
// status would still say "running" while its replicas are being replaced.
func TestTopRow_DeployingOverridesStatus(t *testing.T) {
	row := topRowFor("web", topMetrics{Status: "running", Deploying: true})
	if row.Status != "deploying" {
		t.Errorf("status = %q, want deploying", row.Status)
	}
}

// The ceiling is what predicts a 503, so an elastic pool must multiply by its
// worker ceiling and a multiplex pool by its replicas. Getting this wrong shows
// an app as having 5x the headroom it has.
func TestTopCeiling_ElasticAndMultiplexAndUncapped(t *testing.T) {
	elastic := topMetrics{SessionsCap: 5, WorkerIsolation: "grouped", MaxWorkers: 6}
	if got := topCeiling(elastic, 1); got != 30 {
		t.Errorf("elastic ceiling = %d, want 30 (max_workers 6 x cap 5)", got)
	}
	multiplex := topMetrics{SessionsCap: 4}
	if got := topCeiling(multiplex, 3); got != 12 {
		t.Errorf("multiplex ceiling = %d, want 12 (3 replicas x cap 4)", got)
	}
	if got := topCeiling(topMetrics{SessionsCap: 0}, 3); got != 0 {
		t.Errorf("uncapped ceiling = %d, want 0", got)
	}
}

// The sessions cell pairs the count with the number that will start refusing
// connections, and says only the count when nothing will.
func TestTopSessionsText_ShowsTheCeilingThatCausesA503(t *testing.T) {
	n := int64(7)
	if got := topSessionsText(topRow{Sessions: &n, Ceiling: 10}); got != "7/10" {
		t.Errorf("got %q, want 7/10", got)
	}
	if got := topSessionsText(topRow{Sessions: &n}); got != "7" {
		t.Errorf("uncapped got %q, want 7", got)
	}
}

// Sorting exists to float the heaviest app to the top. A row with nothing to
// rank is not the lightest app; it is an unknown, so it sits at the end in both
// directions rather than winning "lowest CPU".
func TestSortTopRows_UnmeasuredRowsStayLastInBothDirections(t *testing.T) {
	rows := []topRow{
		{Slug: "unknown"},
		{Slug: "light", CPUPercent: f64(1)},
		{Slug: "heavy", CPUPercent: f64(90)},
	}

	sortTopRows(rows, topSortCPU, false)
	if got := slugsOf(rows); got != "heavy,light,unknown" {
		t.Errorf("descending = %s, want heavy,light,unknown", got)
	}

	sortTopRows(rows, topSortCPU, true)
	if got := slugsOf(rows); got != "light,heavy,unknown" {
		t.Errorf("ascending = %s, want light,heavy,unknown: an unmeasured app must not "+
			"be reported as the least busy one", got)
	}
}

// Ties break on the slug so a table where several apps read the same number
// does not reshuffle every tick, which makes the view impossible to read.
func TestSortTopRows_TiesAreStableByName(t *testing.T) {
	rows := []topRow{
		{Slug: "c", CPUPercent: f64(5)},
		{Slug: "a", CPUPercent: f64(5)},
		{Slug: "b", CPUPercent: f64(5)},
	}
	sortTopRows(rows, topSortCPU, false)
	if got := slugsOf(rows); got != "a,b,c" {
		t.Errorf("got %s, want a,b,c", got)
	}
}

func TestSortTopRows_NameSortsAlphabeticallyAndReverses(t *testing.T) {
	rows := []topRow{{Slug: "b"}, {Slug: "a"}, {Slug: "c"}}
	sortTopRows(rows, topSortName, false)
	if got := slugsOf(rows); got != "a,b,c" {
		t.Errorf("got %s, want a,b,c", got)
	}
	sortTopRows(rows, topSortName, true)
	if got := slugsOf(rows); got != "c,b,a" {
		t.Errorf("reversed got %s, want c,b,a", got)
	}
}

// The label describes the order in the words the order is actually in. Calling
// an a-to-z name sort "descending" is the kind of small lie that makes an
// operator distrust the rest of the screen.
func TestTopSortLabel_DescribesTheOrderTruthfully(t *testing.T) {
	cases := []struct {
		by      topSort
		reverse bool
		want    string
	}{
		{topSortName, false, "name a-z"},
		{topSortName, true, "name z-a"},
		{topSortCPU, false, "cpu high to low"},
		{topSortCPU, true, "cpu low to high"},
		{topSortSessions, true, "sessions low to high"},
		{topSortMemory, false, "mem high to low"},
	}
	for _, c := range cases {
		if got := topSortLabel(c.by, c.reverse); got != c.want {
			t.Errorf("topSortLabel(%q, %v) = %q, want %q", c.by, c.reverse, got, c.want)
		}
	}
}

// A fleet total missing one running app's contribution is a floor. Without the
// flag the summary line reads as the server's whole load, which is the number
// an operator uses to decide there is room for another app.
func TestTopTotals_RunningAppThatReportedNothingMakesTheTotalAFloor(t *testing.T) {
	totals := topTotalsFor([]topRow{
		{Slug: "a", Running: 1, CPUPercent: f64(10), RSSBytes: i64(100)},
		{Slug: "b", Running: 1}, // running, reported nothing
	})

	if !totals.CPUPartial || !totals.RSSPartial {
		t.Errorf("partial = cpu:%v rss:%v, want both true", totals.CPUPartial, totals.RSSPartial)
	}
	if totals.CPUPercent == nil || *totals.CPUPercent != 10 {
		t.Errorf("cpu total = %v, want 10", totals.CPUPercent)
	}
	if totals.Running != 2 {
		t.Errorf("running = %d, want 2", totals.Running)
	}
}

// A fleet of entirely stopped apps really is at zero, and saying "at least 0"
// there would be noise that devalues the marker where it matters.
func TestTopTotals_AllStoppedIsNotPartial(t *testing.T) {
	totals := topTotalsFor([]topRow{{Slug: "a", Replicas: 1}, {Slug: "b", Replicas: 1}})
	if totals.CPUPartial || totals.RSSPartial {
		t.Error("a fleet with nothing running was marked partial")
	}
	if totals.Running != 0 {
		t.Errorf("running = %d, want 0", totals.Running)
	}
}

// The lower-bound marker is meaningless without the sentence that defines it,
// and that sentence must not appear when nothing is a lower bound.
func TestRenderTop_ExplainsTheMarkerOnlyWhenSomethingIsAFloor(t *testing.T) {
	partial := renderToString(topView{
		Host: "https://example.test", At: time.Unix(0, 0).UTC(),
		Rows: []topRow{{Slug: "a", Status: "running", Running: 2, Replicas: 2,
			CPUPercent: f64(5), CPUPartial: true, RSSBytes: i64(1000)}},
		Sort: topSortCPU,
	})
	if !strings.Contains(partial, "≥5.0") {
		t.Errorf("frame does not mark the incomplete figure:\n%s", partial)
	}
	if !strings.Contains(partial, "means at least") {
		t.Errorf("frame uses the marker without explaining it:\n%s", partial)
	}

	complete := renderToString(topView{
		Host: "https://example.test", At: time.Unix(0, 0).UTC(),
		Rows: []topRow{{Slug: "a", Status: "running", Running: 1, Replicas: 1,
			CPUPercent: f64(5), RSSBytes: i64(1000)}},
		Sort: topSortCPU,
	})
	if strings.Contains(complete, "means at least") {
		t.Errorf("frame explains a marker it never uses:\n%s", complete)
	}
}

// Keys are advertised only when they will work. A one-shot report has no key
// handling at all, and a live view whose stdin is not a terminal is reading no
// keystrokes, so listing "q quit" there tells the operator to press something
// that does nothing.
func TestTopFooter_AdvertisesKeysOnlyWhenTheyAreRead(t *testing.T) {
	live := topFooter(topView{Sort: topSortCPU, Live: true, Keys: true})
	if !strings.Contains(live, "q quit") {
		t.Errorf("live footer omits the keys: %q", live)
	}
	oneShot := topFooter(topView{Sort: topSortCPU})
	if strings.Contains(oneShot, "q quit") {
		t.Errorf("one-shot footer advertises keys it does not read: %q", oneShot)
	}
	noKeys := topFooter(topView{Sort: topSortCPU, Live: true, Keys: false})
	if strings.Contains(noKeys, "q quit") {
		t.Errorf("footer advertises keys although stdin is not a terminal: %q", noKeys)
	}
	if !strings.Contains(noKeys, "sorted by") {
		t.Errorf("footer dropped the sort description along with the keys: %q", noKeys)
	}
}

// The refresh rate belongs to the live form only: printed in a one-shot report
// it would promise an update that is never coming.
func TestRenderTop_OneShotDoesNotClaimToRefresh(t *testing.T) {
	out := renderToString(topView{
		Host: "h", At: time.Unix(0, 0).UTC(), Sort: topSortCPU,
		Interval: 2 * time.Second,
		Rows:     []topRow{{Slug: "a", Status: "stopped", Replicas: 1}},
	})
	if strings.Contains(out, "every 2s") {
		t.Errorf("a one-shot frame advertises a refresh interval:\n%s", out)
	}
}

// The summary keeps every field even when it has no figure for one. A dropped
// field is the same lie as a fabricated zero: the line reads as a server with
// nothing to report about sessions, rather than one whose sessions could not be
// counted. And a unit only belongs on a number, or "-%" reads as a formatting
// bug rather than as a missing measurement.
func TestTopSummary_KeepsEveryFieldAndDropsOnlyTheUnit(t *testing.T) {
	nothing := topSummary(styler{}, topTotalsFor([]topRow{{Slug: "a", Replicas: 1}}), topView{})
	if !strings.Contains(nothing, "cpu -") {
		t.Errorf("summary does not report the missing cpu figure: %q", nothing)
	}
	if strings.Contains(nothing, "-%") {
		t.Errorf("summary hangs a unit on a missing figure: %q", nothing)
	}
	if !strings.Contains(nothing, "- sessions") {
		t.Errorf("summary silently dropped the sessions field instead of reporting it "+
			"as unmeasured: %q", nothing)
	}

	measured := topSummary(styler{}, topTotalsFor([]topRow{
		{Slug: "a", Running: 1, Replicas: 1, CPUPercent: f64(12.5), RSSBytes: i64(1024), Sessions: i64(3)},
	}), topView{})
	if !strings.Contains(measured, "cpu 12.5%") {
		t.Errorf("summary dropped the unit from a real figure: %q", measured)
	}
	if !strings.Contains(measured, "3 sessions") {
		t.Errorf("summary lost the session count: %q", measured)
	}
}

// The summary counts two things that can be one, and the line leads the whole
// screen. "1 apps · 1 sessions" is the first thing an operator reads.
func TestTopSummary_CountsOfOneTakeTheSingular(t *testing.T) {
	one := topSummary(styler{}, topTotalsFor([]topRow{
		{Slug: "a", Running: 1, Replicas: 1, CPUPercent: f64(1), RSSBytes: i64(8), Sessions: i64(1)},
	}), topView{})

	if strings.Contains(one, "1 apps") {
		t.Errorf("summary pluralises a single app: %q", one)
	}
	if !strings.Contains(one, "1 app ") {
		t.Errorf("summary lost the app count: %q", one)
	}
	if strings.Contains(one, "1 sessions") {
		t.Errorf("summary pluralises a single session: %q", one)
	}
	if !strings.Contains(one, "1 session") {
		t.Errorf("summary lost the session count: %q", one)
	}

	many := topSummary(styler{}, topTotalsFor([]topRow{
		{Slug: "a", Running: 1, Replicas: 1, Sessions: i64(2)},
		{Slug: "b", Replicas: 1},
	}), topView{})
	if !strings.Contains(many, "2 apps") || !strings.Contains(many, "2 sessions") {
		t.Errorf("summary lost a plural: %q", many)
	}
}

// An account that can see nothing must say so. An empty table under a healthy
// header reads as a server with no load rather than a view with no access.
func TestRenderTop_EmptyFleetSaysSoInWords(t *testing.T) {
	out := renderToString(topView{Host: "h", At: time.Unix(0, 0).UTC(), Sort: topSortCPU})
	if !strings.Contains(out, "No apps visible to this account.") {
		t.Errorf("empty view does not explain itself:\n%s", out)
	}
}

// A failed refresh stays on screen. Blanking it would leave a frozen table that
// looks live, which is worse than a stale table that says it is stale.
func TestRenderTop_KeepsTheLastFailureVisible(t *testing.T) {
	out := renderToString(topView{
		Host: "h", At: time.Unix(0, 0).UTC(), Sort: topSortCPU, Live: true,
		Interval: time.Second,
		Rows:     []topRow{{Slug: "a", Status: "running", Running: 1, Replicas: 1}},
		Err:      "last refresh failed: connection refused",
		Age:      47 * time.Second,
	})
	if !strings.Contains(out, "connection refused") {
		t.Errorf("the poll failure is not on screen:\n%s", out)
	}
	if !strings.Contains(out, "47s old") {
		t.Errorf("the frame reports a failure without saying how stale the numbers "+
			"above it have gone:\n%s", out)
	}
}

// The clock in the header is the moment the sample was CAPTURED. Stamping it at
// paint time would tick forward over data that stopped changing, turning the one
// signal that distinguishes a live table from a frozen one into decoration.
func TestRenderTop_ClockIsTheSampleTimeNotThePaintTime(t *testing.T) {
	sampled := time.Now().Add(-3 * time.Hour)
	out := renderToString(topView{
		Host: "h", At: sampled, Sort: topSortCPU, Live: true, Interval: time.Second,
		Rows: []topRow{{Slug: "a", Status: "running", Running: 1, Replicas: 1}},
	})

	if !strings.Contains(out, "sampled "+sampled.Format("15:04:05")) {
		t.Errorf("header does not carry the capture time %s:\n%s",
			sampled.Format("15:04:05"), out)
	}
	if now := time.Now().Format("15:04:05"); strings.Contains(out, now) {
		t.Errorf("header shows the current clock %s over a sample taken three hours "+
			"ago, so a frozen view would keep looking fresh:\n%s", now, out)
	}
}

// A staleness note is only useful once there is staleness to report. Printing
// "0s old" beside every transient blip is noise on a view that has not actually
// fallen behind.
func TestTopStatusLine_AttachesTheAgeOnlyOnceThereIsOne(t *testing.T) {
	fresh := topStatusLine(styler{}, topView{Err: "connection refused", Age: 200 * time.Millisecond})
	if strings.Contains(fresh, "old") {
		t.Errorf("got %q, want no age: the sample is still current", fresh)
	}
	if !strings.Contains(fresh, "connection refused") {
		t.Errorf("got %q, want the failure reported", fresh)
	}

	stale := topStatusLine(styler{}, topView{Err: "connection refused", Age: 47 * time.Second})
	if !strings.Contains(stale, "connection refused") {
		t.Errorf("the age replaced the failure instead of joining it: %q", stale)
	}
	if !strings.Contains(stale, "47s old") {
		t.Errorf("got %q, want the age of the sample still on screen", stale)
	}
}

// A refresh that never comes back produces no error at all: the request is
// simply still out, and can stay out for the whole HTTP timeout. The table then
// shows numbers minutes old under a header whose only tell is a clock that
// stopped ticking, which is what an operator scanning for movement will miss.
func TestTopStatusLine_SaysSoWhenARefreshSimplyNeverCameBack(t *testing.T) {
	hung := topStatusLine(styler{}, topView{
		Live: true, Interval: 2 * time.Second, Age: 26 * time.Second,
	})
	if hung == "" {
		t.Fatal("a view showing a 26s-old sample says nothing about it; the frozen " +
			"clock is the only signal a hung refresh leaves")
	}
	if !strings.Contains(hung, "26s old") {
		t.Errorf("got %q, want the age of the sample", hung)
	}
	if !strings.Contains(hung, "waiting for a refresh") {
		t.Errorf("got %q, want it to say a refresh is outstanding", hung)
	}
}

// One poll still in flight when the next tick fires is ordinary, so the
// threshold is a refresh that actually did not land. Warning on every tick
// would make the line meaningless by the time it mattered.
func TestTopStaleAfter_ToleratesOneSlowPollButNotAMissedRefresh(t *testing.T) {
	v := topView{Live: true, Interval: 2 * time.Second}

	v.Age = 2100 * time.Millisecond // one poll running a little long
	if line := topStatusLine(styler{}, v); line != "" {
		t.Errorf("a poll barely outrunning its tick was reported as stale: %q", line)
	}

	v.Age = 5 * time.Second // a whole refresh missed
	if line := topStatusLine(styler{}, v); line == "" {
		t.Error("a sample older than two refresh intervals was reported as current")
	}

	// With no interval there is nothing to scale to, so the floor applies and a
	// sub-second age is never announced as "0s old".
	if line := topStatusLine(styler{}, topView{Age: 400 * time.Millisecond}); line != "" {
		t.Errorf("a sub-second age produced a staleness note: %q", line)
	}
}

// --limit is invisible in a repainting view: there is no envelope total and no
// stderr hint, so a windowed table reads as the whole server and the app the
// operator came to find is simply not there.
func TestTopBody_LiveWindowSaysWhatIsOutsideThePage(t *testing.T) {
	rows := topFleet(9)

	live := renderToString(topView{
		Host: "h", At: time.Unix(0, 0).UTC(), Sort: topSortCPU, Live: true,
		Interval: time.Second, Rows: rows, Limit: 1,
	})
	if !strings.Contains(live, "8 more apps, outside --limit/--offset") {
		t.Errorf("a windowed live view presents its page as the whole fleet:\n%s", live)
	}

	// A count of one takes the singular. "1 more apps" on a monitoring screen is
	// small, but it is the kind of sloppiness that makes an operator wonder what
	// else on the screen was not thought through.
	one := renderToString(topView{
		Host: "h", At: time.Unix(0, 0).UTC(), Sort: topSortCPU, Live: true,
		Interval: time.Second, Rows: topFleet(2), Limit: 1,
	})
	if !strings.Contains(one, "1 more app,") {
		t.Errorf("a single hidden app is not reported in the singular:\n%s", one)
	}

	// The one-shot forms already say it twice over (the envelope's total, and
	// renderListTo's "showing X of Y" on stderr); a third copy inside the table
	// is noise.
	oneShot := renderToString(topView{
		Host: "h", At: time.Unix(0, 0).UTC(), Sort: topSortCPU, Rows: rows, Limit: 1,
	})
	if strings.Contains(oneShot, "outside --limit/--offset") {
		t.Errorf("one-shot frame repeats what the envelope and the stderr hint "+
			"already say:\n%s", oneShot)
	}

	whole := renderToString(topView{
		Host: "h", At: time.Unix(0, 0).UTC(), Sort: topSortCPU, Live: true,
		Interval: time.Second, Rows: rows,
	})
	if strings.Contains(whole, "outside --limit/--offset") {
		t.Errorf("an unwindowed view claims apps are being hidden:\n%s", whole)
	}
}

// The summary answers "how loaded is this host", so it must cover the host and
// not the page. A --limit'd view that summed only its own rows would report a
// fraction of the load under a heading that claims to be the whole, and would
// disagree with the totals the JSON form of the same command emits.
func TestTopSummary_CoversTheFleetEvenWhenTheTableShowsAPage(t *testing.T) {
	rows := make([]topRow, 4)
	for i := range rows {
		cpu, rss, sessions := 10.0, int64(1<<20), int64(2)
		rows[i] = topRow{
			Slug: string(rune('a'+i)) + "-app", Status: "running", Running: 1, Replicas: 1,
			CPUPercent: &cpu, RSSBytes: &rss, Sessions: &sessions,
		}
	}

	out := renderToString(topView{
		Host: "h", At: time.Unix(0, 0).UTC(), Sort: topSortName, Live: true,
		Interval: time.Second, Rows: rows, Limit: 1,
	})

	for _, want := range []string{"4 apps", "4 running", "cpu 40.0%", "rss 4.0MiB", "8 sessions"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary of a --limit 1 view is missing %q, so it describes the "+
				"page rather than the host:\n%s", want, out)
		}
	}
	// The page itself is still one row: this is not a test that --limit stopped working.
	if strings.Contains(out, "b-app") {
		t.Errorf("--limit 1 rendered more than one row:\n%s", out)
	}
}

// topFleet builds n interchangeable running apps for window tests.
func topFleet(n int) []topRow {
	rows := make([]topRow, n)
	for i := range rows {
		rows[i] = topRow{
			Slug:   fmt.Sprintf("app-%02d", i),
			Status: "running", Running: 1, Replicas: 1,
		}
	}
	return rows
}

// Both notices cost a line, and neither may be paid for out of the footer's.
// A frame one line too tall scrolls in place: the header walks off the top on
// every repaint, which is the failure that makes a live view unusable.
func TestTopBody_BothNoticesFitInsideTheTerminalHeight(t *testing.T) {
	out := renderToString(topView{
		Host: "h", At: time.Unix(0, 0).UTC(), Sort: topSortCPU, Live: true,
		Interval: time.Second, Height: 12, Rows: topFleet(40), Limit: 20,
	})

	if !strings.Contains(out, "hidden by the terminal height") {
		t.Errorf("rows were dropped for height with no notice:\n%s", out)
	}
	if !strings.Contains(out, "outside --limit/--offset") {
		t.Errorf("the window notice was dropped to make room:\n%s", out)
	}
	if lines := strings.Count(out, "\n"); lines > 12 {
		t.Errorf("frame is %d lines on a 12-line terminal:\n%s", lines, out)
	}
	if !strings.Contains(out, "sorted by") {
		t.Errorf("the footer was pushed off a 12-line terminal:\n%s", out)
	}
}

// Clipping to the terminal height must cost a line that says what was cut. A
// silent clip turns "the 40 apps that fit" into "all the apps there are", and
// the app the operator was looking for is the one off the bottom.
func TestTopBody_HeightClipIsAnnounced(t *testing.T) {
	rows := make([]topRow, 40)
	for i := range rows {
		rows[i] = topRow{Slug: string(rune('a'+i%26)) + "-app", Status: "running", Running: 1, Replicas: 1}
	}
	out := renderToString(topView{
		Host: "h", At: time.Unix(0, 0).UTC(), Sort: topSortCPU, Live: true,
		Interval: time.Second, Height: 12, Rows: rows,
	})

	if !strings.Contains(out, "more apps, hidden by the terminal height") {
		t.Errorf("rows were dropped with no notice:\n%s", out)
	}
	if lines := strings.Count(out, "\n"); lines > 12 {
		t.Errorf("frame is %d lines on a 12-line terminal; it would scroll the header away", lines)
	}
}

// With room to spare nothing is hidden, so the notice must not appear: a view
// that always claims hidden rows is one an operator learns to ignore.
func TestTopBody_NoClipNoNotice(t *testing.T) {
	out := renderToString(topView{
		Host: "h", At: time.Unix(0, 0).UTC(), Sort: topSortCPU, Height: 40,
		Rows: []topRow{{Slug: "only", Status: "running", Running: 1, Replicas: 1}},
	})
	if strings.Contains(out, "hidden by the terminal height") {
		t.Errorf("frame claims hidden rows on a terminal with room for all of them:\n%s", out)
	}
}

// Every line is clipped to the terminal width. An over-long line wraps, and a
// wrapped line pushes the whole frame down by one row every repaint, which
// scrolls the table off the screen.
func TestRenderTop_ClipsEveryLineToTheWidth(t *testing.T) {
	out := renderToString(topView{
		Host: strings.Repeat("very-long-host.", 10), At: time.Unix(0, 0).UTC(),
		Sort:  topSortCPU,
		Width: 40,
		Rows:  []topRow{{Slug: strings.Repeat("long-slug-", 8), Status: "running", Running: 1, Replicas: 1}},
	})
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if w := visibleWidth(line); w > 40 {
			t.Errorf("line is %d columns wide on a 40-column terminal: %q", w, line)
		}
	}
}

// A clipped value must look clipped. Cutting "payments-prod" to "payments" at
// the column boundary produces a name that could plausibly be a different app.
func TestTruncateVisible_MarksTheCut(t *testing.T) {
	got := truncateVisible("payments-prod", 9, "…")
	if got != "payments…" {
		t.Errorf("got %q, want %q", got, "payments…")
	}
	if untouched := truncateVisible("short", 40, "…"); untouched != "short" {
		t.Errorf("a line that fits was altered: %q", untouched)
	}
	if unbounded := truncateVisible("anything at all", 0, "…"); unbounded != "anything at all" {
		t.Errorf("width 0 means unbounded, got %q", unbounded)
	}
}

// Color occupies no columns. Measuring the escapes would clip a line that fits,
// and cutting inside a colored run without closing it bleeds that color across
// the rest of the frame.
func TestTruncateVisible_IgnoresColorAndClosesIt(t *testing.T) {
	painted := ansiRed + "abcdefgh" + ansiReset
	if w := visibleWidth(painted); w != 8 {
		t.Errorf("visibleWidth = %d, want 8: escapes must not count as columns", w)
	}
	if got := truncateVisible(painted, 8, "…"); got != painted {
		t.Errorf("a painted line that fits was clipped: %q", got)
	}

	cut := truncateVisible(ansiRed+"abcdefgh", 4, "…")
	if visibleWidth(cut) != 4 {
		t.Errorf("clipped painted line is %d columns, want 4: %q", visibleWidth(cut), cut)
	}
	if !strings.HasSuffix(cut, ansiReset) {
		t.Errorf("clipped painted line does not close its color, so it bleeds into the "+
			"rest of the frame: %q", cut)
	}
}

// In a non-UTF-8 locale every marker has an ASCII form. A terminal that renders
// "≥" as a replacement box turns a lower bound into a mystery character.
func TestTopMarkers_HaveAnASCIIForm(t *testing.T) {
	ascii := styler{ascii: true}
	if got := topAtLeastMark(ascii); got != ">=" {
		t.Errorf("ascii at-least marker = %q, want >=", got)
	}
	if got := topCPUText(ascii, f64(5), true); got != ">=5.0" {
		t.Errorf("ascii cpu text = %q, want >=5.0", got)
	}
	if got := ascii.glyphEllipsis(); got != "~" {
		t.Errorf("ascii ellipsis = %q, want ~", got)
	}
	if got := topAtLeastMark(styler{}); got != "≥" {
		t.Errorf("utf-8 at-least marker = %q, want ≥", got)
	}
}

// The JSON form carries the same absent/partial distinction as the table. A
// consumer that reads 0 where the CLI shows "-" would alert on an idle app or,
// worse, stay quiet about a busy one.
func TestTopItems_AbsentFiguresEncodeAsNullNotZero(t *testing.T) {
	items := topItems([]topRow{{Slug: "a", Status: "running", Running: 1, Replicas: 1, CPUPartial: true}})
	item := items[0]

	if item["cpu_percent"] != (*float64)(nil) {
		t.Errorf("cpu_percent = %#v, want a nil pointer so it encodes as null", item["cpu_percent"])
	}
	if item["rss_bytes"] != (*int64)(nil) {
		t.Errorf("rss_bytes = %#v, want a nil pointer", item["rss_bytes"])
	}
	if item["cpu_percent_partial"] != true {
		t.Errorf("cpu_percent_partial = %v, want true", item["cpu_percent_partial"])
	}
	if item["sessions_ceiling"] != 0 {
		t.Errorf("sessions_ceiling = %v, want 0 for an uncapped app", item["sessions_ceiling"])
	}
}

// The --sort vocabulary the schema publishes is the vocabulary the parser
// accepts. A value the schema advertises and the CLI rejects is a contract the
// agent-facing consumer cannot rely on.
func TestParseTopSort_AcceptsEveryPublishedValueAndNamesTheRest(t *testing.T) {
	for _, v := range topSortValues {
		if _, err := parseTopSort(v); err != nil {
			t.Errorf("parseTopSort(%q) rejected a value the schema publishes: %v", v, err)
		}
	}
	_, err := parseTopSort("ram")
	if err == nil {
		t.Fatal("parseTopSort accepted an unknown column")
	}
	if kind, _ := classify(err); kind != KindValidation {
		t.Errorf("kind = %v, want %v: a bad flag value is the caller's mistake, not the server's",
			kind, KindValidation)
	}
	// The hint has to name the alternatives. "unknown sort column" alone leaves
	// the operator guessing at a vocabulary the CLI already knows.
	hint := hintOf(err)
	for _, v := range topSortValues {
		if !strings.Contains(hint, v) {
			t.Errorf("the hint for an unknown column does not list %q: %s", v, hint)
		}
	}
}

func slugsOf(rows []topRow) string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Slug)
	}
	return strings.Join(out, ",")
}

func i64(v int64) *int64 { return &v }
