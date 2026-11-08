package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

// A misspelled key nested inside a section used to be dropped without a word,
// so `server.drian_timeout: 5s` left the default drain timeout in force and the
// operator none the wiser. Every level of the tree is checked, and the error
// names the full path plus the closest accepted sibling.
func TestLoad_RejectsUnknownNestedKey(t *testing.T) {
	p := writeCfg(t, validSecret+"server:\n  drian_timeout: 5s\n")
	_, err := config.Load(p)
	if err == nil {
		t.Fatal("a misspelled nested key must be rejected, not silently dropped")
	}
	for _, want := range []string{`"server.drian_timeout"`, `"server.drain_timeout"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should contain %s, got: %v", want, err)
		}
	}
}

// Keys inside list elements are checked too, with the element index in the path.
func TestLoad_RejectsUnknownKeyInListElement(t *testing.T) {
	p := writeCfg(t, validSecret+"runtime:\n  tiers:\n    - name: a\n      runtime: docker\n      lanch_type: FARGATE\n")
	_, err := config.Load(p)
	if err == nil || !strings.Contains(err.Error(), `"runtime.tiers[0].lanch_type"`) {
		t.Fatalf("expected an error naming runtime.tiers[0].lanch_type, got %v", err)
	}
	if !strings.Contains(err.Error(), `"runtime.tiers[0].launch_type"`) {
		t.Errorf("error should suggest the accepted sibling launch_type, got: %v", err)
	}
}

// A real key placed under the wrong section is the other silent-drop shape:
// two shipped example configs carried `defaults.timezone`, which the loader
// ignored while every schedule kept firing in UTC. The hint points at the
// section that does accept the key.
func TestLoad_RejectsMisplacedNestedKey(t *testing.T) {
	p := writeCfg(t, validSecret+"defaults:\n  timezone: Europe/Amsterdam\n")
	_, err := config.Load(p)
	if err == nil || !strings.Contains(err.Error(), `"defaults.timezone"`) {
		t.Fatalf("expected an error naming defaults.timezone, got %v", err)
	}
	if !strings.Contains(err.Error(), `"scheduler.timezone"`) {
		t.Errorf("error should hint the section that accepts timezone, got: %v", err)
	}
}

// Fields typed as maps accept arbitrary keys by design; strictness must stop at
// them rather than reject every resource attribute an operator chooses.
func TestLoad_AcceptsArbitraryKeysInMapFields(t *testing.T) {
	p := writeCfg(t,
		"tracing:\n  resource_attributes:\n    deployment.environment: prod\n    team: platform\n"+
			"auth:\n  secret: \"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\"\n  trusted_publishers:\n    - name: ci\n      issuer: https://issuer.example\n      jwks_url: https://issuer.example/jwks\n      audience: shinyhub\n      subject: pipeline:app\n      apps: [demo]\n      claims:\n        project: example\n        anything_else: fine\n")
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("map-typed fields must accept any key: %v", err)
	}
	if cfg.Tracing.ResourceAttributes["deployment.environment"] != "prod" {
		t.Errorf("resource attribute lost: %+v", cfg.Tracing.ResourceAttributes)
	}
}

// The example configs shipped under deploy/ are what operators copy. Each must
// load under the strict loader, so a key the loader does not know cannot sit in
// a template and teach every reader the wrong spelling.
func TestLoad_ShippedExampleConfigsLoad(t *testing.T) {
	for _, rel := range []string{
		"deploy/demo/shinyhub.yaml",
		"deploy/cloudflare-demo/shinyhub.yaml",
		"deploy/docker-compose/shinyhub.yaml",
	} {
		t.Run(rel, func(t *testing.T) {
			if _, err := config.LoadForMaintenance(filepath.Join("..", "..", rel)); err != nil {
				t.Fatalf("shipped example must load: %v", err)
			}
		})
	}
}
