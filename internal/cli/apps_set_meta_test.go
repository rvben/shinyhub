package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// --name and --description reach the wire under the keys PATCH /api/apps
// expects, trimmed, and are absent when not passed.
func TestAppsSet_NameAndDescription(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)

	if _, err := execCLI(t, "apps", "set", "demo",
		"--name", "  Quarterly Revenue  ", "--description", "  Regional roll-up  "); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != "PATCH" || req.Path != "/api/apps/demo" {
		t.Errorf("unexpected %s %s", req.Method, req.Path)
	}
	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got := body["name"]; got != "Quarterly Revenue" {
		t.Errorf("expected name=%q (trimmed), got %v", "Quarterly Revenue", got)
	}
	if got := body["description"]; got != "Regional roll-up" {
		t.Errorf("expected description=%q (trimmed), got %v", "Regional roll-up", got)
	}
	// The slug is not settable here; nothing may sneak onto the wire.
	if _, present := body["slug"]; present {
		t.Errorf("slug must never be sent by apps set, got %v", body["slug"])
	}

	// A different flag, no --name/--description: neither key may be sent as "".
	// max-sessions-per-replica is not a restart change, so it reaches the wire
	// without --yes and the "absent" assertion is not vacuous.
	*reqs = nil
	if _, err := execCLI(t, "apps", "set", "demo", "--max-sessions-per-replica", "25"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body = nil
	if err := json.Unmarshal((*reqs)[0].Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	for _, k := range []string{"name", "description"} {
		if _, present := body[k]; present {
			t.Errorf("expected %s to be absent, got %v", k, body[k])
		}
	}
}

// --description "" is a clear, not a no-op, so the key must be on the wire.
// Built from Changed(), like --icon.
func TestAppsSet_DescriptionClear(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)

	if _, err := execCLI(t, "apps", "set", "demo", "--description", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal((*reqs)[0].Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	v, present := body["description"]
	if !present {
		t.Fatalf(`expected description to be present (value ""), got absent`)
	}
	if v != "" {
		t.Errorf(`expected description="", got %v`, v)
	}
}

// An invalid name fails locally with the shared spec's message and sends no
// request, so a bad value costs no round trip and cannot half-apply alongside
// a valid sibling flag.
func TestAppsSet_InvalidNameRejectedLocally(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)

	cases := map[string]string{
		"whitespace only": "   ",
		"empty":           "",
		"over the limit":  strings.Repeat("a", 129),
	}
	for label, name := range cases {
		t.Run(label, func(t *testing.T) {
			*reqs = nil
			_, err := execCLI(t, "apps", "set", "demo", "--name", name, "--max-sessions-per-replica", "25")
			if err == nil {
				t.Fatalf("expected an error for a %s name", label)
			}
			if kind, _ := classify(err); kind != KindValidation {
				t.Errorf("kind = %v, want %v", kind, KindValidation)
			}
			if len(*reqs) != 0 {
				t.Errorf("expected no request to be sent, got %d", len(*reqs))
			}
		})
	}

	// An over-long description is caught the same way.
	*reqs = nil
	if _, err := execCLI(t, "apps", "set", "demo", "--description", strings.Repeat("a", 281)); err == nil {
		t.Error("expected an error for an over-long description")
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no request to be sent, got %d", len(*reqs))
	}
}

// apps set with no flags names the new flags, so the error text stays a
// complete inventory of what the command can change.
func TestAppsSet_NoFlagsErrorNamesMetaFlags(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{}`)

	_, err := execCLI(t, "apps", "set", "demo")
	if err == nil {
		t.Fatal("expected an error when no flag is passed")
	}
	for _, want := range []string{"--name", "--description"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must mention %s, got %q", want, err.Error())
		}
	}
}
