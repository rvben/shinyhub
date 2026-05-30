package api_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResourceDefaults_NoDockerDirectCallSites asserts that no deploy-path
// handler reads Docker defaults directly. All 8 call sites must use
// DefaultResourcesForApp so Fargate tiers get the right defaults.
func TestResourceDefaults_NoDockerDirectCallSites(t *testing.T) {
	// Files that previously contained the Docker-direct pattern.
	files := []string{
		"apps.go",
		"env.go",
		"redeploy.go",
		"scale.go",
	}
	dir := filepath.Join(".") // test runs in internal/api/
	for _, name := range files {
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(b)
		if strings.Contains(src, "cfg.Runtime.Docker.DefaultMemoryMB") {
			t.Errorf("%s: still contains cfg.Runtime.Docker.DefaultMemoryMB; use DefaultResourcesForApp instead", name)
		}
		if strings.Contains(src, "cfg.Runtime.Docker.DefaultCPUPercent") {
			t.Errorf("%s: still contains cfg.Runtime.Docker.DefaultCPUPercent; use DefaultResourcesForApp instead", name)
		}
		if strings.Contains(src, "DefaultResourcesForTier(s.cfg.Runtime.DefaultTierName())") {
			t.Errorf("%s: still calls DefaultResourcesForTier(DefaultTierName()); use DefaultResourcesForApp(app) for placement-aware defaults", name)
		}
	}
}

func TestResourceDefaults_MainGoDeployFn(t *testing.T) {
	// cmd/shinyhub/main.go deployFn must not read Docker defaults directly
	// and must use DefaultResourcesForApp for placement-aware resolution.
	path := filepath.Join("..", "..", "cmd", "shinyhub", "main.go")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	src := string(b)
	if strings.Contains(src, "cfg.Runtime.Docker.DefaultMemoryMB") {
		t.Error("main.go deployFn: still contains cfg.Runtime.Docker.DefaultMemoryMB; use DefaultResourcesForApp")
	}
	if strings.Contains(src, "cfg.Runtime.Docker.DefaultCPUPercent") {
		t.Error("main.go deployFn: still contains cfg.Runtime.Docker.DefaultCPUPercent; use DefaultResourcesForApp")
	}
	if strings.Contains(src, "DefaultResourcesForTier(cfg.Runtime.DefaultTierName())") {
		t.Error("main.go deployFn: still calls DefaultResourcesForTier(DefaultTierName()); use DefaultResourcesForApp(app)")
	}
}

// TestResourceDefaults_AllSitesUseAppAwarePath asserts that every deploy-path
// handler uses DefaultResourcesForApp (not the tier-name-direct form). This
// catches any new call site that skips the placement-aware helper.
func TestResourceDefaults_AllSitesUseAppAwarePath(t *testing.T) {
	files := map[string]string{
		"apps.go":    filepath.Join(".", "apps.go"),
		"env.go":     filepath.Join(".", "env.go"),
		"redeploy.go": filepath.Join(".", "redeploy.go"),
		"scale.go":   filepath.Join(".", "scale.go"),
	}
	for name, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(b)
		if !strings.Contains(src, "DefaultResourcesForApp(app)") {
			t.Errorf("%s: does not contain DefaultResourcesForApp(app); all deploy-path sites must use the placement-aware helper", name)
		}
	}

	// main.go uses cfg. (not s.cfg.) prefix.
	mainPath := filepath.Join("..", "..", "cmd", "shinyhub", "main.go")
	b, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(b), "DefaultResourcesForApp(app)") {
		t.Error("main.go deployFn: does not contain DefaultResourcesForApp(app)")
	}
}

func TestPatchApp_FargateRejection_ContractExists(t *testing.T) {
	// Assert that handlePatchApp contains a write-time rejection for single-tier Fargate.
	// allTiersFargate is the actual gate function; it has no other call site in
	// apps.go, so its absence means the rejection block was removed.
	path := filepath.Join(".", "apps.go")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read apps.go: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "allTiersFargate") {
		t.Error("apps.go handlePatchApp: missing allTiersFargate gate (write-time Fargate rejection)")
	}
	if !strings.Contains(src, "TaskMemoryMB") {
		t.Error("apps.go handlePatchApp: missing TaskMemoryMB ceiling check")
	}
}
