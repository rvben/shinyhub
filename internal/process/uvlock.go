package process

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// ErrStaleLock marks a uv.lock whose recorded project requirements no longer
// match pyproject.toml.
var ErrStaleLock = errors.New("uv.lock is out of date with pyproject.toml")

// LockSyncFlags returns the `uv sync` flags for the project in dir.
//
// A project that ships a uv.lock is installed with --frozen: exactly the
// versions, hashes and download URLs the lock records, and the lock is never
// rewritten. A plain `uv sync` re-resolves whenever the lock's package sources
// differ from the indexes uv is configured with on this host (a trailing slash
// is enough), rewrites the lock and installs the new resolution without
// reporting it, so the deployed app would stop running the lockfile that was
// tested. --frozen also matches the launch, which is `uv run --frozen` on every
// runtime.
//
// --frozen does not compare the lock with pyproject.toml, and `uv sync --locked`
// cannot do it here either: it treats an index mismatch as an outdated lock.
// Deploys cover that gap with CheckLockCurrent, which refuses a stale lock
// before any build runs. A stale lock that still reaches a build (an
// activation of a deployment accepted before that check existed, or a local
// run) gets a plain `uv sync`, which re-resolves and installs what
// pyproject.toml declares instead of the lock's outdated set.
//
// Without a uv.lock there is nothing to preserve, and a plain `uv sync` creates
// one (nil flags).
func LockSyncFlags(dir string) []string {
	if _, err := os.Stat(filepath.Join(dir, "uv.lock")); err != nil {
		return nil
	}
	if CheckLockCurrent(dir) != nil {
		return nil
	}
	return []string{"--frozen"}
}

// CheckLockCurrent reports ErrStaleLock when dir's uv.lock records a different
// set of project requirements than pyproject.toml declares.
//
// uv stores the requirements it locked against in the root package's
// [package.metadata]: requires-dist for the project's dependencies and extras,
// requires-dev for its dependency groups. The comparison therefore needs no
// index and no network. Each requirement is compared by package name, by the
// extras it requests, and by where it is declared (required, a named extra, or
// a named group), so a package moved from an extra into the required
// dependencies counts as a change: the old lock would install it only with
// that extra. Specifiers and markers are not compared, because uv rewrites
// them into a canonical form (`python_version` becomes `python_full_version`,
// whitespace is dropped) and comparing them would reject current locks. For
// the same reason [tool.uv.sources] is not compared: uv resolves paths and Git
// references before recording them.
//
// The check is deliberately one-sided about certainty: whenever the files do
// not have the shape it understands (no uv.lock or [project] table, dynamic
// dependencies, a root package it cannot find, a requirement or marker it
// cannot parse, an unreadable or malformed file) it returns nil and leaves the
// verdict to uv. It rejects only a lock it can show is stale. Dependency groups
// it cannot resolve are left out of the comparison rather than failing it.
func CheckLockCurrent(dir string) error {
	var project struct {
		Project *struct {
			Name                 string              `toml:"name"`
			Dynamic              []string            `toml:"dynamic"`
			Dependencies         []string            `toml:"dependencies"`
			OptionalDependencies map[string][]string `toml:"optional-dependencies"`
		} `toml:"project"`
		DependencyGroups map[string][]any `toml:"dependency-groups"`
		Tool             struct {
			UV struct {
				DevDependencies []string `toml:"dev-dependencies"`
			} `toml:"uv"`
		} `toml:"tool"`
	}
	if _, err := toml.DecodeFile(filepath.Join(dir, "pyproject.toml"), &project); err != nil {
		return nil
	}
	p := project.Project
	if p == nil || p.Name == "" ||
		slices.Contains(p.Dynamic, "dependencies") || slices.Contains(p.Dynamic, "optional-dependencies") {
		return nil
	}
	declared := map[string]bool{}
	add := func(reqs []string, scope string) bool {
		for _, req := range reqs {
			name, ok := requirementName(req)
			if !ok {
				return false
			}
			declared[requirementLabel(name, scope)] = true
		}
		return true
	}
	if !add(p.Dependencies, "") {
		return nil
	}
	for extra, reqs := range p.OptionalDependencies {
		if !add(reqs, "extra "+normalizePackageName(extra)) {
			return nil
		}
	}
	declaredGroups, groupsKnown := resolveDependencyGroups(project.DependencyGroups, project.Tool.UV.DevDependencies)

	var lock struct {
		Package []uvLockPackage `toml:"package"`
	}
	if _, err := toml.DecodeFile(filepath.Join(dir, "uv.lock"), &lock); err != nil {
		return nil
	}
	root := normalizePackageName(p.Name)
	idx := slices.IndexFunc(lock.Package, func(pkg uvLockPackage) bool {
		return normalizePackageName(pkg.Name) == root && (pkg.Source["virtual"] == "." || pkg.Source["editable"] == ".")
	})
	if idx < 0 {
		return nil
	}
	pkg := lock.Package[idx]
	locked := map[string]bool{}
	lockedGroups := map[string]bool{}
	if pkg.Metadata == nil {
		// uv omits [package.metadata] for a project without requirements. A root
		// that still lists dependencies is a shape this check does not know.
		if len(pkg.Dependencies) > 0 || len(pkg.OptionalDependencies) > 0 || len(pkg.DevDependencies) > 0 {
			return nil
		}
	} else {
		for _, r := range pkg.Metadata.RequiresDist {
			scope := ""
			if strings.Contains(r.Marker, "extra") {
				m := extraMarker.FindAllStringSubmatch(r.Marker, -1)
				if len(m) != 1 {
					return nil
				}
				scope = "extra " + normalizePackageName(m[0][1])
			}
			locked[requirementLabel(packageKey(r.Name, r.Extras), scope)] = true
		}
		// A lock written before uv recorded requires-dev lists groups only as
		// resolved dev-dependencies, which cannot be compared.
		if pkg.Metadata.RequiresDev == nil && len(pkg.DevDependencies) > 0 {
			groupsKnown = false
		}
		for group, reqs := range pkg.Metadata.RequiresDev {
			for _, r := range reqs {
				lockedGroups[requirementLabel(packageKey(r.Name, r.Extras), "group "+normalizePackageName(group))] = true
			}
		}
	}
	if groupsKnown {
		maps.Copy(declared, declaredGroups)
		maps.Copy(locked, lockedGroups)
	}

	var missing, extra []string
	for label := range declared {
		if !locked[label] {
			missing = append(missing, label)
		}
	}
	for label := range locked {
		if !declared[label] {
			extra = append(extra, label)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return nil
	}
	slices.Sort(missing)
	slices.Sort(extra)
	var detail []string
	if len(missing) > 0 {
		detail = append(detail, "pyproject.toml declares "+strings.Join(missing, ", ")+", which uv.lock does not record")
	}
	if len(extra) > 0 {
		detail = append(detail, "uv.lock records "+strings.Join(extra, ", ")+", which pyproject.toml no longer declares")
	}
	return fmt.Errorf("%w: %s. Run `uv lock` and deploy again", ErrStaleLock, strings.Join(detail, "; "))
}

// resolveDependencyGroups returns the labels of every requirement in the
// project's dependency groups, the way uv records them in requires-dev:
// `{include-group = ...}` entries expanded from [dependency-groups], then
// legacy `[tool.uv] dev-dependencies` added to the "dev" group (an include of
// "dev" does not pick those up). ok is false for a group entry it does not
// understand or an include that is missing or cyclic.
func resolveDependencyGroups(groups map[string][]any, legacyDev []string) (labels map[string]bool, ok bool) {
	byName := map[string][]any{}
	for name, entries := range groups {
		key := normalizePackageName(name)
		byName[key] = append(byName[key], entries...)
	}
	var names func(group string, visiting map[string]bool) ([]string, bool)
	names = func(group string, visiting map[string]bool) ([]string, bool) {
		entries, found := byName[group]
		if !found || visiting[group] {
			return nil, false
		}
		visiting[group] = true
		defer delete(visiting, group)
		var out []string
		for _, entry := range entries {
			switch e := entry.(type) {
			case string:
				name, ok := requirementName(e)
				if !ok {
					return nil, false
				}
				out = append(out, name)
			case map[string]any:
				include, isString := e["include-group"].(string)
				if len(e) != 1 || !isString {
					return nil, false
				}
				included, ok := names(normalizePackageName(include), visiting)
				if !ok {
					return nil, false
				}
				out = append(out, included...)
			default:
				return nil, false
			}
		}
		return out, true
	}
	labels = map[string]bool{}
	for group := range byName {
		reqs, ok := names(group, map[string]bool{})
		if !ok {
			return nil, false
		}
		for _, name := range reqs {
			labels[requirementLabel(name, "group "+group)] = true
		}
	}
	for _, req := range legacyDev {
		name, ok := requirementName(req)
		if !ok {
			return nil, false
		}
		labels[requirementLabel(name, "group dev")] = true
	}
	return labels, true
}

// requirementLabel names a requirement together with where it is declared, as
// the stale-lock error reports it: "six", "httpx[http2]", "six (extra fast)",
// "six (group dev)".
func requirementLabel(name, scope string) string {
	if scope == "" {
		return name
	}
	return name + " (" + scope + ")"
}

// extraMarker matches the `extra == '...'` clause uv writes into the marker of
// a requires-dist entry that belongs to an extra.
var extraMarker = regexp.MustCompile(`\bextra\s*==\s*['"]([^'"]*)['"]`)

// uvLockPackage is the part of a uv.lock [[package]] entry CheckLockCurrent
// reads. The project itself is the entry whose source is the project
// directory ("." as a virtual or editable source).
type uvLockPackage struct {
	Name                 string           `toml:"name"`
	Source               map[string]any   `toml:"source"`
	Dependencies         []map[string]any `toml:"dependencies"`
	OptionalDependencies map[string]any   `toml:"optional-dependencies"`
	DevDependencies      map[string]any   `toml:"dev-dependencies"`
	Metadata             *struct {
		RequiresDist []struct {
			Name   string   `toml:"name"`
			Extras []string `toml:"extras"`
			Marker string   `toml:"marker"`
		} `toml:"requires-dist"`
		RequiresDev map[string][]struct {
			Name   string   `toml:"name"`
			Extras []string `toml:"extras"`
		} `toml:"requires-dev"`
	} `toml:"metadata"`
}

// pep508Name is the distribution name at the start of a PEP 508 requirement,
// followed by its requested extras, if any.
var pep508Name = regexp.MustCompile(`^\s*([A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?)\s*(?:\[([^\]]*)\])?`)

// requirementName returns the normalized distribution name of a PEP 508
// requirement string together with its requested extras, as packageKey writes
// them.
func requirementName(req string) (string, bool) {
	m := pep508Name.FindStringSubmatch(req)
	if m == nil {
		return "", false
	}
	var extras []string
	for _, e := range strings.Split(m[2], ",") {
		if e = strings.TrimSpace(e); e != "" {
			extras = append(extras, e)
		}
	}
	return packageKey(m[1], extras), true
}

// packageKey names a requirement by its normalized package name and its sorted,
// normalized extras ("httpx[brotli,http2]"), so requesting a different set of
// extras counts as a different requirement: the lock would not include what
// the new extras pull in.
func packageKey(name string, extras []string) string {
	if len(extras) == 0 {
		return normalizePackageName(name)
	}
	norm := make([]string, len(extras))
	for i, e := range extras {
		norm[i] = normalizePackageName(e)
	}
	slices.Sort(norm)
	return normalizePackageName(name) + "[" + strings.Join(slices.Compact(norm), ",") + "]"
}

var nameSeparators = regexp.MustCompile(`[-_.]+`)

// normalizePackageName applies the PEP 503 name normalization uv uses in
// uv.lock.
func normalizePackageName(name string) string {
	return nameSeparators.ReplaceAllString(strings.ToLower(name), "-")
}
