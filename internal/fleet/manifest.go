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
	HibernateTimeoutMinutes *int `toml:"hibernate_timeout_minutes"`
	Replicas                *int `toml:"replicas"`
	MaxSessionsPerReplica   *int `toml:"max_sessions_per_replica"`
}

// AppEntry is one [[app]] block after validation. Visibility defaults to
// "private" when omitted.
type AppEntry struct {
	Slug       string `toml:"slug"`
	Source     string `toml:"source"`
	Visibility string `toml:"visibility"`
	Config     Config `toml:"config"`
}

// Manifest is a validated shinyhub-fleet.toml.
type Manifest struct {
	FleetID string     `toml:"fleet_id"`
	Apps    []AppEntry `toml:"app"`
}

var fleetIDRe = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

var validVisibility = map[string]bool{"private": true, "shared": true, "public": true}

// knownKeys is the set of accepted manifest keys, used for "did you mean"
// suggestions on unknown-key rejection.
var knownKeys = []string{
	"fleet_id", "app", "slug", "source", "visibility", "config",
	"hibernate_timeout_minutes", "replicas", "max_sessions_per_replica",
}

// ParseManifest strictly decodes a fleet manifest and runs all cheap, local,
// deterministic validations, returning EVERY problem found (compiler-style;
// never first-only). A non-empty []Problem means the manifest must not be
// used. file is the path shown in problem locations.
//
// A later task wires source-form validation (ParseSource) into this same
// aggregated slice so a bad source and a typo'd key are reported together;
// ParseManifest itself performs no filesystem or network I/O.
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
	} else if !fleetIDRe.MatchString(m.FleetID) {
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
		probs = append(probs, validateConfig(file, who, a.Config)...)
		if a.Source == "" {
			probs = append(probs, Problem{File: file, Msg: who + ": source is required"})
		}
		// Note: source form + local existence is validated by the caller via
		// ParseSource (a later task), which appends to the same aggregated slice
		// so a bad source and a typo'd key are reported together.
	}

	if len(probs) > 0 {
		return nil, probs
	}
	return &m, nil
}

func validateConfig(file, who string, c Config) []Problem {
	var probs []Problem
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
