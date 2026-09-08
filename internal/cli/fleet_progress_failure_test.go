package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/fleet"
)

func TestFleetDisplayFailedPollingSettlesAndStops(t *testing.T) {
	for _, concurrency := range []int{1, 3} {
		for _, failure := range []string{"fatal", "cancelled"} {
			t.Run(fmt.Sprintf("%s/concurrency_%d", failure, concurrency), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				pf := buildConcurrencyPreflight(concurrency, "")
				entries := make(map[string]fleet.AppEntry)
				var slugs []string
				for i, entry := range pf.manifest.Apps {
					pf.diff[i].Action = fleet.ActionUnchanged
					entries[entry.Slug] = entry
					slugs = append(slugs, entry.Slug)
				}
				var out bytes.Buffer
				d := testFleetDisplay(&out, slugs...)
				d.started = time.Now()
				d.start()
				closed := false
				defer func() {
					if !closed {
						d.close()
					}
				}()
				var polls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case strings.HasSuffix(r.URL.Path, "/schedules"):
						fmt.Fprint(w, `[{"id":7,"name":"refresh-data","enabled":true,"stale":true}]`)
					case strings.HasSuffix(r.URL.Path, "/refresh-stale"):
						fmt.Fprint(w, `{"schedule_id":7,"schedule":"refresh-data","status":"started","run_id":42}`)
					case strings.HasSuffix(r.URL.Path, "/runs/42"):
						// A real request reaches the polling path before it fails.
						fmt.Fprintln(d, "poll diagnostic: checking admitted run #42")
						count := polls.Add(1)
						if failure == "fatal" {
							http.Error(w, "poll permission denied", http.StatusUnauthorized)
							return
						}
						// Ensure every parallel worker is blocked in its request
						// when the parent context cancels, rather than cancelling
						// some workers before they even start convergence.
						if count == int32(concurrency) {
							cancel()
						}
						<-r.Context().Done()
					case strings.HasSuffix(r.URL.Path, "/logs"):
						fmt.Fprint(w, "producer log retained\n")
					default:
						t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer srv.Close()
				opt := convergeOpts{context: ctx, refreshStale: true, warmTimeout: time.Minute, concurrency: concurrency}
				var results []applyResult
				if concurrency == 1 {
					results = convergeSerial(&cliConfig{Host: srv.URL}, pf, entries, opt, "fleet:eu", d)
				} else {
					results = convergeParallel(&cliConfig{Host: srv.URL}, pf, entries, opt, "fleet:eu", d)
				}
				d.close()
				closed = true
				select {
				case <-d.stopped:
				default:
					t.Fatal("failure returned before animation stopped")
				}
				if polls.Load() != int32(concurrency) {
					t.Fatalf("polls = %d, want %d", polls.Load(), concurrency)
				}
				for i, result := range results {
					row := d.rows[i]
					if result.status != statusFailed || result.err == nil || row.status != statusFailed || row.finished.IsZero() || !row.deadline.IsZero() {
						t.Fatalf("failure did not settle row %d: result=%+v row=%+v", i, result, row)
					}
					if failure == "cancelled" && !errors.Is(result.err, context.Canceled) {
						t.Fatalf("cancellation was lost: %v", result.err)
					}
					if failure == "fatal" && !strings.Contains(result.err.Error(), "401") {
						t.Fatalf("fatal polling error was lost: %v", result.err)
					}
				}
				transcript := out.String()
				finalIndex := strings.LastIndex(transcript, "Fleet finished")
				diagnosticIndex := strings.Index(transcript, "poll diagnostic: checking admitted run #42")
				if diagnosticIndex < 0 || finalIndex < diagnosticIndex {
					t.Fatalf("diagnostic or settled frame missing: %q", transcript)
				}
				final := stripANSI(transcript[finalIndex:])
				if !strings.Contains(final, fmt.Sprintf("%d failed", concurrency)) || strings.Contains(final, "timeout in") {
					t.Fatalf("incorrect final failure frame: %s", final)
				}
				for _, frame := range spinnerFramesUTF8 {
					if strings.Contains(final, frame) {
						t.Fatalf("failed row still spinning: %s", final)
					}
				}
			})
		}
	}
}
