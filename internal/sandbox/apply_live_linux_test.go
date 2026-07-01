//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const liveChildEnv = "SHINYHUB_SANDBOX_LIVE_CHILD"

// TestLandlockEnforces_Live proves, on a kernel that has Landlock, that the
// standard spec actually confines writes: the app dir is writable while an
// off-allowlist directory under the user's home (which is writable via ordinary
// DAC permissions, so Landlock is the only thing that can deny it) is refused,
// and NO_NEW_PRIVS is set. It re-execs the test binary as a sandboxed child.
//
// On a kernel without Landlock, Apply is a graceful no-op and NO_NEW_PRIVS stays
// 0, so the test skips rather than failing - the enforcement path can only be
// asserted where the kernel supports it (verified on a real 6.8 kernel).
func TestLandlockEnforces_Live(t *testing.T) {
	if os.Getenv(liveChildEnv) != "" {
		landlockLiveChild()
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir for an off-allowlist target")
	}
	base, err := os.MkdirTemp(home, "shinyhub-ll")
	if err != nil {
		t.Skipf("home not writable, cannot stage an off-allowlist target: %v", err)
	}
	defer os.RemoveAll(base)

	cmd := exec.Command(os.Args[0], "-test.run=^TestLandlockEnforces_Live$")
	cmd.Env = append(os.Environ(), liveChildEnv+"=1", "SHINYHUB_LL_BASE="+base)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandboxed child failed: %v\n%s", err, out)
	}
	line := lastLine(string(out))
	if strings.Contains(line, "NNP=0") {
		t.Skipf("Landlock not active on this kernel (NO_NEW_PRIVS not set): %s", line)
	}
	if !strings.Contains(line, "IN_OK=true") {
		t.Errorf("write to the app dir should succeed under standard: %s", line)
	}
	if !strings.Contains(line, "OUT_DENIED=true") {
		t.Errorf("write to an off-allowlist dir must be denied by Landlock: %s", line)
	}
}

// landlockLiveChild runs in the re-exec'd child: it applies the standard spec to
// itself, then reports NO_NEW_PRIVS and whether an in-allowlist and an
// off-allowlist write succeeded.
func landlockLiveChild() {
	base := os.Getenv("SHINYHUB_LL_BASE")
	appDir := filepath.Join(base, "app")
	outside := filepath.Join(base, "outside")
	_ = os.MkdirAll(appDir, 0o770)
	_ = os.MkdirAll(outside, 0o770)

	if err := Apply(ComputeSpec(LevelStandard, appDir, "")); err != nil {
		fmt.Println("CHILD_APPLY_ERR", err)
		os.Exit(3)
	}
	inOK := os.WriteFile(filepath.Join(appDir, "ok"), []byte("x"), 0o600) == nil
	outDenied := os.WriteFile(filepath.Join(outside, "bad"), []byte("x"), 0o600) != nil
	fmt.Printf("NNP=%d IN_OK=%v OUT_DENIED=%v\n", noNewPrivs(), inOK, outDenied)
	os.Exit(0)
}

// noNewPrivs reports the NoNewPrivs flag from /proc/self/status (1 when set).
// go-landlock sets it as a precondition of unprivileged Landlock, so it doubles
// as a signal that Landlock actually engaged.
func noNewPrivs() int {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "NoNewPrivs:") && strings.Contains(l, "1") {
			return 1
		}
	}
	return 0
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}
