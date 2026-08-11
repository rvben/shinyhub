// Package appmetaspec holds the pure, I/O-free validation for an app's display
// metadata: the friendly name and the one-line description. It is the single
// source of truth shared by the PATCH/POST /api/apps handlers, the bundle
// manifest ([app] name/description in internal/deploy) and the fleet manifest
// ([[app]].config.name/description in internal/fleet), so the four cannot
// drift. It mirrors the internal/autoscalespec and internal/schedulespec
// pattern.
package appmetaspec

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxNameRunes bounds apps.name. MaxDescriptionRunes bounds apps.description.
// Both count runes, not bytes: the limits are advertised in characters and are
// what the dashboard inputs enforce via maxlength, so an accented or emoji
// value under the limit must not be rejected because its UTF-8 encoding is
// longer.
const (
	MaxNameRunes        = 128
	MaxDescriptionRunes = 280
)

// NormalizeName trims s and validates it as an app display name. Trimming and
// validation are one step so no caller can do half of it: a name that is only
// whitespace must be rejected, not silently stored as "".
//
// The empty name is always an error. apps.name is NOT NULL and every surface
// (dashboard card, detail heading, launchpad tile) renders it as the app's
// primary label, so there is no meaningful "cleared" state to fall back to -
// unlike the description, which renders as nothing when empty.
func NormalizeName(s string) (string, error) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return "", fmt.Errorf("name must not be empty")
	}
	if n > MaxNameRunes {
		return "", fmt.Errorf("name must be between 1 and %d characters (got %d)", MaxNameRunes, n)
	}
	return s, nil
}

// NormalizeProjectName trims s and validates it as a project display name.
// Unlike NormalizeName, "" is valid: a project always has a slug, and the
// name is optional metadata the UI falls back from to the slug, so callers
// cannot distinguish "clear the name" from "no name given" and do not need
// to.
func NormalizeProjectName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n > MaxNameRunes {
		return "", fmt.Errorf("project name must be %d characters or fewer (got %d)", MaxNameRunes, n)
	}
	return s, nil
}

// NormalizeDescription trims s and validates it as an app description. Unlike
// the name, "" is valid and means "no description"; callers use it to clear.
func NormalizeDescription(s string) (string, error) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n > MaxDescriptionRunes {
		return "", fmt.Errorf("description must be %d characters or fewer (got %d)", MaxDescriptionRunes, n)
	}
	return s, nil
}
