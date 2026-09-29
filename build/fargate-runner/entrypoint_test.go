// Package fargaterunner tests the reference runner image's entrypoint script.
package fargaterunner

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCurl writes the bundle named by $FAKE_BUNDLE to the path after -o, the
// way the real fetch writes the downloaded zip.
const fakeCurl = `#!/bin/sh
out=
while [ $# -gt 0 ]; do
    if [ "$1" = "-o" ]; then out="$2"; shift; fi
    shift
done
cp "$FAKE_BUNDLE" "$out"
`

// fakeUV records each invocation's arguments, one line per call.
const fakeUV = `#!/bin/sh
echo "$*" >> "$UV_CALLS"
`

func writeBundle(t *testing.T, files map[string]string) (path, digest string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "bundle.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return path, "sha256:" + hex.EncodeToString(sum[:])
}

// runEntrypoint runs entrypoint.sh on a bundle with fake curl and uv on PATH
// and returns the uv invocations it made.
func runEntrypoint(t *testing.T, files map[string]string, lockMode string) []string {
	t.Helper()
	for _, tool := range []string{"sh", "unzip", "sha256sum", "cut"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	bin := t.TempDir()
	for name, body := range map[string]string{"curl": fakeCurl, "uv": fakeUV} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bundle, digest := writeBundle(t, files)
	work := t.TempDir()
	calls := filepath.Join(work, "uv-calls")

	cmd := exec.Command("sh", "entrypoint.sh", "true")
	cmd.Env = []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_BUNDLE=" + bundle,
		"UV_CALLS=" + calls,
		"SHINYHUB_CONTROL_PLANE_URL=http://control-plane.invalid",
		"SHINYHUB_BUNDLE_TOKEN=token",
		"SHINYHUB_CONTENT_DIGEST=" + digest,
		"SHINYHUB_SLUG=demo",
		"SHINYHUB_RUNNER_BUNDLE_ZIP=" + filepath.Join(work, "bundle.zip"),
		"SHINYHUB_RUNNER_BUNDLE_DIR=" + filepath.Join(work, "bundle"),
	}
	if lockMode != "" {
		cmd.Env = append(cmd.Env, "SHINYHUB_UV_LOCK="+lockMode)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("entrypoint.sh: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(calls)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func TestEntrypointDependencyPreparation(t *testing.T) {
	project := map[string]string{"app.py": "", "pyproject.toml": "[project]\nname = \"app\"\n"}
	locked := map[string]string{"app.py": "", "pyproject.toml": "[project]\nname = \"app\"\n", "uv.lock": "version = 1\n"}
	cases := []struct {
		name     string
		files    map[string]string
		lockMode string
		want     []string
	}{
		{"a shipped lock installs frozen", locked, "", []string{"sync --frozen"}},
		{"a stale lock resolves again", locked, "resolve", []string{"sync"}},
		{"an unknown mode installs the lock frozen", locked, "bogus", []string{"sync --frozen"}},
		{"a project without a lock resolves", project, "", []string{"sync"}},
		{"a project without a lock resolves in resolve mode too", project, "resolve", []string{"sync"}},
		{"requirements-only bundles are left to the launch", map[string]string{"app.py": "", "requirements.txt": "shiny\n"}, "resolve", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runEntrypoint(t, tc.files, tc.lockMode)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("uv calls = %q, want %q", got, tc.want)
			}
		})
	}
}
