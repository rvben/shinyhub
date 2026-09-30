package pythontrace

import (
	"reflect"
	"slices"
	"testing"
)

func TestWrapPreservesCommandContracts(t *testing.T) {
	overlay := []string{"otel-extra"}
	prefix := []string{"uv", "run", "--frozen", "--no-sync", "--with", "otel-extra"}
	for _, tc := range []struct {
		name   string
		argv   []string
		mode   string
		target []string
		flags  []string
	}{
		{"uv script", []string{"uv", "run", "python", "helpers/fetch.py", "hello world"}, "job-script", []string{"helpers/fetch.py", "hello world"}, nil},
		{"uv options", []string{"uv", "run", "--offline", "--with-requirements=reqs.txt", "--", "python", "-u", "-m", "helpers.fetch", "--flag"}, "job-module", []string{"helpers.fetch", "--flag"}, []string{"-u"}},
		{"raw script", []string{"python3", "-B", "task.py", "--one"}, "job-script", []string{"task.py", "--one"}, []string{"-B"}},
		{"inline code", []string{"python", "-c", "raise SystemExit(7)", "arg"}, "job-code", []string{"raise SystemExit(7)", "arg"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := slices.Clone(tc.argv)
			got, ok := Wrap(tc.argv, overlay, prefix)
			if !ok {
				t.Fatal("supported command not instrumented")
			}
			i := slices.Index(got, Bootstrap)
			if i < 1 || got[i-1] != "-c" || got[i+1] != tc.mode || !reflect.DeepEqual(got[i+2:], tc.target) {
				t.Fatalf("command/arguments changed (bootstrap index %d)", i)
			}
			for _, flag := range tc.flags {
				if !slices.Contains(got[:i-1], flag) {
					t.Errorf("lost interpreter flag %s", flag)
				}
			}
			if tc.argv[0] == "uv" && !slices.Contains(got[:i], "--with") {
				t.Fatal("overlay missing")
			}
			if !reflect.DeepEqual(input, tc.argv) {
				t.Fatal("input mutated")
			}
		})
	}
}

func TestWrapLeavesUnsupportedCommandsUntouched(t *testing.T) {
	for _, argv := range [][]string{
		nil, {"Rscript", "task.R"}, {"sh", "-c", "python task.py"},
		{"uv", "run", "--project", "other", "python", "task.py"},
		{"uv", "run", "--with"}, {"uv", "run", "--script", "task.py"},
		{"uv", "run", "--", "bash", "task.sh"}, {"uv", "sync"},
		{"python", "-I", "task.py"}, {".venv/bin/python", "task.py"},
		{"python3.12", "task.py"}, {"python", "-"}, {"python", "-m"},
	} {
		got, ok := Wrap(argv, []string{"otel"}, []string{"uv", "run"})
		if ok || !reflect.DeepEqual(got, argv) {
			t.Fatalf("unsupported argv changed: %q", argv)
		}
	}
	argv := []string{"uv", "run", "python", "task.py"}
	if got, ok := Wrap(argv, nil, nil); ok || !reflect.DeepEqual(got, argv) {
		t.Fatal("disabled instrumentation changed argv")
	}
}
