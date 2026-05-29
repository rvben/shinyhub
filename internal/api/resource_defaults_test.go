package api_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResourceDefaults_NoDockerDirectCallSites asserts that no deploy-path
// handler reads Docker defaults directly. All 8 call sites must use
// DefaultResourcesForTier so Fargate tiers get the right defaults.
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
			t.Errorf("%s: still contains cfg.Runtime.Docker.DefaultMemoryMB; use DefaultResourcesForTier instead", name)
		}
		if strings.Contains(src, "cfg.Runtime.Docker.DefaultCPUPercent") {
			t.Errorf("%s: still contains cfg.Runtime.Docker.DefaultCPUPercent; use DefaultResourcesForTier instead", name)
		}
	}
}

func TestResourceDefaults_MainGoDeployFn(t *testing.T) {
	// cmd/shinyhub/main.go deployFn must not read Docker defaults directly.
	path := filepath.Join("..", "..", "cmd", "shinyhub", "main.go")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	src := string(b)
	if strings.Contains(src, "cfg.Runtime.Docker.DefaultMemoryMB") {
		t.Error("main.go deployFn: still contains cfg.Runtime.Docker.DefaultMemoryMB; use DefaultResourcesForTier")
	}
	if strings.Contains(src, "cfg.Runtime.Docker.DefaultCPUPercent") {
		t.Error("main.go deployFn: still contains cfg.Runtime.Docker.DefaultCPUPercent; use DefaultResourcesForTier")
	}
}

func TestPatchApp_FargateRejection_ContractExists(t *testing.T) {
	// Assert that handlePatchApp contains a write-time rejection for single-tier Fargate.
	// This guards against someone silently removing the check.
	path := filepath.Join(".", "apps.go")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read apps.go: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "DefaultTierName") {
		t.Error("apps.go handlePatchApp: missing DefaultTierName call (write-time Fargate rejection)")
	}
	if !strings.Contains(src, "TaskMemoryMB") {
		t.Error("apps.go handlePatchApp: missing TaskMemoryMB ceiling check")
	}
}
