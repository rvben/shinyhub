//go:build linux

package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/sandbox"
)

// TestRunSandboxedBuildStep_ConfinesWrites_Live proves, through the actual
// production wiring a build/hook command runs under — runSandboxedBuildStep
// -> sandboxedCommand -> a real re-exec of this test binary with argv[1] ==
// deploySandboxShimArg -> this package's init() -> sandbox.RunShim ->
// Landlock — that the command can write inside its own build dir but is
// denied writing to an unrelated directory (SEC-A1: the dependency-build
// and post-deploy-hook exec phases are now confined the same way the app
// process is, instead of running unsandboxed on the host).
//
// On a kernel without Landlock support, Apply is a graceful no-op (see
// internal/sandbox.Apply) and NO_NEW_PRIVS stays unset, so this test skips
// rather than failing — mirroring internal/sandbox.TestLandlockEnforces_Live,
// which verifies the same enforcement one layer down.
func TestRunSandboxedBuildStep_ConfinesWrites_Live(t *testing.T) {
	if !sandbox.Supported() {
		t.Skip("no isolation backend on this platform")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir for an off-allowlist target")
	}
	base, err := os.MkdirTemp(home, "shinyhub-deploy-sandbox-live")
	if err != nil {
		t.Skipf("home not writable, cannot stage an off-allowlist target: %v", err)
	}
	defer os.RemoveAll(base)

	appDir := filepath.Join(base, "app")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(appDir, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o770); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "bad")

	// One shell command: report NO_NEW_PRIVS (to detect whether Landlock is
	// actually active on this kernel), write inside the build dir (must
	// succeed), then write outside it (must be denied).
	script := "grep NoNewPrivs /proc/self/status; touch ok.txt; touch " + outsideFile
	out, runErr := runSandboxedBuildStep(context.Background(), appDir, []string{"sh", "-c", script})

	if !strings.Contains(string(out), "NoNewPrivs:\t1") {
		t.Skipf("Landlock not active on this kernel (NO_NEW_PRIVS not set): %s", out)
	}
	if _, statErr := os.Stat(filepath.Join(appDir, "ok.txt")); statErr != nil {
		t.Errorf("write inside the build dir should have succeeded: %v\noutput:\n%s", statErr, out)
	}
	if _, statErr := os.Stat(outsideFile); statErr == nil {
		t.Error("write outside the build dir must be denied by Landlock, but the file was created")
	}
	if runErr == nil {
		t.Error("expected the sandboxed command to report an error (the outside touch should fail), got nil")
	}
}
