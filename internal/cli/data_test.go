package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestDataPush_DefaultDestIsBasename(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{"path":"seed.txt","size":2,"sha256":"abc","restarted":false}`)

	tmpDir := t.TempDir()
	localFile := filepath.Join(tmpDir, "seed.txt")
	if err := os.WriteFile(localFile, []byte("hi"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}

	if err := runDataPush(cfg.Host, cfg.Token, "demo", localFile, "", false, dataPushStallTimeout); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != "PUT" {
		t.Errorf("expected PUT, got %s", req.Method)
	}
	if req.Path != "/api/apps/demo/data/seed.txt" {
		t.Errorf("unexpected path: %s", req.Path)
	}
	if req.Query != "restart=false" {
		t.Errorf("unexpected query: %s", req.Query)
	}
	if string(req.Body) != "hi" {
		t.Errorf("unexpected body: %q", req.Body)
	}
}

func TestDataPush_RestartFlag(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{"path":"subdir/x","size":3,"sha256":"def","restarted":true}`)

	tmpDir := t.TempDir()
	localFile := filepath.Join(tmpDir, "myfile.bin")
	if err := os.WriteFile(localFile, []byte("foo"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}

	if err := runDataPush(cfg.Host, cfg.Token, "demo", localFile, "subdir/x", true, dataPushStallTimeout); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != "PUT" {
		t.Errorf("expected PUT, got %s", req.Method)
	}
	if req.Path != "/api/apps/demo/data/subdir/x" {
		t.Errorf("unexpected path: %s", req.Path)
	}
	if req.Query != "restart=true" {
		t.Errorf("unexpected query: %s", req.Query)
	}
}

func TestDataPush_QuotaError(t *testing.T) {
	_, _, setResp := setupCLITest(t)

	quotaErr := map[string]any{
		"QuotaBytes":     int64(1024 * 1024),
		"UsedBytes":      int64(900 * 1024),
		"WouldBeBytes":   int64(1100 * 1024),
		"RemainingBytes": int64(124 * 1024),
	}
	body, _ := json.Marshal(quotaErr)
	setResp(413, string(body))

	tmpDir := t.TempDir()
	localFile := filepath.Join(tmpDir, "big.bin")
	if err := os.WriteFile(localFile, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}

	err = runDataPush(cfg.Host, cfg.Token, "demo", localFile, "", false, dataPushStallTimeout)
	if err == nil {
		t.Fatal("expected quota error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "quota") {
		t.Errorf("expected error to contain 'quota', got: %v", err)
	}
}

// TestDataPush_StallTimeoutAbortsHungServer proves that a server that accepts
// the upload body but never sends a response does not hang the push forever:
// it is aborted once no progress has been made for the configured timeout,
// rather than only a real total-request deadline that would also cut off a
// large upload that is still making progress.
func TestDataPush_StallTimeoutAbortsHungServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	localFile := filepath.Join(tmpDir, "f.bin")
	if err := os.WriteFile(localFile, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}

	const stallTimeout = 150 * time.Millisecond
	start := time.Now()
	err := runDataPush(srv.URL, "tok", "demo", localFile, "", false, stallTimeout)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a server that never responds, got nil")
	}
	if elapsed > 5*time.Second {
		t.Errorf("upload took %s to abort a %s stall, want it bounded by the timeout", elapsed, stallTimeout)
	}
}

func TestDataLs(t *testing.T) {
	resetFormatState(t)
	_, _, setResp := setupCLITest(t)
	// The server returns the standard {items,...} envelope with quota siblings.
	setResp(200, `{"items":[{"path":"a.txt","size":2,"sha256":"abc","modified_at":1735689600}],"quota_mb":1024,"used_bytes":2,"total":1,"limit":0,"offset":0}`)

	out, err := execCLI(t, "data", "ls", "demo", "--output", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "a.txt") {
		t.Errorf("expected output to contain 'a.txt', got: %s", out)
	}
	if !strings.Contains(out, "Used:") {
		t.Errorf("expected output to contain 'Used:', got: %s", out)
	}
}

// The quota counts deployment bundles as well as the data dir, so "Used" can
// read in gigabytes while the listed files sum to a few megabytes. Printing the
// charged total alone leaves that gap unexplained and invites an operator to
// delete data to reclaim space old deployments are holding.
func TestDataLsSeparatesDataBytesFromQuotaCharge(t *testing.T) {
	resetFormatState(t)
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"items":[{"path":"a.txt","size":2048,"sha256":"abc","modified_at":1735689600}],`+
		`"quota_mb":1024,"used_bytes":1073741824,"data_bytes":2048,"total":1,"limit":0,"offset":0}`)

	out, err := execCLI(t, "data", "ls", "demo", "--output", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Used: 1.0GiB") {
		t.Errorf("output must still report the quota charge, got: %s", out)
	}
	if !strings.Contains(out, "this data dir: 2.0KiB") {
		t.Errorf("output must break out the data dir's own size, got: %s", out)
	}
}

// A server that predates data_bytes must not have a zero invented for it: an
// unavailable breakdown reads as "this data dir holds nothing", which is a
// different and wrong answer.
func TestDataLsOmitsBreakdownWhenServerDoesNotReportIt(t *testing.T) {
	resetFormatState(t)
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"items":[{"path":"a.txt","size":2048,"sha256":"abc","modified_at":1735689600}],`+
		`"quota_mb":1024,"used_bytes":1073741824,"total":1,"limit":0,"offset":0}`)

	out, err := execCLI(t, "data", "ls", "demo", "--output", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "this data dir") {
		t.Errorf("no data_bytes key means no breakdown line, got: %s", out)
	}
}

func TestDataRm(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(204, "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}

	if err := runDataRm(cfg.Host, cfg.Token, "demo", "a/b/c.txt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != "DELETE" {
		t.Errorf("expected DELETE, got %s", req.Method)
	}
	if req.Path != "/api/apps/demo/data/a/b/c.txt" {
		t.Errorf("unexpected path: %s", req.Path)
	}
}

func TestDataRm_NotFound(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(404, `{"error":"not found"}`)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}

	err = runDataRm(cfg.Host, cfg.Token, "demo", "missing.txt")
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}

// TestDataCmd_RegisteredWithRoot verifies that the data command tree
// is registered with the root cobra command.
func TestDataCmd_RegisteredWithRoot(t *testing.T) {
	root := &cobra.Command{Use: "root"}
	AddCommandsTo(root)
	found := false
	for _, cmd := range root.Commands() {
		if cmd.Use == "data" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'data' command to be registered with root")
	}
}
