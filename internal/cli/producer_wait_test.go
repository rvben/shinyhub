package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/fleet"
)

func TestUnchangedFleetWaitsForRunningProducers(t *testing.T) {
	for _, tc := range []struct {
		name       string
		schedules  bool
		invalidate bool
		after      string
		timeout    time.Duration
		want       string
	}{
		{name: "multiple producers finish", after: "clear", timeout: 3 * time.Second},
		{name: "satisfied deploy obligations still wait", schedules: true, after: "clear", timeout: 3 * time.Second},
		{name: "convergence changes while waiting", schedules: true, invalidate: true, after: "clear", timeout: 3 * time.Second, want: failureWarmNeverSucceeded},
		{name: "producer fails", after: "repair", timeout: 3 * time.Second, want: failureWarmNeverSucceeded},
		{name: "deadline", after: "running", timeout: 30 * time.Millisecond, want: failureWarmWaitTimeout},
		{name: "deployment fence is not transient", after: "deployment", timeout: 3 * time.Second, want: failureWarmNeverSucceeded},
		{name: "older server cannot prove transient fence", after: "unknown", timeout: 3 * time.Second, want: failureWarmNeverSucceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads, mutations := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/apps/demo/schedules/reconcile":
					w.WriteHeader(http.StatusOK)
				case "/api/apps/demo/schedules":
					if tc.invalidate && reads > 1 {
						io.WriteString(w, `[{"id":1,"name":"refresh","enabled":true,"deploy_trigger":"first_deploy","deploy_trigger_satisfied":false}]`)
					} else if tc.schedules {
						io.WriteString(w, `[{"id":1,"name":"refresh","enabled":true,"deploy_trigger":"first_deploy","deploy_trigger_satisfied":true}]`)
					} else {
						io.WriteString(w, `[]`)
					}
				case "/api/apps/demo":
					reads++
					state := map[string]any{"compatibility_quarantined": true, "producer_repair_required": false, "running_data_producers": []string{"refresh-latest-data", "refresh-timechart-data"}, "app": map[string]any{"deployment_repair_required": false}}
					switch tc.after {
					case "clear":
						if reads > 1 {
							state["compatibility_quarantined"] = false
							state["running_data_producers"] = []string{}
						}
					case "repair":
						if reads > 1 {
							state["producer_repair_required"] = true
						}
					case "deployment":
						state["app"] = map[string]any{"deployment_repair_required": true, "last_deployment_status": "failed"}
					case "unknown":
						delete(state, "app")
					}
					json.NewEncoder(w).Encode(state)
				default:
					mutations++
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			var out bytes.Buffer
			res := convergeApp(&cliConfig{Host: srv.URL}, fleet.AppDiff{Slug: "demo", Action: fleet.ActionUnchanged}, fleet.AppEntry{Slug: "demo"}, fleet.ObservedApp{}, "", convergeOpts{waitForWarm: true, warmTimeout: tc.timeout}, "", &out)
			if res.failureKind != tc.want {
				t.Fatalf("kind=%q want=%q result=%+v", res.failureKind, tc.want, res)
			}
			if tc.want == "" && (res.status != statusUnchanged || res.err != nil) {
				t.Fatalf("result=%+v", res)
			}
			if tc.want != "" && res.status != statusFailed {
				t.Fatalf("result=%+v", res)
			}
			if mutations != 0 {
				t.Fatalf("unexpected mutations=%d", mutations)
			}
			waits := tc.after == "clear" || tc.after == "repair" || tc.after == "running"
			if waits && (!strings.Contains(out.String(), "refresh-latest-data") || !strings.Contains(out.String(), "deadline")) {
				t.Fatalf("missing wait details: %s", out.String())
			}
			if !waits && reads != 1 {
				t.Fatalf("deterministic failure retried %d times", reads)
			}
		})
	}
}

func TestProducerWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"compatibility_quarantined":true,"producer_repair_required":false,"running_data_producers":["refresh"],"app":{"deployment_repair_required":false}}`)
		cancel()
	}))
	defer srv.Close()
	res := applyResult{}
	start := time.Now()
	err := waitForAppCompatibilityClear(ctx, &cliConfig{Host: srv.URL}, "demo", &res, io.Discard)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("cancel wait: %v", err)
	}
}

func TestWarmGateBoundsEveryRequest(t *testing.T) {
	for _, stage := range []string{"reconcile", "first list", "final list", "failure logs", "compatibility"} {
		for _, cancelled := range []bool{false, true} {
			name := stage + "/deadline"
			if cancelled {
				name = stage + "/cancel"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				release := make(chan struct{})
				lists := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					block := false
					switch r.URL.Path {
					case "/api/apps/demo/schedules/reconcile":
						block = stage == "reconcile"
					case "/api/apps/demo/schedules":
						lists++
						block = (stage == "first list" && lists == 1) || (stage == "final list" && lists == 2)
						if !block {
							if stage == "failure logs" {
								io.WriteString(w, `[{"id":1,"name":"refresh","enabled":true,"deploy_trigger":"first_deploy","deploy_trigger_satisfied":false,"last_run_id":3,"last_run_status":"failed"}]`)
							} else {
								io.WriteString(w, `[]`)
							}
						}
					case "/api/apps/demo/schedules/1/runs/3/logs":
						block = stage == "failure logs"
					case "/api/apps/demo":
						block = stage == "compatibility"
					default:
						t.Errorf("unexpected path %s", r.URL.Path)
					}
					if block {
						if cancelled {
							cancel()
						}
						select {
						case <-r.Context().Done():
						case <-release:
						}
					}
				}))
				defer srv.Close()
				defer close(release)
				res := applyResult{}
				started := time.Now()
				err := verifyExistingWarmGateContext(ctx, &cliConfig{Host: srv.URL}, "demo", "", &res, 50*time.Millisecond, io.Discard)
				if err == nil || time.Since(started) > time.Second {
					t.Fatalf("deadline/cancellation not honored: elapsed=%s err=%v", time.Since(started), err)
				}
				if !cancelled {
					want := failureWarmWaitTimeout
					if stage == "failure logs" {
						want = failureWarmNeverSucceeded
					}
					if res.failureKind != want {
						t.Fatalf("kind=%s want=%s err=%v", res.failureKind, want, err)
					}
				}
			})
		}
	}
}

func TestProducerWaitObservationErrors(t *testing.T) {
	running := `{"compatibility_quarantined":true,"producer_repair_required":false,"running_data_producers":["refresh"],"app":{"deployment_repair_required":false}}`
	clear := `{"compatibility_quarantined":false,"producer_repair_required":false,"app":{"deployment_repair_required":false}}`
	for _, tc := range []struct {
		name, body string
		status     int
		want       string
		wait       bool
	}{
		{name: "502 retries", status: 502, body: "proxy reloading", wait: true},
		{name: "network reset retries", status: -1, wait: true},
		{name: "torn snapshot rechecks", status: 200, body: `{"compatibility_quarantined":true,"producer_repair_required":false,"app":{"deployment_repair_required":false}}`, wait: true},
		{name: "auth fails", status: 403, body: "denied", want: failureWarmStateUnavailable},
		{name: "malformed fails", status: 200, body: `{`, want: failureWarmStateUnavailable},
		{name: "missing state fails", status: 200, body: `{}`, want: failureWarmStateUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reads++
				switch reads {
				case 1:
					w.Header().Set("Connection", "close")
					io.WriteString(w, running)
				case 2:
					if tc.status == -1 {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						conn.Close()
						return
					}
					w.WriteHeader(tc.status)
					io.WriteString(w, tc.body)
				default:
					io.WriteString(w, clear)
				}
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			res := applyResult{}
			err := waitForAppCompatibilityClear(ctx, &cliConfig{Host: srv.URL}, "demo", &res, io.Discard)
			if (err == nil) != tc.wait || res.failureKind != tc.want {
				t.Fatalf("reads=%d kind=%s err=%v", reads, res.failureKind, err)
			}
			if !tc.wait && reads != 2 {
				t.Fatalf("fatal observation retried: %d", reads)
			}
		})
	}
}

func TestProducerWaitExpiredBudgetDoesNotProbe(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	res := applyResult{}
	err := waitForAppCompatibilityClear(ctx, &cliConfig{Host: "http://127.0.0.1:1"}, "demo", &res, io.Discard)
	if res.failureKind != failureWarmWaitTimeout || err == nil || strings.Contains(err.Error(), "()") || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("kind=%s err=%v", res.failureKind, err)
	}
}

func TestRefreshCompatibilityWait(t *testing.T) {
	for _, mode := range []string{"success", "timeout", "cancel", "repair"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reads := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/apps/demo/schedules":
					io.WriteString(w, `[]`)
				case "/api/apps/demo":
					reads++
					if mode == "cancel" {
						cancel()
					}
					quarantined, repair := true, false
					if reads > 1 {
						quarantined = mode != "success"
						repair = mode == "repair"
					}
					json.NewEncoder(w).Encode(map[string]any{"compatibility_quarantined": quarantined, "producer_repair_required": repair, "producer_repair_schedules": []string{"refresh"}, "running_data_producers": []string{"refresh"}, "app": map[string]any{"deployment_repair_required": false}})
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			budget := 3 * time.Second
			if mode == "timeout" {
				budget = 40 * time.Millisecond
			}
			res := convergeApp(&cliConfig{Host: srv.URL}, fleet.AppDiff{Slug: "demo", Action: fleet.ActionUnchanged}, fleet.AppEntry{Slug: "demo"}, fleet.ObservedApp{}, "", convergeOpts{context: ctx, refreshStale: true, warmTimeout: budget}, "", io.Discard)
			if mode == "success" {
				if res.status != statusUnchanged || res.err != nil || res.failureKind != "" {
					t.Fatalf("result=%+v", res)
				}
				return
			}
			if res.err == nil || res.status != statusFailed {
				t.Fatalf("result=%+v", res)
			}
			want := failureScheduleStateMissing
			if mode == "timeout" {
				want = failureScheduleRefreshTimeout
			}
			if mode == "repair" {
				want = failureScheduleProducer
				if !strings.Contains(res.err.Error(), "shinyhub schedule run demo refresh --follow") {
					t.Fatalf("missing repair guidance: %v", res.err)
				}
			}
			if res.failureKind != want {
				t.Fatalf("kind=%s want=%s err=%v", res.failureKind, want, res.err)
			}
		})
	}
}

// A permanent transport error is reported at once rather than retried until
// the deadline. The deadline is generous so a slow host (a TLS handshake can
// take seconds under load) cannot turn the error into a timeout; whether the
// wait retried is read from the wait itself, not from the clock.
func TestProducerWaitRejectsPermanentTransportErrors(t *testing.T) {
	check := func(t *testing.T, host string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		res := applyResult{}
		waited, err := waitForAppCompatibilityClearObserved(ctx, &cliConfig{Host: host}, "demo", &res, io.Discard)
		if err == nil || waited || res.failureKind != failureWarmStateUnavailable {
			t.Fatalf("host=%s waited=%v kind=%s err=%v", host, waited, res.failureKind, err)
		}
	}
	for _, host := range []string{"bad-scheme://example.invalid", "http://[broken"} {
		check(t, host)
	}
	// The certificate is one the client does not trust, so the handshake fails
	// before any request is served. A request that gets through means the case
	// is testing something other than a certificate error.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request reached the server despite its untrusted certificate")
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	check(t, srv.URL)
}
