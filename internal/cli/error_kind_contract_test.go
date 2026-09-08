package cli

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// A flag-validation failure is the most retry-unsafe error the CLI can produce:
// the same flags always fail the same way. Reporting it as kind "internal" tells
// an automated caller the opposite - that something broke server-side and the
// call is worth retrying - so the kind carries real behavioural weight and is
// pinned here per command rather than left to the catch-all in classify().
func TestClientSideValidationErrorsAreKindValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string // substring the message must still carry
	}{
		{"replicas below one", []string{"apps", "set", "demo", "--replicas", "-1", "--yes"}, "--replicas"},
		{"max sessions out of range", []string{"apps", "set", "demo", "--max-sessions-per-replica", "5000", "--yes"}, "--max-sessions-per-replica"},
		{"min warm replicas out of range", []string{"apps", "set", "demo", "--min-warm-replicas", "-3", "--yes"}, "--min-warm-replicas"},
		{"warm spares out of range", []string{"apps", "set", "demo", "--warm-spares", "-2", "--yes"}, "--warm-spares"},
		{"hibernate timeout below minimum", []string{"apps", "set", "demo", "--hibernate-timeout", "-9", "--yes"}, "--hibernate-timeout"},
		{"autoscale target out of range", []string{"apps", "set", "demo", "--autoscale-target", "3", "--yes"}, "--autoscale-target"},
		{"autoscale min negative", []string{"apps", "set", "demo", "--autoscale-min", "-1", "--yes"}, "--autoscale-min"},
		{"tier malformed", []string{"apps", "set", "demo", "--tier", "nonsense", "--yes"}, "--tier"},
		{"logs tail out of range", []string{"apps", "logs", "demo", "--tail", "0"}, "--tail"},
		{"schedule missing command", []string{"schedule", "add", "demo", "--name", "x", "--cron", "0 * * * *"}, "--cmd"},

		// schedule add and schedule update each parse the command themselves, so
		// every one of these fails before a request is built. They were the last
		// flag rejections in this file still falling through to the catch-all.
		{"schedule add both command forms", []string{"schedule", "add", "demo", "--name", "x", "--cron", "0 * * * *", "--cmd", "Rscript job.R", "--cmd-json", `["Rscript","job.R"]`}, "--cmd-json"},
		{"schedule add unparseable command", []string{"schedule", "add", "demo", "--name", "x", "--cron", "0 * * * *", "--cmd", `Rscript "job.R`}, "--cmd"},
		{"schedule update both command forms", []string{"schedule", "update", "demo", "x", "--cmd", "Rscript job.R", "--cmd-json", `["Rscript","job.R"]`}, "--cmd-json"},
		{"schedule update timezone and clear together", []string{"schedule", "update", "demo", "x", "--timezone", "Europe/Amsterdam", "--clear-timezone"}, "--clear-timezone"},
		{"schedule update unparseable command", []string{"schedule", "update", "demo", "x", "--cmd", `Rscript "job.R`}, "--cmd"},
		{"schedule update malformed command json", []string{"schedule", "update", "demo", "x", "--cmd-json", "not json"}, "--cmd-json"},
		{"schedule update with no field flags", []string{"schedule", "update", "demo", "x"}, "at least one field flag"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, setResp := setupCLITest(t)
			setResp(200, `{}`)

			_, err := execCLI(t, tc.args...)
			if err == nil {
				t.Fatalf("expected an error for %v", tc.args)
			}
			kind, code := classify(err)
			if kind != KindValidation {
				t.Errorf("kind = %q, want %q (message: %v)", kind, KindValidation, err)
			}
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			// Reclassifying must not cost the operator the flag name they need
			// in order to fix the call.
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q lost the offending flag %q", err.Error(), tc.want)
			}
		})
	}
}

// A name that is simply absent is the most common operator typo there is, and it
// is permanent: the schedule will be just as missing on the retry that kind
// "internal" invites. These are detected client-side, by scanning a list the
// server returned rather than by a server 404, which is exactly why they used to
// miss the HTTP-status arm of classify() and land in the catch-all.
func TestAbsentResourceErrorsAreKindNotFound(t *testing.T) {
	t.Run("schedule name not in the app's list", func(t *testing.T) {
		_, _, setResp := setupCLITest(t)
		setResp(200, `{"items":[{"id":1,"name":"nightly"}]}`)

		_, err := execCLI(t, "schedule", "logs", "demo", "missing")
		if err == nil {
			t.Fatal("expected an error for a schedule name that does not exist")
		}
		if kind, code := classify(err); kind != KindNotFound || code != 1 {
			t.Errorf("kind/code = %q/%d, want %q/1 (message: %v)", kind, code, KindNotFound, err)
		}
		if !strings.Contains(err.Error(), `"missing"`) {
			t.Errorf("message %q does not name the schedule the operator asked for", err.Error())
		}
	})

	t.Run("schedule exists but has never run", func(t *testing.T) {
		// Two different absences reach the same command, so the handler has to
		// route: the schedule IS found, and only the run list is empty. Serving one
		// canned body for both paths would let this pass on the previous check.
		setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			if strings.HasSuffix(r.URL.Path, "/runs") {
				_, _ = w.Write([]byte(`{"items":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"id":1,"name":"nightly"}]}`))
		})

		_, err := execCLI(t, "schedule", "logs", "demo", "nightly")
		if err == nil {
			t.Fatal("expected an error for a schedule with no runs")
		}
		if kind, code := classify(err); kind != KindNotFound || code != 1 {
			t.Errorf("kind/code = %q/%d, want %q/1 (message: %v)", kind, code, KindNotFound, err)
		}
		if !strings.Contains(err.Error(), "no runs") {
			t.Errorf("message %q does not say the schedule has no runs", err.Error())
		}
	})
}

// A manifest path that does not exist is the caller's argument being wrong, and
// no retry fixes it. The command prints its own prose here (Reported), so the
// kind is what a machine reading the envelope has to go on.
func TestMissingFleetManifestIsKindValidation(t *testing.T) {
	setupCLITest(t)
	missing := filepath.Join(t.TempDir(), "definitely-not-here.toml")

	_, err := execCLI(t, "fleet", "validate", "-f", missing)
	if err == nil {
		t.Fatal("expected an error for a manifest path that does not exist")
	}
	if kind, code := classify(err); kind != KindValidation || code != 1 {
		t.Errorf("kind/code = %q/%d, want %q/1 (message: %v)", kind, code, KindValidation, err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("message %q does not name the path that was not found", err.Error())
	}
}

// deploy and plan share one source-resolution path, so a missing directory or a
// bad slug reaches the user through both. doctor and run --check already
// classified these correctly; these two did not, which made the kind depend on
// which command you happened to use.
func TestSourceResolutionErrorsAreKindValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"plan missing directory", []string{"plan"}, "missing directory argument"},
		{"plan invalid slug", []string{"plan", ".", "--slug", "Bad Slug!"}, "invalid slug"},
		{"plan git and dir together", []string{"plan", ".", "--git", "https://example.com/x.git"}, "cannot be used together"},
		{"plan branch without git", []string{"plan", ".", "--branch", "main"}, "--branch"},
		{"plan nonexistent source", []string{"plan", "./definitely-not-a-real-directory-xyz"}, "source"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, setResp := setupCLITest(t)
			setResp(200, `{}`)

			_, err := execCLI(t, tc.args...)
			if err == nil {
				t.Fatalf("expected an error for %v", tc.args)
			}
			if kind, code := classify(err); kind != KindValidation || code != 1 {
				t.Errorf("kind/code = %q/%d, want %q/1 (message: %v)", kind, code, KindValidation, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}
