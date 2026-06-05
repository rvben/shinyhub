package deploy

import (
	"path/filepath"
	"testing"
)

func TestBundleDir(t *testing.T) {
	got := BundleDir("/abs/apps", "myapp", "v3")
	want := filepath.Join("/abs/apps", "myapp", "versions", "v3")
	if got != want {
		t.Fatalf("BundleDir = %q, want %q", got, want)
	}
}
