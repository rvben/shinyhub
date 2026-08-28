package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeployRunRefsFromDeployResponse(t *testing.T) {
	body := []byte(`{"deploy_count":3,"manifest":{"schedules":[
		{"name":"warm","action":"created","schedule_id":5,"deploy_run":{"run_id":42}},
		{"name":"other","action":"updated","schedule_id":6}
	]}}`)
	refs := deployRunRefsFromDeployResponse(body)
	if len(refs) != 1 {
		t.Fatalf("len(refs) = %d, want 1", len(refs))
	}
	if refs[0].Schedule != "warm" || refs[0].ScheduleID != 5 || refs[0].RunID != 42 {
		t.Errorf("ref = %+v, want {warm 5 42}", refs[0])
	}
}

func TestDeployRunRefsFromDeployResponse_None(t *testing.T) {
	if got := deployRunRefsFromDeployResponse([]byte(`{"deploy_count":1}`)); len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestDeployRunRefsFromDeployResponse_PrestartDoesNotRequestRedundantWaitOrRestart(t *testing.T) {
	body := []byte(`{"schedule_convergence":[{"schedule":"warm","schedule_id":5,"status":"satisfied","run_id":42,"prestart":true}],"manifest":{"schedules":[{"name":"warm","schedule_id":5,"deploy_run":{"run_id":42}}]}}`)
	if refs := deployRunRefsFromDeployResponse(body); len(refs) != 0 {
		t.Fatalf("pre-start convergence refs=%+v, want none", refs)
	}
}

func TestWaitForDeployRunLoop_Succeeded(t *testing.T) {
	statuses := []string{"running", "running", "succeeded"}
	i := 0
	poll := func() (string, error) {
		s := statuses[i]
		if i < len(statuses)-1 {
			i++
		}
		return s, nil
	}
	now := func() time.Time { return time.Unix(0, 0) }
	status, err := waitForDeployRunLoop(poll, 10*time.Second, time.Millisecond, time.Hour, now, func(time.Duration) {}, io.Discard, "warm")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if status != "succeeded" {
		t.Errorf("status = %q, want succeeded", status)
	}
}

func TestWaitForDeployRunLoop_SkippedOverlapDoesNotProveSuccess(t *testing.T) {
	poll := func() (string, error) { return "skipped_overlap", nil }
	now := func() time.Time { return time.Unix(0, 0) }
	status, err := waitForDeployRunLoop(poll, 10*time.Second, time.Millisecond, time.Hour, now, func(time.Duration) {}, io.Discard, "warm")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if deployRunStatusOK(status) {
		t.Errorf("deployRunStatusOK(%q) = true, want false", status)
	}
}

func TestWaitForDeployRunLoop_TransientErrorThenSucceeds(t *testing.T) {
	i := 0
	responses := []struct {
		status string
		err    error
	}{
		{"", &deployHTTPError{statusCode: 503}},
		{"", &deployHTTPError{statusCode: 503}},
		{"succeeded", nil},
	}
	poll := func() (string, error) {
		r := responses[i]
		if i < len(responses)-1 {
			i++
		}
		return r.status, r.err
	}
	cur := time.Unix(0, 0)
	now := func() time.Time { return cur }
	sleep := func(d time.Duration) { cur = cur.Add(d) }
	status, err := waitForDeployRunLoop(poll, 10*time.Second, time.Millisecond, time.Hour, now, sleep, io.Discard, "warm")
	if err != nil {
		t.Fatalf("err = %v, want nil (transient errors should be skipped)", err)
	}
	if status != "succeeded" {
		t.Errorf("status = %q, want succeeded", status)
	}
}

func TestWaitForDeployRunLoop_FatalErrorAborts(t *testing.T) {
	fatalErr := &deployHTTPError{statusCode: 401}
	poll := func() (string, error) { return "", fatalErr }
	cur := time.Unix(0, 0)
	now := func() time.Time { return cur }
	sleep := func(d time.Duration) { cur = cur.Add(d) }
	_, err := waitForDeployRunLoop(poll, 10*time.Second, time.Second, time.Hour, now, sleep, io.Discard, "warm")
	if err == nil {
		t.Fatal("err = nil, want fatal error")
	}
	if errors.Is(err, errDeployRunTimeout) {
		t.Fatal("err = errDeployRunTimeout, want the 401 error (should abort immediately)")
	}
	var he *deployHTTPError
	if !errors.As(err, &he) || he.statusCode != 401 {
		t.Errorf("err = %v, want *deployHTTPError with statusCode 401", err)
	}
	// Clock must not have advanced to the deadline (abort was immediate).
	if cur != time.Unix(0, 0) {
		t.Errorf("clock advanced to %v, want no advancement (immediate abort)", cur)
	}
}

func TestWaitForDeployRunLoop_Timeout(t *testing.T) {
	poll := func() (string, error) { return "running", nil }
	cur := time.Unix(0, 0)
	now := func() time.Time { return cur }
	sleep := func(d time.Duration) { cur = cur.Add(d) }
	var out bytes.Buffer
	status, err := waitForDeployRunLoop(poll, 5*time.Second, time.Second, time.Hour, now, sleep, &out, "warm")
	if !errors.Is(err, errDeployRunTimeout) {
		t.Fatalf("err = %v, want errDeployRunTimeout", err)
	}
	if status != "running" {
		t.Errorf("status = %q, want running (last seen)", status)
	}
}

func TestWaitForDeployRunLoop_PollThatConsumesDeadlineDoesNotSleepAgain(t *testing.T) {
	cur := time.Unix(0, 0)
	poll := func() (string, error) {
		cur = cur.Add(5 * time.Second)
		return "running", context.DeadlineExceeded
	}
	var slept time.Duration
	_, err := waitForDeployRunLoop(poll, 5*time.Second, time.Second, time.Hour,
		func() time.Time { return cur }, func(d time.Duration) { slept += d }, io.Discard, "warm")
	if !errors.Is(err, errDeployRunTimeout) {
		t.Fatalf("err = %v, want errDeployRunTimeout", err)
	}
	if slept != 0 {
		t.Fatalf("slept %s after poll exhausted deadline, want 0", slept)
	}
}

func TestDeployRunStatusOK(t *testing.T) {
	if !deployRunStatusOK("succeeded") {
		t.Error("deployRunStatusOK(succeeded) = false, want true")
	}
	for _, s := range []string{"skipped_overlap", "failed", "interrupted", "cancelled", "timed_out"} {
		if deployRunStatusOK(s) {
			t.Errorf("deployRunStatusOK(%q) = true, want false", s)
		}
	}
}

func TestVerifyExistingWarmGate_FinalSnapshotRejectsMixedDeployments(t *testing.T) {
	listCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/demo/schedules/reconcile":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo/schedules":
			listCalls++
			if listCalls == 1 {
				_, _ = io.WriteString(w, `[{"id":1,"name":"one","enabled":true,"deploy_trigger":"bundle_change","deploy_trigger_satisfied":true},{"id":2,"name":"two","enabled":true,"deploy_trigger":"bundle_change","deploy_trigger_satisfied":true}]`)
				return
			}
			// A concurrent deployment invalidated schedule one after the first
			// snapshot. The gate must not combine that old success with schedule
			// two's success and report a false whole-app fixed point.
			_, _ = io.WriteString(w, `[{"id":1,"name":"one","enabled":true,"deploy_trigger":"bundle_change","deploy_trigger_satisfied":false},{"id":2,"name":"two","enabled":true,"deploy_trigger":"bundle_change","deploy_trigger_satisfied":true}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	res := applyResult{}
	err := verifyExistingWarmGate(&cliConfig{Host: srv.URL, Token: "test"}, "demo", "", &res)
	if err == nil || !strings.Contains(err.Error(), "convergence changed during verification") {
		t.Fatalf("error = %v, want final whole-set mismatch", err)
	}
	if listCalls != 2 {
		t.Fatalf("schedule list calls = %d, want initial plus final snapshot", listCalls)
	}
}

func TestVerifyExistingWarmGateRejectsDisabledProducerRepairQuarantine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/demo/schedules/reconcile":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo/schedules":
			_, _ = io.WriteString(w, `[{"id":1,"name":"producer","enabled":false,"deploy_trigger":"never","producer_repair_required":true}]`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo":
			_, _ = io.WriteString(w, `{"compatibility_quarantined":true,"producer_repair_required":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	res := applyResult{}
	err := verifyExistingWarmGate(&cliConfig{Host: srv.URL, Token: "test"}, "demo", "", &res)
	if err == nil || !strings.Contains(err.Error(), "successful producer repair") {
		t.Fatalf("error = %v, want producer repair quarantine", err)
	}
}

func TestVerifyExistingWarmGateRejectsDeploymentBarrierWithoutProducer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/demo/schedules/reconcile":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo/schedules":
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo":
			_, _ = io.WriteString(w, `{"compatibility_quarantined":true,"producer_repair_required":false}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	res := applyResult{}
	err := verifyExistingWarmGate(&cliConfig{Host: srv.URL, Token: "test"}, "demo", "", &res)
	if err == nil || !strings.Contains(err.Error(), "incomplete producer barrier") {
		t.Fatalf("error = %v, want deployment barrier quarantine", err)
	}
}

func TestVerifyExistingWarmGateClassifiesCompatibilityObservationFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "server error", status: http.StatusServiceUnavailable, body: `unavailable`},
		{name: "malformed response", status: http.StatusOK, body: `{`},
		{name: "empty object", status: http.StatusOK, body: `{}`},
		{name: "missing quarantine state", status: http.StatusOK, body: `{"producer_repair_required":false}`},
		{name: "missing repair state", status: http.StatusOK, body: `{"compatibility_quarantined":false}`},
		{name: "null quarantine state", status: http.StatusOK, body: `{"compatibility_quarantined":null,"producer_repair_required":false}`},
		{name: "null repair state", status: http.StatusOK, body: `{"compatibility_quarantined":false,"producer_repair_required":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/api/apps/demo/schedules/reconcile":
					w.WriteHeader(http.StatusOK)
				case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo/schedules":
					_, _ = io.WriteString(w, `[]`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo":
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			res := applyResult{}
			if err := verifyExistingWarmGate(&cliConfig{Host: srv.URL, Token: "test"}, "demo", "", &res); err == nil {
				t.Fatal("compatibility observation failure returned nil")
			}
			if res.failureKind != failureWarmStateUnavailable {
				t.Fatalf("failure kind=%q, want %q", res.failureKind, failureWarmStateUnavailable)
			}
		})
	}
}

func TestVerifyExistingWarmGateClassifiesActiveRunRefreshFailure(t *testing.T) {
	listCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/demo/schedules/reconcile":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo/schedules":
			listCalls++
			if listCalls == 1 {
				_, _ = io.WriteString(w, `[{"id":1,"name":"producer","enabled":true,"deploy_trigger":"bundle_change","deploy_trigger_satisfied":false,"convergence_status":"running","convergence_run_id":17,"last_run_id":17,"last_run_status":"running"}]`)
				return
			}
			http.Error(w, "state unavailable", http.StatusServiceUnavailable)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo/schedules/1/runs/17":
			_, _ = io.WriteString(w, `{"status":"succeeded"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	res := applyResult{}
	err := verifyExistingWarmGateWithWait(
		&cliConfig{Host: srv.URL, Token: "test"}, "demo", "", &res, time.Second, io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "refresh schedule") {
		t.Fatalf("error = %v, want authoritative refresh failure", err)
	}
	if res.failureKind != failureWarmStateUnavailable {
		t.Fatalf("failure kind=%q, want %q", res.failureKind, failureWarmStateUnavailable)
	}
}

func TestRestartAppAfterWarm_RestartsRunningApp(t *testing.T) {
	var restartHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo":
			_, _ = io.WriteString(w, `{"app":{"status":"running"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/demo/restart":
			restartHits++
			_, _ = io.WriteString(w, `{"status":"running"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	restarted, err := restartAppAfterWarm(&cliConfig{Host: srv.URL, Token: "test"}, "demo", &out)
	if err != nil {
		t.Fatalf("restartAppAfterWarm: %v", err)
	}
	if !restarted || restartHits != 1 {
		t.Fatalf("restarted=%v hits=%d, want true/1", restarted, restartHits)
	}
	if !strings.Contains(out.String(), "restarted after bundle data convergence") {
		t.Errorf("output = %q", out.String())
	}
}

func TestRestartAppAfterWarm_KeepsStoppedAppStopped(t *testing.T) {
	var restartHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo" {
			_, _ = io.WriteString(w, `{"app":{"status":"stopped"}}`)
			return
		}
		restartHits++
		http.Error(w, "must not restart", http.StatusInternalServerError)
	}))
	defer srv.Close()

	restarted, err := restartAppAfterWarm(&cliConfig{Host: srv.URL, Token: "test"}, "demo", io.Discard)
	if err != nil {
		t.Fatalf("restartAppAfterWarm: %v", err)
	}
	if restarted || restartHits != 0 {
		t.Fatalf("restarted=%v hits=%d, want false/0", restarted, restartHits)
	}
}
