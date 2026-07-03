package fargate

import "testing"

// TierHasDurableData reports whether app-data on this Fargate tier survives task
// restart/hibernation and is shared across replicas. Bare Fargate storage is
// task-local ephemeral scratch, so it is false unless the operator configured a
// durable backend (surfaced to the runtime as Config.DurableData).

func TestTierHasDurableData_BareFargateIsEphemeral(t *testing.T) {
	r := New(&fakeECS{}, testCfg(), nil)
	if r.TierHasDurableData() {
		t.Fatal("bare Fargate has ephemeral task-local storage: want false, got true")
	}
}

func TestTierHasDurableData_DurableBackendConfigured(t *testing.T) {
	cfg := testCfg()
	cfg.DurableData = true
	r := New(&fakeECS{}, cfg, nil)
	if !r.TierHasDurableData() {
		t.Fatal("durable backend configured: want true, got false")
	}
}

func TestTierHasDurableData_S3FilesConfigured(t *testing.T) {
	cfg := testCfg()
	cfg.S3Files = S3FilesMount{
		FileSystemArn: "arn:aws:s3files:us-east-1:123456789012:file-system/fs-abc",
		MountPath:     "/app/bundle/data",
	}
	r := New(&fakeECS{}, cfg, nil)
	if !r.TierHasDurableData() {
		t.Fatal("s3files backend configured: want durable=true, got false")
	}
}
