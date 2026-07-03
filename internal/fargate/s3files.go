package fargate

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// s3filesVolumeName is the task-definition volume name used for the managed
// durable-data mount. It is stable so the container mount point can reference it.
const s3filesVolumeName = "shinyhub-app-data"

// S3FilesMount is the Fargate runtime's view of the managed Amazon S3 Files
// durable-data backend. It is derived from config.FargateS3FilesConfig by
// buildFargateRuntime. When configured, the runtime injects a per-app volume and
// container mount point into each app's task-definition revision so that the
// app's {data_dir} ("<cwd>/data") resolves onto a durable, replica-shared mount.
type S3FilesMount struct {
	FileSystemArn         string
	RootDirectory         string
	AccessPointArn        string
	TransitEncryptionPort int32
	MountPath             string
}

// Configured reports whether the S3 Files backend is enabled.
func (m S3FilesMount) Configured() bool { return m.FileSystemArn != "" }

// volumeAndMount builds the ECS volume and container mount point for slug's
// durable data, returning ok=false when the backend is not configured. Without
// an access point, each app is isolated to a per-app subdirectory
// (RootDirectory/<slug>) so apps never see each other's data. With an access
// point, the access point enforces the root and RootDirectory is left unset (the
// SDK requires it to be omitted or "/" in that case).
//
// CAVEAT (unverified): a non-existent per-app RootDirectory is NOT known to be
// auto-created on mount (the EFS analog fails "No such file or directory"). Until
// this is verified live, the per-app subdirectory must already exist on the file
// system, or an access point (which auto-creates its root) must be used. See
// .claude/investigations/s3files-per-app-subdir.md.
func (m S3FilesMount) volumeAndMount(slug string) (ecstypes.Volume, ecstypes.MountPoint, bool) {
	if !m.Configured() {
		return ecstypes.Volume{}, ecstypes.MountPoint{}, false
	}
	vc := &ecstypes.S3FilesVolumeConfiguration{
		FileSystemArn: aws.String(m.FileSystemArn),
	}
	if m.AccessPointArn != "" {
		vc.AccessPointArn = aws.String(m.AccessPointArn)
	} else {
		vc.RootDirectory = aws.String(perAppRoot(m.RootDirectory, slug))
	}
	if m.TransitEncryptionPort > 0 {
		vc.TransitEncryptionPort = aws.Int32(m.TransitEncryptionPort)
	}
	vol := ecstypes.Volume{
		Name:                       aws.String(s3filesVolumeName),
		S3filesVolumeConfiguration: vc,
	}
	mp := ecstypes.MountPoint{
		SourceVolume:  aws.String(s3filesVolumeName),
		ContainerPath: aws.String(m.MountPath),
	}
	return vol, mp, true
}

// addS3FilesMount injects the per-app S3 Files volume into in and mounts it on
// the container named containerName. It is a no-op when the backend is not
// configured. It returns an error when the named container is absent, so a
// misconfigured container_name fails the registration rather than silently
// producing a task with no durable mount. Applied by resolveTaskDef after
// buildTaskDefInput clones the base task definition.
func addS3FilesMount(in *ecs.RegisterTaskDefinitionInput, containerName string, m S3FilesMount, slug string) error {
	vol, mp, ok := m.volumeAndMount(slug)
	if !ok {
		return nil
	}
	for i := range in.ContainerDefinitions {
		if aws.ToString(in.ContainerDefinitions[i].Name) == containerName {
			in.ContainerDefinitions[i].MountPoints = append(in.ContainerDefinitions[i].MountPoints, mp)
			in.Volumes = append(in.Volumes, vol)
			return nil
		}
	}
	return fmt.Errorf("fargate: base task definition has no container named %q for the S3 Files mount", containerName)
}

// familyPrefix is the per-app task-definition family prefix. It reuses the
// secrets name prefix when set (so a secrets+s3files app keeps one family), and
// otherwise falls back to a constant. Operators running multiple installs on one
// ECS cluster should set runtime.fargate.secrets.name_prefix to disambiguate.
func (r *Runtime) familyPrefix() string {
	if r.cfg.SecretNamePrefix != "" {
		return r.cfg.SecretNamePrefix
	}
	return "shinyhub"
}

// s3filesSyncKey returns a string that changes whenever the effective per-app
// S3 Files mount changes, so the task-def registration cache re-registers on a
// config change. Empty when the backend is not configured.
func (r *Runtime) s3filesSyncKey(slug string) string {
	m := r.cfg.S3Files
	if !m.Configured() {
		return ""
	}
	root := m.RootDirectory
	if m.AccessPointArn == "" {
		root = perAppRoot(m.RootDirectory, slug)
	}
	return strings.Join([]string{m.FileSystemArn, m.AccessPointArn, root, m.MountPath}, "|")
}

// perAppRoot joins the base root directory and slug into an absolute per-app
// directory: ("/", "demo") -> "/demo"; ("/apps", "demo") -> "/apps/demo".
func perAppRoot(base, slug string) string {
	b := strings.Trim(base, "/")
	if b == "" {
		return "/" + slug
	}
	return "/" + b + "/" + slug
}
