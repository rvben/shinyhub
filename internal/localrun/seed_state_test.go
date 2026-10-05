package localrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/deploy"
)

func seedTestWorkspace(t *testing.T) (*workspace, *deploy.LaunchPlan, string) {
	t.Helper()
	source := scheduleFixture(t, `
[[schedule]]
name = "fetch"
cron = "0 * * * *"
deploy_trigger = "bundle_change"
cmd = "sh producer.sh"
`)
	script := `echo attempt >> "$COUNT"
if test "$FAIL" = 1; then exit 7; fi
echo data > "$SHINYHUB_APP_DATA/result"
`
	if err := os.WriteFile(filepath.Join(source, "producer.sh"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	w := &workspace{Root: t.TempDir(), BundleDir: source, DataDir: t.TempDir()}
	unlock, err := w.acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	lock, err := acquireDataLock(w.DataDir, true)
	if err != nil {
		t.Fatal(err)
	}
	w.dataLock = lock
	t.Cleanup(func() { lock.Close() })
	manifest, err := deploy.LoadManifest(source)
	if err != nil {
		t.Fatal(err)
	}
	count := filepath.Join(t.TempDir(), "count")
	return w, &deploy.LaunchPlan{Manifest: manifest}, count
}

func seedAttempts(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Count(data, []byte("attempt\n"))
}

func TestMissingSeedReusesManualSuccessAndInvalidatesFailedRefresh(t *testing.T) {
	w, plan, count := seedTestWorkspace(t)
	run := func(name, mode string, fail bool) error {
		env := []string{"COUNT=" + count}
		if fail {
			env = append(env, "FAIL=1")
		}
		return runSchedules(context.Background(), w, "sales", env, plan, name, mode, io.Discard, io.Discard)
	}
	if err := run("fetch", "never", false); err != nil {
		t.Fatal(err)
	}
	if err := run("", "missing", false); err != nil {
		t.Fatal(err)
	}
	if got := seedAttempts(t, count); got != 1 {
		t.Fatalf("manual success not reused: %d", got)
	}
	if err := run("fetch", "never", true); err == nil {
		t.Fatal("failed refresh succeeded")
	}
	if err := run("", "missing", false); err != nil {
		t.Fatal(err)
	}
	if got := seedAttempts(t, count); got != 3 {
		t.Fatalf("failed refresh did not invalidate success: %d", got)
	}
	if err := run("", "missing", false); err != nil {
		t.Fatal(err)
	}
	if got := seedAttempts(t, count); got != 3 {
		t.Fatalf("repaired data fetched again: %d", got)
	}
}

func TestMissingSeedCommandAndDataGenerationIdentity(t *testing.T) {
	w, plan, count := seedTestWorkspace(t)
	run := func() {
		t.Helper()
		if err := runSchedules(context.Background(), w, "sales", []string{"COUNT=" + count}, plan, "", "missing", io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	run()
	run()
	if got := seedAttempts(t, count); got != 1 {
		t.Fatal(got)
	}
	plan.Manifest.Schedules[0].Command = append(plan.Manifest.Schedules[0].Command, "new-command-argument")
	run()
	run()
	if got := seedAttempts(t, count); got != 2 {
		t.Fatalf("command change not initialized: %d", got)
	}
	if err := os.RemoveAll(w.DataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(w.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	run()
	run()
	if got := seedAttempts(t, count); got != 3 {
		t.Fatalf("replacement data reused old success: %d", got)
	}
	if err := runSchedules(context.Background(), w, "other-app", []string{"COUNT=" + count}, plan, "", "missing", io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := seedAttempts(t, count); got != 4 {
		t.Fatal("shared dir conflated app identities")
	}
}

func TestMissingSeedInterruptedAttemptRepairsEveryProducer(t *testing.T) {
	w, plan, count := seedTestWorkspace(t)
	second := plan.Manifest.Schedules[0]
	second.Name = "second"
	plan.Manifest.Schedules = append(plan.Manifest.Schedules, second)
	run := func() {
		t.Helper()
		if err := runSchedules(context.Background(), w, "sales", []string{"COUNT=" + count}, plan, "", "missing", io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	run()
	run()
	if got := seedAttempts(t, count); got != 2 {
		t.Fatal(got)
	}
	state, err := loadSeedState(w)
	if err != nil {
		t.Fatal(err)
	}
	state.InProgress = true // an attempt was admitted but the parent never recorded completion
	if err := saveSeedState(w, state); err != nil {
		t.Fatal(err)
	}
	run()
	run()
	if got := seedAttempts(t, count); got != 4 {
		t.Fatalf("uncertain writer did not invalidate all producers: %d", got)
	}
}

func TestMissingSeedTracksProducerReplacingItsDataDirectory(t *testing.T) {
	w, plan, count := seedTestWorkspace(t)
	script := `echo attempt >> "$COUNT"
rm -rf "$SHINYHUB_APP_DATA"
mkdir -p "$SHINYHUB_APP_DATA"
echo rebuilt > "$SHINYHUB_APP_DATA/result"
`
	if err := os.WriteFile(filepath.Join(w.BundleDir, "producer.sh"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := runSchedules(context.Background(), w, "sales", []string{"COUNT=" + count}, plan, "", "missing", io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if got := seedAttempts(t, count); got != 1 {
		t.Fatalf("replacement generation not recorded: %d", got)
	}
}

func TestInvalidSeedModeDoesNotCreateWorkspace(t *testing.T) {
	dir := scheduleFixture(t, "")
	state := filepath.Join(t.TempDir(), "state")
	err := Run(context.Background(), Options{BundleDir: dir, StateDir: state, SeedMode: "auto"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("invalid policy accepted")
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("invalid policy created state")
	}
}

func TestStartupSeedRejectsLaterProducerErasingEarlierGeneration(t *testing.T) {
	for _, skipFirst := range []bool{false, true} {
		t.Run(fmt.Sprint("skip-first-", skipFirst), func(t *testing.T) {
			w, plan, count := seedTestWorkspace(t)
			if skipFirst {
				if err := runSchedules(context.Background(), w, "sales", []string{"COUNT=" + count}, plan, "", "missing", io.Discard, io.Discard); err != nil {
					t.Fatal(err)
				}
			}
			script := `echo attempt >> "$COUNT"
rm -rf "$SHINYHUB_APP_DATA"
mkdir -p "$SHINYHUB_APP_DATA"
echo second > "$SHINYHUB_APP_DATA/second"
`
			if err := os.WriteFile(filepath.Join(w.BundleDir, "replace.sh"), []byte(script), 0o644); err != nil {
				t.Fatal(err)
			}
			second := plan.Manifest.Schedules[0]
			second.Name = "second"
			second.Command = []string{"sh", "replace.sh"}
			plan.Manifest.Schedules = append(plan.Manifest.Schedules, second)
			err := runSchedules(context.Background(), w, "sales", []string{"COUNT=" + count}, plan, "", "missing", io.Discard, io.Discard)
			var validation *ValidationError
			if !errors.As(err, &validation) || !strings.Contains(err.Error(), "fetch") || !strings.Contains(err.Error(), "invalidated") {
				t.Fatalf("startup admitted erased data: %v", err)
			}
			// The next missing run repairs the erased first producer and reuses the
			// second producer's current-generation success, rather than looping.
			if err := runSchedules(context.Background(), w, "sales", []string{"COUNT=" + count}, plan, "", "missing", io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(w.DataDir, "result")); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(w.DataDir, "second")); err != nil {
				t.Fatal(err)
			}
			if got := seedAttempts(t, count); got != 3 {
				t.Fatalf("repair attempts=%d", got)
			}
		})
	}
}
