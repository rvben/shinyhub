// Package pythontrace shares the Python bootstrap across app, job and hook
// launches. It runs in the overlay interpreter, never a venv console script.
package pythontrace

import (
	_ "embed"
	"path/filepath"
	"slices"
	"strings"
)

// Bootstrap configures instrumentation before executing Python application code.
//
//go:embed bootstrap.py
var Bootstrap string

// ModulePrefix runs an app module with ASGI send/receive suppression configured
// before the app constructs its middleware. The OTel CLI configures the SDK.
func ModulePrefix() []string {
	return []string{"opentelemetry-instrument", "python", "-c", Bootstrap, "app-module"}
}

// Wrap instruments explicit Python invocations only. UV options and interpreter
// flags are retained. Unknown commands/options are left untouched rather than
// guessing where application arguments start. rawPrefix is the bundle's usual
// uv environment prefix, with the instrumentation overlay already included.
func Wrap(argv, overlay, rawPrefix []string) ([]string, bool) {
	if len(argv) == 0 || len(overlay) == 0 {
		return argv, false
	}
	prefix := slices.Clone(rawPrefix)
	command := argv
	if filepath.Base(argv[0]) == "uv" {
		if len(argv) < 3 || argv[1] != "run" {
			return argv, false
		}
		i := 2
		for i < len(argv) && strings.HasPrefix(argv[i], "-") {
			opt := argv[i]
			if opt == "--" {
				i++
				break
			}
			key, _, hasValue := strings.Cut(opt, "=")
			switch key {
			case "--no-project", "--frozen", "--locked", "--no-sync", "--offline", "--no-dev", "--no-default-groups", "--no-managed-python", "--managed-python", "--no-python-downloads", "--no-index", "--no-config", "--no-cache", "--quiet", "-q", "--verbose", "-v":
				if hasValue {
					return argv, false
				}
				i++
			case "--with", "--with-requirements", "--with-editable", "--python", "-p", "--index", "--default-index", "--index-url", "--extra-index-url", "--find-links", "-f", "--group", "--no-group", "--extra", "--no-extra":
				if hasValue {
					i++
				} else {
					if i+1 >= len(argv) {
						return argv, false
					}
					i += 2
				}
			default:
				return argv, false
			}
		}
		if i >= len(argv) {
			return argv, false
		}
		// Insert the overlay before an optional -- command separator.
		prefix = slices.Clone(argv[:i])
		if prefix[len(prefix)-1] == "--" {
			prefix = prefix[:len(prefix)-1]
		}
		for _, pkg := range overlay {
			prefix = append(prefix, "--with", pkg)
		}
		prefix = append(prefix, "--")
		command = argv[i:]
	}
	if len(command) < 2 || (command[0] != "python" && command[0] != "python3") {
		// Absolute/venv interpreters are deliberately not replaced with uv's Python.
		return argv, false
	}
	flags := []string{}
	args := command[1:]
	for len(args) > 0 {
		switch args[0] {
		case "-u", "-B", "-O", "-OO", "-b", "-bb", "-q":
			flags = append(flags, args[0])
			args = args[1:]
		default:
			goto target
		}
	}
target:
	if len(args) == 0 {
		return argv, false
	}
	mode := "job-script"
	if args[0] == "-m" || args[0] == "-c" {
		if len(args) < 2 {
			return argv, false
		}
		if args[0] == "-m" {
			mode = "job-module"
		} else {
			mode = "job-code"
		}
		args = args[1:]
	} else if strings.HasPrefix(args[0], "-") {
		return argv, false
	}
	prefix = append(prefix, "opentelemetry-instrument", "python")
	prefix = append(prefix, flags...)
	prefix = append(prefix, "-c", Bootstrap, mode)
	return append(prefix, args...), true
}
