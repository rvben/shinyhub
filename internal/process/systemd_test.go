package process

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/nativebroker"
)

func TestSystemdRuntimeRejectsMismatchedAndControlStorage(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := nativebroker.App{ID: 1, Slug: "alpha", BundleRoot: filepath.Join(base, "apps/alpha/versions"), DataRoot: filepath.Join(base, "data/alpha"), CacheRoot: filepath.Join(base, "cache/alpha")}
	rt := SystemdRuntime{policy: nativebroker.Policy{ControlUID: os.Getuid(), Apps: []nativebroker.App{app}}}
	if err := rt.ValidateStorage(filepath.Join(base, "apps"), filepath.Join(base, "data"), filepath.Join(base, "cache")); err != nil {
		t.Fatal(err)
	}
	if err := rt.ValidateStorage(filepath.Join(base, "wrong"), filepath.Join(base, "data"), filepath.Join(base, "cache")); err == nil {
		t.Fatal("mismatched server storage was accepted")
	}
	private := filepath.Join(base, "control")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	if err := rt.ValidateControlDatabase(filepath.Join(private, "db")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(private, 0750); err != nil {
		t.Fatal(err)
	}
	if err := rt.ValidateControlDatabase(filepath.Join(private, "db")); err == nil {
		t.Fatal("non-private database parent was accepted")
	}
	if err := os.MkdirAll(app.BundleRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := rt.ValidateControlDatabase(filepath.Join(app.BundleRoot, "db")); err == nil {
		t.Fatal("database in an app root was accepted")
	}
	secret := filepath.Join(app.BundleRoot, "secret")
	if err := os.WriteFile(secret, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "secret-link")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if err := rt.ValidateControlPath(link); err == nil {
		t.Fatal("control secret symlink into an app tree was accepted")
	}
}

var _ Runtime = (*SystemdRuntime)(nil)
var _ Snapshotter = (*SystemdRuntime)(nil)
var _ ResourceUpdater = (*SystemdRuntime)(nil)
var _ LifetimeFileInheritor = (*SystemdRuntime)(nil)
var _ GuardedStartCapable = (*SystemdRuntime)(nil)
