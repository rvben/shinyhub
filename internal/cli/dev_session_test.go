package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/localrun"
	"github.com/spf13/cobra"
)

func TestDevPresentationWithRedirectedStreams(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want devPresentation
		fail bool
	}{
		{nil, devPlain, false},
		{[]string{"--tui=false"}, devPlain, false},
		{[]string{"--tui"}, "", true},
		{[]string{"--output", "ndjson"}, devNDJSON, false},
		{[]string{"--output", "table"}, devPlain, false},
		{[]string{"--output", "json"}, "", true},
		{[]string{"--output", "ndjson", "--tui"}, "", true},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			resetFormatState(t)
			f := &devFlags{}
			root := &cobra.Command{Use: "shinyhub"}
			AddCommandsTo(root)
			cmd, _, err := root.Find([]string{"dev"})
			if err != nil {
				t.Fatal(err)
			}
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			// Parse inherited flags through Cobra without starting an app.
			cmd.RunE = func(cmd *cobra.Command, _ []string) error {
				f.tui, _ = cmd.Flags().GetBool("tui")
				got, err := resolveDevPresentation(cmd, f)
				if (err != nil) != tc.fail || got != tc.want {
					t.Fatalf("mode=%s err=%v", got, err)
				}
				return nil
			}
			root.SetArgs(append([]string{"dev"}, tc.args...))
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type localDevEventSink struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	cancel context.CancelFunc
	events []localrun.Event
}

func (w *localDevEventSink) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var event localrun.Event
	if err := json.Unmarshal(bytes.TrimSpace(p), &event); err != nil {
		return 0, err
	}
	w.events = append(w.events, event)
	if event.Phase == "ready" {
		w.cancel()
	}
	return w.buf.Write(p)
}
func TestLocalDevNDJSONIsCompleteAppAttributedStream(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	resetFormatState(t)
	source := t.TempDir()
	mustWrite(t, filepath.Join(source, "server.py"), `import http.server, os
print("application stdout", flush=True)
http.server.HTTPServer(("127.0.0.1",int(os.environ["PORT"])), http.server.SimpleHTTPRequestHandler).serve_forever()
`)
	mustWrite(t, filepath.Join(source, "shinyhub.toml"), "[app]\ncommand = [\"python3\", \"server.py\"]\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sink := &localDevEventSink{cancel: cancel}
	root := &cobra.Command{Use: "shinyhub"}
	AddCommandsTo(root)
	root.SetContext(ctx)
	root.SetOut(sink)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"dev", source, "--standalone", "--slug", "stream-demo", "--no-sync", "--state-dir", t.TempDir(), "--output", "ndjson"})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var ready, appLog bool
	for _, event := range sink.events {
		if event.App != "stream-demo" || event.At.IsZero() {
			t.Fatalf("missing event metadata: %+v", event)
		}
		if event.Phase == "ready" {
			ready = true
			if event.Generation != 1 || event.URL == "" {
				t.Fatal("ready event omitted route/generation")
			}
		}
		if event.Source == "app" && event.Message == "application stdout" {
			appLog = true
		}
	}
	if !ready || !appLog || sink.events[len(sink.events)-1].Phase != "stopped" {
		t.Fatalf("incomplete stream: ready=%v log=%v", ready, appLog)
	}
	if strings.Contains(sink.buf.String(), "\x1b") {
		t.Fatal("terminal escapes leaked into NDJSON")
	}
}

func TestLocalDevNDJSONHonorsExplicitFleetSeed(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	for _, tc := range []struct {
		fleetSeed, explicit string
		want                bool
	}{{"always", "never", false}, {"never", "always", true}} {
		t.Run(tc.fleetSeed+" overridden by "+tc.explicit, func(t *testing.T) {
			resetFormatState(t)
			fleet := t.TempDir()
			app := filepath.Join(fleet, "demo")
			if err := os.Mkdir(app, 0755); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(fleet, "fleet.toml"), fmt.Sprintf("fleet_id = \"seed-test\"\n[dev]\nseed = \"%s\"\n[[app]]\nslug = \"demo\"\nsource = \"demo\"\n", tc.fleetSeed))
			mustWrite(t, filepath.Join(app, "server.py"), `import http.server, os
http.server.HTTPServer(("127.0.0.1",int(os.environ["PORT"])), http.server.SimpleHTTPRequestHandler).serve_forever()
`)
			mustWrite(t, filepath.Join(app, "shinyhub.toml"), `[app]
command = ["python3", "server.py"]
[[schedule]]
name = "seed"
cron = "0 * * * *"
deploy_trigger = "bundle_change"
cmd = "sh producer.sh"
`)
			mustWrite(t, filepath.Join(app, "producer.sh"), `echo seeded > "$SHINYHUB_APP_DATA/seeded"`)
			data, state := t.TempDir(), t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			root := &cobra.Command{Use: "shinyhub"}
			AddCommandsTo(root)
			root.SetContext(ctx)
			root.SetOut(&localDevEventSink{cancel: cancel})
			root.SetErr(io.Discard)
			root.SetArgs([]string{"dev", fleet, "--seed=" + tc.explicit, "--no-sync", "--data-dir", data, "--state-dir", state, "--output", "ndjson"})
			if err := root.ExecuteContext(ctx); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(filepath.Join(data, "seeded"))
			if (err == nil) != tc.want {
				t.Fatalf("explicit seed=%s: seeded=%v, want %v (stat=%v)", tc.explicit, err == nil, tc.want, err)
			}
		})
	}
}
