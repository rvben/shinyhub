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

// Manifest is a validated fleet.toml.
type Manifest struct {
	FleetID  string         `toml:"fleet_id"`
	Projects []ProjectEntry `toml:"project"`
	Apps     []AppEntry     `toml:"app"`
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
	"fleet_id", "app", "project", "slug", "source", "visibility", "config",
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
			msg := fmt.Sprintf("unknown key %q", leaf)
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
	// hibernate accepts the existing -1 "reset to default" sentinel (matches
	// internal/deploy/hooks.go), otherwise must be >= 1.
	if c.HibernateTimeoutMinutes != nil {
		v := *c.HibernateTimeoutMinutes
		if v != -1 && v < 1 {
			probs = append(probs, Problem{File: file, Msg: fmt.Sprintf(
				"%s: hibernate_timeout_minutes must be >= 1 (or -1 to reset to default), got %d", who, v)})
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
