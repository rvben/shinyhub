package localrun

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunEventsAndManualRetry(t *testing.T) {
	skipIfNoPython3(t)
	source := t.TempDir()
	server := `import http.server, os, sys
print("runtime stdout", flush=True)
print("runtime stderr", file=sys.stderr, flush=True)
http.server.HTTPServer(("127.0.0.1",int(os.environ["PORT"])), http.server.SimpleHTTPRequestHandler).serve_forever()
`
	for name, body := range map[string]string{"server.py": server, "shinyhub.toml": "[app]\ncommand = [\"python3\", \"server.py\"]\n"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	events := make(chan Event, 256)
	retry := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{BundleDir: source, StateDir: t.TempDir(), Slug: "events-demo", NoSync: true, Reload: retry, OnEvent: func(e Event) { events <- e }}, io.Discard, io.Discard)
	}()
	var collected []Event
	ready := func(generation int64) {
		t.Helper()
		for {
			select {
			case e := <-events:
				collected = append(collected, e)
				if e.Phase == "ready" {
					if e.Generation != generation || e.URL == "" || e.Attempt != generation {
						t.Fatalf("ready = %+v", e)
					}
					return
				}
			case err := <-done:
				t.Fatalf("runner stopped before readiness: %v", err)
			case <-ctx.Done():
				t.Fatal("readiness timed out")
			}
		}
	}
	ready(1)
	retry <- struct{}{}
	ready(2)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for len(events) > 0 {
		collected = append(collected, <-events)
	}
	var stdout, stderr bool
	started, exited := make(map[int64]int), make(map[int64]int)
	for _, e := range collected {
		if e.App != "events-demo" || e.At.IsZero() {
			t.Fatalf("event missing identity/time: %+v", e)
		}
		if e.Type == "process" {
			if e.PID <= 0 {
				t.Fatal("process event omitted PID")
			}
			if e.Phase == "started" {
				started[e.Attempt] = e.PID
			}
			if e.Phase == "exited" {
				exited[e.Attempt] = e.PID
			}
		}
		if e.Type == "log" && strings.Contains(e.Message, "runtime stdout") {
			if e.Source != "app" {
				t.Fatal("runtime log mixed into reload diagnostics")
			}
			stdout = true
		}
		if e.Type == "log" && strings.Contains(e.Message, "runtime stderr") {
			if e.Source != "app" || e.Stream != "stderr" {
				t.Fatal("stderr lost source identity")
			}
			stderr = true
		}
	}
	if len(started) != 2 || len(exited) != 2 || started[1] != exited[1] || started[2] != exited[2] {
		t.Fatalf("incomplete process lifetime: started=%v exited=%v", started, exited)
	}
	if !stdout || !stderr || collected[len(collected)-1].Phase != "stopped" {
		t.Fatalf("incomplete lifecycle: stdout=%v stderr=%v last=%+v", stdout, stderr, collected[len(collected)-1])
	}
}

func TestEventWriterBoundsFragmentsAndFlushesPartialLines(t *testing.T) {
	var events []Event
	observer := &runEvents{app: "demo", callback: func(e Event) { events = append(events, e) }}
	observer.attempt.Store(2)
	w := observer.writer(io.Discard, "app", "stderr", 2)
	w.Write([]byte("split"))
	w.Write([]byte(" line\n" + strings.Repeat("x", 20000)))
	flushEventWriter(w)
	if len(events) != 4 || events[0].Message != "split line" {
		t.Fatalf("fragment records = %d", len(events))
	}
	for _, e := range events {
		if len(e.Message) > 8192 || e.Attempt != 2 {
			t.Fatalf("invalid log event: length=%d attempt=%d", len(e.Message), e.Attempt)
		}
	}
}
