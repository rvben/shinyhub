package schedulespec_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/schedulespec"
)

// validArgs returns arguments that pass all validation rules.
func validArgs() (name, cron, timezone string, cmd []string, timeout int, overlap, missed string) {
	return "my-schedule", "0 6 * * *", "", []string{"echo", "hi"}, 3600, "skip", "skip"
}

func TestValidateActivation_DefaultAndRollContract(t *testing.T) {
	tests := []struct {
		name        string
		action      string
		minInterval time.Duration
		wantAction  string
		wantErr     string
	}{
		{name: "unset defaults to none", wantAction: "none"},
		{name: "explicit none", action: "none", wantAction: "none"},
		{name: "roll", action: "roll", wantAction: "roll"},
		{name: "roll with damper", action: "roll", minInterval: time.Hour, wantAction: "roll"},
		{name: "signal is not a v1 action", action: "signal", wantErr: "none|roll"},
		{name: "unknown action", action: "restart", wantErr: "none|roll"},
		{name: "none cannot carry roll interval", action: "none", minInterval: time.Minute, wantErr: "requires on_success=roll"},
		{name: "negative interval", action: "roll", minInterval: -time.Second, wantErr: "must not be negative"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := schedulespec.ValidateActivation(tc.action, tc.minInterval)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ValidateActivation() error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateActivation() error = %v", err)
			}
			if got != tc.wantAction {
				t.Fatalf("ValidateActivation() action = %q, want %q", got, tc.wantAction)
			}
		})
	}
}

func TestNormalizeDeployTrigger(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: "never"},
		{in: "never", want: "never"},
		{in: "first_deploy", want: "first_deploy"},
		{in: "bundle_change", want: "bundle_change"},
		{in: "sometimes", wantErr: true},
	}
	for _, tc := range tests {
		got, err := schedulespec.NormalizeDeployTrigger(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeDeployTrigger(%q) error = nil", tc.in)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("NormalizeDeployTrigger(%q) = %q, %v; want %q, nil", tc.in, got, err, tc.want)
		}
	}
}

func TestValidateActivationSeconds_RejectsOverflowBeforeConversion(t *testing.T) {
	if _, err := schedulespec.ValidateActivationSeconds("roll", schedulespec.MaxRollIntervalSeconds); err != nil {
		t.Fatalf("maximum safe interval rejected: %v", err)
	}
	if _, err := schedulespec.ValidateActivationSeconds("roll", schedulespec.MaxRollIntervalSeconds+1); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("overflow interval error = %v, want explicit upper bound", err)
	}
	if _, err := schedulespec.ValidateActivationSeconds("roll", -1); err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("negative interval error = %v", err)
	}
}

func TestValidateActivationPolicy(t *testing.T) {
	tests := []struct {
		name         string
		action       string
		fallback     string
		maxDeferAge  time.Duration
		wantAction   string
		wantFallback string
		wantErr      string
	}{
		{name: "roll defaults to defer", action: "roll", wantAction: "roll", wantFallback: "defer"},
		{name: "explicit restart fallback", action: "roll", fallback: "restart", wantAction: "roll", wantFallback: "restart"},
		{name: "bounded defer", action: "roll", maxDeferAge: 6 * time.Hour, wantAction: "roll", wantFallback: "defer"},
		{name: "unknown fallback", action: "roll", fallback: "force", wantErr: "defer|restart"},
		{name: "restart requires roll", action: "none", fallback: "restart", wantErr: "requires on_success=roll"},
		{name: "defer age requires roll", action: "none", maxDeferAge: time.Hour, wantErr: "requires on_success=roll"},
		{name: "negative defer age", action: "roll", maxDeferAge: -time.Second, wantErr: "must not be negative"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			action, fallback, err := schedulespec.ValidateActivationPolicy(tc.action, 0, tc.fallback, tc.maxDeferAge)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ValidateActivationPolicy() error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateActivationPolicy() error = %v", err)
			}
			if action != tc.wantAction || fallback != tc.wantFallback {
				t.Fatalf("ValidateActivationPolicy() = %q, %q; want %q, %q", action, fallback, tc.wantAction, tc.wantFallback)
			}
		})
	}
}

func TestValidateActivationPolicySeconds_RejectsDeferAgeOverflow(t *testing.T) {
	if _, _, err := schedulespec.ValidateActivationPolicySeconds("roll", 0, "defer", schedulespec.MaxRollIntervalSeconds); err != nil {
		t.Fatalf("maximum safe defer age rejected: %v", err)
	}
	if _, _, err := schedulespec.ValidateActivationPolicySeconds("roll", 0, "defer", schedulespec.MaxRollIntervalSeconds+1); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("overflow defer age error = %v, want explicit upper bound", err)
	}
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
