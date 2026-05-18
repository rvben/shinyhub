package schedulespec_test

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/schedulespec"
)

// validArgs returns arguments that pass all validation rules.
func validArgs() (name, cron, timezone string, cmd []string, timeout int, overlap, missed string) {
	return "my-schedule", "0 6 * * *", "", []string{"echo", "hi"}, 3600, "skip", "skip"
}

func TestValidate_ValidInput(t *testing.T) {
	name, cron, tz, cmd, timeout, overlap, missed := validArgs()
	if err := schedulespec.Validate(name, cron, tz, cmd, timeout, overlap, missed); err != nil {
		t.Errorf("expected valid input to pass, got: %v", err)
	}
}

func TestValidate_Timezone_ValidZone(t *testing.T) {
	name, cron, _, cmd, timeout, overlap, missed := validArgs()
	if err := schedulespec.Validate(name, cron, "Europe/Amsterdam", cmd, timeout, overlap, missed); err != nil {
		t.Errorf("expected valid timezone to pass, got: %v", err)
	}
}

func TestValidate_Timezone_EmptyAllowed(t *testing.T) {
	name, cron, _, cmd, timeout, overlap, missed := validArgs()
	if err := schedulespec.Validate(name, cron, "", cmd, timeout, overlap, missed); err != nil {
		t.Errorf("expected empty timezone (inherit) to pass, got: %v", err)
	}
}

func TestValidate_Timezone_InvalidZone(t *testing.T) {
	name, cron, _, cmd, timeout, overlap, missed := validArgs()
	err := schedulespec.Validate(name, cron, "Mars/Olympus", cmd, timeout, overlap, missed)
	if err == nil || !strings.Contains(err.Error(), "timezone") {
		t.Errorf("expected timezone error for Mars/Olympus, got: %v", err)
	}
}

func TestValidate_CronExpr_RejectsCRON_TZ_Prefix(t *testing.T) {
	name, _, _, cmd, timeout, overlap, missed := validArgs()
	err := schedulespec.Validate(name, "CRON_TZ=UTC 0 5 * * *", "", cmd, timeout, overlap, missed)
	if err == nil || !strings.Contains(err.Error(), "cron_expr") {
		t.Errorf("expected cron_expr error for CRON_TZ= prefix, got: %v", err)
	}
}

func TestValidate_CronExpr_RejectsTZ_Prefix(t *testing.T) {
	name, _, _, cmd, timeout, overlap, missed := validArgs()
	err := schedulespec.Validate(name, "TZ=UTC 0 5 * * *", "", cmd, timeout, overlap, missed)
	if err == nil || !strings.Contains(err.Error(), "cron_expr") {
		t.Errorf("expected cron_expr error for TZ= prefix, got: %v", err)
	}
}

func TestValidate_BadName_Spaces(t *testing.T) {
	_, cron, tz, cmd, timeout, overlap, missed := validArgs()
	err := schedulespec.Validate("has spaces", cron, tz, cmd, timeout, overlap, missed)
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("expected name error for name with spaces, got: %v", err)
	}
}

func TestValidate_BadName_TooLong(t *testing.T) {
	_, cron, tz, cmd, timeout, overlap, missed := validArgs()
	longName := strings.Repeat("a", 65)
	err := schedulespec.Validate(longName, cron, tz, cmd, timeout, overlap, missed)
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("expected name error for name too long, got: %v", err)
	}
}

func TestValidate_BadName_Empty(t *testing.T) {
	_, cron, tz, cmd, timeout, overlap, missed := validArgs()
	err := schedulespec.Validate("", cron, tz, cmd, timeout, overlap, missed)
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("expected name error for empty name, got: %v", err)
	}
}

func TestValidate_UnparsableCron(t *testing.T) {
	name, _, tz, cmd, timeout, overlap, missed := validArgs()
	err := schedulespec.Validate(name, "not-a-cron", tz, cmd, timeout, overlap, missed)
	if err == nil || !strings.Contains(err.Error(), "cron_expr") {
		t.Errorf("expected cron error, got: %v", err)
	}
}

func TestValidate_EmptyCmd(t *testing.T) {
	name, cron, tz, _, timeout, overlap, missed := validArgs()
	err := schedulespec.Validate(name, cron, tz, []string{}, timeout, overlap, missed)
	if err == nil || !strings.Contains(err.Error(), "command") {
		t.Errorf("expected command error for empty slice, got: %v", err)
	}
}

func TestValidate_WhitespaceOnlyFirstElement(t *testing.T) {
	name, cron, tz, _, timeout, overlap, missed := validArgs()
	err := schedulespec.Validate(name, cron, tz, []string{"   "}, timeout, overlap, missed)
	if err == nil || !strings.Contains(err.Error(), "command") {
		t.Errorf("expected command error for whitespace-only first element, got: %v", err)
	}
}

func TestValidate_TimeoutZero(t *testing.T) {
	name, cron, tz, cmd, _, overlap, missed := validArgs()
	err := schedulespec.Validate(name, cron, tz, cmd, 0, overlap, missed)
	if err == nil || !strings.Contains(err.Error(), "timeout_seconds") {
		t.Errorf("expected timeout error for 0, got: %v", err)
	}
}

func TestValidate_TimeoutNegative(t *testing.T) {
	name, cron, tz, cmd, _, overlap, missed := validArgs()
	err := schedulespec.Validate(name, cron, tz, cmd, -1, overlap, missed)
	if err == nil || !strings.Contains(err.Error(), "timeout_seconds") {
		t.Errorf("expected timeout error for -1, got: %v", err)
	}
}

func TestValidate_TimeoutTooLarge(t *testing.T) {
	name, cron, tz, cmd, _, overlap, missed := validArgs()
	err := schedulespec.Validate(name, cron, tz, cmd, 86401, overlap, missed)
	if err == nil || !strings.Contains(err.Error(), "timeout_seconds") {
		t.Errorf("expected timeout error for 86401, got: %v", err)
	}
}

func TestValidate_UnknownOverlap(t *testing.T) {
	name, cron, tz, cmd, timeout, _, missed := validArgs()
	err := schedulespec.Validate(name, cron, tz, cmd, timeout, "unknown", missed)
	if err == nil || !strings.Contains(err.Error(), "overlap_policy") {
		t.Errorf("expected overlap_policy error, got: %v", err)
	}
}

func TestValidate_UnknownMissed(t *testing.T) {
	name, cron, tz, cmd, timeout, overlap, _ := validArgs()
	err := schedulespec.Validate(name, cron, tz, cmd, timeout, overlap, "unknown")
	if err == nil || !strings.Contains(err.Error(), "missed_policy") {
		t.Errorf("expected missed_policy error, got: %v", err)
	}
}
