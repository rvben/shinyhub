package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deployevent"
	"github.com/rvben/shinyhub/internal/deployfail"
)

func TestFleetCapacityWait(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		stream, transient, cancel bool
		wait                      time.Duration
		want                      int
		message                   string
	}{
		{name: "json recovers independently of retries", transient: true, wait: 2 * time.Second, want: 2},
		{name: "stream recovers", stream: true, transient: true, wait: 2 * time.Second, want: 2},
		{name: "draining budget recovers", transient: true, wait: 2 * time.Second, want: 2, message: "CPU budget: 0.90 cores allocated to live workers; waiting for draining predecessors"},
		{name: "stream draining budget recovers", stream: true, transient: true, wait: 2 * time.Second, want: 2, message: "CPU budget: 0.90 cores allocated to live workers; waiting for draining predecessors"},
		{name: "surge ceiling recovers", transient: true, wait: 2 * time.Second, want: 2, message: "max_surge_replicas=1 cannot fit candidates and existing non-active workers"},
		{name: "accounting fails fast", wait: 2 * time.Second, want: 1},
		{name: "disabled", transient: true, want: 1},
		{name: "deadline", transient: true, wait: 10 * time.Millisecond, want: 2},
		{name: "cancelled", transient: true, cancel: true, wait: 2 * time.Second, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := "working version preserved"
			if tc.message != "" {
				message += ": " + tc.message
			}
			hits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/apps/demo/deploy":
					hits++
					if hits > 1 {
						io.WriteString(w, `{"status":"ok"}`)
						return
					}
					if tc.stream {
						w.Header().Set("Content-Type", deployevent.MediaType)
						json.NewEncoder(w).Encode(deployevent.Event{Type: deployevent.TypeError, Phase: "capacity", StatusCode: 409, FailureKind: "capacity", Message: message, CapacityRetryable: tc.transient, RetryAfterSeconds: 1})
						return
					}
					w.Header().Set("X-ShinyHub-Conflict", "generation-handoff-deferred")
					w.WriteHeader(409)
					json.NewEncoder(w).Encode(map[string]any{"update_state": "blocked_capacity", "failure_kind": "capacity", "error": message, "capacity_retryable": tc.transient, "retry_after_seconds": 1})
				case "/api/apps/demo":
					io.WriteString(w, `{"app":{"slug":"demo","status":"running"}}`)
				case "/api/apps":
					io.WriteString(w, `[{"slug":"demo","content_digest":"sha256:new"}]`)
				default:
					io.WriteString(w, `{}`)
				}
			}))
			defer srv.Close()
			dir := t.TempDir()
			mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			start := time.Now()
			_, attempts, _, _, failed, err := deployWithRetry(&cliConfig{Host: srv.URL, Token: "test"}, "demo", bundleBuildSpec{Dir: dir}, "private", "", convergeOpts{context: ctx, preconditions: true, capacityWait: tc.wait}, io.Discard, "sha256:old", "fleet:test")
			if attempts != tc.want || hits != tc.want {
				t.Fatalf("attempts=%d hits=%d want=%d err=%v", attempts, hits, tc.want, err)
			}
			if (err == nil) != (tc.want == 2) {
				t.Fatalf("error=%v", err)
			}
			if len(failed) != 1 || failed[0].Kind != deployfail.Capacity {
				t.Fatalf("failures=%+v", failed)
			}
			if tc.name == "deadline" && time.Since(start) < tc.wait {
				t.Fatal("returned before capacity deadline")
			}
		})
	}
}

func TestUnpreservedCapacityIsMutatingFailure(t *testing.T) {
	body := []byte(`{"failure_kind":"capacity","capacity_retryable":true,"error":"activation deferred for capacity after data changed"}`)
	if got := failureKindFromBody(500, body); got != deployfail.ServerError {
		t.Fatalf("late mutating failure classified %s", got)
	}
	response := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"type":"error","status_code":500,"failure_kind":"capacity","phase":"deploy","message":"app left stopped","capacity_retryable":true}`))}
	_, kind, err := consumeFleetDeployEvents(response, "demo", io.Discard)
	var capacity *fleetCapacityError
	if kind != deployfail.ServerError || errors.As(err, &capacity) {
		t.Fatalf("stream treated mutating error as capacity refusal: %s %v", kind, err)
	}
}

// A capacity retry re-sends the bundle the first attempt built. The source
// tree is edited while the server refuses the first upload; the second upload
// must still carry the original bytes, which are the ones the digest
// preconditions describe.
func TestDeployWithRetryUploadsOneBuildAcrossCapacityRetries(t *testing.T) {
	dir := t.TempDir()
	appFile := filepath.Join(dir, "app.py")
	mustWrite(t, appFile, "print('original')\n")
	var uploads [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps/demo/deploy":
			f, _, err := r.FormFile("bundle")
			if err != nil {
				t.Errorf("bundle part: %v", err)
				return
			}
			body, _ := io.ReadAll(f)
			uploads = append(uploads, body)
			if len(uploads) == 1 {
				mustWrite(t, appFile, "print('edited during the capacity wait, and longer')\n")
				w.Header().Set("X-ShinyHub-Conflict", "generation-handoff-deferred")
				w.WriteHeader(409)
				json.NewEncoder(w).Encode(map[string]any{"update_state": "blocked_capacity", "failure_kind": "capacity", "error": "working version preserved", "capacity_retryable": true, "retry_after_seconds": 1})
				return
			}
			io.WriteString(w, `{"status":"ok"}`)
		case "/api/apps/demo":
			io.WriteString(w, `{"app":{"slug":"demo","status":"running"}}`)
		case "/api/apps":
			io.WriteString(w, `[{"slug":"demo","content_digest":"sha256:new"}]`)
		default:
			io.WriteString(w, `{}`)
		}
	}))
	defer srv.Close()
	_, attempts, _, _, _, err := deployWithRetry(&cliConfig{Host: srv.URL, Token: "test"}, "demo", bundleBuildSpec{Dir: dir}, "private", "", convergeOpts{context: context.Background(), preconditions: true, capacityWait: 5 * time.Second}, io.Discard, "sha256:old", "fleet:test")
	if err != nil || attempts != 2 || len(uploads) != 2 {
		t.Fatalf("attempts=%d uploads=%d err=%v", attempts, len(uploads), err)
	}
	if string(uploads[0]) != string(uploads[1]) {
		t.Fatal("the retry uploaded a different bundle than the first attempt built")
	}
}
