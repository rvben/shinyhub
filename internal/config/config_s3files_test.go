package config_test

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

// runtime.fargate.s3files is the managed durable-data backend: an S3 Files file
// system mounted per-app. When configured, the Fargate tier is durable.

func TestRuntime_Fargate_S3FilesParses(t *testing.T) {
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
    s3files:
      file_system_arn: "arn:aws:s3files:us-east-1:123456789012:file-system/fs-abc"
      root_directory: "/apps"
      transit_encryption_port: 2999
      mount_path: "/app/bundle/data"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := cfg.Runtime.Fargate.S3Files
	if s.FileSystemArn != "arn:aws:s3files:us-east-1:123456789012:file-system/fs-abc" {
		t.Errorf("FileSystemArn = %q", s.FileSystemArn)
	}
	if s.RootDirectory != "/apps" {
		t.Errorf("RootDirectory = %q", s.RootDirectory)
	}
	if s.TransitEncryptionPort != 2999 {
		t.Errorf("TransitEncryptionPort = %d", s.TransitEncryptionPort)
	}
	if s.MountPath != "/app/bundle/data" {
		t.Errorf("MountPath = %q", s.MountPath)
	}
	if !s.Configured() {
		t.Error("Configured() = false, want true when file_system_arn is set")
	}
}

func TestRuntime_Fargate_S3FilesRejectsBadArn(t *testing.T) {
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
    s3files:
      file_system_arn: "not-an-arn"
`)
	_, err := config.Load(path)
	if err == nil || !strings.Contains(err.Error(), "file_system_arn") {
		t.Fatalf("want file_system_arn validation error, got: %v", err)
	}
}

func TestRuntime_Fargate_S3FilesRejectsRelativeMountPath(t *testing.T) {
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
    s3files:
      file_system_arn: "arn:aws:s3files:us-east-1:123456789012:file-system/fs-abc"
      mount_path: "relative/data"
`)
	_, err := config.Load(path)
	if err == nil || !strings.Contains(err.Error(), "mount_path") {
		t.Fatalf("want mount_path validation error, got: %v", err)
	}
}

func TestRuntime_Fargate_S3FilesUnsetNotConfigured(t *testing.T) {
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
	if cfg.Runtime.Fargate.S3Files.Configured() {
		t.Error("Configured() = true with no s3files block, want false")
	}
}
