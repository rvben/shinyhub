package process

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDockerStartupGuard(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		acknowledge, failStart, failRemove bool
	}{
		{name: "acknowledge", acknowledge: true},
		{name: "abort"},
		{name: "start failure", acknowledge: true, failStart: true},
		{name: "ambiguous start and cleanup failure", acknowledge: true, failStart: true, failRemove: true},
		{name: "abort cleanup failure", failRemove: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var starts, removes, waits atomic.Int32
			mux := http.NewServeMux()
			mux.HandleFunc("/containers/create", func(w http.ResponseWriter, r *http.Request) {
				var cfg struct{ Labels map[string]string }
				if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
					t.Error(err)
				}
				if cfg.Labels[LabelLaunchID] != "launch-123" {
					t.Errorf("launch label = %q", cfg.Labels[LabelLaunchID])
				}
				w.WriteHeader(http.StatusCreated)
				io.WriteString(w, `{"Id":"guarded"}`)
			})
			mux.HandleFunc("/containers/guarded/start", func(w http.ResponseWriter, r *http.Request) {
				starts.Add(1)
				if tc.failStart {
					http.Error(w, "ambiguous start", 500)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})
			mux.HandleFunc("/containers/guarded", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Query().Get("force") != "true" {
					t.Errorf("unsafe cleanup: %s %s", r.Method, r.URL)
				}
				removes.Add(1)
				if tc.failRemove {
					http.Error(w, "cleanup unavailable", 500)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})
			mux.HandleFunc("/containers/guarded/wait", func(w http.ResponseWriter, r *http.Request) { waits.Add(1); io.WriteString(w, `{"StatusCode":0}`) })
			mux.HandleFunc("/containers/guarded/attach", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
			rt := newDockerRuntimeWithServer(t, mux)
			ep, err := rt.Start(context.Background(), StartParams{Slug: "app", Dir: t.TempDir(), Command: []string{"python", "app.py"}, Port: 8123, GuardUntilAcknowledged: true, LaunchID: "launch-123"}, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if ep.StartupGuard == nil || ep.WorkerID != "guarded" {
				t.Fatalf("missing guarded identity: %+v", ep)
			}
			if starts.Load() != 0 {
				t.Fatal("app started before acknowledgement")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if err := rt.Wait(ctx, ep.Handle); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("pre-ack wait = %v", err)
			}
			if waits.Load() != 0 {
				t.Fatal("wait reached Docker before container start")
			}
			if tc.acknowledge {
				if _, err := io.WriteString(ep.StartupGuard, "ready\n"); err != nil {
					t.Fatal(err)
				}
				if starts.Load() != 0 {
					t.Fatal("container started before closing acknowledgement")
				}
			}
			err = ep.StartupGuard.Close()
			if (err != nil) != (tc.failStart || tc.failRemove) {
				t.Fatalf("close = %v", err)
			}
			if tc.failRemove && !strings.Contains(err.Error(), "cleanup unavailable") {
				t.Fatalf("cleanup failure hidden: %v", err)
			}
			if tc.failStart && !strings.Contains(err.Error(), "ambiguous start") {
				t.Fatalf("start failure hidden: %v", err)
			}
			wantStarts := int32(0)
			if tc.acknowledge {
				wantStarts = 1
			}
			wantRemoves := int32(1)
			if tc.acknowledge && !tc.failStart {
				wantRemoves = 0
			}
			if starts.Load() != wantStarts || removes.Load() != wantRemoves {
				t.Fatalf("starts=%d removes=%d", starts.Load(), removes.Load())
			}
			if tc.acknowledge && !tc.failStart {
				var exit *ProcessExitError
				if err := rt.Wait(context.Background(), ep.Handle); !errors.As(err, &exit) || exit.Code != 0 {
					t.Fatalf("wait after ack = %v", err)
				}
				if waits.Load() != 1 {
					t.Fatal("acknowledged container was not monitored")
				}
			}
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() { defer wg.Done(); _ = ep.StartupGuard.Close() }()
			}
			wg.Wait()
			if starts.Load() != wantStarts || removes.Load() != wantRemoves {
				t.Fatal("repeated close repeated side effects")
			}
			if _, err := io.WriteString(ep.StartupGuard, "ready\n"); !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("write after close = %v", err)
			}
			if _, ok := rt.startupGuards.Load(ep.WorkerID); ok {
				t.Fatal("completed guard retained")
			}
		})
	}
}

func TestDockerStartupGuardRejectsInvalidAcknowledgement(t *testing.T) {
	g := &dockerStartupGuard{}
	for _, input := range []string{"", "ready", "not-ready\n", "ready\nextra"} {
		if n, err := io.WriteString(g, input); n != 0 || err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	if g.acknowledged {
		t.Fatal("invalid acknowledgement armed launch")
	}
}
