package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDataPushSummary(t *testing.T) {
	s := dataPushSummary("/tmp/weekly.csv", "sales.csv", 2048, false)
	for _, want := range []string{"/tmp/weekly.csv", "sales.csv", "->", "2.0KiB", "Uploaded"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary %q missing %q", s, want)
		}
	}
	d := dataPushSummary("/tmp/weekly.csv", "sales.csv", 2048, true)
	if !strings.Contains(d, "sales.csv") || !strings.Contains(strings.ToLower(d), "dry-run") {
		t.Errorf("dry-run summary should mention dest and dry-run, got %q", d)
	}
}

func TestDataPush_DryRunMakesNoRequest(t *testing.T) {
	_, reqs, _ := setupCLITest(t)
	tmpDir := t.TempDir()
	localFile := filepath.Join(tmpDir, "weekly.csv")
	if err := os.WriteFile(localFile, []byte("a,b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newDataCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{"push", "demo", localFile, "--dest", "sales.csv", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*reqs) != 0 {
		t.Fatalf("dry-run must not upload, made %d requests", len(*reqs))
	}
	if !strings.Contains(out.String(), "sales.csv") {
		t.Errorf("dry-run output should report the resolved destination, got %q", out.String())
	}
}

func TestDataPush_JSONCarriesDestAndBytes(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"path":"sales.csv","size":4,"sha256":"x","restarted":false}`)
	tmpDir := t.TempDir()
	localFile := filepath.Join(tmpDir, "weekly.csv")
	if err := os.WriteFile(localFile, []byte("a,b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newDataCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{"push", "demo", localFile, "--dest", "sales.csv"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{`"path"`, `"sales.csv"`, `"bytes"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("JSON output %q missing %q", out.String(), want)
		}
	}
}
