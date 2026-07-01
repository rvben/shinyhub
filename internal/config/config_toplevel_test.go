package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const validSecret = "auth:\n  secret: \"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\"\n"

// A `snapshot:` at the top level belongs under `runtime:`. It must be rejected
// with a clear error, not silently dropped - the regression where an operator's
// snapshot.enabled: true had no effect and no warning.
func TestLoad_RejectsMisplacedTopLevelSnapshot(t *testing.T) {
	p := writeCfg(t, validSecret+"snapshot:\n  enabled: true\n")
	_, err := config.Load(p)
	if err == nil || !strings.Contains(err.Error(), "snapshot") {
		t.Fatalf("expected an error naming the misplaced 'snapshot' key, got %v", err)
	}
	if !strings.Contains(err.Error(), "runtime.snapshot") {
		t.Errorf("error should hint the correct nesting 'runtime.snapshot', got: %v", err)
	}
}

func TestLoad_RejectsUnknownTopLevelKey(t *testing.T) {
	p := writeCfg(t, validSecret+"totally_bogus_key: 5\n")
	_, err := config.Load(p)
	if err == nil || !strings.Contains(err.Error(), "totally_bogus_key") {
		t.Fatalf("expected an error naming the unknown key, got %v", err)
	}
}

// An existing-but-empty (or comment-only) config file is a likely
// misconfiguration - a botched mount, a truncated write - so it must fail loud
// rather than silently start with defaults + env overrides. (Env-only setups
// pass no config path at all, which is a separate, allowed case.)
func TestLoad_RejectsEmptyConfigFile(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	p := writeCfg(t, "")
	if _, err := config.Load(p); err == nil {
		t.Fatal("an existing empty config file must be rejected, not silently defaulted")
	}
}

func TestLoad_RejectsCommentOnlyConfigFile(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	p := writeCfg(t, "# a comment, no actual config\n")
	if _, err := config.Load(p); err == nil {
		t.Fatal("a comment-only config file must be rejected")
	}
}

// A config file split with a "---" separator silently drops every document
// after the first (both the struct decode and the key check only read the first
// document), so real config in a trailing document vanishes without warning.
// Reject it - the same silent-drop class this change fights.
func TestLoad_RejectsMultiDocumentConfig(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	p := writeCfg(t, validSecret+"---\nruntime:\n  snapshot:\n    enabled: true\n")
	_, err := config.Load(p)
	if err == nil || !strings.Contains(err.Error(), "document") {
		t.Fatalf("expected a multi-document rejection, got %v", err)
	}
}

// A single leading "---" marker is still one document and must load normally.
func TestLoad_AcceptsLeadingDocumentMarker(t *testing.T) {
	p := writeCfg(t, "---\n"+validSecret+"runtime:\n  snapshot:\n    enabled: true\n")
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("a single document with a leading '---' must load: %v", err)
	}
	if !cfg.Runtime.Snapshot.Enabled {
		t.Error("runtime.snapshot.enabled lost on a leading-marker document")
	}
}

func TestLoad_AcceptsValidTopLevelKeysAndNesting(t *testing.T) {
	p := writeCfg(t, validSecret+"server:\n  port: 9090\nruntime:\n  snapshot:\n    enabled: true\n")
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("valid config must load: %v", err)
	}
	if !cfg.Runtime.Snapshot.Enabled {
		t.Error("correctly-nested runtime.snapshot.enabled was lost")
	}
}
