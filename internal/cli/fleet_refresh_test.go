package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/fleet"
)

func TestFleetRefreshUnchangedWaitsExactRunAndVerifies(t *testing.T) {
	for _, disposition := range []string{"started", "joined"} {
		t.Run(disposition, func(t *testing.T) {
			var posts, lists, polls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/apps/a":
					fmt.Fprint(w, `{"compatibility_quarantined":false,"producer_repair_required":false}`)
				case "/api/apps/a/schedules":
					lists++
					fmt.Fprintf(w, `{"items":[{"id":7,"name":"refresh-data","enabled":true,"deploy_trigger":"never","stale":%t,"last_run_id":999}]}`, lists == 1)
				case "/api/apps/a/schedules/7/refresh-stale":
					posts++
					fmt.Fprintf(w, `{"schedule_id":7,"schedule":"refresh-data","status":%q,"run_id":42}`, disposition)
				case "/api/apps/a/schedules/7/runs/42":
					polls++
					fmt.Fprint(w, `{"status":"succeeded"}`)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			var out bytes.Buffer
			res := convergeApp(&cliConfig{Host: srv.URL}, fleet.AppDiff{Slug: "a", Action: fleet.ActionUnchanged}, fleet.AppEntry{Slug: "a"}, fleet.ObservedApp{}, "", convergeOpts{refreshStale: true, warmTimeout: time.Second}, "", &out)
			if res.status != statusUnchanged || res.err != nil || posts != 1 || polls != 1 || lists != 2 {
				t.Fatalf("result=%+v requests post=%d poll=%d lists=%d", res, posts, polls, lists)
			}
			for _, want := range []string{
				"a/refresh-data: refresh " + disposition + " (run #42)",
				"a/refresh-data: succeeded (run #42,",
			} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q in progress:\n%s", want, out.String())
				}
			}

			wantMutation := mutationNone
			if disposition == "started" {
				wantMutation = mutationCommitted
			}
			if res.mutation != wantMutation {
				t.Fatalf("mutation=%s want %s", res.mutation, wantMutation)
			}
			if len(res.scheduleRefreshes) != 1 || res.scheduleRefreshes[0].Status != "succeeded" || res.scheduleRefreshes[0].RunID != 42 {
				t.Fatalf("outcomes=%+v", res.scheduleRefreshes)
			}
		})
	}
}

func TestFleetRefreshFreshDisabledAndUnknownState(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantErr    bool
	}{
		{"fresh", `[{"id":7,"name":"refresh","enabled":true,"stale":false}]`, false},
		{"disabled", `[{"id":7,"name":"refresh","enabled":false}]`, false},
		{"unknown", `[{"id":7,"name":"refresh","enabled":true}]`, true},
		{"validate_all_before_start", `[{"id":7,"name":"first","enabled":true,"stale":true},{"id":8,"name":"unknown","enabled":true}]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("unexpected producer request %s", r.Method)
				}
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			res := applyResult{mutation: mutationNone}
			err := refreshStaleSchedules(&cliConfig{Host: srv.URL}, "a", &res, time.Second, io.Discard)
			if (err != nil) != tc.wantErr || res.mutation != mutationNone {
				t.Fatalf("err=%v result=%+v", err, res)
			}
		})
	}
}

func TestFleetRefreshAdmissionNoops(t *testing.T) {
	for _, disposition := range []string{"fresh", "disabled"} {
		t.Run(disposition, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					fmt.Fprint(w, `[{"id":7,"name":"refresh","enabled":true,"stale":true}]`)
				case http.MethodPost:
					fmt.Fprintf(w, `{"schedule_id":7,"schedule":"refresh","status":%q}`, disposition)
				}
			}))
			defer srv.Close()
			res := applyResult{mutation: mutationNone}
			if err := refreshStaleSchedules(&cliConfig{Host: srv.URL}, "a", &res, time.Second, io.Discard); err != nil {
				t.Fatal(err)
			}
			if res.mutation != mutationNone || len(res.scheduleRefreshes) != 1 || res.scheduleRefreshes[0].Disposition != disposition {
				t.Fatalf("result=%+v", res)
			}
		})
	}
}

func TestFleetRefreshFailureStopsAppAndRetainsExactLogs(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps/a/schedules":
			fmt.Fprint(w, `[{"id":7,"name":"first","enabled":true,"stale":true},{"id":8,"name":"second","enabled":true,"stale":true}]`)
		case "/api/apps/a/schedules/7/refresh-stale":
			posts++
			fmt.Fprint(w, `{"schedule_id":7,"schedule":"first","status":"started","run_id":42}`)
		case "/api/apps/a/schedules/7/runs/42":
			fmt.Fprint(w, `{"status":"failed"}`)
		case "/api/apps/a/schedules/7/runs/42/logs":
			fmt.Fprint(w, "producer traceback\n")
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	res := convergeApp(&cliConfig{Host: srv.URL}, fleet.AppDiff{Slug: "a", Action: fleet.ActionUnchanged}, fleet.AppEntry{Slug: "a"}, fleet.ObservedApp{}, "", convergeOpts{refreshStale: true, retries: 3, warmTimeout: time.Second}, "", io.Discard)
	if res.status != statusFailed || res.failureKind != failureScheduleRefreshFailed || res.mutation != mutationPartial || posts != 1 {
		t.Fatalf("result=%+v posts=%d", res, posts)
	}
	if len(res.scheduleLogs) != 1 || res.scheduleLogs[0].RunID != 42 || len(res.scheduleLogs[0].Tail) != 1 {
		t.Fatalf("logs=%+v", res.scheduleLogs)
	}
}

func TestFleetRefreshAmbiguousAdmissionIsNeverRetried(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       int
	}{
		{"malformed", "{", 200}, {"missing_id", `{"schedule_id":7,"schedule":"refresh","status":"started"}`, 202},
		{"wrong_schedule", `{"schedule_id":8,"schedule":"refresh","status":"started","run_id":42}`, 202},
		{"server_error", `{"error":"storage failed"}`, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					fmt.Fprint(w, `[{"id":7,"name":"refresh","enabled":true,"stale":true}]`)
					return
				}
				posts++
				w.WriteHeader(tc.code)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			res := applyResult{mutation: mutationNone}
			err := refreshStaleSchedules(&cliConfig{Host: srv.URL}, "a", &res, time.Second, io.Discard)
			if err == nil || posts != 1 || res.mutation != mutationUnknown {
				t.Fatalf("err=%v result=%+v posts=%d", err, res, posts)
			}
		})
	}
}

func TestFleetRefreshDeadlineBoundsAdmissionAndPolling(t *testing.T) {
	for _, phase := range []string{"admission", "poll"} {
		t.Run(phase, func(t *testing.T) {
			var posts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/apps/a/schedules":
					fmt.Fprint(w, `[{"id":7,"name":"refresh","enabled":true,"stale":true}]`)
				case "/api/apps/a/schedules/7/refresh-stale":
					posts.Add(1)
					if phase == "admission" {
						<-r.Context().Done()
						return
					}
					fmt.Fprint(w, `{"schedule_id":7,"schedule":"refresh","status":"started","run_id":42}`)
				case "/api/apps/a/schedules/7/runs/42":
					<-r.Context().Done()
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			res := applyResult{mutation: mutationNone, warmDeadline: time.Now().Add(60 * time.Millisecond)}
			start := time.Now()
			err := refreshStaleSchedules(&cliConfig{Host: srv.URL}, "a", &res, time.Hour, io.Discard)
			if err == nil || res.failureKind != failureScheduleRefreshTimeout || posts.Load() != 1 || time.Since(start) > time.Second {
				t.Fatalf("elapsed=%s err=%v result=%+v posts=%d", time.Since(start), err, res, posts.Load())
			}
			wantMutation := mutationPartial
			if phase == "admission" {
				wantMutation = mutationUnknown
			}
			if res.mutation != wantMutation {
				t.Fatalf("mutation=%s want %s", res.mutation, wantMutation)
			}
		})
	}
}

func TestFleetRefreshFlagCapabilityAndDryRun(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprint(dryRun), func(t *testing.T) {
			_, requests := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/server-info" {
					fmt.Fprint(w, `{"capabilities":{}}`)
					return
				}
				fmt.Fprint(w, `[]`)
			})
			dir := t.TempDir()
			mustWrite(t, dir+"/a/app.py", "print(1)\n")
			writeFleetManifest(t, dir, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./a\"\n")
			args := []string{"fleet", "apply", "--refresh-stale", "-f", dir + "/shinyhub-fleet.toml", "-o", "table"}
			if dryRun {
				args = append(args, "--dry-run")
			}
			out, err := execCLI(t, args...)
			if dryRun && err != nil {
				t.Fatalf("dry run error=%v out=%s", err, out)
			}
			if !dryRun && (err == nil || !strings.Contains(err.Error(), "does not support --refresh-stale")) {
				t.Fatalf("err=%v out=%s", err, out)
			}
			for _, r := range *requests {
				if r.Method != "GET" {
					t.Errorf("unsupported server/dry-run mutated: %s %s", r.Method, r.Path)
				}
			}
		})
	}
	f := &fleetApplyFlags{refreshStale: true, warmTimeout: 2 * time.Minute}
	if !strings.Contains(fleetApplyRecoveryCommand(f), "--refresh-stale") {
		t.Fatal("recovery lost refresh intent")
	}
}

func TestFleetRefreshCancelledContextNeverAdmits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer srv.Close()
	res := applyResult{mutation: mutationNone}
	if err := refreshStaleSchedulesContext(ctx, &cliConfig{Host: srv.URL}, "a", &res, time.Hour, io.Discard); err == nil {
		t.Fatal("expected cancellation")
	}
	if requests.Load() != 0 || res.mutation != mutationNone {
		t.Fatalf("requests=%d result=%+v", requests.Load(), res)
	}
}

func TestFleetRefreshChecksAppCompatibilityWithoutStaleSchedules(t *testing.T) {
	for _, body := range []string{`[]`, `[{"id":7,"name":"refresh","enabled":true,"stale":false}]`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("unexpected mutation %s %s", r.Method, r.URL.Path)
				}
				switch r.URL.Path {
				case "/api/apps/a/schedules":
					fmt.Fprint(w, body)
				case "/api/apps/a":
					fmt.Fprint(w, `{"compatibility_quarantined":true,"producer_repair_required":true}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			res := convergeApp(&cliConfig{Host: srv.URL}, fleet.AppDiff{Slug: "a", Action: fleet.ActionUnchanged}, fleet.AppEntry{Slug: "a"}, fleet.ObservedApp{}, "", convergeOpts{refreshStale: true, warmTimeout: time.Second}, "", io.Discard)
			if res.status != statusFailed || res.failureKind != failureScheduleProducer || res.mutation != mutationNone {
				t.Fatalf("result=%+v", res)
			}
		})
	}
}

func TestFleetRefreshRestartPrecedesFinalHealth(t *testing.T) {
	var sequence []string
	refreshed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence = append(sequence, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/apps/a/schedules":
			fmt.Fprintf(w, `[{"id":7,"name":"refresh","enabled":true,"deploy_trigger":"never","stale":%t}]`, !refreshed)
		case "/api/apps/a/schedules/7/refresh-stale":
			fmt.Fprint(w, `{"schedule_id":7,"schedule":"refresh","status":"started","run_id":42}`)
		case "/api/apps/a/schedules/7/runs/42":
			refreshed = true
			fmt.Fprint(w, `{"status":"succeeded"}`)
		case "/api/apps/a":
			fmt.Fprint(w, `{"app":{"status":"running"},"compatibility_quarantined":false,"producer_repair_required":false}`)
		case "/api/apps/a/restart":
			fmt.Fprint(w, `{"status":"running"}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	res := convergeApp(&cliConfig{Host: srv.URL}, fleet.AppDiff{Slug: "a", Action: fleet.ActionUnchanged}, fleet.AppEntry{Slug: "a"}, fleet.ObservedApp{}, "", convergeOpts{refreshStale: true, restartAfterWarm: true, verifyHealth: true, warmTimeout: time.Second, healthTimeout: time.Second}, "", io.Discard)
	if res.status != statusUnchanged || !res.warmRestarted {
		t.Fatalf("result=%+v requests=%v", res, sequence)
	}
	if len(sequence) < 2 || sequence[len(sequence)-2] != "POST /api/apps/a/restart" || sequence[len(sequence)-1] != "GET /api/apps/a" {
		t.Fatalf("health must follow restart: %v", sequence)
	}
}

func TestFleetRefreshCancellationDuringPollReturnsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps/a/schedules":
			fmt.Fprint(w, `[{"id":7,"name":"refresh","enabled":true,"stale":true}]`)
		case "/api/apps/a/schedules/7/refresh-stale":
			fmt.Fprint(w, `{"schedule_id":7,"schedule":"refresh","status":"started","run_id":42}`)
		case "/api/apps/a/schedules/7/runs/42":
			cancel()
			<-r.Context().Done()
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	res := applyResult{mutation: mutationNone}
	start := time.Now()
	err := refreshStaleSchedulesContext(ctx, &cliConfig{Host: srv.URL}, "a", &res, time.Minute, io.Discard)
	if err == nil || time.Since(start) > time.Second || res.mutation != mutationPartial {
		t.Fatalf("elapsed=%s err=%v result=%+v", time.Since(start), err, res)
	}
}

func TestFleetRefreshFinalVerificationSharesDeadline(t *testing.T) {
	lists := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/a/schedules" {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		lists++
		if lists == 2 {
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()
	start := time.Now()
	res := convergeApp(&cliConfig{Host: srv.URL}, fleet.AppDiff{Slug: "a", Action: fleet.ActionUnchanged}, fleet.AppEntry{Slug: "a"}, fleet.ObservedApp{}, "", convergeOpts{refreshStale: true, warmTimeout: 60 * time.Millisecond}, "", io.Discard)
	if res.status != statusFailed || res.failureKind != failureScheduleRefreshTimeout || time.Since(start) > time.Second {
		t.Fatalf("elapsed=%s result=%+v", time.Since(start), res)
	}
}

func TestFleetRefreshLogTimeoutPreservesProducerFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps/a/schedules":
			fmt.Fprint(w, `[{"id":7,"name":"refresh","enabled":true,"stale":true}]`)
		case "/api/apps/a/schedules/7/refresh-stale":
			fmt.Fprint(w, `{"schedule_id":7,"schedule":"refresh","status":"started","run_id":42}`)
		case "/api/apps/a/schedules/7/runs/42":
			fmt.Fprint(w, `{"status":"failed"}`)
		case "/api/apps/a/schedules/7/runs/42/logs":
			<-r.Context().Done()
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	res := applyResult{mutation: mutationNone}
	err := refreshStaleSchedules(&cliConfig{Host: srv.URL}, "a", &res, 60*time.Millisecond, io.Discard)
	if err == nil || res.failureKind != failureScheduleRefreshFailed || len(res.scheduleLogs) != 1 {
		t.Fatalf("err=%v result=%+v", err, res)
	}
}
