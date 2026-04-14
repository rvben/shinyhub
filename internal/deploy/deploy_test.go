package deploy_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestExtractBundle(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "app.zip")
	if err := createTestBundle(zipPath, map[string]string{
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
	if err := createTestBundle(zipPath, map[string]string{
		"../escape.txt": "should not appear outside destDir",
	}); err != nil {
		t.Fatal(err)
	}

	destDir := filepath.Join(dir, "extracted")
	err := deploy.ExtractBundle(zipPath, destDir)
	if err == nil {
		t.Fatal("expected error for zip-slip entry, got nil")
	}

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
	if p2 < 20000 || p2 > 60000 {
		t.Errorf("p2 out of range: %d", p2)
	}
}

func TestAllocatePort_WrapAround(t *testing.T) {
	// Drive the counter to 60000, then verify the next call wraps back into range.
	deploy.SetPortCounterForTest(59999)
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
		Slug:      "test-deploy",
		BundleDir: dir,
		Command:   []string{"sleep", "30"},
		Manager:   mgr,
		Proxy:     prx,
		HealthCheck: func(port int, timeout time.Duration) error {
			return nil // no HTTP server in this test
		},
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

func TestBuildRCommand_NoRenv(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app.R"), []byte(""), 0644)

	cmd := deploy.BuildRCommand(dir, 8080)
	if len(cmd) == 0 {
		t.Fatal("expected non-empty command")
	}
	if cmd[0] != "Rscript" {
		t.Errorf("expected Rscript as first arg, got %s", cmd[0])
	}
	full := strings.Join(cmd, " ")
	if !strings.Contains(full, "shiny::runApp") {
		t.Errorf("expected shiny::runApp in command: %s", full)
	}
	if !strings.Contains(full, "8080") {
		t.Errorf("expected port 8080 in command: %s", full)
	}
}

func TestDetectAppType_Python(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app.py"), []byte(""), 0644)
	if deploy.DetectAppType(dir) != "python" {
		t.Error("expected python for app.py")
	}
}

func TestDetectAppType_R(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app.R"), []byte(""), 0644)
	if deploy.DetectAppType(dir) != "r" {
		t.Error("expected r for app.R")
	}
}

func TestDetectAppType_Unknown(t *testing.T) {
	dir := t.TempDir()
	if deploy.DetectAppType(dir) != "" {
		t.Error("expected empty string for unknown app type")
	}
}
