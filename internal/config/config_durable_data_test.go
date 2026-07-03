package config_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

// runtime.fargate.durable_data is the operator assertion that a Fargate tier has
// durable, replica-shared app-data storage (S3 Files or a manually attached
// volume). It suppresses the durable-data guard. Default false.

func TestRuntime_Fargate_DurableDataParses(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: cloud
      runtime: fargate
  fargate:
    cluster: shiny-cluster
    task_definition: shiny-app:7
    container_name: app
    subnets: [subnet-a]
    task_cpu_units: 1024
    task_memory_mb: 2048
    control_plane_url: "https://cp.example.com"
    durable_data: true
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Runtime.Fargate.DurableData {
		t.Fatal("durable_data: true, want DurableData=true, got false")
	}
}

func TestRuntime_Fargate_DurableDataDefaultsFalse(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: cloud
      runtime: fargate
  fargate:
    cluster: shiny-cluster
    task_definition: shiny-app:7
    container_name: app
    subnets: [subnet-a]
    task_cpu_units: 1024
    task_memory_mb: 2048
    control_plane_url: "https://cp.example.com"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Fargate.DurableData {
		t.Fatal("durable_data omitted, want DurableData=false, got true")
	}
}
