package deploy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhost/internal/deploy"
	"github.com/rvben/shinyhost/internal/process"
	"github.com/rvben/shinyhost/internal/proxy"
)

func TestExtractBundle(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "app.zip")
	if err := deploy.CreateTestBundle(zipPath, map[string]string{
		"app.py":           "# shiny app",
		"requirements.txt": "shiny",
	}); err != nil {
		t.Fatal(err)
	}

	destDir := filepath.Join(dir, "extracted")
	if err := deploy.ExtractBundle(zipPath, destDir); err != nil {
		t.Fatalf("extract: %v", err)
	}

	if _, err := os.Stat(filepath.Join(destDir, "app.py")); err != nil {
		t.Error("expected app.py to be extracted")
	}
}

func TestExtractBundle_ZipSlip(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "malicious.zip")
	// Attempt path traversal via a ../../../etc/passwd-style entry name.
	if err := deploy.CreateTestBundle(zipPath, map[string]string{
		"../escape.txt": "should not appear outside destDir",
	}); err != nil {
		t.Fatal(err)
	}

	destDir := filepath.Join(dir, "extracted")
	// ExtractBundle must either reject the entry or sanitize the path so that
	// the file ends up inside destDir, never outside it.
	_ = deploy.ExtractBundle(zipPath, destDir)

	// The file must not have escaped to the parent of destDir.
	escaped := filepath.Join(dir, "escape.txt")
	if _, err := os.Stat(escaped); err == nil {
		t.Error("zip-slip: file escaped destDir — path traversal not prevented")
	}
}

func TestAllocatePort(t *testing.T) {
	p1 := deploy.AllocatePort()
	p2 := deploy.AllocatePort()
	if p1 == p2 {
		t.Error("expected different ports")
	}
	if p1 < 20000 || p1 > 60000 {
		t.Errorf("port out of range: %d", p1)
	}
}

func TestAllocatePort_WrapAround(t *testing.T) {
	// Drive the counter to 60000, then verify the next call wraps back into range.
	deploy.SetPortCounter(59999)
	p1 := deploy.AllocatePort() // 60000 — last valid
	p2 := deploy.AllocatePort() // should wrap to 20001 (or 20000 sentinel + 1)
	if p1 != 60000 {
		t.Errorf("expected 60000, got %d", p1)
	}
	if p2 < 20000 || p2 > 60000 {
		t.Errorf("wrapped port out of range: %d", p2)
	}
}

func TestDeploy_CommandOnly(t *testing.T) {
	mgr := process.NewManager()
	prx := proxy.New()

	dir := t.TempDir()

	params := deploy.Params{
		Slug:            "test-deploy",
		BundleDir:       dir,
		Command:         []string{"sleep", "30"},
		Manager:         mgr,
		Proxy:           prx,
		SkipHealthCheck: true,
	}
	info, err := deploy.Run(params)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	defer mgr.Stop("test-deploy")

	if info.Port <= 0 {
		t.Errorf("expected valid port, got %d", info.Port)
	}
	if info.PID <= 0 {
		t.Errorf("expected valid PID, got %d", info.PID)
	}
}
