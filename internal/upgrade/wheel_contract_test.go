package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The PyPI wheel launches the embedded Go binary via os.execv, and the argv[0]
// it passes becomes os.Args[0] in the server - the exact string a SIGHUP
// handoff re-execs (tableflip resolves os.Args[0] with exec.LookPath). The
// launcher must therefore pass the binary's own absolute path: a bare name
// resolves only if a "shinyhub" happens to be on the service's PATH, which
// silently breaks zero-downtime upgrades on pip/uv installs.
func TestWheelLauncher_PassesAbsoluteArgv0(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "packaging", "python", "src", "shinyhub", "__main__.py"))
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read wheel launcher: %v", err)
	}
	launcher := string(src)

	const want = `os.execv(str(binary), [str(binary), *sys.argv[1:]])`
	if !strings.Contains(launcher, want) {
		t.Fatalf("wheel launcher %s must exec the binary with its own path as argv[0]:\nwant line: %s", path, want)
	}
	if bad := `["shinyhub"`; strings.Contains(launcher, bad) {
		t.Fatalf("wheel launcher %s passes a bare argv[0] (%s...): SIGHUP handoff cannot re-exec a bare name", path, bad)
	}
}
