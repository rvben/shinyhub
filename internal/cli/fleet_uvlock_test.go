package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/process"
)

// uvLockFleetServer is a server that answers the deploy preflight with the
// given problems and advertises the given capabilities. It records requests so
// a test can prove nothing was mutated.
func uvLockFleetServer(t *testing.T, caps string, problems []map[string]string) *[]capturedReq {
	t.Helper()
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/server-info":
			_, _ = w.Write([]byte(`{"version":"1.0.0","capabilities":` + caps + `}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == "POST" && r.URL.Path == "/api/apps/reporting/deploy-preflight":
			if problems == nil {
				problems = []map[string]string{}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"valid": len(problems) == 0, "isolation": "multiplex", "problems": problems})
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
	return reqs
}

// uvLockFleetTree writes a one-app fleet whose bundle ships the given lock.
func uvLockFleetTree(t *testing.T, lock string) string {
	t.Helper()
	file := writeFleetTree(t, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"reporting\"\nsource=\"./reporting\"\nvisibility=\"private\"\n",
		map[string]string{"reporting": ""})
	dir := filepath.Join(filepath.Dir(file), "reporting")
	mustWrite(t, filepath.Join(dir, "pyproject.toml"), lockTestPyproject)
	mustWrite(t, filepath.Join(dir, "uv.lock"), lock)
	return file
}

func staleLockMessage(t *testing.T) string {
	t.Helper()
	err := serverLockVerdict(t, lockTestPyproject, staleLockTestLock)
	if !errors.Is(err, process.ErrStaleLock) {
		t.Fatalf("fixture is not stale to the server: %v", err)
	}
	return err.Error()
}

const uvLockRefusingCaps = `{"deploy_preflight":true,"runtime_capabilities":true,"stale_uv_lock_refusal":true,"fleet_preconditions":true,"content_digest":true}`

// A server that refuses a stale uv.lock on upload has that refusal rehearsed at
// plan time: plan and apply stop with the upload's own message before any
// change, even though the preflight endpoint itself found nothing.
func TestFleetPlan_StaleUVLockRejectsBeforeAnyChange(t *testing.T) {
	reqs := uvLockFleetServer(t, uvLockRefusingCaps, nil)
	file := uvLockFleetTree(t, staleLockTestLock)
	want := `app "reporting": ` + staleLockMessage(t)

	for _, verb := range [][]string{{"plan"}, {"apply", "--yes"}} {
		*reqs = nil
		args := append([]string{"fleet"}, verb...)
		stdout, stderr, err := execCLISplit(t, append(args, "-f", file)...)
		if exitCode(err) != 1 {
			t.Fatalf("%s exit=%d err=%v\nstdout=%s\nstderr=%s", verb[0], exitCode(err), err, stdout, stderr)
		}
		for _, s := range []string{want, "1 problem(s) the server would reject at deploy. Nothing was changed."} {
			if !strings.Contains(stderr, s) {
				t.Errorf("%s stderr lacks %q:\n%s", verb[0], s, stderr)
			}
		}
		for _, r := range *reqs {
			if r.Method != "GET" && r.Path != "/api/apps/reporting/deploy-preflight" {
				t.Errorf("%s mutated the server after a stale-lock rejection: %s %s", verb[0], r.Method, r.Path)
			}
		}
	}
}

// The stale lock is listed beside the endpoint's own rejection, since the
// deploy cannot succeed until both are fixed.
func TestFleetPlan_StaleUVLockListedBesideServerProblem(t *testing.T) {
	uvLockFleetServer(t, uvLockRefusingCaps, []map[string]string{{"stage": "deploy", "message": groupedProducerRejection}})
	file := uvLockFleetTree(t, staleLockTestLock)

	_, stderr, err := execCLISplit(t, "fleet", "plan", "-f", file)
	if exitCode(err) != 1 {
		t.Fatalf("plan exit=%d err=%v\nstderr=%s", exitCode(err), err, stderr)
	}
	for _, s := range []string{`app "reporting": ` + groupedProducerRejection, `app "reporting": ` + staleLockMessage(t), "2 problem(s)"} {
		if !strings.Contains(stderr, s) {
			t.Errorf("plan stderr lacks %q:\n%s", s, stderr)
		}
	}
}

func TestFleetPlan_StaleUVLockOnlyWhereTheServerRefusesIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps string
		lock string
	}{
		// An older server accepts the upload, so the plan must not refuse it.
		{name: "server without the refusal", caps: `{"deploy_preflight":true,"runtime_capabilities":true}`, lock: staleLockTestLock},
		{name: "current lock", caps: uvLockRefusingCaps, lock: currentLockTestLock},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uvLockFleetServer(t, tc.caps, nil)
			file := uvLockFleetTree(t, tc.lock)
			stdout, stderr, err := execCLISplit(t, "fleet", "plan", "-f", file)
			if err != nil {
				t.Fatalf("plan: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
			}
			if strings.Contains(stderr, "uv.lock") {
				t.Errorf("plan reported the lock:\n%s", stderr)
			}
			if !strings.Contains(stdout, "create") {
				t.Errorf("plan did not report the create:\n%s", stdout)
			}
		})
	}
}

// The verdict is taken from the archive the deploy uploads, not from the
// source directory: a stale lock kept out by .shinyhubignore never reaches the
// server, while one a [[bundle_file]] input supplies does.
func TestFleetPlan_StaleUVLockJudgesTheUpload(t *testing.T) {
	t.Run("ignored lock", func(t *testing.T) {
		uvLockFleetServer(t, uvLockRefusingCaps, nil)
		file := uvLockFleetTree(t, staleLockTestLock)
		mustWrite(t, filepath.Join(filepath.Dir(file), "reporting", ".shinyhubignore"), "uv.lock\n")
		if stdout, stderr, err := execCLISplit(t, "fleet", "plan", "-f", file); err != nil {
			t.Fatalf("plan refused a lock the upload leaves out: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
		}
	})
	t.Run("lock from a bundle input", func(t *testing.T) {
		uvLockFleetServer(t, uvLockRefusingCaps, nil)
		root := t.TempDir()
		mustWrite(t, filepath.Join(root, "reporting", "app.py"), "from shiny import App\n")
		mustWrite(t, filepath.Join(root, "reporting", "pyproject.toml"), lockTestPyproject)
		mustWrite(t, filepath.Join(root, "_locks", "uv.lock"), staleLockTestLock)
		file := writeFleetManifest(t, root, `fleet_id = "eu"

[[bundle_file]]
from = "_locks/uv.lock"
to = "uv.lock"
consumers = ["reporting"]

[[app]]
slug = "reporting"
source = "./reporting"
visibility = "private"
`)
		_, stderr, err := execCLISplit(t, "fleet", "plan", "-f", file)
		if exitCode(err) != 1 || !strings.Contains(stderr, `app "reporting": `+staleLockMessage(t)) {
			t.Fatalf("plan exit=%d, want the stale-lock refusal:\n%s", exitCode(err), stderr)
		}
	})
}
