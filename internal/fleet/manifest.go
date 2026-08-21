// Package fleet implements the pure, I/O-free core of the fleet reconcile
// layer: manifest parsing, source-form classification, and the desired-vs-
// observed diff. The CLI layer supplies digests and network results; this
// package never performs I/O so it is exhaustively unit-testable and shared
// by `fleet plan`, `fleet apply --dry-run`, and `fleet status`.
package fleet

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/rvben/shinyhub/internal/appmetaspec"
	"github.com/rvben/shinyhub/internal/autoscalespec"
	"github.com/rvben/shinyhub/internal/iconspec"
	slugpkg "github.com/rvben/shinyhub/internal/slug"
)

// Problem is a single, user-facing validation failure. File/Line/Col are
// best-effort; Line==0 means "no precise location" (rendered without :line).
type Problem struct {
	File string
	Line int
	Col  int
	Msg  string
}

func (p Problem) Error() string {
	loc := p.File
	if p.Line > 0 {
		loc = fmt.Sprintf("%s:%d", p.File, p.Line)
		if p.Col > 0 {
			loc = fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col)
		}
	}
	if loc == "" {
		return p.Msg
	}
	return loc + "  " + p.Msg
}

// Config mirrors the reconcilable subset of shinyhub.toml [app]. Pointers
// distinguish "declared" (drift-protected) from "absent" (server/bundle wins).
type Config struct {
	// Name and Description are the app's display metadata. Declaring either
	// makes the fleet manifest its owner: a rename in the dashboard then shows
	// up as drift on the next plan and is reverted by apply. Leaving the key out
	// keeps the dashboard authoritative.
	Name        *string `toml:"name"`
	Description *string `toml:"description"`
	// Project groups this app on the dashboard. Declaring it makes the fleet
	// manifest the owner, exactly as Name and Description above: a project
	// changed in the dashboard shows as drift on the next plan and is reverted
	// by apply. A declared "" ungroups the app.
	//
	// The TOML key is `project` and the drift key is `project`, but the API
	// field is `project_slug`, so the patch layer maps it explicitly rather
	// than using the body[key] shortcut. See internal/cli/fleet_apply_exec.go.
	Project                 *string          `toml:"project"`
	HibernateTimeoutMinutes *int             `toml:"hibernate_timeout_minutes"`
	Replicas                *int             `toml:"replicas"`
	MaxSessionsPerReplica   *int             `toml:"max_sessions_per_replica"`
	Autoscale               *AutoscaleConfig `toml:"autoscale"`

	// The fields below are populated from the source bundle's shinyhub.toml by
	// fleet preflight. They are deliberately not TOML-decodable here: the fleet
	// manifest keeps its existing public schema, while bundle-declared durable
	// app settings become part of the observed-vs-desired reconcile set.
	Icon                         *string  `toml:"-"`
	RenderSeconds                *float64 `toml:"-"`
	IdentityHeaders              *bool    `toml:"-"`
	MinWarmReplicas              *int     `toml:"-"`
	MemoryLimitMB                *int     `toml:"-"`
	CPUQuotaPercent              *int     `toml:"-"`
	WorkerIsolation              *string  `toml:"-"`
	WorkerGroupedSize            *int     `toml:"-"`
	WorkerMaxWorkers             *int     `toml:"-"`
	WorkerWarmSpares             *int     `toml:"-"`
	WorkerMaxSessionLifetimeSecs *int     `toml:"-"`
	HibernateResetToDefault      bool     `toml:"-"`
}

// AutoscaleConfig mirrors the [app.config] autoscale inline table. It matches
// the bundle manifest's [app] autoscale block (internal/deploy) and the PATCH
// /api/apps autoscale object. Enabled is a pointer so a declared block must
// state it explicitly; nil (block absent) means the policy is not fleet-managed.
type AutoscaleConfig struct {
	Enabled     *bool   `toml:"enabled"`
	MinReplicas int     `toml:"min_replicas"`
	MaxReplicas int     `toml:"max_replicas"`
	Target      float64 `toml:"target"`
}

// AppEntry is one [[app]] block after validation. Visibility defaults to
// "private" when omitted.
type AppEntry struct {
	Slug       string `toml:"slug"`
	Source     string `toml:"source"`
	Visibility string `toml:"visibility"`
	Config     Config `toml:"config"`
	// Bundle is the durable [app] state declared by source/shinyhub.toml. It is
	// filled after source resolution and never decoded from fleet.toml.
	Bundle Config `toml:"-"`
}

// EffectiveConfig overlays the fleet manifest's [app.config] on the source
// bundle's [app] declarations. The outer fleet manifest wins for fields both
// layers can declare; bundle-only durable fields remain managed by the bundle.
func EffectiveConfig(app AppEntry) Config {
	c := app.Bundle
	if app.Config.Name != nil {
		c.Name = app.Config.Name
	}
	if app.Config.Description != nil {
		c.Description = app.Config.Description
	}
	if app.Config.Project != nil {
		c.Project = app.Config.Project
	}
	if app.Config.HibernateTimeoutMinutes != nil {
		c.HibernateTimeoutMinutes = app.Config.HibernateTimeoutMinutes
		c.HibernateResetToDefault = *app.Config.HibernateTimeoutMinutes == -1
	}
	if app.Config.Replicas != nil {
		c.Replicas = app.Config.Replicas
	}
	if app.Config.MaxSessionsPerReplica != nil {
		c.MaxSessionsPerReplica = app.Config.MaxSessionsPerReplica
	}
	if app.Config.Autoscale != nil {
		c.Autoscale = app.Config.Autoscale
	}
	return c
}

// ProjectEntry is one [[project]] block after validation. It carries display
// metadata only: a project has no bundle, no source and no visibility. Pointers
// mean the same thing they do in Config: nil is "not declared, leave the
// server's value alone", and a non-nil "" is an explicit clear.
type ProjectEntry struct {
	Slug        string  `toml:"slug"`
	Name        *string `toml:"name"`
	Description *string `toml:"description"`
	Icon        *string `toml:"icon"`
}

// BundleFileEntry declares one canonical local file that is composed into the
// bundles of the explicitly named consumers. Filesystem resolution is owned by
// the bundle/CLI layers; this package validates only the manifest contract.
type BundleFileEntry struct {
	From      string   `toml:"from"`
	To        string   `toml:"to"`
	Consumers []string `toml:"consumers"`
}

// Manifest is a validated fleet.toml.
type Manifest struct {
	FleetID     string            `toml:"fleet_id"`
	Projects    []ProjectEntry    `toml:"project"`
	BundleFiles []BundleFileEntry `toml:"bundle_file"`
	Apps        []AppEntry        `toml:"app"`
}

var fleetIDRe = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// ValidFleetID reports whether id is a syntactically valid fleet ownership
// scope: [a-z0-9-], 1-64 chars. Shared by manifest validation and
// `fleet init` so the two cannot diverge.
func ValidFleetID(id string) bool { return fleetIDRe.MatchString(id) }

var validVisibility = map[string]bool{"private": true, "shared": true, "public": true}

// knownKeys is the set of accepted manifest keys, used for "did you mean"
// suggestions on unknown-key rejection.
var knownKeys = []string{
	"fleet_id", "app", "project", "bundle_file", "from", "to", "consumers",
	"slug", "source", "visibility", "config",
	"name", "description", "icon",
	"hibernate_timeout_minutes", "replicas", "max_sessions_per_replica",
	"autoscale", "enabled", "min_replicas", "max_replicas", "target",
}

// ParseManifest strictly decodes a fleet manifest and runs all cheap, local,
// deterministic validations, returning EVERY problem found (compiler-style;
// never first-only). A non-empty []Problem means the manifest must not be
// used. file is the path shown in problem locations.
//
// Git source URLs are validated for format; local path existence is a
// filesystem check deferred to the pre-flight step. ParseManifest itself
// performs no filesystem or network I/O.
func ParseManifest(data []byte, file string) (*Manifest, []Problem) {
	var probs []Problem
	var m Manifest

	meta, err := toml.Decode(string(data), &m)
	if err != nil {
		// BurntSushi errors carry line context in the message; surface as-is
		// with the file prefix. This is fatal on its own (no struct to validate).
		return nil, []Problem{{File: file, Msg: fmt.Sprintf("TOML parse error: %v", err)}}
	}

	if und := meta.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		sort.Strings(keys)
		emitted := map[string]bool{}
		for _, k := range keys {
			leaf := k
			if i := strings.LastIndexByte(k, '.'); i >= 0 {
				leaf = k[i+1:]
			}
			msg := fmt.Sprintf("unknown key %q", k)
			if s := suggest(leaf, knownKeys); s != "" {
				msg += fmt.Sprintf(`; did you mean %q?`, s)
			}
			if emitted[msg] {
				continue
			}
			emitted[msg] = true
			probs = append(probs, Problem{File: file, Msg: msg})
		}
	}

	if m.FleetID == "" {
		probs = append(probs, Problem{File: file, Msg: "fleet_id is required"})
	} else if !ValidFleetID(m.FleetID) {
		probs = append(probs, Problem{File: file, Msg: fmt.Sprintf(
			"fleet_id %q invalid: must match [a-z0-9-], 1-64 chars", m.FleetID)})
	}

	seen := map[string]bool{}
	for i := range m.Apps {
		a := &m.Apps[i]
		who := fmt.Sprintf("app[%d]", i)
		if a.Slug == "" {
			probs = append(probs, Problem{File: file, Msg: who + " is missing slug"})
		} else {
			who = fmt.Sprintf("app %q", a.Slug)
			if seen[a.Slug] {
				probs = append(probs, Problem{File: file, Msg: fmt.Sprintf("duplicate slug %q", a.Slug)})
			}
			seen[a.Slug] = true
		}
		if a.Visibility == "" {
			a.Visibility = "private"
		} else if !validVisibility[a.Visibility] {
			probs = append(probs, Problem{File: file, Msg: fmt.Sprintf(
				"%s: invalid visibility %q (allowed: private, shared, public)", who, a.Visibility)})
		}
		probs = append(probs, validateConfig(file, who, &a.Config)...)
		if a.Source == "" {
			probs = append(probs, Problem{File: file, Msg: who + ": source is required"})
		} else if strings.HasPrefix(a.Source, "git+") {
			// Validate git URL format without I/O; local path existence is
			// verified in the pre-flight step where I/O is expected.
			if _, sp := ParseSource(a.Source, ""); sp != nil {
				probs = append(probs, Problem{File: file, Msg: who + ": " + sp.Msg})
			}
		}
	}
	probs = append(probs, validateBundleFiles(file, &m, seen)...)

	probs = append(probs, validateProjects(file, &m)...)

	if len(probs) > 0 {
		// Return the partially-decoded manifest alongside the problems so
		// callers can inspect what was parsed (e.g., to run additional local
		// checks) without re-parsing. A nil manifest is only returned for
		// hard TOML parse failures above where no struct is available.
		return &m, probs
	}
	return &m, nil
}

var bundleControlFiles = map[string]bool{
	"shinyhub.toml":   true,
	".shinyhubignore": true,
	".gitignore":      true,
}

// validateBundleFiles checks the I/O-free portion of the shared-input
// contract. Existence, symlinks, filtering and collisions with source-tree
// entries are checked later, once the CLI has resolved local sources.
func validateBundleFiles(file string, m *Manifest, appSlugs map[string]bool) []Problem {
	var probs []Problem
	destinations := make(map[string][]string)
	for i, bf := range m.BundleFiles {
		who := fmt.Sprintf("bundle_file[%d]", i)
		if bf.From == "" {
			probs = append(probs, Problem{File: file, Msg: who + ": from is required"})
		} else if err := validateBundleRelativePath(bf.From); err != nil {
			probs = append(probs, Problem{File: file, Msg: fmt.Sprintf("%s: from %q: %v", who, bf.From, err)})
		}

		toOK := true
		if bf.To == "" {
			toOK = false
			probs = append(probs, Problem{File: file, Msg: who + ": to is required"})
		} else if err := validateBundleRelativePath(bf.To); err != nil {
			toOK = false
			probs = append(probs, Problem{File: file, Msg: fmt.Sprintf("%s: to %q: %v", who, bf.To, err)})
		} else if bundleControlFiles[bf.To] {
			toOK = false
			probs = append(probs, Problem{File: file, Msg: fmt.Sprintf(
				"%s: to %q is a bundle control file and cannot be composed in V1", who, bf.To)})
		}

		if len(bf.Consumers) == 0 {
			probs = append(probs, Problem{File: file, Msg: who + ": consumers must not be empty"})
		}
		seenConsumers := make(map[string]bool, len(bf.Consumers))
		for _, consumer := range bf.Consumers {
			if seenConsumers[consumer] {
				probs = append(probs, Problem{File: file, Msg: fmt.Sprintf(
					"%s: duplicate consumer %q", who, consumer)})
				continue
			}
			seenConsumers[consumer] = true
			if !appSlugs[consumer] {
				probs = append(probs, Problem{File: file, Msg: fmt.Sprintf(
					"%s: unknown consumer %q; declare it in an [[app]] block", who, consumer)})
				continue
			}
			if !toOK {
				continue
			}
			for _, previous := range destinations[consumer] {
				if bundleDestinationConflict(previous, bf.To) {
					probs = append(probs, Problem{File: file, Msg: fmt.Sprintf(
						"%s: destination conflict for app %q: %q conflicts with %q",
						who, consumer, bf.To, previous)})
				}
			}
			destinations[consumer] = append(destinations[consumer], bf.To)
		}
	}
	return probs
}

func validateBundleRelativePath(value string) error {
	if strings.Contains(value, `\`) {
		return fmt.Errorf("use forward slashes")
	}
	if strings.HasPrefix(value, "/") || looksLikeWindowsAbsolutePath(value) {
		return fmt.Errorf("must be relative")
	}
	if strings.HasSuffix(value, "/") {
		return fmt.Errorf("must name a file, not end in a slash")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("must be normalized and contain no empty, . or .. segments")
		}
	}
	return nil
}

func looksLikeWindowsAbsolutePath(value string) bool {
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') ||
		(value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && value[2] == '/'
}

func bundleDestinationConflict(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// validateConfig validates one [app.config] block and normalizes it in place:
// c is a pointer so the trimmed name/description are what the diff later
// compares against the server, rather than a padded copy that reports drift on
// every plan.
func validateConfig(file, who string, c *Config) []Problem {
	var probs []Problem
	if c.Name != nil {
		v, err := appmetaspec.NormalizeName(*c.Name)
		if err != nil {
			probs = append(probs, Problem{File: file, Msg: fmt.Sprintf("%s: %v", who, err)})
		} else {
			c.Name = &v
		}
	}
	if c.Description != nil {
		v, err := appmetaspec.NormalizeDescription(*c.Description)
		if err != nil {
			probs = append(probs, Problem{File: file, Msg: fmt.Sprintf("%s: %v", who, err)})
		} else {
			c.Description = &v
		}
	}
	// Trimmed in place for the same reason Name is: an untrimmed value would
	// report drift against the server's trimmed one on every plan.
	if c.Project != nil {
		v := strings.TrimSpace(*c.Project)
		if v != "" && !slugpkg.Valid(v) {
			probs = append(probs, Problem{File: file, Msg: fmt.Sprintf(
				"%s: project must be %s", who, slugpkg.HumanRule)})
		} else {
			c.Project = &v
		}
	}
	// hibernate accepts -1 (reset to default), 0 (disable), or a positive
	// timeout, matching the bundle manifest and apps set.
	if c.HibernateTimeoutMinutes != nil {
		v := *c.HibernateTimeoutMinutes
		if v < -1 {
			probs = append(probs, Problem{File: file, Msg: fmt.Sprintf(
				"%s: hibernate_timeout_minutes must be -1 (reset), 0 (disable), or positive, got %d", who, v)})
		}
	}
	if c.Replicas != nil && *c.Replicas < 1 {
		probs = append(probs, Problem{File: file, Msg: fmt.Sprintf(
			"%s: replicas must be >= 1, got %d", who, *c.Replicas)})
	}
	if c.MaxSessionsPerReplica != nil && *c.MaxSessionsPerReplica < 1 {
		probs = append(probs, Problem{File: file, Msg: fmt.Sprintf(
			"%s: max_sessions_per_replica must be >= 1, got %d", who, *c.MaxSessionsPerReplica)})
	}
	if c.Autoscale != nil {
		if err := autoscalespec.Validate(autoscalespec.Params{
			Enabled:     c.Autoscale.Enabled,
			MinReplicas: c.Autoscale.MinReplicas,
			MaxReplicas: c.Autoscale.MaxReplicas,
			Target:      c.Autoscale.Target,
		}); err != nil {
			probs = append(probs, Problem{File: file, Msg: fmt.Sprintf("%s: %v", who, err)})
		}
	}
	return probs
}

// validateProjects validates the [[project]] blocks and normalizes them in
// place, and rejects a project no app in this manifest references.
//
// The unreferenced check is not tidiness. Observed projects come from
// GET /api/projects, which is access-scoped, and a project with no apps is
// invisible to every non-privileged caller by construction. An unreferenced
// project would therefore read as absent on every run, so `fleet plan` would
// report a permanent "1 to create" and --detailed-exitcode would report drift
// forever in CI. Requiring a referencing app means the identity that can
// deploy the app can also see the project, so the diff converges.
//
// It runs after the app loop because it reads the apps' normalized projects.
func validateProjects(file string, m *Manifest) []Problem {
	var probs []Problem

	referenced := map[string]bool{}
	for _, a := range m.Apps {
		if a.Config.Project != nil && *a.Config.Project != "" {
			referenced[*a.Config.Project] = true
		}
	}

	seen := map[string]bool{}
	for i := range m.Projects {
		p := &m.Projects[i]
		who := fmt.Sprintf("project[%d]", i)
		p.Slug = strings.TrimSpace(p.Slug)
		switch {
		case p.Slug == "":
			probs = append(probs, Problem{File: file, Msg: who + " is missing slug"})
		case !slugpkg.Valid(p.Slug):
			probs = append(probs, Problem{File: file, Msg: fmt.Sprintf(
				"%s: slug %q invalid: must be %s", who, p.Slug, slugpkg.HumanRule)})
		default:
			who = fmt.Sprintf("project %q", p.Slug)
			if seen[p.Slug] {
				probs = append(probs, Problem{File: file, Msg: fmt.Sprintf("duplicate project slug %q", p.Slug)})
			}
			seen[p.Slug] = true
			if !referenced[p.Slug] {
				probs = append(probs, Problem{File: file, Msg: fmt.Sprintf(
					"%s: no app in this manifest sets project = %q; "+
						"reference it from an [app.config] block or remove it", who, p.Slug)})
			}
		}
		if p.Name != nil {
			v, err := appmetaspec.NormalizeProjectName(*p.Name)
			if err != nil {
				probs = append(probs, Problem{File: file, Msg: fmt.Sprintf("%s: name: %v", who, err)})
			} else {
				p.Name = &v
			}
		}
		if p.Description != nil {
			v, err := appmetaspec.NormalizeDescription(*p.Description)
			if err != nil {
				probs = append(probs, Problem{File: file, Msg: fmt.Sprintf("%s: description: %v", who, err)})
			} else {
				p.Description = &v
			}
		}
		if p.Icon != nil {
			v := strings.TrimSpace(*p.Icon)
			// "" is a declared clear, so it skips validation: iconspec.Validate
			// rejects the empty string.
			if v != "" {
				if err := iconspec.Validate(v); err != nil {
					probs = append(probs, Problem{File: file, Msg: fmt.Sprintf("%s: icon: %v", who, err)})
				}
			}
			p.Icon = &v
		}
	}
	return probs
}

// suggest returns the closest known key within Levenshtein distance 2, or "".
func suggest(in string, known []string) string {
	best, bestD := "", 3
	for _, k := range known {
		if in == k {
			return ""
		}
		if d := levenshtein(in, k); d < bestD {
			best, bestD = k, d
		}
	}
	return best
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
