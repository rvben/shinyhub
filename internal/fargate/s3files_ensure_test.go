package fargate

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3files"
)

func TestS3FilesMarkerKey(t *testing.T) {
	cases := []struct {
		prefix, root string
		appID        int64
		want         string
	}{
		{"", "/", 7, "app-7/.shinyhub-keep"},
		{"", "/apps", 7, "apps/app-7/.shinyhub-keep"},
		{"myfs", "/apps", 7, "myfs/apps/app-7/.shinyhub-keep"},
		{"myfs", "/", 7, "myfs/app-7/.shinyhub-keep"},
		{"", "", 7, "app-7/.shinyhub-keep"},
	}
	for _, c := range cases {
		if got := s3filesMarkerKey(c.prefix, c.root, c.appID); got != c.want {
			t.Errorf("s3filesMarkerKey(%q,%q,%d) = %q, want %q", c.prefix, c.root, c.appID, got, c.want)
		}
	}
}

func TestFileSystemIDFromArn(t *testing.T) {
	got := fileSystemIDFromArn("arn:aws:s3files:us-east-1:123456789012:file-system/fs-abc123")
	if got != "fs-abc123" {
		t.Errorf("fileSystemIDFromArn = %q, want fs-abc123", got)
	}
}

func TestBucketNameFromArn(t *testing.T) {
	if got := bucketNameFromArn("arn:aws:s3:::my-bucket"); got != "my-bucket" {
		t.Errorf("bucketNameFromArn = %q, want my-bucket", got)
	}
	// A bare name (already resolved) passes through.
	if got := bucketNameFromArn("my-bucket"); got != "my-bucket" {
		t.Errorf("bucketNameFromArn(bare) = %q", got)
	}
}

// fakeS3FilesDescriber / fakePutter record calls for ensureAppDataDir tests.
type fakeS3FilesDescriber struct {
	bucketArn string
	prefix    string
	calls     int
}

func (f *fakeS3FilesDescriber) GetFileSystem(_ context.Context, in *s3files.GetFileSystemInput, _ ...func(*s3files.Options)) (*s3files.GetFileSystemOutput, error) {
	f.calls++
	return &s3files.GetFileSystemOutput{Bucket: aws.String(f.bucketArn), Prefix: aws.String(f.prefix)}, nil
}

type fakePutter struct {
	bucket, key string
	calls       int
}

func (f *fakePutter) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.calls++
	f.bucket = aws.ToString(in.Bucket)
	f.key = aws.ToString(in.Key)
	return &s3.PutObjectOutput{}, nil
}

func TestEnsureAppDataDir_PreCreatesMarker(t *testing.T) {
	cfg := testCfg()
	cfg.S3Files = S3FilesMount{
		FileSystemArn: "arn:aws:s3files:us-east-1:123456789012:file-system/fs-abc",
		RootDirectory: "/apps",
		MountPath:     "/app/bundle/data",
	}
	desc := &fakeS3FilesDescriber{bucketArn: "arn:aws:s3:::my-bucket", prefix: ""}
	put := &fakePutter{}
	r := New(&fakeECS{}, cfg, nil, WithS3FilesDescriber(desc), WithObjectPutter(put))

	if err := r.ensureAppDataDir(context.Background(), 7); err != nil {
		t.Fatalf("ensureAppDataDir: %v", err)
	}
	if put.calls != 1 {
		t.Fatalf("PutObject calls = %d, want 1", put.calls)
	}
	if put.bucket != "my-bucket" {
		t.Errorf("bucket = %q, want my-bucket", put.bucket)
	}
	if put.key != "apps/app-7/.shinyhub-keep" {
		t.Errorf("key = %q, want apps/app-7/.shinyhub-keep", put.key)
	}

	// Bucket resolution is cached: a second app does not re-describe.
	if err := r.ensureAppDataDir(context.Background(), 8); err != nil {
		t.Fatal(err)
	}
	if desc.calls != 1 {
		t.Errorf("GetFileSystem calls = %d, want 1 (cached)", desc.calls)
	}
}

func TestEnsureAppDataDir_SkipsWithAccessPoint(t *testing.T) {
	cfg := testCfg()
	cfg.S3Files = S3FilesMount{
		FileSystemArn:  "arn:aws:s3files:us-east-1:123456789012:file-system/fs-abc",
		AccessPointArn: "arn:aws:s3files:us-east-1:123456789012:access-point/ap-1",
		MountPath:      "/d",
	}
	put := &fakePutter{}
	r := New(&fakeECS{}, cfg, nil, WithObjectPutter(put), WithS3FilesDescriber(&fakeS3FilesDescriber{}))
	if err := r.ensureAppDataDir(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if put.calls != 0 {
		t.Fatalf("access point set: want no pre-create, got %d PutObject calls", put.calls)
	}
}

func TestEnsureAppDataDir_NotConfiguredNoOp(t *testing.T) {
	r := New(&fakeECS{}, testCfg(), nil)
	if err := r.ensureAppDataDir(context.Background(), 7); err != nil {
		t.Fatalf("unconfigured: want nil, got %v", err)
	}
}
