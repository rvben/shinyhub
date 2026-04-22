package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	if err := runDataPush(cfg.Host, cfg.Token, "demo", localFile, "", false); err != nil {
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

	if err := runDataPush(cfg.Host, cfg.Token, "demo", localFile, "subdir/x", true); err != nil {
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

	err = runDataPush(cfg.Host, cfg.Token, "demo", localFile, "", false)
	if err == nil {
		t.Fatal("expected quota error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "quota") {
		t.Errorf("expected error to contain 'quota', got: %v", err)
	}
}

func TestDataLs(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"files":[{"path":"a.txt","size":2,"sha256":"abc","modified_at":1735689600}],"quota_mb":1024,"used_bytes":2}`)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}

	out, err := runDataLs(cfg.Host, cfg.Token, "demo")
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
	found := false
	for _, cmd := range rootCmd.Commands() {
		if cmd.Use == "data" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'data' command to be registered with rootCmd")
	}
}
