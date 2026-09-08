package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/fleet"
)

func testFleetDisplay(out io.Writer, slugs ...string) *fleetLiveDisplay {
	d := &fleetLiveDisplay{out: out, style: styler{tty: true, redraw: true, color: true}, started: time.Unix(0, 0), width: 100, height: 40, live: true}
	for _, slug := range slugs {
		d.rows = append(d.rows, fleetProgressRow{slug: slug, phase: "Queued"})
	}
	return d
}

func TestFleetDisplayStableRowsAndOutcomes(t *testing.T) {
	d := testFleetDisplay(io.Discard, "alpha-dashboard", "beta-dashboard", "gamma-dashboard")
	start := d.started
	d.rows[0] = fleetProgressRow{slug: d.rows[0].slug, phase: "Refreshed", started: start, finished: start.Add(42 * time.Second), status: statusUnchanged}
	d.rows[1] = fleetProgressRow{slug: d.rows[1].slug, phase: "Refreshing data", detail: "refresh-data · run #449", started: start, deadline: start.Add(15 * time.Minute)}
	d.rows[2] = fleetProgressRow{slug: d.rows[2].slug, phase: "Waiting for healthy status", detail: "Degraded", warning: true, started: start, deadline: start.Add(15 * time.Minute)}
	rendered := strings.Join(d.lines(start.Add(52*time.Second)), "\n")
	plain := stripANSI(rendered)
	t.Log("\n" + plain)
	for _, want := range []string{"1 of 3 complete", "✓ alpha-dashboard", "42s", "⠋ beta-dashboard", "Refreshing data", "run #449", "Degraded · timeout in 14m08s"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q:\n%s", want, plain)
		}
	}
	if !strings.Contains(rendered, ansiGreen+"✓") || !strings.Contains(rendered, ansiYellow+"Waiting for healthy status") {
		t.Fatalf("missing outcome colors: %q", rendered)
	}
	d.rows[2].finished, d.rows[2].status, d.rows[2].phase = start.Add(time.Minute), statusFailed, "failed"
	final := stripANSI(strings.Join(d.lines(start.Add(time.Minute)), "\n"))
	if !strings.Contains(final, "1 failed") || !strings.Contains(final, "✗ gamma-dashboard") {
		t.Fatal(final)
	}
	if strings.Index(final, "alpha-dashboard") > strings.Index(final, "beta-dashboard") {
		t.Fatal("rows reordered")
	}
}

func TestFleetDisplayClipsNarrowAndWideText(t *testing.T) {
	d := testFleetDisplay(io.Discard, "a-very-long-application-name-that-does-not-fit")
	d.width = 50
	d.rows[0].started = d.started
	d.rows[0].phase = "Waiting for healthy status"
	d.rows[0].detail = strings.Repeat("数据", 40)
	for _, line := range d.lines(d.started.Add(time.Minute)) {
		if fleetVisibleWidth(line) >= d.width {
			t.Fatalf("row wraps (%d): %q", fleetVisibleWidth(line), line)
		}
	}
	if got := stripANSI(fleetClip(ansiRed+"数据数据"+ansiReset, 5, "…")); got != "数据…" {
		t.Fatalf("wide clipping = %q", got)
	}
	d.style.ascii = true
	ascii := stripANSI(strings.Join(d.lines(d.started.Add(time.Minute)), "\n"))
	if strings.ContainsAny(ascii, "⠋…·") {
		t.Fatalf("ASCII contains decorations: %s", ascii)
	}
	if strings.ContainsAny(liveText("hello\n\x1b\rworld"), "\n\x1b\r") {
		t.Fatal("controls preserved")
	}
}

func TestFleetDisplayLogsResizeAndShutdown(t *testing.T) {
	var out bytes.Buffer
	d := testFleetDisplay(&out, "demo")
	d.started = time.Now()
	d.start()
	w, finish := beginFleetApp(d, "demo")
	if !updateFleetProgress(&resultWarningWriter{Writer: w}, "Refreshing data", "run #42", time.Now().Add(time.Minute), false) {
		t.Fatal("wrapper lost live updates")
	}
	fmt.Fprintln(w, "demo: warning: server advisory")
	finish(applyResult{status: statusFailed, err: errors.New("connection lost")})
	d.close()
	select {
	case <-d.stopped:
	default:
		t.Fatal("animation outlived close")
	}
	text := out.String()
	warning := strings.Index(text, "demo: warning: server advisory")
	final := strings.LastIndex(text, "Fleet finished")
	if warning < 0 || final < warning || !strings.Contains(text[final:], "connection lost") {
		t.Fatalf("lost diagnostic/final frame: %q", text)
	}
	if strings.Contains(text[final:], "⠋") {
		t.Fatal("finished row still spinning")
	}
	if updateFleetProgress(w, "Late update", "", time.Time{}, false) {
		t.Fatal("closed display accepted live update")
	}

	var resized bytes.Buffer
	d = testFleetDisplay(&resized, "demo")
	d.drawLocked(d.started)
	resized.Reset()
	d.size = func() (int, int, error) { return 50, 20, nil }
	fmt.Fprintln(d, "demo: warning during resize")
	d.drawLocked(d.started)
	if d.live || strings.Contains(resized.String(), "\x1b") {
		t.Fatalf("resize attempted unsafe cursor movement: %q", resized.String())
	}
	fmt.Fprintln(d, "demo: durable event")
	if !strings.HasSuffix(resized.String(), "demo: durable event\n") {
		t.Fatal(resized.String())
	}
}

func TestFleetDisplayConvergenceSerialAndParallel(t *testing.T) {
	for _, concurrency := range []int{1, 3} {
		t.Run(fmt.Sprint(concurrency), func(t *testing.T) {
			const n = 3
			var maxInflight atomic.Int32
			srv := concurrencyTestServer(t, n, &maxInflight, 120*time.Millisecond)
			dir := t.TempDir()
			mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
			pf := buildConcurrencyPreflight(n, dir)
			entries := map[string]fleet.AppEntry{}
			var slugs []string
			for _, entry := range pf.manifest.Apps {
				entries[entry.Slug] = entry
				slugs = append(slugs, entry.Slug)
			}
			var out bytes.Buffer
			d := testFleetDisplay(&out, slugs...)
			d.started = time.Now()
			d.start()
			opt := convergeOpts{preconditions: true, concurrency: concurrency, fleetID: "eu"}
			var results []applyResult
			if concurrency == 1 {
				results = convergeSerial(&cliConfig{Host: srv.URL}, pf, entries, opt, "fleet:eu", d)
			} else {
				results = convergeParallel(&cliConfig{Host: srv.URL}, pf, entries, opt, "fleet:eu", d)
			}
			d.close()
			for i, result := range results {
				if result.err != nil || d.rows[i].status != statusUpdated || d.rows[i].finished.IsZero() {
					t.Fatalf("row %d: result=%+v row=%+v", i, result, d.rows[i])
				}
			}
			if !strings.Contains(out.String(), "Fleet finished") {
				t.Fatal("missing settled display")
			}
		})
	}
}

func TestFleetDisplayRunWaitKeepsIdentityWithoutLogSpam(t *testing.T) {
	var out bytes.Buffer
	d := testFleetDisplay(&out, "demo")
	w, finish := beginFleetApp(d, "demo")
	now := time.Unix(0, 0)
	calls := 0
	status, err := waitForDeployRunLoop(func() (string, error) {
		calls++
		if calls == 4 {
			return "succeeded", nil
		}
		return "running", nil
	}, time.Minute, time.Second, time.Second, func() time.Time { return now }, func(delay time.Duration) { now = now.Add(delay) },
		w, "demo/refresh-data", runWaitPresentation{phase: "Refreshing data", detail: "refresh-data · run #449"})
	if err != nil || status != "succeeded" {
		t.Fatalf("status=%s err=%v", status, err)
	}
	if d.rows[0].phase != "Schedule succeeded" || d.rows[0].detail != "refresh-data · run #449" || !d.rows[0].deadline.IsZero() {
		t.Fatalf("lost phase/run: %+v", d.rows[0])
	}
	if out.Len() != 0 {
		t.Fatalf("live polling leaked event spam: %q", out.String())
	}
	finish(applyResult{status: statusUnchanged, scheduleRefreshes: []scheduleRefreshOutcome{{RunID: 449, Status: "succeeded"}}})
	if d.rows[0].phase != "Refreshed" {
		t.Fatalf("missing completion: %+v", d.rows[0])
	}
}

func TestFleetDisplayClipsWholeEmojiClusters(t *testing.T) {
	for _, cluster := range []string{"❤️", "🏳️‍🌈", "🇳🇱", "👨‍👩‍👧‍👦", "👍🏽"} {
		if got := fleetVisibleWidth(cluster); got != 2 {
			t.Fatalf("width(%q)=%d, want 2", cluster, got)
		}
		text := strings.Repeat(cluster, 30)
		if got, want := stripANSI(fleetClip(ansiRed+text+ansiReset, 5, "…")), cluster+cluster+"…"; got != want {
			t.Fatalf("clip(%q)=%q, want %q", cluster, got, want)
		}
		d := testFleetDisplay(io.Discard, "demo")
		d.width = 50
		d.rows[0].detail = text
		lines := d.lines(d.started)
		if got, want := stripANSI(lines[3]), "    "+strings.Repeat(cluster, 22)+"…"; got != want {
			t.Fatalf("detail wraps or splits emoji: %q, want %q", got, want)
		}
	}
	if got := fleetClip("e\u0301e\u0301e\u0301", 2, "…"); got != "e\u0301…" {
		t.Fatalf("split combining sequence: %q", got)
	}
}

func TestFleetDisplayResizeBeforeCompletionUsesEventLog(t *testing.T) {
	var out bytes.Buffer
	d := testFleetDisplay(&out, "demo")
	w, _ := beginFleetApp(d, "demo")
	d.drawLocked(d.started)
	out.Reset()
	d.size = func() (int, int, error) { return 60, 20, nil }
	if !updateFleetProgress(w, "Healthy", "", time.Time{}, false) {
		fmt.Fprintln(w, "demo: healthy")
	}
	if d.live || !strings.Contains(out.String(), "demo: healthy\n") || strings.Contains(out.String(), "\x1b") {
		t.Fatalf("completion lost after resize: %q", out.String())
	}
}

func TestFleetDisplayParkedAndStoppedClearPreviousHealthState(t *testing.T) {
	for _, status := range []string{"stopped", "hibernated", "suspended"} {
		t.Run(status, func(t *testing.T) {
			d := testFleetDisplay(io.Discard, "demo")
			w, finish := beginFleetApp(d, "demo")
			now := time.Unix(0, 0)
			calls := 0
			err := waitForFleetHealthLoop("demo", time.Minute, time.Second, 15*time.Second, func() (bool, string, error) {
				calls++
				if calls == 1 {
					return false, "degraded", nil
				}
				return true, status, nil
			}, func() time.Time { return now }, func(delay time.Duration) { now = now.Add(delay) }, w)
			if err != nil {
				t.Fatal(err)
			}
			finish(applyResult{status: statusUnchanged})
			want := "Parked"
			if status == "stopped" {
				want = "Stopped"
			}
			row := d.rows[0]
			if row.phase != want || !row.deadline.IsZero() || strings.Contains(row.detail, "degraded") {
				t.Fatalf("stale final health state: %+v", row)
			}
		})
	}
}
