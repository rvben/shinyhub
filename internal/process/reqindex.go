package process

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/rvben/shinyhub/internal/spanerr"
)

// RequirementsIndex is the package-index configuration a bundle's
// requirements.txt declares through option lines (--index-url,
// --extra-index-url, --find-links, --no-index).
//
// uv honours those lines only under `uv pip`. The commands ShinyHub runs -
// `uv add --requirements` when it converts the bundle into a project, `uv
// sync`, and `uv run --with-requirements` - drop them and resolve from PyPI,
// so a bundle that names a private index would silently receive same-named
// public packages. Every one of those steps therefore gets this configuration
// explicitly: as environment (Env), which keeps credentials out of argv, plus
// the one option uv exposes only as a flag (Args).
type RequirementsIndex struct {
	// DefaultIndex replaces PyPI (--index-url / -i). The last declaration wins.
	DefaultIndex string
	// Indexes are additional indexes (--extra-index-url), in declaration order.
	Indexes []string
	// FindLinks are wheel locations (--find-links / -f). Local paths are
	// relative to the bundle root, where every uv step runs.
	FindLinks []string
	// NoIndex disables every registry index (--no-index).
	NoIndex bool

	// expanded holds the values substituted for ${NAME} references, which
	// are typically credentials; see Redact.
	expanded []string
}

// Redact masks in s every value the requirements substituted for a ${NAME}
// reference, so a diagnostic can show the index configuration without the
// credentials it was built from. URL userinfo is a separate concern, masked
// by spanerr.RedactURLUserinfo.
func (r RequirementsIndex) Redact(s string) string {
	vals := slices.Clone(r.expanded)
	// Longest first, so a value that contains another is masked whole.
	slices.SortFunc(vals, func(a, b string) int { return len(b) - len(a) })
	for _, v := range vals {
		s = strings.ReplaceAll(s, v, "***")
	}
	return s
}

// IsZero reports whether the requirements declare no index configuration.
func (r RequirementsIndex) IsZero() bool {
	return r.DefaultIndex == "" && len(r.Indexes) == 0 && len(r.FindLinks) == 0 && !r.NoIndex
}

// requirementsEnvRef matches the ${NAME} references pip and uv expand in a
// requirements file. Only upper-case names are variables.
var requirementsEnvRef = regexp.MustCompile(`\$\{([A-Z0-9_]+)\}`)

// ReadRequirementsIndex reads the index configuration from dir/requirements.txt
// and the files it includes (-r / -c). ${NAME} references expand against env
// (last occurrence wins, as os/exec resolves duplicates); an undefined name
// stays literal. An include that resolves outside dir is an error: following
// it would read host files, and skipping it would drop configuration.
//
// It returns the zero value when there is no requirements.txt, or when an
// author-shipped pyproject.toml owns the dependency configuration (a project
// EnsureProject synthesized from the requirements still follows them).
func ReadRequirementsIndex(dir string, env []string) (RequirementsIndex, error) {
	var idx RequirementsIndex
	if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); err == nil && !IsSynthesizedProject(dir) {
		return idx, nil
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return idx, err
	}
	p := &requirementsParser{root: root, env: envLookup(env), active: map[string]bool{}}
	if err := p.parseFile(filepath.Join(root, "requirements.txt")); err != nil {
		// Include paths are expanded before they are resolved and read, so
		// the error can quote a value a ${NAME} reference expanded to.
		return RequirementsIndex{}, redactedError{msg: p.idx.Redact(err.Error()), err: err}
	}
	return p.idx, nil
}

// redactedError replaces an error's message while keeping it unwrappable.
type redactedError struct {
	msg string
	err error
}

func (e redactedError) Error() string { return e.msg }
func (e redactedError) Unwrap() error { return e.err }

type requirementsParser struct {
	root string
	env  map[string]string
	// active holds the files being read, so an include cycle ends at the
	// file that started it; a file included again elsewhere is read again,
	// keeping options in declaration order.
	active map[string]bool
	// reads counts files read, bounded by maxRequirementsReads.
	reads int
	idx   RequirementsIndex
}

// maxRequirementsReads bounds how many files one requirements.txt may pull
// in through includes, counting repeats, so a fanned-out include graph fails
// instead of stalling the deploy.
const maxRequirementsReads = 1000

func (p *requirementsParser) parseFile(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, fs.ErrNotExist) {
		// A missing top-level file means there is nothing to read; a missing
		// include is uv's error to report, with its own message.
		return nil
	}
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(p.root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("requirements include %s resolves outside the bundle", path)
	}
	if p.active[resolved] {
		return nil
	}
	if p.reads++; p.reads > maxRequirementsReads {
		return fmt.Errorf("requirements includes read more than %d files", maxRequirementsReads)
	}
	p.active[resolved] = true
	defer delete(p.active, resolved)

	f, err := os.Open(resolved)
	if err != nil {
		return err
	}
	defer f.Close()
	baseDir := filepath.Dir(resolved)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var pending string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasSuffix(line, "\\") {
			pending += strings.TrimSuffix(line, "\\") + " "
			continue
		}
		line, pending = pending+line, ""
		if err := p.parseLine(line, baseDir); err != nil {
			return err
		}
	}
	if pending != "" {
		if err := p.parseLine(pending, baseDir); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// parseLine handles one logical line. Requirement lines are uv's to parse;
// only the index-related options and includes matter here.
func (p *requirementsParser) parseLine(line, baseDir string) error {
	fields := strings.Fields(stripRequirementsComment(line))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "-") {
		return nil
	}
	name, value, ok := splitRequirementsOption(fields)
	if !ok {
		return nil
	}
	raw := value
	value = p.expand(value)
	switch name {
	case "--index-url":
		p.idx.DefaultIndex = value
	case "--extra-index-url":
		p.idx.Indexes = appendUnique(p.idx.Indexes, value)
	case "--find-links":
		// uv resolves a relative path against its working directory, the
		// bundle root, not the declaring file, so it is only cleaned.
		if strings.Contains(value, ",") {
			return fmt.Errorf("--find-links %s contains a comma, which UV_FIND_LINKS cannot carry; percent-encode it as %%2C", spanerr.RedactURLs(value))
		}
		if !strings.Contains(value, "://") && !filepath.IsAbs(value) {
			value = filepath.ToSlash(filepath.Clean(value))
		}
		p.idx.FindLinks = appendUnique(p.idx.FindLinks, value)
	case "--no-index":
		p.idx.NoIndex = true
	case "--requirement", "--constraint":
		if strings.Contains(value, "://") {
			// raw, not value: an expanded reference may be a credential.
			return fmt.Errorf("requirements include %s is remote; its package-index options cannot be applied", spanerr.RedactURLs(raw))
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(baseDir, value)
		}
		return p.parseFile(value)
	}
	return nil
}

// requirementsOptionNames maps every accepted spelling to its long form.
var requirementsOptionNames = map[string]string{
	"-i": "--index-url", "--index-url": "--index-url",
	"--extra-index-url": "--extra-index-url",
	"-f":                "--find-links", "--find-links": "--find-links",
	"--no-index": "--no-index",
	"-r":         "--requirement", "--requirement": "--requirement",
	"-c": "--constraint", "--constraint": "--constraint",
}

// splitRequirementsOption returns the canonical option name and its value from
// the fields of an option line, accepting `--opt value`, `--opt=value`,
// `-o value` and `-ovalue`. ok is false for options this parser ignores.
func splitRequirementsOption(fields []string) (name, value string, ok bool) {
	head := fields[0]
	if strings.HasPrefix(head, "--") {
		if n, v, found := strings.Cut(head, "="); found {
			canon, known := requirementsOptionNames[n]
			return canon, v, known
		}
	} else if len(head) > 2 {
		canon, known := requirementsOptionNames[head[:2]]
		return canon, head[2:], known
	}
	canon, known := requirementsOptionNames[head]
	if !known {
		return "", "", false
	}
	if canon == "--no-index" {
		return canon, "", true
	}
	if len(fields) < 2 {
		return "", "", false
	}
	return canon, fields[1], true
}

// stripRequirementsComment removes a comment, which in a requirements file
// starts with # at the beginning of the line or after whitespace.
func stripRequirementsComment(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return line[:i]
		}
	}
	return line
}

func (p *requirementsParser) expand(value string) string {
	return requirementsEnvRef.ReplaceAllStringFunc(value, func(ref string) string {
		if v, ok := p.env[ref[2:len(ref)-1]]; ok {
			if v != "" && !slices.Contains(p.idx.expanded, v) {
				p.idx.expanded = append(p.idx.expanded, v)
			}
			return v
		}
		return ref
	})
}

func appendUnique(list []string, v string) []string {
	for _, have := range list {
		if have == v {
			return list
		}
	}
	return append(list, v)
}

// envLookup indexes KEY=VALUE entries, the last occurrence winning.
func envLookup(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			m[k] = v
		}
	}
	return m
}

// Env returns the uv environment entries that apply this configuration on top
// of base, the environment the uv step would otherwise run with. Appended
// after base, they win under last-occurrence-wins.
//
//   - The default index goes in UV_DEFAULT_INDEX, which uv prefers over
//     UV_INDEX_URL: the bundle names where its packages come from, as a
//     requirements file does for pip.
//   - Extra indexes go last in UV_EXTRA_INDEX_URL. uv tries UV_INDEX before
//     UV_EXTRA_INDEX_URL and each list in order, so the operator's indexes in
//     either variable keep their priority; UV_INDEX is left untouched, and an
//     index base already lists in either is not repeated.
//   - Find-links locations are added to base's UV_FIND_LINKS (comma-separated).
//
// NoIndex has no environment variable; see Args.
func (r RequirementsIndex) Env(base []string) []string {
	if r.IsZero() {
		return nil
	}
	have := envLookup(base)
	var out []string
	if r.DefaultIndex != "" {
		out = append(out, "UV_DEFAULT_INDEX="+r.DefaultIndex)
	}
	var extra []string
	for _, v := range r.Indexes {
		if !slices.Contains(strings.Fields(have["UV_INDEX"]), v) {
			extra = append(extra, v)
		}
	}
	if len(extra) > 0 {
		out = append(out, "UV_EXTRA_INDEX_URL="+mergeList(have["UV_EXTRA_INDEX_URL"], extra, " "))
	}
	if len(r.FindLinks) > 0 {
		out = append(out, "UV_FIND_LINKS="+mergeList(have["UV_FIND_LINKS"], r.FindLinks, ","))
	}
	return out
}

func mergeList(existing string, add []string, sep string) string {
	var list []string
	for _, v := range strings.Split(existing, sep) {
		if v = strings.TrimSpace(v); v != "" {
			list = appendUnique(list, v)
		}
	}
	for _, v := range add {
		list = appendUnique(list, v)
	}
	return strings.Join(list, sep)
}

// Args returns the uv flags this configuration needs beyond Env: --no-index,
// which uv accepts only on the command line. It carries no credentials.
func (r RequirementsIndex) Args() []string {
	if r.NoIndex {
		return []string{"--no-index"}
	}
	return nil
}

// Overrides returns the names of base's default-index variables that the
// bundle's --index-url replaces with a different value, so the deploy can say
// that a server or per-app setting was set aside.
func (r RequirementsIndex) Overrides(base []string) []string {
	if r.DefaultIndex == "" {
		return nil
	}
	have := envLookup(base)
	var out []string
	for _, key := range []string{"UV_DEFAULT_INDEX", "UV_INDEX_URL"} {
		if v, ok := have[key]; ok && v != "" && v != r.DefaultIndex {
			out = append(out, key)
		}
	}
	return out
}

// HasURLCredentials reports whether a KEY=VALUE entry's value holds a URL with
// userinfo (https://user:pass@host). Values may list several URLs separated
// by spaces or commas. Such an entry is a secret and travels as one.
func HasURLCredentials(entry string) bool {
	_, value, _ := strings.Cut(entry, "=")
	for _, part := range strings.FieldsFunc(value, func(c rune) bool { return c == ' ' || c == ',' }) {
		if u, err := url.Parse(part); err == nil && u.User != nil {
			return true
		}
	}
	return false
}

// RequirementsLaunch adapts a launch of argv in dir so that `uv run
// --with-requirements`, which ignores the index options in requirements.txt,
// resolves from the indexes the file names. base is the environment the
// launch otherwise runs with. It returns the command to run and the
// environment entries to add; a command other than uv is returned unchanged
// with no entries.
//
// --no-index has no environment variable, so it goes on the command line,
// directly after `run` (which may follow uv's global options), unless argv
// already carries it (uv rejects it twice). A uv launch that is not `uv run`
// has no place for it and fails rather than resolving from a registry the
// bundle disabled.
func RequirementsLaunch(dir string, argv, base []string) (cmd, env []string, err error) {
	if !IsUVCommand(argv) {
		return argv, nil, nil
	}
	idx, err := ReadRequirementsIndex(dir, base)
	if err != nil {
		return nil, nil, fmt.Errorf("read package-index options from requirements.txt: %w", err)
	}
	cmd = argv
	if idx.NoIndex {
		at := uvRunIndex(argv)
		if at < 0 {
			return nil, nil, fmt.Errorf("requirements.txt sets --no-index, which %q cannot take; launch with `uv run`", strings.Join(argv, " "))
		}
		if !uvRunHasNoIndex(argv, at) {
			cmd = slices.Concat(argv[:at+1], []string{"--no-index"}, argv[at+1:])
		}
	}
	return cmd, idx.Env(base), nil
}

// uvGlobalValueOptions are uv's global options that take their value as the
// next argument, which must not be mistaken for the subcommand.
var uvGlobalValueOptions = map[string]bool{
	"--color": true, "--allow-insecure-host": true, "--directory": true,
	"--project": true, "--config-file": true, "--cache-dir": true,
	"--python-preference": true,
}

// uvRunValueOptions are `uv run` options that take their value as the next
// argument. An option missing here is read as a flag, so its value ends the
// scan early: at worst --no-index is added twice, which uv rejects at launch,
// rather than skipped.
var uvRunValueOptions = map[string]bool{
	"--extra": true, "--no-extra": true, "--group": true, "--no-group": true,
	"--only-group": true, "--env-file": true, "--with": true,
	"--with-editable": true, "--with-requirements": true, "--package": true,
	"--index": true, "--default-index": true, "-i": true, "--index-url": true,
	"--extra-index-url": true, "-f": true, "--find-links": true,
	"--index-strategy": true, "--keyring-provider": true, "-P": true,
	"--upgrade-package": true, "--resolution": true, "--prerelease": true,
	"--fork-strategy": true, "--exclude-newer": true,
	"--reinstall-package": true, "--link-mode": true, "-C": true,
	"--config-setting": true, "--no-build-isolation-package": true,
	"--no-build-package": true, "--no-binary-package": true,
	"--refresh-package": true, "-p": true, "--python": true,
}

// uvRunHasNoIndex reports whether uv itself already receives --no-index:
// among the options before `run` at position at, or those after it up to the
// command. The same flag in the command's own arguments belongs to the app.
func uvRunHasNoIndex(argv []string, at int) bool {
	if slices.Contains(argv[1:at], "--no-index") {
		return true
	}
	for i := at + 1; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--no-index":
			return true
		case arg == "--" || !strings.HasPrefix(arg, "-"):
			return false
		case !strings.Contains(arg, "=") && (uvRunValueOptions[arg] || uvGlobalValueOptions[arg]):
			i++
		}
	}
	return false
}

// uvRunIndex returns the position of the `run` subcommand in a uv argv,
// skipping the global options before it, or -1 when the subcommand is not
// `run`.
func uvRunIndex(argv []string) int {
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "run":
			return i
		case strings.HasPrefix(arg, "-"):
			if !strings.Contains(arg, "=") && uvGlobalValueOptions[arg] {
				i++
			}
		default:
			return -1
		}
	}
	return -1
}

// IsUVCommand reports whether argv launches uv, the only launcher that reads
// requirements.txt.
func IsUVCommand(argv []string) bool {
	return len(argv) > 0 && filepath.Base(argv[0]) == "uv"
}

// applyRequirementsLaunchEnv adds the bundle's requirements index
// configuration to a uv launch (see RequirementsLaunch). Each entry replaces every same-named entry in
// both slices: runtimes apply SecretEnv after Env, so a leftover per-app
// secret would otherwise override the merged value. An entry goes in
// SecretEnv when any part of it may be secret: it merges a value that came
// from SecretEnv, it carries URL credentials or a query string (a signed
// URL's token), or it contains a secret's value (a ${NAME} reference expanded
// anywhere in a URL); the rest go in Env.
func applyRequirementsLaunchEnv(p *StartParams) error {
	if p.Dir == "" || !IsUVCommand(p.Command) {
		return nil
	}
	base := append(append(SanitizedEnv(), p.Env...), p.SecretEnv...)
	cmd, add, err := RequirementsLaunch(p.Dir, p.Command, base)
	if err != nil {
		return err
	}
	p.Command = cmd
	if len(add) == 0 {
		return nil
	}
	secretKeys := make(map[string]bool, len(p.SecretEnv))
	var secretValues []string
	for _, e := range p.SecretEnv {
		k, v, _ := strings.Cut(e, "=")
		secretKeys[k] = true
		if v != "" {
			secretValues = append(secretValues, v)
		}
	}
	isSecret := func(entry string) bool {
		k, v, _ := strings.Cut(entry, "=")
		if secretKeys[k] || HasURLCredentials(entry) || spanerr.RedactURLs(v) != v {
			return true
		}
		for _, s := range secretValues {
			if strings.Contains(v, s) {
				return true
			}
		}
		return false
	}
	keys := make(map[string]bool, len(add))
	for _, e := range add {
		k, _, _ := strings.Cut(e, "=")
		keys[k] = true
	}
	p.Env, p.SecretEnv = withoutEnvKeys(p.Env, keys), withoutEnvKeys(p.SecretEnv, keys)
	for _, e := range add {
		if isSecret(e) {
			p.SecretEnv = append(p.SecretEnv, e)
		} else {
			p.Env = append(p.Env, e)
		}
	}
	return nil
}

func withoutEnvKeys(env []string, keys map[string]bool) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if k, _, _ := strings.Cut(e, "="); !keys[k] {
			out = append(out, e)
		}
	}
	return out
}
