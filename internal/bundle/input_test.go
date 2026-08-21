package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileInputsResolveAndSnapshot(t *testing.T) {
	root := t.TempDir()
	bundleRoot := filepath.Join(root, "app")
	if err := os.MkdirAll(filepath.Join(root, "_shared"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bundleRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "_shared", "theme.py")
	if err := os.WriteFile(source, []byte("COLOR = 'orange'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	resolved, err := ResolveFileInputs(root, bundleRoot, []FileInputSpec{{
		From: "_shared/theme.py",
		To:   "helpers/theme.py",
	}})
	if err != nil {
		t.Fatalf("ResolveFileInputs: %v", err)
	}
	snapshots, err := SnapshotFileInputs(resolved)
	if err != nil {
		t.Fatalf("SnapshotFileInputs: %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].From != "_shared/theme.py" ||
		snapshots[0].To != "helpers/theme.py" || string(snapshots[0].Data) != "COLOR = 'orange'\n" {
		t.Fatalf("snapshots = %+v", snapshots)
	}
	if got := snapshots[0].Mode.Perm(); got != 0o755 {
		t.Fatalf("mode = %o, want 755", got)
	}
}

func TestResolveFileInputsRejectsUnsafeOrInvisibleDestinations(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, root, app string) []FileInputSpec
		want  string
	}{
		{
			name: "parent traversal source",
			setup: func(t *testing.T, root, _ string) []FileInputSpec {
				writeBundleInputTestFile(t, filepath.Dir(root), "outside.py", "outside")
				return []FileInputSpec{{From: "../outside.py", To: "helpers/outside.py"}}
			},
			want: "no empty, . or ..",
		},
		{
			name: "absolute source",
			setup: func(t *testing.T, root, _ string) []FileInputSpec {
				return []FileInputSpec{{From: filepath.Join(root, "absolute.py"), To: "helpers/absolute.py"}}
			},
			want: "relative path",
		},
		{
			name: "missing source",
			setup: func(t *testing.T, _, _ string) []FileInputSpec {
				return []FileInputSpec{{From: "_shared/missing.py", To: "helpers/missing.py"}}
			},
			want: "no such file",
		},
		{
			name: "source is directory",
			setup: func(t *testing.T, root, _ string) []FileInputSpec {
				if err := os.MkdirAll(filepath.Join(root, "_shared", "directory"), 0o755); err != nil {
					t.Fatal(err)
				}
				return []FileInputSpec{{From: "_shared/directory", To: "helpers/directory"}}
			},
			want: "regular file",
		},
		{
			name: "existing destination",
			setup: func(t *testing.T, root, app string) []FileInputSpec {
				writeBundleInputTestFile(t, root, "_shared/theme.py", "theme")
				writeBundleInputTestFile(t, app, "helpers/theme.py", "vendored")
				return []FileInputSpec{{From: "_shared/theme.py", To: "helpers/theme.py"}}
			},
			want: "already exists",
		},
		{
			name: "input prefix conflict",
			setup: func(t *testing.T, root, _ string) []FileInputSpec {
				writeBundleInputTestFile(t, root, "_shared/a", "a")
				writeBundleInputTestFile(t, root, "_shared/b", "b")
				return []FileInputSpec{{From: "_shared/a", To: "helpers"}, {From: "_shared/b", To: "helpers/b.py"}}
			},
			want: "destination conflict",
		},
		{
			name: "non-directory destination ancestor",
			setup: func(t *testing.T, root, app string) []FileInputSpec {
				writeBundleInputTestFile(t, root, "_shared/theme.py", "theme")
				writeBundleInputTestFile(t, app, "helpers", "not a directory")
				return []FileInputSpec{{From: "_shared/theme.py", To: "helpers/theme.py"}}
			},
			want: "not a directory",
		},
		{
			name: "ignored ancestor",
			setup: func(t *testing.T, root, app string) []FileInputSpec {
				writeBundleInputTestFile(t, root, "_shared/theme.py", "theme")
				writeBundleInputTestFile(t, app, ".shinyhubignore", "helpers/\n")
				return []FileInputSpec{{From: "_shared/theme.py", To: "helpers/theme.py"}}
			},
			want: "ignored ancestor",
		},
		{
			name: "ignored destination",
			setup: func(t *testing.T, root, app string) []FileInputSpec {
				writeBundleInputTestFile(t, root, "_shared/theme.py", "theme")
				writeBundleInputTestFile(t, app, ".shinyhubignore", "helpers/theme.py\n")
				return []FileInputSpec{{From: "_shared/theme.py", To: "helpers/theme.py"}}
			},
			want: "destination is ignored",
		},
		{
			name: "protected destination",
			setup: func(t *testing.T, root, _ string) []FileInputSpec {
				writeBundleInputTestFile(t, root, "_shared/theme.sqlite", "not really sqlite")
				return []FileInputSpec{{From: "_shared/theme.sqlite", To: "helpers/theme.sqlite"}}
			},
			want: "reject-extension",
		},
		{
			name: "oversized source",
			setup: func(t *testing.T, root, _ string) []FileInputSpec {
				p := filepath.Join(root, "_shared", "large.py")
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				f, err := os.Create(p)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.Truncate(DefaultRules().MaxFileBytes + 1); err != nil {
					_ = f.Close()
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
				return []FileInputSpec{{From: "_shared/large.py", To: "helpers/large.py"}}
			},
			want: "reject-file-size",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			app := filepath.Join(root, "app")
			if err := os.MkdirAll(app, 0o755); err != nil {
				t.Fatal(err)
			}
			specs := tc.setup(t, root, app)
			_, err := ResolveFileInputs(root, app, specs)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ResolveFileInputs error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestSnapshotFileInputsNormalizesNonExecutableMode(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBundleInputTestFile(t, root, "_shared/theme.py", "theme")
	resolved, err := ResolveFileInputs(root, app, []FileInputSpec{{From: "_shared/theme.py", To: "helpers/theme.py"}})
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := SnapshotFileInputs(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshots[0].Mode.Perm(); got != 0o644 {
		t.Fatalf("mode = %o, want 644", got)
	}
}

func TestResolveFileInputsRejectsSymlinkComponents(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	realShared := filepath.Join(root, "real-shared")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBundleInputTestFile(t, realShared, "theme.py", "theme")
	if err := os.Symlink(realShared, filepath.Join(root, "_shared")); err != nil {
		t.Fatal(err)
	}

	_, err := ResolveFileInputs(root, app, []FileInputSpec{{From: "_shared/theme.py", To: "helpers/theme.py"}})
	if err == nil || !strings.Contains(err.Error(), "symlink component") {
		t.Fatalf("ResolveFileInputs error = %v, want symlink-component rejection", err)
	}
}

func TestSnapshotFileInputsRevalidatesResolvedFile(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBundleInputTestFile(t, root, "_shared/theme.py", "safe")
	resolved, err := ResolveFileInputs(root, app, []FileInputSpec{{From: "_shared/theme.py", To: "helpers/theme.py"}})
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "_shared", "theme.py")
	outside := filepath.Join(root, "outside.py")
	writeBundleInputTestFile(t, root, "outside.py", "changed")
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, source); err != nil {
		t.Fatal(err)
	}

	if _, err := SnapshotFileInputs(resolved); err == nil || !strings.Contains(err.Error(), "changed after resolution") {
		t.Fatalf("SnapshotFileInputs error = %v, want changed-after-resolution rejection", err)
	}
}

func writeBundleInputTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
