// Package sandbox applies best-effort, unprivileged process isolation to native
// app processes. On Linux it uses Landlock (kernel-enforced filesystem access
// control, which also sets NO_NEW_PRIVS); on every other platform enforcement
// is a no-op so the same binaries build and run everywhere.
//
// The model is "restrict in place": the app keeps the shinyhub service user's
// UID but its filesystem reach is narrowed. It is a blast-radius boundary, not a
// defense against determined hostile code (no PID/user/network isolation in the
// standard tier); operators who need that run the Docker runtime.
package sandbox

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Level is the operator-facing isolation dial. Ordered from least to most
// restrictive. Only off and standard are implemented; strict (tighter reads +
// network restriction) is reserved for a follow-up and is rejected until then.
type Level string

const (
	// LevelOff applies no isolation (the historical native behavior).
	LevelOff Level = "off"
	// LevelStandard confines writes to the app's own directories while leaving
	// reads open, a robust blast-radius boundary that does not break interpreter
	// or library loading.
	LevelStandard Level = "standard"
)

// ParseLevel validates and normalizes an operator-supplied isolation value.
func ParseLevel(s string) (Level, error) {
	switch Level(s) {
	case LevelOff, LevelStandard:
		return Level(s), nil
	case "":
		return LevelOff, nil
	case "strict":
		return "", fmt.Errorf("isolation level %q is not yet implemented; use \"off\" or \"standard\"", s)
	default:
		return "", fmt.Errorf("invalid isolation level %q; valid values are \"off\", \"standard\"", s)
	}
}

// Enabled reports whether the level requests any enforcement.
func (l Level) Enabled() bool { return l != "" && l != LevelOff }

// Spec is the concrete, resolved isolation policy handed to the re-exec shim. It
// is deliberately explicit (absolute path lists) rather than recomputed in the
// child, so what the parent intends is exactly what the child enforces.
type Spec struct {
	Level Level `json:"level"`
	// WritePaths are directory subtrees the app may read AND write. Everything
	// else is read-only (standard) under the RO base below.
	WritePaths []string `json:"write_paths"`
	// ReadPaths are additional read-only directory subtrees. Standard uses the
	// filesystem root so interpreter/library loads never break.
	ReadPaths []string `json:"read_paths"`
}

// EnvVar carries the JSON-encoded Spec from the control plane to the re-exec
// shim. The shim removes it from the environment before exec'ing the app, so an
// app never sees its own sandbox policy.
const EnvVar = "SHINYHUB_SANDBOX_SPEC"

// systemWritePaths are the scratch and device areas essentially every process
// needs to write to: /dev (for /dev/null, /dev/urandom, /dev/tty, ...) and /tmp.
// Allowing them does not weaken the blast-radius goal - dangerous device nodes
// (/dev/sd*, /dev/mem) remain protected by ordinary DAC permissions, and cross-
// app /tmp visibility is a concern the (future) strict tier addresses with a
// private tmp. Denying these instead breaks real apps (proven: a denied
// /dev/null write fails ordinary shell redirects).
var systemWritePaths = []string{"/dev", "/tmp"}

// ComputeSpec resolves the isolation policy for one native replica. appDir is
// the deployment working directory and dataDir the persistent per-app data dir
// (may be empty). Only non-empty paths are included.
//
// Standard: read-only everywhere ("/") so interpreter and library loads never
// break, writable only within the app's own directories plus the shared system
// scratch/device areas. This narrows the blast radius - an app cannot tamper
// with other apps' bundles, the control-plane database, or system files - while
// staying robust enough not to break ordinary programs.
func ComputeSpec(level Level, appDir, dataDir string) Spec {
	s := Spec{Level: level}
	if !level.Enabled() {
		return s
	}
	for _, p := range []string{appDir, dataDir} {
		if p != "" {
			s.WritePaths = append(s.WritePaths, p)
		}
	}
	if level == LevelStandard {
		s.WritePaths = append(s.WritePaths, systemWritePaths...)
		s.ReadPaths = []string{"/"}
	}
	sort.Strings(s.WritePaths)
	return s
}

// Encode serializes a Spec for transport in EnvVar.
func (s Spec) Encode() (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("encode sandbox spec: %w", err)
	}
	return string(b), nil
}

// DecodeSpec parses a Spec produced by Encode.
func DecodeSpec(s string) (Spec, error) {
	var spec Spec
	if err := json.Unmarshal([]byte(s), &spec); err != nil {
		return Spec{}, fmt.Errorf("decode sandbox spec: %w", err)
	}
	return spec, nil
}
