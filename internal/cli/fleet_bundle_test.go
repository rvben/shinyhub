package cli

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/fleet"
)

func TestResolveLocalFleetBundleSpecsSharesOneSnapshotAcrossConsumers(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"apps/sales", "apps/operations", "_shared"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "_shared", "theme.py"), []byte("COLOR = 'orange'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &fleet.Manifest{
		FleetID: "analytics",
		Apps: []fleet.AppEntry{
			{Slug: "sales", Source: "./apps/sales"},
			{Slug: "operations", Source: "./apps/operations"},
		},
		BundleFiles: []fleet.BundleFileEntry{{
			From: "_shared/theme.py", To: "helpers/theme.py", Consumers: []string{"sales", "operations"},
		}},
	}
	specs, problems := resolveLocalFleetBundleSpecs(m, filepath.Join(root, "fleet.toml"), map[string]string{
		"sales": filepath.Join(root, "apps", "sales"), "operations": filepath.Join(root, "apps", "operations"),
	})
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}

	want := "COLOR = 'orange'\n"
	for _, slug := range []string{"sales", "operations"} {
		preview, err := buildBundlePreviewFromSpec(specs[slug])
		if err != nil {
			t.Fatalf("%s preview: %v", slug, err)
		}
		if got := composedZipEntry(t, preview.Buffer.Bytes(), "helpers/theme.py"); got != want {
			t.Fatalf("%s shared bytes = %q, want %q", slug, got, want)
		}
	}
}

func composedZipEntry(t *testing.T, raw []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range zr.File {
		if file.Name != name {
			continue
		}
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	t.Fatalf("zip entry %q not found", name)
	return ""
}
