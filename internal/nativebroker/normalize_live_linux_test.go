//go:build linux

package nativebroker

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// Only the disposable fixture supplies these numeric identities. This never
// changes ownership in an operator's live app trees.
func TestNormalizeLive(t *testing.T) {
	if os.Getenv("NATIVE_BROKER_TEST_ROOT") != "1" || os.Geteuid() != 0 {
		t.Skip("disposable root fixture is not configured")
	}
	const control, app, gid = 22100, 22101, 22101
	t.Run("controller hardlink cannot widen secret permissions", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "app")
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
		secret := filepath.Join(base, "secret")
		if err := os.WriteFile(secret, []byte("synthetic"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(secret, control, control); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(secret, filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		if err := normalize(root, control, app, gid); err == nil {
			t.Fatal("private controller hardlink was accepted")
		}
		info, err := os.Stat(secret)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatal("secret permissions changed")
		}
	})
	t.Run("symlink target remains private", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "app")
		os.Mkdir(root, 0700)
		secret := filepath.Join(base, "secret")
		os.WriteFile(secret, []byte("synthetic"), 0600)
		if err := os.Symlink(secret, filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		if err := normalize(root, control, app, gid); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(secret)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("symlink target permissions changed")
		}
	})
	t.Run("app hardlinks retain controller backup access", func(t *testing.T) {
		root := t.TempDir()
		file := filepath.Join(root, "a")
		os.WriteFile(file, []byte("synthetic"), 0600)
		os.Chown(file, app, gid)
		if err := os.Link(file, filepath.Join(root, "b")); err != nil {
			t.Fatal(err)
		}
		if err := normalize(root, control, app, gid); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0660 {
			t.Fatal("app hardlink permissions incorrect")
		}
	})
	t.Run("special files are refused", func(t *testing.T) {
		root := t.TempDir()
		if err := unix.Mkfifo(filepath.Join(root, "fifo"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := normalize(root, control, app, gid); err == nil {
			t.Fatal("FIFO was accepted")
		}
	})
	t.Run("another app's inodes are refused", func(t *testing.T) {
		root := t.TempDir()
		file := filepath.Join(root, "foreign")
		os.WriteFile(file, []byte("synthetic"), 0600)
		os.Chown(file, 22102, 22102)
		if err := normalize(root, control, app, gid); err == nil {
			t.Fatal("foreign inode was accepted")
		}
	})
}
