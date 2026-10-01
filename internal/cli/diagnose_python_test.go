package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func diagnosticPython(t *testing.T, supported bool) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell interpreter fixture needs POSIX")
	}
	path := filepath.Join(t.TempDir(), "python")
	info, _ := json.Marshal(map[string]any{"version": "3.15.0", "supported": supported})
	script := "#!/bin/sh\nif [ \"$2\" = '-c' ]; then\n  printf '%s\\n' '" + string(info) + "'\n  exit 0\nfi\n"
	script += "previous=''\nfor arg in \"$@\"; do\n  if [ \"$previous\" = '-o' ]; then printf '%s' '<html>profile</html>' > \"$arg\"; fi\n  previous=\"$arg\"\ndone\nprintf '%s\\n' \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPythonDiagnosticDumpDoesNotNeedCredentials(t *testing.T) {
	setupCLITest(t)
	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"diagnose", "python", "4321", "--python", diagnosticPython(t, true), "--async-aware", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var report pythonDiagnosticReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.PID != 4321 || report.Status != "inspected" || report.Path != "" {
		t.Fatalf("unexpected report: %+v", report)
	}
	for _, want := range []string{"dump\n-a\n", "--async-aware\n--async-mode\nall\n4321\n"} {
		if !strings.Contains(report.Stacks, want) {
			t.Fatalf("missing diagnostic options %q in %q", want, report.Stacks)
		}
	}
}

func TestPythonDiagnosticProfileIsPrivateAndNeverOverwrites(t *testing.T) {
	setupCLITest(t)
	python := diagnosticPython(t, true)
	destination := filepath.Join(t.TempDir(), "profile.html")
	args := []string{"diagnose", "python", "4321", "--python", python, "--save", destination, "--duration", "1s", "-o", "json"}
	if _, err := execCLI(t, args...); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("profile permissions: %v, %v", info, err)
	}
	if _, err := execCLI(t, args...); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing profile was not refused: %v", err)
	}
	body, _ := os.ReadFile(destination)
	if string(body) != "<html>profile</html>" {
		t.Fatalf("profile changed: %q", body)
	}
}

func TestPythonDiagnosticRejectsInvalidRequestsBeforeLaunch(t *testing.T) {
	setupCLITest(t)
	for _, args := range [][]string{
		{"0"}, {"-1"}, {"not-a-pid"},
		{"4321", "--duration", "1s"},
		{"4321", "--save", "unused.html", "--duration", "500ms"},
		{"4321", "--save", "unused.html", "--duration", "6m"},
		{"4321", "--host", "https://example.invalid"},
	} {
		_, err := execCLI(t, append([]string{"diagnose", "python"}, args...)...)
		if err == nil {
			t.Errorf("accepted %q", args)
		} else if kind, _ := classify(err); kind != KindValidation {
			t.Errorf("%q: expected validation error, got %v", args, err)
		}
	}
}

func TestPythonDiagnosticReportsUnsupportedInterpreter(t *testing.T) {
	setupCLITest(t)
	_, err := execCLI(t, "diagnose", "python", "4321", "--python", diagnosticPython(t, false))
	if err == nil || !strings.Contains(err.Error(), "select Python 3.15") {
		t.Fatalf("missing interpreter guidance: %v", err)
	}
}

func TestPrivateProfileRefusesSymlink(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	os.WriteFile(source, []byte("new"), 0600)
	target := filepath.Join(t.TempDir(), "target")
	os.WriteFile(target, []byte("original"), 0600)
	link := target + ".link"
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if err := copyPrivateProfile(source, link); err == nil {
		t.Fatal("profile followed an existing symlink")
	}
	body, _ := os.ReadFile(target)
	if string(body) != "original" {
		t.Fatalf("symlink target modified: %q", body)
	}
}
