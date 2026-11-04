package process

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Exercise actual native jobs, including the last revoked mount. The old
// early return on an empty desired set left source bytes readable forever.
func TestNativeSharedMountRevocationRemovesOldAccess(t *testing.T) {
	rt := NewNativeRuntime()
	bundle, source, keep := t.TempDir(), t.TempDir(), t.TempDir()
	for _, root := range []string{source, keep} {
		if err := os.WriteFile(filepath.Join(root, "marker"), []byte("source bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := StartParams{Dir: bundle, Slug: "consumer", Command: []string{"sh", "-c", "cat data/shared/source/marker"}, SharedMounts: []SharedMount{{SourceSlug: "source", HostPath: source}, {SourceSlug: "keep", HostPath: keep}}}
	var out bytes.Buffer
	info, err := rt.RunOnce(context.Background(), p, &out)
	if err != nil || info.Code != 0 || out.String() != "source bytes" {
		t.Fatalf("initial read: %+v, %v, %q", info, err, out.String())
	}
	p.SharedMounts = p.SharedMounts[1:]
	p.Command = []string{"sh", "-c", "test ! -e data/shared/source/marker && cat data/shared/keep/marker"}
	out.Reset()
	info, err = rt.RunOnce(context.Background(), p, &out)
	if err != nil || info.Code != 0 || out.String() != "source bytes" {
		t.Fatalf("partial revoke: %+v, %v, %q", info, err, out.String())
	}
	p.SharedMounts = nil
	p.Command = []string{"sh", "-c", "test ! -e data/shared/keep/marker"}
	out.Reset()
	info, err = rt.RunOnce(context.Background(), p, &out)
	if err != nil || info.Code != 0 {
		t.Fatalf("last revoke: %+v, %v, %q", info, err, out.String())
	}
	for _, root := range []string{source, keep} {
		if content, err := os.ReadFile(filepath.Join(root, "marker")); err != nil || string(content) != "source bytes" {
			t.Fatalf("source was changed: %q %v", content, err)
		}
	}
}

func TestSharedMountCleanupPreservesRealDataAndRejectsSharedSymlink(t *testing.T) {
	dataDir := t.TempDir()
	base := filepath.Join(dataDir, "shared")
	if err := os.MkdirAll(filepath.Join(base, "user-dir"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "user-file"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(base, "stale")); err != nil {
		t.Fatal(err)
	}
	if err := applySharedMounts(StartParams{AppDataPath: dataDir}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"user-dir", "user-file"} {
		if _, err := os.Lstat(filepath.Join(base, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Lstat(filepath.Join(base, "stale")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale link: %v", err)
	}
	external := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(external, "secret")); err != nil {
		t.Fatal(err)
	}
	aliasDir := t.TempDir()
	if err := os.Symlink(external, filepath.Join(aliasDir, "shared")); err != nil {
		t.Fatal(err)
	}
	if err := applySharedMounts(StartParams{AppDataPath: aliasDir}); err == nil {
		t.Fatal("followed symlinked shared directory")
	}
	if err := RemoveSharedMountLink(aliasDir, "secret"); err == nil {
		t.Fatal("cleanup followed symlinked shared directory")
	}
	if _, err := os.Lstat(filepath.Join(external, "secret")); err != nil {
		t.Fatal("external link changed:", err)
	}
}

func TestRemoveSharedMountLinkIsIdempotentAndPreservesSourceAndDockerTarget(t *testing.T) {
	root, source := t.TempDir(), t.TempDir()
	base := filepath.Join(root, "shared")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, filepath.Join(base, "source")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := RemoveSharedMountLink(root, "source"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "docker-target"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSharedMountLink(root, "docker-target"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "docker-target")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../source", "/source", ".", "a/b"} {
		if err := RemoveSharedMountLink(root, name); err == nil {
			t.Fatalf("accepted unsafe source %q", name)
		}
	}
}

// A fresh container must use only the remaining grants. Empty host-side mount
// targets may remain, but they must no longer expose the producer's files.
func TestDockerRuntimeRunOnce_SharedMountRevocation(t *testing.T) {
	rt := dockerRuntimeWithImage(t, "alpine:3")
	source, keep := t.TempDir(), t.TempDir()
	for _, root := range []string{source, keep} {
		if err := os.WriteFile(filepath.Join(root, "marker"), []byte("source bytes"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	p := StartParams{Dir: t.TempDir(), AppDataPath: t.TempDir(), Slug: "consumer", Command: []string{"sh", "-c", "cat /app/data/shared/source/marker"}, SharedMounts: []SharedMount{{SourceSlug: "source", HostPath: source}, {SourceSlug: "keep", HostPath: keep}}}
	for _, stage := range []struct {
		name, command, output string
		mounts                []SharedMount
	}{
		{"initial", "cat /app/data/shared/source/marker", "source bytes", p.SharedMounts},
		{"partial revoke", "test ! -e /app/data/shared/source/marker && cat /app/data/shared/keep/marker", "source bytes", p.SharedMounts[1:]},
		{"last revoke", "test ! -e /app/data/shared/source/marker && test ! -e /app/data/shared/keep/marker", "", nil},
	} {
		p.Command = []string{"sh", "-c", stage.command}
		p.SharedMounts = stage.mounts
		var out bytes.Buffer
		info, err := rt.RunOnce(context.Background(), p, &out)
		if err != nil || info.Code != 0 || out.String() != stage.output {
			t.Fatalf("%s: %+v, %v, %q", stage.name, info, err, out.String())
		}
	}
	for _, root := range []string{source, keep} {
		if content, err := os.ReadFile(filepath.Join(root, "marker")); err != nil || string(content) != "source bytes" {
			t.Fatalf("source was changed: %q %v", content, err)
		}
	}
}
