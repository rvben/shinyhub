package fargate

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
)

// buildTaskDefInputForTest returns a registration input cloned from baseTaskDef
// (container "app", no volumes/mount points), the starting point addS3FilesMount
// mutates.
func buildTaskDefInputForTest() *ecs.RegisterTaskDefinitionInput {
	in, err := buildTaskDefInput(baseTaskDef(), "fam", "app", nil)
	if err != nil {
		panic(err)
	}
	return in
}

func TestS3FilesMount_PerAppSubdirIsolation(t *testing.T) {
	m := S3FilesMount{
		FileSystemArn: "arn:aws:s3files:us-east-1:123456789012:file-system/fs-abc",
		RootDirectory: "/apps",
		MountPath:     "/app/bundle/data",
	}
	vol, mp, ok := m.volumeAndMount(7)
	if !ok {
		t.Fatal("configured mount: want ok=true")
	}
	if aws.ToString(vol.Name) != s3filesVolumeName {
		t.Errorf("volume name = %q", aws.ToString(vol.Name))
	}
	vc := vol.S3filesVolumeConfiguration
	if vc == nil {
		t.Fatal("nil S3filesVolumeConfiguration")
	}
	if aws.ToString(vc.FileSystemArn) != m.FileSystemArn {
		t.Errorf("FileSystemArn = %q", aws.ToString(vc.FileSystemArn))
	}
	// Each app is isolated to RootDirectory/<slug>.
	if aws.ToString(vc.RootDirectory) != "/apps/app-7" {
		t.Errorf("RootDirectory = %q, want /apps/app-7", aws.ToString(vc.RootDirectory))
	}
	if vc.AccessPointArn != nil {
		t.Error("AccessPointArn set without an access point configured")
	}
	if aws.ToString(mp.SourceVolume) != s3filesVolumeName {
		t.Errorf("mountpoint sourceVolume = %q", aws.ToString(mp.SourceVolume))
	}
	if aws.ToString(mp.ContainerPath) != "/app/bundle/data" {
		t.Errorf("mountpoint containerPath = %q", aws.ToString(mp.ContainerPath))
	}
}

func TestS3FilesMount_RootDefaultsToSlugUnderRoot(t *testing.T) {
	m := S3FilesMount{FileSystemArn: "arn:...:fs-x", RootDirectory: "/", MountPath: "/d"}
	vol, _, _ := m.volumeAndMount(7)
	if got := aws.ToString(vol.S3filesVolumeConfiguration.RootDirectory); got != "/app-7" {
		t.Errorf("RootDirectory = %q, want /app-7", got)
	}
}

func TestS3FilesMount_AccessPointPinsRoot(t *testing.T) {
	m := S3FilesMount{
		FileSystemArn:  "arn:...:fs-x",
		AccessPointArn: "arn:aws:s3files:us-east-1:123456789012:access-point/ap-1",
		RootDirectory:  "/apps",
		MountPath:      "/d",
	}
	vol, _, ok := m.volumeAndMount(7)
	if !ok {
		t.Fatal("want ok")
	}
	vc := vol.S3filesVolumeConfiguration
	if aws.ToString(vc.AccessPointArn) != m.AccessPointArn {
		t.Errorf("AccessPointArn = %q", aws.ToString(vc.AccessPointArn))
	}
	// With an access point, per-app RootDirectory isolation is not applied; the
	// access point enforces the root.
	if vc.RootDirectory != nil {
		t.Errorf("RootDirectory = %q, want unset with an access point", aws.ToString(vc.RootDirectory))
	}
}

func TestS3FilesMount_NotConfigured(t *testing.T) {
	var m S3FilesMount
	if _, _, ok := m.volumeAndMount(7); ok {
		t.Fatal("unconfigured mount: want ok=false")
	}
	if m.Configured() {
		t.Fatal("Configured() = true for zero value")
	}
}

func TestAddS3FilesMount_AppendsVolumeAndMountPoint(t *testing.T) {
	in := buildTaskDefInputForTest()
	m := S3FilesMount{FileSystemArn: "arn:...:fs-x", RootDirectory: "/", MountPath: "/app/bundle/data"}
	if err := addS3FilesMount(in, "app", m, 7); err != nil {
		t.Fatalf("addS3FilesMount: %v", err)
	}
	if len(in.Volumes) != 1 || aws.ToString(in.Volumes[0].Name) != s3filesVolumeName {
		t.Fatalf("volumes = %+v", in.Volumes)
	}
	var mps int
	for _, c := range in.ContainerDefinitions {
		if aws.ToString(c.Name) == "app" {
			mps = len(c.MountPoints)
			if mps != 1 || aws.ToString(c.MountPoints[0].ContainerPath) != "/app/bundle/data" {
				t.Fatalf("app mountPoints = %+v", c.MountPoints)
			}
		}
	}
	if mps == 0 {
		t.Fatal("named container got no mount point")
	}
}

func TestAddS3FilesMount_NotConfiguredNoOp(t *testing.T) {
	in := buildTaskDefInputForTest()
	if err := addS3FilesMount(in, "app", S3FilesMount{}, 7); err != nil {
		t.Fatalf("addS3FilesMount: %v", err)
	}
	if len(in.Volumes) != 0 {
		t.Fatalf("unconfigured backend added volumes: %+v", in.Volumes)
	}
}

func TestAddS3FilesMount_ContainerNotFound(t *testing.T) {
	in := buildTaskDefInputForTest()
	m := S3FilesMount{FileSystemArn: "arn:...:fs-x", MountPath: "/d"}
	if err := addS3FilesMount(in, "nope", m, 7); err == nil {
		t.Fatal("want error when the named container is absent")
	}
}
