package process_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rvben/shinyhub/internal/process"
)

// probeWheel builds a minimal pure-Python wheel for a distribution that
// exists on no public index, so it can only resolve from the test's index.
func probeWheel(t *testing.T, dist, version string) (name string, data []byte) {
	t.Helper()
	mod := strings.ReplaceAll(dist, "-", "_")
	info := fmt.Sprintf("%s-%s.dist-info", mod, version)
	files := []struct{ path, body string }{
		{mod + ".py", "VALUE = 42\n"},
		{info + "/METADATA", fmt.Sprintf("Metadata-Version: 2.1\nName: %s\nVersion: %s\n", dist, version)},
		{info + "/WHEEL", "Wheel-Version: 1.0\nGenerator: shinyhub-test\nRoot-Is-Purelib: true\nTag: py3-none-any\n"},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var record strings.Builder
	for _, f := range files {
		w, err := zw.Create(f.path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(f.body))
		fmt.Fprintf(&record, "%s,sha256=%s,%d\n", f.path, base64.RawURLEncoding.EncodeToString(sum[:]), len(f.body))
	}
	fmt.Fprintf(&record, "%s/RECORD,,\n", info)
	w, err := zw.Create(info + "/RECORD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(record.String())); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s-%s-py3-none-any.whl", mod, version), buf.Bytes()
}

// End to end against the real uv: a bundle whose requirements.txt names an
// authenticated private index, with the credential in the app's env, converts
// into a project and syncs from that index. Before the fix uv ignored the
// file's --index-url and asked PyPI, which has no such distribution.
func TestRequirementsIndex_RealUVResolvesFromBundleIndex(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed")
	}
	const dist, user, token = "shinyhub-reqidx-probe", "deploy", "s3cret-token"
	wheelName, wheel := probeWheel(t, dist, "1.0")

	var authed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != user || p != token {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authed.Add(1)
		switch strings.TrimSuffix(r.URL.Path, "/") {
		case "/simple/" + dist:
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<html><body><a href="/files/%s">%s</a></body></html>`, wheelName, wheelName)
		case "/files/" + wheelName:
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(wheel)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	for _, k := range []string{"UV_DEFAULT_INDEX", "UV_INDEX", "UV_INDEX_URL", "UV_EXTRA_INDEX_URL", "UV_FIND_LINKS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("UV_CACHE_DIR", t.TempDir())

	dir := t.TempDir()
	host := strings.TrimPrefix(srv.URL, "http://")
	writeBundleFile(t, dir, "requirements.txt",
		"--index-url http://"+user+":${PRIVATE_INDEX_TOKEN}@"+host+"/simple\n"+dist+"\n")
	appEnv := []string{"PRIVATE_INDEX_TOKEN=" + token}

	ctx := context.Background()
	if err := process.EnsureProject(ctx, dir, appEnv); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	lock, err := os.ReadFile(filepath.Join(dir, "uv.lock"))
	if err != nil {
		t.Fatalf("read uv.lock: %v", err)
	}
	if !strings.Contains(string(lock), `name = "`+dist+`"`) || !strings.Contains(string(lock), host) {
		t.Fatalf("uv.lock does not pin %s from the bundle index:\n%s", dist, lock)
	}
	if strings.Contains(string(lock), token) {
		t.Errorf("uv.lock records the credential")
	}
	if authed.Load() == 0 {
		t.Fatal("the private index never received an authenticated request")
	}

	// The deploy's sync composes its step exactly this way.
	build, err := process.ReadRequirementsBuild(dir, appEnv)
	if err != nil {
		t.Fatalf("ReadRequirementsBuild: %v", err)
	}
	cmd := exec.CommandContext(ctx, "uv", append([]string{"sync"}, build.Flags...)...)
	cmd.Dir = dir
	cmd.Env = process.WithBuildInterpreterPolicy(append(process.SanitizedEnv(), build.Env...))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("uv sync: %v\n%s", err, out)
	}
	py := filepath.Join(dir, ".venv", "bin", "python")
	if out, err := exec.Command(py, "-c", "import shinyhub_reqidx_probe as m; print(m.VALUE)").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "42" {
		t.Fatalf("installed probe not importable: %v\n%s", err, out)
	}
}

// uv masks URL userinfo in its errors but prints a query string as is, so a
// token a ${NAME} reference put there must be masked before the failed
// conversion's output reaches the deploy error.
func TestEnsureProject_RealUVFailureMasksExpandedSecrets(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed")
	}
	const token = "zq-add-5c7e2"
	srv := httptest.NewServer(http.NotFoundHandler())
	host := strings.TrimPrefix(srv.URL, "http://")
	srv.Close() // nothing listens there any more, so every fetch fails

	for _, k := range []string{"UV_DEFAULT_INDEX", "UV_INDEX", "UV_INDEX_URL", "UV_EXTRA_INDEX_URL", "UV_FIND_LINKS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("UV_CACHE_DIR", t.TempDir())

	dir := t.TempDir()
	writeBundleFile(t, dir, "requirements.txt",
		"--index-url http://"+host+"/simple?token=${PRIVATE_INDEX_TOKEN}\nshinyhub-reqidx-absent\n")
	err := process.EnsureProject(context.Background(), dir, []string{"PRIVATE_INDEX_TOKEN=" + token})
	if err == nil {
		t.Fatal("EnsureProject succeeded against an index nothing serves")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error leaks the token: %v", err)
	}
	if !strings.Contains(err.Error(), "token=***") {
		t.Errorf("error lost uv's masked output: %v", err)
	}
}
