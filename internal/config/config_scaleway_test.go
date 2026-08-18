package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

func writeScalewayCfg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const scalewayValidSecret = "auth:\n  secret: \"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\"\n"

func TestLoadScalewayServerlessRuntime(t *testing.T) {
	p := writeScalewayCfg(t, scalewayValidSecret+`
runtime:
  tiers:
    - name: serverless
      runtime: scaleway_serverless
  scaleway:
    region: nl-ams
    project_id: project-id
    namespace_id: namespace-id
    image: rg.nl-ams.scw.cloud/shinyhub/runner:latest
    name_prefix: demo
    control_plane_url: https://demo.shinyhub.dev
    bundle_token_ttl: 12m
    private_network_id: private-network-id
    default_memory_mb: 512
    default_mvcpu: 250
`)
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, ok := cfg.Runtime.RuntimeForTier("serverless"); !ok || got != "scaleway_serverless" {
		t.Fatalf("tier runtime = %q,%v", got, ok)
	}
	s := cfg.Runtime.Scaleway
	if s.Region != "nl-ams" || s.ProjectID != "project-id" || s.NamespaceID != "namespace-id" || s.Image == "" {
		t.Fatalf("Scaleway config = %+v", s)
	}
	if s.BundleTokenTTL != 12*time.Minute || s.DefaultMemoryMB != 512 || s.DefaultMVCpu != 250 {
		t.Fatalf("Scaleway defaults = %+v", s)
	}
	if memory, cpu := cfg.Runtime.DefaultResourcesForTier("serverless"); memory != 512 || cpu != 25 {
		t.Fatalf("tier defaults = %d MB/%d%%, want 512/25", memory, cpu)
	}
}

func TestLoadScalewayServerlessRequiresSecureCompleteConfig(t *testing.T) {
	tests := map[string]string{
		"namespace":         "project_id: p\nimage: rg/image:tag\ncontrol_plane_url: https://demo.example",
		"image":             "project_id: p\nnamespace_id: n\ncontrol_plane_url: https://demo.example",
		"https control URL": "project_id: p\nnamespace_id: n\nimage: rg/image:tag\ncontrol_plane_url: http://demo.example",
		"memory range":      "project_id: p\nnamespace_id: n\nimage: rg/image:tag\ncontrol_plane_url: https://demo.example\ndefault_memory_mb: 64",
		"cpu range":         "project_id: p\nnamespace_id: n\nimage: rg/image:tag\ncontrol_plane_url: https://demo.example\ndefault_mvcpu: 10",
	}
	for name, block := range tests {
		t.Run(name, func(t *testing.T) {
			p := writeScalewayCfg(t, scalewayValidSecret+"\nruntime:\n  tiers:\n    - name: serverless\n      runtime: scaleway_serverless\n  scaleway:\n    region: nl-ams\n"+indent(block, 4)+"\n")
			_, err := config.Load(p)
			if err == nil || !strings.Contains(err.Error(), "runtime.scaleway") {
				t.Fatalf("error = %v, want runtime.scaleway validation", err)
			}
		})
	}
}

func TestLoadScalewayServerlessUsesStandardSDKEnvironment(t *testing.T) {
	t.Setenv("SCW_DEFAULT_REGION", "fr-par")
	t.Setenv("SCW_DEFAULT_PROJECT_ID", "project-from-environment")
	t.Setenv("SHINYHUB_RUNTIME_SCALEWAY_DURABLE_DATA", "true")
	p := writeScalewayCfg(t, scalewayValidSecret+`
runtime:
  tiers:
    - name: serverless
      runtime: scaleway_serverless
  scaleway:
    region: nl-ams
    project_id: project-from-file
    namespace_id: namespace-id
    image: rg.fr-par.scw.cloud/shinyhub/runner:latest
    control_plane_url: https://demo.shinyhub.dev
`)

	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Scaleway.Region != "fr-par" || cfg.Runtime.Scaleway.ProjectID != "project-from-environment" {
		t.Fatalf("standard SDK environment not applied: %+v", cfg.Runtime.Scaleway)
	}
	if !cfg.Runtime.Scaleway.DurableData {
		t.Fatal("SHINYHUB_RUNTIME_SCALEWAY_DURABLE_DATA was not applied")
	}
}

func indent(value string, spaces int) string {
	pad := strings.Repeat(" ", spaces)
	return pad + strings.ReplaceAll(value, "\n", "\n"+pad)
}
