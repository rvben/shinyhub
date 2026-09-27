package deploy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/deploy"
)

// The result cache expression must run before runApp: runApp never returns,
// so anything after it is dead code.
func TestResolveLaunch_R_SetsTheResultCacheBeforeRunApp(t *testing.T) {
	for _, reload := range []bool{false, true} {
		plan, err := deploy.ResolveLaunch(renvActivatedBundle(t), deploy.LaunchOptions{
			Port: 9300, BindHost: "127.0.0.1", Reload: reload,
		})
		if err != nil {
			t.Fatalf("ResolveLaunch: %v", err)
		}
		expr := rExpr(t, plan.Command)
		cache := strings.Index(expr, deploy.RResultCacheExpr)
		runApp := strings.Index(expr, "shiny::runApp(")
		if cache < 0 || runApp < 0 || cache > runApp {
			t.Fatalf("reload=%v: result cache expression missing or after runApp: %s", reload, expr)
		}
	}
}

// runRCacheProbe evaluates RResultCacheExpr in a real R session and reports
// which cache Shiny would use: "none", "memory:<class>" or "disk:<dir>".
func runRCacheProbe(t *testing.T, dir string, env []string, preamble string) string {
	t.Helper()
	probe := preamble + deploy.RResultCacheExpr + ` c <- shiny::getShinyOption("cache"); ` +
		`cat(if (is.null(c)) "none" else if (inherits(c, "cache_disk")) paste0("disk:", c$info()$dir) else paste0("memory:", class(c)[1]))`
	cmd := exec.Command("Rscript", "--vanilla", "-e", probe)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("Rscript: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestRResultCacheExpr_InRealR(t *testing.T) {
	if _, err := exec.LookPath("Rscript"); err != nil {
		t.Skip("Rscript not installed")
	}
	if err := exec.Command("Rscript", "--vanilla", "-e", `stopifnot(requireNamespace("shiny", quietly=TRUE), requireNamespace("cachem", quietly=TRUE))`).Run(); err != nil {
		t.Skip("shiny or cachem not installed")
	}
	dir := t.TempDir()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ns := filepath.Join(root, "demo", "d1")
	if err := os.MkdirAll(ns, 0o750); err != nil {
		t.Fatal(err)
	}

	t.Run("points Shiny at the namespace", func(t *testing.T) {
		got := runRCacheProbe(t, dir, []string{"SHINYHUB_CACHE_DIR=" + ns, "SHINYHUB_CACHE_MAX_MB=8"}, "")
		if got != "disk:"+ns {
			t.Fatalf("cache = %q, want disk:%s", got, ns)
		}
	})
	t.Run("does nothing without a namespace", func(t *testing.T) {
		got := runRCacheProbe(t, dir, []string{"SHINYHUB_CACHE_DIR="}, "")
		if got != "none" {
			t.Fatalf("cache = %q, want none", got)
		}
	})
	t.Run("keeps a cache the app already chose", func(t *testing.T) {
		got := runRCacheProbe(t, dir, []string{"SHINYHUB_CACHE_DIR=" + ns},
			`shiny::shinyOptions(cache = cachem::cache_mem()); `)
		if got != "memory:cache_mem" {
			t.Fatalf("cache = %q, want the app's own cache_mem", got)
		}
	})
	t.Run("a file is not used as the cache", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := runRCacheProbe(t, dir, []string{"SHINYHUB_CACHE_DIR=" + file}, ""); got != "none" {
			t.Fatalf("cache = %q, want none", got)
		}
	})
	t.Run("a read-only directory is not used as the cache", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes through directory permissions")
		}
		ro := filepath.Join(t.TempDir(), "ro")
		if err := os.Mkdir(ro, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
		if got := runRCacheProbe(t, dir, []string{"SHINYHUB_CACHE_DIR=" + ro}, ""); got != "none" {
			t.Fatalf("cache = %q, want none: cachem fails every write there", got)
		}
	})
}
