package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		filepath.Join(dir, "seed.csv"), dataStallTimeout, nil)
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

// TestDataPull_GlobalClientTimeoutDoesNotBoundTheTransfer reproduces the
// original defect at test scale: runDataPull used to stream through the
// package-global httpClient, whose fixed Client.Timeout bounds the whole
// response body read regardless of progress, so a download taking longer
// than that is killed even while still receiving bytes. Shrinking the global
// client's timeout here makes the bug reproducible in milliseconds: a
// transfer that keeps making progress every 40ms, for longer than the
// (shrunk) global timeout but well within the per-call timeout, must still
// succeed - proof that pull no longer depends on the global client at all.
func TestDataPull_GlobalClientTimeoutDoesNotBoundTheTransfer(t *testing.T) {
	prev := httpClient
	httpClient = &apiClient{&http.Client{Timeout: 100 * time.Millisecond}}
	t.Cleanup(func() { httpClient = prev })

	content := []byte("stream-me")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, _ := w.(http.Flusher)
		for i := range content {
			_, _ = w.Write(content[i : i+1])
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(40 * time.Millisecond)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")

	size, sum, err := runDataPull(srv.URL, "tok", "demo", "f.bin", dest, 5*time.Second, io.Discard)
	if err != nil {
		t.Fatalf("a transfer making steady progress must not be bounded by the global httpClient's timeout, got: %v", err)
	}
	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}
	want := sha256.Sum256(content)
	if sum != hex.EncodeToString(want[:]) {
		t.Errorf("sha256 = %s, want %s", sum, hex.EncodeToString(want[:]))
	}
}

// TestDataPull_SlowButSteadyTransferSucceeds proves --timeout is a stall
// timeout, not a total-request deadline: a transfer that keeps delivering
// bytes, each gap shorter than the timeout, must succeed even though the
// whole transfer runs longer than the timeout itself.
func TestDataPull_SlowButSteadyTransferSucceeds(t *testing.T) {
	// A 10x margin between gap and timeout keeps this deterministic on a loaded
	// host, where a scheduled 70ms sleep has been observed to take 150ms; 16
	// gaps still add up to more than the timeout.
	content := []byte("steady-progress!")
	const gap = 100 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, _ := w.(http.Flusher)
		for i := range content {
			_, _ = w.Write(content[i : i+1])
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(gap)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")

	const timeout = time.Second
	start := time.Now()
	size, sum, err := runDataPull(srv.URL, "tok", "demo", "f.bin", dest, timeout, io.Discard)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("a transfer making progress every %s must not be aborted by a %s stall timeout, even though the whole transfer (%s) ran longer than it: %v",
			gap, timeout, elapsed, err)
	}
	if elapsed < timeout {
		t.Fatalf("test setup is not exercising the total-transfer-exceeds-timeout case: elapsed %s, want > %s", elapsed, timeout)
	}
	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}
	want := sha256.Sum256(content)
	if sum != hex.EncodeToString(want[:]) {
		t.Errorf("sha256 = %s, want %s", sum, hex.EncodeToString(want[:]))
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("file contents = %q, want %q", got, content)
	}
}

// TestDataPull_StallAbortsWithTimeoutKindAndNoPartialFile mirrors
// TestDataPush_StallTimeoutAbortsHungServer for the download direction: a
// server that starts answering and then goes silent must be aborted within
// the configured stall timeout, classified as KindTimeout (so an operator
// knows a retry is worth something), and must not leave a partial file at the
// destination - the same invariant TestDataPull_LeavesNoFileWhenTheTransferFails
// pins for a connection that closes outright.
func TestDataPull_StallAbortsWithTimeoutKindAndNoPartialFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")

	const stallTimeout = 150 * time.Millisecond
	start := time.Now()
	_, _, err := runDataPull(srv.URL, "tok", "demo", "f.bin", dest, stallTimeout, io.Discard)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a transfer that stalls mid-body, got nil")
	}
	if elapsed > 5*time.Second {
		t.Errorf("pull took %s to abort a %s stall, want it bounded by the timeout", elapsed, stallTimeout)
	}
	if kind, _ := classify(err); kind != KindTimeout {
		t.Errorf("error kind = %v, want %v: a stalled transfer is retryable, unlike an internal failure", kind, KindTimeout)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("a stalled pull left a partial file at the destination")
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
