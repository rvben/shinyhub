package deploy

import (
	"strings"
	"testing"
)

// A manifest declaring name/description parses into the pointers the reconcile
// layer reads, with both values trimmed at load so the stored value never
// carries the manifest's padding.
func TestLoadManifestAppNameDescription(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[app]
name = "  Quarterly Revenue  "
description = "  Regional roll-up  "
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.App.Name == nil {
		t.Fatal("name not decoded")
	}
	if *m.App.Name != "Quarterly Revenue" {
		t.Errorf("name = %q, want %q (trimmed at load)", *m.App.Name, "Quarterly Revenue")
	}
	if m.App.Description == nil {
		t.Fatal("description not decoded")
	}
	if *m.App.Description != "Regional roll-up" {
		t.Errorf("description = %q, want %q (trimmed at load)", *m.App.Description, "Regional roll-up")
	}
}

// Absent keys stay nil. This is what makes a name set in the dashboard survive
// a deploy from a manifest that says nothing about it: a non-nil "" would
// instead be reconciled as a value.
func TestLoadManifestAppNameDescriptionAbsent(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[app]
replicas = 2
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.App.Name != nil {
		t.Errorf("name = %q, want nil for an absent key", *m.App.Name)
	}
	if m.App.Description != nil {
		t.Errorf("description = %q, want nil for an absent key", *m.App.Description)
	}
}

// An empty description is a declared value (it clears the field), unlike an
// empty name which has no meaningful stored form.
func TestLoadManifestAppDescriptionEmptyIsDeclared(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
[app]
description = ""
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.App.Description == nil {
		t.Fatal(`description = nil, want a declared ""`)
	}
	if *m.App.Description != "" {
		t.Errorf("description = %q, want %q", *m.App.Description, "")
	}
}

// Invalid metadata is rejected at manifest load so `shinyhub manifest validate`
// catches it locally rather than deferring to the server.
func TestLoadManifestRejectsInvalidAppMeta(t *testing.T) {
	cases := map[string]struct {
		toml string
		want string
	}{
		"empty name":            {toml: "name = \"\"", want: "name"},
		"whitespace-only name":  {toml: "name = \"   \"", want: "name"},
		"over-long name":        {toml: "name = \"" + strings.Repeat("a", 129) + "\"", want: "name"},
		"over-long description": {toml: "description = \"" + strings.Repeat("a", 281) + "\"", want: "description"},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			dir := t.TempDir()
			writeManifest(t, dir, "[app]\n"+tc.toml+"\n")
			_, err := LoadManifest(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected a %s validation error, got %v", tc.want, err)
			}
		})
	}
}

// A manifest declaring only display metadata must not be zero, or the deploy
// response summary and the audit event are both skipped.
func TestAppSettingsIsZeroWithNameDescription(t *testing.T) {
	name := "Quarterly Revenue"
	if (AppSettings{Name: &name}).IsZero() {
		t.Error("AppSettings{Name} reports zero; the apply would go unreported")
	}
	empty := ""
	if (AppSettings{Description: &empty}).IsZero() {
		t.Error(`AppSettings{Description: ""} reports zero; clearing would go unreported`)
	}
}
