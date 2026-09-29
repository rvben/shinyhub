package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/bundle"
	"github.com/rvben/shinyhub/internal/process"
)

// lockTestPyproject declares six; staleLockTestLock was locked when the project
// declared idna instead, so it no longer records what the project declares and
// the server refuses it on upload. currentLockTestLock is its matched pair.
const lockTestPyproject = `[project]
name = "lock-app"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = ["six"]
`

const staleLockTestLock = `version = 1
revision = 3
requires-python = ">=3.12"

[[package]]
name = "lock-app"
version = "0.1.0"
source = { virtual = "." }
dependencies = [
    { name = "idna" },
]

[package.metadata]
requires-dist = [{ name = "idna" }]
`

const currentLockTestLock = `version = 1
revision = 3
requires-python = ">=3.12"

[[package]]
name = "lock-app"
version = "0.1.0"
source = { virtual = "." }
dependencies = [
    { name = "six" },
]

[package.metadata]
requires-dist = [{ name = "six" }]
`

// lockTestApp writes a deployable Python app with the given files beside
// app.py; an empty body leaves that file out.
func lockTestApp(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "lock-app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files["app.py"] = "# shiny app\n"
	for name, body := range files {
		if body == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// serverLockVerdict is the check the upload handler runs on the extracted
// bundle, so the CLI's verdict can be compared with it word for word.
func serverLockVerdict(t *testing.T, pyproject, lock string) error {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"pyproject.toml": pyproject, "uv.lock": lock} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return process.CheckLockCurrent(dir)
}

func TestCheckSourceLockMatchesTheServerVerdict(t *testing.T) {
	want := serverLockVerdict(t, lockTestPyproject, staleLockTestLock)
	if !errors.Is(want, process.ErrStaleLock) {
		t.Fatalf("fixture is not stale to the server: %v", want)
	}

	for _, tc := range []struct {
		name   string
		files  map[string]string
		inputs []bundle.FileInputSnapshot
		stale  bool
	}{
		{name: "stale lock", files: map[string]string{"pyproject.toml": lockTestPyproject, "uv.lock": staleLockTestLock}, stale: true},
		{name: "current lock", files: map[string]string{"pyproject.toml": lockTestPyproject, "uv.lock": currentLockTestLock}},
		{name: "no lock", files: map[string]string{"pyproject.toml": lockTestPyproject}},
		{name: "lock without pyproject", files: map[string]string{"requirements.txt": "six\n", "uv.lock": staleLockTestLock}},
		// A lock .shinyhubignore keeps out of the upload never reaches the
		// server, so it cannot be refused.
		{name: "ignored stale lock", files: map[string]string{"pyproject.toml": lockTestPyproject, "uv.lock": staleLockTestLock, ".shinyhubignore": "uv.lock\n"}},
		// A bundle input lands in the upload, so a stale lock it supplies is
		// refused like a checked-in one.
		{
			name:   "stale lock from a bundle input",
			files:  map[string]string{"pyproject.toml": lockTestPyproject},
			inputs: []bundle.FileInputSnapshot{{From: "locks/uv.lock", To: "uv.lock", Mode: 0o644, Data: []byte(staleLockTestLock)}},
			stale:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSourceLock(lockTestApp(t, tc.files), tc.inputs)
			if !tc.stale {
				if err != nil {
					t.Fatalf("checkSourceLock = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, process.ErrStaleLock) {
				t.Fatalf("checkSourceLock = %v, want ErrStaleLock", err)
			}
			if err.Error() != want.Error() {
				t.Fatalf("message differs from the server's refusal:\n got %q\nwant %q", err, want)
			}
		})
	}
}

func TestRunCheckRefusesStaleLockWithTheServerMessage(t *testing.T) {
	isolatedCredentials(t)
	dir := lockTestApp(t, map[string]string{"pyproject.toml": lockTestPyproject, "uv.lock": staleLockTestLock})
	stateDir := filepath.Join(t.TempDir(), "state")

	_, stderr, err := execCLISplit(t, "run", dir, "--check", "--no-reload", "--state-dir", stateDir)
	if err == nil {
		t.Fatal("run --check accepted a uv.lock the server refuses")
	}
	kind, code := classify(err)
	if kind != KindValidation || code != 1 {
		t.Fatalf("classify = (%s, %d), want (validation, 1): %v", kind, code, err)
	}
	want := serverLockVerdict(t, lockTestPyproject, staleLockTestLock)
	if !strings.Contains(err.Error(), want.Error()) {
		t.Fatalf("error = %q, want the server's %q (stderr %q)", err, want, stderr)
	}
	// The refusal is part of the read-only preflight: nothing is created.
	if _, statErr := os.Stat(stateDir); !os.IsNotExist(statErr) {
		t.Fatalf("state dir created before the refusal: %v", statErr)
	}
}

func TestDoctorUVLockCheck(t *testing.T) {
	want := serverLockVerdict(t, lockTestPyproject, staleLockTestLock)
	for _, tc := range []struct {
		name       string
		files      map[string]string
		wantStatus string
	}{
		{name: "stale", files: map[string]string{"pyproject.toml": lockTestPyproject, "uv.lock": staleLockTestLock}, wantStatus: "fail"},
		{name: "current", files: map[string]string{"pyproject.toml": lockTestPyproject, "uv.lock": currentLockTestLock}, wantStatus: "pass"},
		{name: "ignored", files: map[string]string{"pyproject.toml": lockTestPyproject, "uv.lock": staleLockTestLock, ".shinyhubignore": "uv.lock\n"}, wantStatus: "pass"},
		{name: "absent", files: map[string]string{"requirements.txt": "shiny\n"}, wantStatus: "pass"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedCredentials(t)
			stubDoctorRuntime(t)
			dir := lockTestApp(t, tc.files)

			stdout, _, err := execCLISplit(t, "doctor", dir, "--local", "--output", "json")
			if (err != nil) != (tc.wantStatus == "fail") {
				t.Fatalf("doctor err = %v, want failure only for a stale lock", err)
			}
			report := decodeDoctorReport(t, stdout)
			check := doctorCheckNamed(t, report, "uv-lock")
			if check.Status != tc.wantStatus {
				t.Fatalf("uv-lock = %+v, want status %s", check, tc.wantStatus)
			}
			if tc.wantStatus == "fail" && check.Detail != want.Error() {
				t.Fatalf("uv-lock detail = %q, want the server's %q", check.Detail, want)
			}
		})
	}
}
