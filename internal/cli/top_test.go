package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTopServer answers /api/apps/metrics with a fleet that exercises every
// distinction the view has to preserve: an app whose replicas all reported, an
// app one running replica short of a complete reading, and an app the sampler
// could not read at all.
func newTopServer(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()
	body := map[string]any{
		"metrics": map[string]any{
			"busy": map[string]any{
				"status":       "running",
				"sessions_cap": 5,
				"replicas": []map[string]any{
					{"status": "running", "cpu_percent": 40.0, "rss_bytes": 200_000_000,
						"sessions": 3, "metrics_available": true},
					{"status": "running", "cpu_percent": 20.0, "rss_bytes": 100_000_000,
						"sessions": 2, "metrics_available": true},
				},
			},
			"halfread": map[string]any{
				"status": "running",
				"replicas": []map[string]any{
					{"status": "running", "cpu_percent": 1.0, "rss_bytes": 10_000_000,
						"sessions": 1, "metrics_available": true},
					{"status": "running", "sessions": 0, "metrics_available": false},
				},
			},
			"dark": map[string]any{
				"status": "running",
				"replicas": []map[string]any{
					{"status": "running", "sessions": -1, "metrics_available": false},
				},
			},
		},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		if r.URL.Path != "/api/apps/metrics" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func runTopCLI(t *testing.T, srv *httptest.Server, args ...string) (string, error) {
	t.Helper()
	resetFormatState(t)
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")
	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"top"}, args...))
	err := root.Execute()
	return out.String(), err
}

// Off a terminal there is nothing to repaint, so top must behave like every
// other read command: one document, then exit. A command that blocked here
// would hang any script that ran it.
func TestTop_PipedOutputIsASingleSnapshot(t *testing.T) {
	var hits int32
	srv := newTopServer(t, &hits)
	defer srv.Close()

	done := make(chan struct{})
	var out string
	var err error
	go func() {
		defer close(done)
		// No flags at all: this is the invocation a script writes, and the one
		// that would hang forever if the live view were chosen off a terminal.
		out, err = runTopCLI(t, srv)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("top did not return off a terminal; a piped run must print once and exit")
	}
	if err != nil {
		t.Fatalf("top failed: %v\n%s", err, out)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("server was polled %d times, want 1: a one-shot run must not loop", n)
	}
	if !json.Valid([]byte(out)) {
		t.Errorf("the default off a terminal is not the machine-readable form:\n%s", out)
	}
}

// The JSON document is what a script alerts on, so every distinction the table
// draws has to survive into it: a summed figure, a figure that is only a floor,
// and a figure that does not exist.
func TestTop_JSONKeepsAbsentAndPartialApart(t *testing.T) {
	srv := newTopServer(t, nil)
	defer srv.Close()

	out, err := runTopCLI(t, srv, "--output", "json")
	if err != nil {
		t.Fatalf("top failed: %v\n%s", err, out)
	}

	var env struct {
		Items  []map[string]any `json:"items"`
		Total  int              `json:"total"`
		Host   string           `json:"host"`
		At     string           `json:"captured_at"`
		Totals map[string]any   `json:"totals"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("not the standard envelope: %s", out)
	}
	if env.Total != 3 || len(env.Items) != 3 {
		t.Fatalf("total=%d items=%d, want 3 and 3", env.Total, len(env.Items))
	}
	if env.Host != srv.URL {
		t.Errorf("host = %q, want %q: a stored snapshot must say which server it came from", env.Host, srv.URL)
	}
	if _, err := time.Parse(time.RFC3339, env.At); err != nil {
		t.Errorf("captured_at %q is not RFC3339: %v", env.At, err)
	}

	byslug := map[string]map[string]any{}
	for _, it := range env.Items {
		byslug[fmt.Sprint(it["slug"])] = it
	}

	busy := byslug["busy"]
	if busy["cpu_percent"] != 60.0 {
		t.Errorf("busy cpu_percent = %v, want 60 (both replicas summed)", busy["cpu_percent"])
	}
	if busy["cpu_percent_partial"] != false {
		t.Errorf("busy marked partial although both replicas reported")
	}
	if busy["sessions"] != 5.0 || busy["sessions_ceiling"] != 10.0 {
		t.Errorf("busy sessions = %v/%v, want 5/10 (2 replicas x cap 5)",
			busy["sessions"], busy["sessions_ceiling"])
	}

	half := byslug["halfread"]
	if half["cpu_percent"] != 1.0 {
		t.Errorf("halfread cpu_percent = %v, want the 1 that was measured", half["cpu_percent"])
	}
	if half["cpu_percent_partial"] != true || half["rss_bytes_partial"] != true {
		t.Errorf("halfread does not report its figures as floors: %v", half)
	}

	dark := byslug["dark"]
	if dark["cpu_percent"] != nil || dark["rss_bytes"] != nil || dark["sessions"] != nil {
		t.Errorf("an app that reported nothing encoded as a number rather than null: %v", dark)
	}

	if env.Totals["cpu_percent"] != 61.0 {
		t.Errorf("total cpu = %v, want 61", env.Totals["cpu_percent"])
	}
	if env.Totals["cpu_percent_partial"] != true {
		t.Errorf("the fleet total is missing two running apps' contributions but is not "+
			"marked as a floor: %v", env.Totals)
	}
}

// --output table off a terminal is the form an operator reads through `less` or
// finds in a CI log. It must carry the same absent marker as the JSON, and no
// escape sequences: a pipe cannot interpret them.
func TestTop_PipedTableIsPlainAndMarksAbsentFigures(t *testing.T) {
	srv := newTopServer(t, nil)
	defer srv.Close()

	out, err := runTopCLI(t, srv, "--output", "table")
	if err != nil {
		t.Fatalf("top failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("piped output carries ANSI escapes:\n%q", out)
	}
	if !strings.Contains(out, "SESSIONS") {
		t.Errorf("table header missing:\n%s", out)
	}
	for _, slug := range []string{"busy", "halfread", "dark"} {
		if !strings.Contains(out, slug) {
			t.Errorf("app %q is missing from the table:\n%s", slug, out)
		}
	}
	if !strings.Contains(out, "≥") {
		t.Errorf("the half-read app's figures are shown as complete:\n%s", out)
	}
	if strings.Contains(out, "q quit") {
		t.Errorf("a piped snapshot advertises keys it will never read:\n%s", out)
	}
}

// --limit pages the same ordering the table shows, so the JSON and the table
// agree on which apps were dropped. total stays the full count: paging must not
// make the fleet look smaller than it is.
func TestTop_LimitPagesTheSortedOrderAndKeepsTheRealTotal(t *testing.T) {
	srv := newTopServer(t, nil)
	defer srv.Close()

	out, err := runTopCLI(t, srv, "--output", "json", "--limit", "1")
	if err != nil {
		t.Fatalf("top failed: %v\n%s", err, out)
	}
	var env struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("not the standard envelope: %s", out)
	}
	if env.Total != 3 {
		t.Errorf("total = %d, want 3: --limit must not shrink the reported fleet", env.Total)
	}
	if len(env.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(env.Items))
	}
	if got := fmt.Sprint(env.Items[0]["slug"]); got != "busy" {
		t.Errorf("first item = %q, want busy: --limit 1 on a cpu sort must keep the "+
			"busiest app, not an arbitrary one", got)
	}

	table, err := runTopCLI(t, srv, "--output", "table", "--limit", "1")
	if err != nil {
		t.Fatalf("top table failed: %v\n%s", err, table)
	}
	if strings.Contains(table, "halfread") {
		t.Errorf("the table shows an app the JSON paged out, so the two forms disagree "+
			"about the same window:\n%s", table)
	}
}

// A flag value the CLI cannot honour is rejected before the request. Accepting
// it and silently ignoring it inside the refresh loop would leave a flag that
// appears to work and never does.
func TestTop_BadFlagsAreRejectedBeforeAnyRequest(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"unknown sort column", []string{"--sort", "ram"}},
		{"interval below the floor", []string{"--interval", "100ms"}},
		{"negative limit", []string{"--limit", "-1"}},
		{"negative offset", []string{"--offset", "-1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var hits int32
			srv := newTopServer(t, &hits)
			defer srv.Close()

			out, err := runTopCLI(t, srv, c.args...)
			if err == nil {
				t.Fatalf("accepted %v:\n%s", c.args, out)
			}
			if kind, code := classify(err); kind != KindValidation || code != 1 {
				t.Errorf("classified as %v/%d, want %v/1: a bad flag is the caller's mistake",
					kind, code, KindValidation)
			}
			if n := atomic.LoadInt32(&hits); n != 0 {
				t.Errorf("the server was polled %d times for a request that could never be "+
					"rendered", n)
			}
		})
	}
}

// The refusal has to name the number it refused and the floor it fell under, or
// the operator has to guess what to pass instead.
func TestTop_IntervalRefusalNamesTheFloor(t *testing.T) {
	srv := newTopServer(t, nil)
	defer srv.Close()

	_, err := runTopCLI(t, srv, "--interval", "100ms")
	if err == nil {
		t.Fatal("accepted an interval below the floor")
	}
	if !strings.Contains(err.Error(), "100ms") || !strings.Contains(err.Error(), "1s") {
		t.Errorf("the refusal names neither the value nor the floor: %v", err)
	}
	if hintOf(err) == "" {
		t.Error("the refusal offers no reason, so it reads as an arbitrary limit")
	}
}

// A credentials failure has to arrive as an auth failure, not as a generic one:
// exit code 3 is how a caller knows to re-login rather than retry.
func TestTop_ExpiredCredentialsClassifyAsAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	_, err := runTopCLI(t, srv, "--output", "json")
	if err == nil {
		t.Fatal("a 401 was not reported as a failure")
	}
	if kind, code := classify(err); kind != KindAuth || code != 3 {
		t.Errorf("classified as %v/%d, want %v/3", kind, code, KindAuth)
	}
}

// An account with nothing to watch gets an empty document, not an error: "no
// apps" is an answer, and a script that treats it as a failure would page
// someone at three in the morning over a correctly empty server.
func TestTop_EmptyFleetIsAnAnswerNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"metrics":{}}`))
	}))
	defer srv.Close()

	out, err := runTopCLI(t, srv, "--output", "json")
	if err != nil {
		t.Fatalf("an empty fleet was reported as a failure: %v", err)
	}
	var env struct {
		Items  []map[string]any `json:"items"`
		Total  int              `json:"total"`
		Totals map[string]any   `json:"totals"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("not the standard envelope: %s", out)
	}
	if env.Total != 0 || len(env.Items) != 0 {
		t.Errorf("total=%d items=%d, want 0 and 0", env.Total, len(env.Items))
	}
	if env.Totals["cpu_percent"] != nil {
		t.Errorf("an empty fleet reported a cpu figure (%v); there was nothing to measure",
			env.Totals["cpu_percent"])
	}
	if env.Totals["cpu_percent_partial"] != false {
		t.Error("an empty fleet was marked as an incomplete reading")
	}
}

// A monitor that quits the first time a refresh fails is worse than no monitor:
// the failure it exits on is usually a restart it was there to watch. Only an
// answer that will repeat forever is worth quitting over.
func TestTopFatal_QuitsOnlyWhenRetryingCannotHelp(t *testing.T) {
	cases := []struct {
		err  error
		want bool
		why  string
	}{
		{errors.New("connection refused"), false, "a transport failure may be gone by the next tick"},
		{&httpStatusError{Status: 502, msg: "bad gateway"}, false, "a restarting server answers again shortly"},
		{&httpStatusError{Status: 429, msg: "slow down"}, false, "backing off is the answer, not exiting"},
		{&httpStatusError{Status: 408, msg: "timeout"}, false, "a timed-out request can simply be retried"},
		{&httpStatusError{Status: 401, msg: "unauthorized"}, true, "the token will be rejected identically forever"},
		{&httpStatusError{Status: 403, msg: "forbidden"}, true, "the answer cannot change without a new grant"},
		{&httpStatusError{Status: 404, msg: "no such endpoint"}, true, "the endpoint will not appear on its own"},
	}
	for _, c := range cases {
		if got := topFatal(c.err); got != c.want {
			t.Errorf("topFatal(%v) = %v, want %v: %s", c.err, got, c.want, c.why)
		}
	}
}

// The window guard belongs to the command, not to the table renderer, so a
// value that cannot be honoured never reaches a frame. topWindowOf itself must
// be total: with the guard passed, no offset can panic it, and the live view
// re-windows on every tick against a fleet that can shrink under it.
func TestTopWindow_ClampsRatherThanPanics(t *testing.T) {
	rows := []topRow{{Slug: "a"}, {Slug: "b"}, {Slug: "c"}}
	if got := topWindowOf(rows, 0, 99); len(got) != 0 {
		t.Errorf("offset past the end returned %d rows, want 0", len(got))
	}
	if got := topWindowOf(rows, 99, 0); len(got) != 3 {
		t.Errorf("limit past the end returned %d rows, want 3", len(got))
	}
	if got := topWindowOf(rows, 1, 1); len(got) != 1 || got[0].Slug != "b" {
		t.Errorf("offset 1 limit 1 = %v, want [b]", slugsOf(got))
	}
	if got := topWindowOf(rows, 0, 0); len(got) != 3 {
		t.Errorf("an unset window returned %d rows, want all 3", len(got))
	}
}

func TestTopLiveState_NavigatesFiltersPausesAndSorts(t *testing.T) {
	rows := []topRow{{Slug: "alpha", Status: "running"}, {Slug: "beta", Status: "stopped"}, {Slug: "gamma", Status: "running"}}
	f := &topFlags{}
	state := topLiveState{by: topSortCPU}
	state.ensureSelection(rows, f)
	if state.selected != "alpha" {
		t.Fatalf("initial selection = %q, want alpha", state.selected)
	}

	state.handleKey(topKey{kind: topKeyDown}, rows, f, 2)
	if state.selected != "beta" {
		t.Errorf("Down selected %q, want beta", state.selected)
	}
	state.handleKey(topKey{kind: topKeyPageDown}, rows, f, 2)
	if state.selected != "gamma" {
		t.Errorf("PageDown selected %q, want gamma", state.selected)
	}

	state.handleKey(topKey{kind: topKeyRune, b: '/'}, rows, f, 2)
	state.handleKey(topKey{kind: topKeyRune, b: 'b'}, rows, f, 2)
	state.handleKey(topKey{kind: topKeyRune, b: 'e'}, rows, f, 2)
	state.handleKey(topKey{kind: topKeyRune, b: '\r'}, rows, f, 2)
	if state.filter != "be" || state.filtering || state.selected != "beta" {
		t.Errorf("filter state = filter:%q editing:%v selected:%q, want be/false/beta",
			state.filter, state.filtering, state.selected)
	}
	state.handleKey(topKey{kind: topKeyEscape}, rows, f, 2)
	if state.filter != "" {
		t.Errorf("Escape left filter %q, want it cleared", state.filter)
	}

	state.handleKey(topKey{kind: topKeyRune, b: ' '}, rows, f, 2)
	if !state.paused {
		t.Error("Space did not pause refreshes")
	}
	state.handleKey(topKey{kind: topKeyRune, b: '\t'}, rows, f, 2)
	if state.by != topSortMemory {
		t.Errorf("Tab changed sort to %q, want mem", state.by)
	}
	state.handleKey(topKey{kind: topKeyRune, b: '?'}, rows, f, 2)
	if !state.help {
		t.Error("? did not open help")
	}
	state.handleKey(topKey{kind: topKeyEscape}, rows, f, 2)
	if state.help {
		t.Error("Escape did not close help")
	}
}

func TestDecodeTopEscape_RecognizesNavigationKeys(t *testing.T) {
	cases := map[string]topKeyKind{
		"\x1b[A":  topKeyUp,
		"\x1b[B":  topKeyDown,
		"\x1b[5~": topKeyPageUp,
		"\x1b[6~": topKeyPageDown,
		"\x1b[H":  topKeyHome,
		"\x1b[F":  topKeyEnd,
	}
	for seq, want := range cases {
		got, complete, _ := decodeTopEscape(seq)
		if !complete || got.kind != want {
			t.Errorf("decodeTopEscape(%q) = kind %v complete %v, want %v/true",
				seq, got.kind, complete, want)
		}
	}
	if _, complete, prefix := decodeTopEscape("\x1b["); complete || !prefix {
		t.Errorf("partial CSI reported complete=%v prefix=%v, want false/true", complete, prefix)
	}
}
