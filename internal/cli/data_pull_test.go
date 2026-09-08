package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pullServer stands up a CLI test harness that serves fixed bytes for any GET.
func pullServer(t *testing.T, content []byte) *[]capturedReq {
	t.Helper()
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(content)
	})
	return reqs
}

// chdirTemp moves into a fresh temp dir for the duration of the test, so the
// default "write to ./<basename>" behaviour can be exercised without leaving
// files in the package directory.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	return dir
}

// TestDataPull_WritesFileAndReportsDigest covers the command's reason for
// existing. The digest is what makes a pull a verification rather than just a
// copy, so this asserts it is the digest of the actual bytes and not, say, of
// an empty buffer or a name.
func TestDataPull_WritesFileAndReportsDigest(t *testing.T) {
	content := []byte("id,value\n1,42\n")
	reqs := pullServer(t, content)
	dir := chdirTemp(t)

	size, sum, err := runDataPull(mustHost(t), mustToken(t), "demo", "datasets/seed.csv",
		filepath.Join(dir, "seed.csv"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}
	want := sha256.Sum256(content)
	if sum != hex.EncodeToString(want[:]) {
		t.Errorf("sha256 = %s, want %s: the digest must cover the bytes that arrived", sum, hex.EncodeToString(want[:]))
	}
	got, err := os.ReadFile(filepath.Join(dir, "seed.csv"))
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("file contents = %q, want %q", got, content)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", req.Method)
	}
	// The nested path must survive as a path, not be collapsed or double-encoded.
	if req.Path != "/api/apps/demo/data/datasets/seed.csv" {
		t.Errorf("path = %s, want /api/apps/demo/data/datasets/seed.csv", req.Path)
	}
}

// TestDataPull_StdoutDestination pins the piping form. Writing the summary to
// stdout alongside the payload would corrupt exactly the use this flag is for:
// piping the bytes into a checksum.
func TestDataPull_StdoutDestination(t *testing.T) {
	content := []byte("raw-bytes")
	pullServer(t, content)
	chdirTemp(t)

	var stdout, stderr bytes.Buffer
	cmd := newDataCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"pull", "demo", "seed.bin", "--dest", "-"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if stdout.String() != string(content) {
		t.Errorf("stdout = %q, want exactly the file bytes %q: a summary line on stdout would corrupt a checksum pipeline",
			stdout.String(), content)
	}
	if !strings.Contains(stderr.String(), "sha256") {
		t.Errorf("stderr = %q, want the summary with its digest; sending it nowhere leaves the operator without the verification", stderr.String())
	}
}

// TestDataPull_RefusesToClobberWithoutForce guards a local destructive default.
// Pulling to verify a restore, into a directory that already holds the good
// copy, must not overwrite it by surprise.
func TestDataPull_RefusesToClobberWithoutForce(t *testing.T) {
	pullServer(t, []byte("from-server"))
	dir := chdirTemp(t)

	existing := filepath.Join(dir, "seed.csv")
	if err := os.WriteFile(existing, []byte("local-original"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newDataCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"pull", "demo", "seed.csv"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error when the destination already exists")
	}
	if kind, _ := classify(err); kind != KindValidation {
		t.Errorf("error kind = %v, want %v", kind, KindValidation)
	}
	got, _ := os.ReadFile(existing)
	if string(got) != "local-original" {
		t.Errorf("existing file = %q, want it untouched", got)
	}

	// The other bound: --force is what makes it proceed. Without this the check
	// above is satisfied by a command that can never write anything at all.
	cmd = newDataCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"pull", "demo", "seed.csv", "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--force should overwrite, got: %v", err)
	}
	got, _ = os.ReadFile(existing)
	if string(got) != "from-server" {
		t.Errorf("after --force the file = %q, want the downloaded bytes", got)
	}
}

// TestDataPull_LeavesNoFileWhenTheTransferFails is why the download goes
// through a temp file. A failed pull that leaves a file behind is worse than
// one that writes nothing: the operator checking a restore sees a file with the
// right name and concludes it came back.
func TestDataPull_LeavesNoFileWhenTheTransferFails(t *testing.T) {
	// Promise more than is delivered, then hang up: io.Copy fails partway
	// through with bytes already written. A 404 would not exercise this at all,
	// because nothing is ever created for a response that never starts.
	_, _ = setupCLITestHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("truncated"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Returning without writing the remaining bytes closes the response
		// early, which the client sees as an unexpected EOF mid-body.
	})
	dir := chdirTemp(t)

	cmd := newDataCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"pull", "demo", "big.csv"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error when the transfer is cut short mid-body")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("a failed pull left %q behind; the destination must not exist", e.Name())
	}
}

// mustHost and mustToken read the harness config the way the commands do, so a
// direct runDataPull call in a test talks to the same test server.
func mustHost(t *testing.T) string {
	t.Helper()
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	return cfg.Host
}

func mustToken(t *testing.T) string {
	t.Helper()
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	return cfg.Token
}
