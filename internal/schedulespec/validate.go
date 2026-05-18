// Package schedulespec defines schedule validation rules shared between the
// HTTP API and the deploy-manifest application path. Single source of truth
// so manifest deploys and `POST /api/apps/:slug/schedules` enforce identical
// constraints.
package schedulespec

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// nameRE is the canonical schedule-name regex: alphanumerics, dashes,
// underscores; 1..64 chars.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Validate checks every field of a schedule. Mirrors the rules enforced by
// the HTTP API (POST /api/apps/{slug}/schedules) so a manifest-driven deploy
// and a direct API call cannot produce different on-disk states.
//
// timezone may be empty (meaning "inherit server default"). A non-empty value
// must be a valid IANA timezone name (e.g. "Europe/Amsterdam"). cronExpr must
// not contain a TZ= or CRON_TZ= prefix; use the separate timezone field instead.
func Validate(name, cronExpr, timezone string, cmd []string, timeoutSec int, overlap, missed string) error {
	if !nameRE.MatchString(name) {
		return errors.New("name: must match [A-Za-z0-9_-]{1,64}")
	}
	// Reject embedded timezone prefixes before passing to the parser. The
	// scheduler always prepends the resolved CRON_TZ= prefix itself; an
	// operator-supplied prefix would produce a double-prefix and fire in the
	// wrong zone.
	trimmed := strings.TrimSpace(cronExpr)
	if strings.HasPrefix(trimmed, "TZ=") || strings.HasPrefix(trimmed, "CRON_TZ=") {
		return errors.New("cron_expr: must not contain a TZ=/CRON_TZ= prefix; use the timezone field instead")
	}
	if _, err := cron.ParseStandard(cronExpr); err != nil {
		return fmt.Errorf("cron_expr: %w", err)
	}
	if timezone != "" {
		if _, err := time.LoadLocation(timezone); err != nil {
			return fmt.Errorf("timezone: unknown IANA zone %q", timezone)
		}
	}
	if len(cmd) == 0 || strings.TrimSpace(cmd[0]) == "" {
		return errors.New("command: must not be empty")
	}
	if timeoutSec < 1 || timeoutSec > 86400 {
		return errors.New("timeout_seconds: must be 1..86400")
	}
	switch overlap {
	case "skip", "queue", "concurrent":
	default:
		return errors.New("overlap_policy: must be skip|queue|concurrent")
	}
	switch missed {
	case "skip", "run_once":
	default:
		return errors.New("missed_policy: must be skip|run_once")
	}
	return nil
}
