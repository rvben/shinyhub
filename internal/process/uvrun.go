package process

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// uvRunFlagOptions are the uv options, global and `uv run`'s own, that take
// no value (uv 0.13, `uv run --help`). Together with uvGlobalValueOptions and
// uvRunValueOptions they are every option UVRunEnvironmentProbe accepts: an
// option outside all three makes the command unparseable rather than guessed
// at, because a value mistaken for the command would probe the wrong thing.
var uvRunFlagOptions = map[string]bool{
	"--all-extras": true, "--no-dev": true, "--only-dev": true,
	"--no-default-groups": true, "--all-groups": true, "--no-editable": true,
	"--exact": true, "--no-env-file": true, "--isolated": true, "--active": true,
	"--no-sync": true, "--locked": true, "--frozen": true, "--all-packages": true,
	"--no-project": true, "--no-index": true, "-U": true, "--upgrade": true,
	"--no-sources": true, "--reinstall": true, "--compile-bytecode": true,
	"--no-build-isolation": true, "--no-build": true, "--no-binary": true,
	"--require-build-hashes": true, "--no-require-build-hashes": true,
	"-n": true, "--no-cache": true, "--refresh": true, "--managed-python": true,
	"--no-managed-python": true, "--no-python-downloads": true, "-q": true,
	"--quiet": true, "-v": true, "--verbose": true, "--system-certs": true,
	"--offline": true, "--no-progress": true, "--no-config": true,
	"--native-tls": true, "--preview": true, "--no-preview": true,
}

// uvFreshOptions are the uv options that make a launch resolve or download
// again rather than use what uv's cache holds.
var uvFreshOptions = map[string]bool{
	"-n": true, "--no-cache": true, "--refresh": true, "--refresh-package": true,
	"-U": true, "--upgrade": true, "-P": true, "--upgrade-package": true,
	"--upgrade-group": true, "--reinstall": true, "--reinstall-package": true,
}

// uvPathOptions are the uv options whose value is a path uv reads or writes.
var uvPathOptions = map[string]bool{
	"--with-requirements": true, "--with-editable": true, "--env-file": true,
	"--cache-dir": true, "--config-file": true, "-f": true, "--find-links": true,
}

// uvPathEnvVars are the environment twins of uvPathOptions. UV_FIND_LINKS
// holds a comma-separated list and UV_ENV_FILE a space-separated one.
var uvPathEnvVars = []string{"UV_ENV_FILE", "UV_CACHE_DIR", "UV_CONFIG_FILE", "UV_FIND_LINKS"}

// maxProbeInputBytes bounds a file the probe reads to plan the check.
const maxProbeInputBytes = 16 << 20

// UVRunProbe is the outcome of UVRunEnvironmentProbe for one command.
type UVRunProbe struct {
	// Argv builds the command's uv environment and then runs the server's own
	// `true` in it instead of the command. It is nil when the probe cannot
	// stand in for the command, and NotChecked then says why.
	Argv []string
	// OfflineArgv is Argv with uv's --offline: it builds the same environment
	// from uv's cache alone.
	OfflineArgv []string
	// OfflineCommand is the command itself with uv's --offline, for running it
	// once Argv or OfflineArgv has built its environment: uv then needs nothing
	// that is not already in its cache. --offline governs only this uv; the
	// command's own processes get the same environment as without it.
	//
	// OfflineArgv and OfflineCommand are nil when the command needs the
	// network at every launch: it disables uv's cache (--no-cache, or
	// UV_NO_CACHE in env), asks uv to refresh, upgrade or reinstall, or reads
	// its requirements from a URL. Running it offline would change what it
	// installs or make uv refuse it.
	OfflineCommand []string
	NotChecked     string
}

// uvRunNoop resolves the absolute path of the server's own `true`, which the
// probe runs in place of the command. A bare `true` would be looked up by uv
// in the environment it builds, whose bin directory comes first and can hold
// an executable the app's dependencies install.
var uvRunNoop = sync.OnceValues(func() (string, error) {
	path, err := exec.LookPath("true")
	if err == nil && !filepath.IsAbs(path) {
		err = errors.New("true is not on an absolute PATH entry")
	}
	return path, err
})

// UVRunEnvironmentProbe rewrites a `uv [global options] run [options]
// <command>` argv, run in dir with the environment variables env, into one
// that resolves and installs the same
// environment and then runs `true` in it: every uv option is kept and the
// command is replaced by `-- true`. uv prepares the environment before it
// starts the command, so the probe fails where the command would fail to get
// an environment, without running the command or any interpreter of the app.
//
// ok is false when argv does not launch uv at all, so there is no uv
// environment to reproduce. A uv launch the probe cannot reproduce exactly
// returns ok with a NotChecked reason; the caller must report such a command
// as not checked, never as passed. Those are:
//   - a uv given as a relative path or in the bundle (the app's own launcher),
//     `uvx` and other subcommands, and --script and --gui-script;
//   - a script read from stdin or a URL, a Python file with inline script
//     metadata (uv builds the script's own environment), and a Python file
//     whose directory would make uv discover a different project than uv's
//     working directory does;
//   - a --directory or --project outside dir, where building the environment
//     would write outside the bundle, and a UV_PROJECT_ENVIRONMENT (or with
//     --active a VIRTUAL_ENV) outside the bundle;
//   - anything in the bundle's data dir, which the check does not get: the
//     working directory, the project, a Python target, a path option or its
//     environment twin (uvPathEnvVars), a local --with requirement, a
//     requirements include or --find-links, and UV_PROJECT_ENVIRONMENT;
//   - a requirements file or Python target that is not a regular file of
//     bounded size.
//
// UV_WORKING_DIR and UV_PROJECT in env stand in for the options when argv
// does not give them, as they do for uv.
func UVRunEnvironmentProbe(argv, env []string, dir string) (probe UVRunProbe, ok bool) {
	if len(argv) == 0 {
		return UVRunProbe{}, false
	}
	switch filepath.Base(argv[0]) {
	case "uvx":
		return UVRunProbe{NotChecked: "uvx builds its own tool environment"}, true
	case "uv":
	default:
		return UVRunProbe{}, false
	}
	notChecked := func(format string, args ...any) (UVRunProbe, bool) {
		return UVRunProbe{NotChecked: fmt.Sprintf(format, args...)}, true
	}
	// A bare name is the server's uv, found on the server PATH, and an
	// absolute path outside the bundle is an installed one. A uv given as a
	// relative path or in the bundle is the app's own launcher, which the
	// check must not start: it runs no app code.
	if strings.ContainsRune(argv[0], filepath.Separator) {
		root := filepath.Clean(dir)
		if _, inside := withinDir(root, root, argv[0]); inside || !filepath.IsAbs(argv[0]) {
			return notChecked("%s is the app's own launcher, not an installed uv", argv[0])
		}
	}
	out := []string{argv[0]}
	sawRun, sawOffline, sawActive := false, false, false
	module := "" // -m or --module, when the command is a module
	// online is set when the command must reach the network at every launch,
	// so it has no offline forms.
	online := uvEnvTrue(env, "UV_NO_CACHE")
	runAt := 0 // the index of `run` in argv, and in out
	target, directory, project := "", "", ""
	var inputs [][2]string // path-valued options and their values, in order
	pathOption := func(name, value string) {
		switch name {
		case "--directory":
			directory = value
		case "--project":
			project = value
		case "--with-requirements":
			// A remote requirements file is fetched at every launch.
			_, local := localPath(value)
			online = online || !local
		case "--with", "-w":
			for _, item := range strings.Split(value, ",") {
				if path := withRequirementPath(item); path != "" {
					inputs = append(inputs, [2]string{name, path})
				}
			}
		}
		if uvFreshOptions[name] {
			online = true
		}
		if path, local := localPath(value); uvPathOptions[name] && local {
			inputs = append(inputs, [2]string{name, path})
		}
	}
	for i := 1; i < len(argv) && target == ""; i++ {
		arg := argv[i]
		switch {
		case !sawRun && !strings.HasPrefix(arg, "-"):
			if arg != "run" {
				return notChecked("uv %s is not uv run", arg)
			}
			sawRun, runAt = true, i
			out = append(out, arg)
		case sawRun && arg == "--":
			if i+1 >= len(argv) {
				return notChecked("uv run has no command")
			}
			target = argv[i+1]
		case sawRun && (arg == "-m" || arg == "--module"):
			// `uv run -m mod` runs `python -m mod` in the same environment.
			// The flag can precede more uv options, so the module is the
			// next positional. The probe runs no module, so the flag is left
			// out of it.
			module = arg
		case sawRun && (arg == "-s" || arg == "--script" || arg == "--gui-script"):
			return notChecked("uv run %s builds the script's own environment", arg)
		case arg == "-h" || arg == "--help" || arg == "-V" || arg == "--version":
			return notChecked("uv %s builds no environment", arg)
		case strings.HasPrefix(arg, "--") && strings.Contains(arg, "="):
			name, value, _ := strings.Cut(arg, "=")
			if !uvValueOption(name, sawRun) {
				return notChecked("unrecognized uv option %q", name)
			}
			pathOption(name, value)
			out = append(out, arg)
		case strings.HasPrefix(arg, "-") && arg != "-":
			switch {
			case uvValueOption(arg, sawRun):
				if i+1 >= len(argv) {
					return notChecked("uv option %s has no value", arg)
				}
				pathOption(arg, argv[i+1])
				out = append(out, arg, argv[i+1])
				i++
			case uvRunFlagOptions[arg] || isRepeatedShortFlag(arg):
				sawOffline = sawOffline || arg == "--offline"
				sawActive = sawActive || arg == "--active"
				online = online || uvFreshOptions[arg]
				out = append(out, arg)
			default:
				return notChecked("unrecognized uv option %q", arg)
			}
		default:
			// The first positional after `run` is the command. A bare `-` is a
			// script on stdin.
			target = arg
		}
	}
	switch {
	case !sawRun:
		return notChecked("uv has no subcommand")
	case module != "" && target == "":
		return notChecked("uv run %s has no module", module)
	case target == "":
		return notChecked("uv run has no command")
	case target == "-":
		return notChecked("uv run reads its script from stdin")
	case strings.Contains(target, "://"):
		// The URL is left out: it can carry an access token.
		return notChecked("uv run fetches its script from a URL")
	}
	// A reason names an option with its value, but an env var by name alone:
	// the var may be a secret.
	directoryLabel, projectLabel := "uv --directory "+directory, "uv --project "+project
	if directory == "" {
		directory, directoryLabel = launchEnvValue(env, "UV_WORKING_DIR"), "UV_WORKING_DIR"
	}
	if project == "" {
		project, projectLabel = launchEnvValue(env, "UV_PROJECT"), "UV_PROJECT"
	}
	root := filepath.Clean(dir)
	cwd := root
	if directory != "" {
		var ok bool
		if cwd, ok = withinDir(root, root, directory); !ok {
			return notChecked("%s is outside the bundle", directoryLabel)
		}
	}
	// A relative --project is resolved from uv's working directory.
	if project != "" {
		if _, ok := withinDir(root, cwd, project); !ok {
			return notChecked("%s is outside the bundle", projectLabel)
		}
	}
	// The check runs without the app's data, which a launch links into the
	// bundle as data/: a path there is absent from a fresh bundle, and in one
	// that has the link, building through it would write to live data.
	data := filepath.Join(root, "data")
	if isWithin(data, cwd) {
		return notChecked("%s is in the app's data dir, which the check does not get", directoryLabel)
	}
	if project != "" {
		if path, _ := withinDir(root, cwd, project); isWithin(data, path) {
			return notChecked("%s is in the app's data dir, which the check does not get", projectLabel)
		}
	}
	for _, input := range inputs {
		path, _ := withinDir(root, cwd, input[1])
		readsData := isWithin(data, path)
		if !readsData && input[0] == "--with-requirements" {
			var err error
			if readsData, err = requirementsReadData(data, cwd, path, map[string]bool{}); err != nil {
				return notChecked("uv %s %s cannot be inspected: %v", input[0], input[1], err)
			}
		}
		if readsData {
			return notChecked("uv %s %s reads from the app's data dir, which the check does not get", input[0], input[1])
		}
	}
	for _, name := range uvPathEnvVars {
		value := launchEnvValue(env, name)
		paths := []string{value}
		switch name {
		case "UV_FIND_LINKS":
			paths = strings.Split(value, ",")
		case "UV_ENV_FILE":
			paths = strings.Fields(value)
		}
		for _, p := range paths {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			if path, _ := withinDir(root, cwd, p); isWithin(data, path) {
				return notChecked("%s is in the app's data dir, which the check does not get", name)
			}
		}
	}
	// The environment uv syncs: UV_PROJECT_ENVIRONMENT, or with --active the
	// active VIRTUAL_ENV. uv places a relative UV_PROJECT_ENVIRONMENT in the
	// project root, which lies at or below the bundle root. Resolved from the
	// bundle root instead, a path that would land in data/ or outside the
	// bundle from a deeper root lands outside it, so the check is
	// conservative.
	venvs := []string{"UV_PROJECT_ENVIRONMENT"}
	if sawActive {
		venvs = append(venvs, "VIRTUAL_ENV")
	}
	for _, name := range venvs {
		if venv := launchEnvValue(env, name); venv != "" {
			if path, ok := withinDir(root, root, venv); !ok || isWithin(data, path) {
				return notChecked("%s is outside the bundle or in its data dir", name)
			}
		}
	}
	if strings.HasSuffix(target, ".py") || strings.HasSuffix(target, ".pyw") {
		// uv reads a script target for its inline dependencies.
		if path, _ := withinDir(root, cwd, target); isWithin(data, path) {
			return notChecked("uv run %s is in the app's data dir, which the check does not get", target)
		}
		if reason := pythonTargetDiverges(cwd, target, project != ""); reason != "" {
			return notChecked("%s", reason)
		}
	}
	noop, err := uvRunNoop()
	if err != nil {
		return notChecked("the server has no true executable to probe with: %v", err)
	}
	probe = UVRunProbe{Argv: append(out, "--", noop)}
	if online {
		return probe, true
	}
	probe.OfflineArgv, probe.OfflineCommand = slices.Clone(probe.Argv), slices.Clone(argv)
	if !sawOffline {
		probe.OfflineArgv = slices.Insert(probe.OfflineArgv, runAt+1, "--offline")
		probe.OfflineCommand = slices.Insert(probe.OfflineCommand, runAt+1, "--offline")
	}
	return probe, true
}

// requirementsReadData reports whether the requirements file at path names a
// path in data: an include (-r, -c) or --find-links there, in it or in a file
// it includes. A missing or unreadable file is uv's to report; one that is not
// a regular file of bounded size is an error, as reading it could block or
// never end.
func requirementsReadData(data, cwd, path string, seen map[string]bool) (bool, error) {
	if isWithin(data, path) {
		return true, nil
	}
	if seen[path] || len(seen) >= maxRequirementsReads {
		return false, nil
	}
	seen[path] = true
	raw, err := readProbeInput(path)
	if errors.Is(err, errUninspectable) {
		return false, err
	}
	if err != nil {
		return false, nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\\\n", " "), "\n") {
		fields := strings.Fields(stripRequirementsComment(line))
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "-") {
			continue
		}
		name, value, ok := splitRequirementsOption(fields)
		if !ok {
			continue
		}
		if value, ok = localPath(value); !ok {
			continue
		}
		if (name == "--requirement" || name == "--constraint" || name == "--find-links") && requirementsEnvRef.MatchString(value) {
			// uv expands the reference against the launch env, so the path
			// it names is not known here.
			return false, fmt.Errorf("%s names %s through an env var reference", filepath.Base(path), name)
		}
		switch name {
		case "--requirement", "--constraint":
			// An include is relative to the file that declares it.
			if !filepath.IsAbs(value) {
				value = filepath.Join(filepath.Dir(path), value)
			}
			if readsData, err := requirementsReadData(data, cwd, filepath.Clean(value), seen); readsData || err != nil {
				return readsData, err
			}
		case "--find-links":
			// uv resolves --find-links from its working directory.
			if !filepath.IsAbs(value) {
				value = filepath.Join(cwd, value)
			}
			if isWithin(data, filepath.Clean(value)) {
				return true, nil
			}
		}
	}
	return false, nil
}

// withRequirementPath is the local path a `uv run --with` requirement names,
// as a path or a file URL, bare or after "name @"; "" for a package name or
// a remote reference.
func withRequirementPath(item string) string {
	item = strings.TrimSpace(item)
	if _, ref, ok := strings.Cut(item, "@"); ok {
		item = strings.TrimSpace(ref)
	}
	if path, local := localPath(item); local && strings.ContainsRune(path, filepath.Separator) {
		return path
	}
	return ""
}

// localPath is the path a uv path value names, as a path or a file URL; ok is
// false for a remote URL.
func localPath(value string) (path string, ok bool) {
	if path, ok := strings.CutPrefix(value, "file://"); ok {
		return path, true
	}
	return value, !strings.Contains(value, "://")
}

// errUninspectable marks a probe input that is not a regular file of at most
// maxProbeInputBytes.
var errUninspectable = errors.New("not a regular file of bounded size")

// readProbeInput reads the file at path if it is a regular file of at most
// maxProbeInputBytes, and no more than that if it grows. It checks before
// opening, since opening a FIFO blocks.
func readProbeInput(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxProbeInputBytes {
		return nil, fmt.Errorf("%s is %w", filepath.Base(path), errUninspectable)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxProbeInputBytes))
}

// launchEnvValue is the value of name in env, the last entry winning as it does
// for the launched process; "" when it is absent.
func launchEnvValue(env []string, name string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if value, found := strings.CutPrefix(env[i], name+"="); found {
			return value
		}
	}
	return ""
}

// uvEnvTrue reports whether env sets the uv boolean variable name, which uv
// reads as false only when empty or one of its false spellings.
func uvEnvTrue(env []string, name string) bool {
	switch strings.ToLower(launchEnvValue(env, name)) {
	case "", "0", "false", "no", "n", "off":
		return false
	}
	return true
}

// withinDir resolves path from base and reports whether it stays inside root,
// lexically and, when it exists, after resolving symlinks.
func withinDir(root, base, path string) (string, bool) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	path = filepath.Clean(path)
	if !isWithin(root, path) {
		return path, false
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		resolvedRoot, rootErr := filepath.EvalSymlinks(root)
		if rootErr != nil || !isWithin(resolvedRoot, resolved) {
			return path, false
		}
	}
	return path, true
}

func isWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// uvValueOption reports whether name takes the next argument as its value.
// Before `run` only uv's global options can appear.
func uvValueOption(name string, afterRun bool) bool {
	if uvGlobalValueOptions[name] {
		return true
	}
	return afterRun && uvRunValueOptions[name]
}

// pythonTargetDiverges says why `uv run <target>` would build a different
// environment than `uv run -- true` from uv's working directory cwd, or ""
// when it builds the same. With an explicit --project (projectSet) uv takes
// the project from it rather than from the target's directory.
func pythonTargetDiverges(cwd, target string, projectSet bool) string {
	path, ok := withinDir(cwd, cwd, target)
	if !ok {
		return fmt.Sprintf("%s is outside uv's working directory", target)
	}
	if hasInlineScriptMetadata(path) {
		return fmt.Sprintf("%s declares inline script dependencies", target)
	}
	if projectSet {
		return ""
	}
	// uv also takes the Python version a script runs with from a
	// .python-version beside it.
	for d := filepath.Dir(path); d != cwd; d = filepath.Dir(d) {
		rel := strings.TrimPrefix(d, cwd+string(filepath.Separator))
		if _, err := os.Lstat(filepath.Join(d, "pyproject.toml")); err == nil {
			return fmt.Sprintf("%s belongs to the project in %s", target, rel)
		}
		if _, err := os.Lstat(filepath.Join(d, ".python-version")); err == nil {
			return fmt.Sprintf("%s has its own Python version pin in %s", target, rel)
		}
	}
	return ""
}

// isRepeatedShortFlag accepts uv's stackable verbosity flags (-qq, -vvv).
func isRepeatedShortFlag(arg string) bool {
	if len(arg) < 3 || arg[0] != '-' || arg[1] == '-' {
		return false
	}
	c := arg[1]
	if c != 'q' && c != 'v' {
		return false
	}
	return strings.Trim(arg[1:], string(c)) == ""
}

// hasInlineScriptMetadata reports whether a Python file carries a PEP 723
// `# /// script` block, which makes `uv run file.py` build the script's own
// environment instead of the project's. A file that exists but cannot be read,
// or is not a regular file of bounded size, is reported as carrying one, so the probe declines rather than checks the
// wrong environment.
func hasInlineScriptMetadata(path string) bool {
	data, err := readProbeInput(path)
	if err != nil {
		return !os.IsNotExist(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimRight(line, " \t\r") == "# /// script" {
			return true
		}
	}
	return false
}
