package fargate

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3files"
)

// s3filesVolumeName is the task-definition volume name used for the managed
// durable-data mount. It is stable so the container mount point can reference it.
const s3filesVolumeName = "shinyhub-app-data"

// s3filesDirMarker is the zero-byte object written into the linked bucket to make
// a per-app directory exist on the file system before the app's first mount.
const s3filesDirMarker = ".shinyhub-keep"

// S3FilesDescriber resolves the S3 bucket (and prefix) linked to an S3 Files file
// system, so the runtime can pre-create per-app directories in that bucket.
type S3FilesDescriber interface {
	GetFileSystem(context.Context, *s3files.GetFileSystemInput, ...func(*s3files.Options)) (*s3files.GetFileSystemOutput, error)
}

// ObjectPutter writes the per-app directory marker into the linked bucket. S3
// Files mirrors the bucket into the file system, so the marker makes the app's
// mount directory exist (a non-existent rootDirectory fails to mount).
type ObjectPutter interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// ensureAppDataDir pre-creates slug's data directory on the S3 Files file system
// by writing a marker object to the linked bucket, so the app's per-app
// rootDirectory exists before the first task mounts it. It is a no-op when S3
// Files is not configured, or when an access point is used (the access point
// auto-creates its root). Called once per app from resolveTaskDef's register
// path; failures fail the Start closed rather than surfacing as a mount error.
func (r *Runtime) ensureAppDataDir(ctx context.Context, slug string) error {
	m := r.cfg.S3Files
	if !m.Configured() || m.AccessPointArn != "" {
		return nil
	}
	if r.s3put == nil || r.s3filesDesc == nil {
		return fmt.Errorf("fargate: s3files is configured but the S3 clients are not wired (WithS3FilesDescriber/WithObjectPutter)")
	}
	bucket, prefix, err := r.resolveBucket(ctx)
	if err != nil {
		return err
	}
	key := s3filesMarkerKey(prefix, m.RootDirectory, slug)
	if _, err := r.s3put.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   strings.NewReader(""),
	}); err != nil {
		return fmt.Errorf("fargate: pre-create app-data dir for %q (s3://%s/%s): %w", slug, bucket, key, err)
	}
	return nil
}

// resolveBucket returns the bucket name and prefix linked to the configured S3
// Files file system, describing it once and caching the result.
func (r *Runtime) resolveBucket(ctx context.Context) (bucket, prefix string, err error) {
	r.bucketMu.Lock()
	defer r.bucketMu.Unlock()
	if r.bucketDone {
		return r.bucketName, r.bucketPrefix, nil
	}
	out, err := r.s3filesDesc.GetFileSystem(ctx, &s3files.GetFileSystemInput{
		FileSystemId: aws.String(fileSystemIDFromArn(r.cfg.S3Files.FileSystemArn)),
	})
	if err != nil {
		return "", "", fmt.Errorf("fargate: resolve bucket for s3files file system: %w", err)
	}
	b := bucketNameFromArn(aws.ToString(out.Bucket))
	if b == "" {
		return "", "", fmt.Errorf("fargate: s3files GetFileSystem returned no bucket for %q", r.cfg.S3Files.FileSystemArn)
	}
	r.bucketName, r.bucketPrefix, r.bucketDone = b, strings.Trim(aws.ToString(out.Prefix), "/"), true
	return r.bucketName, r.bucketPrefix, nil
}

// s3filesMarkerKey builds the bucket object key whose creation makes the per-app
// directory exist on the file system: <prefix>/<root>/<slug>/.shinyhub-keep,
// with empty segments dropped and no leading slash.
func s3filesMarkerKey(prefix, root, slug string) string {
	parts := make([]string, 0, 4)
	for _, p := range []string{prefix, root, slug} {
		if t := strings.Trim(p, "/"); t != "" {
			parts = append(parts, t)
		}
	}
	parts = append(parts, s3filesDirMarker)
	return strings.Join(parts, "/")
}

// fileSystemIDFromArn extracts the fs-... id from an S3 Files file-system ARN.
func fileSystemIDFromArn(arn string) string {
	if i := strings.LastIndex(arn, "file-system/"); i >= 0 {
		return arn[i+len("file-system/"):]
	}
	return arn
}

// bucketNameFromArn strips an "arn:aws:s3:::" prefix to the bare bucket name,
// passing an already-bare name through unchanged.
func bucketNameFromArn(s string) string {
	if i := strings.LastIndex(s, ":::"); i >= 0 {
		return s[i+len(":::"):]
	}
	return s
}

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
// A non-existent per-app RootDirectory fails to mount ("No such file or
// directory", verified live). ensureAppDataDir pre-creates the subdirectory (via
// a bucket marker) before the first mount, unless an access point is used (which
// auto-creates its own root).
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
