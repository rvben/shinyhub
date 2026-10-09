package schedulespec

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestProducerInputsUseAcceptedArchiveAndNormalizeNames(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "versions", "v1")
	if err := os.MkdirAll(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bundles"), 0700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "bundles", "v1.zip")
	write := func(value string) {
		t.Helper()
		f, err := os.Create(archive)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(f)
		w, err := zw.Create("helpers//producer.py")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	identity := func() string {
		t.Helper()
		_, fp, err := ProducerBundleIdentity(`["true"]`, `["helpers/*"]`, bundle)
		if err != nil {
			t.Fatal(err)
		}
		return fp
	}
	write("v1")
	first := identity()
	if err := os.WriteFile(filepath.Join(bundle, "generated"), []byte("output"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := identity(); got != first {
		t.Fatal("generated output changed accepted input identity")
	}
	write("v2")
	if identity() == first {
		t.Fatal("normalized archive input change was ignored")
	}
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ProducerBundleIdentity(`["true"]`, `["helpers/*"]`, bundle); err == nil {
		t.Fatal("missing accepted archive must fail closed")
	}
}

func TestProducerBundleIdentityTracksDeclaredInputs(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("helpers/fetch.py", "v1")
	write("helpers/nested/lib.py", "lib")
	write("app.py", "ui1")
	identity := func(command, inputs string) string {
		t.Helper()
		_, fp, err := ProducerBundleIdentity(command, inputs, root)
		if err != nil {
			t.Fatal(err)
		}
		return fp
	}
	const command = `["python","helpers/fetch.py"]`
	const inputs = `["helpers/**","uv.lock"]`
	before := identity(command, inputs)
	write("app.py", "ui2")
	if got := identity(command, inputs); got != before {
		t.Fatal("UI-only edit invalidated producer")
	}
	if got := identity(command, `["uv.lock","helpers/**","helpers/**"]`); got != before {
		t.Fatal("equivalent glob order or duplicates changed identity")
	}
	write("helpers/nested/lib.py", "changed")
	if got := identity(command, inputs); got == before {
		t.Fatal("nested input change was ignored")
	}
	before = identity(command, inputs)
	write("uv.lock", "lock")
	if got := identity(command, inputs); got == before {
		t.Fatal("newly matching file was ignored")
	}
	before = identity(command, inputs)
	if err := os.Remove(filepath.Join(root, "uv.lock")); err != nil {
		t.Fatal(err)
	}
	if got := identity(command, inputs); got == before {
		t.Fatal("input deletion was ignored")
	}
	if got := identity(`["python","helpers/fetch.py","--new"]`, inputs); got == identity(command, inputs) {
		t.Fatal("command edit was ignored")
	}
	_, unscoped, _ := ProducerBundleIdentity(command, "", "")
	_, legacy, _ := ProducerIdentity(command)
	if unscoped != legacy {
		t.Fatal("unscoped identity changed")
	}
}

func TestProducerInputsRejectEscapesAndSymlinks(t *testing.T) {
	for _, pattern := range []string{"../secret", "helpers/../secret", "/secret", "helpers\\secret", "[", "./helper", "helpers//file"} {
		if _, err := NormalizeInputs([]string{pattern}); err == nil {
			t.Errorf("accepted %q", pattern)
		}
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ProducerBundleIdentity(`["true"]`, `["**"]`, root); err == nil {
		t.Fatal("followed symlink outside bundle")
	}
}
